package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
)

func (e *env) putCollection(path, body, key, role string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.sendCollection("PUT", path, body, key, role)
}

func (e *env) sendCollection(method, path, body, key, role string) *httptest.ResponseRecorder {
	e.t.Helper()
	r := httptest.NewRequest(method, APIBase+path, strings.NewReader(body))
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

// fakeIntakeTMDb is TMDb for the movie intake behind the Collections and Lists pages: collection 7 with parts 101
// (in the seeded library) and 901, 902 (new), list 9 with movie 903 and a series, and nothing else.
type fakeIntakeTMDb struct{ fail bool }

func (f *fakeIntakeTMDb) SearchCollection(context.Context, string) ([]tmdb.CollectionResult, error) {
	return []tmdb.CollectionResult{{ID: 7, Name: "Alpha Collection", PosterPath: "/posterOne123.jpg", Overview: "All of Alpha."},
		{ID: 70, Name: "Alpha Returns Collection"}}, nil
}

func (f *fakeIntakeTMDb) Collection(_ context.Context, id int) (*tmdb.Collection, error) {
	if id != 7 {
		return nil, fmt.Errorf("%w: collection %d", tmdb.ErrNotFound, id)
	}
	return &tmdb.Collection{ID: 7, Name: "Alpha Collection", Parts: []tmdb.MovieSummary{
		{ID: 101, Title: "Alpha", ReleaseDate: "2020-01-01", PosterPath: "/posterOne123.jpg"},
		{ID: 901, Title: "Alpha 2", ReleaseDate: "2021-01-01"}, {ID: 902, Title: "Alpha 3", ReleaseDate: "2022-01-01"}}}, nil
}

func (f *fakeIntakeTMDb) List(_ context.Context, id int) (*tmdb.List, error) {
	if id != 9 {
		return nil, fmt.Errorf("%w: list %d", tmdb.ErrNotFound, id)
	}
	return &tmdb.List{ID: 9, Name: "Favourites", Description: "The good ones", Items: []tmdb.MovieSummary{
		{ID: 903, Title: "Listed", ReleaseDate: "2020-01-01", MediaType: "movie"}, {ID: 950, Title: "A show", MediaType: "tv"}}}, nil
}

func (f *fakeIntakeTMDb) MovieDetails(_ context.Context, id int) (*tmdb.Movie, error) {
	if f.fail {
		return nil, fmt.Errorf("tmdb: movie %d: unavailable", id)
	}
	return &tmdb.Movie{ID: id, Title: "Movie " + strconv.Itoa(id), ReleaseDate: "2020-01-01"}, nil
}

// testIntake is the web layer's movie intake as serve wires it: the real intake over a fake TMDb.
type testIntake struct {
	*catalog.MovieIntake
	*fakeIntakeTMDb
}

// withIntake makes movie-remux an active library and gives the web layer a movie intake over tm.
func withIntake(tm *fakeIntakeTMDb) func(*Config) {
	return func(c *Config) {
		c.Policy.Versions["movie-remux"].Mode = policy.ModeActive
		c.Add.Collections = testIntake{&catalog.MovieIntake{Store: c.Store, Policy: c.Policy, TMDb: tm, Now: c.Now}, tm}
	}
}

// Adding a collection from the page: search TMDb, POST it with its libraries, and its missing movies are added at
// once; "Add missing movies now" is the same refresh for a known collection.
func TestAddCollection(t *testing.T) {
	t.Parallel()
	tm := &fakeIntakeTMDb{}
	e := newEnvWith(t, withIntake(tm))
	key := e.apiKey()
	var page CollectionsPage
	if code := e.getKey(APIBase+"/collections", key, &page); code != 200 || !page.CanAdd {
		t.Fatalf("GET %d can_add=%v", code, page.CanAdd)
	}
	e.exec(`INSERT INTO movie_collection (tmdb_id, name, versions, view, quality_profile_id, root_folder_path, updated_at)
		VALUES (70, 'Alpha Returns Collection', '[]', 'radarr', 4, '/movies', 0)`)
	var found CollectionSearch
	if code := e.getKey(APIBase+"/collections/search?q=alpha", key, &found); code != 200 || len(found.Results) != 2 {
		t.Fatalf("search %d %+v", code, found)
	}
	if h := found.Results[0]; h.TmdbID != 7 || h.Name != "Alpha Collection" || h.Added || h.Overview == "" ||
		h.Poster != "/img/tmdb/"+tmdbPosterSize+"/posterOne123.jpg" {
		t.Errorf("hit %+v", h)
	}
	if h := found.Results[1]; h.TmdbID != 70 || !h.Added {
		t.Errorf("known hit %+v", h)
	}
	if code := e.getKey(APIBase+"/collections/search?q=", key, nil); code != 400 {
		t.Errorf("empty search %d", code)
	}

	for _, body := range []string{
		`{"tmdb_id":7}`,                          // monitored by default: needs a library
		`{"tmdb_id":7,"versions":["movie-web"]}`, // shadow
		`{"tmdb_id":7,"versions":["tv-2160p"]}`,  // series
		`{"versions":["movie-remux"]}`,           // no id
		`{"tmdb_id":7,"versions":["movie-remux"],"minimum_availability":"soon"}`,
	} {
		if rec := e.sendCollection("POST", "/collections", body, key, ""); rec.Code != 400 {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := e.sendCollection("POST", "/collections", `{"tmdb_id":8,"versions":["movie-remux"]}`, key, ""); rec.Code != 404 {
		t.Errorf("collection TMDb lacks: %d %s", rec.Code, rec.Body)
	}
	var n int
	if err := e.st.Reader().QueryRow(`SELECT count(*) FROM movie_collection WHERE tmdb_id IN (7, 8)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("refused adds left %d rows: %v", n, err)
	}

	// Monitored (the default): stored with name and parts, and the two movies the library lacks are added now.
	rec := e.sendCollection("POST", "/collections", `{"tmdb_id":7,"versions":["movie-remux"],"search_on_add":true}`, "", auth.RoleManager)
	var out CollectionRefreshed
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("POST %d %s", rec.Code, rec.Body)
	}
	if c := out.Collection; c.TmdbID != 7 || c.Name != "Alpha Collection" || !c.Monitored || strings.Join(c.Versions, ",") != "movie-remux" ||
		!c.SearchOnAdd || c.MinimumAvailability != "released" || c.Total != 3 || c.Held != 3 || out.Added != 2 || out.RefreshError != "" {
		t.Errorf("added %+v", out)
	}
	if err := e.st.Reader().QueryRow(`SELECT count(*) FROM title t JOIN version v ON v.title_id = t.id
		WHERE t.tmdb_id IN (901, 902) AND v.policy_key = 'movie-remux' AND v.add_method = 'collection'`).Scan(&n); err != nil || n != 2 {
		t.Errorf("collection movies in the library: %d %v", n, err)
	}
	// Again: nothing new; the refresh endpoint is the same pass.
	rec = e.sendCollection("POST", "/collections/7/refresh", ``, key, "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Added != 0 || out.Skipped != 3 || out.Collection.Held != 3 {
		t.Errorf("refresh %d %+v", rec.Code, out)
	}
	if rec := e.sendCollection("POST", "/collections/99/refresh", ``, key, ""); rec.Code != 404 {
		t.Errorf("refresh of an unknown collection %d", rec.Code)
	}

	// A known collection is updated like PUT and keeps what an arr client stored on it.
	e.exec(`UPDATE movie_collection SET view = 'radarr', quality_profile_id = 4, root_folder_path = '/movies' WHERE tmdb_id = 7`)
	rec = e.sendCollection("POST", "/collections", `{"tmdb_id":7,"versions":[],"monitored":false,"minimum_availability":"announced"}`, key, "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Collection.Monitored || out.Collection.SearchOnAdd ||
		out.Collection.MinimumAvailability != "announced" || out.Added != 0 {
		t.Errorf("update by POST %d %+v", rec.Code, out)
	}
	var view, root string
	var profile int
	if err := e.st.Reader().QueryRow(`SELECT view, quality_profile_id, root_folder_path FROM movie_collection WHERE tmdb_id = 7`).
		Scan(&view, &profile, &root); err != nil || view != "radarr" || profile != 4 || root != "/movies" {
		t.Errorf("arr mapping after POST: %q %d %q %v", view, profile, root, err)
	}

	// A movie TMDb will not describe: the collection stays stored, and the answer says the refresh failed.
	e.exec(`DELETE FROM movie_collection WHERE tmdb_id = 7`)
	e.exec(`INSERT INTO movie_exclusion (tmdb_id, created_at) VALUES (901, 0)`)
	e.exec(`DELETE FROM title WHERE tmdb_id = 902`)
	tm.fail = true
	rec = e.sendCollection("POST", "/collections", `{"tmdb_id":7,"versions":["movie-remux"]}`, key, "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.RefreshError == "" || out.Collection.Name != "Alpha Collection" ||
		strings.Contains(out.RefreshError, "unavailable") {
		t.Errorf("failed refresh %d %+v", rec.Code, out)
	}

	if rec := e.sendCollection("POST", "/collections", `{"tmdb_id":7,"versions":["movie-remux"]}`, "", auth.RoleUser); rec.Code != 403 {
		t.Errorf("user POST %d", rec.Code)
	}
}

// Without TMDb the page lists and edits collections but cannot search, add or refresh.
func TestAddCollectionNeedsTMDb(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	var page CollectionsPage
	if code := e.getKey(APIBase+"/collections", key, &page); code != 200 || page.CanAdd {
		t.Fatalf("GET %d can_add=%v", code, page.CanAdd)
	}
	if code := e.getKey(APIBase+"/collections/search?q=alpha", key, nil); code != 503 {
		t.Errorf("search %d", code)
	}
	for _, path := range []string{"/collections", "/collections/7/refresh"} {
		if rec := e.sendCollection("POST", path, `{"tmdb_id":7}`, key, ""); rec.Code != 503 || !strings.Contains(rec.Body.String(), "TMDb is not set up") {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}
