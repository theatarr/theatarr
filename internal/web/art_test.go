package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/metadata/art"
	"github.com/theatarr/theatarr/internal/store"
)

func TestTitleArtAPI(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	now := store.Millis(testNow)
	long := strings.Repeat("word ", 100)
	e.exec(`INSERT INTO title_meta (title_id, tmdb_id, overview, tagline, poster_path, backdrop_path, logo_path, genres,
		runtime_minutes, rating, rating_votes, certification, status, network, original_language, release_date,
		art_hash, refreshed_at, attempted_at, refresh_after) VALUES
		(1, 101, ?, 'Tag.', '/posterAbcdefghij.jpg', '/backdropAbcdefgh.jpg', '/logoAbcdefghijk.png', '["Action","Drama"]',
		 121, 7.5, 900, 'R', 'Released', 'Studio', 'en', '2020-01-01', 'abc1234567', ?, ?, ?)`,
		long, now, now, now+1000)

	var page TitlePage
	if code := e.getKey(APIBase+"/titles", key, &page); code != 200 || len(page.Items) != 3 {
		t.Fatalf("titles: %d %+v", code, page)
	}
	a := page.Items[0]
	if a.Name != "Alpha" || a.Poster != "/img/t/1/poster/w342?v=abc1234567" ||
		a.Backdrop != "/img/t/1/backdrop/w1280?v=abc1234567" || !strings.HasSuffix(a.Overview, "…") ||
		len([]rune(a.Overview)) > overviewShort+1 {
		t.Errorf("alpha: %+v", a.TitleArt)
	}
	if b := page.Items[1]; b.Poster != "/img/t/2/poster/w342" || b.Overview != "" {
		t.Errorf("no meta yet: %+v", b.TitleArt)
	}

	var d TitleDetail
	if code := e.getKey(APIBase+"/titles/1", key, &d); code != 200 || d.Meta == nil {
		t.Fatalf("title: %d %+v", code, d)
	}
	m := d.Meta
	if m.Overview != long || m.Tagline != "Tag." || m.RuntimeMinutes == nil ||
		*m.RuntimeMinutes != 121 || m.Rating == nil || *m.Rating != 7.5 || m.Certification != "R" ||
		strings.Join(m.Genres, ",") != "Action,Drama" || m.Logo != "/img/t/1/logo/w500?v=abc1234567" ||
		m.RefreshedAt == nil || d.Poster != "/img/t/1/poster/w342?v=abc1234567" {
		t.Errorf("meta %+v", m)
	}
	var raw map[string]any
	if code := e.getKey(APIBase+"/titles/2", key, &raw); code != 200 || raw["meta"] != nil || raw["poster"] != "/img/t/2/poster/w342" {
		t.Errorf("title 2: %d meta %v poster %v", code, raw["meta"], raw["poster"])
	}

	// Without an art service: refresh is off and /img answers 404.
	r := httptest.NewRequest("POST", APIBase+"/titles/1/refresh", nil)
	r.Header.Set(auth.APIKeyHeader, key)
	if rec := e.do(r); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("refresh without art: %d", rec.Code)
	}
	r = httptest.NewRequest("GET", "/img/t/1/poster/w342", nil)
	r.Header.Set(auth.APIKeyHeader, key)
	if rec := e.do(r); rec.Code != 404 {
		t.Errorf("img without art: %d", rec.Code)
	}
}

// With an art service (no TMDb key, no image cache): /img serves generated art to signed-in users only, under
// the unchanged CSP, and refresh says the job is off.
func TestImgRoutes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	svc, err := art.New(art.Config{Store: e.st, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	e.web.art = svc
	key := e.apiKey()
	get := func(path string, withKey bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if withKey {
			r.Header.Set(auth.APIKeyHeader, key)
		}
		return e.do(r)
	}
	rec := get("/img/t/1/poster/w342", true)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(rec.Body.String(), "Alpha") ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "img-src 'self' data:") ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("fallback: %d %v", rec.Code, rec.Header())
	}
	if rec := get("/img/t/1/poster/w342", false); rec.Code != http.StatusSeeOther {
		t.Errorf("signed out: %d", rec.Code)
	}
	for _, p := range []string{"/img/t/1/banner/w342", "/img/t/1/poster/w12", "/img/t/99/poster/w342", "/img/t/x/poster/w342",
		"/img/tmdb/w342/notatmdbpath.gif", "/img/tmdb/w342/..%2F..%2Fetc%2Fpasswd", "/img/tmdb/huge/abcdefghij0123.jpg"} {
		if rec := get(p, true); rec.Code != 404 {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	r := httptest.NewRequest("POST", APIBase+"/titles/1/refresh", nil)
	r.Header.Set(auth.APIKeyHeader, key)
	if rec := e.do(r); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("refresh: %d %s", rec.Code, rec.Body.String())
	}
}
