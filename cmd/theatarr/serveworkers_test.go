package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// fakeWorker records its runs: how many started, how many ran at once, and the prev key each start got.
type fakeWorker struct {
	mu      sync.Mutex
	keys    map[int64]string
	starts  []string // "gen:prev"
	running atomic.Int32
	maxRun  atomic.Int32
	// hold makes a run ignore its cancel until released.
	hold chan struct{}
	fail error
}

func (f *fakeWorker) spec(name string) serveWorker {
	return serveWorker{name: name, key: func(rt *serveRuntime) string {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.keys[rt.Generation]
	}, run: func(ctx context.Context, rt *serveRuntime, prev string) error {
		n := f.running.Add(1)
		defer f.running.Add(-1)
		for {
			m := f.maxRun.Load()
			if n <= m || f.maxRun.CompareAndSwap(m, n) {
				break
			}
		}
		f.mu.Lock()
		f.starts = append(f.starts, keyStart(rt.Generation, prev))
		fail, hold := f.fail, f.hold
		f.mu.Unlock()
		if fail != nil {
			return fail
		}
		<-ctx.Done()
		if hold != nil {
			<-hold
		}
		return nil
	}}
}

func keyStart(gen int64, prev string) string { return string(rune('0'+gen)) + ":" + prev }

func (f *fakeWorker) startsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.starts...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func workerStatus(s *serveWorkers, name string) serveWorkerStatus {
	for _, st := range s.Status() {
		if st.Name == name {
			return st
		}
	}
	return serveWorkerStatus{}
}

// A worker restarts once when its own settings change, never runs twice at once, and keeps running through a save
// that leaves its settings alone.
func TestServeWorkerRestartsOnlyOnItsSettings(t *testing.T) {
	t.Parallel()
	f := &fakeWorker{keys: map[int64]string{1: "every 5m", 2: "every 5m", 3: "every 10m"}}
	s := newServeWorkers(context.Background(), quietLog(), f.spec("library"))
	defer s.Close()
	s.Start(&serveRuntime{Generation: 1})
	waitFor(t, "first start", func() bool { return len(f.startsSeen()) == 1 })

	// An unrelated section's save: same key, the run continues on the newer generation.
	s.Apply(&serveRuntime{Generation: 2})
	waitFor(t, "generation 2 recorded", func() bool { return workerStatus(s, "library").Generation == 2 })
	if got := f.startsSeen(); len(got) != 1 {
		t.Fatalf("an unrelated save restarted the worker: %v", got)
	}

	// Its own section changes: stopped, then started once on the new settings with the old key as prev.
	s.Apply(&serveRuntime{Generation: 3})
	waitFor(t, "restart", func() bool { return len(f.startsSeen()) == 2 })
	if got := f.startsSeen(); got[1] != keyStart(3, "every 5m") {
		t.Fatalf("restart %v", got)
	}
	if st := workerStatus(s, "library"); !st.Running || st.Starts != 2 || st.Generation != 3 {
		t.Fatalf("status %+v", st)
	}
	if f.maxRun.Load() != 1 {
		t.Fatalf("%d runs at once", f.maxRun.Load())
	}
	if r := s.Health(context.Background()); r.Type != health.OK {
		t.Fatalf("health %+v", r)
	}
	s.Close()
	if f.running.Load() != 0 {
		t.Fatal("Close returned with a run going")
	}
}

// Quick saves while a run is stopping collapse into one start on the newest generation, after the old run ended.
func TestServeWorkerCollapsesSavesAndWaitsForStop(t *testing.T) {
	hold := make(chan struct{})
	f := &fakeWorker{keys: map[int64]string{1: "a", 2: "b", 3: "c", 4: "d"}, hold: hold}
	s := newServeWorkers(context.Background(), quietLog(), f.spec("shadow"))
	s.stuckAfter = 20 * time.Millisecond
	defer s.Close()
	s.Start(&serveRuntime{Generation: 1})
	waitFor(t, "first start", func() bool { return len(f.startsSeen()) == 1 })
	s.Apply(&serveRuntime{Generation: 2})
	// The first run ignores its cancel: the restart waits, and health says the worker is not stopping.
	waitFor(t, "stuck", func() bool { return workerStatus(s, "shadow").Stuck })
	if r := s.Health(context.Background()); r.Type != health.Warning {
		t.Fatalf("health while stuck %+v", r)
	}
	s.Apply(&serveRuntime{Generation: 3})
	s.Apply(&serveRuntime{Generation: 4})
	if got := f.startsSeen(); len(got) != 1 {
		t.Fatalf("started while the old run was going: %v", got)
	}
	close(hold)
	waitFor(t, "restart", func() bool { return len(f.startsSeen()) >= 2 })
	waitFor(t, "settled", func() bool { return workerStatus(s, "shadow").Generation == 4 })
	time.Sleep(20 * time.Millisecond)
	got := f.startsSeen()
	// Generation 2 was taken before the stop; 3 and 4 collapse into one start on 4.
	if len(got) != 3 || got[1] != keyStart(2, "a") || got[2] != keyStart(4, "b") {
		t.Fatalf("starts %v", got)
	}
	if f.maxRun.Load() != 1 {
		t.Fatalf("%d runs at once", f.maxRun.Load())
	}
	if r := s.Health(context.Background()); r.Type != health.OK {
		t.Fatalf("health after stopping %+v", r)
	}
}

// A run that fails on its new settings is visible in health and the next save starts it again; an empty key stops it.
func TestServeWorkerFailedStartRetriedAndOff(t *testing.T) {
	t.Parallel()
	f := &fakeWorker{keys: map[int64]string{1: "on", 2: "on", 3: ""}, fail: errors.New("catalog not ready")}
	s := newServeWorkers(context.Background(), quietLog(), f.spec("cardigann"))
	defer s.Close()
	s.Start(&serveRuntime{Generation: 1})
	waitFor(t, "failure recorded", func() bool { return workerStatus(s, "cardigann").Error != "" })
	if r := s.Health(context.Background()); r.Type != health.Error || r.Detail.(map[string]any)["cardigann"] != "catalog not ready" {
		t.Fatalf("health %+v", r)
	}
	f.mu.Lock()
	f.fail = nil
	f.mu.Unlock()
	s.Apply(&serveRuntime{Generation: 2})
	waitFor(t, "retry", func() bool { return len(f.startsSeen()) == 2 && workerStatus(s, "cardigann").Running })
	if st := workerStatus(s, "cardigann"); st.Error != "" {
		t.Fatalf("error kept after a good start: %+v", st)
	}
	s.Apply(&serveRuntime{Generation: 3})
	waitFor(t, "off", func() bool { st := workerStatus(s, "cardigann"); return !st.Running && st.Generation == 3 })
	if f.running.Load() != 0 {
		t.Fatal("worker still running with its section off")
	}
}

// A worker that returns by itself (nothing to do on its settings) is not started again by an unrelated save.
func TestServeWorkerExitedStaysUntilItsSettingsChange(t *testing.T) {
	t.Parallel()
	var runs atomic.Int32
	keys := map[int64]string{1: "off-by-itself", 2: "off-by-itself", 3: "tmdb set"}
	s := newServeWorkers(context.Background(), quietLog(), serveWorker{name: "art",
		key: func(rt *serveRuntime) string { return keys[rt.Generation] },
		run: func(ctx context.Context, rt *serveRuntime, _ string) error {
			runs.Add(1)
			if rt.Generation == 3 {
				<-ctx.Done()
			}
			return nil
		}})
	defer s.Close()
	s.Start(&serveRuntime{Generation: 1})
	waitFor(t, "first run returned", func() bool { return runs.Load() == 1 && !workerStatus(s, "art").Running })
	s.Apply(&serveRuntime{Generation: 2})
	waitFor(t, "generation 2", func() bool { return workerStatus(s, "art").Generation == 2 })
	if runs.Load() != 1 {
		t.Fatalf("unrelated save ran it again: %d", runs.Load())
	}
	s.Apply(&serveRuntime{Generation: 3})
	waitFor(t, "started on its new settings", func() bool { return runs.Load() == 2 && workerStatus(s, "art").Running })
}

// Publications before Start are ignored (the startup runtime is published before the loops are wired) and a nil
// supervisor (tests and commands without serve) is safe to use.
func TestServeWorkersBeforeStartAndNil(t *testing.T) {
	f := &fakeWorker{keys: map[int64]string{1: "x", 2: "x"}}
	s := newServeWorkers(context.Background(), quietLog(), f.spec("poll"))
	s.Apply(&serveRuntime{Generation: 1})
	time.Sleep(10 * time.Millisecond)
	if len(f.startsSeen()) != 0 {
		t.Fatal("started before Start")
	}
	s.Start(&serveRuntime{Generation: 2})
	waitFor(t, "start", func() bool { return len(f.startsSeen()) == 1 })
	s.Close()
	var none *serveWorkers
	none.Apply(&serveRuntime{})
	none.Start(&serveRuntime{})
	none.Close()
	(&serveRuntime{}).AfterPublish()
}

// A worker that panics is recorded in the error inbox with its stack and its status shows the error; the
// supervisor keeps running and starts it again on its next settings.
func TestServeWorkerPanicRecorded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	in := errinbox.New(st, errinbox.Options{Version: "test"})
	errinbox.Install(in)
	defer errinbox.Install(nil)

	var runs atomic.Int32
	keys := map[int64]string{1: "a", 2: "b"}
	s := newServeWorkers(ctx, quietLog(), serveWorker{name: "watch",
		key: func(rt *serveRuntime) string { return keys[rt.Generation] },
		run: func(ctx context.Context, rt *serveRuntime, _ string) error {
			runs.Add(1)
			if rt.Generation == 1 {
				panic("watch exploded")
			}
			<-ctx.Done()
			return nil
		}})
	defer s.Close()
	s.Start(&serveRuntime{Generation: 1})
	waitFor(t, "panic recorded in the status", func() bool { return workerStatus(s, "watch").Error != "" })
	s.Apply(&serveRuntime{Generation: 2})
	waitFor(t, "started again", func() bool { return runs.Load() == 2 && workerStatus(s, "watch").Running })
	if err := in.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := errinbox.List(ctx, st, errinbox.Filter{Source: errinbox.SourcePanic})
	if err != nil || len(p.Items) != 1 {
		t.Fatalf("panic rows: %v %+v", err, p)
	}
	row, err := errinbox.Get(ctx, st, p.Items[0].ID)
	if err != nil || row.Component != "live-settings" || !strings.Contains(row.Sample, "watch exploded") ||
		!strings.Contains(row.Stack, "TestServeWorkerPanicRecorded") || !strings.Contains(string(row.Context), `"worker":"watch"`) {
		t.Fatalf("panic row: %v %+v", err, row)
	}
}

// The search round worker waits while router.enabled is false (Pause) and is off with no Live library; turning Pause
// off changes its key from "paused", which starts the first round at once.
func TestWantedWorkerFollowsTheRouterSwitch(t *testing.T) {
	var key func(*serveRuntime) string
	for _, w := range (serveLoops{}).workers() {
		if w.name == "wanted" {
			key = w.key
		}
	}
	if key == nil {
		t.Fatal("no wanted worker")
	}
	rt := &serveRuntime{Acquirer: &acquire.Acquirer{}, Policy: &policy.Policy{Router: &policy.Router{}}}
	if k := key(rt); k != "" {
		t.Fatalf("no Live library: key %q", k)
	}
	rt.Policy.Versions = map[string]*policy.VersionPolicy{"movie": {Mode: policy.ModeActive}}
	if k := key(rt); k != "paused" {
		t.Fatalf("paused key %q", k)
	}
	rt.Policy.Router.Enabled = true
	if k := key(rt); k != "on" {
		t.Fatalf("key %q with downloads on", k)
	}
}
