package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/catalog"
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
	if err := e.st.Reader().QueryRow(`SELECT view, quality_profile_id, root_folder_path FROM import_list WHERE id = ?`, l.ID).
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
	if err := e.st.Reader().QueryRow(`SELECT (SELECT count(*) FROM import_list) + 10 * (SELECT count(*) FROM title WHERE tmdb_id = 903)`).Scan(&n); err != nil || n != 10 {
		t.Errorf("after delete: %d %v", n, err)
	}
}

// A list an arr client made is shown and can be changed, but stays that view's: its view, profile and root are never
// written, its libraries stay within the view's, and enable_auto stays the client's.
func TestListsKeepArrView(t *testing.T) {
	t.Parallel()
	e := newEnvWith(t, withIntake(&fakeIntakeTMDb{}))
	key := e.apiKey()
	e.exec(`INSERT INTO import_list (id, name, tmdb_id, enabled, enable_auto, monitor, versions, view, quality_profile_id,
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
	if err := e.st.Reader().QueryRow(`SELECT name, quality_profile_id, root_folder_path, enable_auto FROM import_list
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
	e.exec(`INSERT INTO import_list (id, name, tmdb_id, versions, created_at, updated_at) VALUES (1, 'L', 9, '["movie-web"]', 0, 0)`)
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

// webFakeArr is a Radarr or Sonarr instance for the list routes.
type webFakeArr struct{ app, titles string }

func (f webFakeArr) GetJSON(_ context.Context, endpoint string, _ url.Values, v any) error {
	switch endpoint {
	case "system/status":
		return json.Unmarshal([]byte(`{"appName":"`+f.app+`","version":"5.2.0"}`), v)
	case "movie", "series":
		return json.Unmarshal([]byte(f.titles), v)
	}
	return errors.New("no " + endpoint)
}

// withArrIntake is withIntake with a Radarr instance at 10.0.0.5 and a Sonarr instance at 10.0.0.7 (key "good-key").
func withArrIntake(c *Config) {
	withIntake(&fakeIntakeTMDb{})(c)
	ti := c.Add.Collections.(testIntake)
	ti.MovieIntake.Arr = func(u, key string) (catalog.ArrReader, error) {
		if key != "good-key" {
			return webFakeArr{}, errors.New("arr: unauthorized with key " + key)
		}
		switch u {
		case "http://10.0.0.5:7878":
			return webFakeArr{app: "Radarr", titles: `[{"tmdbId":903,"title":"Listed","year":2020}]`}, nil
		case "http://10.0.0.7:8989":
			return webFakeArr{app: "Sonarr", titles: `[{"tvdbId":601,"title":"Show"}]`}, nil
		}
		return nil, errors.New("no instance at " + u)
	}
}

// Radarr and Sonarr lists: the address is normalised and must be on the LAN, the key is required and never answered,
// a new address needs the key again, a Sonarr list takes series libraries; Check, Preview and Sync now read the
// instance.
func TestArrListsAPI(t *testing.T) {
	t.Parallel()
	e := newEnvWith(t, withArrIntake)
	key := e.apiKey()
	for _, body := range []string{
		`{"list_type":"radarr","name":"R","url":"http://8.8.8.8:7878","api_key":"good-key","versions":["movie-remux"]}`, // public IP
		`{"list_type":"radarr","name":"R","url":"http://10.0.0.5:7878","versions":["movie-remux"]}`,                     // no key
		`{"list_type":"radarr","name":"R","url":"ftp://10.0.0.5","api_key":"good-key","versions":["movie-remux"]}`,      // scheme
		`{"list_type":"radarr","name":"R","url":"http://10.0.0.5:7878","api_key":"good-key","tmdb_id":9,"versions":["movie-remux"]}`,
		`{"list_type":"radarr","name":"R","url":"http://10.0.0.5:7878","api_key":"good-key","monitor":"all","versions":["movie-remux"]}`,
		`{"list_type":"sonarr","name":"S","url":"http://10.0.0.7:8989","api_key":"good-key","versions":["movie-remux"]}`, // movie library
		`{"list_type":"lidarr","name":"L","url":"http://10.0.0.7:8686","api_key":"good-key","versions":["movie-remux"]}`,
	} {
		if rec := e.sendCollection("POST", "/lists", body, key, ""); rec.Code != 400 {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	rec := e.sendCollection("POST", "/lists", `{"list_type":"radarr","name":"Radarr","url":" HTTP://10.0.0.5:7878/ ","api_key":"good-key","versions":["movie-remux"]}`, key, "")
	var l MovieList
	if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &l) != nil || strings.Contains(rec.Body.String(), "good-key") {
		t.Fatalf("POST %d %s", rec.Code, rec.Body)
	}
	if l.ListType != "radarr" || l.Kind != "movie" || l.URL != "http://10.0.0.5:7878" || !l.APIKeySet || l.TmdbID != 0 ||
		l.Monitor != "movieOnly" {
		t.Errorf("radarr list %+v", l)
	}
	radarr := l.ID
	if rec := e.sendCollection("POST", "/lists", `{"list_type":"radarr","name":"Again","url":"http://10.0.0.5:7878","api_key":"good-key","versions":["movie-remux"]}`, key, ""); rec.Code != 409 {
		t.Errorf("same instance twice %d %s", rec.Code, rec.Body)
	}
	rec = e.sendCollection("POST", "/lists", `{"list_type":"sonarr","name":"Sonarr","url":"http://10.0.0.7:8989","api_key":"good-key","versions":["tv-2160p"]}`, key, "")
	if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &l) != nil || l.Kind != "series" || l.Monitor != "all" ||
		!slices.Contains(l.Libraries, "tv-2160p") || slices.Contains(l.Libraries, "movie-remux") {
		t.Fatalf("sonarr POST %d %s", rec.Code, rec.Body)
	}
	var page ListsPage
	if code := e.getKey(APIBase+"/lists", key, &page); code != 200 || len(page.Lists) != 2 || page.SeriesLibraries["tv-2160p"] == "" {
		t.Errorf("GET %d %+v", code, page)
	}
	rec = e.sendCollection("GET", "/lists", ``, key, "")
	if strings.Contains(rec.Body.String(), "good-key") || strings.Contains(rec.Body.String(), "api_key\"") {
		t.Errorf("the key is answered: %s", rec.Body)
	}

	// A new address needs the key again; the same address spelled differently is no change.
	if rec := e.sendCollection("PUT", "/lists/"+strconv.FormatInt(radarr, 10), `{"url":"http://10.0.0.9:7878"}`, key, ""); rec.Code != 400 ||
		!strings.Contains(rec.Body.String(), "API key again") {
		t.Errorf("new address without key %d %s", rec.Code, rec.Body)
	}
	if rec := e.sendCollection("PUT", "/lists/"+strconv.FormatInt(radarr, 10), `{"url":"http://10.0.0.5:7878/"}`, key, ""); rec.Code != 200 {
		t.Errorf("same address %d %s", rec.Code, rec.Body)
	}
	if rec := e.sendCollection("PUT", "/lists/"+strconv.FormatInt(radarr, 10), `{"url":"http://10.0.0.7:8989","api_key":"other"}`, key, ""); rec.Code != 200 {
		// A Radarr list may point at the address a Sonarr list uses: the uniqueness is per type.
		t.Errorf("PUT another address %d %s", rec.Code, rec.Body)
	}
	if rec := e.sendCollection("PUT", "/lists/"+strconv.FormatInt(radarr, 10), `{"url":"http://10.0.0.5:7878","api_key":"good-key"}`, key, ""); rec.Code != 200 {
		t.Errorf("PUT back %d %s", rec.Code, rec.Body)
	}

	// Check reads the instance: by address and key, or by a stored list; a refusal never carries the key.
	var check ListCheckResult
	rec = e.sendCollection("POST", "/lists/check", `{"list_type":"radarr","url":"http://10.0.0.5:7878/","api_key":"good-key"}`, key, "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &check) != nil || check.App != "Radarr" || check.Titles != 1 {
		t.Errorf("check %d %s", rec.Code, rec.Body)
	}
	rec = e.sendCollection("POST", "/lists/check", `{"id":`+strconv.FormatInt(l.ID, 10)+`}`, key, "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &check) != nil || check.App != "Sonarr" || check.Titles != 1 {
		t.Errorf("check by id %d %s", rec.Code, rec.Body)
	}
	rec = e.sendCollection("POST", "/lists/check", `{"list_type":"sonarr","url":"http://10.0.0.5:7878","api_key":"good-key"}`, key, "")
	if rec.Code != 502 {
		t.Errorf("radarr checked as sonarr %d %s", rec.Code, rec.Body)
	}
	rec = e.sendCollection("POST", "/lists/check", `{"list_type":"radarr","url":"http://10.0.0.5:7878","api_key":"wrong-key-123"}`, key, "")
	if rec.Code != 502 || strings.Contains(rec.Body.String(), "wrong-key-123") {
		t.Errorf("wrong key %d %s", rec.Code, rec.Body)
	}

	// Preview, then Sync now.
	var p ListPreview
	rec = e.sendCollection("POST", "/lists/"+strconv.FormatInt(radarr, 10)+"/preview", ``, key, "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.AddTitle != 1 || len(p.Titles) != 1 || p.Titles[0].TmdbID != 903 {
		t.Errorf("preview %d %s", rec.Code, rec.Body)
	}
	var synced ListSynced
	rec = e.sendCollection("POST", "/lists/"+strconv.FormatInt(radarr, 10)+"/sync", ``, key, "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &synced) != nil || synced.Added != 1 || synced.SyncError != "" {
		t.Errorf("sync %d %s", rec.Code, rec.Body)
	}
	if rec := e.sendCollection("POST", "/lists/99/preview", ``, key, ""); rec.Code != 404 {
		t.Errorf("preview of no list %d", rec.Code)
	}
}
