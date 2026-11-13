package router

import (
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"testing"
	"time"
)

// A queued row without its identity and origin (release_data lost) is unproven: it is not re-decided from a
// guess but ended, and its episodes are searched again.
func TestLegacyNullReleaseData(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Profiles = map[string]*policy.Profile{"tv": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}}
	e.pol.Versions["series"].Profile = "tv"
	e.pol.Versions["series"].PreferSeaDex = true
	e.exec(`UPDATE title SET runtime_minutes=45 WHERE id=2`)
	e.exec(`INSERT INTO episode(id,title_id,season,number,name,air_date,created_at,updated_at) VALUES(101,2,1,1,'Pilot','2024-01-01',0,0),(102,2,1,2,'Second','2024-01-02',0,0)`)
	e.exec(`UPDATE episode SET air_date_utc_at=1000 WHERE title_id=2`)
	e.link(2, 20, 101, "WEBDL-2160p", 0, 1000)
	eng, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetProfiles(eng.Profile)
	e.r.SetDecider(eng.Decide, eng.DecideSearch)
	q := jobs.NewQueue(e.st)
	e.r.RegisterJobs(jobs.NewWorker(q, jobs.WorkerOptions{}), q)
	c := cand(h("a"), "Show.S01.2160p.WEB-DL-GRP")
	c.Release.Seeders = 10
	c.Episodes = []int64{101, 102}
	c.Search = true
	c.Criteria = []*decide.SeriesSearch{{Kind: profile.SearchSeason, Season: 1, Episodes: []int64{101, 102}, MonitoredOnly: true}}
	for _, k := range []string{"rd", "ad", "tb"} {
		e.exec(`INSERT INTO router_pacing(provider,backoff_until,failures,reason,updated_at) VALUES(?,?,1,'x',0)`, k, e.clock.Add(time.Minute).UnixMilli())
	}
	res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "wanted", Candidates: []Candidate{c}})
	if err != nil || res.Outcome != OutcomeDeferred {
		t.Fatalf("bad setup: %+v %v", res, err)
	}
	e.exec(`UPDATE download SET release_data=NULL WHERE id=?`, res.DownloadID)
	req, err := DownloadRequest(e.ctx, e.st.Reader(), e.pol, res.DownloadID)
	if err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.Add(2 * time.Minute)
	res, err = e.r.Route(e.ctx, *req)
	if err != nil || res.Outcome != OutcomeCancelled {
		t.Fatalf("unproven row was trusted: %+v %v", res, err)
	}
	if len(e.ad["rd"].adds) != 0 {
		t.Fatal("unproven row added")
	}
	if got := researchJobs(e); len(got) != 1 || got[0].Reason != "legacy_criteria" {
		t.Fatalf("missing re-search: %+v", got)
	}
}

func TestLegacyUnprovablePackResearches(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Profiles = map[string]*policy.Profile{"tv": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}}
	e.pol.Versions["series"].Profile = "tv"
	e.pol.Versions["series"].PreferSeaDex = true
	e.exec(`UPDATE title SET runtime_minutes=45, use_scene_numbering=1 WHERE id=2`)
	e.exec(`INSERT INTO episode(id,title_id,season,number,name,air_date,created_at,updated_at) VALUES(101,2,1,1,'Pilot','2024-01-01',0,0),(102,2,1,2,'Second','2024-01-02',0,0)`)
	e.exec(`UPDATE episode SET air_date_utc_at=1000 WHERE title_id=2`)
	e.exec(`INSERT INTO episode_number(episode_id,title_id,scheme,season,number) VALUES(101,2,'scene',2,1),(102,2,'scene',2,2)`)
	e.link(2, 20, 101, "WEBDL-2160p", 0, 1000)
	q := jobs.NewQueue(e.st)
	e.r.RegisterJobs(jobs.NewWorker(q, jobs.WorkerOptions{}), q)
	eng, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetProfiles(eng.Profile)
	e.r.SetDecider(eng.Decide, eng.DecideSearch)
	c := cand(h("a"), "Show.S02.2160p.WEB-DL-GRP")
	c.Release.Seeders = 10
	c.Episodes = []int64{101, 102}
	c.Search = true
	c.Criteria = []*decide.SeriesSearch{{Kind: profile.SearchSeason, Season: 2, Episodes: []int64{101, 102}, MonitoredOnly: true}}
	for _, k := range []string{"rd", "ad", "tb"} {
		e.exec(`INSERT INTO router_pacing(provider,backoff_until,failures,reason,updated_at) VALUES(?,?,1,'x',0)`, k, e.clock.Add(time.Minute).UnixMilli())
	}
	res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "wanted", Candidates: []Candidate{c}})
	if err != nil || res.Outcome != OutcomeDeferred {
		t.Fatalf("bad setup: %+v %v", res, err)
	}
	e.exec(`UPDATE download SET release_data=json_remove(release_data, '$.route_search', '$.route_criteria') WHERE id=?`, res.DownloadID)
	e.exec(`DELETE FROM episode_number WHERE title_id=2`)
	req, err := DownloadRequest(e.ctx, e.st.Reader(), e.pol, res.DownloadID)
	if err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.Add(2 * time.Minute)
	res, err = e.r.Route(e.ctx, *req)
	if err != nil || res.Outcome != OutcomeCancelled {
		t.Fatalf("unproven route: %+v %v", res, err)
	}
	if len(e.ad["rd"].adds) != 0 {
		t.Fatal("unproven pack added")
	}
	if got := researchJobs(e); len(got) != 1 || got[0].Reason != "legacy_criteria" {
		t.Fatalf("missing re-search: %+v", got)
	}
}
