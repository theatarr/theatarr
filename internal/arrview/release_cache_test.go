package arrview

import (
	"strconv"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// A second view's search of the same title must not take over the first view's cached release: a grab through the
// first view routes that view's own decisions.
func TestReleaseCacheIsPerView(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	next, err := e.pol.Clone()
	if err != nil {
		t.Fatal(err)
	}
	next.Views["remuxonly"] = &policy.View{Flavour: "radarr", Path: "/remuxonly", Versions: []string{"movie-remux"},
		Profiles: map[string][]string{"Remux": {"movie-remux"}}, APIKeyEnv: "THEATARR_VIEW_RADARR_KEY"}
	// /radarr serves movie-web only here, so any movie-remux row after its grab came from the other view.
	next.Views["radarr"].Versions = []string{"movie-web"}
	next.Views["radarr"].Profiles = map[string][]string{"WEB only": {"movie-web"}}
	e.pol = next
	e.startDev(e.svc.decider)
	var rs []map[string]any
	e.call("GET", "/radarr/api/v3/release?movieId="+strconv.Itoa(IDBase+1), nil, 200, &rs)
	if len(rs) == 0 || rs[0]["approved"] != true {
		t.Fatalf("radarr releases %v", rs)
	}
	good := rs[0]
	var other []map[string]any
	e.call("GET", "/remuxonly/api/v3/release?movieId="+strconv.Itoa(IDBase+1), nil, 200, &other)
	if len(other) == 0 || other[0]["guid"] != good["guid"] {
		t.Fatalf("remuxonly releases %v, want the same guid %v", other, good["guid"])
	}
	remuxRows := func() int64 {
		return e.one(`SELECT COUNT(*) FROM router_plan WHERE version_key = 'movie-remux'`) +
			e.one(`SELECT COUNT(*) FROM download WHERE version_key = 'movie-remux'`)
	}
	e.drain()
	before := remuxRows()
	e.call("POST", "/radarr/api/v3/release", map[string]any{"guid": good["guid"], "indexerId": good["indexerId"]}, 200, nil)
	e.drain()
	adds := e.adds()
	web := e.one(`SELECT COUNT(*) FROM download WHERE version_key = 'movie-web'`)
	remux := remuxRows() - before
	if len(adds) != 1 || adds[0] != strings.Repeat("9", 40) || web != 1 || remux != 0 {
		t.Errorf("grab through /radarr after a /remuxonly search: adds %v, movie-web rows %d, movie-remux rows %d",
			adds, web, remux)
	}
}

// Two titles of one view can return the same release (one info hash, so one guid). A grab that names its movie goes
// to that movie; one that names none goes to the title searched last, as the arrs' cache does.
func TestReleaseCacheSameHashTwoTitles(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	var a, b []map[string]any
	e.call("GET", "/radarr/api/v3/release?movieId="+strconv.Itoa(IDBase+1), nil, 200, &a)
	e.call("GET", "/radarr/api/v3/release?movieId="+strconv.Itoa(IDBase+2), nil, 200, &b)
	if len(a) == 0 || len(b) == 0 || a[0]["guid"] != b[0]["guid"] {
		t.Fatalf("want one guid for both titles: %v / %v", a, b)
	}
	e.call("POST", "/radarr/api/v3/release", map[string]any{"guid": a[0]["guid"], "indexerId": a[0]["indexerId"],
		"movieId": IDBase + 1}, 200, nil)
	e.drain()
	if n := e.one(`SELECT COUNT(*) FROM download WHERE title_id = 1`); n != 1 {
		t.Errorf("grab naming movie 1: %d download rows for title 1, want 1", n)
	}
	if n := e.one(`SELECT COUNT(*) FROM download WHERE title_id = 2`); n != 0 {
		t.Errorf("grab naming movie 1: %d download rows for title 2, want 0", n)
	}
	// An echoed resource carries mappedMovieId; a movie that was not searched is not in the cache.
	e.call("POST", "/radarr/api/v3/release", map[string]any{"guid": a[0]["guid"], "indexerId": a[0]["indexerId"],
		"mappedMovieId": IDBase + 3}, 404, nil)
}
