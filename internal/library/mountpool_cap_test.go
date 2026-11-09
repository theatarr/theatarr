package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Killed helpers that do not go (a call stuck in the kernel) count toward a cap on processes: at the cap a call
// fails at once as the mount down, and no helper is started.
func TestMountHelperLingeringCap(t *testing.T) {
	m := helperMount(t, 500*time.Millisecond)
	m.BreakAfter = 100
	m.pool.cfg.Size, m.pool.cfg.Max = 1, 2
	var alive atomic.Bool
	alive.Store(true)
	m.pool.mu.Lock()
	m.pool.reapFn = func(p *os.Process) bool { return !alive.Load() && reapProcess(p) }
	m.pool.mu.Unlock()
	t.Cleanup(func() { alive.Store(false) })
	ctx := context.Background()
	hang := filepath.Join(m.AllPath(), "HANG")
	for range 2 {
		if _, err := m.Stat(ctx, hang); !errors.Is(err, ErrMountDown) {
			t.Fatalf("hung call: %v", err)
		}
	}
	h := m.Breaker().Helper
	if h.Lingering != 2 || h.Running != 0 || h.Starts != 2 || h.Max != 2 {
		t.Fatalf("helper state %+v", h)
	}
	start := time.Now()
	if _, err := m.Stat(ctx, m.AllPath()); !errors.Is(err, ErrMountDown) {
		t.Fatalf("call at the cap: %v", err)
	}
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Errorf("call at the cap took %v", took)
	}
	if h := m.Breaker().Helper; h.Starts != 2 {
		t.Errorf("helper started at the cap: %+v", h)
	}
	// Once they go, helpers start again.
	alive.Store(false)
	waitUntil(t, func() bool { return m.pool.state().Lingering == 0 })
	time.Sleep(300 * time.Millisecond) // the back-off after two kills
	if _, err := m.Stat(ctx, m.AllPath()); err != nil {
		t.Errorf("after the reap: %v", err)
	}
}
