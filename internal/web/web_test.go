package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/server"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func init() { auth.SetFastHashing() }

var testNow = time.Date(2026, 10, 26, 12, 30, 0, 0, time.UTC)

// fakeEgress fakes the egress summary; State comes from a real gateway over the test store (built after seeding,
// so it loads the seeded back-offs and budget windows) with the fake summary in place of its own.
type fakeEgress struct {
	mu   sync.Mutex
	ok   bool
	mode string
	gw   *gateway.Gateway
}

func (f *fakeEgress) State() gateway.State {
	s := f.gw.State()
	s.Egress = f.EgressSummary()
	return s
}

func (f *fakeEgress) set(ok bool) { f.mu.Lock(); f.ok = ok; f.mu.Unlock() }

func (f *fakeEgress) EgressSummary() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]any{"mode": f.mode, "vpn_ok": f.ok, "require_vpn_for": []string{"indexers"}, "paths": []map[string]any{
		{"path": "direct", "ok": f.ok, "ip": "203.0.113.7", "checked_at": "2026-10-26T12:29:00Z", "age_s": 60}}}
}

type env struct {
	t      *testing.T
	st     *store.Store
	auth   *auth.Service
	web    *Web
	eg     *fakeEgress
	mux    *http.ServeMux
	pw     string
	policy string
}

const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvWith(t, nil)
}

// newEnvWith is newEnv with a chance to change the web config (logger, Plex sign-in) first.
func newEnvWith(t *testing.T, cfg func(*Config)) *env {
	t.Helper()
	return newEnvAccount(t, cfg, true)
}

// testAdminPassword is the admin password of newEnv's admin account (id 1).
const testAdminPassword = "test admin password"

// newEnvAccount is newEnvWith; withAdmin false leaves the user table empty, as on a new install nobody has opened.
func newEnvAccount(t *testing.T, cfg func(*Config), withAdmin bool) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	au, _, err := auth.New(ctx, st, auth.Options{Logger: log, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	pw := ""
	if withAdmin {
		// The first start creates no account; the admin is made here, as the create page or reset-password would.
		if err := auth.ResetPassword(ctx, st, auth.AdminUsername, testAdminPassword); err != nil {
			t.Fatal(err)
		}
		pw = testAdminPassword
	}
	dir := filepath.Join("..", "..", "examples", "policy")
	pol, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	eg := &fakeEgress{ok: true}
	c := Config{Store: st, Auth: au, Policy: pol, Egress: eg, Logger: log, Version: "test",
		Now: func() time.Time { return testNow }, PollInterval: 20 * time.Millisecond, UI: testUI, Logs: testLogs}
	if cfg != nil {
		cfg(&c)
	}
	w, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	w.Mount(mux)
	e := &env{t: t, st: st, auth: au, web: w, eg: eg, mux: mux, pw: pw, policy: dir}
	e.seed()
	if eg.gw, err = gateway.New(ctx, pol, gateway.Options{Store: st, Logger: log,
		Now: func() time.Time { return testNow }}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer().Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

// seed: title 1 "Alpha" with movie-remux (file, hash A) and movie-web (wanted, no file); title 2 "Beta" with
// movie-web (file); title 3 "Gamma" series with no versions.
func (e *env) seed() {
	now := store.Millis(testNow)
	e.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, imdb_id, created_at, updated_at) VALUES
		(1, 'movie', 'Alpha', 2020, 101, 'tt0000101', ?, ?), (2, 'movie', 'Beta', 2021, 102, NULL, ?, ?),
		(3, 'series', 'Gamma', 2019, NULL, NULL, ?, ?)`, now, now, now, now, now, now)
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES
		(1, 1, 'movie-remux', 'cutoff_met', ?, ?), (2, 1, 'movie-web', 'wanted', ?, ?), (3, 2, 'movie-web', 'filled', ?, ?)`,
		now, now, now, now, now, now)
	e.exec(`INSERT INTO version_source (version_id, instance, arr_id) VALUES (1, 'radarr_4k_debrid', 11)`)
	e.exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES
		(1, ?, 'Alpha.2020.2160p.REMUX', 'unknown', ?, ?)`, hashA, now, now)
	e.exec(`INSERT INTO content_version (content_id, version_id, release_title, source, grabbed_at, imported_at) VALUES
		(1, 1, 'Alpha.2020.2160p.BluRay.REMUX-GRP', 'arr_history', ?, ?)`, now-1000, now)
	e.exec(`INSERT INTO version_file (version_id, title_id, instance, arr_file_id, path, size_bytes, quality,
		languages, custom_format_score, release_title, content_id) VALUES
		(1, 1, 'radarr_4k_debrid', 501, '/movies/Alpha (2020)/Alpha.mkv', 50000000000, 'Remux-2160p', '["English"]', 3200,
		'Alpha.2020.2160p.BluRay.REMUX-GRP', 1),
		(3, 2, 'radarr_4k_web', 502, '/movies/Beta (2021)/Beta.mkv', 9000000000, 'WEBDL-2160p', '[]', 1500, NULL, NULL)`)
	e.exec(`INSERT INTO job (kind, state, run_at, created_at, updated_at) VALUES ('db.nightly', 'queued', ?, ?, ?),
		('db.nightly', 'failed', ?, ?, ?)`, now+3600000, now, now, now, now, now)
	e.exec(`UPDATE job SET last_error = 'disk full' WHERE state = 'failed'`)
	e.exec(`INSERT INTO gateway_backoff (subject, until_at, failures, reason, updated_at) VALUES
		('indexer:dmm', ?, 3, '503 from upstream', ?), ('budget:seadex', ?, 1, '429', ?)`, now+600000, now, now-1000, now)
	hour := store.Millis(testNow.Truncate(time.Hour))
	day := store.Millis(time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	e.exec(`INSERT INTO gateway_budget_window (budget, period, window_start_at, count) VALUES
		('seadex', 'hour', ?, 42), ('torrentio', 'day', ?, 5000)`, hour, day)
}

// login signs in through the same JSON endpoint as the React login page.
func (e *env) login() *http.Cookie {
	e.t.Helper()
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": e.pw})
	r := httptest.NewRequest("POST", APIBase+"/auth/login", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	rec := e.do(r)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	c := findCookie(rec, auth.SessionCookie)
	if c == nil {
		e.t.Fatal("no session cookie")
	}
	return c
}

func (e *env) do(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, r)
	return rec
}

func (e *env) get(path string, c *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	if c != nil {
		r.AddCookie(c)
	}
	return e.do(r)
}

func (e *env) apiKey() string {
	k, _, err := e.auth.CreateKey(context.Background(), "test-client", 1)
	if err != nil {
		e.t.Fatal(err)
	}
	return k
}

func (e *env) getKey(path, key string, v any) int {
	e.t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set(auth.APIKeyHeader, key)
	rec := e.do(r)
	if v != nil && rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			e.t.Fatalf("%s: %v\n%s", path, err, rec.Body.String())
		}
	}
	return rec.Code
}

func findCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// concrete turns a route pattern into a request path.
func concrete(pattern string) (method, path string) {
	method, path, _ = strings.Cut(pattern, " ")
	path = strings.NewReplacer("{id}", "1", "{hash}", hashA, "{$}", "", "{slug}", "x").Replace(path)
	return method, path
}

// TestEveryRouteNeedsAuth: every route not marked public refuses a request without credentials, with a wrong key,
// and with a revoked key; session-only routes refuse API keys. The public set is pinned, so a new public route has
// to be added here on purpose.
func TestEveryRouteNeedsAuth(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	revoked, info, _ := e.auth.CreateKey(ctx, "old", 1)
	e.auth.RevokeKey(ctx, info.ID, 0)
	good := e.apiKey()

	wantPublic := map[string]bool{"POST " + APIBase + "/auth/login": true, "/": true, "GET " + APIBase + "/auth/plex": true,
		"POST " + APIBase + "/auth/plex/start": true, "POST " + APIBase + "/auth/plex/poll": true,
		"GET " + APIBase + "/auth/first-account": true, "POST " + APIBase + "/auth/first-account": true}
	for _, rd := range pageRedirects {
		wantPublic["GET "+rd.from] = true
	}
	routes := e.web.Routes()
	if len(routes) < 25 {
		t.Fatalf("only %d routes registered", len(routes))
	}
	for _, rt := range routes {
		if (rt.Access == Public) != wantPublic[rt.Pattern] {
			t.Errorf("%s: access %v, public set says %v", rt.Pattern, rt.Access, wantPublic[rt.Pattern])
		}
		if rt.Access == Public {
			continue
		}
		method, path := concrete(rt.Pattern)
		for _, key := range []string{"", "theatarr_wrong", revoked} {
			r := httptest.NewRequest(method, path, nil)
			if key != "" {
				r.Header.Set(auth.APIKeyHeader, key)
			}
			// A query-string key is never enough.
			if key == "" {
				r.URL.RawQuery = "apikey=" + url.QueryEscape(good)
			}
			rec := e.do(r)
			switch {
			case rt.API && rec.Code != http.StatusUnauthorized:
				t.Errorf("%s %s key=%q: %d, want 401", method, path, key, rec.Code)
			case !rt.API && (rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login")):
				t.Errorf("%s %s key=%q: %d %s, want a redirect to /login", method, path, key, rec.Code,
					rec.Header().Get("Location"))
			}
		}
		if rt.Access == SessionOnly {
			r := httptest.NewRequest(method, path, nil)
			r.Header.Set(auth.APIKeyHeader, good)
			if rec := e.do(r); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s with an API key: %d, want 403", method, path, rec.Code)
			}
		}
	}
}

func TestServerKeepsHealthOpen(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, c := range []struct {
		pol        MetricsPolicy
		remote     string
		key        bool
		wantMetric int
	}{
		{MetricsOpen, "198.51.100.1:5000", false, 200},
		{MetricsLAN, "192.168.1.50:5000", false, 200},
		{MetricsLAN, "198.51.100.1:5000", false, 403},
		{MetricsKey, "192.168.1.50:5000", false, 401},
		{MetricsKey, "192.168.1.50:5000", true, 200},
	} {
		srv := server.New(server.Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test",
			MetricsGuard: MetricsGuard(c.pol, e.auth)})
		e.web.Mount(srv.Mux())
		h := srv.Handler()
		call := func(path string, key bool) int {
			r := httptest.NewRequest("GET", path, nil)
			r.RemoteAddr = c.remote
			if key {
				r.Header.Set(auth.APIKeyHeader, e.apiKeyNamed(string(c.pol)+c.remote))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			return rec.Code
		}
		if code := call("/healthz", false); code != 200 {
			t.Errorf("%s: /healthz %d", c.pol, code)
		}
		if code := call("/readyz", false); code != 200 {
			t.Errorf("%s: /readyz %d", c.pol, code)
		}
		if code := call("/metrics", c.key); code != c.wantMetric {
			t.Errorf("%s from %s key=%v: /metrics %d, want %d", c.pol, c.remote, c.key, code, c.wantMetric)
		}
		if code := call(APIBase+"/titles", false); code != 401 {
			t.Errorf("%s: API %d", c.pol, code)
		}
	}
}

func (e *env) apiKeyNamed(name string) string {
	name = regexp.MustCompile(`[^A-Za-z0-9._-]`).ReplaceAllString(name, "-")
	k, _, err := e.auth.CreateKey(context.Background(), name, 1)
	if err != nil {
		e.t.Fatal(err)
	}
	return k
}

func TestAPILoginKeysAndMe(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	post := func(path, body, ctype string, c *http.Cookie, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		if ctype != "" {
			r.Header.Set("Content-Type", ctype)
		}
		if c != nil {
			r.AddCookie(c)
		}
		if csrf != "" {
			r.Header.Set(auth.CSRFHeader, csrf)
		}
		return e.do(r)
	}
	body := `{"username":"admin","password":"` + e.pw + `"}`
	if rec := post(APIBase+"/auth/login", body, "application/x-www-form-urlencoded", nil, ""); rec.Code != 403 {
		t.Errorf("form-encoded API login: %d", rec.Code)
	}
	if rec := post(APIBase+"/auth/login", `{"username":"admin","password":"x"}`, "application/json", nil, ""); rec.Code != 401 {
		t.Errorf("wrong password: %d", rec.Code)
	}
	rec := post(APIBase+"/auth/login", body, "application/json", nil, "")
	if rec.Code != 200 {
		t.Fatalf("API login: %d %s", rec.Code, rec.Body.String())
	}
	var lr struct {
		CSRF string `json:"csrf_token"`
	}
	json.Unmarshal(rec.Body.Bytes(), &lr)
	c := findCookie(rec, auth.SessionCookie)

	if rec := post(APIBase+"/keys", `{"name":"seerr"}`, "application/json", c, ""); rec.Code != 403 {
		t.Errorf("create key without csrf: %d", rec.Code)
	}
	rec = post(APIBase+"/keys", `{"name":"seerr"}`, "application/json", c, lr.CSRF)
	if rec.Code != 201 {
		t.Fatalf("create key: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Key  string       `json:"key"`
		Info auth.KeyInfo `json:"info"`
	}
	json.Unmarshal(rec.Body.Bytes(), &created)
	if rec := post(APIBase+"/keys", `{"name":"seerr"}`, "application/json", c, lr.CSRF); rec.Code != 409 {
		t.Errorf("duplicate key: %d", rec.Code)
	}
	var me map[string]any
	if code := e.getKey(APIBase+"/auth/me", created.Key, &me); code != 200 || me["kind"] != "apikey" || me["key_name"] != "seerr" {
		t.Errorf("me with key: %d %v", code, me)
	}
	// Revoke via DELETE with the session.
	r := httptest.NewRequest("DELETE", APIBase+"/keys/"+itoa(created.Info.ID), nil)
	r.AddCookie(c)
	r.Header.Set(auth.CSRFHeader, lr.CSRF)
	if rec := e.do(r); rec.Code != 204 {
		t.Errorf("revoke: %d", rec.Code)
	}
	if code := e.getKey(APIBase+"/titles", created.Key, nil); code != 401 {
		t.Errorf("revoked key: %d", code)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestAPITitles(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	var page TitlePage
	if code := e.getKey(APIBase+"/titles", key, &page); code != 200 || page.Total != 3 {
		t.Fatalf("all titles: %d %+v", code, page)
	}
	names := func(p TitlePage) string {
		var s []string
		for _, it := range p.Items {
			s = append(s, it.Name)
		}
		return strings.Join(s, ",")
	}
	for q, want := range map[string]string{
		"kind=movie":                           "Alpha,Beta",
		"kind=series":                          "Gamma",
		"without=movie-web":                    "Alpha,Gamma",
		"without=movie-web&kind=movie":         "Alpha",
		"with=movie-remux":                     "Alpha",
		"with=movie-web":                       "Beta",
		"state=wanted":                         "Alpha",
		"q=et":                                 "Beta",
		"q=%25":                                "",
		"limit=1&offset=1":                     "Beta",
		"with=movie-remux&without=movie-remux": "",
	} {
		var p TitlePage
		if code := e.getKey(APIBase+"/titles?"+q, key, &p); code != 200 {
			t.Errorf("%s: %d", q, code)
			continue
		}
		if got := names(p); got != want {
			t.Errorf("%s: %q, want %q", q, got, want)
		}
	}
	for _, bad := range []string{"kind=film", "state=x", "limit=-1", "offset=a"} {
		if code := e.getKey(APIBase+"/titles?"+bad, key, nil); code != 400 {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}
	// Coverage on list rows.
	var p TitlePage
	e.getKey(APIBase+"/titles?q=Alpha", key, &p)
	vs := p.Items[0].Versions
	if len(vs) != 2 || vs[0].Key != "movie-remux" || !vs[0].HasFile || vs[1].HasFile {
		t.Errorf("Alpha versions: %+v", vs)
	}

	var d TitleDetail
	if code := e.getKey(APIBase+"/titles/1", key, &d); code != 200 {
		t.Fatalf("title 1: %d", code)
	}
	if len(d.Versions) != 2 || len(d.Versions[0].Files) != 1 || !d.Versions[0].Files[0].Current ||
		d.Versions[0].Files[0].InfoHash != hashA || d.Versions[0].Source != "radarr_4k_debrid" {
		t.Errorf("title versions: %+v", d.Versions)
	}
	if len(d.Registry) != 1 || d.Registry[0].ReleaseTitle != "Alpha.2020.2160p.BluRay.REMUX-GRP" {
		t.Errorf("title registry: %+v", d.Registry)
	}
	if code := e.getKey(APIBase+"/titles/99", key, nil); code != 404 {
		t.Errorf("missing title: %d", code)
	}
	if code := e.getKey(APIBase+"/titles/abc", key, nil); code != 404 {
		t.Errorf("bad id: %d", code)
	}
}

func TestAPIOther(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()

	var vl struct{ Items []Version }
	if code := e.getKey(APIBase+"/versions?key=movie-web&has_file=false", key, &vl); code != 200 || len(vl.Items) != 1 ||
		vl.Items[0].TitleName != "Alpha" {
		t.Errorf("versions: %d %+v", code, vl)
	}
	var v Version
	if code := e.getKey(APIBase+"/versions/3", key, &v); code != 200 || v.TitleName != "Beta" || !v.HasFile {
		t.Errorf("version 3: %d %+v", code, v)
	}

	var c Content
	if code := e.getKey(APIBase+"/registry/"+strings.ToUpper(hashA), key, &c); code != 200 {
		t.Fatalf("registry: %d", code)
	}
	if len(c.Versions) != 1 || c.Versions[0].TitleName != "Alpha" || len(c.ArrFiles) != 1 {
		t.Errorf("content: %+v", c)
	}
	if code := e.getKey(APIBase+"/registry/xyz", key, nil); code != 400 {
		t.Errorf("bad hash: %d", code)
	}
	if code := e.getKey(APIBase+"/registry/"+strings.Repeat("b", 40), key, nil); code != 404 {
		t.Errorf("unknown hash: %d", code)
	}
	var rl struct {
		Total int
		Items []ContentRow
	}
	if code := e.getKey(APIBase+"/registry?q=aaaa", key, &rl); code != 200 || rl.Total != 1 || rl.Items[0].Versions != 1 {
		t.Errorf("registry list: %d %+v", code, rl)
	}
	// Every word: in the name, or a hash prefix.
	for q, want := range map[string]int{"remux+alpha": 1, "AAAA+2160p": 1, "alpha+zzz": 0} {
		if code := e.getKey(APIBase+"/registry?q="+q, key, &rl); code != 200 || rl.Total != want {
			t.Errorf("registry q=%s: %d total %d, want %d", q, code, rl.Total, want)
		}
	}

	var jl JobList
	if code := e.getKey(APIBase+"/jobs", key, &jl); code != 200 || jl.Counts["queued"] != 1 || jl.Counts["failed"] != 1 ||
		jl.Counts["running"] != 0 || len(jl.Items) != 2 || jl.Items[0].State != "queued" {
		t.Errorf("jobs: %d %+v", code, jl)
	}
	if code := e.getKey(APIBase+"/jobs?state=failed", key, &jl); code != 200 || len(jl.Items) != 1 || jl.Items[0].LastError != "disk full" {
		t.Errorf("failed jobs: %+v", jl)
	}

	var gs GatewayState
	if code := e.getKey(APIBase+"/gateway", key, &gs); code != 200 {
		t.Fatalf("gateway: %d", code)
	}
	if gs.Egress["vpn_ok"] != true {
		t.Errorf("egress: %v", gs.Egress)
	}
	byKey := map[string]BudgetUse{}
	for _, b := range gs.Budgets {
		byKey[b.Key] = b
	}
	if b := byKey["seadex"]; b.UsedHour != 42 || b.Hourly != 600 || b.Backoff == nil || b.Backoff.Active {
		t.Errorf("seadex budget: %+v", b)
	}
	if b := byKey["torrentio"]; b.UsedDay != 5000 || !b.DayExceeded {
		t.Errorf("torrentio budget: %+v", b)
	}
	if b := byKey["dmm-key"]; len(b.UsedBy) == 0 || b.UsedBy[0] != "indexer:dmm" {
		t.Errorf("dmm budget users: %+v", b)
	}
	var active int
	for _, b := range gs.Backoffs {
		if b.Active {
			active++
			if b.Subject != "indexer:dmm" {
				t.Errorf("active back-off %s", b.Subject)
			}
		}
	}
	if active != 1 {
		t.Errorf("backoffs: %+v", gs.Backoffs)
	}

	var ps PolicySummary
	if code := e.getKey(APIBase+"/policy", key, &ps); code != 200 || len(ps.Versions) != 5 || len(ps.Hosts) == 0 {
		t.Errorf("policy: %d %+v", code, ps)
	}
	var vr ValidateResult
	if code := e.getKey(APIBase+"/policy/validate", key, &vr); code != 200 || !vr.OK {
		t.Errorf("validate: %d %+v", code, vr)
	}
	var spec map[string]any
	if code := e.getKey(APIBase+"/openapi.json", key, &spec); code != 200 || spec["openapi"] != "3.1.0" {
		t.Errorf("openapi: %d", code)
	}
	var st Status
	if code := e.getKey(APIBase+"/status", key, &st); code != 200 || st.Counts["title"] != 3 ||
		st.PolicyRevision == "" || st.PolicyRevision != e.web.pol.Revision || st.Health != "" {
		t.Errorf("status: %d %+v", code, st)
	}
	var hr HealthReport
	if code := e.getKey(APIBase+"/health", key, &hr); code != 200 || hr.Status != "ok" || len(hr.Checks) != 0 {
		t.Errorf("health without checks: %d %+v", code, hr)
	}
	fh := &fakeChecker{rs: []health.Result{{Source: "MountCheck", Name: "Mount", Type: health.OK},
		{Source: "BackupCheck", Name: "Backup", Type: health.Warning, Message: "The newest backup is 2d old"}}}
	e.web.health = fh
	if code := e.getKey(APIBase+"/health?refresh=1", key, &hr); code != 200 || hr.Status != "warning" ||
		len(hr.Checks) != 2 || hr.Checks[1].Message != "The newest backup is 2d old" || fh.refreshed != 1 {
		t.Errorf("health: %d %+v", code, hr)
	}
	if code := e.getKey(APIBase+"/status", key, &st); code != 200 || st.Health != "warning" {
		t.Errorf("status health: %d %+v", code, st)
	}
}

type fakeChecker struct {
	rs        []health.Result
	refreshed int
}

func (f *fakeChecker) Results(context.Context) []health.Result { return f.rs }
func (f *fakeChecker) Refresh()                                { f.refreshed++ }

// TestOpenAPICoversRoutes keeps the hand-written spec in step with the API routes.
func TestOpenAPICoversRoutes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var spec struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, rt := range e.web.Routes() {
		if !rt.API {
			continue
		}
		method, path, _ := strings.Cut(rt.Pattern, " ")
		p := strings.TrimPrefix(path, APIBase)
		seen[strings.ToLower(method)+" "+p] = true
		if _, ok := spec.Paths[p][strings.ToLower(method)]; !ok {
			t.Errorf("openapi.json lacks %s %s", method, p)
		}
	}
	for p, ops := range spec.Paths {
		for m := range ops {
			if !seen[m+" "+p] {
				t.Errorf("openapi.json documents %s %s, which is not routed", m, p)
			}
		}
	}
}

// TestOpenAPIDeclaresSettingsUnavailable checks the spec lists 503 on every settings endpoint that answers it while
// the settings service is missing (refuseUnavailable).
func TestOpenAPIDeclaresSettingsUnavailable(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	var spec struct {
		Paths map[string]map[string]struct {
			Responses map[string]any `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(openapiJSON, &spec); err != nil {
		t.Fatal(err)
	}
	param := regexp.MustCompile(`\{[^}]+\}`)
	var refused, undeclared []string
	for _, rt := range e.web.Routes() {
		method, path, _ := strings.Cut(rt.Pattern, " ")
		p := strings.TrimPrefix(path, APIBase)
		if !rt.API || !strings.HasPrefix(p, "/settings") && p != "/nowplaying/rclone/test" {
			continue
		}
		r := httptest.NewRequest(method, APIBase+param.ReplaceAllString(p, "1"), strings.NewReader(`{"generation":1}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(auth.APIKeyHeader, key)
		rec := e.do(r)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "settings service unavailable") {
			continue
		}
		op := strings.ToLower(method) + " " + p
		refused = append(refused, op)
		if _, ok := spec.Paths[p][strings.ToLower(method)].Responses["503"]; !ok {
			undeclared = append(undeclared, op)
		}
	}
	if len(refused) < 50 || len(undeclared) != 0 {
		t.Fatalf("%d endpoints answer 503 without settings; openapi.json lacks it on %d:\n%s", len(refused), len(undeclared),
			strings.Join(undeclared, "\n"))
	}
}

func TestPolicyValidateReportsProblems(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.web.policyDir = t.TempDir() // empty: no policy files
	res := e.web.validatePolicy()
	if res.OK || len(res.Problems) == 0 {
		t.Errorf("empty dir validated: %+v", res)
	}
}

func TestEvents(t *testing.T) {
	e := newEnv(t)
	key := e.apiKey()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { e.web.Run(ctx); close(done) }()
	ts := httptest.NewServer(e.mux)
	defer ts.Close()

	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+APIBase+"/events", nil)
	req.Header.Set(auth.APIKeyHeader, key)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	events := make(chan [2]string, 32)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var name string
		for sc.Scan() {
			line := sc.Text()
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				name = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok {
				events <- [2]string{name, v}
			}
		}
		close(events)
	}()
	// next waits for an event named want whose data contains every part, skipping others. One poll reads the
	// changed jobs and the counts in two queries, so a change can show in two events; the test waits for the one
	// that has it all.
	next := func(want string, parts ...string) {
		t.Helper()
		timeout := time.After(5 * time.Second)
		var seen []string
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					t.Fatalf("stream ended waiting for %s %v; saw %v", want, parts, seen)
				}
				seen = append(seen, ev[0]+" "+ev[1])
				if ev[0] != want {
					continue
				}
				all := true
				for _, p := range parts {
					all = all && strings.Contains(ev[1], p)
				}
				if all {
					return
				}
			case <-timeout:
				t.Fatalf("no %s event with %v; saw %v", want, parts, seen)
			}
		}
	}
	next("jobs", `"queued":1`)
	next("gateway", `"vpn_ok":true`)
	// A job change and an egress change each produce an event. Wait for the poller's baseline first, so the change
	// comes after it.
	deadline := time.Now().Add(5 * time.Second)
	for e.web.polls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the poller took no baseline")
		}
		time.Sleep(2 * time.Millisecond)
	}
	e.exec(`INSERT INTO job (kind, state, run_at, created_at, updated_at) VALUES ('x.test', 'running', ?, ?, ?)`,
		store.Millis(testNow), store.Millis(testNow), store.Millis(testNow)+1)
	next("jobs", `"running":1`, "x.test")
	e.eg.set(false)
	next("gateway", `"vpn_ok":false`)
	// Shutting down ends the stream.
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}

// The event stream rechecks its credential at each ping: a revoked key ends it.
func TestEventsEndWhenKeyRevoked(t *testing.T) {
	old := eventPing
	eventPing = 20 * time.Millisecond
	t.Cleanup(func() { eventPing = old })
	e := newEnv(t)
	key := e.apiKey()
	ts := httptest.NewServer(e.mux)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+APIBase+"/events", nil)
	req.Header.Set(auth.APIKeyHeader, key)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("events: %d", resp.StatusCode)
	}
	e.exec(`UPDATE api_key SET revoked_at = 1`)
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("stream did not end after the key was revoked: %v", err)
	}
}

func TestAPILoginThrottled(t *testing.T) {
	e := newEnv(t)
	var last int
	for range 7 {
		r := httptest.NewRequest("POST", APIBase+"/auth/login", strings.NewReader(`{"username":"admin","password":"guess"}`))
		r.Header.Set("Content-Type", "application/json")
		rec := e.do(r)
		last = rec.Code
		if last == http.StatusTooManyRequests && rec.Header().Get("Retry-After") == "" {
			t.Error("429 without Retry-After")
		}
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("after 7 failures: %d, want 429", last)
	}
}

func TestAPILogoutCSRF(t *testing.T) {
	e := newEnv(t)
	c := e.login()
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie flags: %+v", c)
	}
	var me struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(e.get(APIBase+"/auth/me", c).Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"http://evil.example", ""} {
		r := httptest.NewRequest("POST", APIBase+"/auth/logout", nil)
		r.AddCookie(c)
		r.Header.Set(auth.CSRFHeader, me.CSRF)
		r.Header.Set("Origin", origin)
		want := http.StatusNoContent
		if origin != "" {
			want = http.StatusForbidden
		}
		if rec := e.do(r); rec.Code != want {
			t.Fatalf("logout origin %q: %d %s", origin, rec.Code, rec.Body.String())
		}
	}
	if rec := e.get(APIBase+"/auth/me", c); rec.Code != http.StatusUnauthorized {
		t.Errorf("session still valid: %d", rec.Code)
	}
}

// airAtMidnight gives every dated episode an air instant, midnight UTC of its air date, as a metadata refresh without
// an air time does; "aired" reads the instant.
func airAtMidnight(exec func(string, ...any)) {
	exec(`UPDATE episode SET air_date_utc_at = CAST(strftime('%s', air_date) AS INTEGER) * 1000 WHERE air_date IS NOT NULL`)
}
