package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/web"
)

// TestSettingsAPIProviderContinuityRefusal drives the admin API over the real continuity checks and commit:
// an account change it cannot verify and a disable that would orphan an active download are clear 422s, the
// orphan flag must be stated, and only true fails the download.
func TestSettingsAPIProviderContinuityRefusal(t *testing.T) {
	t.Parallel()
	db, s, base, initial := runtimeFixture(t)
	ctx := context.Background()
	for _, query := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Movie', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'movie-web', 0, 0)`,
		`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider, state, created_at, updated_at)
			VALUES (1, 1, 1, 'movie-web', '` + strings.Repeat("a", 40) + `', 'Movie', 'pub', 'rd', 'added', 0, 0)`,
	} {
		if _, err := db.Writer().ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	keysFor := func(snap *settings.Snapshot) map[string]string {
		keys := map[string]string{}
		for _, r := range snap.Records.Providers {
			if r.Enabled {
				snap.WithSecret("providers", r.Key, nil, func(v string) error { keys[r.Key] = v; return nil })
			}
		}
		return keys
	}
	manager, err := settings.NewRuntimeCoordinator(s, base, initial, &serveRuntime{Policy: initial.Policy, ProviderKeys: keysFor(initial)},
		func(ctx context.Context, snap *settings.Snapshot, previous *settings.Runtime) (any, error) {
			keys := keysFor(snap)
			c, err := validateProviderContinuity(ctx, db, snap, previous.Components.(*serveRuntime), keys, nil)
			if err != nil {
				return nil, err
			}
			return &serveRuntime{Policy: snap.Policy, ProviderKeys: keys, Continuity: c, ProviderEpoch: preparedProviderEpoch(snap, c)}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	au, _, err := auth.New(ctx, db, auth.Options{Logger: log, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ui, err := web.New(web.Config{Store: db, Auth: au, Policy: initial.Policy, Logger: log, Settings: manager})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	ui.Mount(mux)
	token, err := au.StartSession(ctx, 1, "192.0.2.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	var rd int64
	for _, r := range initial.Records.Providers {
		if r.Key == "rd" {
			rd = r.ID
		}
	}
	put := func(body map[string]any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest("PUT", fmt.Sprintf("%s/settings/providers/%d", web.APIBase, rd), strings.NewReader(string(b)))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
		r.Header.Set(auth.CSRFHeader, au.SessionCSRF(token))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec
	}
	gen := initial.Generation
	state := func() string {
		var s string
		if err := db.Reader().QueryRow(`SELECT state FROM download WHERE id = 1`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	// An account change whose active torrents cannot be verified.
	rec := put(map[string]any{"generation": gen, "orphan_active_downloads": false, "secret": "rotated-account-key"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "could not verify 1 active download(s)") ||
		strings.Contains(rec.Body.String(), "rotated-account-key") {
		t.Fatalf("unverifiable account change: %d %s", rec.Code, rec.Body)
	}
	// Disabling the provider: the flag must be stated, false refuses, true fails the download.
	if rec := put(map[string]any{"generation": gen, "enabled": false}); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "orphan_active_downloads is required") {
		t.Fatalf("missing flag: %d %s", rec.Code, rec.Body)
	}
	rec = put(map[string]any{"generation": gen, "enabled": false, "orphan_active_downloads": false})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "would orphan 1 active download(s)") {
		t.Fatalf("orphaning disable: %d %s", rec.Code, rec.Body)
	}
	if manager.Current().Generation != gen || state() != "added" {
		t.Fatal("a refused save changed the runtime or the download")
	}
	rec = put(map[string]any{"generation": gen, "enabled": false, "orphan_active_downloads": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("confirmed disable: %d %s", rec.Code, rec.Body)
	}
	var reason string
	db.Reader().QueryRow(`SELECT error FROM download WHERE id = 1`).Scan(&reason)
	if state() != "failed" || reason != "provider removed by settings" || manager.Current().Generation != gen+1 {
		t.Fatalf("after confirmed disable: %s %q generation %d", state(), reason, manager.Current().Generation)
	}
}

// TestSettingsTesterUsesLiveGatewayState: a source test goes through a gateway that carries the live runtime's
// egress checks, budgets and back-offs. It is refused until the VPN is confirmed, spends the live budget (and is
// refused when that budget is empty), respects back-offs without clearing them, and tests a candidate URL without
// storing it.
func TestSettingsTesterUsesLiveGatewayState(t *testing.T) {
	db, s, base, initial := runtimeFixture(t, vpnGuardOn)
	ctx := context.Background()
	var lastKey, lastPath atomic.Value
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/echo" {
			io.WriteString(w, "127.0.0.1")
			return
		}
		requests.Add(1)
		lastKey.Store(r.URL.Query().Get("apikey"))
		lastPath.Store(r.URL.Path)
		io.WriteString(w, `<rss version="2.0"><channel><item><title>A.Movie.1994.1080p</title></item></channel></rss>`)
	}))
	defer server.Close()
	req := settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Base: base, Records: initial.Records,
		SecretWrites: map[string]settings.Secret{"indexers/dmm": settings.NewSecret("live-key")}}
	for i := range req.Records.Indexers {
		if req.Records.Indexers[i].Key == "dmm" {
			req.Records.Indexers[i].Config.URL = server.URL + "/api/torznab/rd/cached"
		}
	}
	base.Egress.CheckURL = server.URL + "/echo"
	base.Egress.VPNIPs = []string{"127.0.0.1"}
	initial, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
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
	tester := serveSettingsTester{manager: manager, st: db, log: log}
	live := first.Gateway
	// No egress check yet: the VPN guard refuses before anything is sent.
	if out := tester.TestSource(ctx, web.SettingsTestRequest{Snapshot: initial, Entity: "indexers", Key: "dmm"}); out.Status != "refused" || out.Category != "vpn" || requests.Load() != 0 {
		t.Fatalf("before the egress check: %+v, %d requests", out, requests.Load())
	}
	live.CheckEgress(ctx)
	budget := func() float64 {
		for _, b := range live.State().Budgets {
			if b.Key == "dmm-key" {
				return b.Tokens
			}
		}
		t.Fatal("no dmm-key budget")
		return 0
	}
	before := budget()
	out := tester.TestSource(ctx, web.SettingsTestRequest{Snapshot: initial, Entity: "indexers", Key: "dmm"})
	if out.Status != "ok" || out.Results == nil || *out.Results != 1 || lastKey.Load() != "live-key" {
		t.Fatalf("test: %+v key %v", out, lastKey.Load())
	}
	// The test's request counts as a test of the saved indexer (docs/design/gateway.md).
	if got := live.State().Requests["dmm"].Today; len(got) != 1 || got[gateway.PurposeTest] != 1 {
		t.Fatalf("requests by purpose after the test: %v", got)
	}
	// The test spent the live bucket's token (burst 1), so an immediate second test within a short deadline is
	// refused by the budget instead of resetting it.
	if after := budget(); after >= before {
		t.Fatalf("test did not spend the live budget: %v -> %v", before, after)
	}
	short, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	out = tester.TestSource(short, web.SettingsTestRequest{Snapshot: initial, Entity: "indexers", Key: "dmm"})
	cancel()
	if out.Status != "refused" || out.Category != "budget" || requests.Load() != 1 {
		t.Fatalf("empty budget: %+v, %d requests", out, requests.Load())
	}
	// A live back-off refuses the test and survives it.
	until := time.Now().Add(time.Hour)
	live.BackOff(ctx, gateway.Target{Class: gateway.ClassIndexer, Key: "dmm"}, until, "test back-off")
	if out := tester.TestSource(ctx, web.SettingsTestRequest{Snapshot: initial, Entity: "indexers", Key: "dmm"}); out.Status != "refused" || out.Category != "backoff" {
		t.Fatalf("backing off: %+v", out)
	}
	found := false
	for _, b := range live.State().Backoffs {
		if b.Subject == "indexer:dmm" && b.Active {
			found = true
		}
	}
	if !found {
		t.Fatal("the test cleared the live back-off")
	}
	// A candidate (another indexer key and URL) is tested without being stored.
	cand := settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Records: initial.Records,
		SecretWrites: map[string]settings.Secret{"indexers/zilean": settings.NewSecret("candidate-key")}}
	cand.Records.Indexers = append([]settings.Indexer(nil), cand.Records.Indexers...)
	for i := range cand.Records.Indexers {
		if cand.Records.Indexers[i].Key == "zilean" {
			cand.Records.Indexers[i].Config.URL = server.URL + "/candidate"
		}
	}
	snap, err := manager.Candidate(ctx, cand)
	if err != nil {
		t.Fatal(err)
	}
	if out := tester.TestSource(ctx, web.SettingsTestRequest{Snapshot: snap, Saved: initial, NewSecret: true, Entity: "indexers", Key: "zilean"}); out.Category == "internal" || !strings.HasPrefix(lastPath.Load().(string), "/candidate") {
		t.Fatalf("candidate: %+v path %v", out, lastPath.Load())
	}
	stored, err := manager.Snapshot(ctx)
	if err != nil || stored.Generation != initial.Generation || manager.Current().Generation != initial.Generation {
		t.Fatalf("candidate test stored or published: %v", err)
	}
	for _, r := range stored.Records.Indexers {
		if r.Key == "zilean" && strings.HasPrefix(r.Config.URL, server.URL) {
			t.Fatal("candidate URL stored")
		}
	}
}
