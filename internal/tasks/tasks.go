// Package tasks is the scheduled-tasks table (migration 00133), as Sonarr's System › Tasks: one row per recurring
// task with its interval or time of day, its last run and its next run.
//
// Each recurring loop keeps its own timing (start-up delay, Pause, wall-clock slots, settings in force) and registers
// its task while it runs. When its time comes the loop calls [Table.Run], which runs the task in the loop's goroutine
// and updates the row: no job row is written. Two runs of one task never overlap: a Run while one is going (the
// loop's or a Run now) does nothing. A failed run is tried again after the job queue's back-off (10 s, doubling), up
// to the task's attempts; a run lost to a restart is not resumed, the loop's next time covers it. Whether a task is
// running is known in this process only, so nothing shows as running after a restart.
package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/store"
)

var (
	// ErrUnknown is a task no loop has registered now.
	ErrUnknown = errors.New("tasks: no such task scheduled")
	// ErrRunning is a Run now while the task runs.
	ErrRunning = errors.New("tasks: the task is already running")
	// ErrPaused is a Run now of a task that waits for Pause to be turned off (or a Live library).
	ErrPaused = errors.New("tasks: the task waits while Pause is on or no library is Live")
)

// Task is one recurring task as its loop registers it.
type Task struct {
	Key  string
	Name string
	// Every is the interval; At the local time of day ("03:00") of a daily task, whose Every is 0.
	Every time.Duration
	At    string
	// Attempts is how many tries one run gets (0 or 1: one).
	Attempts int
	// Next is the first run the loop has planned, zero when not known.
	Next time.Time
	// Paused: the loop waits (Pause on, or no Live library); Run now is refused.
	Paused bool
	// Run is the work. Required unless Paused.
	Run func(ctx context.Context) error
}

// Table holds the registered tasks and writes their rows.
type Table struct {
	st  *store.Store
	log *slog.Logger
	// Now and Backoff can be replaced in tests.
	Now     func() time.Time
	Backoff func(attempt int) time.Duration

	mu      sync.Mutex
	base    context.Context
	gen     uint64
	tasks   map[string]*entry
	running map[string]time.Time
}

type entry struct {
	t   Task
	gen uint64
}

// New returns a table on st. Run now runs under base (the service's context).
func New(base context.Context, st *store.Store, log *slog.Logger) *Table {
	if log == nil {
		log = slog.Default()
	}
	return &Table{st: st, log: log.With("component", "tasks"), Now: time.Now, Backoff: backoff, base: base,
		tasks: map[string]*entry{}, running: map[string]time.Time{}}
}

// backoff is the job worker's default: 10 s × 2^(attempt-1), at most an hour.
func backoff(attempt int) time.Duration {
	d := 10 * time.Second
	for i := 1; i < attempt && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}

// Register adds (or replaces) a task and writes its row's name, schedule and planned next run; its last run is kept.
// The returned func removes it again (the loop stopped), unless a newer registration replaced it.
func (tb *Table) Register(ctx context.Context, t Task) (unregister func()) {
	if tb == nil {
		return func() {}
	}
	tb.mu.Lock()
	tb.gen++
	gen := tb.gen
	tb.tasks[t.Key] = &entry{t: t, gen: gen}
	tb.mu.Unlock()
	now := store.Millis(tb.Now())
	err := tb.st.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO task (key, name, every_s, at, next_run, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET name = excluded.name, every_s = excluded.every_s, at = excluded.at,
				next_run = excluded.next_run, updated_at = excluded.updated_at`,
			t.Key, t.Name, int64(t.Every/time.Second), t.At, optMillis(t.Next), now)
		return err
	})
	if err != nil && ctx.Err() == nil {
		tb.log.Warn("task not recorded", "task", t.Key, "err", err)
	}
	return func() {
		tb.mu.Lock()
		defer tb.mu.Unlock()
		if e := tb.tasks[t.Key]; e != nil && e.gen == gen {
			delete(tb.tasks, t.Key)
		}
	}
}

// SetNext records when the loop runs the task next (a loop whose next run is not the last start plus Every).
func (tb *Table) SetNext(ctx context.Context, key string, at time.Time) {
	if tb == nil {
		return
	}
	err := tb.st.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE task SET next_run = ?, updated_at = ? WHERE key = ?`, optMillis(at),
			store.Millis(tb.Now()), key)
		return err
	})
	if err != nil && ctx.Err() == nil {
		tb.log.Warn("task next run not recorded", "task", key, "err", err)
	}
}

// Run runs t now in the caller's goroutine and records it, unless a run of t.Key is already going (ran false). The
// error is the last try's. A nil table runs t without recording it.
func (tb *Table) Run(ctx context.Context, t Task) (ran bool, err error) {
	if tb == nil {
		return true, try(ctx, t, nil, backoff)
	}
	tb.mu.Lock()
	if _, busy := tb.running[t.Key]; busy {
		tb.mu.Unlock()
		return false, nil
	}
	start := tb.Now()
	tb.running[t.Key] = start
	tb.mu.Unlock()
	defer func() {
		tb.mu.Lock()
		delete(tb.running, t.Key)
		tb.mu.Unlock()
	}()
	err = try(ctx, t, tb.log, tb.Backoff)
	tb.record(ctx, t, start, err)
	return true, err
}

// try runs t up to its attempts, waiting the back-off between tries.
func try(ctx context.Context, t Task, log *slog.Logger, wait func(int) time.Duration) error {
	attempts := max(t.Attempts, 1)
	for attempt := 1; ; attempt++ {
		err := t.Run(ctx)
		if err == nil || attempt >= attempts || ctx.Err() != nil {
			return err
		}
		if log != nil {
			log.Warn("task failed; trying again", "task", t.Key, "attempt", attempt, "attempts", attempts, "err", err)
		}
		timer := time.NewTimer(wait(attempt))
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		timer.Stop()
	}
}

// record writes a finished run: its start, end, duration, error and the next run its schedule gives.
func (tb *Table) record(ctx context.Context, t Task, start time.Time, runErr error) {
	end := tb.Now()
	msg := ""
	if runErr != nil {
		msg = runErr.Error()
		if ctx.Err() == nil {
			tb.log.Warn("task failed", "task", t.Key, "err", runErr)
			errinbox.Report(errinbox.Event{Source: errinbox.SourceJob, Component: t.Key, Message: msg, Context: map[string]string{"task": t.Key}})
		}
	}
	var next time.Time
	switch {
	case t.At != "":
		next = NextAt(t.At, end)
	case t.Every > 0:
		next = start.Add(t.Every)
		if next.Before(end) {
			next = end
		}
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err := tb.st.WriteTx(wctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(wctx, `UPDATE task SET last_started = ?, last_finished = ?, last_duration_ms = ?,
			last_error = ?, next_run = COALESCE(?, next_run), updated_at = ? WHERE key = ?`,
			store.Millis(start), store.Millis(end), end.Sub(start).Milliseconds(), truncate(msg), optMillis(next),
			store.Millis(end), t.Key)
		return err
	})
	if err != nil {
		tb.log.Warn("task run not recorded", "task", t.Key, "err", err)
	}
}

// RunNow starts a run of the task in the background (System › Tasks › Run now, the arr commands).
func (tb *Table) RunNow(key string) error {
	if tb == nil {
		return ErrUnknown
	}
	tb.mu.Lock()
	e := tb.tasks[key]
	_, busy := tb.running[key]
	base := tb.base
	tb.mu.Unlock()
	switch {
	case e == nil:
		return ErrUnknown
	case e.t.Paused || e.t.Run == nil:
		return ErrPaused
	case busy:
		return ErrRunning
	}
	go errinbox.Go("task "+key, func() {
		if ran, err := tb.Run(base, e.t); ran && err == nil {
			tb.log.Info("task run now", "task", key)
		}
	})()
	return nil
}

// LastStarted is when the task's newest recorded run started (zero for none).
func (tb *Table) LastStarted(ctx context.Context, key string) (time.Time, error) {
	var at sql.NullInt64
	err := tb.st.Reader().QueryRowContext(ctx, `SELECT last_started FROM task WHERE key = ?`, key).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !at.Valid) {
		return time.Time{}, nil
	}
	return store.FromMillis(at.Int64), err
}

// Status is a registered task with its row, for System › Tasks.
type Status struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	EverySeconds int64  `json:"every_seconds"`
	At           string `json:"at,omitempty"`
	Paused       bool   `json:"paused,omitempty"`
	// Running is set while a run goes on, with when it started.
	Running        *time.Time `json:"running,omitempty"`
	LastStarted    *time.Time `json:"last_started,omitempty"`
	LastFinished   *time.Time `json:"last_finished,omitempty"`
	LastDurationMS int64      `json:"last_duration_ms"`
	LastError      string     `json:"last_error,omitempty"`
	NextRun        *time.Time `json:"next_run,omitempty"`
}

// List returns the registered tasks by name.
func (tb *Table) List(ctx context.Context) ([]Status, error) {
	tb.mu.Lock()
	out := make([]Status, 0, len(tb.tasks))
	for _, e := range tb.tasks {
		s := Status{Key: e.t.Key, Name: e.t.Name, EverySeconds: int64(e.t.Every / time.Second), At: e.t.At, Paused: e.t.Paused}
		if at, ok := tb.running[e.t.Key]; ok {
			s.Running = &at
		}
		out = append(out, s)
	}
	tb.mu.Unlock()
	rows, err := tb.st.Reader().QueryContext(ctx, `SELECT key, last_started, last_finished, COALESCE(last_duration_ms, 0),
		last_error, next_run FROM task`)
	if err != nil {
		return nil, fmt.Errorf("tasks: list: %w", err)
	}
	defer rows.Close()
	byKey := map[string]int{}
	for i, s := range out {
		byKey[s.Key] = i
	}
	for rows.Next() {
		var key, lastErr string
		var started, finished, next sql.NullInt64
		var dur int64
		if err := rows.Scan(&key, &started, &finished, &dur, &lastErr, &next); err != nil {
			return nil, err
		}
		i, ok := byKey[key]
		if !ok {
			continue
		}
		s := &out[i]
		s.LastStarted, s.LastFinished, s.LastDurationMS, s.LastError = optTime(started), optTime(finished), dur, lastErr
		if !s.Paused {
			s.NextRun = optTime(next)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b Status) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// NextAt is the next local time of day at ("15:04") after now.
func NextAt(at string, now time.Time) time.Time {
	tod, err := time.Parse("15:04", at)
	if err != nil {
		return time.Time{}
	}
	t := time.Date(now.Year(), now.Month(), now.Day(), tod.Hour(), tod.Minute(), 0, 0, now.Location())
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

func optMillis(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return store.Millis(t)
}

func optTime(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := store.FromMillis(n.Int64)
	return &t
}

func truncate(s string) string {
	const max = 4000
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
