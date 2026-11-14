package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/logring"
)

var (
	testUI   = fstest.MapFS{"index.html": {Data: []byte(`<!doctype html><title>spa</title><script type="module" src="/assets/app-1.js"></script>`)}}
	testLogs = logring.New(100)
)

func TestOldPathsRedirect(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for from, to := range map[string]string{
		"/library?without=movie-web":        "/movies?without=movie-web",
		"/library?kind=series&state=wanted": "/series?state=wanted",
		"/titles/7":                         "/title/7",
		"/registry?q=abc":                   "/admin/torrents/inventory?q=abc",
		"/registry/" + hashA:                "/admin/torrents/inventory/" + hashA,
		"/settings":                         "/admin/settings",
		"/anime/re-zero":                    "/admin/reviews/anime/re-zero",
	} {
		rec := e.get(from, nil)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != to {
			t.Errorf("%s: %d %q, want %q", from, rec.Code, rec.Header().Get("Location"), to)
		}
	}
}

func TestSPACatchAll(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, p := range []string{"/", "/login", "/admin/system", "/admin/jobs", "/anime", "/movies", "/title/1"} {
		rec := e.get(p, nil)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>spa</title>") {
			t.Errorf("%s: %d", p, rec.Code)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") ||
			!strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") {
			t.Errorf("%s: CSP %q", p, csp)
		}
	}
	for _, path := range []string{"/classic/", "/classic/login", "/classic/shadow/1", "/classic/anime/example", "/static/app.css"} {
		if rec := e.get(path, nil); rec.Code != http.StatusNotFound {
			t.Errorf("removed page %s: %d", path, rec.Code)
		}
	}
	// An unknown API path is a JSON 404, not the app.
	key := e.apiKey()
	r := httptest.NewRequest("GET", APIBase+"/nope", nil)
	r.Header.Set(auth.APIKeyHeader, key)
	if rec := e.do(r); rec.Code != 404 || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("unknown API path: %d %s", rec.Code, rec.Body.String())
	}
	c := e.login()
	// A state-changing request on a session without the CSRF header is refused, on the API and on the catch-all.
	for _, p := range []string{APIBase + "/auth/logout", "/"} {
		r := httptest.NewRequest("POST", p, nil)
		r.AddCookie(c)
		if rec := e.do(r); rec.Code != http.StatusForbidden {
			t.Errorf("POST %s without CSRF: %d", p, rec.Code)
		}
	}
}

func TestAPISourcesLogsMe(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	now := testNow.UnixMilli()
	e.exec(`INSERT INTO content_provider (content_id, provider, seen_at, state) VALUES (1, 'rd', ?, 'present')`, now)
	e.exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES
		(2, ?, 'Orphan', 'unreferenced', ?, ?)`, strings.Repeat("c", 40), now, now)
	e.exec(`INSERT INTO content_provider (content_id, provider, seen_at, state) VALUES (2, 'rd', ?, 'present')`, now)
	e.exec(`INSERT INTO provider_sync_run (provider, started_at, finished_at, ok, error, summary) VALUES
		('ad', ?, ?, 0, 'AUTH_BLOCKED', '{}'), ('rd', ?, ?, 1, NULL, '{"present":2}')`, now-2000, now-1000, now-2000, now-1000)
	e.exec(`INSERT INTO router_pacing (provider, window_adds, backoff_until, failures, reason, updated_at) VALUES
		('rd', 2, ?, 1, 'HTTP 503', ?)`, now+60000, now)

	var s Sources
	if code := e.getKey(APIBase+"/sources", key, &s); code != 200 {
		t.Fatalf("sources: %d", code)
	}
	if len(s.Indexers) != len(e.web.pol.Indexers) || len(s.Providers) != 3 || s.Providers[0].Key != "rd" {
		t.Fatalf("sources: %+v", s)
	}
	rd := s.Providers[0]
	if rd.Counts["present"] != 2 || rd.Record != nil || rd.LastSync == nil || !rd.LastSync.OK ||
		rd.LastOKSync == nil || rd.Pacing == nil || rd.Pacing.WindowAdds != 2 || rd.Pacing.BackoffUntil == nil ||
		len(rd.Versions) == 0 {
		t.Errorf("rd: %+v", rd)
	}
	for _, p := range s.Providers {
		if p.Key == "ad" && (p.LastSync == nil || p.LastSync.OK || p.LastSync.Error != "AUTH_BLOCKED" || p.LastOKSync != nil) {
			t.Errorf("ad: %+v", p)
		}
	}
	for _, ix := range s.Indexers {
		if ix.Key == "dmm" && (ix.Backoff == nil || !ix.Backoff.Active || ix.Backoff.Reason != "503 from upstream") {
			t.Errorf("dmm: %+v", ix)
		}
	}

	// Its own ring: the package's testLogs is shared by every test and outlives a -count run.
	e.web.logs = logring.New(100)
	l := slog.New(e.web.logs.Handler(slog.NewTextHandler(io.Discard, nil))).With("component", "spa-test")
	l.Info("hello from the test", "password", "secret-value")
	var lp LogPage
	if code := e.getKey(APIBase+"/logs?component=spa-test", key, &lp); code != 200 || len(lp.Items) != 1 ||
		lp.Items[0].Message != "hello from the test" || lp.Items[0].Attrs["password"] != "[redacted]" {
		t.Errorf("logs: %d %+v", code, lp)
	}
	if code := e.getKey(APIBase+"/logs?level=loud", key, nil); code != 400 {
		t.Errorf("bad level: %d", code)
	}

	var me map[string]any
	r := httptest.NewRequest("GET", APIBase+"/auth/me", nil)
	r.AddCookie(e.login())
	rec := e.do(r)
	json.Unmarshal(rec.Body.Bytes(), &me)
	if me["role"] != "admin" || me["csrf_token"] == "" {
		t.Errorf("me: %v", me)
	}
}

func TestUnknownAPIAlwaysJSON(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	c := e.login()
	for _, path := range []string{"/api", "/api/nope", APIBase + "/setup/preflight", "/api/other/v1/nope"} {
		for _, method := range []string{"GET", "POST", "DELETE"} {
			for _, cookie := range []*http.Cookie{nil, c} {
				r := httptest.NewRequest(method, path, nil)
				if cookie != nil {
					r.AddCookie(cookie)
				}
				rec := e.do(r)
				var body apiError
				if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/json" || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Error == "" {
					t.Errorf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
				}
			}
		}
	}
}
