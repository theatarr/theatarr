package library

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Mount calls that hang. A call on a dead FUSE mount can block in the kernel for as long as the mount stays dead, and
// nothing in Go can cancel it: withTimeout gives up waiting, but the goroutine stays blocked. So every mount call
// goes through a guard:
//
//   - at most Mount.MaxCalls calls are outstanding at once, counting those still blocked after their timeout. A call
//     that finds every slot taken waits for one up to its timeout and then counts as a timeout itself;
//   - after Mount.BreakAfter timeouts in a row the circuit opens: every mount call fails at once with ErrMountDown,
//     without a goroutine, so import, relink and dangling passes back off instead of piling up blocked calls;
//   - after Mount.Cooldown with the circuit open, one call is let through (half open): an answer in time closes the
//     circuit, another timeout opens it for another cooldown.
//
// Errors that come back (ENOTCONN, EIO, a missing release root) are ErrMountDown too, but they do not trip the
// circuit: they leave no goroutine behind. Mount.Breaker reports the state; the library exports it as metrics.
//
// With helper processes (theatarr serve; mountpool.go) a call does not run in Theatarr at all: the caller waits for the helper's
// answer until the deadline, and a helper that misses it is killed and replaced. The circuit works the same; the
// slots bound only the calls still run in Theatarr (no helpers, or a test filesystem).

// Defaults for the mount guard.
const (
	DefaultMountMaxCalls   = 8
	DefaultMountBreakAfter = 3
	DefaultMountCooldown   = time.Minute
)

// probeTimeout bounds a header probe of one file on the mount. Probes read up to probe_max_bytes, so they get much
// longer than a stat.
var probeTimeout = 2 * time.Minute

// Breaker states.
const (
	BreakerClosed   = "closed"
	BreakerOpen     = "open"
	BreakerHalfOpen = "half_open"
)

// BreakerState is the mount guard's state, for health checks and metrics.
type BreakerState struct {
	State       string    `json:"state"`       // closed, open, half_open
	Outstanding int       `json:"outstanding"` // mount calls running, including those blocked past their timeout
	MaxCalls    int       `json:"max_calls"`
	Consecutive int       `json:"consecutive"` // timeouts in a row
	Timeouts    int64     `json:"timeouts"`    // since start
	Opens       int64     `json:"opens"`       // times the circuit opened since start
	OpenedAt    time.Time `json:"opened_at,omitzero"`
	RetryAt     time.Time `json:"retry_at,omitzero"` // when an open circuit lets one call through
	LastError   string    `json:"last_error,omitempty"`
	// Helper is the helper processes the calls run in; nil when they run in Theatarr itself.
	Helper *HelperState `json:"helper,omitempty"`
}

// Open reports whether new mount calls are refused now.
func (s BreakerState) Open() bool { return s.State != BreakerClosed }

// guard is the mount's call limiter and circuit breaker.
type guard struct {
	sem        chan struct{}
	breakAfter int
	cooldown   time.Duration
	now        func() time.Time

	mu          sync.Mutex
	consecutive int
	open        bool
	openedAt    time.Time
	probing     bool // half open: the one call let through is running
	timeouts    int64
	opens       int64
	lastErr     string
	// listMax is the largest listing accepted per release root (Mount.checkListing). It lives with the guard so a
	// later settings generation for the same mount (Mount.inherit) keeps it.
	listMax map[string]int
}

func (m *Mount) guard() *guard {
	m.once.Do(func() {
		n, b, c := m.MaxCalls, m.BreakAfter, m.Cooldown
		if n <= 0 {
			n = DefaultMountMaxCalls
		}
		if b <= 0 {
			b = DefaultMountBreakAfter
		}
		if c <= 0 {
			c = DefaultMountCooldown
		}
		now := m.Now
		if now == nil {
			now = time.Now
		}
		m.g = &guard{sem: make(chan struct{}, n), breakAfter: b, cooldown: c, now: now}
	})
	return m.g
}

// Breaker returns the mount guard's state.
func (m *Mount) Breaker() BreakerState {
	g := m.guard()
	g.mu.Lock()
	defer g.mu.Unlock()
	s := BreakerState{State: BreakerClosed, Outstanding: len(g.sem), MaxCalls: cap(g.sem), Consecutive: g.consecutive,
		Timeouts: g.timeouts, Opens: g.opens, LastError: g.lastErr}
	if m.pool != nil {
		h := m.pool.state()
		s.Helper = &h
		s.Outstanding += h.Busy
	}
	if g.open {
		s.State, s.OpenedAt, s.RetryAt = BreakerOpen, g.openedAt, g.openedAt.Add(g.cooldown)
		if g.probing || !g.now().Before(s.RetryAt) {
			s.State = BreakerHalfOpen
		}
	}
	return s
}

// admit says whether a call may start, and whether it is the half-open probe.
func (g *guard) admit() (probe bool, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.open {
		return false, nil
	}
	retry := g.openedAt.Add(g.cooldown)
	if g.probing || g.now().Before(retry) {
		return false, fmt.Errorf("%w: circuit open after %d timeouts in a row (%s); retried after %s", ErrMountDown,
			g.consecutive, g.lastErr, retry.Format(time.RFC3339))
	}
	g.probing = true
	return true, nil
}

// timedOut records a timeout and opens the circuit when it is one too many.
func (g *guard) timedOut(probe bool, why string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.timeouts++
	g.consecutive++
	g.lastErr = why
	if probe {
		g.probing = false
	}
	if probe || (!g.open && g.consecutive >= g.breakAfter) {
		if !g.open {
			g.opens++
		}
		g.open, g.openedAt = true, g.now()
	}
}

// answered records a call that came back in time (whatever it returned): the mount is alive.
func (g *guard) answered(probe bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.consecutive = 0
	if probe {
		g.probing = false
	}
	if g.open && (probe || !g.probing) {
		g.open = false
	}
}

// abandoned releases a half-open probe that ended without a verdict (the caller's context ended).
func (g *guard) abandoned(probe bool) {
	if !probe {
		return
	}
	g.mu.Lock()
	g.probing = false
	g.mu.Unlock()
}

// withTimeout runs a filesystem call that may hang on a dead FUSE mount, under the guard, with StatTimeout. A hang,
// ENOTCONN or EIO is ErrMountDown. path is the path the call reads: the call goes to the helper processes when the
// mount has them (helperFor), outside the mount root too; "" runs in this process.
func (m *Mount) withTimeout(ctx context.Context, path string, f func(mountFS) (any, error)) (any, error) {
	t := m.StatTimeout
	if t <= 0 {
		t = 5 * time.Second
	}
	return m.guarded(ctx, t, path, f)
}

// helperFor returns the helper pool for a call on path, nil when the call runs in this process: no helper, a test
// filesystem, or no path. A path outside the mount root goes to a helper too: whether it reaches the mount (a
// client's folder of symlinks into it) is only known by following it, which must not block Theatarr. The helper answers
// such a path's stat, lstat, readlink and listing only (helperServer.place).
func (m *Mount) helperFor(path string) *helperPool {
	if m.pool == nil || m.fs != nil || path == "" {
		return nil
	}
	return m.pool
}

func (m *Mount) guarded(ctx context.Context, t time.Duration, path string, f func(mountFS) (any, error)) (any, error) {
	g := m.guard()
	probe, err := g.admit()
	if err != nil {
		return nil, err
	}
	if pool := m.helperFor(path); pool != nil {
		return m.helperCall(ctx, g, probe, t, pool, f)
	}
	timer := time.NewTimer(t)
	defer timer.Stop()
	// A slot, or a timeout: every slot held means calls are blocked in the kernel.
	select {
	case g.sem <- struct{}{}:
	case <-timer.C:
		why := fmt.Sprintf("%d mount calls still blocked", cap(g.sem))
		g.timedOut(probe, why)
		return nil, fmt.Errorf("%w: %s", ErrMountDown, why)
	case <-ctx.Done():
		g.abandoned(probe)
		return nil, ctx.Err()
	}
	type result struct {
		v   any
		err error
	}
	ch := make(chan result, 1)
	fsys := m.fsys()
	go func() {
		defer func() { <-g.sem }() // only when the call returns: a blocked call keeps its slot
		v, err := f(fsys)
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		g.answered(probe)
		return answer(r.v, r.err)
	case <-timer.C:
		why := fmt.Sprintf("no answer in %v", t)
		g.timedOut(probe, why)
		return nil, fmt.Errorf("%w: %s", ErrMountDown, why)
	case <-ctx.Done():
		g.abandoned(probe)
		return nil, ctx.Err()
	}
}

// helperCall runs f in this goroutine with the helper pool's calls, each bounded by the call's deadline: a helper
// that misses it is killed, so nothing stays blocked here (mountpool.go).
func (m *Mount) helperCall(ctx context.Context, g *guard, probe bool, t time.Duration, pool *helperPool,
	f func(mountFS) (any, error)) (any, error) {
	hf := &helperFS{m: m, pool: pool, ctx: ctx, deadline: time.Now().Add(t)}
	v, err := f(hf)
	hf.done.Store(true)
	switch {
	case err != nil && helperNoAnswer(err):
		g.timedOut(probe, err.Error())
		return nil, fmt.Errorf("%w: %v", ErrMountDown, err)
	case errors.Is(err, errHelperBusy):
		g.abandoned(probe)
		return nil, fmt.Errorf("%w: %v", ErrMountDown, err)
	case err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()):
		g.abandoned(probe)
		return nil, err
	}
	g.answered(probe)
	return answer(v, err)
}

// answer is a call that came back: ENOTCONN and EIO are the mount failing.
func answer(v any, err error) (any, error) {
	if err != nil && (errors.Is(err, syscall.ENOTCONN) || errors.Is(err, syscall.EIO)) {
		return nil, fmt.Errorf("%w: %v", ErrMountDown, err)
	}
	return v, err
}

// RegisterMountMetrics exports the mount guard's state of the library current returns, read at each scrape: a
// settings save that moves the mount starts a fresh guard in the new generation's library, and the metrics follow it
// (serve passes the active generation's library). A nil library reads as zero.
func RegisterMountMetrics(reg prometheus.Registerer, current func() *Library) {
	mount := func() *Mount {
		if l := current(); l != nil {
			return l.mount
		}
		return nil
	}
	state := func(f func(BreakerState) float64) func() float64 {
		return func() float64 {
			if m := mount(); m != nil {
				return f(m.Breaker())
			}
			return 0
		}
	}
	helper := func(f func(HelperState) float64) func() float64 {
		return func() float64 {
			if m := mount(); m != nil && m.pool != nil {
				return f(m.pool.state())
			}
			return 0
		}
	}
	reg.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "theatarr_library_mount_breaker_open",
			Help: "1 while the mount circuit breaker refuses mount calls (open or half open), else 0."},
			state(func(s BreakerState) float64 {
				if s.Open() {
					return 1
				}
				return 0
			})),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "theatarr_library_mount_calls_outstanding",
			Help: "Mount calls running, including those still blocked after their timeout."},
			state(func(s BreakerState) float64 { return float64(s.Outstanding) })),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "theatarr_library_mount_timeouts_total",
			Help: "Mount calls that did not answer in time."},
			state(func(s BreakerState) float64 { return float64(s.Timeouts) })),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "theatarr_library_mount_breaker_opens_total",
			Help: "Times the mount circuit breaker opened."},
			state(func(s BreakerState) float64 { return float64(s.Opens) })),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "theatarr_library_mount_helper_starts_total",
			Help: "Mount helper processes started."}, helper(func(h HelperState) float64 { return float64(h.Starts) })),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "theatarr_library_mount_helper_kills_total",
			Help: "Mount helper processes killed because a call did not answer in time."},
			helper(func(h HelperState) float64 { return float64(h.Kills) })),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "theatarr_library_mount_helper_crashes_total",
			Help: "Mount helper processes that ended mid-call or could not start."},
			helper(func(h HelperState) float64 { return float64(h.Crashes) })),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "theatarr_library_mount_helpers_running",
			Help: "Mount helper processes running."}, helper(func(h HelperState) float64 { return float64(h.Running) })),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "theatarr_library_mount_helpers_lingering",
			Help: "Killed mount helper processes still there (a call stuck in the kernel)."},
			helper(func(h HelperState) float64 { return float64(h.Lingering) })),
	)
}
