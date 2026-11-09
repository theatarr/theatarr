package router

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

// With PreferSeaDex off too, a deferred RSS release whose scene mapping changed while it waited is placed for the episodes it maps to
// now, not the ones it was queued with.
func TestDeferredRemapWithoutSeaDex(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Profiles = map[string]*policy.Profile{"tv": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}},
		Cutoff: "WEBDL-2160p"}}
	e.pol.Versions["series"].Profile = "tv"
	e.pol.Versions["series"].PreferSeaDex = false
	e.exec(`UPDATE title SET runtime_minutes = 45, use_scene_numbering = 1 WHERE id = 2`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, air_date, air_date_utc_at, created_at, updated_at)
		VALUES (101, 2, 1, 1, 'Pilot', '2024-01-01', 1000, 0, 0), (102, 2, 1, 2, 'Second', '2024-01-02', 1000, 0, 0)`)
	e.exec(`INSERT INTO episode_number (episode_id, title_id, scheme, season, number)
		VALUES (101, 2, 'scene', 2, 1), (102, 2, 'scene', 2, 2)`)
	eng, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetProfiles(eng.Profile)
	e.r.SetDecider(eng.Decide, eng.DecideSearch)
	for _, k := range []string{"rd", "ad", "tb"} {
		e.exec(`INSERT INTO router_pacing (provider, backoff_until, failures, reason, updated_at)
			VALUES (?, ?, 1, 'x', 0)`, k, e.clock.Add(time.Minute).UnixMilli())
	}
	c := cand(h("a"), "Show.S02E01.2160p.WEB-DL-GRP")
	c.Release.Seeders = 10
	c.Episodes = []int64{101}
	res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "rss",
		Candidates: []Candidate{c}})
	if err != nil || res.Outcome != OutcomeDeferred {
		t.Fatalf("initial route: %+v %v steps=%s", res, err, steps(res))
	}
	// The mapping swaps: scene S02E01 is now episode 102.
	e.exec(`DELETE FROM episode_number WHERE title_id = 2 AND scheme = 'scene'`)
	e.exec(`INSERT INTO episode_number (episode_id, title_id, scheme, season, number)
		VALUES (102, 2, 'scene', 2, 1), (101, 2, 'scene', 2, 2)`)
	req, err := DownloadRequest(e.ctx, e.st.Reader(), e.pol, res.DownloadID)
	if err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.Add(2 * time.Minute)
	if res, err = e.r.Route(e.ctx, *req); err != nil {
		t.Fatal(err)
	}
	var eps string
	if err := e.st.Reader().QueryRow(`SELECT episodes FROM download WHERE id = ?`, res.DownloadID).Scan(&eps); err != nil {
		t.Fatal(err)
	}
	if eps != "[102]" {
		t.Fatalf("download episodes = %s, want [102] (the new mapping); %+v steps=%s", eps, res, steps(res))
	}
}
