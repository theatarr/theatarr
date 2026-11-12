package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
	"github.com/theatarr/theatarr/internal/web"
)

func TestAcquirerEvidenceUsesCapturedPolicy(t *testing.T) {
	t.Parallel()
	db, s, base, initial := runtimeFixture(t)
	ctx := context.Background()
	for _, query := range []string{
		`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Movie',0,0)`,
		`INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(1,1,'movie-web',0,0)`,
	} {
		if _, err := db.Writer().ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	cfg := acquire.Config{Store: db, Policy: initial.Policy, Queue: jobs.NewQueue(db), Router: &serveRouter{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Search: func(context.Context, decide.Title, decide.Version, *decide.ExistingFile, *decide.SeriesSearch) (*router.Searched, error) {
			d := &decide.Decision{MatchType: decide.MatchTitle, Release: "Movie", Quality: "WEBDL-1080p"}
			return &router.Searched{Ranked: []*decide.Decision{d}, Releases: map[*decide.Decision]indexer.Release{
				d: {Indexer: "dmm", Title: "Movie"},
			}}, nil
		}}
	first, err := acquire.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	service := &serveAcquirer{fallback: first}
	manager, err := settings.NewRuntimeCoordinator(s, base, initial, &serveRuntime{Acquirer: first},
		func(_ context.Context, snap *settings.Snapshot, _ *settings.Runtime) (any, error) {
			runtime := &serveRuntime{Policy: snap.Policy}
			return runtime, service.prepare(cfg, runtime)
		})
	if err != nil {
		t.Fatal(err)
	}
	service.manager = manager
	oldCtx := manager.Capture(ctx)
	req := settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Base: base, Records: initial.Records}
	for i := range req.Records.Indexers {
		if req.Records.Indexers[i].Key == "dmm" {
			req.Records.Indexers[i].Config.Kind = "torznab"
		}
	}
	// Another kind with the kept key is another address for it: confirmed.
	proof, err := manager.Confirm(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ConfirmationToken = proof.Token
	if _, err := manager.SaveAndApply(ctx, req); err != nil {
		t.Fatal(err)
	}
	for _, opCtx := range []context.Context{ctx, oldCtx} {
		if _, err := service.Interactive(opCtx, 1, "movie-web", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"dmm", "torznab"} {
		var count int
		if err := db.Reader().QueryRowContext(ctx, `SELECT count FROM source_sighting WHERE source_kind = ?`, kind).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s evidence count=%d, err=%v", kind, count, err)
		}
	}
}

type continuityProbe struct {
	provider.Provider
	probe func(context.Context, string) (*provider.Torrent, error)
}

func (p continuityProbe) Torrent(ctx context.Context, id string) (*provider.Torrent, error) {
	return p.probe(ctx, id)
}

// vpnGuardOn gives a fixture policy the VPN egress guard on indexers (the example ships it off).
func vpnGuardOn(p *policy.Policy) {
	p.Egress = &policy.Egress{CheckURL: "https://api.ipify.org", VPNIPs: []string{"198.51.100.7"},
		RequireVPNFor: []string{policy.EgressIndexers}}
}

func runtimeFixture(t *testing.T, edits ...func(*policy.Policy)) (*store.Store, *settings.Store, *policy.Policy, *settings.Snapshot) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	base, err := policy.Load("../../examples/policy")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range base.Profiles {
		p.Formats = nil
		p.FromArr = ""
		p.FormatSet = ""
	}
	for _, edit := range edits {
		edit(base)
	}
	s, err := settings.New(db)
	if err != nil {
		t.Fatal(err)
	}
	s.SetTrashRoot("../../internal/settings/testdata/trash")
	snap, err := s.Bootstrap(ctx, base, settings.ImportRequest{Dir: base.Dir, Actor: "test", Lookup: func(name string) (string, bool) { return "fixture-" + name, true }})
	if err != nil {
		t.Fatal(err)
	}
	return db, s, base, snap
}

func TestStartupFallbackRegistersGatewayCollectorsOnce(t *testing.T) {
	t.Parallel()
	db, s, base, initial := runtimeFixture(t)
	ctx := context.Background()
	if err := s.MarkPublished(ctx, initial.Generation); err != nil {
		t.Fatal(err)
	}
	head, err := s.Save(ctx, settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Base: base, Records: initial.Records})
	if err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	attempts := 0
	_, _, fallback, err := prepareStartupRuntime(ctx, s, base, head, slog.New(slog.NewTextHandler(io.Discard, nil)), func(ctx context.Context, snap *settings.Snapshot) (any, error) {
		attempts++
		// Gateway construction succeeds; the indexer constructor then rejects the unsupported adapter.
		if snap.Generation == head.Generation {
			snap.Policy.Indexers["prowlarr-public"].Kind = "unsupported"
		}
		return prepareServeRuntime(ctx, snap, nil, db, jobs.NewQueue(db), reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	if err != nil || !fallback || attempts != 2 {
		t.Fatalf("fallback=%t attempts=%d err=%v", fallback, attempts, err)
	}
	if _, err := reg.Gather(); err != nil {
		t.Fatal(err)
	}
}

func TestStartupProfilesTolerantReloadStrict(t *testing.T) {
	t.Parallel()
	p := &policy.Policy{Versions: map[string]*policy.VersionPolicy{"good": {Kind: policy.KindMovie, Profile: "good"}, "bad": {Kind: policy.KindMovie, Profile: "bad"}},
		Profiles: map[string]*policy.Profile{"good": {Qualities: []policy.QualityItem{{Name: "WEBDL-1080p"}}, Cutoff: "WEBDL-1080p"}, "bad": {FromArr: "invalid"}}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, source := range []string{"yaml", "first-db"} {
		t.Run(source, func(t *testing.T) {
			eng, err := newShadowEngine(p, log, nil)
			if err != nil || eng == nil || eng.Profile("good", format.Radarr) == nil || eng.Profile("bad", format.Radarr) != nil {
				t.Fatalf("startup engine=%v err=%v", eng, err)
			}
			if _, err := newShadowEngine(p, log, eng); err == nil {
				t.Fatal("reload accepted a failed profile")
			}
		})
	}
}

func TestAccountProbesBoundedAndUseCandidateGateway(t *testing.T) {
	db, _, _, snap := runtimeFixture(t)
	ctx := context.Background()
	if _, err := db.Writer().Exec(`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Movie',0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(`INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(1,1,'movie-web',0,0)`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 8; i++ {
		if _, err := db.Writer().Exec(`INSERT INTO download(id,title_id,version_id,version_key,info_hash,release_title,indexer,provider,provider_torrent_id,state,created_at,updated_at)
   VALUES(?,1,1,'movie-web',?,'Movie','dmm','rd',?,'added',0,0)`, i, strings.Repeat("a", 40), fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	var inFlight, peak atomic.Int32
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	probe := continuityProbe{probe: func(ctx context.Context, _ string) (*provider.Torrent, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 60*time.Second {
			return nil, errors.New("missing overall deadline")
		}
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &provider.Torrent{Hash: strings.Repeat("a", 40)}, nil
	}}
	previous := &serveRuntime{Policy: snap.Policy, ProviderKeys: map[string]string{"rd": "old"}}
	done := make(chan error, 1)
	go func() {
		_, err := validateProviderContinuity(ctx, db, snap, previous, map[string]string{"rd": "new"}, map[string]provider.Provider{"rd": probe})
		done <- err
	}()
	for range 4 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("probes did not run concurrently")
		}
	}
	if n := peak.Load(); n != 4 {
		t.Fatalf("peak concurrency=%d", n)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := peak.Load(); n > 4 {
		t.Fatalf("peak concurrency=%d", n)
	}
	if _, err := db.Writer().Exec(`WITH RECURSIVE ids(id) AS (SELECT 9 UNION ALL SELECT id + 1 FROM ids WHERE id < 101)
		INSERT INTO download(id,title_id,version_id,version_key,info_hash,release_title,indexer,provider,provider_torrent_id,state,created_at,updated_at)
		SELECT id,1,1,'movie-web',?,'Movie','dmm','rd',CAST(id AS TEXT),'added',0,0 FROM ids`, strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	var excessProbes atomic.Int32
	_, err := validateProviderContinuity(ctx, db, snap, previous, map[string]string{"rd": "new"}, map[string]provider.Provider{
		"rd": continuityProbe{probe: func(context.Context, string) (*provider.Torrent, error) {
			excessProbes.Add(1)
			return &provider.Torrent{Hash: strings.Repeat("a", 40)}, nil
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "probe limit is 100") || excessProbes.Load() != 0 {
		t.Fatalf("probe cap: calls=%d err=%v", excessProbes.Load(), err)
	}
	// The real candidate client must see the candidate host and credential, despite a published old gateway.
	if _, err := db.Writer().Exec("DELETE FROM download WHERE id > 1"); err != nil {
		t.Fatal(err)
	}
	seen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		fmt.Fprintf(w, `{"id":"1","hash":%q,"status":"downloading"}`, strings.Repeat("a", 40))
	}))
	defer server.Close()
	oldGateway, err := gateway.New(ctx, snap.Policy, gateway.Options{})
	if err != nil {
		t.Fatal(err)
	}
	oldGateway.Activate()
	previous.Gateway = oldGateway
	// Put the new URL and credential into a validated candidate.
	ss, err := settings.New(db)
	if err != nil {
		t.Fatal(err)
	}
	ss.SetTrashRoot("../../internal/settings/testdata/trash")
	req := settings.SaveRequest{ExpectedGeneration: snap.Generation, Actor: "test", Base: snap.Policy, Records: snap.Records, SecretWrites: map[string]settings.Secret{"providers/rd": settings.NewSecret("candidate-key")}}
	for i := range req.Records.Providers {
		if req.Records.Providers[i].Key == "rd" {
			req.Records.Providers[i].Config.URL = server.URL
		}
	}
	candidate, err := ss.Candidate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	next, err := prepareServeRuntime(ctx, candidate, previous, db, jobs.NewQueue(db), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Continuity.probed["rd"]) != 1 {
		t.Fatal("candidate torrent was not verified")
	}
	if got := <-seen; got != "Bearer candidate-key" {
		t.Fatalf("wrong candidate credential: %q", got)
	}
}

func TestAfterPublishChecksCanceledJoinedAndDeduplicated(t *testing.T) {
	started, canceled := make(chan struct{}, 2), make(chan struct{}, 2)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		started <- struct{}{}
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	defer server.Close()
	gw, err := gateway.New(context.Background(), &policy.Policy{Egress: &policy.Egress{CheckURL: server.URL, VPNIPs: []string{"127.0.0.1"}}}, gateway.Options{})
	if err != nil {
		t.Fatal(err)
	}
	serviceCtx, stop := context.WithCancel(context.Background())
	checks := newServeEgressChecks(serviceCtx)
	defer checks.Close()
	runtime := &serveRuntime{Gateway: gw, EgressChecks: checks}
	runtime.AfterPublish()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("check did not start")
	}
	for range 10 {
		runtime.AfterPublish()
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate immediate check")
	}
	stop()
	checks.Close()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel check")
	}
	checks.mu.Lock()
	remaining := len(checks.running)
	checks.mu.Unlock()
	if remaining != 0 {
		t.Fatal("shutdown did not join checks")
	}
}

func TestStartupWiredSearchUsesRotatedIndexerCredential(t *testing.T) {
	t.Parallel()
	db, s, base, initial := runtimeFixture(t, vpnGuardOn)
	var key atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/echo" {
			io.WriteString(w, "127.0.0.1")
			return
		}
		key.Store(r.URL.Query().Get("apikey"))
		io.WriteString(w, `<rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()
	req := settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Base: base, Records: initial.Records, SecretWrites: map[string]settings.Secret{"indexers/dmm": settings.NewSecret("first")}}
	for i := range req.Records.Indexers {
		if req.Records.Indexers[i].Key == "dmm" {
			req.Records.Indexers[i].Config.URL = server.URL + "/api/torznab/rd/cached"
		}
	}
	base.Egress.CheckURL = server.URL + "/echo"
	base.Egress.VPNIPs = []string{"127.0.0.1"}
	base.Versions["movie-web"].Indexers = []string{"dmm"}
	initial, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	first, err := prepareServeRuntime(context.Background(), initial, nil, db, jobs.NewQueue(db), nil, log)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := settings.NewRuntimeCoordinator(s, base, initial, first, func(ctx context.Context, snap *settings.Snapshot, old *settings.Runtime) (any, error) {
		return prepareServeRuntime(ctx, snap, old.Components.(*serveRuntime), db, jobs.NewQueue(db), nil, log)
	})
	if err != nil {
		t.Fatal(err)
	}
	lookup, _, _ := runtimeSearchers(manager)
	// This lookup is wired once at startup, then called after credential rotation.
	req = settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Base: base, Records: initial.Records, SecretWrites: map[string]settings.Secret{"indexers/dmm": settings.NewSecret("rotated")}}
	if _, err := manager.SaveAndApply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	manager.Current().Components.(*serveRuntime).Gateway.CheckEgress(context.Background())
	// An empty library title can still issue an IMDb query for DMM.
	_, err = lookup(context.Background(), decide.Title{ID: 1, Name: "Movie", Kind: policy.KindMovie, IMDb: "tt1234567"}, decide.Version{Key: "movie-web", Policy: base.Versions["movie-web"]}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := key.Load(); got != "rotated" {
		t.Fatalf("startup lookup credential=%v", got)
	}
}

func TestHTTPPolicyReadersUseCapturedRuntime(t *testing.T) {
	t.Parallel()
	db, s, base, initial := runtimeFixture(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	au, _, err := auth.New(ctx, db, auth.Options{Logger: log, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// The first start creates no account; the admin (id 1) is made here.
	if err := auth.ResetPassword(ctx, db, auth.AdminUsername, "test admin password"); err != nil {
		t.Fatal(err)
	}
	key, _, err := au.CreateKey(ctx, "runtime-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	services := &serveHTTPServices{ctx: ctx}
	defer services.Close()
	build := func(ctx context.Context, snap *settings.Snapshot) (*serveRuntime, error) {
		h, err := newServeHTTPRuntime(ctx, arrview.Config{Store: db, Auth: au, Policy: snap.Policy, Logger: log,
			Getenv: func(string) string { return "" }}, web.Config{Store: db, Auth: au, Policy: snap.Policy, Logger: log}, services)
		return &serveRuntime{HTTP: h}, err
	}
	first, err := build(ctx, initial)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := settings.NewRuntimeCoordinator(s, base, initial, first, func(ctx context.Context, snap *settings.Snapshot, _ *settings.Runtime) (any, error) {
		return build(ctx, snap)
	})
	if err != nil {
		t.Fatal(err)
	}
	oldCtx := manager.Capture(ctx)
	handler := runtimeHTTPHandler(manager, first.HTTP.mux)
	req := settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Base: base, Records: initial.Records}
	for i := range req.Records.Indexers {
		if req.Records.Indexers[i].Key == "dmm" {
			req.Records.Indexers[i].Config.Kind = "torznab"
		}
	}
	// Another kind with the kept key is another address for it: confirmed.
	proof, err := manager.Confirm(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ConfirmationToken = proof.Token
	if _, err := manager.SaveAndApply(ctx, req); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		kind string
	}{{"new request", ctx, "torznab"}, {"captured request", oldCtx, "dmm"}} {
		t.Run(tc.name, func(t *testing.T) {
			// The native API and an arr view.
			for _, path := range []string{web.APIBase + "/policy", "/radarr/api/v3/indexer"} {
				r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(tc.ctx)
				r.Header.Set(auth.APIKeyHeader, key)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != http.StatusOK {
					t.Fatalf("%s: status %d: %s", path, w.Code, w.Body)
				}
				if path != "/radarr/api/v3/indexer" {
					var summary web.PolicySummary
					if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
						t.Fatal(err)
					}
					found := false
					for _, ix := range summary.Indexers {
						if ix.Key == "dmm" {
							found = ix.Kind == tc.kind
						}
					}
					if !found {
						t.Fatalf("native policy ignored captured runtime: %s", w.Body)
					}
				} else {
					var indexers []struct {
						Name   string
						Fields []struct {
							Name  string
							Value string
						}
					}
					if err := json.Unmarshal(w.Body.Bytes(), &indexers); err != nil {
						t.Fatal(err)
					}
					found := false
					for _, ix := range indexers {
						if ix.Name == "dmm" {
							for _, f := range ix.Fields {
								if f.Name == "kind" {
									found = f.Value == tc.kind
								}
							}
						}
					}
					if !found {
						t.Fatalf("arr policy ignored captured runtime: %s", w.Body)
					}
				}
			}
		})
	}
}

func TestHealthEgressUsesCapturedRuntime(t *testing.T) {
	t.Parallel()
	db, _, base, _ := runtimeFixture(t, vpnGuardOn)
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "127.0.0.1") }))
	defer server.Close()
	p := &policy.Policy{Egress: &policy.Egress{CheckURL: server.URL, VPNIPs: []string{"127.0.0.1"}}}
	old, err := gateway.New(ctx, p, gateway.Options{})
	if err != nil {
		t.Fatal(err)
	}
	next, err := gateway.New(ctx, p, gateway.Options{})
	if err != nil {
		t.Fatal(err)
	}
	next.CheckEgress(ctx)
	for _, tc := range []struct {
		gw   *gateway.Gateway
		want string
	}{{old, health.Error}, {next, health.OK}} {
		hc := newServeHealth(serveHealthConfig{pol: base, st: db, q: jobs.NewQueue(db), gw: old, dbPath: db.Path()})
		captured := settings.WithRuntime(ctx, &settings.Runtime{Components: &serveRuntime{Gateway: tc.gw}})
		found := false
		for _, result := range hc.Results(captured) {
			if result.Source == "VpnEgressCheck" {
				found = true
				if result.Type != tc.want {
					t.Fatalf("VPN result=%+v, want %s", result, tc.want)
				}
			}
		}
		if !found {
			t.Fatal("VPN check missing")
		}
	}
}

// A startup fallback runtime can run a different provider account than the stored head. A budget-only save against
// the head then changes the active account, and the prepared router, the committed epoch and the published runtime
// must all agree on the advanced epoch.
func TestFallbackSavePublishesCommittedProviderEpoch(t *testing.T) {
	t.Parallel()
	db, s, base, initial := runtimeFixture(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := s.MarkPublished(ctx, initial.Generation); err != nil {
		t.Fatal(err)
	}
	// The head moves rd to account B; the DB records that account change at epoch 1.
	headReq := settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Base: base, Records: initial.Records,
		SecretWrites: map[string]settings.Secret{"providers/rd": settings.NewSecret("account-b")}}
	head, err := s.Save(ctx, headReq)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec("UPDATE settings_runtime_state SET provider_epoch = 1 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	// Startup fell back to the published generation, which still runs account A, at the current epoch.
	fallback, err := s.LoadGeneration(ctx, base, initial.Generation)
	if err != nil || fallback.ProviderEpoch != 1 {
		t.Fatalf("fallback=%+v err=%v", fallback, err)
	}
	active, err := prepareServeRuntime(ctx, fallback, nil, db, jobs.NewQueue(db), nil, log)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := settings.NewRuntimeCoordinator(s, base, fallback, active, func(ctx context.Context, snap *settings.Snapshot, old *settings.Runtime) (any, error) {
		return prepareServeRuntime(ctx, snap, old.Components.(*serveRuntime), db, jobs.NewQueue(db), nil, log)
	})
	if err != nil {
		t.Fatal(err)
	}
	budget := settings.SaveRequest{ExpectedGeneration: head.Generation, Actor: "test", Base: base, Records: head.Records}
	budget.Records.Budgets[0].Config.Requests++
	saved, err := manager.SaveAndApply(ctx, budget)
	if err != nil {
		t.Fatal(err)
	}
	stored := int64(eCount(db, "SELECT provider_epoch FROM settings_runtime_state WHERE id = 1"))
	published := manager.Current()
	prepared := published.Components.(*serveRuntime)
	if stored != 2 || saved.ProviderEpoch != stored || published.ProviderEpoch != stored || prepared.ProviderEpoch != stored ||
		prepared.Router.ProviderEpoch() != stored {
		t.Fatalf("db epoch=%d saved=%d published=%d prepared=%d router=%d", stored, saved.ProviderEpoch, published.ProviderEpoch,
			prepared.ProviderEpoch, prepared.Router.ProviderEpoch())
	}
	// A later budget-only save against the now-active account keeps the epoch.
	budget = settings.SaveRequest{ExpectedGeneration: saved.Generation, Actor: "test", Base: base, Records: saved.Records}
	budget.Records.Budgets[0].Config.Requests++
	again, err := manager.SaveAndApply(ctx, budget)
	if err != nil || again.ProviderEpoch != 2 || manager.Current().ProviderEpoch != 2 ||
		eCount(db, "SELECT provider_epoch FROM settings_runtime_state WHERE id = 1") != 2 {
		t.Fatalf("budget save after fallback recovery: saved=%v err=%v", again, err)
	}
}

// The gateway of every prepared generation masks that generation's DB-backed credentials, including a credential
// that only exists after a reload.
func TestReloadedGatewayRedactsDBCredential(t *testing.T) {
	t.Parallel()
	const secret = "rotated-db-credential-5521"
	db, s, base, initial := runtimeFixture(t)
	ctx := context.Background()
	var logs syncLog
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "{}") }))
	defer server.Close()
	first, err := prepareServeRuntime(ctx, initial, nil, db, jobs.NewQueue(db), nil, log)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := settings.NewRuntimeCoordinator(s, base, initial, first, func(ctx context.Context, snap *settings.Snapshot, old *settings.Runtime) (any, error) {
		return prepareServeRuntime(ctx, snap, old.Components.(*serveRuntime), db, jobs.NewQueue(db), nil, log)
	})
	if err != nil {
		t.Fatal(err)
	}
	req := settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Base: base, Records: initial.Records,
		SecretWrites: map[string]settings.Secret{"providers/rd": settings.NewSecret(secret)}}
	for i := range req.Records.Providers {
		if req.Records.Providers[i].Key == "rd" {
			req.Records.Providers[i].Config.URL = server.URL
		}
	}
	if _, err := manager.SaveAndApply(ctx, req); err != nil {
		t.Fatal(err)
	}
	gw := manager.Current().Components.(*serveRuntime).Gateway
	resp, err := gw.Get(ctx, gateway.Target{Class: gateway.ClassProvider, Key: "rd"}, server.URL+"/torrents/"+secret+"/info")
	if err == nil {
		resp.Body.Close()
	}
	if text := logs.String(); strings.Contains(text, secret) || !strings.Contains(text, "/torrents/***/info") {
		t.Fatalf("reloaded DB credential not masked (err=%v):\n%s", err, text)
	}
}

type syncLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A provider repointed with its kept credential is the same account. Preparing the confirmed save does
// not probe its downloads at the new address, so the stored key never reaches it before the change commits; the
// downloads stay and the epoch advances.
func TestProviderRepointKeptKeyIsNotProbed(t *testing.T) {
	t.Parallel()
	db, s, base, initial := runtimeFixture(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Movie', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'movie-web', 0, 0)`,
	} {
		if _, err := db.Writer().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Writer().ExecContext(ctx, `INSERT INTO download (id, title_id, version_id, version_key, info_hash,
		release_title, indexer, provider, provider_torrent_id, state, created_at, updated_at)
		VALUES (1, 1, 1, 'movie-web', ?, 'Movie', 'dmm', 'rd', 'rd-1', 'downloading', 0, 0)`, strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprintf(w, `{"id":"rd-1","hash":%q,"status":"downloading"}`, strings.Repeat("a", 40))
	}))
	defer server.Close()
	previous := &serveRuntime{Policy: initial.Policy, ProviderKeys: map[string]string{}}
	if err := initial.WithSecret("providers", "rd", nil, func(key string) error {
		previous.ProviderKeys["rd"] = key
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	req := settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Base: base, Records: initial.Records}
	req.Records.Providers = slices.Clone(initial.Records.Providers)
	for i := range req.Records.Providers {
		if req.Records.Providers[i].Key == "rd" {
			req.Records.Providers[i].Config.URL = server.URL
		}
	}
	candidate, err := s.Candidate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	next, err := prepareServeRuntime(ctx, candidate, previous, db, jobs.NewQueue(db), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("the kept key was sent to the new address %d time(s) while preparing", n)
	}
	if !slices.Contains(next.Continuity.changed, "rd") || next.Continuity.probed["rd"] != nil || !next.Continuity.advancesEpoch() {
		t.Fatalf("continuity: %+v", next.Continuity)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error { return next.CommitSettings(ctx, tx) }); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var state string
	if err := db.Reader().QueryRowContext(ctx, "SELECT state FROM download WHERE id = 1").Scan(&state); err != nil || state != "downloading" {
		t.Fatalf("download after commit: %q %v", state, err)
	}
}

// The serve search handler hands the job's retry_new mark to the acquirer: a Search now search of a movie released
// yesterday that accepts nothing queues the first follow-up when router.wanted.retry_new is on, and nothing when off.
func TestServeSearchJobKeepsRetryNewMark(t *testing.T) {
	t.Parallel()
	for _, on := range []bool{true, false} {
		ctx := context.Background()
		db, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if _, err := db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		pol := &policy.Policy{
			Indexers: map[string]*policy.Indexer{"pub": {Kind: "torznab", URL: "http://pub.test/api", Privacy: policy.PrivacyPublic}},
			Providers: map[string]*policy.Provider{"rd": {Kind: provider.KindRealDebrid, Order: 1, URL: "http://rd.test/rest/1.0",
				APIKeyEnv: "THEATARR_RD_KEY"}},
			Versions: map[string]*policy.VersionPolicy{"movie": {Mode: policy.ModeActive, Grab: true, Kind: policy.KindMovie,
				Storage: policy.StorageDebrid, Indexers: []string{"pub"}, Providers: []string{"rd"}}},
			Router: &policy.Router{Enabled: true, Wanted: &policy.Wanted{RetryNew: new(on)}},
		}
		now := time.Now()
		for _, q := range []string{
			`INSERT INTO title (id, kind, name, year, created_at, updated_at) VALUES (1, 'movie', 'One', 2026, 0, 0)`,
			`INSERT INTO title_release_date (title_id, source, type, date) VALUES (1, 'fixture', 'digital', '` +
				now.Add(-24*time.Hour).UTC().Format("2006-01-02") + `')`,
			`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (101, 1, 'movie', 'wanted', 0, 0)`,
			`INSERT INTO search_request (title_id, version_key, reason, season, created_at) VALUES (1, 'movie', 'missing', -1, 0)`,
		} {
			if _, err := db.Writer().ExecContext(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		q := jobs.NewQueue(db)
		ac, err := acquire.New(acquire.Config{Store: db, Policy: pol, Queue: q, Router: &serveRouter{},
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Search: func(_ context.Context, title decide.Title, v decide.Version, _ *decide.ExistingFile, _ *decide.SeriesSearch) (*router.Searched, error) {
				return &router.Searched{Ranked: []*decide.Decision{{}}, Request: router.Request{TitleID: title.ID,
					VersionID: v.ID, Version: v.Key}}, nil
			}})
		if err != nil {
			t.Fatal(err)
		}
		if n, err := ac.DrainSearchNow(ctx); err != nil || n != 1 {
			t.Fatalf("search now started %d searches: %v", n, err)
		}
		w := jobs.NewWorker(q, jobs.WorkerOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		(&serveAcquirer{fallback: ac}).RegisterJobs(w)
		if ran, err := w.RunOne(ctx); err != nil || !ran {
			t.Fatalf("search job: ran=%v err=%v", ran, err)
		}
		var state string
		if err := db.Reader().QueryRowContext(ctx, `SELECT state FROM acquire_run`).Scan(&state); err != nil || state != "nothing" {
			t.Fatalf("run state %q: %v", state, err)
		}
		var followUps int
		if err := db.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'
			AND json_extract(payload, '$.reason') = ? AND json_extract(payload, '$.retry_new') = 1
			AND json_extract(payload, '$.step') = 1`, router.JobResearch, router.ReasonFresh).Scan(&followUps); err != nil {
			t.Fatal(err)
		}
		var jobsAfter int
		if err := db.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM job WHERE kind = ?`, router.JobResearch).Scan(&jobsAfter); err != nil {
			t.Fatal(err)
		}
		if want := map[bool]int{true: 1, false: 0}[on]; followUps != want || jobsAfter != want {
			t.Fatalf("retry_new %v: %d follow-ups (%d re-search jobs), want %d", on, followUps, jobsAfter, want)
		}
	}
}

// A published runtime with router.wanted.retry_new off cancels the queued work of its chains (AfterPublish, which
// runs for every published generation and at start); one with the switch on leaves it.
func TestAfterPublishCancelsRetryNewWhenOff(t *testing.T) {
	t.Parallel()
	for _, on := range []bool{false, true} {
		ctx := context.Background()
		db, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if _, err := db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		pol := &policy.Policy{
			Indexers: map[string]*policy.Indexer{"pub": {Kind: "torznab", URL: "http://pub.test/api", Privacy: policy.PrivacyPublic}},
			Providers: map[string]*policy.Provider{"rd": {Kind: provider.KindRealDebrid, Order: 1, URL: "http://rd.test/rest/1.0",
				APIKeyEnv: "THEATARR_RD_KEY"}},
			Versions: map[string]*policy.VersionPolicy{"movie": {Mode: policy.ModeActive, Grab: true, Kind: policy.KindMovie,
				Storage: policy.StorageDebrid, Indexers: []string{"pub"}, Providers: []string{"rd"}}},
			Router: &policy.Router{Enabled: true, Wanted: &policy.Wanted{RetryNew: new(on)}},
		}
		q := jobs.NewQueue(db)
		ac, err := acquire.New(acquire.Config{Store: db, Policy: pol, Queue: q, Router: &serveRouter{},
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Search: func(context.Context, decide.Title, decide.Version, *decide.ExistingFile, *decide.SeriesSearch) (*router.Searched, error) {
				return nil, errors.New("not searched")
			}})
		if err != nil {
			t.Fatal(err)
		}
		for key, p := range map[string]router.ResearchRequest{
			"chain":    {TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonFresh, Step: 2, RetryNew: true},
			"ordinary": {TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonFresh, Step: 2},
		} {
			if _, _, err := q.Enqueue(ctx, router.JobResearch, p, jobs.EnqueueOptions{DedupeKey: key,
				RunAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
		}
		(&serveRuntime{Policy: pol, Acquirer: ac}).AfterPublish()
		states := map[string]string{}
		rows, err := db.Reader().QueryContext(ctx, `SELECT dedupe_key, state FROM job`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var key, state string
			if err := rows.Scan(&key, &state); err != nil {
				t.Fatal(err)
			}
			states[key] = state
		}
		rows.Close()
		want := map[bool]string{false: "done", true: "queued"}[on]
		if states["chain"] != want || states["ordinary"] != "queued" {
			t.Fatalf("retry_new %v: jobs %v, want the chain's %s and the ordinary one queued", on, states, want)
		}
	}
}
