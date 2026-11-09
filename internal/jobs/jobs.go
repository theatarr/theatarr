// Package jobs is the persistent job queue on the store (docs/design/overview.md: one process, with a job queue in
// the database).
//
// A job has a kind, a JSON payload, a run-at time and an optional dedupe key. A [Worker] leases ready jobs by
// setting lease_until, runs the handler registered for the kind, and then completes the job or fails it. A failed job
// is retried with exponential back-off until it reaches its max attempts; a [Permanent] error fails it at once.
//
// Leases: a worker extends its lease while the handler runs. If the process dies, the lease expires and another
// worker (or the restarted process) leases the job again. So a job can run more than once, and handlers must be
// idempotent: every job step is idempotent, so a restart resumes where it stopped. Each lease counts as
// an attempt, so a job that keeps crashing the process still ends at max attempts. A run that loses its lease has its
// context cancelled, and [Fence] lets a handler confirm (and extend) its lease right before a side effect it cannot
// take back, so two runs of one job do not both take it (fence.go).
//
// Dedupe: at most one job per dedupe key is queued or running. Enqueueing a key that is already queued or running
// returns the existing job. A handler should therefore read current state when it starts, not trust a payload
// captured at enqueue time.
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// Job states, as stored.
const (
	StateQueued  = "queued"
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
)

// DefaultMaxAttempts applies when EnqueueOptions.MaxAttempts is 0.
const DefaultMaxAttempts = 5

// Queue enqueues jobs and answers questions about them.
type Queue struct {
	st     *store.Store
	now    func() time.Time
	notify chan struct{}
}

// NewQueue returns a queue on st.
func NewQueue(st *store.Store) *Queue {
	return &Queue{st: st, now: time.Now, notify: make(chan struct{}, 1)}
}

// SetClock replaces the clock (tests).
func (q *Queue) SetClock(now func() time.Time) { q.now = now }

// Job is a leased job as a handler sees it.
type Job struct {
	ID          int64
	Kind        string
	Payload     json.RawMessage
	DedupeKey   string
	Attempt     int // 1 on the first run
	MaxAttempts int
	RunAt       time.Time
}

// Decode unmarshals the payload into v.
func (j *Job) Decode(v any) error { return json.Unmarshal(j.Payload, v) }

// EnqueueOptions are optional settings for Enqueue.
type EnqueueOptions struct {
	// RunAt is when the job becomes ready. Zero means now.
	RunAt time.Time
	// DedupeKey, if set, makes the enqueue a no-op while a job with the same key is queued or running.
	DedupeKey string
	// MaxAttempts caps runs, including runs lost to expired leases. 0 means DefaultMaxAttempts.
	MaxAttempts int
	// Priority: higher runs first among ready jobs.
	Priority int
}

// Enqueue adds a job and returns its id. If DedupeKey matches a queued or running job, that job's id is returned
// with created = false.
func (q *Queue) Enqueue(ctx context.Context, kind string, payload any, opts EnqueueOptions) (id int64, created bool, err error) {
	err = q.st.WriteTx(ctx, func(tx *sql.Tx) error {
		id, created, err = q.EnqueueTx(ctx, tx, kind, payload, opts)
		return err
	})
	if err == nil && created {
		q.wake()
	}
	return id, created, err
}

// EnqueueTx is Enqueue inside the caller's write transaction, so a state change and the job that follows it commit
// together. Workers in this process are not woken early; they find the job on their next poll.
func (q *Queue) EnqueueTx(ctx context.Context, tx *sql.Tx, kind string, payload any, opts EnqueueOptions) (int64, bool, error) {
	if kind == "" {
		return 0, false, errors.New("jobs: empty kind")
	}
	body, err := marshalPayload(payload)
	if err != nil {
		return 0, false, err
	}
	now := q.now()
	runAt := opts.RunAt
	if runAt.IsZero() {
		runAt = now
	}
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}
	var dedupe sql.NullString
	if opts.DedupeKey != "" {
		dedupe = sql.NullString{String: opts.DedupeKey, Valid: true}
	}
	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO job (kind, payload, dedupe_key, state, priority, run_at, max_attempts, created_at, updated_at)
		VALUES (?, ?, ?, 'queued', ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING
		RETURNING id`,
		kind, string(body), dedupe, opts.Priority, store.Millis(runAt), maxAttempts, store.Millis(now), store.Millis(now),
	).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) || !dedupe.Valid {
		return 0, false, fmt.Errorf("jobs: enqueue %s: %w", kind, err)
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM job WHERE dedupe_key = ? AND state IN ('queued', 'running')`,
		dedupe).Scan(&id)
	if err != nil {
		return 0, false, fmt.Errorf("jobs: enqueue %s: dedupe lookup: %w", kind, err)
	}
	return id, false, nil
}

func marshalPayload(p any) ([]byte, error) {
	switch v := p.(type) {
	case nil:
		return []byte("{}"), nil
	case json.RawMessage:
		if !json.Valid(v) {
			return nil, errors.New("jobs: payload is not valid JSON")
		}
		return v, nil
	case []byte:
		if !json.Valid(v) {
			return nil, errors.New("jobs: payload is not valid JSON")
		}
		return v, nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("jobs: payload: %w", err)
	}
	return b, nil
}

func (q *Queue) wake() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// Info is a job row, for status pages and tests.
type Info struct {
	ID          int64
	Kind        string
	State       string
	Attempts    int
	MaxAttempts int
	RunAt       time.Time
	LastError   string
	DedupeKey   string
}

// Get returns one job.
func (q *Queue) Get(ctx context.Context, id int64) (Info, error) {
	var in Info
	var lastErr, dedupe sql.NullString
	var runAt int64
	err := q.st.Reader().QueryRowContext(ctx, `
		SELECT id, kind, state, attempts, max_attempts, run_at, last_error, dedupe_key FROM job WHERE id = ?`, id,
	).Scan(&in.ID, &in.Kind, &in.State, &in.Attempts, &in.MaxAttempts, &runAt, &lastErr, &dedupe)
	if err != nil {
		return in, err
	}
	in.RunAt, in.LastError, in.DedupeKey = store.FromMillis(runAt), lastErr.String, dedupe.String
	return in, nil
}

// Seen reports whether a job of kind whose dedupe key starts with prefix is queued or running, or was created at or
// after since (finished jobs are kept 14 days before the nightly prune).
func (q *Queue) Seen(ctx context.Context, kind, prefix string, since time.Time) (bool, error) {
	var n int
	err := q.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM job WHERE kind = ?
		AND (state IN ('queued', 'running') OR created_at >= ?) AND substr(dedupe_key, 1, ?) = ?`,
		kind, store.Millis(since), len(prefix), prefix).Scan(&n)
	return n > 0, err
}

// Counts returns the number of jobs per state.
func (q *Queue) Counts(ctx context.Context) (map[string]int, error) {
	rows, err := q.st.Reader().QueryContext(ctx, `SELECT state, count(*) FROM job GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]int{StateQueued: 0, StateRunning: 0, StateDone: 0, StateFailed: 0}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		m[s] = n
	}
	return m, rows.Err()
}

// Prune deletes finished (done or failed) jobs that finished before cutoff, and returns how many.
func (q *Queue) Prune(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := q.st.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM job WHERE state IN ('done', 'failed') AND finished_at < ?`,
			store.Millis(cutoff))
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}

// permanentError marks an error that must not be retried.
type permanentError struct{ err error }

func (p permanentError) Error() string { return p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent wraps err so the job fails at once instead of retrying.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

// IsPermanent reports whether err was wrapped by Permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// laterError asks for the job to run again after a wait, without counting the run as an attempt.
type laterError struct{ after time.Duration }

func (l laterError) Error() string { return fmt.Sprintf("jobs: not now; again in %v", l.after) }

// Later returns an error that hands the job back to the queue to run again after d, without counting the run as an
// attempt or as a failure: for a handler that must wait its turn (one run of a kind at a time), not for errors.
func Later(d time.Duration) error { return laterError{after: d} }

func laterOf(err error) (time.Duration, bool) {
	var l laterError
	if errors.As(err, &l) {
		return l.after, true
	}
	return 0, false
}
