package router

import (
	"encoding/json"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"testing"
)

func TestHashlessReleaseMustNotAcquireSnapshotHash(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	const record = "6vdej49pr26gca0"
	snapshot := indexer.SeaDexSnapshot{AniList: 1535, Torrents: []indexer.SeaDexStanding{{RecordID: record, InfoHash: h("a"), Preference: 3}}}
	body, _ := json.Marshal(snapshot)
	e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','1535',200,?,0,253402214400000)`, body)
	c := cand("", "Movie.2024.1080p.BluRay ("+record+")")
	c.Release.Magnet = indexer.NewLink("magnet:?xt=urn:btih:" + h("b"))
	req := grabReq(c)
	res, err := e.r.Route(e.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if req.Candidates[0].Release.InfoHash != "" {
		t.Fatal("route mutated caller candidates")
	}
	if len(e.ad["rd"].adds) != 1 || e.ad["rd"].adds[0] != h("b") {
		t.Fatalf("hashless release's magnet names %s, but router added %s: %+v", h("b"), e.ad["rd"].adds, res)
	}
}

func TestWantedMovieRetainsSearchContextDuringRedecision(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	freshMovie(e)
	e.pol.Versions["movie"].PreferSeaDex = true
	e.exec(`UPDATE title SET year=2024,runtime_minutes=110 WHERE id=1`)
	e.link(1, 10, 0, "WEBDL-2160p", 0, 1000)
	eng, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetProfiles(eng.Profile)
	e.r.SetDecider(eng.Decide, eng.DecideSearch)
	c := cand(h("a"), "Movie.2024.PROPER.2160p.WEB-DL-GRP")
	c.Release.Seeders = 10
	title, err := decide.LoadTitle(e.ctx, e.st.Reader(), 1)
	if err != nil {
		t.Fatal(err)
	}
	version, err := decide.LoadVersion(e.ctx, e.st.Reader(), e.pol, 1, "movie")
	if err != nil {
		t.Fatal(err)
	}
	held, err := decide.LoadExisting(e.ctx, e.st.Reader(), 10)
	if err != nil {
		t.Fatal(err)
	}
	d, err := eng.Decide(decide.WithSearch(e.ctx), decide.Release{Release: c.Release}, title, version, held)
	if err != nil || !d.Accepted {
		t.Fatalf("bad setup: %+v %v", d, err)
	}
	res, err := e.r.Route(e.ctx, freshReq(c, "wanted"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded {
		t.Fatalf("wanted search decision cancelled during redecision: %+v", res)
	}
}

func TestRSSSeriesRetainsRSSContextDuringRedecision(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Profiles = map[string]*policy.Profile{"tv": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}}
	e.pol.Versions["series"].Profile = "tv"
	e.pol.Versions["series"].PreferSeaDex = true
	e.exec(`UPDATE title SET year=2024,runtime_minutes=45 WHERE id=2`)
	e.exec(`INSERT INTO episode(id,title_id,season,number,name,air_date,created_at,updated_at) VALUES(101,2,1,1,'Pilot','2024-01-01',0,0)`)
	e.link(2, 20, 101, "WEBDL-2160p", 0, 1000)
	e.exec(`UPDATE episode SET air_date_utc_at=1000 WHERE title_id=2`)
	eng, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetProfiles(eng.Profile)
	e.r.SetDecider(eng.Decide, eng.DecideSearch)
	c := cand(h("a"), "Show.S01E01.PROPER.2160p.WEB-DL-GRP")
	c.Release.Seeders = 10
	c.Episodes = []int64{101}
	title, err := decide.LoadTitle(e.ctx, e.st.Reader(), 2)
	if err != nil {
		t.Fatal(err)
	}
	version, err := decide.LoadVersion(e.ctx, e.st.Reader(), e.pol, 2, "series")
	if err != nil {
		t.Fatal(err)
	}
	if err := decide.LoadSeriesVersion(e.ctx, e.st.Reader(), &title, 20); err != nil {
		t.Fatal(err)
	}
	d, err := eng.Decide(e.ctx, decide.Release{Release: c.Release}, title, version, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Accepted {
		t.Fatalf("bad setup, RSS should reject proper for old file: %+v", d)
	}
	res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "rss", Candidates: []Candidate{c}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome == OutcomeAdded {
		t.Fatalf("RSS proper for old file was routed: %+v", res)
	}
}

func TestRSSPackRedecisionKeepsSingleWantedEpisode(t *testing.T) {
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
	res, err := e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "rss", Candidates: []Candidate{c}})
	if err != nil || res.Outcome != OutcomeAdded {
		t.Fatalf("pack with one wanted episode: %+v %v", res, err)
	}
}
