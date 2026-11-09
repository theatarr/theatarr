package arrview

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

func TestImportListCRUDRoundTrip(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	base := "/radarr/api/v3"
	var got map[string]any
	e.call("POST", base+"/importlist", map[string]any{
		"name": "TMDb favourites", "listType": "TMDbList", "enabled": true, "enableAuto": true,
		"shouldMonitor": "movieOnly", "searchOnAdd": true, "fields": []any{map[string]any{"name": "listId", "value": "42"}},
	}, 201, &got)
	if got["name"] != "TMDb favourites" || got["id"].(float64) == 0 {
		t.Fatalf("POST import list: %v", got)
	}
	id := int(got["id"].(float64))
	var lists []map[string]any
	e.call("GET", base+"/importlist", nil, 200, &lists)
	if len(lists) != 1 || lists[0]["fields"].([]any)[0].(map[string]any)["value"] != "42" {
		t.Fatalf("GET import list: %v", lists)
	}
	e.call("PUT", base+"/importlist/"+itoa(int64(id)), map[string]any{
		"id": id, "name": "Updated", "enabled": false, "enableAuto": true,
		"shouldMonitor": "movieAndCollection", "fields": []any{map[string]any{"name": "listId", "value": "42"}},
	}, 202, &got)
	if got["name"] != "Updated" || got["enabled"] != false {
		t.Fatalf("PUT import list: %v", got)
	}
	e.call("DELETE", base+"/importlist/"+itoa(int64(id)), nil, 200, nil)
	e.call("GET", base+"/importlist", nil, 200, &lists)
	if len(lists) != 0 {
		t.Fatalf("DELETE import list: %v", lists)
	}
}

func TestCollectionCRUDRoundTrip(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// A monitored collection needs an active target (TestCollectionPutTargets): the /radarr movie versions are.
	for _, k := range []string{"movie-remux", "movie-web", "anime-movie"} {
		e.pol.Versions[k].Mode = policy.ModeActive
	}
	e.start()
	base := "/radarr/api/v3"
	var got map[string]any
	e.call("PUT", base+"/collection", map[string]any{"tmdbId": 77, "name": "A Collection", "monitored": true,
		"qualityProfileId": 1, "rootFolderPath": "/media/movies", "searchOnAdd": true}, 202, &got)
	if got["tmdbId"] != float64(77) || got["monitored"] != true {
		t.Fatalf("PUT collection: %v", got)
	}
	var all []map[string]any
	e.call("GET", base+"/collection", nil, 200, &all)
	if len(all) != 1 || all[0]["name"] != "A Collection" {
		t.Fatalf("GET collection: %v", all)
	}
}

func TestIntakeSchemaValidationAndExclusions(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	base := "/radarr/api/v3"
	var schemas []map[string]any
	e.call("GET", base+"/importlist/schema", nil, 200, &schemas)
	if len(schemas) != 1 || schemas[0]["implementation"] != "TmdbList" || schemas[0]["configContract"] != "TmdbListSettings" {
		t.Fatalf("schema=%v", schemas)
	}
	body := map[string]any{"name": "List", "implementation": "TmdbList", "fields": []any{map[string]any{"name": "listId", "value": 42}}, "monitor": "none", "minimumAvailability": "inCinemas"}
	var got map[string]any
	e.call("POST", base+"/importlist", body, 201, &got)
	id := int64(got["id"].(float64))
	e.call("GET", base+"/importlist/"+itoa(id), nil, 200, &got)
	if got["monitor"] != "none" || got["shouldMonitor"] != "none" || got["minimumAvailability"] != "inCinemas" {
		t.Fatalf("roundtrip=%v", got)
	}
	e.call("POST", base+"/importlist", body, 400, nil)
	body["id"] = id
	body["monitor"] = "movieOnly"
	e.call("PUT", base+"/importlist", body, 202, &got)
	body["implementation"] = "OtherList"
	e.call("POST", base+"/importlist", body, 400, nil)
	body["implementation"] = "TmdbList"
	body["monitor"] = "unexpected"
	e.call("POST", base+"/importlist", body, 400, nil)
	body["monitor"] = "none"
	body["minimumAvailability"] = "unexpected"
	e.call("POST", base+"/importlist", body, 400, nil)
	body["minimumAvailability"] = "released"
	e.call("PUT", base+"/importlist/99999", body, 404, nil)
	e.call("DELETE", base+"/importlist/99999", nil, 404, nil)
	e.call("GET", base+"/importlist/99999", nil, 404, nil)
	e.call("POST", base+"/exclusions", map[string]any{"tmdbId": 201}, 201, &got)
	var exclusions []map[string]any
	e.call("GET", base+"/exclusions", nil, 200, &exclusions)
	if len(exclusions) != 1 || exclusions[0]["tmdbId"] != float64(201) {
		t.Fatalf("exclusions=%v", exclusions)
	}
	e.call("DELETE", base+"/exclusions/201", nil, 200, nil)
	e.call("GET", base+"/exclusions", nil, 200, &exclusions)
	if len(exclusions) != 0 {
		t.Fatalf("deleted exclusion=%v", exclusions)
	}
	e.call("POST", base+"/exclusions", map[string]any{"tmdbId": 0}, 400, nil)
}

func TestIntakeMapsProfilesAndRootsLikeMovies(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	base := "/radarr/api/v3"
	for _, tc := range []struct {
		name    string
		profile int64
		root    string
	}{
		{"profile", e.profileID("radarr", "Rules default"), ""},
		{"root", 0, "/media/movies/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var movie, list map[string]any
			e.call("POST", base+"/movie", map[string]any{"title": "Movie", "tmdbId": 90001, "qualityProfileId": tc.profile, "rootFolderPath": tc.root, "monitored": true}, 201, &movie)
			e.call("POST", base+"/importlist", map[string]any{"name": tc.name, "qualityProfileId": tc.profile, "rootFolderPath": tc.root, "fields": []any{map[string]any{"name": "listId", "value": "91"}}}, 201, &list)
			var versions string
			if err := e.st.Reader().QueryRow(`SELECT versions FROM movie_import_list WHERE id=?`, int64(list["id"].(float64))).Scan(&versions); err != nil {
				t.Fatal(err)
			}
			var keys []string
			if err := json.Unmarshal([]byte(versions), &keys); err != nil {
				t.Fatal(err)
			}
			on := e.versionsOf(int64(movie["id"].(float64)))
			for key := range on {
				if !slices.Contains(keys, key) {
					t.Fatalf("list versions=%v movie versions=%v", keys, on)
				}
			}
			e.call("DELETE", base+"/importlist/"+itoa(int64(list["id"].(float64))), nil, 200, nil)
		})
	}
}

func TestCollectionDiscoveryAndMissingPartsInView(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// Gamma's anime-movie is preselected by discovery; monitoring needs it active (TestCollectionPutTargets).
	e.pol.Versions["anime-movie"].Mode = policy.ModeActive
	e.start()
	e.exec(`INSERT INTO title_external_id(title_id,source,external_id) VALUES(3,'tmdb_collection','77')`)
	var collections []map[string]any
	e.call("GET", "/radarr/api/v3/collection", nil, 200, &collections)
	if len(collections) != 1 || collections[0]["monitored"] != false {
		t.Fatalf("discovered=%v", collections)
	}
	e.exec(`UPDATE movie_collection SET name='Collection',movies='[{"id":603,"title":"Existing"},{"id":99999,"title":"Future","release_date":"2027-01-01"}]' WHERE tmdb_id=77`)
	var c map[string]any
	e.call("GET", "/radarr/api/v3/collection/77", nil, 200, &c)
	if c["title"] != "Collection" {
		t.Fatalf("collection=%v", c)
	}
	movies := c["movies"].([]any)
	if len(movies) < 2 {
		t.Fatalf("missing collection parts: %v", movies)
	}
	found := false
	for _, m := range movies {
		if m.(map[string]any)["tmdbId"] == float64(99999) {
			found = true
			if m.(map[string]any)["movieId"] != float64(0) {
				t.Fatal("missing part has catalog id")
			}
		}
	}
	if !found {
		t.Fatalf("missing part omitted: %v", movies)
	}
	e.call("PUT", "/radarr/api/v3/collection", map[string]any{"collectionIds": []int{77}, "monitored": true}, 200, &collections)
	if len(collections) != 1 || collections[0]["monitored"] != true {
		t.Fatalf("bulk update=%v", collections)
	}
}

func TestIntakeTargetsPerVersionView(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	base := "/v/movie-web/radarr/api/v3"
	var list map[string]any
	e.call("POST", base+"/importlist", map[string]any{"name": "List", "qualityProfileId": e.profileID("v/movie-web", "movie-web"), "fields": []any{map[string]any{"name": "listId", "value": "9"}}}, 201, &list)
	id := itoa(int64(list["id"].(float64)))
	e.call("GET", base+"/importlist/"+id, nil, 200, &list)
	if list["qualityProfileId"] != float64(e.profileID("v/movie-web", "movie-web")) {
		t.Fatalf("profile not mapped to view: %v", list)
	}
	// Even the aggregate view cannot read a per-version view's import list.
	e.call("GET", "/radarr/api/v3/importlist/"+id, nil, 404, nil)
}

// Import lists belong to the view that created them, including update-by-body-id.
func TestImportListsAreViewScoped(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"GET", "PUT", "PUT body", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			e := newEnv(t)
			e.exec(`INSERT INTO movie_import_list(id,name,tmdb_id,versions,view,created_at,updated_at) VALUES(901,'Other view',42,'["movie-web"]','other',0,0)`)
			base := "/radarr/api/v3/importlist"
			var lists []map[string]any
			e.call("GET", base, nil, 200, &lists)
			if method == "GET" && len(lists) != 0 {
				t.Fatalf("another view's lists exposed: %v", lists)
			}
			path := base + "/901"
			body := map[string]any{"id": 901, "name": "Taken over", "fields": []any{map[string]any{"name": "listId", "value": "42"}}}
			verb := method
			if method == "PUT body" {
				verb, path = "PUT", base
			}
			e.call(verb, path, body, 404, nil)
			var name, view string
			if err := e.st.Reader().QueryRow(`SELECT name,view FROM movie_import_list WHERE id=901`).Scan(&name, &view); err != nil {
				t.Fatal(err)
			}
			if name != "Other view" || view != "other" {
				t.Fatalf("list changed: %s %s", name, view)
			}
		})
	}
}

// The Radarr view's PUT collection follows the native rule: a collection it leaves monitored targets only collection
// targets (active movie versions nothing follows), stored explicitly; a per-version view of one falls back to its own
// version; otherwise 400.
func TestCollectionPutTargets(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.pol.Versions["movie-web"].Mode = policy.ModeActive
	e.start()
	e.exec(`INSERT INTO movie_collection (tmdb_id, name, versions, updated_at) VALUES (80, 'Empty', '[]', 0),
		(81, 'Shadow', '["movie-remux"]', 0), (82, 'Mixed', '["movie-remux","movie-web"]', 0)`)
	row := func(id int) (monitored bool, versions, view string) {
		t.Helper()
		if err := e.st.Reader().QueryRow(`SELECT monitored, versions, view FROM movie_collection WHERE tmdb_id = ?`, id).
			Scan(&monitored, &versions, &view); err != nil {
			t.Fatal(err)
		}
		return
	}
	e.call("PUT", "/radarr/api/v3/collection", map[string]any{"tmdbId": 80, "monitored": true}, 400, nil)
	e.call("PUT", "/v/movie-remux/radarr/api/v3/collection", map[string]any{"tmdbId": 81, "monitored": true}, 400, nil)
	e.call("PUT", "/radarr/api/v3/collection", map[string]any{"collectionIds": []int{82, 81}, "monitored": true}, 400, nil)
	if on, _, _ := row(82); on {
		t.Error("a refused bulk PUT monitored 82")
	}
	var got map[string]any
	e.call("PUT", "/v/movie-web/radarr/api/v3/collection", map[string]any{"tmdbId": 80, "monitored": true}, 202, &got)
	if on, versions, view := row(80); !on || versions != `["movie-web"]` || view != "v/movie-web" || got["monitored"] != true {
		t.Errorf("per-version view: %v %s %s %v", on, versions, view, got)
	}
	e.call("PUT", "/radarr/api/v3/collection", map[string]any{"tmdbId": 82, "monitored": true}, 202, nil)
	if on, versions, _ := row(82); !on || versions != `["movie-web"]` {
		t.Errorf("mixed: %v %s", on, versions)
	}
	e.call("PUT", "/radarr/api/v3/collection", map[string]any{"tmdbId": 81, "monitored": true,
		"qualityProfileId": e.profileID("radarr", "WEB only")}, 202, nil)
	if on, versions, _ := row(81); !on || versions != `["movie-web"]` {
		t.Errorf("profile: %v %s", on, versions)
	}
	// Unmonitoring needs no target.
	e.call("PUT", "/radarr/api/v3/collection", map[string]any{"tmdbId": 80, "monitored": false}, 202, nil)
}
