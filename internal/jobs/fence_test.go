package jobs

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// expire ends job id's lease now, as a stalled process would let it lapse.
func expire(t *testing.T, q *Queue, id int64) {
	t.Helper()
	if _, err := q.st.Writer().ExecContext(context.Background(), `UPDATE job SET lease_until = 0 WHERE id = ?`,
		id); err != nil {
		t.Fatal(err)
	}
}

// A second worker takes over an expired lease; the first run's next renewal finds no row and cancels its handler
// with ErrLeaseLost, and the first run records nothing over the second's outcome.
func TestLostLeaseCancelsHandler(t *testing.T) {
	q, _ := setup(t)
	q.SetClock(time.Now)
	ctx := context.Background()
	id, _, _ := q.Enqueue(ctx, "k", nil, EnqueueOptions{})
	a := NewWorker(q, WorkerOptions{Owner: "a", Lease: 90 * time.Millisecond, Logger: quiet})
	b := NewWorker(q, WorkerOptions{Owner: "b", Lease: time.Minute, Logger: quiet})
	started, taken := make(chan struct{}), make(chan struct{})
	var cause error
	a.Handle("k", func(ctx context.Context, j *Job) error {
		close(started)
		<-taken
		select {
		case <-ctx.Done():
			cause = context.Cause(ctx)
		case <-time.After(5 * time.Second):
			t.Error("the handler was not cancelled after its lease was lost")
		}
		return ctx.Err()
	})
	var bRuns int
	b.Handle("k", func(ctx context.Context, j *Job) error { bRuns = j.Attempt; return nil })
	done := make(chan struct{})
	go func() { a.RunOne(ctx); close(done) }()
	<-started
	expire(t, q, id)
	if ran, err := b.RunOne(ctx); !ran || err != nil {
		t.Fatal("the expired lease was not taken over", ran, err)
	}
	close(taken)
	<-done
	if !errors.Is(cause, ErrLeaseLost) {
		t.Errorf("cause %v, want ErrLeaseLost", cause)
	}
	if bRuns != 2 {
		t.Errorf("second run's attempt %d, want 2", bRuns)
	}
	if in := mustGet(t, q, id); in.State != StateDone || in.Attempts != 2 {
		t.Errorf("%+v, want done by the second run (attempts 2)", in)
	}
}

// Fence, between two renewals: a run whose lease was taken over learns it before its side effect, not after.
func TestFenceStopsATakenOverRun(t *testing.T) {
	q, c := setup(t)
	ctx := context.Background()
	id, _, _ := q.Enqueue(ctx, "k", nil, EnqueueOptions{})
	// The fake clock never moves on its own and the renewal ticks every 20s: only Fence can notice the takeover.
	a := worker(q, "a")
	b := worker(q, "b")
	started, taken := make(chan struct{}), make(chan struct{})
	var first, second error
	sideEffects := 0
	a.Handle("k", func(ctx context.Context, j *Job) error {
		first = Fence(ctx) // still ours: extends the lease
		close(started)
		<-taken
		if second = Fence(ctx); second != nil {
			return second
		}
		sideEffects++
		return nil
	})
	b.Handle("k", func(ctx context.Context, j *Job) error { sideEffects++; return nil })
	done := make(chan struct{})
	go func() { a.RunOne(ctx); close(done) }()
	<-started
	var until int64
	if err := q.st.Reader().QueryRow(`SELECT lease_until FROM job WHERE id = ?`, id).Scan(&until); err != nil {
		t.Fatal(err)
	}
	if want := store.Millis(c.now().Add(time.Minute)); until != want {
		t.Errorf("lease_until after Fence %d, want %d (a whole lease from now)", until, want)
	}
	c.add(2 * time.Minute)
	if ran, _ := b.RunOne(ctx); !ran {
		t.Fatal("the expired lease was not taken over")
	}
	close(taken)
	<-done
	if first != nil {
		t.Errorf("Fence on an owned job: %v", first)
	}
	if !errors.Is(second, ErrLeaseLost) {
		t.Errorf("Fence after the takeover: %v, want ErrLeaseLost", second)
	}
	if sideEffects != 1 {
		t.Errorf("%d side effects, want 1 (the second run's only)", sideEffects)
	}
	if in := mustGet(t, q, id); in.State != StateDone || in.Attempts != 2 {
		t.Errorf("%+v, want done by the second run", in)
	}
}

func TestFenceOutsideAJob(t *testing.T) {
	if err := Fence(context.Background()); err != nil {
		t.Errorf("no job: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Fence(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

// Fence waiting for the writer longer than a lease: the deadline is computed once it holds the writer, and the lease
// that ran out meanwhile is not revived (another worker could take the job at once), so Fence says lost.
func TestFenceAfterAWriterStallLongerThanTheLease(t *testing.T) {
	q, c := setup(t)
	ctx := context.Background()
	id, _, _ := q.Enqueue(ctx, "k", nil, EnqueueOptions{})
	a := worker(q, "a")
	held, release := make(chan struct{}), make(chan struct{})
	var fenceErr error
	sideEffects := 0
	a.Handle("k", func(ctx context.Context, j *Job) error {
		// Another writer holds the database while the clock passes the lease.
		go func() {
			_ = q.st.WriteTx(context.Background(), func(*sql.Tx) error {
				close(held)
				<-release
				return nil
			})
		}()
		<-held
		go func() {
			time.Sleep(100 * time.Millisecond) // Fence is waiting for the writer by now
			c.add(2 * time.Minute)
			close(release)
		}()
		if fenceErr = Fence(ctx); fenceErr != nil {
			return fenceErr
		}
		sideEffects++
		return nil
	})
	if ran, err := a.RunOne(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	if !errors.Is(fenceErr, ErrLeaseLost) || sideEffects != 0 {
		t.Fatalf("Fence after the lease ran out: %v, side effects %d; want ErrLeaseLost and none", fenceErr,
			sideEffects)
	}
	var until int64
	if err := q.st.Reader().QueryRow(`SELECT lease_until FROM job WHERE id = ?`, id).Scan(&until); err != nil {
		t.Fatal(err)
	}
	if until > store.Millis(c.now()) {
		t.Errorf("the expired lease was extended to %d (now %d)", until, store.Millis(c.now()))
	}
}
