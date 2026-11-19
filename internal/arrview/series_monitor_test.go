package arrview

import (
	"strconv"
	"testing"
)

// seasonsOf maps the series' seasons to their monitored flag as the view reports them.
func seasonsOf(t *testing.T, s map[string]any) map[int]bool {
	t.Helper()
	out := map[int]bool{}
	for _, x := range s["seasons"].([]any) {
		se := x.(map[string]any)
		out[int(se["seasonNumber"].(float64))] = se["monitored"] == true
	}
	return out
}

// Seerr's re-request (sonarr.ts addSeries, existing series): the lookup object comes back whole with the requested
// season set monitored. Like Sonarr's UpdateSeries, only a season whose flag changed touches its episodes, so a
// partly monitored season stays partly monitored.
func TestPutSeriesAppliesSeasonDelta(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	now := ms(testNow)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, monitored, created_at, updated_at) VALUES
		(201, 10, 2, 1, 'Two', 0, ?, ?), (202, 10, 2, 2, 'Two b', 0, ?, ?), (301, 10, 3, 1, 'Three', 0, ?, ?)`,
		now, now, now, now, now, now)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at) VALUES
		(10, 201, 1, 'wanted', ?), (10, 202, 0, 'wanted', ?), (10, 301, 0, 'wanted', ?)`, now, now, now)
	// S01E02 unmonitored by hand: season 1 is partly monitored, and reported monitored.
	e.call("PUT", "/sonarr/api/v3/episode/monitor", map[string]any{"episodeIds": []int{102}, "monitored": false},
		202, nil)
	var lk []map[string]any
	e.call("GET", "/sonarr/api/v3/series/lookup?term=tvdb:201", nil, 200, &lk)
	series := lk[0]
	if got := seasonsOf(t, series); !got[1] || !got[2] || got[3] {
		t.Fatalf("seasons before %v", got)
	}
	for _, x := range series["seasons"].([]any) {
		if se := x.(map[string]any); se["seasonNumber"].(float64) == 3 {
			se["monitored"] = true
		}
	}
	e.call("PUT", "/sonarr/api/v3/series", series, 202, nil)
	mon := func(ep int) int64 {
		return e.one(`SELECT monitored FROM episode_version_state WHERE version_id = 10 AND episode_id = ?`, ep)
	}
	if mon(102) != 0 || mon(202) != 0 {
		t.Errorf("echoed seasons re-monitored: S01E02 %d, S02E02 %d", mon(102), mon(202))
	}
	if mon(101) != 1 || mon(103) != 1 || mon(201) != 1 || mon(301) != 1 {
		t.Error("monitored episodes changed, or the requested season was not monitored")
	}
	// A season whose flag does change is applied whole, and kept as the version's intent.
	var s map[string]any
	e.call("GET", "/sonarr/api/v3/series/"+strconv.Itoa(IDBase+10), nil, 200, &s)
	for _, x := range s["seasons"].([]any) {
		if se := x.(map[string]any); se["seasonNumber"].(float64) == 2 {
			se["monitored"] = false
		}
	}
	s["seasons"] = append(s["seasons"].([]any), map[string]any{"seasonNumber": 4, "monitored": false})
	e.call("PUT", "/sonarr/api/v3/series", s, 202, &s)
	if mon(201) != 0 || mon(202) != 0 || mon(101) != 1 || mon(102) != 0 {
		t.Error("season 2 not unmonitored, or another season changed")
	}
	if got := seasonsOf(t, s); got[2] || !got[1] || !got[3] {
		t.Errorf("seasons after %v", got)
	}
	var seasons string
	if err := e.st.Reader().QueryRow(`SELECT seasons FROM series_monitor WHERE version_id = 10`).Scan(&seasons); err != nil {
		t.Fatal(err)
	}
	if seasons != `{"2":false,"3":true,"4":false}` {
		t.Errorf("intent %s", seasons)
	}
}

// Episodes the metadata source dropped (removed_at, kept for their files and history) are gone from the arr API,
// as Sonarr deletes them on refresh.
func TestRemovedEpisodesHidden(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.exec(`UPDATE episode SET removed_at = ? WHERE id = 102`, ms(testNow))
	var eps []map[string]any
	e.call("GET", "/sonarr/api/v3/episode?seriesId="+strconv.Itoa(IDBase+10), nil, 200, &eps)
	for _, x := range eps {
		if x["id"].(float64) == 102 {
			t.Errorf("removed episode listed: %v", x)
		}
	}
	if len(eps) != 3 {
		t.Errorf("%d episodes", len(eps))
	}
	e.call("GET", "/sonarr/api/v3/episode/102", nil, 404, nil)
	var s map[string]any
	e.call("GET", "/sonarr/api/v3/series/"+strconv.Itoa(IDBase+10), nil, 200, &s)
	for _, x := range s["seasons"].([]any) {
		se := x.(map[string]any)
		if st := se["statistics"].(map[string]any); se["seasonNumber"].(float64) == 1 && st["totalEpisodeCount"].(float64) != 2 {
			t.Errorf("season 1 statistics %v", st)
		}
	}
	// S01E02 was the one aired monitored episode without a file.
	var page map[string]any
	e.call("GET", "/sonarr/api/v3/wanted/missing", nil, 200, &page)
	if page["totalRecords"].(float64) != 0 {
		t.Errorf("wanted/missing %v", page["records"])
	}
	var cal []map[string]any
	e.call("GET", "/sonarr/api/v3/calendar?start=2019-01-01&end=2019-02-01", nil, 200, &cal)
	if len(cal) != 1 || cal[0]["id"].(float64) != 101 {
		t.Errorf("calendar %v", cal)
	}
}

// monitorNewItems from PUT series/editor reaches the versions' intent (series_monitor), which the metadata refresh
// follows, and the resource reports that intent.
func TestEditorMonitorNewItems(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var series []map[string]any
	e.call("PUT", "/sonarr/api/v3/series/editor", map[string]any{"seriesIds": []int{IDBase + 10},
		"monitorNewItems": "none"}, 202, &series)
	if len(series) != 1 || series[0]["monitorNewItems"] != "none" {
		t.Errorf("editor %v", series)
	}
	var mn string
	if err := e.st.Reader().QueryRow(`SELECT monitor_new FROM series_monitor WHERE version_id = 10`).Scan(&mn); err != nil ||
		mn != "none" {
		t.Fatalf("intent %q %v", mn, err)
	}
	// The intent is what the resource reports, even without the view's record (an imported series).
	e.exec(`DELETE FROM arrview_title`)
	var s map[string]any
	e.call("GET", "/sonarr/api/v3/series/"+strconv.Itoa(IDBase+10), nil, 200, &s)
	if s["monitorNewItems"] != "none" {
		t.Errorf("monitorNewItems %v", s["monitorNewItems"])
	}
}

// Ruddarr and LunaSea add with Sonarr's addOptions.monitor. Before the metadata arrives it becomes the seasons'
// pending mode; on a series whose episodes are known it sets them as Sonarr's EpisodeMonitoredService does.
func TestAddSeriesMonitorOption(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	seasons := []map[string]any{{"seasonNumber": 0, "monitored": false}, {"seasonNumber": 1, "monitored": true},
		{"seasonNumber": 2, "monitored": true}, {"seasonNumber": 3, "monitored": true}}
	var s map[string]any
	e.call("POST", "/sonarr/api/v3/series", map[string]any{"tvdbId": 302, "title": "Fresh", "seasons": seasons,
		"monitored": true, "status": "continuing", "qualityProfileId": e.profileID("sonarr", "Rules default"),
		"addOptions": map[string]any{"monitor": "firstSeason", "searchForMissingEpisodes": false}}, 201, &s)
	tid := int64(s["id"].(float64)) - IDBase
	var intent string
	if err := e.st.Reader().QueryRow(`SELECT m.seasons FROM series_monitor m JOIN version v ON v.id = m.version_id
		WHERE v.title_id = ?`, tid).Scan(&intent); err != nil {
		t.Fatal(err)
	}
	if intent != `{"0":false,"1":true,"2":true,"3":true}` {
		t.Errorf("firstSeason intent %s", intent)
	}
	if n := e.one(`SELECT COUNT(*) FROM series_monitor m JOIN version v ON v.id=m.version_id WHERE v.title_id=? AND pending_monitor='firstSeason'`, tid); n != 1 {
		t.Error("firstSeason mode not persisted")
	}
	// An integer is MonitorTypes' index: 2 is future. The Show is known with episodes; through the per-version view
	// that already has it, future monitors only S01E03 (airs 2027).
	e.call("POST", "/v/tv-2160p/sonarr/api/v3/series", map[string]any{"tvdbId": 201, "title": "The Show",
		"monitored": true, "status": "continuing", "addOptions": map[string]any{"monitor": 2}}, 201, nil)
	for ep, want := range map[int]int64{100: 0, 101: 0, 102: 0, 103: 1} {
		if got := e.one(`SELECT monitored FROM episode_version_state WHERE version_id = 10 AND episode_id = ?`, ep); got != want {
			t.Errorf("episode %d monitored %d, want %d", ep, got, want)
		}
	}
}

// A profile edit that names no seasons (the mass editor, a PUT series without seasons) restores a removed copy and
// gives its library in sync a copy: the partner takes the restored copy's season and episode choices, as the native
// routes do (catalog.PartnerSeasonIntent).
func TestProfileEditPartnerSeasons(t *testing.T) {
	t.Parallel()
	for _, route := range []string{"editor", "put"} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			cp := *e.pol.Versions["tv-2160p"]
			cp.Library = "/media/tv-copy"
			e.pol.Versions["tv-copy"] = &cp
			hd := cp
			hd.Library = "/media/tv-1080p"
			e.pol.Versions["tv-1080p"] = &hd
			e.pol.Library.SyncGroups = [][]string{{"tv-copy", "tv-1080p"}}
			v := e.pol.Views["sonarr"]
			v.Versions = []string{"tv-2160p", "tv-copy"}
			v.Profiles = map[string][]string{"Original": {"tv-2160p"}, "Other copy": {"tv-copy"}}
			// tv-copy was removed holding the pilot only, nothing new.
			e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at)
				VALUES (20, 10, 'tv-copy', 0, 'wanted', 0, 0)`)
			e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at)
				SELECT 20, id, number = 1 AND season = 1, 'wanted', 0 FROM episode WHERE title_id = 10`)
			e.exec(`INSERT INTO series_monitor (version_id, monitor_new, updated_at) VALUES (20, 'none', 0)`)
			e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (20, 'sonarr', 0)`)
			e.start()
			pid := e.profileID("sonarr", "Other copy")
			path := "/sonarr/api/v3/series/" + strconv.Itoa(IDBase+10)
			if route == "editor" {
				e.call("PUT", "/sonarr/api/v3/series/editor", map[string]any{"seriesIds": []int{IDBase + 10},
					"qualityProfileId": pid}, 202, nil)
			} else {
				var s map[string]any
				e.call("GET", path, nil, 200, &s)
				delete(s, "seasons")
				s["qualityProfileId"] = pid
				e.call("PUT", path, s, 202, nil)
			}
			if n := e.one(`SELECT COUNT(*) FROM arrview_removed_version WHERE version_id = 20`); n != 0 {
				t.Fatal("fixture: the edit did not restore tv-copy")
			}
			if n := e.one(`SELECT COUNT(*) FROM version WHERE title_id = 10 AND policy_key = 'tv-1080p'`); n != 1 {
				t.Fatalf("the edit gave the library in sync %d copies, want 1", n)
			}
			if got := e.str(`SELECT COALESCE(group_concat(e.number || ':' || s.monitored, ','), '') FROM (SELECT * FROM
				episode_version_state ORDER BY episode_id) s JOIN version v ON v.id = s.version_id JOIN episode e
				ON e.id = s.episode_id WHERE v.policy_key = 'tv-1080p' AND e.season = 1`); got != "1:1,2:0,3:0" {
				t.Errorf("partner's season 1 %q, want the restored copy's 1:1,2:0,3:0", got)
			}
			// The PUT's echoed monitorNewItems is a choice for the view's copies and the partners alike.
			want := map[string]string{"editor": "none", "put": "all"}[route]
			if got := e.str(`SELECT group_concat(v.policy_key || ':' || COALESCE(m.monitor_new, ''), ',') FROM version v
				LEFT JOIN series_monitor m ON m.version_id = v.id WHERE v.policy_key IN ('tv-copy', 'tv-1080p')
				ORDER BY v.policy_key`); got != "tv-1080p:"+want+",tv-copy:"+want {
				t.Errorf("monitor new %q, want %s on both", got, want)
			}
		})
	}
}

// An unmonitored series with two libraries of the view: tv-2160p holds season 1 but S01E02, tv-copy nothing. The
// view reports its seasons off (effective), so clients send back what they read: turning the series on changes no
// episode choice, a season request reaches both libraries and no other season, and a season sent off while turning
// the series on stays off.
func TestSeasonRequestReenablingSeriesReachesEveryLibrary(t *testing.T) {
	t.Parallel()
	setup := func() *env {
		e := newEnv(t)
		cp := *e.pol.Versions["tv-2160p"]
		cp.Library = "/media/tv-copy"
		e.pol.Versions["tv-copy"] = &cp
		e.pol.Views["sonarr"].Versions = []string{"tv-2160p", "tv-copy"}
		now := ms(testNow)
		e.exec(`INSERT INTO episode (id, title_id, season, number, name, monitored, created_at, updated_at) VALUES
			(201, 10, 2, 1, 'Two', 0, ?, ?), (202, 10, 2, 2, 'Two b', 0, ?, ?)`, now, now, now, now)
		e.exec(`UPDATE version SET monitored = 0 WHERE id = 10`)
		e.exec(`UPDATE episode_version_state SET monitored = 0 WHERE version_id = 10 AND episode_id = 102`)
		e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at)
			SELECT 10, id, 0, 'wanted', 0 FROM episode WHERE id IN (201, 202)`)
		e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at)
			VALUES (20, 10, 'tv-copy', 0, 'wanted', 0, 0)`)
		e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at)
			SELECT 20, id, 0, 'wanted', 0 FROM episode WHERE title_id = 10`)
		e.start()
		return e
	}
	path := "/sonarr/api/v3/series/" + strconv.Itoa(IDBase+10)
	states := func(e *env, version int) string {
		return e.str(`SELECT group_concat(episode_id || ':' || monitored, ',') FROM (SELECT episode_id, monitored
			FROM episode_version_state WHERE version_id = ? AND episode_id IN (101, 102, 103, 201, 202)
			ORDER BY episode_id)`, version)
	}
	const was10, was20 = "101:1,102:0,103:1,201:0,202:0", "101:0,102:0,103:0,201:0,202:0"
	put := func(e *env, edit func(s map[string]any)) {
		var s map[string]any
		e.call("GET", path, nil, 200, &s)
		if s["monitored"] != false || seasonsOf(t, s)[1] {
			t.Fatalf("series before %v %v", s["monitored"], seasonsOf(t, s))
		}
		s["monitored"] = true
		edit(s)
		e.call("PUT", path, s, 202, nil)
	}
	season := func(s map[string]any, n int, on bool) {
		for _, x := range s["seasons"].([]any) {
			if se := x.(map[string]any); se["seasonNumber"].(float64) == float64(n) {
				se["monitored"] = on
			}
		}
	}

	// A series toggle (Ruddarr): no episode choice changes.
	e := setup()
	put(e, func(map[string]any) {})
	if got10, got20 := states(e, 10), states(e, 20); got10 != was10 || got20 != was20 {
		t.Errorf("toggle changed episodes: tv-2160p %s, tv-copy %s", got10, got20)
	}
	// Seerr requests season 2: both libraries take it, season 1 stays as it was.
	e = setup()
	put(e, func(s map[string]any) { season(s, 2, true) })
	if got10, got20 := states(e, 10), states(e, 20); got10 != "101:1,102:0,103:1,201:1,202:1" ||
		got20 != "101:0,102:0,103:0,201:1,202:1" {
		t.Errorf("season 2 request: tv-2160p %s, tv-copy %s", got10, got20)
	}
	// Season 1 sent off while turning the series on: tv-copy's season 1 stays off.
	e = setup()
	put(e, func(s map[string]any) { season(s, 1, false) })
	if got20 := states(e, 20); got20 != was20 {
		t.Errorf("season 1 off: tv-copy %s", got20)
	}
}
