package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/policy"
)

// slowFS is a mount that stops answering: while hung, every call blocks until released, like a stat on a dead FUSE
// mount. It counts the calls that reached it and the most in flight at once.
type slowFS struct {
	mu       sync.Mutex
	hung     bool
	release  chan struct{}
	calls    atomic.Int64
	inFlight atomic.Int64
	maxIn    atomic.Int64
}

func newSlowFS() *slowFS { return &slowFS{release: make(chan struct{})} }

func (f *slowFS) hang() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hung, f.release = true, make(chan struct{})
}

// answer releases the blocked calls and answers from now on.
func (f *slowFS) answer() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hung {
		close(f.release)
	}
	f.hung = false
}

func (f *slowFS) wait() {
	f.calls.Add(1)
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		m := f.maxIn.Load()
		if n <= m || f.maxIn.CompareAndSwap(m, n) {
			break
		}
	}
	f.mu.Lock()
	hung, ch := f.hung, f.release
	f.mu.Unlock()
	if hung {
		<-ch
	}
}

func (f *slowFS) Stat(name string) (os.FileInfo, error)      { f.wait(); return os.Stat(name) }
func (f *slowFS) ReadDir(name string) ([]os.DirEntry, error) { f.wait(); return os.ReadDir(name) }
func (f *slowFS) Lstat(name string) (os.FileInfo, error)     { f.wait(); return os.Lstat(name) }
func (f *slowFS) Readlink(name string) (string, error)       { f.wait(); return os.Readlink(name) }
func (f *slowFS) EvalSymlinks(name string) (string, error) {
	f.wait()
	return filepath.EvalSymlinks(name)
}

func slowMount(t *testing.T, fs *slowFS, now *time.Time) *Mount {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "__all__"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Mount{Path: dir, AllDir: "__all__", StatTimeout: 20 * time.Millisecond, MaxCalls: 2, BreakAfter: 3,
		Cooldown: time.Minute, Now: func() time.Time { return *now }, fs: fs}
}

// Repeated timeouts open the circuit: later calls fail at once without reaching the mount, and blocked calls stay
// bounded. After the cooldown one call goes through; an answer closes the circuit.
func TestMountBreakerOpensAndRecovers(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	fs := newSlowFS()
	m := slowMount(t, fs, &now)
	defer fs.answer()
	if err := m.Healthy(ctx); err != nil {
		t.Fatal(err)
	}
	fs.hang()
	for i := range 3 {
		if err := m.Healthy(ctx); !errors.Is(err, ErrMountDown) {
			t.Fatalf("call %d on a hung mount: %v", i, err)
		}
	}
	b := m.Breaker()
	if b.State != BreakerOpen || b.Consecutive != 3 || b.Timeouts != 3 || b.Opens != 1 || !b.Open() {
		t.Fatalf("%+v, want open after 3 timeouts", b)
	}
	// Two calls are blocked in the "kernel"; the third waited for a slot and never reached the mount.
	if b.Outstanding != 2 || fs.calls.Load() != 3 || fs.maxIn.Load() != 2 {
		t.Errorf("outstanding %d, calls %d, max in flight %d; want 2, 3 (the first answered), 2", b.Outstanding,
			fs.calls.Load(), fs.maxIn.Load())
	}
	start := time.Now()
	for range 20 {
		if _, err := m.ReadDir(ctx, m.AllPath()); !errors.Is(err, ErrMountDown) {
			t.Fatalf("open circuit: %v", err)
		}
	}
	if time.Since(start) > 15*time.Millisecond || fs.calls.Load() != 3 {
		t.Errorf("an open circuit still called the mount (calls %d, took %v)", fs.calls.Load(), time.Since(start))
	}
	// The quiet period ends; the mount answers again.
	now = now.Add(time.Minute)
	if m.Breaker().State != BreakerHalfOpen {
		t.Errorf("after the cooldown: %+v", m.Breaker())
	}
	fs.answer()
	waitFor(t, func() bool { return m.Breaker().Outstanding == 0 })
	if err := m.Healthy(ctx); err != nil {
		t.Fatalf("half-open call on a live mount: %v", err)
	}
	if b := m.Breaker(); b.State != BreakerClosed || b.Consecutive != 0 {
		t.Errorf("%+v, want closed", b)
	}
}

// The call let through after the cooldown times out too: the circuit opens for another cooldown.
func TestMountBreakerHalfOpenProbeFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	fs := newSlowFS()
	m := slowMount(t, fs, &now)
	m.MaxCalls = 10
	defer fs.answer()
	fs.hang()
	for range 3 {
		_ = m.Healthy(ctx)
	}
	now = now.Add(time.Minute)
	if err := m.Healthy(ctx); !errors.Is(err, ErrMountDown) {
		t.Fatal(err)
	}
	b := m.Breaker()
	if b.State != BreakerOpen || !b.OpenedAt.Equal(now) || b.Opens != 1 || fs.calls.Load() != 4 {
		t.Errorf("%+v, calls %d; want open again from now, one call let through", b, fs.calls.Load())
	}
	if err := m.Healthy(ctx); !errors.Is(err, ErrMountDown) || fs.calls.Load() != 4 {
		t.Errorf("open again: %v, calls %d", err, fs.calls.Load())
	}
}

// Many concurrent callers on a hung mount: at most MaxCalls calls ever block in the kernel.
func TestMountCallsBounded(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	fs := newSlowFS()
	m := slowMount(t, fs, &now)
	m.BreakAfter = 1000
	defer fs.answer()
	fs.hang()
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.ReadDir(context.Background(), m.AllPath())
		}()
	}
	wg.Wait()
	if fs.maxIn.Load() > 2 || m.Breaker().Outstanding > 2 {
		t.Errorf("max in flight %d, outstanding %d; want at most 2", fs.maxIn.Load(), m.Breaker().Outstanding)
	}
}

// The library's passes on a hung mount: each fails with ErrMountDown; once the circuit is open they stop at once,
// without touching the mount, and the state is exported for health checks.
func TestLibraryBacksOffWhileMountBreakerOpen(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	fs := newSlowFS()
	e.lib.mount.fs, e.lib.mount.StatTimeout = fs, 20*time.Millisecond
	defer fs.answer()
	fs.hang()
	for range DefaultMountBreakAfter {
		if _, err := e.lib.Import(e.ctx, 1, false); !errors.Is(err, ErrMountDown) {
			t.Fatalf("import on a hung mount: %v", err)
		}
	}
	if b := e.lib.MountBreaker(); b.State != BreakerOpen {
		t.Fatalf("%+v", b)
	}
	calls := fs.calls.Load()
	if _, err := e.lib.Relink(e.ctx, "", false); !errors.Is(err, ErrMountDown) {
		t.Errorf("relink: %v", err)
	}
	if _, err := e.lib.DanglingReport(e.ctx); !errors.Is(err, ErrMountDown) {
		t.Errorf("dangling: %v", err)
	}
	if n, err := e.lib.Run(e.ctx); !errors.Is(err, ErrMountDown) || n != 0 {
		t.Errorf("import pass: %d %v", n, err)
	}
	if fs.calls.Load() != calls {
		t.Errorf("passes reached the mount while the circuit was open (%d calls)", fs.calls.Load()-calls)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "ready" {
		t.Errorf("download %s: an outage must not fail it", got)
	}
}

// A probe that hangs is a mount outage, not a verdict on the file: the import pauses and nothing is rejected.
func TestHungProbePausesImport(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	old := probeTimeout
	probeTimeout = 20 * time.Millisecond
	defer func() { probeTimeout = old }()
	release := make(chan struct{})
	defer close(release)
	inner := e.lib.probe
	e.lib.probe = func(p string, n int64) (*probe.Result, error) {
		<-release
		return inner(p, n)
	}
	if _, err := e.lib.Import(e.ctx, 1, false); !errors.Is(err, ErrMountDown) {
		t.Fatalf("import with a hung probe: %v", err)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "ready" {
		t.Errorf("download %s", got)
	}
	if got := e.one(`SELECT count(*) FROM library_import WHERE state = 'rejected'`); got != "0" {
		t.Errorf("%s rejected imports", got)
	}
}

// A settings generation for the same mount keeps the guard, the largest listing and the helpers of the generation
// it replaces, and its own mount settings: a changed release directory or stat timeout applies at once.
func TestInheritMountKeepsStateNotSettings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lib := func(allDir string, timeout time.Duration, path string) *Library {
		pol := &policy.Policy{Library: &policy.Library{Mount: policy.LibraryMount{Path: path, AllDir: allDir,
			StatTimeout: policy.Duration(timeout)}}}
		return New(Options{Policy: pol, MountHelper: &MountHelper{Exe: "/bin/false"}})
	}
	mountPath := filepath.Join(dir, "zurg")
	first := lib("old", time.Second, mountPath)
	g := first.mount.guard()
	g.mu.Lock()
	g.open, g.openedAt, g.now = true, time.Now(), time.Now
	g.mu.Unlock()
	if err := first.mount.checkListing(100, 0); err != nil {
		t.Fatal(err)
	}
	second := lib("old", 9*time.Second, mountPath)
	second.InheritMount(first)
	third := lib("new", 7*time.Second, mountPath)
	third.InheritMount(second)
	if third.mount.AllDir != "new" || third.mount.StatTimeout != 7*time.Second || third.mount.AllPath() != filepath.Join(mountPath, "new") {
		t.Errorf("settings not applied: %+v", third.mount)
	}
	if !third.MountBreaker().Open() || third.mount.pool != first.mount.pool || third.mount.pool == nil {
		t.Errorf("guard or helpers not carried over: %+v", third.MountBreaker())
	}
	// The listing floor is per release root: "old"'s carries to second, "new" starts empty.
	if err := second.mount.checkListing(10, 0); !errors.Is(err, ErrMountDown) {
		t.Errorf("old root floor lost: %v", err)
	}
	if err := third.mount.checkListing(10, 0); err != nil {
		t.Errorf("new root: %v", err)
	}
	moved := lib("new", time.Second, filepath.Join(dir, "other"))
	moved.InheritMount(third)
	if moved.MountBreaker().Open() || moved.mount.pool == third.mount.pool {
		t.Error("a moved mount kept the old guard")
	}
}
