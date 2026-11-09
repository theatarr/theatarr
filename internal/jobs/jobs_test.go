package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func setup(t *testing.T) (*Queue, *clock) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	q := NewQueue(st)
	c := &clock{t: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	q.SetClock(c.now)
	return q, c
}

func worker(q *Queue, owner string) *Worker {
	return NewWorker(q, WorkerOptions{Owner: owner, Lease: time.Minute, Poll: 10 * time.Millisecond,
		BackoffBase: 10 * time.Second, BackoffMax: time.Minute, Logger: quiet})
}

func mustGet(t *testing.T, q *Queue, id int64) Info {
	t.Helper()
	in, err := q.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestEnqueueAndRun(t *testing.T) {
	q, _ := setup(t)
	ctx := context.Background()
	type payload struct{ TitleID int }
	id, created, err := q.Enqueue(ctx, "refresh", payload{42}, EnqueueOptions{})
	if err != nil || !created {
		t.Fatalf("enqueue: %v %v", created, err)
	}
	w := worker(q, "a")
	var got payload
	w.Handle("refresh", func(ctx context.Context, j *Job) error {
		if j.Attempt != 1 || j.ID != id {
			t.Errorf("job %+v", j)
		}
		return j.Decode(&got)
	})
	ran, err := w.RunOne(ctx)
	if err != nil || !ran {
		t.Fatalf("RunOne: %v %v", ran, err)
	}
	if got.TitleID != 42 {
		t.Errorf("payload %+v", got)
	}
	if in := mustGet(t, q, id); in.State != StateDone || in.Attempts != 1 {
		t.Errorf("after run: %+v", in)
	}
	if ran, _ := w.RunOne(ctx); ran {
		t.Error("ran a second time")
	}
}

func TestDedupe(t *testing.T) {
	q, _ := setup(t)
	ctx := context.Background()
	opts := EnqueueOptions{DedupeKey: "search:title:7"}
	a, created, err := q.Enqueue(ctx, "search", nil, opts)
	if err != nil || !created {
		t.Fatal(created, err)
	}
	b, created, err := q.Enqueue(ctx, "search", nil, opts)
	if err != nil || created || b != a {
		t.Fatalf("second enqueue: id %d created %v err %v", b, created, err)
	}
	// Still deduped while running.
	w := worker(q, "a")
	w.Handle("search", func(ctx context.Context, j *Job) error {
		c, created, err := q.Enqueue(ctx, "search", nil, opts)
		if err != nil || created || c != a {
			t.Errorf("enqueue while running: id %d created %v err %v", c, created, err)
		}
		return nil
	})
	if _, err := w.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	// Once done, the key is free again.
	c, created, err := q.Enqueue(ctx, "search", nil, opts)
	if err != nil || !created || c == a {
		t.Fatalf("after done: id %d created %v err %v", c, created, err)
	}
	// No key: never deduped.
	x, _, _ := q.Enqueue(ctx, "search", nil, EnqueueOptions{})
	y, _, _ := q.Enqueue(ctx, "search", nil, EnqueueOptions{})
	if x == y {
		t.Error("jobs without a key were deduped")
	}
}

func TestRunAtAndPriority(t *testing.T) {
	q, c := setup(t)
	ctx := context.Background()
	later, _, _ := q.Enqueue(ctx, "k", nil, EnqueueOptions{RunAt: c.now().Add(time.Hour)})
	low, _, _ := q.Enqueue(ctx, "k", nil, EnqueueOptions{})
	high, _, _ := q.Enqueue(ctx, "k", nil, EnqueueOptions{Priority: 10})
	var order []int64
	w := worker(q, "a")
	w.Handle("k", func(ctx context.Context, j *Job) error { order = append(order, j.ID); return nil })
	for {
		ran, err := w.RunOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			break
		}
	}
	if len(order) != 2 || order[0] != high || order[1] != low {
		t.Fatalf("order %v, want [%d %d]", order, high, low)
	}
	c.add(time.Hour)
	if ran, _ := w.RunOne(ctx); !ran || order[2] != later {
		t.Errorf("future job did not run once due: %v", order)
	}
}

func TestRetryBackoffAndMaxAttempts(t *testing.T) {
	q, c := setup(t)
	ctx := context.Background()
	id, _, _ := q.Enqueue(ctx, "flaky", nil, EnqueueOptions{MaxAttempts: 3})
	w := worker(q, "a")
	w.Handle("flaky", func(ctx context.Context, j *Job) error { return errors.New("provider said no") })

	start := c.now()
	if ran, _ := w.RunOne(ctx); !ran {
		t.Fatal("did not run")
	}
	in := mustGet(t, q, id)
	if in.State != StateQueued || in.Attempts != 1 || in.LastError != "provider said no" {
		t.Fatalf("after attempt 1: %+v", in)
	}
	if want := start.Add(10 * time.Second); !in.RunAt.Equal(want) {
		t.Errorf("run_at %v, want %v (base back-off)", in.RunAt, want)
	}
	if ran, _ := w.RunOne(ctx); ran {
		t.Fatal("ran before its back-off elapsed")
	}
	c.add(10 * time.Second)
	w.RunOne(ctx)
	in = mustGet(t, q, id)
	if want := c.now().Add(20 * time.Second); in.Attempts != 2 || !in.RunAt.Equal(want) {
		t.Errorf("after attempt 2: %+v, want run_at %v (doubled)", in, want)
	}
	c.add(20 * time.Second)
	w.RunOne(ctx)
	if in = mustGet(t, q, id); in.State != StateFailed || in.Attempts != 3 {
		t.Errorf("after attempt 3: %+v, want failed", in)
	}

	if got := w.Backoff(10); got != time.Minute {
		t.Errorf("Backoff(10) = %v, want the 1m cap", got)
	}
}

func TestPermanentAndPanic(t *testing.T) {
	q, _ := setup(t)
	ctx := context.Background()
	p, _, _ := q.Enqueue(ctx, "perm", nil, EnqueueOptions{})
	x, _, _ := q.Enqueue(ctx, "boom", nil, EnqueueOptions{})
	w := worker(q, "a")
	w.Handle("perm", func(ctx context.Context, j *Job) error { return Permanent(errors.New("bad payload")) })
	w.Handle("boom", func(ctx context.Context, j *Job) error { panic("oops") })
	w.RunOne(ctx)
	w.RunOne(ctx)
	if in := mustGet(t, q, p); in.State != StateFailed || in.Attempts != 1 {
		t.Errorf("permanent: %+v", in)
	}
	if in := mustGet(t, q, x); in.State != StateQueued || in.LastError != "handler panic: oops" {
		t.Errorf("panic: %+v", in)
	}
}

// A worker leases a job and dies. After the lease expires, another worker takes it, and the dead worker's late
// completion is ignored.
func TestExpiredLeaseIsReleased(t *testing.T) {
	q, c := setup(t)
	ctx := context.Background()
	id, _, _ := q.Enqueue(ctx, "k", nil, EnqueueOptions{})
	dead := worker(q, "dead")
	dead.Handle("k", func(context.Context, *Job) error { return nil })
	j, err := dead.lease(ctx, false) // leased, never run
	if err != nil || j == nil {
		t.Fatal(j, err)
	}

	alive := worker(q, "alive")
	var runs []int
	alive.Handle("k", func(ctx context.Context, j *Job) error { runs = append(runs, j.Attempt); return nil })
	if ran, _ := alive.RunOne(ctx); ran {
		t.Fatal("leased job taken before its lease expired")
	}
	c.add(time.Minute)
	if ran, _ := alive.RunOne(ctx); !ran {
		t.Fatal("expired lease was not taken")
	}
	if len(runs) != 1 || runs[0] != 2 {
		t.Errorf("runs %v, want one run as attempt 2", runs)
	}
	if ok, err := dead.finish(ctx, j, nil); err != nil || ok {
		t.Errorf("late finish by the dead worker: ok %v err %v, want ignored", ok, err)
	}
	if in := mustGet(t, q, id); in.State != StateDone || in.Attempts != 2 {
		t.Errorf("%+v", in)
	}
}

func TestExpiredLeaseOnLastAttemptFails(t *testing.T) {
	q, c := setup(t)
	ctx := context.Background()
	id, _, _ := q.Enqueue(ctx, "k", nil, EnqueueOptions{MaxAttempts: 1})
	w := worker(q, "a")
	w.Handle("k", func(context.Context, *Job) error { return nil })
	if j, _ := w.lease(ctx, false); j == nil {
		t.Fatal("no lease")
	}
	c.add(2 * time.Minute)
	if ran, _ := w.RunOne(ctx); ran {
		t.Error("job ran past its max attempts")
	}
	if in := mustGet(t, q, id); in.State != StateFailed {
		t.Errorf("%+v, want failed", in)
	}
}

func TestUnregisteredKindsWait(t *testing.T) {
	q, _ := setup(t)
	ctx := context.Background()
	id, _, _ := q.Enqueue(ctx, "other", nil, EnqueueOptions{})
	w := worker(q, "a")
	w.Handle("mine", func(context.Context, *Job) error { return nil })
	if ran, _ := w.RunOne(ctx); ran {
		t.Error("leased a kind with no handler")
	}
	if in := mustGet(t, q, id); in.State != StateQueued {
		t.Errorf("%+v", in)
	}
}

// Run with several goroutines processes every job exactly once, and stops cleanly.
func TestRunLoop(t *testing.T) {
	q, _ := setup(t)
	q.SetClock(time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const n = 100
	var count atomic.Int64
	seen := sync.Map{}
	w := NewWorker(q, WorkerOptions{Concurrency: 4, Poll: 5 * time.Millisecond, Logger: quiet})
	w.Handle("k", func(ctx context.Context, j *Job) error {
		if _, dup := seen.LoadOrStore(j.ID, true); dup {
			t.Errorf("job %d ran twice", j.ID)
		}
		count.Add(1)
		return nil
	})
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	for i := range n {
		if _, _, err := q.Enqueue(ctx, "k", map[string]int{"i": i}, EnqueueOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for count.Load() < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if count.Load() != n {
		t.Fatalf("%d of %d jobs ran", count.Load(), n)
	}
	counts, err := q.Counts(context.Background())
	if err != nil || counts[StateDone] != n {
		t.Errorf("counts %v %v", counts, err)
	}
}

// A shutdown mid-run hands the job back, ready and without spending an attempt.
func TestShutdownRequeues(t *testing.T) {
	q, _ := setup(t)
	q.SetClock(time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	id, _, _ := q.Enqueue(ctx, "slow", nil, EnqueueOptions{})
	started := make(chan struct{})
	w := NewWorker(q, WorkerOptions{Concurrency: 1, Poll: 5 * time.Millisecond, Logger: quiet})
	w.Handle("slow", func(ctx context.Context, j *Job) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	<-started
	cancel()
	<-done
	if in := mustGet(t, q, id); in.State != StateQueued || in.Attempts != 0 {
		t.Errorf("%+v, want queued with 0 attempts", in)
	}
}

func TestLeaseRenewal(t *testing.T) {
	q, _ := setup(t)
	q.SetClock(time.Now)
	ctx := context.Background()
	id, _, _ := q.Enqueue(ctx, "k", nil, EnqueueOptions{})
	w := NewWorker(q, WorkerOptions{Lease: 90 * time.Millisecond, Logger: quiet})
	other := NewWorker(q, WorkerOptions{Owner: "other", Lease: 90 * time.Millisecond, Logger: quiet})
	other.Handle("k", func(context.Context, *Job) error { t.Error("other worker stole a renewed lease"); return nil })
	w.Handle("k", func(ctx context.Context, j *Job) error {
		// Run well past the original lease; renewal keeps it ours.
		for range 6 {
			time.Sleep(50 * time.Millisecond)
			other.RunOne(ctx)
		}
		return nil
	})
	if ran, err := w.RunOne(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	if in := mustGet(t, q, id); in.State != StateDone || in.Attempts != 1 {
		t.Errorf("%+v", in)
	}
}

func TestPrune(t *testing.T) {
	q, c := setup(t)
	ctx := context.Background()
	q.Enqueue(ctx, "k", nil, EnqueueOptions{})
	keep, _, _ := q.Enqueue(ctx, "k", nil, EnqueueOptions{RunAt: c.now().Add(time.Hour)})
	w := worker(q, "a")
	w.Handle("k", func(context.Context, *Job) error { return nil })
	w.RunOne(ctx)
	c.add(48 * time.Hour)
	n, err := q.Prune(ctx, c.now().Add(-24*time.Hour))
	if err != nil || n != 1 {
		t.Errorf("pruned %d, err %v", n, err)
	}
	if _, err := q.Get(ctx, keep); err != nil {
		t.Errorf("queued job pruned: %v", err)
	}
}

func TestEnqueueRejectsBadPayload(t *testing.T) {
	q, _ := setup(t)
	if _, _, err := q.Enqueue(context.Background(), "k", []byte("{not json"), EnqueueOptions{}); err == nil {
		t.Error("invalid JSON accepted")
	}
	if _, _, err := q.Enqueue(context.Background(), "", nil, EnqueueOptions{}); err == nil {
		t.Error("empty kind accepted")
	}
}
