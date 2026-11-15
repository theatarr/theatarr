// Package tasks is the scheduled-tasks table (migration 00133), as Sonarr's System › Tasks: each recurring task with
// its interval or time of day, its last run and its next run.
//
// Each recurring loop keeps its own timing (start-up delay, Pause, wall-clock slots, settings in force) and registers
// its task while it runs. When its time comes the loop calls [Table.Run] with the task's key and the next run it
// plans; the run updates the task's row: no job row is written. Two runs of one task never overlap: a Run while one
// is going (the loop's or a Run now) does nothing. A failed run is tried again after the job queue's back-off (10 s,
// doubling), up to the task's attempts; a run lost to a restart is not resumed, the loop's next time covers it.
// Whether a task is running, and its name and schedule, are known in this process only; the row keeps what outlives
// a restart: the last run, the last run that worked, and the next run.
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
	"sync/atomic"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

var (
	// ErrUnknown is a task no loop has registered now.
	ErrUnknown = errors.New("tasks: no such task scheduled")
	// ErrRunning is a Run now while the task runs.
	ErrRunning = errors.New("tasks: the task is already running")
	// ErrPaused is a Run now of a task that waits for Pause to be turned off (or a library).
	ErrPaused = errors.New("tasks: the task waits while Pause is on or no library is set up")
	// ErrSkip is returned by a task's Run that had nothing to do (nothing due, another run holds the work): no run is
	// recorded, only the next run.
	ErrSkip = errors.New("tasks: nothing to do")
)

// Task is one recurring task as its loop registers it.
type Task struct {
	Key  string
	Name string
	// Every is the interval; At the local time of day ("03:00") of a daily task. Both zero: run on demand only.
	Every time.Duration
	At    string
	// Attempts is how many tries one run gets (0 or 1: one).
	Attempts int
	// Paused: the loop waits (Pause on, or no library); Run now is refused.
	Paused bool
	// Run is the work.
	Run func(ctx context.Context) error
}

// Table holds the registered tasks and writes their rows. A table without a store (tests) records nothing.
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
	changes atomic.Int64
}

type entry struct {
	t      Task
	gen    uint64
	next   time.Time // the next run shown
	failed bool      // the newest run failed
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

// Register adds (or replaces) a task with its next run (zero: not known) and writes that next run to its row. The returned
// func removes it again (the loop stopped), unless a newer registration replaced it.
func (tb *Table) Register(ctx context.Context, t Task, next time.Time) (unregister func()) {
	failed := false
	if tb.st != nil {
		var lastErr string
		_ = tb.st.Reader().QueryRowContext(ctx, `SELECT last_error FROM task WHERE key = ?`, t.Key).Scan(&lastErr)
		failed = lastErr != ""
	}
	tb.mu.Lock()
	tb.gen++
	gen := tb.gen
	tb.tasks[t.Key] = &entry{t: t, gen: gen, failed: failed}
	tb.mu.Unlock()
	tb.changes.Add(1)
	tb.SetNext(t.Key, next)
	tb.write(ctx, t.Key, `INSERT INTO task (key, next_run) VALUES (?1, ?2)
		ON CONFLICT (key) DO UPDATE SET next_run = excluded.next_run`, t.Key, optMillis(next))
	return func() {
		tb.mu.Lock()
		defer tb.mu.Unlock()
		if e := tb.tasks[t.Key]; e != nil && e.gen == gen {
			delete(tb.tasks, t.Key)
			tb.changes.Add(1)
		}
	}
}

// SetNext changes the next run shown for the task (a loop that moved it after a run); its row gets it with the next
// run recorded.
func (tb *Table) SetNext(key string, next time.Time) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	if e := tb.tasks[key]; e != nil && !next.IsZero() && !e.next.Equal(next) {
		e.next = next
		tb.changes.Add(1)
	}
}

// Changes counts the changes of what List shows (a task registered or gone, a run started or recorded, a next run
// moved): the event stream tells open pages when it moves.
// A nil table has none.
func (tb *Table) Changes() int64 {
	if tb == nil {
		return 0
	}
	return tb.changes.Load()
}

// Run runs the registered task now in the caller's goroutine and records it with next as its next run (zero: keep
// the next run as it is), unless a run of it is already going (ran false). The error is the last try's.
func (tb *Table) Run(ctx context.Context, key string, next time.Time) (ran bool, err error) {
	tb.mu.Lock()
	e := tb.tasks[key]
	if e == nil {
		tb.mu.Unlock()
		return false, ErrUnknown
	}
	if _, busy := tb.running[key]; busy {
		tb.mu.Unlock()
		return false, nil
	}
	t := e.t
	start := tb.Now()
	tb.running[key] = start
	tb.mu.Unlock()
	tb.changes.Add(1)
	defer func() {
		tb.mu.Lock()
		delete(tb.running, key)
		tb.mu.Unlock()
		tb.changes.Add(1)
	}()
	err = tb.try(ctx, t)
	if errors.Is(err, ErrSkip) {
		tb.SetNext(key, next) // shown; written with the next run recorded
		return false, nil
	}
	tb.record(ctx, t, start, next, err)
	return true, err
}

// Start is Run in a goroutine of its own, for a loop that must not wait for the work (it keeps polling meanwhile).
// It reports false when the task is not registered or a run of it is already going.
func (tb *Table) Start(ctx context.Context, key string, next time.Time) bool {
	tb.mu.Lock()
	_, busy := tb.running[key]
	_, ok := tb.tasks[key]
	tb.mu.Unlock()
	if busy || !ok {
		return false
	}
	go func() { _, _ = tb.Run(ctx, key, next) }()
	return true
}

// Loop registers t and runs it every t.Every until ctx ends, the first run after first (0: at once).
func (tb *Table) Loop(ctx context.Context, t Task, first time.Duration) {
	defer tb.Register(ctx, t, time.Now().Add(first))()
	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		next := time.Now().Add(t.Every)
		_, _ = tb.Run(ctx, t.Key, next)
		timer.Reset(time.Until(next))
	}
}

// try runs t up to its attempts, waiting the back-off between tries.
func (tb *Table) try(ctx context.Context, t Task) error {
	attempts := max(t.Attempts, 1)
	for attempt := 1; ; attempt++ {
		err := t.Run(ctx)
		if err == nil || errors.Is(err, ErrSkip) || attempt >= attempts || ctx.Err() != nil {
			return err
		}
		tb.log.Warn("task failed; trying again", "task", t.Key, "attempt", attempt, "attempts", attempts, "err", err)
		timer := time.NewTimer(tb.Backoff(attempt))
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		timer.Stop()
	}
}

// record writes a finished run: its start and end, its error (” and the last good run when it worked), and next.
func (tb *Table) record(ctx context.Context, t Task, start, next time.Time, runErr error) {
	end := tb.Now()
	msg := ""
	if runErr != nil {
		msg = runErr.Error()
		if ctx.Err() == nil {
			tb.log.Warn("task failed", "task", t.Key, "err", runErr)
		}
	}
	var ok any
	if runErr == nil {
		ok = store.Millis(end)
	}
	tb.mu.Lock()
	if e := tb.tasks[t.Key]; e != nil {
		e.failed = runErr != nil
		if !next.IsZero() {
			e.next = next
		}
	}
	tb.mu.Unlock()
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	tb.write(wctx, t.Key, `INSERT INTO task (key, last_started, last_finished, last_error, last_ok, next_run)
		VALUES (?1, ?2, ?3, ?4, ?5, ?6)
		ON CONFLICT (key) DO UPDATE SET last_started = excluded.last_started, last_finished = excluded.last_finished,
			last_error = excluded.last_error, last_ok = COALESCE(excluded.last_ok, task.last_ok),
			next_run = COALESCE(excluded.next_run, task.next_run)`,
		t.Key, store.Millis(start), store.Millis(end), truncate(msg), ok, optMillis(next))
}

func (tb *Table) write(ctx context.Context, key, q string, args ...any) {
	if tb.st == nil {
		return
	}
	err := tb.st.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	})
	if err != nil && ctx.Err() == nil {
		tb.log.Warn("task not recorded", "task", key, "err", err)
	}
}

// RunNow starts a run of the task in the background (System › Tasks › Run now, the arr commands). It leaves the
// next scheduled run as it is: the loop's timer does not move.
func (tb *Table) RunNow(key string) error {
	tb.mu.Lock()
	e := tb.tasks[key]
	_, busy := tb.running[key]
	base := tb.base
	tb.mu.Unlock()
	switch {
	case e == nil:
		return ErrUnknown
	case e.t.Paused:
		return ErrPaused
	case busy:
		return ErrRunning
	}
	go func() {
		if ran, err := tb.Run(base, key, time.Time{}); ran && err == nil {
			tb.log.Info("task run now", "task", key)
		}
	}()
	return nil
}

// Row is what the table keeps of a task across restarts.
type Row struct {
	// LastStarted is when the newest run started; LastOK when the newest run that worked ended; NextRun the next
	// run as last planned (read before Register: the previous start's plan). Zero when none.
	LastStarted, LastOK, NextRun time.Time
}

// Read returns the task's row (zero for a task that never ran or was planned).
func (tb *Table) Read(ctx context.Context, key string) (Row, error) {
	var r Row
	if tb.st == nil {
		return r, nil
	}
	var started, ok, next sql.NullInt64
	err := tb.st.Reader().QueryRowContext(ctx, `SELECT last_started, last_ok, next_run FROM task WHERE key = ?`, key).
		Scan(&started, &ok, &next)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	r.LastStarted, r.LastOK, r.NextRun = timeOf(started), timeOf(ok), timeOf(next)
	return r, err
}

// Status is a registered task with its row, for System › Tasks.
type Status struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	EverySeconds int64  `json:"every_seconds"`
	At           string `json:"at,omitempty"`
	Paused       bool   `json:"paused,omitempty"`
	// Running is set while a run goes on, with when it started.
	Running      *time.Time `json:"running,omitempty"`
	LastStarted  *time.Time `json:"last_started,omitempty"`
	LastFinished *time.Time `json:"last_finished,omitempty"`
	// LastError is the newest run's error (it needs attention until a run works); LastOK when the newest run that
	// worked ended.
	LastError string     `json:"last_error,omitempty"`
	LastOK    *time.Time `json:"last_ok,omitempty"`
	NextRun   *time.Time `json:"next_run,omitempty"`
}

// List returns the registered tasks by name.
func (tb *Table) List(ctx context.Context) ([]Status, error) {
	tb.mu.Lock()
	out := make([]Status, 0, len(tb.tasks))
	byKey := map[string]int{}
	for _, e := range tb.tasks {
		s := Status{Key: e.t.Key, Name: e.t.Name, EverySeconds: int64(e.t.Every / time.Second), At: e.t.At, Paused: e.t.Paused}
		if at, ok := tb.running[e.t.Key]; ok {
			s.Running = &at
		}
		if !e.next.IsZero() && !e.t.Paused {
			next := e.next
			s.NextRun = &next
		}
		byKey[s.Key] = len(out)
		out = append(out, s)
	}
	tb.mu.Unlock()
	if tb.st != nil {
		rows, err := tb.st.Reader().QueryContext(ctx, `SELECT key, last_started, last_finished, last_error, last_ok FROM task`)
		if err != nil {
			return nil, fmt.Errorf("tasks: list: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var key, lastErr string
			var started, finished, ok sql.NullInt64
			if err := rows.Scan(&key, &started, &finished, &lastErr, &ok); err != nil {
				return nil, err
			}
			if i, found := byKey[key]; found {
				s := &out[i]
				s.LastStarted, s.LastFinished, s.LastError, s.LastOK = optTime(started), optTime(finished), lastErr, optTime(ok)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(out, func(a, b Status) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// Failed counts the registered tasks whose newest run failed: they need attention until a run works.
func (tb *Table) Failed() int {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	n := 0
	for _, e := range tb.tasks {
		if e.failed {
			n++
		}
	}
	return n
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

func timeOf(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return store.FromMillis(n.Int64)
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
