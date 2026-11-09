package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

// testSecret is the credential every settings test sends; no response, log line or audit record may contain it.
const testSecret = "sentinel-credential-9f27c41e"

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// fakeTester records what it was asked and answers through fn.
type fakeTester struct {
	mu      sync.Mutex
	seen    []string // "entity/key url secret"
	running atomic.Int32
	peak    atomic.Int32
	fn      func(ctx context.Context) SettingsTestOutcome
}

func (f *fakeTester) TestSource(ctx context.Context, req SettingsTestRequest) SettingsTestOutcome {
	snap, entity, key := req.Snapshot, req.Entity, req.Key
	n := f.running.Add(1)
	defer f.running.Add(-1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	var secret, url string
	snap.WithSecret(entity, key, nil, func(v string) error { secret = v; return nil })
	if entity == entityIndexers && snap.Policy.Indexers[key] != nil {
		url = snap.Policy.Indexers[key].URL
	}
	if entity == entityProviders && snap.Policy.Providers[key] != nil {
		url = snap.Policy.Providers[key].URL
	}
	f.mu.Lock()
	f.seen = append(f.seen, entity+"/"+key+" "+url+" "+secret)
	f.mu.Unlock()
	if f.fn != nil {
		return f.fn(ctx)
	}
	n0 := 3
	return SettingsTestOutcome{Status: "ok", Category: "ok", Results: &n0}
}

type settingsEnv struct {
	*env
	coord  *settings.RuntimeCoordinator
	tester *fakeTester
	logs   *syncBuffer
	cookie *http.Cookie
	csrf   string
	// bodies keeps every response body, for the secret scans.
	bodies []string
	dir    string
	// mcpKey, when set, is the API key mcpRPC sends instead of the session.
	mcpKey string
}

type fakeRuntime struct{}

// newSettingsEnv is a web env backed by a real settings store and coordinator whose prepare builds nothing.
func newSettingsEnv(t *testing.T, limits *SettingsTestLimits, edit func(*Config)) *settingsEnv {
	t.Helper()
	return newSettingsEnvBase(t, limits, nil, edit)
}

// newSettingsEnvBase is newSettingsEnv with base changing the YAML policy before the settings bootstrap.
func newSettingsEnvBase(t *testing.T, limits *SettingsTestLimits, base func(*policy.Policy), edit func(*Config)) *settingsEnv {
	t.Helper()
	se := &settingsEnv{tester: &fakeTester{}, logs: &syncBuffer{}}
	se.dir = filepath.Join(t.TempDir(), "policy")
	if err := copyDir(filepath.Join("..", "..", "examples", "policy"), se.dir); err != nil {
		t.Fatal(err)
	}
	lookup := func(name string) (string, bool) { return "fixture-" + name, true }
	se.env = newEnvWith(t, func(c *Config) {
		ctx := context.Background()
		if base != nil {
			base(c.Policy)
		}
		st, err := settings.New(c.Store)
		if err != nil {
			t.Fatal(err)
		}
		st.SetTrashRoot("../settings/testdata/trash")
		snap, err := st.Bootstrap(ctx, c.Policy, settings.ImportRequest{Dir: c.Policy.Dir, Actor: "test", Lookup: lookup})
		if err != nil {
			t.Fatal(err)
		}
		se.coord, err = settings.NewRuntimeCoordinator(st, c.Policy, snap, fakeRuntime{},
			func(context.Context, *settings.Snapshot, *settings.Runtime) (any, error) { return fakeRuntime{}, nil })
		if err != nil {
			t.Fatal(err)
		}
		c.Settings, c.SettingsTester, c.SettingsTests, c.SettingsLookup = se.coord, se.tester, limits, lookup
		c.PolicyDir = se.dir
		c.Logger = slog.New(slog.NewTextHandler(se.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		if edit != nil {
			edit(c)
		}
	})
	se.cookie, se.csrf = se.session(1)
	return se
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
}

// call sends an admin session request with its CSRF token.
func (se *settingsEnv) call(method, path string, body any) *httptest.ResponseRecorder {
	se.t.Helper()
	var rd io.Reader
	switch v := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			se.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, APIBase+"/settings"+path, rd)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(se.cookie)
	r.Header.Set(auth.CSRFHeader, se.csrf)
	rec := se.do(r)
	se.bodies = append(se.bodies, rec.Body.String())
	return rec
}

func (se *settingsEnv) want(rec *httptest.ResponseRecorder, code int, label string) map[string]any {
	se.t.Helper()
	if rec.Code != code {
		se.t.Fatalf("%s: %d, want %d: %s", label, rec.Code, code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func (se *settingsEnv) generation() int64 {
	se.t.Helper()
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		se.t.Fatal(err)
	}
	return snap.Generation
}

func (se *settingsEnv) id(entity, key string) int64 {
	se.t.Helper()
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		se.t.Fatal(err)
	}
	for _, r := range snap.Records.Indexers {
		if entity == entityIndexers && r.Key == key {
			return r.ID
		}
	}
	for _, r := range snap.Records.Providers {
		if entity == entityProviders && r.Key == key {
			return r.ID
		}
	}
	for _, r := range snap.Records.Budgets {
		if entity == entityBudgets && r.Key == key {
			return r.ID
		}
	}
	se.t.Fatalf("no %s %s", entity, key)
	return 0
}

func (se *settingsEnv) storedSecret(entity, key string) string {
	se.t.Helper()
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		se.t.Fatal(err)
	}
	var v string
	if err := snap.WithSecret(entity, key, nil, func(s string) error { v = s; return nil }); err != nil {
		se.t.Fatal(err)
	}
	return v
}

func (se *settingsEnv) noSecretAnywhere() {
	se.t.Helper()
	for i, b := range se.bodies {
		if strings.Contains(b, testSecret) {
			se.t.Errorf("response %d discloses the secret: %s", i, b)
		}
	}
	if strings.Contains(se.logs.String(), testSecret) {
		se.t.Errorf("log discloses the secret: %s", se.logs.String())
	}
	audit, err := se.coord.Audit(context.Background(), 0, 1000)
	if err != nil {
		se.t.Fatal(err)
	}
	b, _ := json.Marshal(audit)
	if strings.Contains(string(b), testSecret) {
		se.t.Errorf("audit discloses the secret")
	}
	var leaked int
	if err := se.st.Reader().QueryRow(`SELECT COUNT(*) FROM settings_audit WHERE before LIKE ? OR after LIKE ?`,
		"%"+testSecret+"%", "%"+testSecret+"%").Scan(&leaked); err != nil || leaked != 0 {
		se.t.Errorf("audit rows with the secret: %d (%v)", leaked, err)
	}
}

func TestSettingsAPIAuthAndCSRF(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	gen := se.generation()
	body := map[string]any{"generation": gen, "key": "new-budget", "config": map[string]any{"requests": 5, "per": "1m"}}
	b, _ := json.Marshal(body)
	send := func(r *http.Request) int {
		r.Header.Set("Content-Type", "application/json")
		return se.do(r).Code
	}
	// No credentials.
	if code := send(httptest.NewRequest("POST", APIBase+"/settings/budgets", bytes.NewReader(b))); code != http.StatusUnauthorized {
		t.Fatalf("anonymous write: %d", code)
	}
	if code := send(httptest.NewRequest("GET", APIBase+"/settings", nil)); code != http.StatusUnauthorized {
		t.Fatalf("anonymous read: %d", code)
	}
	// Admin session without the CSRF token.
	r := httptest.NewRequest("POST", APIBase+"/settings/budgets", bytes.NewReader(b))
	r.AddCookie(se.cookie)
	if code := send(r); code != http.StatusForbidden {
		t.Fatalf("session write without CSRF: %d", code)
	}
	r = httptest.NewRequest("POST", APIBase+"/settings/indexers/testall", nil)
	r.AddCookie(se.cookie)
	r.Header.Set(auth.CSRFHeader, "wrong")
	if code := send(r); code != http.StatusForbidden {
		t.Fatalf("test with a bad CSRF token: %d", code)
	}
	// A manager and a user are refused, reads included.
	for _, role := range []string{auth.RoleManager, auth.RoleUser} {
		uid := se.account("acct-"+role, role, auth.StatusActive, false)
		c, csrf := se.session(uid)
		for _, m := range []string{"GET", "POST"} {
			path := APIBase + "/settings"
			var rd io.Reader
			if m == "POST" {
				path, rd = APIBase+"/settings/budgets", bytes.NewReader(b)
			}
			r := httptest.NewRequest(m, path, rd)
			r.AddCookie(c)
			r.Header.Set(auth.CSRFHeader, csrf)
			if code := send(r); code != http.StatusForbidden {
				t.Fatalf("%s %s as %s: %d", m, path, role, code)
			}
		}
	}
	if se.generation() != gen {
		t.Fatal("a refused request saved settings")
	}
	// The admin session with its token, and an admin API key (no CSRF, like every admin write), get through.
	se.want(se.call("POST", "/budgets", body), http.StatusOK, "admin create")
	body["generation"], body["key"] = gen+1, "key-budget"
	r = httptest.NewRequest("POST", APIBase+"/settings/budgets", strings.NewReader(mustJSON(t, body)))
	r.Header.Set(auth.APIKeyHeader, se.apiKey())
	if code := send(r); code != http.StatusOK {
		t.Fatalf("admin API key write: %d", code)
	}
	if se.generation() != gen+2 {
		t.Fatalf("generation %d, want %d", se.generation(), gen+2)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSettingsAPIStaleGeneration409(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	gen := se.generation()
	id := se.id(entityIndexers, "zilean")
	// Missing generation is a 400, never a blind overwrite.
	se.want(se.call("PUT", fmt.Sprintf("/indexers/%d", id), map[string]any{"enabled": false}), http.StatusBadRequest, "no generation")
	// A stale generation is refused before and after another writer.
	se.want(se.call("PUT", fmt.Sprintf("/indexers/%d", id), map[string]any{"generation": gen - 1, "enabled": false}),
		http.StatusConflict, "stale before")
	se.want(se.call("PUT", fmt.Sprintf("/indexers/%d", id), map[string]any{"generation": gen, "priority": 7}), http.StatusOK, "first writer")
	out := se.want(se.call("PUT", fmt.Sprintf("/indexers/%d", id), map[string]any{"generation": gen, "enabled": false}),
		http.StatusConflict, "second writer with the old generation")
	if out["generation"] != float64(gen+1) {
		t.Fatalf("409 does not name the current generation: %v", out)
	}
	for _, req := range []struct{ method, path string }{{"DELETE", fmt.Sprintf("/budgets/%d?generation=%d", se.id(entityBudgets, "kitsu"), gen)},
		{"PUT", "/indexers/priority"}, {"POST", "/import"}} {
		var body any
		if req.method != "DELETE" {
			body = map[string]any{"generation": gen, "priorities": []map[string]any{{"id": id, "priority": 1}}}
			if req.path == "/import" {
				body = map[string]any{"generation": gen, "orphan_active_downloads": false}
			}
		}
		se.want(se.call(req.method, req.path, body), http.StatusConflict, req.method+" "+req.path)
	}
	// The coordinator's own check (a save racing between read and commit) also answers 409.
	snap, _ := se.coord.Snapshot(context.Background())
	if _, err := se.coord.SaveAndApply(context.Background(), settings.SaveRequest{ExpectedGeneration: snap.Generation - 1,
		Actor: "race", Records: snap.Records}); err == nil {
		t.Fatal("stale coordinator save accepted")
	} else {
		rec := httptest.NewRecorder()
		se.web.settingsError(rec, httptest.NewRequest("GET", "/", nil), err, nil)
		if rec.Code != http.StatusConflict {
			t.Fatalf("coordinator conflict mapped to %d", rec.Code)
		}
	}
	if se.generation() != gen+1 {
		t.Fatalf("generation %d after refused writes, want %d", se.generation(), gen+1)
	}
}

func TestSettingsAPISecretsNeverDisclosed(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, &SettingsTestLimits{Cooldown: -1}, nil)
	gen := se.generation()
	// Create an indexer and a provider with the secret.
	out := se.want(se.call("POST", "/indexers", map[string]any{"generation": gen, "key": "secret-feed", "enabled": true, "secret": testSecret,
		"config": map[string]any{"kind": "torznab", "url": "http://prowlarr:9696/9/api", "privacy": "public", "budget": "prowlarr"}}),
		http.StatusOK, "create indexer")
	rec := out["record"].(map[string]any)
	if rec["secret_set"] != true || rec["enabled"] != true {
		t.Fatalf("created record: %v", rec)
	}
	if _, ok := rec["config"].(map[string]any)["api_key_env"]; ok {
		t.Fatal("config shows api_key_env")
	}
	gen++
	pid := se.id(entityProviders, "ad")
	se.want(se.call("PUT", fmt.Sprintf("/providers/%d", pid), map[string]any{"generation": gen, "secret": testSecret,
		"orphan_active_downloads": false}), http.StatusOK, "rotate provider credential")
	gen++
	if se.storedSecret(entityProviders, "ad") != testSecret || se.storedSecret(entityIndexers, "secret-feed") != testSecret {
		t.Fatal("secrets were not stored")
	}
	// A refused write carrying the secret in a bad URL: the validation error must not echo it.
	se.want(se.call("POST", "/indexers", map[string]any{"generation": gen, "key": "bad-feed", "secret": testSecret,
		"config": map[string]any{"kind": "torznab", "url": "not a url " + testSecret}}), http.StatusUnprocessableEntity, "invalid url")
	se.want(se.call("POST", "/indexers", map[string]any{"generation": gen, "key": "typed", "secret": testSecret,
		"config": map[string]any{"kind": "torznab", "url": "http://x", "page_cap": testSecret}}), http.StatusBadRequest, "type error")
	se.want(se.call("POST", "/indexers", map[string]any{"generation": gen, "key": "env-feed", "config": map[string]any{
		"kind": "torznab", "url": "http://x", "api_key_env": testSecret}}), http.StatusBadRequest, "env name refused")
	// Candidate test with the secret, saved tests, test-all.
	se.want(se.call("POST", "/indexers/test", map[string]any{"key": "cand", "secret": testSecret,
		"config": map[string]any{"kind": "torznab", "url": "http://prowlarr:9696/3/api", "privacy": "public"}}), http.StatusOK, "candidate test")
	se.want(se.call("POST", fmt.Sprintf("/indexers/%d/test", se.id(entityIndexers, "secret-feed")), nil), http.StatusOK, "saved test")
	se.want(se.call("POST", "/providers/testall", nil), http.StatusOK, "provider testall")
	// Every read.
	for _, path := range []string{"", "/indexers", "/providers", "/budgets", fmt.Sprintf("/indexers/%d", se.id(entityIndexers, "secret-feed")),
		fmt.Sprintf("/providers/%d", pid), "/export", "/audit", "/audit?limit=2", "/schema"} {
		se.want(se.call("GET", path, nil), http.StatusOK, "GET "+path)
	}
	se.want(se.call("POST", "/import/preview", map[string]any{"generation": gen}), http.StatusOK, "preview")
	// An apply failure is logged without the request's credential.
	se.web.settingsError(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil),
		fmt.Errorf("constructor said %s", testSecret), []string{testSecret})
	if !strings.Contains(se.logs.String(), "settings: apply failed") {
		t.Fatal("apply failure was not logged")
	}
	audit, _ := se.coord.Audit(context.Background(), 0, 1000)
	tests := 0
	for _, a := range audit {
		if strings.HasPrefix(a.Operation, "test") {
			tests++
		}
	}
	if tests < 3 {
		t.Fatalf("tests audited: %d", tests)
	}
	se.noSecretAnywhere()
}

func TestSettingsAPIKeepsSecretOnUpdate(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	gen := se.generation()
	id := se.id(entityIndexers, "dmm")
	se.want(se.call("PUT", fmt.Sprintf("/indexers/%d", id), map[string]any{"generation": gen, "secret": testSecret}), http.StatusOK, "set")
	gen++
	// A config-only update keeps it, and so does an enable/priority update.
	cfg := map[string]any{"kind": "dmm", "url": "https://debridmediamanager.com/api/torznab/ad/cached", "privacy": "public",
		"budget": "dmm-key", "page_cap": 100}
	out := se.want(se.call("PUT", fmt.Sprintf("/indexers/%d", id), map[string]any{"generation": gen, "config": cfg}), http.StatusOK, "config update")
	gen++
	rec := out["record"].(map[string]any)
	if rec["secret_set"] != true || rec["config"].(map[string]any)["page_cap"] != float64(100) {
		t.Fatalf("after config update: %v", rec)
	}
	se.want(se.call("PUT", fmt.Sprintf("/indexers/%d", id), map[string]any{"generation": gen, "priority": 3, "enabled": false}), http.StatusOK, "flags")
	gen++
	if se.storedSecret(entityIndexers, "dmm") != testSecret {
		t.Fatal("update without a secret changed the stored credential")
	}
	// The retained environment fallback name survives a config replacement although the API never shows it.
	snap, _ := se.coord.Snapshot(context.Background())
	for _, r := range snap.Records.Indexers {
		if r.Key == "dmm" && (r.Config.APIKeyEnv != "THEATARR_DMM_KEY" || r.Priority != 3 || r.Enabled || r.Config.PageCap != 100) {
			t.Fatalf("dmm after updates: %+v", r)
		}
	}
	// An empty secret is ambiguous and refused; clear_secret removes it.
	se.want(se.call("PUT", fmt.Sprintf("/indexers/%d", id), map[string]any{"generation": gen, "secret": ""}), http.StatusBadRequest, "empty")
	out = se.want(se.call("PUT", fmt.Sprintf("/indexers/%d", id), map[string]any{"generation": gen, "clear_secret": true}), http.StatusOK, "clear")
	if out["record"].(map[string]any)["secret_set"] != false || se.storedSecret(entityIndexers, "dmm") != "" {
		t.Fatal("clear_secret kept the credential")
	}
	// Keys cannot be renamed and unknown ids are 404.
	se.want(se.call("PUT", fmt.Sprintf("/indexers/%d", id), map[string]any{"generation": gen + 1, "key": "other"}), http.StatusBadRequest, "rename")
	se.want(se.call("PUT", "/indexers/99999", map[string]any{"generation": gen + 1}), http.StatusNotFound, "unknown id")
	se.noSecretAnywhere()
}

func TestSettingsAPITestBounds(t *testing.T) {
	t.Run("timeout and concurrency", func(t *testing.T) {
		limits := &SettingsTestLimits{Timeout: 80 * time.Millisecond, Concurrency: 2, Cooldown: -1}
		se := newSettingsEnv(t, limits, nil)
		// The tester ignores nothing: it waits for its context, as a hung upstream would.
		se.tester.fn = func(ctx context.Context) SettingsTestOutcome {
			<-ctx.Done()
			return SettingsTestOutcome{Status: "failed", Category: "connectivity"}
		}
		start := time.Now()
		rec := se.want(se.call("POST", fmt.Sprintf("/indexers/%d/test", se.id(entityIndexers, "dmm")), nil), http.StatusOK, "single")
		if rec["status"] != "timeout" || time.Since(start) > 2*time.Second {
			t.Fatalf("single test: %v after %v", rec, time.Since(start))
		}
		start = time.Now()
		out := se.want(se.call("POST", "/indexers/testall", map[string]any{"include_disabled": true}), http.StatusOK, "testall")
		results := out["results"].([]any)
		if len(results) != 6 {
			t.Fatalf("testall results: %d", len(results))
		}
		for _, r := range results {
			if r.(map[string]any)["status"] != "timeout" {
				t.Fatalf("testall result: %v", r)
			}
		}
		if peak := se.tester.peak.Load(); peak != 2 {
			t.Fatalf("peak concurrent tests %d, want the cap 2", peak)
		}
		// Six items, two at a time, 80ms each: about 240ms, never one unbounded wait.
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("testall took %v", d)
		}
	})
	t.Run("slots are shared across requests", func(t *testing.T) {
		// The tester holding the slot is slow to stop: it ignores its timeout and returns only once another request
		// was turned away, so the slot stays taken however slowly a loaded machine starts the other requests.
		limits := &SettingsTestLimits{Timeout: 150 * time.Millisecond, Concurrency: 1, Cooldown: -1}
		se := newSettingsEnv(t, limits, nil)
		release := make(chan struct{})
		var releaseOnce sync.Once
		se.tester.fn = func(context.Context) SettingsTestOutcome {
			<-release
			return SettingsTestOutcome{Status: "ok", Category: "ok"}
		}
		var wg sync.WaitGroup
		codes := make([]int, 3)
		statuses := make([]string, 3)
		for i, key := range []string{"dmm", "zilean", "seadex"} {
			id := se.id(entityIndexers, key)
			wg.Go(func() {
				r := httptest.NewRequest("POST", fmt.Sprintf("%s/settings/indexers/%d/test", APIBase, id), nil)
				r.AddCookie(se.cookie)
				r.Header.Set(auth.CSRFHeader, se.csrf)
				rec := se.do(r)
				codes[i] = rec.Code
				if rec.Code == http.StatusTooManyRequests {
					releaseOnce.Do(func() { close(release) })
				}
				var res SettingsTestResult
				json.Unmarshal(rec.Body.Bytes(), &res)
				statuses[i] = res.Status
			})
		}
		wg.Wait()
		releaseOnce.Do(func() { close(release) })
		if peak := se.tester.peak.Load(); peak != 1 {
			t.Fatalf("peak %d across requests, want 1", peak)
		}
		busy := 0
		for i := range codes {
			if codes[i] == http.StatusTooManyRequests && statuses[i] == "busy" {
				busy++
			}
		}
		if busy == 0 {
			t.Fatalf("no request was told the slots were busy: %v %v", codes, statuses)
		}
	})
	t.Run("cooldown", func(t *testing.T) {
		now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
		limits := &SettingsTestLimits{Cooldown: time.Minute, Now: func() time.Time { return now }}
		se := newSettingsEnv(t, limits, nil)
		path := fmt.Sprintf("/providers/%d/test", se.id(entityProviders, "rd"))
		se.want(se.call("POST", path, nil), http.StatusOK, "first")
		rec := se.call("POST", path, nil)
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "60" {
			t.Fatalf("second test: %d Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
		}
		// Test-all reports the cooled-down source without calling it again.
		calls := len(se.tester.seen)
		out := se.want(se.call("POST", "/providers/testall", nil), http.StatusOK, "testall")
		for _, r := range out["results"].([]any) {
			m := r.(map[string]any)
			if m["key"] == "rd" && m["status"] != "cooldown" {
				t.Fatalf("rd in testall: %v", m)
			}
		}
		if len(se.tester.seen) != calls+len(out["results"].([]any))-1 {
			t.Fatalf("testall called %d sources", len(se.tester.seen)-calls)
		}
		// The last result is shown on the record.
		got := se.want(se.call("GET", fmt.Sprintf("/providers/%d", se.id(entityProviders, "rd")), nil), http.StatusOK, "get")
		if lt := got["record"].(map[string]any)["last_test"]; lt == nil || lt.(map[string]any)["status"] != "ok" {
			t.Fatalf("last_test: %v", got)
		}
	})
}

func TestSettingsAPITestDoesNotSave(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, &SettingsTestLimits{Cooldown: -1}, nil)
	gen := se.generation()
	before, _ := se.coord.Snapshot(context.Background())
	auditBefore, _ := se.coord.Audit(context.Background(), 0, 1000)
	id := se.id(entityIndexers, "dmm")
	// Changes to a saved record, with a new credential, and a new record: tested, never stored.
	cfg := map[string]any{"kind": "dmm", "url": "https://debridmediamanager.com/api/torznab/tb/cached", "privacy": "public", "budget": "dmm-key"}
	out := se.want(se.call("POST", "/indexers/test", map[string]any{"generation": gen, "id": id, "config": cfg, "secret": testSecret}),
		http.StatusOK, "candidate")
	if out["candidate"] != true || out["status"] != "ok" {
		t.Fatalf("candidate result: %v", out)
	}
	se.want(se.call("POST", "/providers/test", map[string]any{"key": "tb2", "enabled": true, "config": map[string]any{
		"kind": "torbox", "order": 9, "url": "https://api.torbox.app", "budget": "torbox-api"}, "secret": testSecret}), http.StatusOK, "new provider")
	// The tester saw the candidate, with the candidate's credential.
	if !strings.Contains(strings.Join(se.tester.seen, "\n"), "indexers/dmm https://debridmediamanager.com/api/torznab/tb/cached "+testSecret) {
		t.Fatalf("tester saw %v", se.tester.seen)
	}
	// An invalid candidate is refused before any outbound call.
	calls := len(se.tester.seen)
	se.want(se.call("POST", "/indexers/test", map[string]any{"id": id, "config": map[string]any{"kind": "dmm", "url": "ftp://x"}}),
		http.StatusUnprocessableEntity, "invalid candidate")
	if len(se.tester.seen) != calls {
		t.Fatal("invalid candidate was tested")
	}
	after, _ := se.coord.Snapshot(context.Background())
	if after.Generation != gen || mustJSON(t, after.Records) != mustJSON(t, before.Records) || se.storedSecret(entityIndexers, "dmm") == testSecret {
		t.Fatal("a test saved settings")
	}
	if se.coord.Current().Generation != gen {
		t.Fatal("a test published a generation")
	}
	auditAfter, _ := se.coord.Audit(context.Background(), 0, 1000)
	for _, a := range auditAfter[len(auditBefore):] {
		if a.Operation != "test-candidate" || a.Generation != gen {
			t.Fatalf("test wrote a non-test audit row: %+v", a)
		}
	}
	// Candidate results never become the saved record's last test.
	got := se.want(se.call("GET", fmt.Sprintf("/indexers/%d", id), nil), http.StatusOK, "get")
	if got["record"].(map[string]any)["last_test"] != nil {
		t.Fatal("candidate test shown as the saved record's status")
	}
	se.noSecretAnywhere()
}

// TestSettingsAPIKillSwitchesUntouchable: nothing here can change router.enabled, library.enabled, notify or a
// version's mode without confirmation: requests naming them are refused, and an import that activates modes is
// refused. An ordinary import still cannot change global switches.
func TestSettingsAPIKillSwitchesUntouchable(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	gen := se.generation()
	id := se.id(entityIndexers, "dmm")
	pid := se.id(entityProviders, "rd")
	refused := []struct {
		method, path string
		body         any
	}{
		{"PUT", fmt.Sprintf("/indexers/%d", id), map[string]any{"generation": gen, "router": map[string]any{"enabled": true}}},
		{"PUT", fmt.Sprintf("/indexers/%d", id), map[string]any{"generation": gen, "config": map[string]any{"kind": "dmm", "url": "https://x", "mode": "active"}}},
		{"POST", "/indexers", map[string]any{"generation": gen, "key": "k", "library": map[string]any{"enabled": true}, "config": map[string]any{"kind": "zilean", "url": "http://z"}}},
		{"PUT", fmt.Sprintf("/providers/%d", pid), map[string]any{"generation": gen, "orphan_active_downloads": false, "config": map[string]any{
			"kind": "realdebrid", "order": 1, "url": "https://api.real-debrid.com/rest/1.0", "router_enabled": true}}},
		{"PUT", fmt.Sprintf("/providers/%d", pid), map[string]any{"generation": gen, "orphan_active_downloads": false, "notify": map[string]any{"enabled": true}}},
		{"POST", "/budgets", map[string]any{"generation": gen, "key": "b", "versions": map[string]any{"movie-web": map[string]any{"mode": "active"}},
			"config": map[string]any{"requests": 1, "per": "1m"}}},
		{"POST", "/import", map[string]any{"generation": gen, "orphan_active_downloads": false, "router": map[string]any{"enabled": true}}},
		{"PUT", "/indexers/priority", map[string]any{"generation": gen, "priorities": []any{}, "mode": "active"}},
	}
	for _, r := range refused {
		rec := se.call(r.method, r.path, r.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown field") {
			t.Errorf("%s %s: %d %s", r.method, r.path, rec.Code, rec.Body)
		}
	}
	if se.generation() != gen {
		t.Fatal("a refused request saved settings")
	}
	// Turn every switch on in the YAML that an explicit import reads.
	edit := func(file, from, to string) {
		path := filepath.Join(se.dir, file)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), from) {
			t.Fatalf("%s has no %q", file, from)
		}
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(b), from, to)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	edit("router.yaml", "\n  enabled: false", "\n  enabled: true")
	edit("notify.yaml", "enabled: false", "enabled: true")
	edit("versions.yaml", "mode: shadow", "mode: active")
	if p, err := policy.Read(se.dir); err != nil || !p.Router.Enabled || !p.Notify.Enabled {
		t.Fatalf("edited fixture: %v", err)
	}
	// The import still changes the records it owns.
	edit("indexers.yaml", "page_cap: 200", "page_cap: 150")
	se.want(se.call("POST", "/import", map[string]any{"generation": gen, "orphan_active_downloads": false}), http.StatusUnprocessableEntity, "unconfirmed active modes")
	if se.generation() != gen || se.coord.Current().Policy.Indexers["dmm"].PageCap != 200 {
		t.Fatal("unconfirmed import changed settings")
	}
	edit("versions.yaml", "mode: active", "mode: shadow")
	body := map[string]any{"generation": gen, "orphan_active_downloads": false}
	proof := se.want(se.call("POST", "/import/confirm", body), 200, "confirm missing sections")
	body["confirmation_token"] = proof["token"]
	out := se.want(se.call("POST", "/import", body), http.StatusOK, "import")
	if out["generation"] != float64(gen+1) {
		t.Fatalf("import: %v", out)
	}
	cur := se.coord.Current().Policy
	if cur.Router.Enabled || cur.Library.Enabled || (cur.Notify != nil && cur.Notify.Enabled) || cur.Indexers["dmm"].PageCap != 150 {
		t.Fatalf("after import: router %v library %v notify %+v page_cap %d", cur.Router.Enabled, cur.Library.Enabled, cur.Notify, cur.Indexers["dmm"].PageCap)
	}
	for k, v := range cur.Versions {
		if v.Mode == policy.ModeActive {
			t.Fatalf("version %s became active through the settings API", k)
		}
	}
	// An import needs the orphan confirmation stated either way.
	se.want(se.call("POST", "/import", map[string]any{"generation": gen + 1}), http.StatusBadRequest, "import without orphan flag")
}

func TestSettingsAPICRUDAndSchema(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	gen := se.generation()
	schema := se.want(se.call("GET", "/schema", nil), http.StatusOK, "schema")
	for _, k := range []string{"indexers", "providers", "budgets", "rules"} {
		if schema[k] == nil {
			t.Fatalf("schema lacks %s", k)
		}
	}
	// Budget create, update, delete.
	out := se.want(se.call("POST", "/budgets", map[string]any{"generation": gen, "key": "extra", "config": map[string]any{"requests": 10, "per": "1m", "hourly": 100}}),
		http.StatusOK, "create budget")
	bid := int64(out["record"].(map[string]any)["id"].(float64))
	gen++
	se.want(se.call("POST", "/budgets", map[string]any{"generation": gen, "key": "extra", "config": map[string]any{"requests": 1, "per": "1m"}}),
		http.StatusBadRequest, "duplicate key")
	se.want(se.call("PUT", fmt.Sprintf("/budgets/%d", bid), map[string]any{"generation": gen, "config": map[string]any{"requests": 0, "per": "1m"}}),
		http.StatusUnprocessableEntity, "invalid budget")
	se.want(se.call("PUT", fmt.Sprintf("/budgets/%d", bid), map[string]any{"generation": gen, "secret": "x"}), http.StatusBadRequest, "budget secret")
	// A budget in use cannot be deleted (validation), an unused one can.
	se.want(se.call("DELETE", fmt.Sprintf("/budgets/%d?generation=%d", se.id(entityBudgets, "dmm-key"), gen), nil), http.StatusUnprocessableEntity, "in use")
	se.want(se.call("DELETE", fmt.Sprintf("/budgets/%d?generation=%d", bid, gen), nil), http.StatusOK, "delete budget")
	gen++
	// A new indexer defaults to disabled; priorities reorder.
	out = se.want(se.call("POST", "/indexers", map[string]any{"generation": gen, "key": "feed2",
		"config": map[string]any{"kind": "zilean", "url": "http://zilean2:8181", "privacy": "public"}}), http.StatusOK, "create indexer")
	if out["record"].(map[string]any)["enabled"] != false {
		t.Fatal("a new indexer was enabled by default")
	}
	gen++
	out = se.want(se.call("PUT", "/indexers/priority", map[string]any{"generation": gen, "priorities": []map[string]any{
		{"id": se.id(entityIndexers, "feed2"), "priority": 0}, {"id": se.id(entityIndexers, "dmm"), "priority": 5}}}), http.StatusOK, "priority")
	gen++
	recs := out["records"].([]any)
	if len(recs) == 0 || recs[len(recs)-1].(map[string]any)["key"] != "dmm" {
		t.Fatalf("priority order: %v", recs)
	}
	se.want(se.call("DELETE", fmt.Sprintf("/indexers/%d?generation=%d", se.id(entityIndexers, "feed2"), gen), nil), http.StatusOK, "delete indexer")
	gen++
	// Provider deletes state the orphan flag; an order that contradicts the versions is refused by validation.
	se.want(se.call("DELETE", fmt.Sprintf("/providers/%d?generation=%d", se.id(entityProviders, "tb"), gen), nil), http.StatusBadRequest, "no flag")
	snap, _ := se.coord.Snapshot(context.Background())
	var order []int64
	for i := len(snap.Records.Providers) - 1; i >= 0; i-- {
		order = append(order, snap.Records.Providers[i].ID)
	}
	se.want(se.call("PUT", "/providers/order", map[string]any{"generation": gen, "order": order[:1]}), http.StatusBadRequest, "partial order")
	se.want(se.call("PUT", "/providers/order", map[string]any{"generation": gen, "order": order}), http.StatusUnprocessableEntity, "order vs versions")
	audit := se.want(se.call("GET", "/audit?limit=3", nil), http.StatusOK, "audit page")
	if len(audit["records"].([]any)) != 3 || audit["next"] == float64(0) {
		t.Fatalf("audit page: %v", audit)
	}
	next := se.want(se.call("GET", fmt.Sprintf("/audit?after=%d&limit=500", int64(audit["next"].(float64))), nil), http.StatusOK, "next page")
	if len(next["records"].([]any)) == 0 || next["next"] != float64(0) {
		t.Fatalf("next page: %v", next)
	}
	// order=desc pages newest first with a before cursor; the default above stays oldest first.
	all := se.want(se.call("GET", "/audit?limit=500", nil), http.StatusOK, "whole audit")["records"].([]any)
	id := func(r any) float64 { return r.(map[string]any)["id"].(float64) }
	desc := se.want(se.call("GET", "/audit?order=desc&limit=2", nil), http.StatusOK, "newest page")
	rows := desc["records"].([]any)
	if len(rows) != 2 || id(rows[0]) != id(all[len(all)-1]) || id(rows[1]) != id(all[len(all)-2]) || desc["next"] != id(rows[1]) {
		t.Fatalf("newest page: %v", desc)
	}
	older := se.want(se.call("GET", fmt.Sprintf("/audit?order=desc&before=%d&limit=500", int64(desc["next"].(float64))), nil), http.StatusOK, "older page")
	if rows := older["records"].([]any); len(rows) != len(all)-2 || id(rows[len(rows)-1]) != id(all[0]) || older["next"] != float64(0) {
		t.Fatalf("older page: %v", older)
	}
	se.want(se.call("GET", "/audit?order=sideways", nil), http.StatusBadRequest, "bad order")
	// A cursor belongs to its order: before with the default order or after with desc is refused, as is a bad one.
	for _, q := range []string{"before=1", "before=bad", "order=asc&before=1", "order=desc&after=1", "order=desc&after=bad", "order=desc&before=bad", "after=bad"} {
		se.want(se.call("GET", "/audit?"+q, nil), http.StatusBadRequest, q)
	}
	ov := se.want(se.call("GET", "", nil), http.StatusOK, "overview")
	if ov["source"] != "db" || ov["generation"] != float64(gen) || ov["active_generation"] != float64(gen) {
		t.Fatalf("overview: %v", ov)
	}
}

// The built-in public sources (torrentio, dmm, zilean, seadex) stored with no privacy read back public; an explicit
// label is kept, and another kind with no privacy stays unset.
func TestSettingsAPIBuiltinSourcesReadPublic(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	gen := se.generation()
	for _, ix := range []struct{ key, kind, url, privacy string }{
		{"torrentio2", "torrentio", "https://torrentio.example.org", ""},
		{"dmm2", "dmm", "https://dmm.example.org/api/torznab/cached", ""},
		{"zilean2", "zilean", "http://zilean2:8181", ""},
		{"seadex2", "seadex", "https://seadex.example.org", ""},
		{"torrentio3", "torrentio", "https://torrentio3.example.org", "private"},
		{"feed2", "torznab", "http://prowlarr:9696/2/api", ""},
	} {
		config := map[string]any{"kind": ix.kind, "url": ix.url}
		if ix.privacy != "" {
			config["privacy"] = ix.privacy
		}
		se.want(se.call("POST", "/indexers", map[string]any{"generation": gen, "key": ix.key, "config": config}),
			http.StatusOK, "create "+ix.key)
		gen++
	}
	got := map[string]any{}
	for _, r := range se.want(se.call("GET", "/indexers", nil), http.StatusOK, "list")["indexers"].([]any) {
		rec := r.(map[string]any)
		got[rec["key"].(string)] = rec["config"].(map[string]any)["privacy"]
	}
	want := map[string]any{"torrentio2": "public", "dmm2": "public", "zilean2": "public", "seadex2": "public",
		"torrentio3": "private", "feed2": ""}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s privacy %v, want %v", k, got[k], w)
		}
	}
}

// The overview repeats the YAML-owned switches for the UI, read-only, from the running policy in both modes.
func TestSettingsAPIOverviewSwitches(t *testing.T) {
	t.Parallel()
	check := func(label string, o SettingsOverview, pol *policy.Policy) {
		t.Helper()
		if o.Switches.RouterEnabled != (pol.Router != nil && pol.Router.Enabled) ||
			o.Switches.LibraryEnabled != (pol.Library != nil && pol.Library.Enabled) ||
			o.Switches.NotifyEnabled != (pol.Notify != nil && pol.Notify.Enabled) {
			t.Errorf("%s: switches %+v", label, o.Switches)
		}
		if len(o.Switches.Versions) != len(pol.Versions) {
			t.Fatalf("%s: %d version modes, want %d", label, len(o.Switches.Versions), len(pol.Versions))
		}
		for _, v := range o.Switches.Versions {
			if pol.Versions[v.Key] == nil || string(pol.Versions[v.Key].Mode) != v.Mode {
				t.Errorf("%s: version %s mode %q", label, v.Key, v.Mode)
			}
		}
	}
	se := newSettingsEnv(t, nil, nil)
	var o SettingsOverview
	if err := json.Unmarshal(se.call("GET", "", nil).Body.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	check("db", o, se.coord.Current().Policy)
	// The versions page carries each version's grab state from the stored settings: the example's five versions run
	// in shadow on a new database, so none grabs.
	var versions struct {
		Switches SettingsSwitches `json:"switches"`
	}
	rec := se.call("GET", "/versions", nil)
	se.want(rec, http.StatusOK, "versions")
	if err := json.Unmarshal(rec.Body.Bytes(), &versions); err != nil {
		t.Fatal(err)
	}
	if len(versions.Switches.Versions) != 5 {
		t.Fatalf("settings versions = %+v", versions.Switches.Versions)
	}
	for _, v := range versions.Switches.Versions {
		if v.GrabState != (VersionGrabState{Mode: "shadow"}) {
			t.Errorf("version %s grab_state = %+v, want shadow and no grab", v.Key, v.GrabState)
		}
	}
}
