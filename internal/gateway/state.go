package gateway

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// state holds back-offs and budget window counts in memory and writes them through to SQLite when a store is set.
// Memory is the source of truth while running; the database is read once at start. A failed write is logged and the
// request goes on: the in-memory state still enforces the caps until the next restart.
type state struct {
	st  *store.Store // nil: memory only (theatarr egress-check, tests)
	log *slog.Logger

	mu       sync.Mutex
	backoffs map[string]*backoff
	windows  map[windowKey]int
	// requests counts indexer requests per indexer, purpose and UTC day (purpose.go); requestDay is the latest day
	// counted. pending are the counts not yet written and requestCut the day before which stored rows are to be
	// pruned (0: none), both written by flushRequests, which flushMu runs one at a time.
	requests   map[requestKey]int
	requestDay int64
	pending    map[requestKey]int
	requestCut int64
	flushMu    sync.Mutex
	// seq numbers back-off changes. A request notes it when it is sent (mark), and its success clears only a
	// back-off that changed before that: a 429 another request got meanwhile stands.
	seq uint64
	// wmu orders the back-off writes: each writes the subject's state as memory holds it then, so a stale row never
	// lands after a newer one.
	wmu sync.Mutex
	// checking holds the subjects with a health check (HealthCheck) in flight through their failure back-off: one
	// at a time each.
	checking map[string]bool
}

type backoff struct {
	until    time.Time
	failures int
	reason   string
	updated  time.Time
	seq      uint64 // state.seq at its last change
	// streak: the back-off is only the failures-in-a-row one (targetFailure), which a health check may go through.
	// A rate limit or an out-of-band back-off on the subject clears it.
	streak bool
}

// streakReason starts the reason of a failures-in-a-row back-off; a persisted row is recognized by it at start.
const streakReason = " failures in a row, last: "

type windowKey struct {
	budget string
	period string // "hour" or "day"
	start  int64  // window start, Unix ms
}

func newState(ctx context.Context, st *store.Store, log *slog.Logger, now time.Time) (*state, error) {
	s := &state{st: st, log: log, backoffs: map[string]*backoff{}, windows: map[windowKey]int{}}
	if st == nil {
		return s, nil
	}
	rows, err := st.Reader().QueryContext(ctx, `SELECT subject, until_at, failures, reason, updated_at FROM gateway_backoff`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var subj, reason string
		var until, updated int64
		var failures int
		if err := rows.Scan(&subj, &until, &failures, &reason, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		b := &backoff{failures: failures, reason: reason, updated: store.FromMillis(updated),
			streak: strings.Contains(reason, streakReason)}
		if until > 0 {
			b.until = store.FromMillis(until)
		}
		s.backoffs[subj] = b
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	since := store.Millis(now.Add(-48 * time.Hour))
	rows, err = st.Reader().QueryContext(ctx,
		`SELECT budget, period, window_start_at, count FROM gateway_budget_window WHERE window_start_at >= ?`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k windowKey
		var n int
		if err := rows.Scan(&k.budget, &k.period, &k.start, &n); err != nil {
			return nil, err
		}
		s.windows[k] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s, s.loadRequests(ctx, now)
}

// backoff returns the active back-off on subject, if any.
func (s *state) active(subject string, now time.Time) (until time.Time, reason string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.backoffs[subject]
	if b == nil || !b.until.After(now) {
		return time.Time{}, "", false
	}
	return b.until, b.reason, true
}

// streakOnly reports whether subject has no active back-off, or only the failures-in-a-row one.
func (s *state) streakOnly(subject string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.backoffs[subject]
	return b == nil || !b.until.After(now) || b.streak
}

// beginCheck claims subject's one health check in flight; false when another holds it. endCheck releases it.
func (s *state) beginCheck(subject string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checking[subject] {
		return false
	}
	if s.checking == nil {
		s.checking = map[string]bool{}
	}
	s.checking[subject] = true
	return true
}

func (s *state) endCheck(subject string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.checking, subject)
}

// setBackoff backs subject off until until. failures is incremented when count is true. It is a rate limit or an
// out-of-band back-off: a health check no longer goes through it (failureBackoff is the one that may).
func (s *state) setBackoff(ctx context.Context, subject string, until time.Time, reason string, count bool, now time.Time) int {
	return s.backOff(ctx, subject, until, reason, count, false, now)
}

// failureBackoff is setBackoff for the failures-in-a-row back-off (targetFailure). An active rate limit or
// out-of-band back-off on the subject keeps it from becoming one a health check may go through.
func (s *state) failureBackoff(ctx context.Context, subject string, until time.Time, reason string, now time.Time) {
	s.backOff(ctx, subject, until, reason, false, true, now)
}

func (s *state) backOff(ctx context.Context, subject string, until time.Time, reason string, count, streak bool, now time.Time) int {
	s.mu.Lock()
	b := s.backoffs[subject]
	if b == nil {
		b = &backoff{}
		s.backoffs[subject] = b
	}
	if count {
		b.failures++
	}
	// A failure back-off is one a health check may go through, unless a rate limit or out-of-band back-off on the
	// subject is still running: that one stays in force until its own end.
	b.streak = streak && (b.streak || !b.until.After(now))
	if until.After(b.until) {
		b.until = until
	}
	b.reason = reason
	b.updated = now
	s.seq++
	b.seq = s.seq
	n := b.failures
	s.mu.Unlock()
	s.persist(ctx, subject)
	return n
}

// failure counts one failure on subject without setting a back-off yet, and returns the count.
func (s *state) failure(ctx context.Context, subject, reason string, now time.Time) int {
	s.mu.Lock()
	b := s.backoffs[subject]
	if b == nil {
		b = &backoff{}
		s.backoffs[subject] = b
	}
	b.failures++
	b.reason = reason
	b.updated = now
	s.seq++
	b.seq = s.seq
	n := b.failures
	s.mu.Unlock()
	s.persist(ctx, subject)
	return n
}

// mark is the back-off change number a request sees when it is sent; success takes it back.
func (s *state) mark() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq
}

// success clears subject's failures and back-off, unless they changed after sent (mark when the request was sent):
// a slow request's answer says nothing about a 429 or failure that came after it left. It writes only when there
// was something to clear.
func (s *state) success(ctx context.Context, subject string, sent uint64) {
	s.mu.Lock()
	b := s.backoffs[subject]
	if b == nil || b.seq > sent {
		s.mu.Unlock()
		return
	}
	delete(s.backoffs, subject)
	s.mu.Unlock()
	s.persist(ctx, subject)
}

func (s *state) failures(subject string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b := s.backoffs[subject]; b != nil {
		return b.failures
	}
	return 0
}

// persist writes subject's back-off row as memory holds it now, or deletes it when memory has none. Writes are
// serialized and each reads memory under the write lock, so the last write is always the newest state.
func (s *state) persist(ctx context.Context, subject string) {
	if s.st == nil {
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	var row *backoff
	if b := s.backoffs[subject]; b != nil {
		cp := *b
		row = &cp
	}
	s.mu.Unlock()
	if row == nil {
		if _, err := s.st.Writer().ExecContext(ctx, `DELETE FROM gateway_backoff WHERE subject = ?`, subject); err != nil {
			s.log.Warn("gateway: clearing back-off", "subject", subject, "err", err)
		}
		return
	}
	var until int64
	if !row.until.IsZero() {
		until = store.Millis(row.until)
	}
	_, err := s.st.Writer().ExecContext(ctx, `
		INSERT INTO gateway_backoff (subject, until_at, failures, reason, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (subject) DO UPDATE SET until_at = excluded.until_at, failures = excluded.failures,
			reason = excluded.reason, updated_at = excluded.updated_at`,
		subject, until, row.failures, row.reason, store.Millis(row.updated))
	if err != nil {
		s.log.Warn("gateway: saving back-off", "subject", subject, "err", err)
	}
}

// count returns the requests counted in a window.
func (s *state) count(k windowKey) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.windows[k]
}

// reserve counts one request in each window unless one of them is at its limit (limits[i] for keys[i]), as one step:
// concurrent requests cannot all pass the check before any of them counts. It returns the full window's index, or
// -1 when it counted, and writes the count through.
func (s *state) reserve(ctx context.Context, keys []windowKey, limits []int, now time.Time) int {
	s.mu.Lock()
	for i, k := range keys {
		if s.windows[k] >= limits[i] {
			s.mu.Unlock()
			return i
		}
	}
	fresh := s.bump(keys, now)
	s.mu.Unlock()
	s.saveWindows(ctx, keys, fresh, now)
	return -1
}

// bump counts one request in each window, in memory; s.mu is held. fresh is whether a new window began.
func (s *state) bump(keys []windowKey, now time.Time) (fresh bool) {
	for _, k := range keys {
		if _, ok := s.windows[k]; !ok {
			fresh = true
		}
		s.windows[k]++
	}
	if fresh {
		// A new window began: drop old ones from memory.
		cut := store.Millis(now.Add(-48 * time.Hour))
		for k := range s.windows {
			if k.start < cut {
				delete(s.windows, k)
			}
		}
	}
	return fresh
}

// saveWindows writes one counted request in each window through to the database.
func (s *state) saveWindows(ctx context.Context, keys []windowKey, fresh bool, now time.Time) {
	if s.st == nil || len(keys) == 0 {
		return
	}
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, k := range keys {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO gateway_budget_window (budget, period, window_start_at, count) VALUES (?, ?, ?, 1)
				ON CONFLICT (budget, period, window_start_at) DO UPDATE SET count = count + 1`,
				k.budget, k.period, k.start); err != nil {
				return err
			}
		}
		if fresh {
			_, err := tx.ExecContext(ctx, `DELETE FROM gateway_budget_window WHERE window_start_at < ?`,
				store.Millis(now.Add(-48*time.Hour)))
			return err
		}
		return nil
	})
	if err != nil {
		s.log.Warn("gateway: saving budget count", "err", err)
	}
}

// prune drops back-offs that are over and were last updated before cut (memory-only probe health).
func (s *state) prune(now, cut time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, b := range s.backoffs {
		if !b.until.After(now) && b.updated.Before(cut) {
			delete(s.backoffs, k)
		}
	}
}

// snapshot copies the back-offs and window counts (metrics).
func (s *state) snapshot() (map[string]backoff, map[windowKey]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := make(map[string]backoff, len(s.backoffs))
	for k, v := range s.backoffs {
		b[k] = *v
	}
	w := make(map[windowKey]int, len(s.windows))
	for k, v := range s.windows {
		w[k] = v
	}
	return b, w
}
