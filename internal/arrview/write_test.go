package arrview

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

func (e *env) profileID(view, name string) int64 {
	e.t.Helper()
	return e.one(`SELECT id FROM arrview_profile WHERE view = ? AND name = ?`, view, name)
}

func TestRssSyncCommandRunsImmediately(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	calls := 0
	e.svc.rssSync = func(_ context.Context) error { calls++; return nil }
	var cmd map[string]any
	e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "RssSync"}, 201, &cmd)
	if calls != 1 || cmd["status"] != "completed" || cmd["result"] != "successful" {
		t.Fatalf("calls=%d command=%v", calls, cmd)
	}
}

// versionsOf lists the title's versions that are on: present, monitored and not removed.
func (e *env) versionsOf(titleID int64) map[string]bool {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT policy_key FROM version v WHERE title_id = ? AND monitored = 1 AND NOT EXISTS
		(SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)`, titleID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			e.t.Fatal(err)
		}
		out[k] = true
	}
	return out
}

func TestAddMoviePerView(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	web := e.profileID("radarr", "WEB only")
	def := e.profileID("radarr", "Rules default")
	// A new title with "WEB only": the title is created with movie-web on.
	var m map[string]any
	e.call("POST", "/radarr/api/v3/movie", map[string]any{"title": "Epsilon", "year": 2024, "tmdbId": 105,
		"qualityProfileId": web, "rootFolderPath": "/media/movies-web", "minimumAvailability": "released",
		"monitored": true, "tags": []int{}, "addOptions": map[string]any{"searchForMovie": false}}, 201, &m)
	tid := int64(m["id"].(float64)) - IDBase
	if m["title"] != "Epsilon" || m["qualityProfileId"].(float64) != float64(web) || m["monitored"] != true {
		t.Fatalf("added %v", m)
	}
	if got := e.versionsOf(tid); len(got) != 1 || !got["movie-web"] {
		t.Fatalf("versions after WEB only: %v", got)
	}
	// A second add with "Rules default" turns on the extra version instead of failing.
	e.call("POST", "/radarr/api/v3/movie", map[string]any{"title": "Epsilon", "tmdbId": 105, "qualityProfileId": def,
		"rootFolderPath": "/media/movies", "monitored": true}, 201, &m)
	if got := e.versionsOf(tid); len(got) != 2 || !got["movie-remux"] || !got["movie-web"] {
		t.Fatalf("versions after Rules default: %v", got)
	}
	if n := e.one(`SELECT COUNT(*) FROM title WHERE tmdb_id = 105`); n != 1 {
		t.Errorf("%d titles", n)
	}
	// Each per-version view sees it.
	e.call("GET", "/v/movie-remux/radarr/api/v3/movie/"+strconv.FormatInt(IDBase+tid, 10), nil, 200, nil)
	// Anime: Rules default on an anime title (root folder of the anime version) falls back to the profile's
	// versions only if none fits; with the Anime profile it gets anime-movie.
	anime := e.profileID("radarr", "Anime")
	e.call("POST", "/radarr/api/v3/movie", map[string]any{"title": "Zeta", "tmdbId": 106, "qualityProfileId": anime,
		"rootFolderPath": "/media/anime-movies", "monitored": true}, 201, &m)
	zid := int64(m["id"].(float64)) - IDBase
	if got := e.versionsOf(zid); len(got) != 1 || !got["anime-movie"] {
		t.Errorf("anime add: %v", got)
	}
	if e.one(`SELECT anime FROM title WHERE id = ?`, zid) != 1 {
		t.Error("anime hint not stored on the new title")
	}
	// No profile: the root folder picks the version.
	e.call("POST", "/radarr/api/v3/movie", map[string]any{"title": "Eta", "tmdbId": 107,
		"rootFolderPath": "/media/movies"}, 201, &m)
	if got := e.versionsOf(int64(m["id"].(float64)) - IDBase); len(got) != 1 || !got["movie-remux"] {
		t.Errorf("root-folder add: %v", got)
	}
	// No profile, no root: the view's default-enabled versions that fit (non-anime: remux and web).
	e.call("POST", "/radarr/api/v3/movie", map[string]any{"title": "Theta", "tmdbId": 108}, 201, &m)
	if got := e.versionsOf(int64(m["id"].(float64)) - IDBase); len(got) != 2 || got["anime-movie"] {
		t.Errorf("default add: %v", got)
	}
	e.call("POST", "/radarr/api/v3/movie", map[string]any{"title": "No id"}, 400, nil)
	// Through a per-version view, only that version.
	e.call("POST", "/v/anime-movie/radarr/api/v3/movie", map[string]any{"title": "Alpha", "tmdbId": 101}, 201, &m)
	if got := e.versionsOf(1); !got["anime-movie"] || !got["movie-remux"] || !got["movie-web"] {
		t.Errorf("per-version add on Alpha: %v", got)
	}
	// Nothing was searched: the add asked for no search, so no command.
	if n := e.one(`SELECT COUNT(*) FROM arrview_command`); n != 0 {
		t.Errorf("%d commands", n)
	}
}

func TestDeleteUnlinksOnlyTheView(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	alpha := strconv.Itoa(IDBase + 1)
	// movie-web's view unlinks movie-web only; /radarr still has Alpha through movie-remux.
	rec := e.call("DELETE", "/v/movie-web/radarr/api/v3/movie/"+alpha+"?deleteFiles=true&addImportExclusion=false",
		nil, 200, nil)
	if rec.Body.Len() != 0 {
		t.Errorf("DELETE body %q", rec.Body.String())
	}
	e.call("GET", "/v/movie-web/radarr/api/v3/movie/"+alpha, nil, 404, nil)
	var m map[string]any
	e.call("GET", "/radarr/api/v3/movie/"+alpha, nil, 200, &m)
	if m["hasFile"] != true {
		t.Errorf("Alpha in /radarr after the web delete: %v", m["hasFile"])
	}
	if got := e.versionsOf(1); len(got) != 1 || !got["movie-remux"] {
		t.Errorf("versions %v", got)
	}
	// Nothing is deleted: the rows and the file stay.
	if n := e.one(`SELECT COUNT(*) FROM version WHERE title_id = 1`); n != 2 {
		t.Errorf("%d version rows", n)
	}
	// /radarr's DELETE (Seerr's remove, by the alias it stored) unlinks the rest, once radarr_4k_debrid no longer owns
	// movie-remux.
	e.call("DELETE", "/radarr/api/v3/movie/7?deleteFiles=true&addImportExclusion=false", nil, 409, nil)
	e.cutover()
	e.call("DELETE", "/radarr/api/v3/movie/7?deleteFiles=true&addImportExclusion=false", nil, 200, nil)
	e.call("GET", "/radarr/api/v3/movie/7", nil, 404, nil)
	e.call("GET", "/radarr/api/v3/movie/"+alpha, nil, 404, nil)
	var lk []map[string]any
	e.call("GET", "/radarr/api/v3/movie/lookup?term=tmdb:101", nil, 200, &lk)
	if lk[0]["id"] != nil {
		t.Errorf("lookup after delete still has an id: %v", lk[0]["id"])
	}
	if n := e.one(`SELECT COUNT(*) FROM version_file WHERE title_id = 1`); n != 1 {
		t.Errorf("file rows %d", n)
	}
	if e.one(`SELECT monitored FROM title WHERE id = 1`) != 0 {
		t.Error("title still monitored")
	}
	e.call("DELETE", "/radarr/api/v3/movie/"+alpha, nil, 404, nil)
	// A new add restores the removed version, file and all.
	e.call("POST", "/radarr/api/v3/movie", map[string]any{"title": "Alpha", "tmdbId": 101,
		"qualityProfileId": e.profileID("radarr", "Rules default")}, 201, &m)
	if m["hasFile"] != true || m["id"].(float64) != IDBase+1 {
		t.Errorf("re-added %v %v", m["id"], m["hasFile"])
	}
	if n := e.one(`SELECT COUNT(*) FROM arrview_removed_version`); n != 0 {
		t.Errorf("%d removed markers left", n)
	}
}

func TestPutMovie(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// Beta is unmonitored in /radarr: Seerr sends the lookup object back with monitored true (radarr.ts L157-180).
	var lk []map[string]any
	e.call("GET", "/radarr/api/v3/movie/lookup?term=tmdb:102", nil, 200, &lk)
	if lk[0]["monitored"] != false {
		t.Fatalf("Beta monitored %v", lk[0]["monitored"])
	}
	tg := e.call("POST", "/radarr/api/v3/tag", map[string]any{"label": "2-friend"}, 201, nil)
	tagID := int(decode(t, tg.Body.Bytes()).(map[string]any)["id"].(float64))
	obj := lk[0]
	obj["monitored"] = true
	obj["qualityProfileId"] = e.profileID("radarr", "WEB only")
	obj["tags"] = []int{tagID}
	obj["minimumAvailability"] = "inCinemas"
	var m map[string]any
	e.call("PUT", "/radarr/api/v3/movie", obj, 202, &m)
	if m["monitored"] != true || m["minimumAvailability"] != "inCinemas" || len(m["tags"].([]any)) != 1 {
		t.Errorf("after PUT %v %v %v", m["monitored"], m["minimumAvailability"], m["tags"])
	}
	if got := e.versionsOf(2); len(got) != 1 || !got["movie-web"] {
		t.Errorf("Beta versions %v", got)
	}
	// PUT by id path, integer enum, unmonitor; tags replaced (none).
	e.call("PUT", "/radarr/api/v3/movie/"+strconv.Itoa(IDBase+2), map[string]any{"monitored": false,
		"minimumAvailability": 1, "tags": []int{}}, 202, &m)
	if m["monitored"] != false || m["minimumAvailability"] != "announced" || len(m["tags"].([]any)) != 0 {
		t.Errorf("after second PUT %v %v %v", m["monitored"], m["minimumAvailability"], m["tags"])
	}
	e.call("PUT", "/radarr/api/v3/movie", map[string]any{"id": IDBase + 4, "monitored": true}, 404, nil)
}

func TestCommands(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var c map[string]any
	e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "MoviesSearch", "movieIds": []int{IDBase + 1}},
		201, &c)
	// CommandResource; decypharr reads id, name, commandName, status, queued, body (pkg/arr/media.go L18-33).
	wantKeys(t, "command", any(c), fields(`id name commandName message body priority status result queued started
		ended duration trigger stateChangeTime sendUpdatesToClient updateScheduledTask`), "clientUserAgent")
	if c["status"] != "completed" || c["commandName"] != "Movies Search" ||
		c["message"] != "Shadow mode: nothing searched or grabbed (versions movie-remux, movie-web)" {
		t.Errorf("command %v", c)
	}
	id := strconv.Itoa(int(c["id"].(float64)))
	var got map[string]any
	e.call("GET", "/sonarr/api/v3/command/"+id, nil, 200, &got)
	if got["name"] != "MoviesSearch" {
		t.Errorf("command ids are global: %v", got)
	}
	var list []map[string]any
	e.call("GET", "/radarr/api/v3/command", nil, 200, &list)
	if len(list) != 1 {
		t.Errorf("command list %v", list)
	}
	e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "RefreshMonitoredDownloads"}, 201, &c)
	if c["status"] != "failed" || c["message"] != "maintenance job queue is unavailable" {
		t.Errorf("refresh %v", c)
	}
	e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "EpisodeSearch"}, 400, nil)
	e.call("POST", "/sonarr/api/v3/command", map[string]any{"name": "SeasonSearch", "seriesId": 3, "seasonNumber": 1},
		201, &c)
	if c["message"] != "Shadow mode: nothing searched or grabbed (versions tv-2160p)" {
		t.Errorf("season search %v", c["message"])
	}
	e.call("POST", "/sonarr/api/v3/command", map[string]any{"name": "EpisodeSearch", "episodeIds": []int{101}}, 201, &c)
	if c["status"] != "completed" {
		t.Errorf("episode search %v", c)
	}
	e.call("GET", "/radarr/api/v3/command/999", nil, 404, nil)
	// With an add's search option, the add records the search.
	e.call("POST", "/radarr/api/v3/movie", map[string]any{"title": "Iota", "tmdbId": 109,
		"addOptions": map[string]any{"searchForMovie": true}}, 201, nil)
	if n := e.one(`SELECT COUNT(*) FROM arrview_command WHERE name = 'MoviesSearch' AND status = 'completed'`); n != 2 {
		t.Errorf("%d MoviesSearch commands", n)
	}
}

func TestSonarrWrites(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// Anime series via Seerr's anime settings: seriesType anime and the anime profile.
	var s map[string]any
	e.call("POST", "/sonarr/api/v3/series", map[string]any{"tvdbId": 301, "title": "Kaiju", "qualityProfileId": e.profileID("sonarr", "Anime"), "languageProfileId": 1, "seasons": []map[string]any{}, "tags": []int{},
		"seasonFolder": true, "monitored": true, "monitorNewItems": "all", "rootFolderPath": "/media/anime",
		"seriesType": "anime", "addOptions": map[string]any{"ignoreEpisodesWithFiles": true,
			"searchForMissingEpisodes": true}}, 201, &s)
	kid := int64(s["id"].(float64)) - IDBase
	if got := e.versionsOf(kid); len(got) != 1 || !got["anime-tv"] {
		t.Errorf("anime series versions %v", got)
	}
	if s["seriesType"] != "anime" || s["rootFolderPath"] != "/media/anime" {
		t.Errorf("series %v %v", s["seriesType"], s["rootFolderPath"])
	}
	if n := e.one(`SELECT COUNT(*) FROM arrview_command WHERE name = 'MissingEpisodeSearch'`); n != 1 {
		t.Errorf("%d searches recorded", n)
	}
	// Episode monitoring is per version of the view.
	var eps []map[string]any
	e.call("PUT", "/sonarr/api/v3/episode/monitor", map[string]any{"episodeIds": []int{102, 103}, "monitored": false},
		202, &eps)
	if len(eps) != 2 || eps[0]["monitored"] != false {
		t.Errorf("monitor %v", eps)
	}
	if n := e.one(`SELECT COUNT(*) FROM episode_version_state WHERE version_id = 10 AND monitored = 0`); n != 3 {
		t.Errorf("unmonitored states %d", n)
	}
	e.call("PUT", "/sonarr/api/v3/episode/monitor", map[string]any{"episodeIds": []int{}, "monitored": true}, 400, nil)
	// PUT series with seasons: season 0 monitored creates/updates per-version rows.
	var one map[string]any
	e.call("GET", "/sonarr/api/v3/series/3", nil, 200, &one)
	one["seasons"] = []map[string]any{{"seasonNumber": 0, "monitored": true}, {"seasonNumber": 1, "monitored": true}}
	e.call("PUT", "/sonarr/api/v3/series", one, 202, &one)
	for _, se := range one["seasons"].([]any) {
		if se.(map[string]any)["monitored"] != true {
			t.Errorf("season after PUT %v", se)
		}
	}
	// DELETE from /sonarr after cutover, then the per-version view hides it too.
	e.cutover()
	e.call("DELETE", "/sonarr/api/v3/series/3?deleteFiles=true", nil, 200, nil)
	e.call("GET", "/v/tv-2160p/sonarr/api/v3/series/"+strconv.Itoa(IDBase+10), nil, 404, nil)
}

func TestSeriesTypeStoredOnAddAndUpdate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var s map[string]any
	e.call("POST", "/sonarr/api/v3/series", map[string]any{"tvdbId": 303, "title": "Daily Show", "monitored": true, "seriesType": "daily", "qualityProfileId": e.profileID("sonarr", "Rules default")}, 201, &s)
	tid := int64(s["id"].(float64)) - IDBase
	if e.one(`SELECT COUNT(*) FROM title WHERE id=? AND series_type='daily'`, tid) != 1 {
		t.Fatal("daily add not stored")
	}
	s["seriesType"] = "standard"
	e.call("PUT", "/sonarr/api/v3/series/"+strconv.FormatInt(IDBase+tid, 10), s, 202, nil)
	if e.one(`SELECT COUNT(*) FROM title WHERE id=? AND series_type='standard'`, tid) != 1 {
		t.Fatal("series type update not stored")
	}
}

// cutover drops the policy's arr imports, as at cutover: no arr instance owns a version any more.
func (e *env) cutover() {
	e.t.Helper()
	e.pol.Import = nil
	e.start()
}

// An arr view's DELETE ends the removed versions' downloads in hand in our records (they are not imported back) and
// refuses a version an arr instance still owns before anything changes, as the web UI's remove does.
func TestDeleteEndsDownloadsAndRefusesManaged(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	now := ms(testNow)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES
		(71, 1, 2, 'movie-web', 'aaaa', 'Alpha.2020.2160p.WEB-DL-GRP', 'idx', 'ready', ?, ?),
		(72, 1, 2, 'movie-web', 'bbbb', 'Alpha.2020.1080p.WEB-DL-GRP', 'idx', 'imported', ?, ?),
		(73, 1, 1, 'movie-remux', 'cccc', 'Alpha.2020.2160p.REMUX-GRP', 'idx', 'downloading', ?, ?)`,
		now, now, now, now, now, now)
	alpha := strconv.Itoa(IDBase + 1)
	// movie-web has no owning arr: its view's DELETE removes it and ends its download in hand.
	e.call("DELETE", "/v/movie-web/radarr/api/v3/movie/"+alpha, nil, 200, nil)
	var state, reason string
	if err := e.st.Reader().QueryRow(`SELECT state, COALESCE(error, '') FROM download WHERE id = 71`).Scan(&state,
		&reason); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || !strings.Contains(reason, "deleted through v/movie-web") {
		t.Errorf("download 71 %s %q", state, reason)
	}
	if n := e.one(`SELECT COUNT(*) FROM download WHERE id = 72 AND state = 'imported'`); n != 1 {
		t.Error("an imported download changed")
	}
	if n := e.one(`SELECT COUNT(*) FROM download WHERE id = 73 AND state = 'downloading'`); n != 1 {
		t.Error("another version's download ended")
	}
	// movie-remux is still radarr_4k_debrid's: refused, nothing changes.
	rec := e.call("DELETE", "/radarr/api/v3/movie/"+alpha, nil, 409, nil)
	if !strings.Contains(rec.Body.String(), "Managed by radarr_4k_debrid") {
		t.Errorf("refusal %s", rec.Body.String())
	}
	if n := e.one(`SELECT COUNT(*) FROM arrview_removed_version WHERE version_id = 1`); n != 0 {
		t.Error("managed version removed")
	}
	if n := e.one(`SELECT COUNT(*) FROM download WHERE id = 73 AND state = 'downloading'`); n != 1 {
		t.Error("managed version's download ended")
	}
}
