package arrview

import (
	"fmt"
	"testing"
)

// A client echoing the composite view's movie back must not turn on, or restore, a copy left off.
func TestPutEchoKeepsOtherCopies(t *testing.T) {
	e := newEnv(t)
	path := fmt.Sprintf("/radarr/api/v3/movie/%d", IDBase+1)
	e.exec(`UPDATE version SET monitored = 0 WHERE id = 2`)
	var movie map[string]any
	e.call("GET", path, nil, 200, &movie)
	if movie["monitored"] != true {
		t.Fatalf("fixture: movie not monitored: %v", movie["monitored"])
	}
	e.call("PUT", path, movie, 202, nil)
	if got := e.one(`SELECT monitored FROM version WHERE id = 2`); got != 0 {
		t.Fatalf("echoed PUT turned on the unmonitored copy: monitored=%d", got)
	}

	e.exec(`UPDATE version SET monitored = 1 WHERE id = 2`)
	e.call("DELETE", fmt.Sprintf("/v/movie-web/radarr/api/v3/movie/%d", IDBase+1), nil, 200, nil)
	if n := e.one(`SELECT count(*) FROM arrview_removed_version WHERE version_id = 2`); n != 1 {
		t.Fatal("fixture: copy not removed")
	}
	e.call("GET", path, nil, 200, &movie)
	e.call("PUT", path, movie, 202, nil)
	if n := e.one(`SELECT count(*) FROM arrview_removed_version WHERE version_id = 2`); n != 1 {
		t.Fatal("echoed PUT restored a removed copy")
	}

	// A real change still applies to every copy of the view.
	movie["monitored"] = false
	e.call("PUT", path, movie, 202, nil)
	if n := e.one(`SELECT count(*) FROM version WHERE id = 1 AND monitored = 1`); n != 0 {
		t.Fatal("monitored=false did not apply")
	}
}

// A profile change that turns on an existing, unmonitored copy gives it the body's seasons.
func TestPutProfileTurnsOnSeasonsOfExistingCopy(t *testing.T) {
	e := newEnv(t)
	cp := *e.pol.Versions["tv-2160p"]
	e.pol.Versions["tv-copy"] = &cp
	v := e.pol.Views["sonarr"]
	v.Versions = []string{"tv-2160p", "tv-copy"}
	v.Profiles = map[string][]string{"Original": {"tv-2160p"}, "Other copy": {"tv-copy"}}
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at)
		VALUES (20, 10, 'tv-copy', 0, 'wanted', 0, 0)`)
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
	if got := e.one(`SELECT monitored FROM version WHERE id = 20`); got != 1 {
		t.Fatalf("copy not turned on: %d", got)
	}
	if got := e.one(`SELECT monitored FROM episode_version_state WHERE version_id = 20 AND episode_id = 102`); got != 1 {
		t.Fatalf("re-requested season 1 left S01E02 of the copy unmonitored: %d", got)
	}
}
