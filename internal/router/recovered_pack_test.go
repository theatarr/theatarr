package router

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// A download resumed from its row (requeue, strays, retry) keeps the search criteria it was found with.
func TestRecoveredWantedPackKeepsCriteria(t *testing.T) {
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
	req, err := DownloadRequest(e.ctx, e.st.Reader(), e.pol, res.DownloadID)
	if err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.Add(2 * time.Minute)
	res, err = e.r.Route(e.ctx, *req)
	if err != nil || res.Outcome != OutcomeAdded {
		t.Fatalf("recovered wanted pack lost criteria: %+v %v", res, err)
	}
}

// A: a hashless prefer-on release is added by its magnet's hash; the caller's candidates are not mutated.
func TestPreferOnTransportHash(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie"].PreferSeaDex = true
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
