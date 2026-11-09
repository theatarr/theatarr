package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
)

func (e *env) putCollection(path, body, key, role string) *httptest.ResponseRecorder {
	e.t.Helper()
	r := httptest.NewRequest("PUT", APIBase+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if key == "" {
		uid := e.account(role, role, auth.StatusActive, true)
		c, csrf := e.session(uid)
		r.AddCookie(c)
		r.Header.Set(auth.CSRFHeader, csrf)
	} else {
		r.Header.Set(auth.APIKeyHeader, key)
	}
	return e.do(r)
}

// Collections target only collection targets: here movie-remux, made active; movie-web stays shadow. Held counts
// the catalog's visible movies among the TMDb parts, membership rows or not.
func TestCollectionsAPI(t *testing.T) {
	t.Parallel()
	e := newEnvWith(t, func(c *Config) { c.Policy.Versions["movie-remux"].Mode = policy.ModeActive })
	// Alpha (TMDb 101) belongs to collection 7, which intake refreshed: its parts are 101, 103 and an announced 104.
	// Alpha 2 (103) has no membership row but is held; 104 is not in the catalog.
	e.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at) VALUES (4, 'movie', 'Alpha 2', 2024, 103, 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (4, 4, 'movie-remux', 'wanted', 0, 0)`)
	e.exec(`INSERT INTO title_external_id (title_id, source, external_id) VALUES (1, 'tmdb_collection', '7'),
		(2, 'tmdb_collection', '8')`)
	e.exec(`INSERT INTO movie_collection (tmdb_id, name, monitored, versions, search_on_add, minimum_availability, movies,
		updated_at) VALUES (7, 'Alpha Collection', 1, '["movie-remux","movie-web"]', 1, 'announced', ?, 0)`,
		`[{"id":104,"title":"Alpha 3","release_date":""},{"id":103,"title":"Alpha 2","release_date":"2024-01-01","poster_path":"/posterTwo123.jpg"},`+
			`{"id":101,"title":"Alpha","release_date":"2020-01-01","poster_path":"/posterOne123.jpg"}]`)
	// Beta's collection as discovery left it (Beta's shadow version preselected), with its one part.
	e.exec(`INSERT INTO movie_collection (tmdb_id, name, versions, movies, updated_at) VALUES (8, 'Beta Collection', '["movie-web"]', '[{"id":102}]', 0)`)
	key := e.apiKey()
	var page CollectionsPage
	if code := e.getKey(APIBase+"/collections", key, &page); code != 200 {
		t.Fatalf("GET %d", code)
	}
	if len(page.Collections) != 2 {
		t.Fatalf("collections %+v", page.Collections)
	}
	want := Collection{TmdbID: 7, Name: "Alpha Collection", Monitored: true, Versions: []string{"movie-remux"}, SearchOnAdd: true,
		MinimumAvailability: "announced", Poster: "/img/tmdb/" + tmdbPosterSize + "/posterOne123.jpg", Held: 2, Total: 3}
	if got := page.Collections[0]; got.TmdbID != want.TmdbID || got.Name != want.Name || !got.Monitored ||
		strings.Join(got.Versions, ",") != "movie-remux" || !got.SearchOnAdd || got.MinimumAvailability != want.MinimumAvailability ||
		got.Poster != want.Poster || got.Held != 2 || got.Total != 3 {
		t.Errorf("collection %+v, want %+v", got, want)
	}
	if got := page.Collections[1]; got.TmdbID != 8 || got.Monitored || got.Held != 1 || got.Total != 1 ||
		got.Versions == nil || len(got.Versions) != 0 {
		t.Errorf("discovered %+v", got)
	}
	if len(page.Libraries) != 1 || page.Libraries["movie-remux"] == "" {
		t.Errorf("libraries %v, want movie-remux only", page.Libraries)
	}

	// Monitoring needs an eligible library: the stored shadow one does not count, and an empty list is refused.
	for _, body := range []string{`{"monitored":true}`, `{"monitored":true,"versions":[]}`} {
		if rec := e.putCollection("/collections/8", body, key, ""); rec.Code != 400 {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	rec := e.putCollection("/collections/8", `{"monitored":true,"versions":["movie-remux","movie-remux"],"search_on_add":true,"minimum_availability":"inCinemas"}`, key, "")
	var c Collection
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &c) != nil {
		t.Fatalf("PUT %d %s", rec.Code, rec.Body)
	}
	if !c.Monitored || strings.Join(c.Versions, ",") != "movie-remux" || !c.SearchOnAdd || c.MinimumAvailability != "inCinemas" || c.Held != 1 {
		t.Errorf("updated %+v", c)
	}
	if rec := e.putCollection("/collections/8", `{"versions":[]}`, key, ""); rec.Code != 400 {
		t.Errorf("monitored with no library %d", rec.Code)
	}
	// Turned off, a collection may have none; absent fields keep their value.
	rec = e.putCollection("/collections/8", `{"monitored":false,"versions":[]}`, key, "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &c) != nil || c.Monitored || c.Versions == nil || len(c.Versions) != 0 || !c.SearchOnAdd {
		t.Errorf("off, no libraries %d %+v", rec.Code, c)
	}
	var versions string
	if err := e.st.Reader().QueryRow(`SELECT versions FROM movie_collection WHERE tmdb_id = 8`).Scan(&versions); err != nil || versions != "[]" {
		t.Errorf("stored versions %q %v", versions, err)
	}

	for _, tc := range []struct{ path, body string }{
		{"/collections/8", `{"versions":["movie-web"]}`}, // shadow
		{"/collections/8", `{"versions":["tv-2160p"]}`},
		{"/collections/8", `{"versions":["nope"]}`},
		{"/collections/8", `{"minimum_availability":"soon"}`},
		{"/collections/8", `{"name":"x"}`},
	} {
		if rec := e.putCollection(tc.path, tc.body, key, ""); rec.Code != 400 {
			t.Errorf("%s: %d", tc.body, rec.Code)
		}
	}
	if rec := e.putCollection("/collections/99", `{"monitored":true}`, key, ""); rec.Code != 404 {
		t.Errorf("unknown collection %d", rec.Code)
	}

	// A movie removed from every view is no longer held.
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (3, 'radarr', 0)`)
	rec = e.putCollection("/collections/8", `{"search_on_add":false}`, key, "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &c) != nil || c.Held != 0 || c.Total != 1 {
		t.Errorf("removed movie held: %d %+v", rec.Code, c)
	}

	// Managers may change collections; users may not see or change them.
	if rec := e.putCollection("/collections/7", `{"monitored":false}`, "", auth.RoleManager); rec.Code != 200 {
		t.Errorf("manager PUT %d %s", rec.Code, rec.Body)
	}
	if rec := e.putCollection("/collections/7", `{"monitored":true}`, "", auth.RoleUser); rec.Code != 403 {
		t.Errorf("user PUT %d", rec.Code)
	}
	var monitored bool
	if err := e.st.Reader().QueryRow(`SELECT monitored FROM movie_collection WHERE tmdb_id = 7`).Scan(&monitored); err != nil || monitored {
		t.Errorf("monitored %v %v", monitored, err)
	}
}
