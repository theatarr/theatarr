package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

// liveFixture is a settings database with every section seeded (as theatarr serve's first start does) and a runtime
// builder like serve's prepare: the generation's services, its library and metadata, and the worker supervisor.
type liveFixture struct {
	db      *store.Store
	st      *settings.Store
	base    *policy.Policy
	initial *settings.Snapshot
	q       *jobs.Queue
	log     *slog.Logger
	deps    *serveMetadataDeps
}

func newLiveFixture(t *testing.T, edits ...func(*policy.Policy)) *liveFixture {
	t.Helper()
	// The metadata keys come from the environment fallback; the fixture runs with none set.
	for _, env := range []string{"THEATARR_TMDB_KEY", "THEATARR_TVDB_KEY", "THEATARR_PLEX_TOKEN"} {
		t.Setenv(env, "")
	}
	db, st, base, _ := runtimeFixture(t, edits...)
	ctx := context.Background()
	if _, err := st.BootstrapSwitches(ctx, base, "test", nil); err != nil {
		t.Fatal(err)
	}
	initial, err := st.BootstrapSections(ctx, base, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := jobs.NewQueue(db)
	return &liveFixture{db: db, st: st, base: base, initial: initial, q: q, log: log,
		deps: &serveMetadataDeps{st: db, q: q, dbPath: filepath.Join(t.TempDir(), "theatarr.db"), log: log}}
}

func (f *liveFixture) build(ctx context.Context, snap *settings.Snapshot, prev *serveRuntime, workers *serveWorkers) (*serveRuntime, error) {
	rt, err := prepareServeRuntime(ctx, snap, prev, f.db, f.q, nil, f.log)
	if err != nil {
		return nil, err
	}
	if rt.Library, err = newServeLibrary(rt.Policy, f.db, f.q, rt.Gateway, f.log, rt.Engine, nil, nil, nil); err != nil {
		return nil, err
	}
	var prevMeta *serveMetadata
	if prev != nil {
		prevMeta = prev.Metadata
	}
	if rt.Metadata, err = prepareServeMetadata(prevMeta, rt.Policy, rt.Gateway, f.deps); err != nil {
		return nil, err
	}
	rt.Acquirer, rt.Workers = &acquire.Acquirer{}, workers
	return rt, nil
}

// recordingWorkers are serve's workers with their real keys and a run that records each start.
type recordingWorkers struct {
	mu     sync.Mutex
	starts map[string]int
}

func (r *recordingWorkers) specs() []serveWorker {
	real := serveLoops{}.workers()
	out := make([]serveWorker, len(real))
	for i, w := range real {
		out[i] = serveWorker{name: w.name, key: w.key, run: func(ctx context.Context, _ *serveRuntime, _ string) error {
			r.mu.Lock()
			r.starts[w.name]++
			r.mu.Unlock()
			<-ctx.Done()
			return nil
		}}
	}
	return out
}

func (r *recordingWorkers) snapshot() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]int{}
	for k, v := range r.starts {
		out[k] = v
	}
	return out
}

// editSection changes one field of a stored section's config (policy field names, as the editor sends them).
func editSection(t *testing.T, r *settings.Records, key string, edit func(map[string]any)) {
	t.Helper()
	for i := range r.Sections {
		if r.Sections[i].Key != key {
			continue
		}
		cfg := map[string]any{}
		if len(r.Sections[i].Config) > 0 && string(r.Sections[i].Config) != "null" {
			if err := json.Unmarshal(r.Sections[i].Config, &cfg); err != nil {
				t.Fatal(err)
			}
		}
		edit(cfg)
		b, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		r.Sections[i].Config = b
		return
	}
	t.Fatalf("no section %s", key)
}

func child(m map[string]any, key string) map[string]any {
	c, _ := m[key].(map[string]any)
	if c == nil {
		c = map[string]any{}
		m[key] = c
	}
	return c
}

// A section save through the settings API path (SaveAndApply) restarts exactly the loops that section schedules, once
// each, and nothing else; a save of a section no loop reads restarts nothing.
func TestLiveSectionSaveRestartsOnlyItsWorkers(t *testing.T) {
	f := newLiveFixture(t, vpnGuardOn)
	ctx := context.Background()
	rec := &recordingWorkers{starts: map[string]int{}}
	workers := newServeWorkers(ctx, f.log, rec.specs()...)
	defer workers.Close()
	first, err := f.build(ctx, f.initial, nil, workers)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := settings.NewRuntimeCoordinator(f.st, f.base, f.initial, first,
		func(ctx context.Context, snap *settings.Snapshot, previous *settings.Runtime) (any, error) {
			var prev *serveRuntime
			if previous != nil {
				prev, _ = previous.Components.(*serveRuntime)
			}
			return f.build(ctx, snap, prev, workers)
		})
	if err != nil {
		t.Fatal(err)
	}
	workers.Start(first)
	settled := func(gen int64) {
		t.Helper()
		waitFor(t, "workers on the saved generation", func() bool {
			for _, st := range workers.Status() {
				if st.Generation != gen {
					return false
				}
			}
			return true
		})
	}
	settled(f.initial.Generation)
	before := rec.snapshot()
	for _, name := range []string{"library", "provider-sync", "router-poll", "shadow", "rss", "playlists", "art", "series-metadata"} {
		if before[name] != 1 {
			t.Fatalf("fixture: %s started %d times: %v", name, before[name], before)
		}
	}
	save := func(section string, edit func(map[string]any)) int64 {
		t.Helper()
		head, err := manager.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		req := settings.SaveRequest{ExpectedGeneration: head.Generation, Actor: "test", Records: head.Records}
		editSection(t, &req.Records, section, edit)
		if proof, err := manager.Confirm(ctx, req); err == nil {
			req.ConfirmationToken = proof.Token
		}
		saved, err := manager.SaveAndApply(ctx, req)
		if err != nil {
			t.Fatalf("save %s: %v", section, err)
		}
		settled(saved.Generation)
		return saved.Generation
	}
	cases := []struct {
		section string
		edit    func(map[string]any)
		want    []string
	}{
		{"notify", func(c map[string]any) { c["retries"] = 7 }, nil},
		// The periodic egress check is a settings worker keyed on its interval.
		{"egress", func(c map[string]any) { c["interval"] = "5m" }, []string{"egress"}},
		{"library", func(c map[string]any) { child(c, "mount")["poll_interval"] = "40s" }, []string{"library"}},
		{"router", func(c map[string]any) { c["poll_interval"] = "2m" }, []string{"router-poll"}},
		{"provider_sync", func(c map[string]any) { c["interval"] = "7h" }, []string{"provider-sync"}},
		{"shadow", func(c map[string]any) { c["import_interval"] = "20m" }, []string{"shadow"}},
		// The RSS schedule follows each generation by itself: an RSS change restarts no worker.
		{"shadow", func(c map[string]any) { c["rss_interval"] = "20m" }, nil},
		{"playlists", func(c map[string]any) { c["interval"] = "7h" }, []string{"playlists"}},
		{"metadata", func(c map[string]any) { child(c, "art")["interval"] = "7h" }, []string{"art"}},
		{"metadata", func(c map[string]any) { child(c, "series")["interval"] = "2h" }, []string{"series-metadata"}},
		{"ui", func(c map[string]any) { child(c, "images")["max_bytes"] = 64 << 20 }, []string{"art"}},
		{"cardigann", func(c map[string]any) { c["definitions"] = map[string]any{"refresh": "48h"} }, []string{"cardigann"}},
	}
	for _, tc := range cases {
		was := rec.snapshot()
		gen := save(tc.section, tc.edit)
		// A worker reports the new generation just before its run starts and is counted; wait for the count.
		waitFor(t, tc.section+" restarts counted", func() bool {
			now := rec.snapshot()
			for _, name := range tc.want {
				if now[name] <= was[name] {
					return false
				}
			}
			return true
		})
		now := rec.snapshot()
		var restarted []string
		for name, n := range now {
			if n != was[name] {
				if n != was[name]+1 {
					t.Fatalf("%s save: %s started %d times", tc.section, name, n-was[name])
				}
				restarted = append(restarted, name)
			}
		}
		sort.Strings(restarted)
		if strings.Join(restarted, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("%s save (generation %d) restarted %v, want %v", tc.section, gen, restarted, tc.want)
		}
	}
	if r := workers.Health(ctx); r.Type != health.OK {
		t.Fatalf("health %+v", r)
	}
	// The switches stay where they were: no section save turned grabbing, the library or notifications on.
	if p := manager.Current().Policy; p.RouterSettings().Enabled || p.LibrarySettings().Enabled || p.NotifySettings().Enabled {
		t.Fatalf("a section save turned a switch on: router=%t library=%t notify=%t", p.RouterSettings().Enabled,
			p.LibrarySettings().Enabled, p.NotifySettings().Enabled)
	}
}

// The metadata section, the ui section and the TMDb key apply on the next generation: services whose inputs changed
// are built again, the others (and their clients and loops) are kept.
func TestLiveMetadataServicesFollowTheirInputs(t *testing.T) {
	f := newLiveFixture(t)
	gw, err := gateway.New(context.Background(), f.base, gateway.Options{Store: f.db, Logger: f.log})
	if err != nil {
		t.Fatal(err)
	}
	pol := f.base
	off, err := prepareServeMetadata(nil, pol, gw, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if off.art.Enabled() || off.add.TMDb != nil || off.intake != nil || off.viewTMDb != nil || off.add.Collections != nil {
		t.Fatal("fixture: TMDb on without its key")
	}
	same, err := prepareServeMetadata(off, pol, gw, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if same.art != off.art || same.tv != off.tv || same.tvc != off.tvc {
		t.Fatal("an unchanged generation rebuilt the metadata services")
	}
	// A key saved for metadata.sources.tmdb: art, search and add, the views' lookups and the intake turn on.
	t.Setenv("THEATARR_TMDB_KEY", "tmdb-test-key")
	on, err := prepareServeMetadata(same, pol, gw, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if !on.art.Enabled() || on.add.TMDb == nil || on.viewTMDb == nil || on.intake == nil || on.art == same.art {
		t.Fatalf("TMDb key not applied: art=%t add=%t view=%t intake=%t", on.art.Enabled(), on.add.TMDb != nil,
			on.viewTMDb != nil, on.intake != nil)
	}
	if strings.Contains(on.tmdbKey+on.artKey, "tmdb-test-key") {
		t.Fatal("a metadata key holds the secret")
	}
	// ui.images: the art service (its image cache) is built again; the series refresh and the search client stay.
	next, err := pol.Clone()
	if err != nil {
		t.Fatal(err)
	}
	next.UI = &policy.UI{Images: &policy.ImagesConfig{MaxBytes: 64 << 20}}
	images, err := prepareServeMetadata(on, next, gw, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if images.art == on.art || images.tv != on.tv || images.add.TMDb != on.add.TMDb {
		t.Fatalf("ui.images rebuilt the wrong services: art kept=%t tv rebuilt=%t search rebuilt=%t", images.art == on.art,
			images.tv != on.tv, images.add.TMDb != on.add.TMDb)
	}
	// The intake reads the versions of its generation; its TMDb client is kept.
	if images.intake == on.intake || images.intake.Policy != next || images.intake.TMDb != on.intake.TMDb {
		t.Fatal("intake not rebuilt on its generation's policy")
	}
	// The Collections page gets the intake of its generation, though search and add's TMDb client was kept.
	if c, ok := images.add.Collections.(serveMovieIntake); !ok || c.MovieIntake != images.intake || on.add.Collections == nil {
		t.Fatal("the web layer's movie intake is not its generation's")
	}
}

// The cardigann section applies on save: the catalog is made when definitions are first set, and its source,
// FlareSolverr URL and Prowlarr reader are read from the settings in force; a second save keeps the catalog.
func TestLiveCardigannSectionAppliesOnSave(t *testing.T) {
	t.Cleanup(func() { serveCardigann.Store(nil) })
	serveCardigann.Store(nil)
	f := newLiveFixture(t)
	live, err := f.base.Clone()
	if err != nil {
		t.Fatal(err)
	}
	live.Cardigann = nil
	var mu sync.Mutex
	current := func(context.Context) (*gateway.Gateway, *policy.Policy, error) {
		mu.Lock()
		defer mu.Unlock()
		return nil, live, nil
	}
	dir := t.TempDir()
	if err := cardigannEnsure(live, dir, f.db, current, f.log); err != nil || serveCardigann.Load() != nil {
		t.Fatalf("catalog made without definitions: %v", err)
	}
	if cardigannWorkerKey(live) != "" {
		t.Fatal("refresh loop keyed on without definitions")
	}
	mu.Lock()
	live.Cardigann = &policy.Cardigann{Definitions: &policy.CardigannDefinitions{Repo: "example/defs"},
		FlareSolverrURL: "http://flare.example:8191"}
	mu.Unlock()
	if err := cardigannEnsure(live, dir, f.db, current, f.log); err != nil {
		t.Fatal(err)
	}
	c := serveCardigann.Load()
	if c == nil || cardigannManager() == nil {
		t.Fatal("catalog not made when definitions were saved")
	}
	ctx := context.Background()
	if got := c.source(ctx); got.Repo != "example/defs" || c.settings(ctx).FlareSolverrURL != "http://flare.example:8191" {
		t.Fatalf("source %+v", got)
	}
	if _, err := c.ProwlarrProposals(ctx); err != errNoProwlarr {
		t.Fatalf("prowlarr without a reader: %v", err)
	}
	key := cardigannWorkerKey(live)
	mu.Lock()
	live.Cardigann = &policy.Cardigann{Definitions: &policy.CardigannDefinitions{Repo: "example/other", Branch: "main"},
		Prowlarr: &policy.CardigannProwlarr{URL: "http://prowlarr.example:9696", APIKeyEnv: "THEATARR_TEST_UNSET_PROWLARR_KEY"}}
	mu.Unlock()
	if err := cardigannEnsure(live, dir, f.db, current, f.log); err != nil || serveCardigann.Load() != c {
		t.Fatalf("a second save replaced the catalog: %v", err)
	}
	if got := c.source(ctx); got.Repo != "example/other" || got.Branch != "main" || c.settings(ctx).FlareSolverrURL != "" {
		t.Fatalf("saved source not read: %+v", got)
	}
	if _, err := c.ProwlarrProposals(ctx); err == nil || !strings.Contains(err.Error(), "THEATARR_TEST_UNSET_PROWLARR_KEY") {
		t.Fatalf("saved Prowlarr reader not read: %v", err)
	}
	if cardigannWorkerKey(live) == key {
		t.Fatal("a changed source left the refresh loop running")
	}
}

// The import section applies on save: the arr clients come from the generation in force (an instance added later
// resolves, one removed answers an error rather than a panic).
func TestLiveImportSectionArr(t *testing.T) {
	f := newLiveFixture(t)
	ctx := context.Background()
	pol, err := f.base.Clone()
	if err != nil {
		t.Fatal(err)
	}
	pol.Import = &policy.Import{Arr: map[string]*policy.ArrSource{
		"radarr-new": {URL: "http://radarr.example:7878", APIKeyEnv: "THEATARR_TEST_UNSET_RADARR_KEY"}}}
	rt := &serveRuntime{Generation: 9, Policy: pol}
	resolved, ok := shadowRuntimeSource{}.Resolve(settings.WithRuntime(ctx, &settings.Runtime{Generation: 9, Policy: pol, Components: rt}), 0)
	if !ok || resolved.Arr == nil {
		t.Fatal("shadow runtime without the generation's arr clients")
	}
	if _, err := resolved.Arr("radarr-new"); err == nil || !strings.Contains(err.Error(), "THEATARR_TEST_UNSET_RADARR_KEY") {
		t.Fatalf("new instance: %v", err)
	}
	if _, err := resolved.Arr("radarr-gone"); err == nil {
		t.Fatal("removed instance resolved")
	}
}

// The mount metrics and the preflight read serveLibrary.current, the active generation's library. A save
// that moves the mount starts a fresh guard there; the startup library's guard is no longer the one in use.
func TestServeLibraryCurrentFollowsSaves(t *testing.T) {
	f := newLiveFixture(t)
	ctx := context.Background()
	workers := newServeWorkers(ctx, f.log)
	defer workers.Close()
	first, err := f.build(ctx, f.initial, nil, workers)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := settings.NewRuntimeCoordinator(f.st, f.base, f.initial, first,
		func(ctx context.Context, snap *settings.Snapshot, previous *settings.Runtime) (any, error) {
			var prev *serveRuntime
			if previous != nil {
				prev, _ = previous.Components.(*serveRuntime)
			}
			return f.build(ctx, snap, prev, workers)
		})
	if err != nil {
		t.Fatal(err)
	}
	lb, err := newServeLibrary(f.base, f.db, f.q, nil, f.log, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lb.current() != lb.lib {
		t.Fatal("without settings in the database current is the startup library")
	}
	lb.manager = manager
	if lb.current() != first.Library.lib {
		t.Fatal("current is not the active generation's library")
	}
	head, err := manager.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	req := settings.SaveRequest{ExpectedGeneration: head.Generation, Actor: "test", Records: head.Records}
	editSection(t, &req.Records, "library", func(c map[string]any) {
		m, _ := c["mount"].(map[string]any)
		if m == nil {
			m = map[string]any{}
			c["mount"] = m
		}
		m["path"] = filepath.Join(t.TempDir(), "moved")
	})
	if proof, err := manager.Confirm(ctx, req); err == nil {
		req.ConfirmationToken = proof.Token
	}
	if _, err := manager.SaveAndApply(ctx, req); err != nil {
		t.Fatal(err)
	}
	saved, _ := manager.Current().Components.(*serveRuntime)
	if saved == nil || saved == first || lb.current() != saved.Library.lib || lb.current() == lb.lib {
		t.Fatal("current did not follow the save")
	}
}
