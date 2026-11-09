package arrview

import (
	"context"
	"fmt"
	"testing"

	"github.com/theatarr/theatarr/internal/catalog"
)

// The anime version a recovered TMDb check turns on takes the original request's seasons.
func TestAnimeRecoveryKeepsRequestedSeasons(t *testing.T) {
	e := newEnv(t)
	f := &fakeTMDb{fail: true, tvByTVDB: map[int]int{301: 901}, tv: map[int]string{901: `{"id":901,"name":"Anime Show",
		"original_language":"ja","genres":[{"id":16,"name":"Animation"}]}`}}
	e.withTMDb(f)
	body := map[string]any{"title": "Anime Show", "tvdbId": 301, "qualityProfileId": e.profileID("sonarr", "Rules default"),
		"rootFolderPath": "/media/tv-uhd", "monitored": true, "seasons": []any{
			map[string]any{"seasonNumber": 1, "monitored": true}, map[string]any{"seasonNumber": 2, "monitored": false}}}
	var got map[string]any
	e.call("POST", "/sonarr/api/v3/series", body, 201, &got)
	tid := int64(got["id"].(float64)) - IDBase
	if src := e.str(`SELECT anime_source FROM arrview_title WHERE title_id = ?`, tid); src != "unknown" {
		t.Fatal("fixture", src)
	}
	f.mu.Lock()
	f.fail = false
	f.mu.Unlock()
	e.call("POST", "/sonarr/api/v3/series", body, 201, &got)
	vid := e.one(`SELECT id FROM version WHERE title_id = ? AND policy_key = 'anime-tv'`, tid)
	m, err := catalog.LoadMonitor(context.Background(), e.st.Reader(), vid)
	if err != nil {
		t.Fatal(err)
	}
	if mon, ok := m.Seasons[2]; !ok || mon {
		t.Fatalf("anime version lost season 2 off: %+v", m)
	}
	if !m.Seasons[1] {
		t.Fatalf("anime version lost season 1 on: %+v", m)
	}
}

// Picking the profile of a copy that is on but has the requested season off monitors that
// season on the copy, though another copy already reports it monitored.
func TestSeasonRequestForMonitoredCopyWithSeasonOff(t *testing.T) {
	e := newEnv(t)
	cp := *e.pol.Versions["tv-2160p"]
	e.pol.Versions["tv-copy"] = &cp
	v := e.pol.Views["sonarr"]
	v.Versions = []string{"tv-2160p", "tv-copy"}
	v.Profiles = map[string][]string{"Original": {"tv-2160p"}, "Other copy": {"tv-copy"}}
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at)
		VALUES (20, 10, 'tv-copy', 1, 'wanted', 0, 0)`)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at)
		SELECT 20, id, 0, 'wanted', 0 FROM episode WHERE title_id = 10`)
	e.start()
	path := fmt.Sprintf("/sonarr/api/v3/series/%d", IDBase+10)
	var s map[string]any
	e.call("GET", path, nil, 200, &s)
	if !seasonsOf(t, s)[1] {
		t.Fatal("fixture: season 1 not reported monitored")
	}
	s["qualityProfileId"] = e.profileID("sonarr", "Other copy")
	e.call("PUT", path, s, 202, nil)
	if got := e.one(`SELECT monitored FROM episode_version_state WHERE version_id = 20 AND episode_id = 102`); got != 1 {
		t.Fatalf("re-requested season 1 left S01E02 of the copy unmonitored: %d", got)
	}
}
