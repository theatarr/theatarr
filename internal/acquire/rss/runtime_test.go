package rss

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

type runtimeSnapshotSource struct {
	mu      sync.RWMutex
	runtime Runtime
}

func (s *runtimeSnapshotSource) Resolve(context.Context, int64) (Runtime, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.runtime, true
}

func (s *runtimeSnapshotSource) set(runtime Runtime) {
	s.mu.Lock()
	s.runtime = runtime
	s.mu.Unlock()
}

func TestRSSSchedulerUsesCurrentRuntimeEligibility(t *testing.T) {
	e := newEnv(t, true)
	currentPolicy, err := e.r.pol.Clone()
	if err != nil {
		t.Fatal(err)
	}
	off := false
	currentPolicy.Shadow = &policy.Shadow{RSS: &off}
	e.r.set.RSS = false // no feed was eligible when the long-lived scheduler started
	e.r.active = map[string]bool{}
	source := &runtimeSnapshotSource{runtime: Runtime{Generation: 1, Policy: currentPolicy, Engine: e.eng,
		Indexers: e.svc}}
	e.r.runtime = source

	active, err := e.r.forOperation(e.ctx)
	if err != nil || active.set.RSS || len(active.active) != 0 {
		t.Fatalf("disabled startup runtime unexpectedly enabled RSS: active=%v err=%v", active, err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() {
		e.r.Schedule(ctx, 0)
		close(done)
	}()
	time.Sleep(25 * time.Millisecond)
	// A new generation is a new policy value: the running scheduler may still be reading generation 1's.
	currentPolicy, err = currentPolicy.Clone()
	if err != nil {
		t.Fatal(err)
	}
	on := true
	currentPolicy.Shadow = &policy.Shadow{RSS: &on}
	source.set(Runtime{Generation: 2, Policy: currentPolicy, Engine: e.eng, Indexers: e.svc, Router: e.rec})
	var count int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT COUNT(*) FROM task WHERE key = ? AND last_started IS NOT NULL`, KindRSS).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if count == 0 {
		t.Fatal("RSS scheduler did not read promptly after the runtime enabled its first feed")
	}
	if _, err := e.st.Writer().ExecContext(e.ctx, `UPDATE task SET last_started = NULL WHERE key = ?`, KindRSS); err != nil {
		t.Fatal(err)
	}
	off = false
	currentPolicy.Shadow = &policy.Shadow{RSS: &off}
	source.set(Runtime{Generation: 3, Policy: currentPolicy, Engine: e.eng, Indexers: e.svc})
	ctx2, cancel2 := context.WithCancel(e.ctx)
	done2 := make(chan struct{})
	go func() {
		e.r.Schedule(ctx2, 0)
		close(done2)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel2()
	<-done2
	var idle int
	if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT COUNT(*) FROM task WHERE key = ? AND last_started IS NOT NULL`, KindRSS).Scan(&idle); err != nil {
		t.Fatal(err)
	}
	if idle != 0 {
		t.Fatal("RSS scheduler read after all feeds were disabled")
	}
}

// A profile saved and assigned after start decides the next RSS run: the run decides with the engine of the
// settings generation it resolved, not the startup engine.
func TestRSSDecidesWithCurrentRuntimeEngine(t *testing.T) {
	e := newEnv(t, false)
	e.seed(t, alien)
	current, err := e.r.pol.Clone()
	if err != nil {
		t.Fatal(err)
	}
	current.Profiles["web-1080p"] = &policy.Profile{Qualities: []policy.QualityItem{{Name: "WEBDL-1080p"}}, Cutoff: "WEBDL-1080p"}
	current.Versions["movie-web"].Profile = "web-1080p"
	eng, err := decide.New(current, decide.Options{Now: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	eng.SetLibrary(e.eng.Library())
	e.r.runtime = &runtimeSnapshotSource{runtime: Runtime{Generation: 2, Policy: current, Engine: eng, Indexers: e.svc,
		Budgets: e.gw, Router: e.rec}}
	e.prow.setRSS(item{title: alien1080p, hash: hAlienWeb, size: 8e9, pub: t0})
	rs, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Errors != 0 || len(e.rec.reqs) != 1 || e.rec.reqs[0].Version != "movie-web" {
		t.Fatalf("rss %+v routed %+v", rs, e.rec.reqs)
	}
}
