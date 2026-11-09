package router

import (
	"slices"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

// The unproven candidates of one route are re-searched together, with all their episodes (one
// re-search each was deduplicated into the first).
func TestUnprovenBatchResearchesAllEpisodes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["series"].PreferSeaDex = true
	e.exec(`INSERT INTO episode(id,title_id,season,number,name,air_date,created_at,updated_at) VALUES(101,2,1,1,'One','2024-01-01',0,0),(102,2,1,2,'Two','2024-01-02',0,0)`)
	e.exec(`UPDATE episode SET air_date_utc_at=1000 WHERE title_id=2`)
	withQueue(e)
	eng, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetProfiles(eng.Profile)
	e.r.SetDecider(eng.Decide, eng.DecideSearch)
	a := cand(h("a"), "Show.S01E01.2160p.WEB-DL-GRP")
	b := cand(h("b"), "Show.S01E02.2160p.WEB-DL-GRP")
	a.Episodes, b.Episodes = []int64{101}, []int64{102}
	res, err := e.r.routeStored(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "rss", Candidates: []Candidate{a, b}})
	if err != nil || res.Outcome != OutcomeCancelled {
		t.Fatalf("route: %+v %v", res, err)
	}
	got := researchJobs(e)
	if len(got) != 1 || got[0].Reason != "legacy_criteria" {
		t.Fatalf("want one re-search for the batch: %+v", got)
	}
	eps := slices.Clone(got[0].Episodes)
	slices.Sort(eps)
	if !slices.Equal(eps, []int64{101, 102}) {
		t.Fatalf("re-search episodes %v, want 101 and 102", eps)
	}
}
