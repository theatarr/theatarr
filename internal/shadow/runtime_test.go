package shadow

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/importarr"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

type fixedRuntimeSource struct{ generation int64 }

func (s fixedRuntimeSource) CurrentGeneration() int64 { return s.generation }
func (fixedRuntimeSource) Resolve(context.Context, int64) (Runtime, bool) {
	return Runtime{}, false
}

func TestQueueRSSDoesNotPersistRuntimeGeneration(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.r.runtime = fixedRuntimeSource{generation: 12}
	if err := e.r.QueueRSS(e.ctx); err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT payload FROM job WHERE kind = ?`, KindRSS).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, `"generation"`) {
		t.Fatalf("RSS job persisted a generation: %s", payload)
	}
}

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
	for _, version := range currentPolicy.Versions {
		version.Mode = ""
	}
	e.r.set.RSS = false // no feed was eligible when the long-lived scheduler started
	e.r.versions = map[string]bool{}
	e.r.active = map[string]bool{}
	source := &runtimeSnapshotSource{runtime: Runtime{Generation: 1, Policy: currentPolicy, Engine: e.eng,
		Indexers: e.svc}}
	e.r.runtime = source

	active, err := e.r.forOperation(e.ctx)
	if err != nil || active.set.RSS || len(active.rssVersions()) != 0 {
		t.Fatalf("disabled startup runtime unexpectedly enabled RSS: active=%v err=%v", active, err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() {
		e.r.Run(ctx, time.Hour, 0)
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
	currentPolicy.Versions["movie-remux"].Mode = policy.ModeShadow
	source.set(Runtime{Generation: 2, Policy: currentPolicy, Engine: e.eng, Indexers: e.svc})
	var count int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT COUNT(*) FROM job WHERE kind = ?`, KindRSS).Scan(&count); err != nil {
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
		t.Fatal("RSS scheduler did not enqueue promptly after the runtime enabled its first feed")
	}
	if _, err := e.st.Writer().ExecContext(e.ctx, `DELETE FROM job WHERE kind = ?`, KindRSS); err != nil {
		t.Fatal(err)
	}
	off = false
	currentPolicy.Shadow = &policy.Shadow{RSS: &off}
	for _, version := range currentPolicy.Versions {
		version.Mode = ""
	}
	source.set(Runtime{Generation: 3, Policy: currentPolicy, Engine: e.eng, Indexers: e.svc})
	ctx2, cancel2 := context.WithCancel(e.ctx)
	done2 := make(chan struct{})
	go func() {
		e.r.Run(ctx2, time.Hour, 0)
		close(done2)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel2()
	<-done2
	var idle int
	if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT COUNT(*) FROM job WHERE kind = ?`, KindRSS).Scan(&idle); err != nil {
		t.Fatal(err)
	}
	if idle != 0 {
		t.Fatal("RSS scheduler queued work after all feeds were disabled")
	}
}

func TestSearchCacheIsScopedToRuntimeGeneration(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	it := indexer.Title{Kind: policy.KindMovie, Title: dune.title, Year: dune.year, IMDb: dune.imdb, TMDb: dune.tmdb}
	e.r.generation = 1
	if _, err := e.r.searchTitle(e.ctx, it, "same-title", "movie-remux"); err != nil {
		t.Fatal(err)
	}
	first := len(e.net.requests("prowlarr.test/1/api?"))
	if first == 0 {
		t.Fatal("first generation did not search its source")
	}
	e.r.generation = 2
	if _, err := e.r.searchTitle(e.ctx, it, "same-title", "movie-remux"); err != nil {
		t.Fatal(err)
	}
	second := len(e.net.requests("prowlarr.test/1/api?"))
	if second <= first {
		t.Fatalf("generation 2 reused generation 1 results: requests %d -> %d", first, second)
	}
}

// An import section saved after start schedules arr.import when the schedule starts again (theatarr serve restarts it
// on that change), with the arr clients of the generation in force.
func TestScheduleUsesCurrentRuntimeImport(t *testing.T) {
	e := newEnv(t, true)
	e.r.arr = nil // no import.arr when the runner was built
	if e.r.pol.Import != nil {
		e.r.pol.Import = nil
	}
	currentPolicy, err := e.r.pol.Clone()
	if err != nil {
		t.Fatal(err)
	}
	currentPolicy.Import = &policy.Import{Arr: map[string]*policy.ArrSource{"radarr": {URL: "http://radarr.example:7878",
		APIKeyEnv: "THEATARR_TEST_UNSET_KEY", Flavour: "radarr", Version: "movie-remux"}}}
	e.r.runtime = &runtimeSnapshotSource{runtime: Runtime{Generation: 2, Policy: currentPolicy, Engine: e.eng, Indexers: e.svc,
		Arr: func(string) (importarr.Getter, error) { return nil, context.Canceled }}}
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() {
		e.r.Schedule(ctx, 0, time.Hour)
		close(done)
	}()
	var count int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT COUNT(*) FROM job WHERE kind = ?`, KindImport).Scan(&count); err != nil {
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
		t.Fatal("arr.import not scheduled from the current runtime's import section")
	}
}
