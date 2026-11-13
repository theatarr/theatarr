package router

import (
	"fmt"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"testing"
)

func TestCuratedQueue(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	freshMovie(e)
	e.exec(`INSERT INTO title_external_id VALUES(1,'anilist','1')`)
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','1',200,?,0,253402214400000)`, fmt.Sprintf(`{"anilist":1,"torrents":[{"info_hash":"%s","preference":3,"is_best":true,"facts":{"isBest":true}}]}`, h("a")))
	vp := e.pol.Versions["movie"]
	vp.PreferSeaDex = true
	p, err := e.r.routeProfile(vp)
	if err != nil {
		t.Fatal(err)
	}
	low := cand(h("a"), "Movie.2024.1080p.WEB-DL-GRP")
	low.Quality = "WEBDL-1080p"
	low.QualityRank = 1
	low.Release.Flags = []string{indexer.FlagSeaDex, indexer.FlagSeaDexPreferred}
	high := cand(h("b"), "Movie.2024.2160p.WEB-DL-GRP")
	high.Quality = "WEBDL-2160p"
	high.QualityRank = 2
	s := &routeState{vp: vp, profile: p, req: Request{Version: "movie"}}
	if why := queueRefusal(s, &high, low.Quality, low.Score, low.Release); why == "" {
		t.Fatal("ordinary release can displace queued curated release")
	}
	if why := queueRefusal(s, &low, high.Quality, high.Score, high.Release); why != "" {
		t.Fatal("curated upgrade blocked by queue:", why)
	}
}

func TestCuratorUpgradeSurvivesAutomaticRoute(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	freshMovie(e)
	e.exec(`INSERT INTO title_external_id VALUES(1,'anilist','1')`)
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','1',200,?,0,253402214400000)`, fmt.Sprintf(`{"anilist":1,"torrents":[{"info_hash":"%s","preference":3,"is_best":true,"facts":{"isBest":true}}]}`, h("a")))
	e.pol.Versions["movie"].PreferSeaDex = true
	eng, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetProfiles(eng.Profile)
	e.r.SetDecider(eng.Decide, eng.DecideSearch)
	e.exec(`UPDATE title SET year=2024,runtime_minutes=110 WHERE id=1`)
	e.link(1, 10, 0, "WEBDL-2160p", 0, 1000)
	c := cand(h("a"), "Movie.2024.1080p.WEB-DL-GRP")
	c.Quality = "WEBDL-1080p"
	c.Release.Seeders = 10
	c.Release.Flags = []string{indexer.FlagSeaDex, indexer.FlagSeaDexPreferred}
	ctx, err := decide.WithStoredSeaDex(e.ctx, e.st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	d, err := eng.Decide(ctx, decide.Release{Release: c.Release}, decide.Title{ID: 1, Name: "Movie", Year: 2024, Kind: policy.KindMovie, Runtime: 110}, decide.Version{Key: "movie"}, &decide.ExistingFile{Quality: "WEBDL-2160p", Revision: 1, Size: 30e9})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Accepted || !d.Upgrade {
		t.Fatalf("decision setup: %+v", d)
	}
	res, err := e.r.Route(e.ctx, freshReq(c, "wanted"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded {
		t.Fatalf("accepted curator upgrade cancelled by router: %s %+v", res.Outcome, res.Steps)
	}
}
