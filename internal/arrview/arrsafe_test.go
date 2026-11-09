package arrview

import (
	"strconv"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
)

// Write paths never widen an empty or unresolved selection to the whole view.

// DELETE moviefile|episodefile/{id} with an id that does not parse or names no file is a 404 and unlinks nothing
// (MovieFileController.DeleteMovieFile: "Movie file not found"); before, a nil id list meant every file.
func TestDeleteFileBadIDUnlinksNothing(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	e.seedFiles()
	e.exec(`INSERT INTO link (id, path, target, title_id, state, created_at, updated_at)
		VALUES (8, '/media/movies-web/Alpha (2020)/Alpha (2020) other.mkv',
		'/mnt/debrid/__all__/Alpha.WEB/other.mkv', 1, 'active', 0, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (8, 2)`)
	e.pol.Library = &policy.Library{Enabled: true, Versions: map[string]*policy.LibraryVersion{
		"movie-web": {Roots: []string{"/media/movies-web"}}, "tv-2160p": {Roots: []string{t.TempDir()}}}}
	e.lib.gate = library.NewGate(e.pol)
	active := `SELECT COUNT(*) FROM link WHERE state = 'active'`
	before, files := e.one(active), e.one(`SELECT COUNT(*) FROM version_file`)
	for _, p := range []string{
		"/v/movie-web/radarr/api/v3/moviefile/0", "/v/movie-web/radarr/api/v3/moviefile/-1",
		"/v/movie-web/radarr/api/v3/moviefile/undefined", "/radarr/api/v3/moviefile/null",
		"/v/movie-web/radarr/api/v3/moviefile/" + strconv.Itoa(LinkFileBase+999),
		"/sonarr/api/v3/episodefile/0", "/v/tv-2160p/sonarr/api/v3/episodefile/undefined",
		"/v/tv-2160p/sonarr/api/v3/episodefile/-5", "/v/tv-2160p/sonarr/api/v3/episodefile/9999",
	} {
		if rec := e.do("DELETE", p, e.key, nil); rec.Code != 404 {
			t.Errorf("DELETE %s: %d %s", p, rec.Code, rec.Body.String())
		}
	}
	// Bulk: an empty list is a 400; ids that name no file (0, -1, unknown) unlink nothing.
	for _, ids := range [][]int{{}, {0}, {-1}, {0, -1, LinkFileBase + 999}} {
		rec := e.do("DELETE", "/v/movie-web/radarr/api/v3/moviefile/bulk", e.key, map[string]any{"movieFileIds": ids})
		if rec.Code != 400 && rec.Code != 404 {
			t.Errorf("bulk movie %v: %d", ids, rec.Code)
		}
		rec = e.do("DELETE", "/v/tv-2160p/sonarr/api/v3/episodefile/bulk", e.key, map[string]any{"episodeFileIds": ids})
		if rec.Code != 400 && rec.Code != 404 {
			t.Errorf("bulk episode %v: %d", ids, rec.Code)
		}
	}
	rec := e.do("DELETE", "/v/movie-web/radarr/api/v3/moviefile/bulk", e.key, map[string]any{})
	if rec.Code != 400 {
		t.Errorf("bulk without ids: %d", rec.Code)
	}
	if len(e.lib.unlinks) != 0 || e.one(active) != before || e.one(`SELECT COUNT(*) FROM version_file`) != files {
		t.Fatalf("bad ids unlinked %v", e.lib.unlinks)
	}
	if len(e.events.events) != 0 {
		t.Errorf("events %+v", e.events.events)
	}
	// A good id still unlinks that file only.
	e.call("DELETE", "/v/movie-web/radarr/api/v3/moviefile/"+strconv.Itoa(LinkFileBase+7), nil, 200, nil)
	if len(e.lib.unlinks) != 1 || !strings.HasSuffix(e.lib.unlinks[0], "Alpha (2020) WEBDL-2160p.mkv") ||
		e.one(active) != before-1 {
		t.Errorf("good id: unlinks %v", e.lib.unlinks)
	}
}

// PUT movie|series/editor with ids that resolve to no title of the view ([], 0, stale arr ids) changes nothing and
// answers [] (SaveAll over GetMovies/GetSeries of unknown ids); before, a nil list meant every title.
func TestEditorUnresolvedIDsEditNothing(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	state := func() string {
		return e.str(`SELECT group_concat(id || ':' || policy_key || ':' || monitored, ',') FROM
			(SELECT id, policy_key, monitored FROM version ORDER BY id)`) + " " +
			e.str(`SELECT COALESCE(group_concat(title_id || ':' || tag_id, ','), '') FROM
			(SELECT title_id, tag_id FROM arrview_title_tag ORDER BY title_id, tag_id)`) + " " +
			e.str(`SELECT COALESCE(group_concat(version_id || ':' || monitor_new, ','), '') FROM
			(SELECT version_id, monitor_new FROM series_monitor ORDER BY version_id)`)
	}
	var tag map[string]any
	e.call("POST", "/radarr/api/v3/tag", map[string]any{"label": "x"}, 201, &tag)
	before := state()
	rp, sp := e.profileID("radarr", "Rules default"), e.profileID("sonarr", "Rules default")
	for _, ids := range []any{[]int{}, []int{0}, []int{-1}, []int{987654}, []int{IDBase + 987654}, nil} {
		var out []map[string]any
		e.call("PUT", "/radarr/api/v3/movie/editor", map[string]any{"movieIds": ids, "monitored": false,
			"qualityProfileId": rp, "tags": []any{tag["id"]}, "applyTags": "replace"}, 202, &out)
		if len(out) != 0 {
			t.Errorf("movie editor %v: %d titles", ids, len(out))
		}
		out = nil
		e.call("PUT", "/sonarr/api/v3/series/editor", map[string]any{"seriesIds": ids, "monitored": false,
			"qualityProfileId": sp, "monitorNewItems": "none", "tags": []any{tag["id"]}}, 202, &out)
		if len(out) != 0 {
			t.Errorf("series editor %v: %d titles", ids, len(out))
		}
		if got := state(); got != before {
			t.Fatalf("editor with ids %v changed the library:\n%s\n%s", ids, before, got)
		}
	}
	// An unknown profile is still a 400, whatever the ids.
	e.call("PUT", "/radarr/api/v3/movie/editor", map[string]any{"movieIds": []int{}, "qualityProfileId": 99999}, 400, nil)
	// A resolving id still edits that title only.
	var out []map[string]any
	e.call("PUT", "/radarr/api/v3/movie/editor", map[string]any{"movieIds": []int{IDBase + 1, 987654},
		"monitored": false}, 202, &out)
	if len(out) != 1 || out[0]["id"].(float64) != IDBase+1 || out[0]["monitored"] != false {
		t.Errorf("one title %v", out)
	}
}

// A search command that names ids none of which resolve (0, [], stale) searches nothing, even a Missing or Cutoff
// one, which without ids would search the whole view.
func TestSearchCommandUnresolvedIDsSearchNothing(t *testing.T) {
	t.Parallel()
	e := newSearchEnv(t)
	for _, tc := range []struct {
		path string
		body map[string]any
	}{
		{"/radarr/api/v3/command", map[string]any{"name": "MoviesSearch", "movieIds": []int{}}},
		{"/radarr/api/v3/command", map[string]any{"name": "MoviesSearch", "movieIds": []int{0, -1}}},
		{"/radarr/api/v3/command", map[string]any{"name": "MissingMoviesSearch", "movieIds": []int{0}}},
		{"/radarr/api/v3/command", map[string]any{"name": "MissingMoviesSearch", "movieIds": []int{}}},
		{"/radarr/api/v3/command", map[string]any{"name": "MissingMoviesSearch", "movieIds": []int{987654}}},
		{"/radarr/api/v3/command", map[string]any{"name": "CutOffUnmetMoviesSearch", "movieId": 0}},
		{"/sonarr/api/v3/command", map[string]any{"name": "MissingEpisodeSearch", "seriesId": 0}},
		{"/sonarr/api/v3/command", map[string]any{"name": "MissingEpisodeSearch", "seriesId": 987654}},
		{"/sonarr/api/v3/command", map[string]any{"name": "MissingEpisodeSearch", "episodeIds": []int{}}},
		{"/sonarr/api/v3/command", map[string]any{"name": "EpisodeSearch", "episodeIds": []int{987654}}},
		{"/sonarr/api/v3/command", map[string]any{"name": "SeriesSearch", "seriesId": -1}},
	} {
		var c map[string]any
		e.call("POST", tc.path, tc.body, 201, &c)
		e.drain()
		if got := e.searches(); len(got) != 0 {
			t.Errorf("%v searched %v", tc.body, got)
		}
		if c["status"] != "completed" || !strings.HasPrefix(c["message"].(string), "Nothing to search") {
			t.Errorf("%v: %v %v", tc.body, c["status"], c["message"])
		}
	}
	if n := e.one(`SELECT COUNT(*) FROM download`); n != 0 {
		t.Errorf("%d downloads", n)
	}
	// Without ids a Missing search still covers the view (the control).
	e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "MissingMoviesSearch"}, 201, nil)
	e.drain()
	if got := e.searches(); len(got) == 0 {
		t.Error("MissingMoviesSearch without ids searched nothing")
	}
}

// Maintenance commands (rescan, refresh) that name episode ids resolving to nothing fail rather than run for the
// whole view.
func TestMaintenanceUnresolvedEpisodeIDs(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ids, err := e.svc.maintenanceTitles(t.Context(), e.svc.views[0], map[string]any{"episodeIds": []any{float64(987654)}})
	if err == nil {
		t.Errorf("unresolved episodeIds: titles %v", ids)
	}
	ids, err = e.svc.maintenanceTitles(t.Context(), e.svc.views[0], map[string]any{"movieIds": []any{}})
	if err == nil {
		t.Errorf("empty movieIds: titles %v", ids)
	}
}
