package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Urgent kinds go first, ahead of priority and age, and only they use the reserved slots.
func TestUrgentFirstAndReserved(t *testing.T) {
	q, c := setup(t)
	ctx := context.Background()
	art, _, _ := q.Enqueue(ctx, "meta.art", nil, EnqueueOptions{Priority: 10, RunAt: c.now().Add(-time.Hour)})
	search, _, _ := q.Enqueue(ctx, "acquire.search", nil, EnqueueOptions{})
	w := NewWorker(q, WorkerOptions{Owner: "a", Poll: 10 * time.Millisecond, Logger: quiet,
		Urgent: []string{"acquire.search"}, Reserved: 1})
	var order []int64
	h := func(ctx context.Context, j *Job) error { order = append(order, j.ID); return nil }
	w.Handle("meta.art", h)
	w.Handle("acquire.search", h)
	// A reserved slot leaves background work alone.
	if ran, err := w.RunOneUrgent(ctx); err != nil || !ran {
		t.Fatalf("urgent slot: %v %v", ran, err)
	}
	if ran, err := w.RunOneUrgent(ctx); err != nil || ran {
		t.Fatalf("urgent slot ran background work: %v %v", ran, err)
	}
	if ran, err := w.RunOne(ctx); err != nil || !ran {
		t.Fatalf("general slot: %v %v", ran, err)
	}
	if len(order) != 2 || order[0] != search || order[1] != art {
		t.Fatalf("order %v, want [%d %d]", order, search, art)
	}
	// A general slot also takes the urgent kind first.
	art2, _, _ := q.Enqueue(ctx, "meta.art", nil, EnqueueOptions{Priority: 10})
	search2, _, _ := q.Enqueue(ctx, "acquire.search", nil, EnqueueOptions{})
	order = nil
	w.RunOne(ctx)
	w.RunOne(ctx)
	if len(order) != 2 || order[0] != search2 || order[1] != art2 {
		t.Fatalf("order %v, want [%d %d]", order, search2, art2)
	}
}

// With every general slot busy on slow background work, an acquisition still runs in the reserved slot.
func TestReservedSlotRunsWhileBackgroundBusy(t *testing.T) {
	q, _ := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(2)
	w := NewWorker(q, WorkerOptions{Owner: "a", Concurrency: 2, Reserved: 1, Urgent: []string{"router.route"},
		Poll: 5 * time.Millisecond, Logger: quiet})
	w.Handle("meta.art", func(ctx context.Context, j *Job) error {
		started.Done()
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	routed := make(chan int64, 1)
	w.Handle("router.route", func(ctx context.Context, j *Job) error { routed <- j.ID; return nil })
	for range 5 {
		q.Enqueue(ctx, "meta.art", nil, EnqueueOptions{})
	}
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	started.Wait()
	id, _, _ := q.Enqueue(ctx, "router.route", nil, EnqueueOptions{})
	select {
	case got := <-routed:
		if got != id {
			t.Fatalf("routed %d, want %d", got, id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the route waited behind background work")
	}
	close(release)
	cancel()
	<-done
}

func TestBacklog(t *testing.T) {
	q, c := setup(t)
	ctx := context.Background()
	start := c.now()
	q.Enqueue(ctx, "meta.art", nil, EnqueueOptions{RunAt: start.Add(-30 * time.Minute)})
	q.Enqueue(ctx, "meta.art", nil, EnqueueOptions{RunAt: start.Add(-5 * time.Minute)})
	q.Enqueue(ctx, "meta.art", nil, EnqueueOptions{RunAt: start.Add(time.Hour)})
	q.Enqueue(ctx, "acquire.search", nil, EnqueueOptions{})
	fail, _, _ := q.Enqueue(ctx, "provider.sync", nil, EnqueueOptions{MaxAttempts: 1})
	w := worker(q, "a")
	w.Handle("provider.sync", func(ctx context.Context, j *Job) error { return errors.New("down") })
	if ran, err := w.RunOne(ctx); err != nil || !ran {
		t.Fatal(ran, err)
	}
	if in := mustGet(t, q, fail); in.State != StateFailed {
		t.Fatalf("provider.sync: %+v", in)
	}
	c.add(time.Minute)
	bl, err := q.Backlog(ctx, start.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]KindBacklog{}
	for _, k := range bl {
		got[k.Kind] = k
	}
	if a := got["meta.art"]; a.Ready != 2 || a.Later != 1 || a.OldestReady != 31*time.Minute || a.OldestReadyS != 31*60 {
		t.Errorf("meta.art: %+v", a)
	}
	if s := got["acquire.search"]; s.Ready != 1 || s.OldestReady != time.Minute {
		t.Errorf("acquire.search: %+v", s)
	}
	if p := got["provider.sync"]; p.Failed != 1 || p.Ready != 0 || len(bl) != 3 {
		t.Errorf("provider.sync: %+v (%d kinds)", p, len(bl))
	}
	// A failure before the window is not counted.
	if bl, _ := q.Backlog(ctx, start.Add(time.Hour)); len(bl) != 2 {
		t.Errorf("window: %+v", bl)
	}
}
