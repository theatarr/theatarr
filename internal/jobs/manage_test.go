package jobs

import (
	"context"
	"errors"
	"testing"
)

// failJob enqueues a job of kind with payload and fails it permanently.
func failJob(t *testing.T, q *Queue, kind string, payload any, opts EnqueueOptions) int64 {
	t.Helper()
	return runJob(t, q, kind, payload, opts, Permanent(errors.New("AUTH_BLOCKED")))
}

func runJob(t *testing.T, q *Queue, kind string, payload any, opts EnqueueOptions, result error) int64 {
	t.Helper()
	ctx := context.Background()
	id, created, err := q.Enqueue(ctx, kind, payload, opts)
	if err != nil || !created {
		t.Fatalf("enqueue: %v %v", created, err)
	}
	w := worker(q, "t")
	w.Handle(kind, func(context.Context, *Job) error { return result })
	if ran, err := w.RunOne(ctx); !ran || err != nil {
		t.Fatalf("run: %v %v", ran, err)
	}
	return id
}

func mustStatus(t *testing.T, q *Queue, id int64) Status {
	t.Helper()
	s, err := q.Status(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func attention(t *testing.T, q *Queue) int {
	t.Helper()
	n, err := q.AttentionCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

type sub struct {
	Title int `json:"title"`
}

func TestAutoResolve(t *testing.T) {
	q, _ := setup(t)
	ctx := context.Background()

	syncID := failJob(t, q, "provider.sync", nil, EnqueueOptions{})
	art7 := failJob(t, q, "meta.art", sub{7}, EnqueueOptions{})
	art8 := failJob(t, q, "meta.art", sub{8}, EnqueueOptions{})
	if s := mustStatus(t, q, syncID); !s.Attention || s.ResolvedBy != 0 || s.LastError != "AUTH_BLOCKED" {
		t.Fatalf("failed sync: %+v", s)
	}
	if n := attention(t, q); n != 3 {
		t.Fatalf("attention %d, want 3", n)
	}

	// A later failure of the same kind resolves nothing; another kind's success neither.
	failJob(t, q, "provider.sync", nil, EnqueueOptions{})
	runJob(t, q, "db.nightly", nil, EnqueueOptions{}, nil)
	if s := mustStatus(t, q, syncID); !s.Attention {
		t.Fatalf("sync resolved too early: %+v", s)
	}

	// A done sync after it resolves it (and the second failure too).
	done := runJob(t, q, "provider.sync", nil, EnqueueOptions{}, nil)
	if s := mustStatus(t, q, syncID); s.Attention || s.ResolvedBy != done || s.State != StateFailed {
		t.Fatalf("sync after a done sync: %+v", s)
	}

	// Subjects: title 7 done resolves the title 7 failure only; the batch {} resolves the rest.
	d7 := runJob(t, q, "meta.art", sub{7}, EnqueueOptions{}, nil)
	if s := mustStatus(t, q, art7); s.ResolvedBy != d7 {
		t.Errorf("art 7: %+v", s)
	}
	if s := mustStatus(t, q, art8); !s.Attention {
		t.Errorf("art 8 resolved by title 7: %+v", s)
	}
	if n := attention(t, q); n != 1 {
		t.Errorf("attention %d, want 1", n)
	}
	batch := runJob(t, q, "meta.art", nil, EnqueueOptions{}, nil)
	if s := mustStatus(t, q, art8); s.ResolvedBy != batch {
		t.Errorf("art 8 after the batch: %+v", s)
	}
	if n := attention(t, q); n != 0 {
		t.Errorf("attention %d, want 0", n)
	}

	// An earlier done job does not resolve a later failure.
	late := failJob(t, q, "db.nightly", nil, EnqueueOptions{})
	if s := mustStatus(t, q, late); !s.Attention {
		t.Errorf("failure after a done run: %+v", s)
	}

	// The list still has every failed job, and the attention filter only the open one.
	all, err := q.List(ctx, ListFilter{State: StateFailed})
	if err != nil || len(all) != 5 {
		t.Fatalf("failed list: %d %v", len(all), err)
	}
	open, err := q.List(ctx, ListFilter{Attention: true})
	if err != nil || len(open) != 1 || open[0].ID != late {
		t.Fatalf("attention list: %+v %v", open, err)
	}
}

func TestRetry(t *testing.T) {
	q, _ := setup(t)
	ctx := context.Background()
	old := failJob(t, q, "provider.sync", sub{1}, EnqueueOptions{Priority: 3, MaxAttempts: 2, DedupeKey: "provider.sync"})

	id, created, err := q.Retry(ctx, old)
	if err != nil || !created || id == old {
		t.Fatalf("retry: %d %v %v", id, created, err)
	}
	s := mustStatus(t, q, id)
	if s.Kind != "provider.sync" || string(s.Payload) != `{"title":1}` || s.Priority != 3 || s.MaxAttempts != 2 ||
		s.DedupeKey != "provider.sync" || s.State != StateQueued || s.RetryOf != old || s.Attempts != 0 {
		t.Fatalf("retry job: %+v", s)
	}
	o := mustStatus(t, q, old)
	if o.RetriedAs != id || o.Attention || o.State != StateFailed || o.LastError == "" {
		t.Fatalf("old job after retry: %+v", o)
	}

	// Retrying again while the retry is queued returns it.
	again, created, err := q.Retry(ctx, old)
	if err != nil || created || again != id {
		t.Fatalf("second retry: %d %v %v", again, created, err)
	}

	// The retry fails: it needs attention now, not the old one.
	w := worker(q, "t")
	w.Handle("provider.sync", func(context.Context, *Job) error { return Permanent(errors.New("still blocked")) })
	if _, err := w.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	open, _ := q.List(ctx, ListFilter{Attention: true})
	if len(open) != 1 || open[0].ID != id {
		t.Fatalf("attention after the retry failed: %+v", open)
	}

	// Retry the retry and let it pass: both resolve.
	third, _, err := q.Retry(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	w2 := worker(q, "t2")
	w2.Handle("provider.sync", func(context.Context, *Job) error { return nil })
	if _, err := w2.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	if s := mustStatus(t, q, old); s.ResolvedBy != third {
		t.Errorf("old: %+v", s)
	}
	if n := attention(t, q); n != 0 {
		t.Errorf("attention %d", n)
	}

	// Only failed jobs can be retried; unknown ids are not found.
	if _, _, err := q.Retry(ctx, third); !errors.Is(err, ErrNotFailed) {
		t.Errorf("retry a done job: %v", err)
	}
	if _, _, err := q.Retry(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("retry a missing job: %v", err)
	}
}

func TestRetryLinksTheDedupedJob(t *testing.T) {
	q, _ := setup(t)
	ctx := context.Background()
	old := failJob(t, q, "provider.sync", nil, EnqueueOptions{DedupeKey: "provider.sync"})
	queued, _, err := q.Enqueue(ctx, "provider.sync", nil, EnqueueOptions{DedupeKey: "provider.sync"})
	if err != nil {
		t.Fatal(err)
	}
	id, created, err := q.Retry(ctx, old)
	if err != nil || created || id != queued {
		t.Fatalf("retry with a queued twin: %d %v %v", id, created, err)
	}
	if s := mustStatus(t, q, old); s.RetriedAs != queued || s.Attention {
		t.Errorf("old: %+v", s)
	}
}

func TestDismiss(t *testing.T) {
	q, c := setup(t)
	ctx := context.Background()
	a := failJob(t, q, "provider.sync", sub{1}, EnqueueOptions{})
	b := failJob(t, q, "provider.sync", sub{2}, EnqueueOptions{})
	done := runJob(t, q, "db.nightly", nil, EnqueueOptions{}, nil)

	s, err := q.Dismiss(ctx, a, "admin")
	if err != nil || s.DismissedAt == nil || !s.DismissedAt.Equal(c.now()) || s.DismissedBy != "admin" || s.Attention ||
		s.State != StateFailed {
		t.Fatalf("dismiss: %+v %v", s, err)
	}
	c.add(1)
	s2, err := q.Dismiss(ctx, a, "someone else")
	if err != nil || !s2.DismissedAt.Equal(*s.DismissedAt) || s2.DismissedBy != "admin" {
		t.Errorf("dismiss twice: %+v %v", s2, err)
	}
	if n := attention(t, q); n != 1 {
		t.Errorf("attention %d, want 1", n)
	}
	if _, err := q.Dismiss(ctx, done, "admin"); !errors.Is(err, ErrNotFailed) {
		t.Errorf("dismiss a done job: %v", err)
	}
	if _, err := q.Dismiss(ctx, 999, "admin"); !errors.Is(err, ErrNotFound) {
		t.Errorf("dismiss a missing job: %v", err)
	}
	vis, _ := q.List(ctx, ListFilter{State: StateFailed, HideDismissed: true})
	if len(vis) != 1 || vis[0].ID != b {
		t.Errorf("hide dismissed: %+v", vis)
	}
	// History stays.
	if all, _ := q.List(ctx, ListFilter{}); len(all) != 3 {
		t.Errorf("history: %d jobs", len(all))
	}
}

func TestDismissResolved(t *testing.T) {
	q, _ := setup(t)
	ctx := context.Background()
	r1 := failJob(t, q, "provider.sync", nil, EnqueueOptions{})
	open := failJob(t, q, "meta.art", sub{4}, EnqueueOptions{})
	runJob(t, q, "provider.sync", nil, EnqueueOptions{}, nil)
	r2 := failJob(t, q, "meta.art", sub{5}, EnqueueOptions{})
	runJob(t, q, "meta.art", sub{5}, EnqueueOptions{}, nil)

	n, err := q.DismissResolved(ctx, "key:ops")
	if err != nil || n != 2 {
		t.Fatalf("dismiss resolved: %d %v", n, err)
	}
	for _, id := range []int64{r1, r2} {
		if s := mustStatus(t, q, id); s.DismissedAt == nil || s.DismissedBy != "key:ops" {
			t.Errorf("job %d: %+v", id, s)
		}
	}
	if s := mustStatus(t, q, open); s.DismissedAt != nil || !s.Attention {
		t.Errorf("open job dismissed: %+v", s)
	}
	if n, _ := q.DismissResolved(ctx, "x"); n != 0 {
		t.Errorf("second pass dismissed %d", n)
	}
}

func TestManageNeedsMigration(t *testing.T) {
	q, _ := setup(t)
	ctx := context.Background()
	if _, err := q.st.Writer().ExecContext(ctx, `ALTER TABLE job DROP COLUMN dismissed_by`); err != nil {
		t.Fatal(err)
	}
	if _, err := q.List(ctx, ListFilter{}); !errors.Is(err, ErrNotMigrated) {
		t.Errorf("list: %v", err)
	}
	if _, _, err := q.Retry(ctx, 1); !errors.Is(err, ErrNotMigrated) {
		t.Errorf("retry: %v", err)
	}
}
