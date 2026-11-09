package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/errinbox"
)

// TestErrorInboxJobs: a handler panic is recorded with its stack and the worker goes on to the next job; a job that
// fails for good is recorded by kind; a failure that will be retried is not, and neither is the panic's failure.
func TestErrorInboxJobs(t *testing.T) {
	q, _ := setup(t)
	ctx := context.Background()
	in := errinbox.New(q.st, errinbox.Options{Logger: quiet})
	errinbox.Install(in)
	t.Cleanup(func() { errinbox.Install(nil) })
	q.Enqueue(ctx, "boom", nil, EnqueueOptions{MaxAttempts: 1})
	q.Enqueue(ctx, "perm", nil, EnqueueOptions{})
	q.Enqueue(ctx, "flaky", nil, EnqueueOptions{MaxAttempts: 3})
	ok, _, _ := q.Enqueue(ctx, "ok", nil, EnqueueOptions{})
	w := worker(q, "a")
	w.Handle("boom", func(ctx context.Context, j *Job) error { var s []int; _ = s[j.Attempt]; return nil })
	w.Handle("perm", func(ctx context.Context, j *Job) error { return Permanent(errors.New("bad payload for title 42")) })
	w.Handle("flaky", func(ctx context.Context, j *Job) error { return errors.New("try again") })
	w.Handle("ok", func(ctx context.Context, j *Job) error { return nil })
	for range 4 {
		if _, err := w.RunOne(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if s := mustGet(t, q, ok); s.State != StateDone {
		t.Errorf("the job after the panic: %+v", s)
	}
	if err := in.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := errinbox.List(ctx, q.st, errinbox.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]errinbox.Row{}
	for _, r := range p.Items {
		got[r.Source+" "+r.Component] = r
	}
	if len(got) != 2 {
		t.Fatalf("rows %+v", got)
	}
	pr, found := got["panic jobs"]
	if !found {
		t.Fatalf("no panic row: %+v", got)
	}
	full, _ := errinbox.Get(ctx, q.st, pr.ID)
	if !strings.Contains(full.Stack, "TestErrorInboxJobs") || !strings.Contains(string(full.Context), `"kind":"boom"`) {
		t.Errorf("panic row = %+v", full)
	}
	if jr, found := got["job perm"]; !found || jr.Message != "bad payload for title <n>" {
		t.Errorf("job row = %+v", jr)
	}
}
