package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/auth"
)

// The Lists tab's API: look a TMDb list up, add it to movie libraries, change it, sync it now and delete it.
func TestListsAPI(t *testing.T) {
	t.Parallel()
	e := newEnvWith(t, withIntake(&fakeIntakeTMDb{}))
	key := e.apiKey()
	var page ListsPage
	if code := e.getKey(APIBase+"/lists", key, &page); code != 200 || len(page.Lists) != 0 || !page.CanSync {
		t.Fatalf("GET %d %+v", code, page)
	}
	// Every movie library, shadow ones too: intake picks the fitting ones per movie.
	if len(page.Libraries) != 3 || page.Libraries["movie-web"] == "" || page.Libraries["tv-2160p"] != "" {
		t.Errorf("libraries %v", page.Libraries)
	}

	var look ListLookup
	if code := e.getKey(APIBase+"/lists/lookup?tmdb_id=9", key, &look); code != 200 || look.Name != "Favourites" ||
		look.Description == "" || look.Movies != 1 || look.Added {
		t.Errorf("lookup %d %+v", code, look)
	}
	if code := e.getKey(APIBase+"/lists/lookup?tmdb_id=10", key, nil); code != 404 {
		t.Errorf("lookup of a list TMDb lacks %d", code)
	}
	if code := e.getKey(APIBase+"/lists/lookup?tmdb_id=x", key, nil); code != 400 {
		t.Errorf("lookup without an id %d", code)
	}

	for _, body := range []string{
		`{"tmdb_id":9,"name":"Favourites"}`,                                              // no library
		`{"tmdb_id":9,"name":"Favourites","versions":["tv-2160p"]}`,                      // series library
		`{"tmdb_id":9,"name":"Favourites","versions":["nope"]}`,                          // unknown
		`{"tmdb_id":9,"versions":["movie-remux"]}`,                                       // no name
		`{"name":"Favourites","versions":["movie-remux"]}`,                               // no id
		`{"tmdb_id":9,"name":"Favourites","versions":["movie-remux"],"monitor":"all"}`,   // monitor
		`{"tmdb_id":9,"name":"Favourites","versions":["movie-remux"],"view":"radarr"}`,   // not a field
		`{"tmdb_id":9,"name":"F","versions":["movie-remux"],"minimum_availability":"x"}`, // availability
	} {
		if rec := e.sendCollection("POST", "/lists", body, key, ""); rec.Code != 400 {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	rec := e.sendCollection("POST", "/lists", `{"tmdb_id":9,"name":" Favourites ","versions":["movie-remux","movie-remux"],"search_on_add":true}`, "", auth.RoleManager)
	var l MovieList
	if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &l) != nil {
		t.Fatalf("POST %d %s", rec.Code, rec.Body)
	}
	if l.ID == 0 || l.Name != "Favourites" || l.ListType != "tmdb" || l.TmdbID != 9 || !l.Enabled || !l.EnableAuto || l.Monitor != "movieOnly" ||
		!l.SearchOnAdd || strings.Join(l.Versions, ",") != "movie-remux" || l.MinimumAvailability != "released" || l.View != "" ||
		l.LastSyncAt != nil || len(l.Libraries) != 3 {
		t.Errorf("added %+v", l)
	}
	if rec := e.sendCollection("POST", "/lists", `{"tmdb_id":9,"name":"Again","versions":["movie-remux"]}`, key, ""); rec.Code != 409 {
		t.Errorf("duplicate %d %s", rec.Code, rec.Body)
	}
	if code := e.getKey(APIBase+"/lists/lookup?tmdb_id=9", key, &look); code != 200 || !look.Added {
		t.Errorf("lookup of an added list %d %+v", code, look)
	}
	// A list made here belongs to no arr view.
	var view, root string
	var profile int
	if err := e.st.Reader().QueryRow(`SELECT view, quality_profile_id, root_folder_path FROM movie_import_list WHERE id = ?`, l.ID).
		Scan(&view, &profile, &root); err != nil || view != "" || profile != 0 || root != "" {
		t.Errorf("native row: %q %d %q %v", view, profile, root, err)
	}

	// Sync now: the list's movie is added to its library; the series on the list is skipped.
	rec = e.sendCollection("POST", "/lists/1/sync", ``, key, "")
	var synced ListSynced
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &synced) != nil || synced.Added != 1 || synced.Skipped != 1 ||
		synced.SyncError != "" || synced.List.LastSyncAt == nil {
		t.Fatalf("sync %d %s", rec.Code, rec.Body)
	}
	var n int
	if err := e.st.Reader().QueryRow(`SELECT count(*) FROM title t JOIN version v ON v.title_id = t.id
		WHERE t.tmdb_id = 903 AND v.policy_key = 'movie-remux' AND v.add_method = 'list'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("list movie in the library: %d %v", n, err)
	}

	// Changes: absent fields keep their value; on a list made here enable_auto follows enabled.
	rec = e.sendCollection("PUT", "/lists/1", `{"name":"Best","enabled":false,"monitor":"none","search_on_add":false,"minimum_availability":"inCinemas","versions":["movie-web","anime-movie"]}`, key, "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &l) != nil || l.Name != "Best" || l.Enabled || l.EnableAuto || l.Monitor != "none" ||
		l.SearchOnAdd || l.MinimumAvailability != "inCinemas" || strings.Join(l.Versions, ",") != "anime-movie,movie-web" {
		t.Errorf("PUT %d %+v", rec.Code, l)
	}
	rec = e.sendCollection("PUT", "/lists/1", `{"enabled":true}`, key, "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &l) != nil || !l.Enabled || !l.EnableAuto || l.Name != "Best" || l.Monitor != "none" {
		t.Errorf("enable %d %+v", rec.Code, l)
	}
	for _, body := range []string{`{"versions":[]}`, `{"versions":["tv-2160p"]}`, `{"name":" "}`, `{"monitor":"x"}`,
		`{"minimum_availability":"x"}`, `{"tmdb_id":10}`, `{"view":"radarr"}`} {
		if rec := e.sendCollection("PUT", "/lists/1", body, key, ""); rec.Code != 400 {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}

	// Managers manage lists (the add above); users do not.
	if rec := e.sendCollection("PUT", "/lists/1", `{"enabled":true}`, "", auth.RoleUser); rec.Code != 403 {
		t.Errorf("user PUT %d", rec.Code)
	}

	for _, tc := range []struct{ method, path string }{{"PUT", "/lists/99"}, {"DELETE", "/lists/99"}, {"POST", "/lists/99/sync"}} {
		if rec := e.sendCollection(tc.method, tc.path, `{}`, key, ""); rec.Code != 404 {
			t.Errorf("%s %s: %d", tc.method, tc.path, rec.Code)
		}
	}
	if rec := e.sendCollection("DELETE", "/lists/1", ``, key, ""); rec.Code != 204 {
		t.Errorf("DELETE %d %s", rec.Code, rec.Body)
	}
	// The movie the list added stays.
	if err := e.st.Reader().QueryRow(`SELECT (SELECT count(*) FROM movie_import_list) + 10 * (SELECT count(*) FROM title WHERE tmdb_id = 903)`).Scan(&n); err != nil || n != 10 {
		t.Errorf("after delete: %d %v", n, err)
	}
}

// A list an arr client made is shown and can be changed, but stays that view's: its view, profile and root are never
// written, its libraries stay within the view's, and enable_auto stays the client's.
func TestListsKeepArrView(t *testing.T) {
	t.Parallel()
	e := newEnvWith(t, withIntake(&fakeIntakeTMDb{}))
	key := e.apiKey()
	e.exec(`INSERT INTO movie_import_list (id, name, tmdb_id, enabled, enable_auto, monitor, versions, view, quality_profile_id,
		root_folder_path, created_at, updated_at) VALUES (5, 'From Radarr', 9, 1, 0, 'movieOnly', '["movie-remux"]', 'radarr', 3, '/movies', 0, 0),
		(6, 'Per version', 11, 1, 1, 'movieOnly', '["movie-web"]', 'v/movie-web', 0, '', 0, 0)`)
	var page ListsPage
	if code := e.getKey(APIBase+"/lists", key, &page); code != 200 || len(page.Lists) != 2 {
		t.Fatalf("GET %d %+v", code, page)
	}
	if l := page.Lists[0]; l.ID != 5 || l.View != "radarr" || l.EnableAuto || strings.Join(l.Libraries, ",") != "anime-movie,movie-remux,movie-web" {
		t.Errorf("arr list %+v", l)
	}
	if l := page.Lists[1]; l.View != "v/movie-web" || strings.Join(l.Libraries, ",") != "movie-web" {
		t.Errorf("per-version list %+v", l)
	}
	rec := e.sendCollection("PUT", "/lists/5", `{"name":"Renamed","enabled":false,"versions":["movie-web"],"search_on_add":true}`, key, "")
	var l MovieList
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &l) != nil || l.Name != "Renamed" || l.Enabled || l.View != "radarr" ||
		strings.Join(l.Versions, ",") != "movie-web" {
		t.Fatalf("PUT %d %s", rec.Code, rec.Body)
	}
	// What the Radarr view reads its lists by (internal/arrview readImportLists: WHERE view = ?) is untouched.
	var name, root string
	var profile int
	var auto bool
	if err := e.st.Reader().QueryRow(`SELECT name, quality_profile_id, root_folder_path, enable_auto FROM movie_import_list
		WHERE view = 'radarr'`).Scan(&name, &profile, &root, &auto); err != nil || name != "Renamed" || profile != 3 || root != "/movies" || auto {
		t.Errorf("the radarr view's list: %q %d %q %v %v", name, profile, root, auto, err)
	}
	// The Radarr view itself still lists it, renamed, under a profile of its own that holds the chosen library.
	av, err := arrview.New(context.Background(), arrview.Config{Store: e.st, Auth: e.auth, Policy: e.web.pol,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return testNow },
		Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	av.Mount(e.mux)
	viewLists := func() []struct {
		ID               int64  `json:"id"`
		Name             string `json:"name"`
		QualityProfileID int64  `json:"qualityProfileId"`
		RootFolderPath   string `json:"rootFolderPath"`
	} {
		t.Helper()
		r := httptest.NewRequest("GET", "/radarr/api/v3/importlist", nil)
		r.Header.Set("X-Api-Key", key)
		rec := e.do(r)
		var out []struct {
			ID               int64  `json:"id"`
			Name             string `json:"name"`
			QualityProfileID int64  `json:"qualityProfileId"`
			RootFolderPath   string `json:"rootFolderPath"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("radarr importlist %d %s", rec.Code, rec.Body)
		}
		return out
	}
	if got := viewLists(); len(got) != 1 || got[0].ID != 5 || got[0].Name != "Renamed" || got[0].QualityProfileID == 0 ||
		got[0].RootFolderPath != "/movies" {
		t.Errorf("the radarr view lists %+v", got)
	}
	// Libraries outside the list's view are refused.
	if rec := e.sendCollection("PUT", "/lists/6", `{"versions":["movie-remux"]}`, key, ""); rec.Code != 400 ||
		!strings.Contains(rec.Body.String(), "v/movie-web") {
		t.Errorf("library outside the view %d %s", rec.Code, rec.Body)
	}
	// The same TMDb list is added once, whichever side made it.
	if rec := e.sendCollection("POST", "/lists", `{"tmdb_id":9,"name":"Mine","versions":["movie-remux"]}`, key, ""); rec.Code != 409 {
		t.Errorf("duplicate of an arr list %d", rec.Code)
	}
}

// Without TMDb lists are still listed and edited; lookup and sync answer 503.
func TestListsNeedTMDbToSync(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	e.exec(`INSERT INTO movie_import_list (id, name, tmdb_id, versions, created_at, updated_at) VALUES (1, 'L', 9, '["movie-web"]', 0, 0)`)
	var page ListsPage
	if code := e.getKey(APIBase+"/lists", key, &page); code != 200 || page.CanSync || len(page.Lists) != 1 {
		t.Fatalf("GET %d %+v", code, page)
	}
	if code := e.getKey(APIBase+"/lists/lookup?tmdb_id=9", key, nil); code != 503 {
		t.Errorf("lookup %d", code)
	}
	if rec := e.sendCollection("POST", "/lists/1/sync", ``, key, ""); rec.Code != 503 {
		t.Errorf("sync %d", rec.Code)
	}
	if rec := e.sendCollection("PUT", "/lists/1", `{"enabled":false}`, key, ""); rec.Code != 200 {
		t.Errorf("PUT %d %s", rec.Code, rec.Body)
	}
}
