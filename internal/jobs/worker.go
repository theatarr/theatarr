package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/store"
)

// Handler runs one job. A nil return completes it; an error fails it (retried unless Permanent). The context is
// cancelled when the worker stops; a handler interrupted that way is retried later, as with an expired lease. It is
// cancelled too when the run loses its lease (cause ErrLeaseLost, fence.go); call Fence before an irreversible step.
type Handler func(ctx context.Context, j *Job) error

// WorkerOptions tune a Worker. The zero value is fine.
type WorkerOptions struct {
	// Owner names this worker in lease_owner. Default host:pid.
	Owner string
	// Concurrency is how many jobs of any kind run at once. Default 2.
	Concurrency int
	// Urgent are the kinds that must not wait behind background work (acquisition and import). Among ready jobs they
	// are leased first, before priority and run_at order, and they alone may use the Reserved slots.
	Urgent []string
	// Reserved is how many extra slots run Urgent kinds only, on top of Concurrency, so a backlog of background jobs
	// (metadata and art refreshes, syncs) can never hold every slot while an acquisition waits. Default 0.
	Reserved int
	// Lease is how long a lease lasts before another worker may take the job. It is extended while the handler
	// runs. Default 2m.
	Lease time.Duration
	// Poll is how often an idle worker looks for ready jobs. Default 1s.
	Poll time.Duration
	// BackoffBase and BackoffMax shape retries: base × 2^(attempt-1), capped at max. Defaults 10s and 1h.
	BackoffBase time.Duration
	BackoffMax  time.Duration
	Logger      *slog.Logger
	// Registerer, if set, receives the worker's metrics.
	Registerer prometheus.Registerer
}

// Worker leases and runs jobs.
type Worker struct {
	q        *Queue
	opts     WorkerOptions
	log      *slog.Logger
	mu       sync.RWMutex
	handlers map[string]Handler

	runs     *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewWorker returns a worker on q. Register handlers with Handle before Run.
func NewWorker(q *Queue, opts WorkerOptions) *Worker {
	if opts.Owner == "" {
		host, _ := os.Hostname()
		opts.Owner = host + ":" + strconv.Itoa(os.Getpid())
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 2
	}
	if opts.Lease <= 0 {
		opts.Lease = 2 * time.Minute
	}
	if opts.Poll <= 0 {
		opts.Poll = time.Second
	}
	if opts.BackoffBase <= 0 {
		opts.BackoffBase = 10 * time.Second
	}
	if opts.BackoffMax <= 0 {
		opts.BackoffMax = time.Hour
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	w := &Worker{q: q, opts: opts, log: log.With("component", "jobs"), handlers: map[string]Handler{}}
	w.runs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "theatarr_jobs_runs_total", Help: "Job runs by kind and outcome (done, retry, failed, lost).",
	}, []string{"kind", "outcome"})
	w.duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "theatarr_jobs_run_seconds", Help: "Job handler run time.", Buckets: prometheus.ExponentialBuckets(0.01, 4, 8),
	}, []string{"kind"})
	if opts.Registerer != nil {
		opts.Registerer.MustRegister(w.runs, w.duration)
	}
	return w
}

// Handle registers the handler for kind. Only registered kinds are leased.
func (w *Worker) Handle(kind string, h Handler) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, dup := w.handlers[kind]; dup {
		panic("jobs: duplicate handler for " + kind)
	}
	w.handlers[kind] = h
}

func (w *Worker) kinds() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	ks := make([]string, 0, len(w.handlers))
	for k := range w.handlers {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

// Backoff returns the wait before the retry that follows the given attempt (1-based).
func (w *Worker) Backoff(attempt int) time.Duration {
	d := w.opts.BackoffBase
	for i := 1; i < attempt && d < w.opts.BackoffMax; i++ {
		d *= 2
	}
	return min(d, w.opts.BackoffMax)
}

// Run processes jobs until ctx is cancelled, then waits for running handlers to return.
func (w *Worker) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for i := range w.opts.Concurrency + w.reserved() {
		urgentOnly := i >= w.opts.Concurrency
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.loop(ctx, urgentOnly)
		}()
	}
	wg.Wait()
	return nil
}

// reserved is the number of urgent-only slots: none without urgent kinds.
func (w *Worker) reserved() int {
	if len(w.opts.Urgent) == 0 || w.opts.Reserved < 0 {
		return 0
	}
	return w.opts.Reserved
}

func (w *Worker) loop(ctx context.Context, urgentOnly bool) {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-w.q.notify:
		}
		for ctx.Err() == nil {
			ran, err := w.runNext(ctx, urgentOnly)
			if err != nil && ctx.Err() == nil {
				w.log.Error("job loop", "err", err)
				break
			}
			if !ran {
				break
			}
		}
		t.Reset(w.opts.Poll)
	}
}

// RunOne leases one ready job and runs it. It reports whether a job ran.
func (w *Worker) RunOne(ctx context.Context) (bool, error) { return w.runNext(ctx, false) }

// RunOneUrgent is RunOne as a reserved slot runs it: urgent kinds only.
func (w *Worker) RunOneUrgent(ctx context.Context) (bool, error) { return w.runNext(ctx, true) }

func (w *Worker) runNext(ctx context.Context, urgentOnly bool) (bool, error) {
	j, err := w.lease(ctx, urgentOnly)
	if err != nil || j == nil {
		return false, err
	}
	w.run(ctx, j)
	return true, nil
}

// lease takes the next ready job of a registered kind (an urgent one with urgentOnly), or returns nil: urgent kinds
// first, then by priority, run_at and id. Jobs whose lease expired after their last attempt are failed on the way.
func (w *Worker) lease(ctx context.Context, urgentOnly bool) (*Job, error) {
	kinds := w.kinds()
	if urgentOnly {
		kinds = slices.DeleteFunc(kinds, func(k string) bool { return !slices.Contains(w.opts.Urgent, k) })
	}
	if len(kinds) == 0 {
		return nil, nil
	}
	// Urgent kinds sort first; the urgent set is bound like the kinds.
	urgentRank := ""
	var urgentArgs []any
	for _, k := range w.opts.Urgent {
		if slices.Contains(kinds, k) {
			urgentArgs = append(urgentArgs, k)
		}
	}
	if len(urgentArgs) > 0 {
		urgentRank = "kind IN (" + store.Placeholders(len(urgentArgs)) + ") DESC, "
	}
	now := store.Millis(w.q.now())
	leaseUntil := now + w.opts.Lease.Milliseconds()
	in := store.Placeholders(len(kinds))

	var j *Job
	err := w.q.st.WriteTx(ctx, func(tx *sql.Tx) error {
		j = nil
		if _, err := tx.ExecContext(ctx, `
			UPDATE job SET state = 'failed', finished_at = ?, updated_at = ?, lease_owner = NULL, lease_until = NULL,
			       last_error = 'lease expired on the last attempt (' || COALESCE(lease_owner, '?') || '): ' || COALESCE(last_error, '')
			WHERE state = 'running' AND lease_until <= ? AND attempts >= max_attempts`,
			now, now, now); err != nil {
			return err
		}
		args := []any{w.opts.Owner, leaseUntil, now}
		for _, k := range kinds {
			args = append(args, k)
		}
		args = append(args, now, now)
		args = append(args, urgentArgs...)
		row := tx.QueryRowContext(ctx, `
			UPDATE job SET state = 'running', lease_owner = ?, lease_until = ?, attempts = attempts + 1, updated_at = ?
			WHERE id = (
				SELECT id FROM job
				WHERE kind IN (`+in+`)
				  AND ((state = 'queued' AND run_at <= ?) OR (state = 'running' AND lease_until <= ?))
				ORDER BY `+urgentRank+`priority DESC, run_at, id
				LIMIT 1)
			RETURNING id, kind, payload, COALESCE(dedupe_key, ''), attempts, max_attempts, run_at`, args...)
		var payload string
		var runAt int64
		var job Job
		err := row.Scan(&job.ID, &job.Kind, &payload, &job.DedupeKey, &job.Attempt, &job.MaxAttempts, &runAt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		job.Payload, job.RunAt = []byte(payload), store.FromMillis(runAt)
		j = &job
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: lease: %w", err)
	}
	return j, nil
}

func (w *Worker) run(ctx context.Context, j *Job) {
	w.mu.RLock()
	h := w.handlers[j.Kind]
	w.mu.RUnlock()
	log := w.log.With("job", j.ID, "kind", j.Kind, "attempt", j.Attempt)

	hctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	f := &fence{w: w, j: j, cancel: cancel}
	f.until.Store(store.Millis(w.q.now().Add(w.opts.Lease)))
	hctx = context.WithValue(hctx, fenceKey{}, f)
	stopRenew := w.renew(hctx, f, log)

	start := time.Now()
	err := safeRun(hctx, h, j)
	w.duration.WithLabelValues(j.Kind).Observe(time.Since(start).Seconds())
	stopRenew()

	// Record the outcome even if ctx was cancelled, so a clean shutdown doesn't leave the job leased.
	octx, ocancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer ocancel()
	switch {
	case f.lost.Load():
		// The lease went to another run (or could not be kept): its outcome is that run's to record.
		w.runs.WithLabelValues(j.Kind, "lost").Inc()
		log.Warn("job lost its lease; the handler was stopped and its outcome is not recorded", "err", err)
	case err == nil:
		if ok, ferr := w.finish(octx, j, nil); ferr != nil {
			log.Error("job complete", "err", ferr)
		} else if !ok {
			w.runs.WithLabelValues(j.Kind, "lost").Inc()
			log.Warn("job finished after its lease was lost; another run owns it now")
		} else {
			w.runs.WithLabelValues(j.Kind, "done").Inc()
			log.Debug("job done", "took", time.Since(start))
		}
	case ctx.Err() != nil:
		// Shutting down: hand the job back without counting the interrupted run as a failure.
		if rerr := w.release(octx, j); rerr != nil {
			log.Error("job release", "err", rerr)
		}
		log.Info("job interrupted by shutdown; requeued")
	case isLater(err):
		after, _ := laterOf(err)
		if rerr := w.releaseAt(octx, j, w.q.now().Add(after)); rerr != nil {
			log.Error("job release", "err", rerr)
		}
		w.runs.WithLabelValues(j.Kind, "later").Inc()
		log.Debug("job handed back to run later", "after", after)
	default:
		outcome := "retry"
		if IsPermanent(err) || j.Attempt >= j.MaxAttempts {
			outcome = "failed"
		}
		if ok, ferr := w.finish(octx, j, err); ferr != nil {
			log.Error("job fail", "err", ferr)
		} else if !ok {
			outcome = "lost"
		}
		w.runs.WithLabelValues(j.Kind, outcome).Inc()
		log.Warn("job error", "err", err, "outcome", outcome)
		if outcome == "failed" {
			reportFailure(j, err)
		}
	}
}

// panicError is a handler's recovered panic. The error inbox has it with its stack already, so the job's failure is
// not recorded again.
type panicError struct{ p any }

func (e *panicError) Error() string { return fmt.Sprintf("handler panic: %v", e.p) }

func safeRun(ctx context.Context, h Handler, j *Job) (err error) {
	defer func() {
		if p := recover(); p != nil {
			errinbox.Panic("jobs", p, map[string]string{"kind": j.Kind, "job": strconv.FormatInt(j.ID, 10),
				"attempt": strconv.Itoa(j.Attempt)})
			err = &panicError{p}
		}
	}()
	return h(ctx, j)
}

// reportFailure records a job that failed for good in the error inbox: its last attempt, or a permanent error.
// Retries, panics (recorded by safeRun) and cancellations are left out.
func reportFailure(j *Job, err error) {
	var pe *panicError
	if errors.As(err, &pe) || errors.Is(err, context.Canceled) {
		return
	}
	errinbox.Report(errinbox.Event{Source: errinbox.SourceJob, Component: j.Kind, Message: err.Error(),
		Context: map[string]string{"kind": j.Kind, "job": strconv.FormatInt(j.ID, 10),
			"attempt": strconv.Itoa(j.Attempt), "max_attempts": strconv.Itoa(j.MaxAttempts)}})
}

// renew extends the lease every third of its length until stopped. A renewal that matches no row means another
// run took the job; failed renewals until the lease ran out mean another run may have. Either way the handler's
// context is cancelled with ErrLeaseLost.
func (w *Worker) renew(ctx context.Context, f *fence, log *slog.Logger) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(w.opts.Lease / 3)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				ok, err := f.extend(ctx)
				switch {
				case err != nil && ctx.Err() != nil:
					return
				case err != nil:
					if !w.q.now().Before(store.FromMillis(f.until.Load())) {
						log.Warn("lease renew failed until the lease ran out; stopping the handler", "err", err)
						f.lose()
						return
					}
					log.Warn("lease renew", "err", err)
				case !ok:
					log.Warn("lease lost to another run; stopping the handler")
					f.lose()
					return
				}
			}
		}
	}()
	return func() { close(done); wg.Wait() }
}

// finish completes (err == nil) or fails the job, if this run still owns it. It reports whether it did.
func (w *Worker) finish(ctx context.Context, j *Job, runErr error) (bool, error) {
	now := w.q.now()
	var res sql.Result
	err := w.q.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		own := ` WHERE id = ? AND state = 'running' AND lease_owner = ? AND attempts = ?`
		switch {
		case runErr == nil:
			res, err = tx.ExecContext(ctx, `
				UPDATE job SET state = 'done', finished_at = ?, updated_at = ?, lease_owner = NULL, lease_until = NULL`+own,
				store.Millis(now), store.Millis(now), j.ID, w.opts.Owner, j.Attempt)
		case IsPermanent(runErr) || j.Attempt >= j.MaxAttempts:
			res, err = tx.ExecContext(ctx, `
				UPDATE job SET state = 'failed', finished_at = ?, updated_at = ?, lease_owner = NULL, lease_until = NULL,
				       last_error = ?`+own,
				store.Millis(now), store.Millis(now), truncate(runErr.Error()), j.ID, w.opts.Owner, j.Attempt)
		default:
			res, err = tx.ExecContext(ctx, `
				UPDATE job SET state = 'queued', run_at = ?, updated_at = ?, lease_owner = NULL, lease_until = NULL,
				       last_error = ?`+own,
				store.Millis(now.Add(w.Backoff(j.Attempt))), store.Millis(now), truncate(runErr.Error()),
				j.ID, w.opts.Owner, j.Attempt)
		}
		return err
	})
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func isLater(err error) bool { _, ok := laterOf(err); return ok }

// release hands an interrupted job back: queued, ready now, and the interrupted run not counted.
func (w *Worker) release(ctx context.Context, j *Job) error { return w.releaseAt(ctx, j, w.q.now()) }

// releaseAt hands a job back, queued and ready at at, with the run not counted.
func (w *Worker) releaseAt(ctx context.Context, j *Job, at time.Time) error {
	now := store.Millis(w.q.now())
	return w.q.st.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE job SET state = 'queued', run_at = ?, updated_at = ?, lease_owner = NULL, lease_until = NULL,
			       attempts = attempts - 1
			WHERE id = ? AND state = 'running' AND lease_owner = ? AND attempts = ?`,
			store.Millis(at), now, j.ID, w.opts.Owner, j.Attempt)
		return err
	})
}

func truncate(s string) string {
	const max = 4000
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
