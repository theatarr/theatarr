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
