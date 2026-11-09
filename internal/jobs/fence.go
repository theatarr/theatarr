package jobs

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"

	"github.com/theatarr/theatarr/internal/store"
)

// Fencing. A job runs at least once, and a run can outlive its lease: the process stalls, the lease expires, and
// another worker takes the job while the first handler is still going. The worker cancels a handler's context as
// soon as it sees the loss (a renewal that updates no row, or no successful renewal before the lease ran out), with
// ErrLeaseLost as the cause. Between two renewals that can take up to a third of the lease, so a handler calls Fence
// right before a step it cannot take back (a provider add, a link replaced): Fence extends the lease in the same
// statement that checks ownership, so the step starts with a whole lease ahead of it, or not at all.

// ErrLeaseLost is the cause of a handler context cancelled because the run lost its lease, and Fence's answer then.
var ErrLeaseLost = errors.New("jobs: the run lost its lease; another worker may own the job")

type fenceKey struct{}

// fence is one run's hold on its job, carried in the handler's context.
type fence struct {
	w      *Worker
	j      *Job
	cancel context.CancelCauseFunc
	lost   atomic.Bool
	// until is the lease end this run last wrote (unix ms on the queue's clock).
	until atomic.Int64
}

// lose marks the run as having lost its lease and cancels its handler.
func (f *fence) lose() {
	f.lost.Store(true)
	f.cancel(ErrLeaseLost)
}

// extend extends the lease if this run still owns the job and its lease has not run out. ok is false when it does
// not (no row matched, or the lease committed is already over). The clock is read inside the write transaction, on
// every try: a wait for the writer (or a stall) longer than the lease must not write a deadline already past, and a
// lease that ran out while this run waited is not revived, since another worker may have taken the job meanwhile.
func (f *fence) extend(ctx context.Context) (ok bool, err error) {
	var n, until int64
	err = f.w.q.st.WriteTx(ctx, func(tx *sql.Tx) error {
		now := f.w.q.now()
		until = store.Millis(now.Add(f.w.opts.Lease))
		res, err := tx.ExecContext(ctx, `
			UPDATE job SET lease_until = ?, updated_at = ?
			WHERE id = ? AND state = 'running' AND lease_owner = ? AND attempts = ? AND lease_until > ?`,
			until, store.Millis(now), f.j.ID, f.w.opts.Owner, f.j.Attempt, store.Millis(now))
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, nil
	}
	f.until.Store(until)
	// The lease committed must still be running now: a commit that took a whole lease proves nothing.
	return store.Millis(f.w.q.now()) < until, nil
}

// Fence reports whether the job running in ctx still holds its lease, and extends it to a whole lease. Call it
// right before a step that cannot be undone; a non-nil error means stop without taking the step. ErrLeaseLost (the
// handler's context is cancelled too) means another run may own the job; a database error means ownership could
// not be shown, which is treated the same way. Outside a job (a CLI or API call) it only checks ctx.
func Fence(ctx context.Context) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	f, _ := ctx.Value(fenceKey{}).(*fence)
	if f == nil {
		return nil
	}
	if f.lost.Load() {
		return ErrLeaseLost
	}
	ok, err := f.extend(ctx)
	if err != nil {
		return err
	}
	if !ok {
		f.lose()
		return ErrLeaseLost
	}
	return nil
}
