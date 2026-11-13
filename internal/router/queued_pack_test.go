package router

import (
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
	"testing"
)

func TestQueuedSearchPackKeepsSeason(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Profiles = map[string]*policy.Profile{"tv": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}}
	e.pol.Versions["series"].Profile = "tv"
	e.pol.Versions["series"].PreferSeaDex = true
	e.exec(`UPDATE title SET runtime_minutes=45 WHERE id=2`)
	e.exec(`INSERT INTO episode(id,title_id,season,number,name,air_date,created_at,updated_at) VALUES(101,2,1,1,'Pilot','2024-01-01',0,0),(102,2,1,2,'Second','2024-01-02',0,0)`)
	e.link(2, 20, 101, "WEBDL-2160p", 0, 1000)
	e.exec(`UPDATE episode SET air_date_utc_at=1000 WHERE title_id=2`)
	eng, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetProfiles(eng.Profile)
	e.r.SetDecider(eng.Decide, eng.DecideSearch)
	c := cand(h("a"), "Show.S01.2160p.WEB-DL-GRP")
	c.Release.Seeders = 10
	c.Episodes = []int64{101, 102}
	c.Search = true
	c.Criteria = []*decide.SeriesSearch{{Kind: profile.SearchSeason, Season: 1, Episodes: []int64{101, 102}, MonitoredOnly: true}}
	res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "wanted", Candidates: []Candidate{c}})
	if err != nil || res.Outcome != OutcomeAdded {
		t.Fatalf("pack with one wanted episode: %+v %v", res, err)
	}
}
