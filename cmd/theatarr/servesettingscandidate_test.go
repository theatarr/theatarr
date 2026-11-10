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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/web"
)

// probeServer is an httptest upstream that counts requests and remembers the API keys it was sent. DMM's tb feed
// answers 500, its all-provider feed (/api/torznab/cached) 429, /api/torznab/rd/ad/tb/cached error 102,
// /api/torznab/ad/tb/cached error 900 to a movie search and an empty feed otherwise, /echo the egress check, anything
// else a one-item RSS feed.
type probeServer struct {
	*httptest.Server
	requests atomic.Int32
	mu       sync.Mutex
	keys     []string
}

func newProbeServer(t *testing.T) *probeServer {
	t.Helper()
	ps := &probeServer{}
	ps.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/echo" {
			io.WriteString(w, "127.0.0.1")
			return
		}
		ps.requests.Add(1)
		ps.mu.Lock()
		ps.keys = append(ps.keys, r.URL.Query().Get("apikey"))
		ps.mu.Unlock()
		switch {
		case r.URL.Path == "/api/torznab/rd/ad/tb/cached/api":
			// DMM with no TorBox key linked: the full multi-provider path fails; its rd/ad RSS endpoint answers.
			io.WriteString(w, `<error code="102" description="no TorBox key linked"/>`)
		case r.URL.Path == "/api/torznab/ad/tb/cached/api" && r.URL.Query().Get("t") == "movie":
			// A failing movie search whose text search would answer (an empty feed, below).
			io.WriteString(w, `<error code="900" description="internal error"/>`)
		case r.URL.Path == "/api/torznab/ad/tb/cached/api":
			io.WriteString(w, `<rss version="2.0"><channel></channel></rss>`)
		case strings.HasPrefix(r.URL.Path, "/api/torznab/tb/"):
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasPrefix(r.URL.Path, "/api/torznab/cached"):
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			io.WriteString(w, `<rss version="2.0"><channel><item><title>A.Movie.1994.1080p</title></item></channel></rss>`)
		}
	}))
	t.Cleanup(ps.Close)
	return ps
}

func (ps *probeServer) sawKey(key string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, k := range ps.keys {
		if k == key {
			return true
		}
	}
	return false
}

// testerAPI is the admin settings API over the real source tester and a live gateway whose only allowlisted test
// upstream is live (dmm is saved there with the credential "live-key"; the egress check passes).
type testerAPI struct {
	t       *testing.T
	mux     *http.ServeMux
	token   string
	csrf    string
	manager *settings.RuntimeCoordinator
	gw      *gateway.Gateway
	dmm     int64
}

func newTesterAPI(t *testing.T, live *probeServer, limits *web.SettingsTestLimits, dmmBudget string) *testerAPI {
	t.Helper()
	db, s, base, initial := runtimeFixture(t, vpnGuardOn)
	ctx := context.Background()
	req := settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Base: base, Records: initial.Records,
		SecretWrites: map[string]settings.Secret{"indexers/dmm": settings.NewSecret("live-key")}}
	req.Records.Indexers = append([]settings.Indexer(nil), req.Records.Indexers...)
	var dmm int64
	for i := range req.Records.Indexers {
		if req.Records.Indexers[i].Key == "dmm" {
			req.Records.Indexers[i].Config.URL = live.URL + "/api/torznab/rd/cached"
			if dmmBudget != "" {
				req.Records.Indexers[i].Config.Budget = dmmBudget
			}
			dmm = req.Records.Indexers[i].ID
		}
	}
	base.Egress.CheckURL = live.URL + "/echo"
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
	first.Gateway.CheckEgress(ctx)
	au, _, err := auth.New(ctx, db, auth.Options{Logger: log, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// The first start creates no account; the admin (id 1) is made here.
	if err := auth.ResetPassword(ctx, db, auth.AdminUsername, "test admin password"); err != nil {
		t.Fatal(err)
	}
	ui, err := web.New(web.Config{Store: db, Auth: au, Policy: initial.Policy, Logger: log, Settings: manager,
		SettingsTester: serveSettingsTester{manager: manager, st: db, log: log}, SettingsTests: limits})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	ui.Mount(mux)
	token, err := au.StartSession(ctx, 1, "192.0.2.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	return &testerAPI{t: t, mux: mux, token: token, csrf: au.SessionCSRF(token), manager: manager, gw: first.Gateway, dmm: dmm}
}

func (a *testerAPI) post(path string, body any) (int, map[string]any) {
	a.t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", web.APIBase+"/settings"+path, strings.NewReader(string(b)))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: a.token})
	r.Header.Set(auth.CSRFHeader, a.csrf)
	rec := httptest.NewRecorder()
	a.mux.ServeHTTP(rec, r)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// liveHealth reports the live gateway's back-off rows with failures or an active back-off.
func (a *testerAPI) liveHealth() []string {
	var out []string
	for _, b := range a.gw.State().Backoffs {
		if b.Active || b.Failures > 0 {
			out = append(out, fmt.Sprintf("%s failures=%d active=%t", b.Subject, b.Failures, b.Active))
		}
	}
	return out
}

func dmmConfig(url string) map[string]any {
	return map[string]any{"kind": "dmm", "url": url, "privacy": "public", "budget": "dmm-key"}
}

// TestCandidateTestDestinations: a candidate never widens its own authorization. The stored
// credential only goes to the saved scheme, host and port; private and loopback destinations must already be in the
// live allowlist; brand-new candidates follow the same rules.
func TestCandidateTestDestinations(t *testing.T) {
	t.Parallel()
	live, other := newProbeServer(t), newProbeServer(t)
	a := newTesterAPI(t, live, &web.SettingsTestLimits{Cooldown: -1}, "")
	// An existing source pointed at a new address without a credential: refused before any outbound call, so the
	// stored credential never reaches it.
	code, out := a.post("/indexers/test", map[string]any{"id": a.dmm, "config": dmmConfig(other.URL + "/api/torznab/rd/cached")})
	if code != http.StatusUnprocessableEntity || !strings.Contains(fmt.Sprint(out["error"]), "send the credential again") {
		t.Errorf("new address without a credential: %d %v", code, out)
	}
	if other.sawKey("live-key") || other.requests.Load() != 0 {
		t.Fatalf("the stored credential went to an unsaved address (%d requests)", other.requests.Load())
	}
	// With a new credential the address is still a loopback one that the live allowlist does not hold.
	code, out = a.post("/indexers/test", map[string]any{"id": a.dmm, "secret": "candidate-key", "config": dmmConfig(other.URL + "/api/torznab/rd/cached")})
	if code != http.StatusOK || out["status"] != "refused" || out["category"] != "host" || other.requests.Load() != 0 {
		t.Errorf("loopback candidate address: %d %v, %d requests", code, out, other.requests.Load())
	}
	// A brand-new candidate gets no further.
	code, out = a.post("/indexers/test", map[string]any{"key": "probe-new", "secret": "candidate-key", "config": dmmConfig(other.URL + "/api/torznab/rd/cached")})
	if code != http.StatusOK || out["status"] != "refused" || out["category"] != "host" || other.requests.Load() != 0 {
		t.Errorf("new loopback candidate: %d %v, %d requests", code, out, other.requests.Load())
	}
	// The saved host itself (live allowlist) can be tested on another path without resending the credential.
	before := live.requests.Load()
	code, out = a.post("/indexers/test", map[string]any{"id": a.dmm, "config": dmmConfig(live.URL + "/api/torznab/ad/cached")})
	if code != http.StatusOK || out["status"] != "ok" || live.requests.Load() != before+1 {
		t.Errorf("same address, new path: %d %v", code, out)
	}
}

// TestCandidateTestBudget: a test of an existing source is charged to its saved budget whatever
// the candidate says, and brand-new candidates share a bounded probe budget; an empty budget is never unlimited.
func TestCandidateTestBudget(t *testing.T) {
	t.Parallel()
	live := newProbeServer(t)
	a := newTesterAPI(t, live, &web.SettingsTestLimits{Cooldown: -1, Timeout: 500 * time.Millisecond}, "")
	// The saved test spends dmm-key's only token (burst 1, 16/min).
	if code, out := a.post(fmt.Sprintf("/indexers/%d/test", a.dmm), nil); code != http.StatusOK || out["status"] != "ok" {
		t.Fatalf("saved test: %d %v", code, out)
	}
	before := live.requests.Load()
	// A candidate that drops the budget is still charged to dmm-key, which has no token now.
	code, out := a.post("/indexers/test", map[string]any{"id": a.dmm, "config": map[string]any{"budget": ""}})
	if code != http.StatusOK || out["status"] != "refused" || out["category"] != "budget" || live.requests.Load() != before {
		t.Errorf("candidate without a budget: %d %v, %d new requests", code, out, live.requests.Load()-before)
	}
	// Brand-new candidates without a budget share the probe budget: a few go through, then it refuses.
	refused := 0
	for i := range 8 {
		code, out := a.post("/indexers/test", map[string]any{"key": fmt.Sprintf("new%d", i),
			"config": map[string]any{"kind": "dmm", "url": live.URL + "/api/torznab/ad/cached", "privacy": "public"}})
		if code == http.StatusOK && out["status"] == "refused" && out["category"] == "budget" {
			refused++
		}
	}
	if refused == 0 {
		t.Errorf("eight unbudgeted candidate tests were all sent (%d requests)", live.requests.Load()-before)
	}
}

// TestCandidateTestHealthIsolated: failing candidates never touch the live source's failure
// streak or back-off (500s and a 429), yet they are refused while the live source backs off.
func TestCandidateTestHealthIsolated(t *testing.T) {
	t.Parallel()
	live := newProbeServer(t)
	// A roomy budget so repeated tests are not throttled; dmm is its only indexer user.
	a := newTesterAPI(t, live, &web.SettingsTestLimits{Cooldown: -1}, "tmdb")
	cfg := func(path string) map[string]any {
		return map[string]any{"kind": "dmm", "url": live.URL + path, "privacy": "public", "budget": "tmdb"}
	}
	for range 3 {
		if code, out := a.post("/indexers/test", map[string]any{"id": a.dmm, "config": cfg("/api/torznab/tb/cached")}); code != http.StatusOK || out["status"] == "ok" {
			t.Fatalf("failing candidate: %d %v", code, out)
		}
	}
	a.post("/indexers/test", map[string]any{"id": a.dmm, "config": cfg("/api/torznab/cached")})
	if h := a.liveHealth(); len(h) != 0 {
		t.Errorf("candidate failures changed the live health: %v", h)
	}
	// The saved source as saved still works and is not backing off.
	if code, out := a.post(fmt.Sprintf("/indexers/%d/test", a.dmm), nil); code != http.StatusOK || out["status"] != "ok" {
		t.Errorf("saved test after failing candidates: %d %v", code, out)
	}
	// While the live source backs off, a candidate for it is refused without being sent.
	a.gw.BackOff(context.Background(), gateway.Target{Class: gateway.ClassIndexer, Key: "dmm"}, time.Now().Add(time.Hour), "test")
	before := live.requests.Load()
	if code, out := a.post("/indexers/test", map[string]any{"id": a.dmm, "config": cfg("/api/torznab/ad/cached")}); code != http.StatusOK ||
		out["status"] != "refused" || out["category"] != "backoff" || live.requests.Load() != before {
		t.Errorf("candidate while the live source backs off: %d %v", code, out)
	}
}
