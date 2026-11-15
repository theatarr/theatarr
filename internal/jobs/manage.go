package jobs

// Managing failed jobs. A failed job needs attention until one of these happens:
//
//   - Resolved: a later job (higher id) of the same kind finished done with the same payload, or with the empty
//     payload {}; or the scheduled task of that key (internal/tasks: provider.sync of every provider, the art batch,
//     the series sweep) worked after the job failed. Nothing is stored for this; it is read when jobs are listed.
//   - Retried: Retry queued a fresh job with the same kind, payload, priority, max attempts and dedupe key, linked by
//     retry_of. The new job carries the attention from then on: if it fails, it needs attention itself; if it
//     finishes done, it also resolves the old one.
//   - Dismissed: someone hid it (dismissed_at, dismissed_by).
//
// No job is ever deleted here; the Jobs view keeps listing failed jobs with how they ended.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

var (
	// ErrNotFound is a job id that does not exist.
	ErrNotFound = errors.New("jobs: no such job")
	// ErrNotFailed is a retry or dismissal of a job that has not failed.
	ErrNotFailed = errors.New("jobs: only a failed job can be retried or dismissed")
)

// Status is a job row with what became of it, for the Jobs view, the API and theatarr jobs (which prints it as JSON).
type Status struct {
	ID          int64           `json:"id"`
	Kind        string          `json:"kind"`
	State       string          `json:"state"`
	Priority    int             `json:"priority"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"max_attempts"`
	RunAt       time.Time       `json:"run_at"`
	LastError   string          `json:"last_error,omitempty"`
	DedupeKey   string          `json:"dedupe_key,omitempty"`
	Payload     json.RawMessage `json:"payload"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
	// StartedAt is when the current (or last) run was claimed; nil for a job never claimed, or claimed before the
	// column existed.
	StartedAt *time.Time `json:"started_at,omitempty"`
	// RetryOf is the failed job this one re-runs, 0 if none.
	RetryOf int64 `json:"retry_of,omitempty"`
	// RetriedAs is the newest job that re-runs this one, 0 if none.
	RetriedAs int64 `json:"retried_as,omitempty"`
	// ResolvedBy is, for a failed job, the first later done job of the same kind and payload (or payload {}).
	ResolvedBy int64 `json:"resolved_by,omitempty"`
	// ResolvedByTask: the scheduled task of the job's kind worked after the job failed (its sweep covers it).
	ResolvedByTask bool       `json:"resolved_by_task,omitempty"`
	DismissedAt    *time.Time `json:"dismissed_at,omitempty"`
	DismissedBy    string     `json:"dismissed_by,omitempty"`
	// Attention: failed, and not resolved, retried or dismissed.
	Attention bool `json:"attention"`
}

// ListFilter selects jobs for List. The zero value lists the latest 100 of every state.
type ListFilter struct {
	State string // queued, running, done or failed; empty for all
	Kind  string
	// Attention keeps only failed jobs that still need attention.
	Attention bool
	// HideDismissed leaves out dismissed jobs.
	HideDismissed bool
	// Limit caps the rows (1..500, default 100).
	Limit int
}

// The derived columns, on a job aliased j.
const (
	resolvedBySQL = `CASE WHEN j.state = 'failed' THEN (SELECT MIN(d.id) FROM job d
		WHERE d.kind = j.kind AND d.state = 'done' AND d.id > j.id AND (d.payload = j.payload OR d.payload = '{}')) END`
	resolvedByTaskSQL = `(j.state = 'failed' AND EXISTS (SELECT 1 FROM task t WHERE t.key = j.kind
		AND t.last_ok > COALESCE(j.finished_at, j.updated_at)))`
	retriedAsSQL = `(SELECT MAX(r.id) FROM job r WHERE r.retry_of = j.id)`
	attentionSQL = `(j.state = 'failed' AND j.dismissed_at IS NULL AND ` + retriedAsSQL + ` IS NULL AND ` +
		resolvedBySQL + ` IS NULL AND NOT ` + resolvedByTaskSQL + `)`
	statusColumns = `j.id, j.kind, j.state, j.priority, j.attempts, j.max_attempts, j.run_at, COALESCE(j.last_error, ''),
		COALESCE(j.dedupe_key, ''), j.payload, j.created_at, j.updated_at, j.finished_at, COALESCE(j.retry_of, 0),
		COALESCE(` + retriedAsSQL + `, 0), COALESCE(` + resolvedBySQL + `, 0), j.dismissed_at, COALESCE(j.dismissed_by, ''),
		j.started_at, ` + resolvedByTaskSQL
)

func scanStatus(sc interface{ Scan(...any) error }) (Status, error) {
	var s Status
	var payload string
	var runAt, created, updated int64
	var finished, dismissed, started sql.NullInt64
	err := sc.Scan(&s.ID, &s.Kind, &s.State, &s.Priority, &s.Attempts, &s.MaxAttempts, &runAt, &s.LastError,
		&s.DedupeKey, &payload, &created, &updated, &finished, &s.RetryOf, &s.RetriedAs, &s.ResolvedBy, &dismissed,
		&s.DismissedBy, &started, &s.ResolvedByTask)
	if err != nil {
		return s, err
	}
	s.Payload = json.RawMessage(payload)
	s.RunAt, s.CreatedAt, s.UpdatedAt = store.FromMillis(runAt), store.FromMillis(created), store.FromMillis(updated)
	s.FinishedAt, s.DismissedAt, s.StartedAt = optTime(finished), optTime(dismissed), optTime(started)
	s.Attention = s.State == StateFailed && s.DismissedAt == nil && s.RetriedAs == 0 && s.ResolvedBy == 0 &&
		!s.ResolvedByTask
	return s, nil
}

func optTime(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := store.FromMillis(n.Int64)
	return &t
}

// listOrder is List's order, one indexed read per state group: running and queued by run time (job_ready), then the
// latest finished, newest first (job_finished).
var listOrder = []struct{ states, order string }{
	{`j.state = 'running'`, `j.run_at, j.id`},
	{`j.state = 'queued'`, `j.run_at, j.id`},
	{`j.state IN ('done', 'failed')`, `j.finished_at DESC, j.id DESC`},
}

// stateGroup is the listOrder group a state filter reads.
var stateGroup = map[string]int{StateRunning: 0, StateQueued: 1, StateDone: 2, StateFailed: 2}

// List returns jobs: running first, then queued by run time, then the latest finished.
func (q *Queue) List(ctx context.Context, f ListFilter) ([]Status, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	conds := []string{"1=1"}
	var args []any
	if f.State != "" {
		conds = append(conds, "j.state = ?")
		args = append(args, f.State)
	}
	if f.Kind != "" {
		conds = append(conds, "j.kind = ?")
		args = append(args, f.Kind)
	}
	if f.Attention {
		conds = append(conds, attentionSQL)
	}
	if f.HideDismissed {
		conds = append(conds, "j.dismissed_at IS NULL")
	}
	where := strings.Join(conds, " AND ")
	out := []Status{}
	for i, g := range listOrder {
		if f.State != "" && stateGroup[f.State] != i {
			continue // a state filter reads its own group only
		}
		left := f.Limit - len(out)
		if left <= 0 {
			break
		}
		rows, err := q.st.Reader().QueryContext(ctx, `SELECT `+statusColumns+` FROM job j
			WHERE `+g.states+` AND `+where+` ORDER BY `+g.order+` LIMIT ?`, append(args, left)...)
		if err != nil {
			return nil, fmt.Errorf("jobs: list: %w", err)
		}
		for rows.Next() {
			s, err := scanStatus(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, s)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Status returns one job with what became of it.
func (q *Queue) Status(ctx context.Context, id int64) (Status, error) {
	s, err := scanStatus(q.st.Reader().QueryRowContext(ctx, `SELECT `+statusColumns+` FROM job j WHERE j.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	return s, err
}

// AttentionCount is the number of failed jobs that still need attention.
func (q *Queue) AttentionCount(ctx context.Context) (int, error) {
	var n int
	err := q.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM job j WHERE j.state = 'failed' AND `+attentionSQL).
		Scan(&n)
	return n, err
}

// Retry queues a fresh run of a failed job: same kind, payload, priority, max attempts and dedupe key, with retry_of
// pointing at the old job, which stays as it is. Retrying again while an earlier retry is queued or running returns
// that one (created false). If the dedupe key belongs to a job already queued or running, that job is returned and
// linked as the retry, unless it already re-runs another job.
func (q *Queue) Retry(ctx context.Context, id int64) (newID int64, created bool, err error) {
	err = q.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var kind, state, payload string
		var priority, maxAttempts int
		var dedupe sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT kind, state, payload, priority, max_attempts, dedupe_key FROM job WHERE id = ?`,
			id).Scan(&kind, &state, &payload, &priority, &maxAttempts, &dedupe)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if state != StateFailed {
			return ErrNotFailed
		}
		err = tx.QueryRowContext(ctx, `SELECT id FROM job WHERE retry_of = ? AND state IN ('queued', 'running')
			ORDER BY id DESC LIMIT 1`, id).Scan(&newID)
		if err == nil {
			created = false
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		newID, created, err = q.EnqueueTx(ctx, tx, kind, json.RawMessage(payload),
			EnqueueOptions{DedupeKey: dedupe.String, MaxAttempts: maxAttempts, Priority: priority})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE job SET retry_of = ? WHERE id = ? AND retry_of IS NULL`, id, newID)
		return err
	})
	if err != nil {
		return 0, false, err
	}
	if created {
		q.wake()
	}
	return newID, created, nil
}

// Dismiss hides a failed job from attention, recording when and by whom. Dismissing it again changes nothing.
func (q *Queue) Dismiss(ctx context.Context, id int64, by string) (Status, error) {
	now := store.Millis(q.now())
	err := q.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var state string
		var dismissed sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT state, dismissed_at FROM job WHERE id = ?`, id).Scan(&state, &dismissed)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if state != StateFailed {
			return ErrNotFailed
		}
		if dismissed.Valid {
			return nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE job SET dismissed_at = ?, dismissed_by = ?, updated_at = ? WHERE id = ?`,
			now, by, now, id)
		return err
	})
	if err != nil {
		return Status{}, err
	}
	return q.Status(ctx, id)
}

// DismissResolved dismisses every failed job that a later done job resolved, and returns how many.
func (q *Queue) DismissResolved(ctx context.Context, by string) (int64, error) {
	now := store.Millis(q.now())
	var n int64
	err := q.st.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE job AS j SET dismissed_at = ?, dismissed_by = ?, updated_at = ?
			WHERE j.state = 'failed' AND j.dismissed_at IS NULL AND (`+resolvedBySQL+` IS NOT NULL OR `+resolvedByTaskSQL+`)`, now, by, now)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}
