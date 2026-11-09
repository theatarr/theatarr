package router

import (
	"context"
	"fmt"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"testing"
)

func TestOfferSweepSharesStanding(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	for k := range e.off.Policy.Versions {
		if k != "movie-remux" && k != "movie-web" {
			delete(e.off.Policy.Versions, k)
		}
	}
	e.off.Policy.Versions["movie-web"].PreferSeaDex = true
	e.exec(`INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(21,2,'movie-web',0,0)`)
	e.download(1, 1, 10, "movie-remux", h("a"), "Movie.2001.2160p.WEB-DL-GRP", "imported")
	e.download(2, 2, 20, "movie-remux", h("b"), "Other.2002.2160p.WEB-DL-GRP", "imported")
	for i, hash := range []string{h("a"), h("b")} {
		e.exec(`INSERT INTO title_external_id VALUES(?,'anilist',?)`, i+1, i+1)
		e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing',?,200,?,0,9999999999999)`, i+1, fmt.Sprintf(`{"anilist":%d,"torrents":[{"info_hash":"%s","preference":2,"is_best":true,"facts":{"isBest":true}}]}`, i+1, hash))
	}
	var ranks []int
	e.off.Decide = func(ctx context.Context, rel decide.Release, _ decide.Title, v decide.Version, _ *decide.ExistingFile) (*decide.Decision, error) {
		fresh := decide.RefreshSeaDexRelease(ctx, rel.Release)
		ranks = append(ranks, indexer.SeaDexPreference(&fresh))
		if len(ranks) == 1 {
			e.exec(`UPDATE metadata_cache SET body=json_set(body,'$.torrents',json('[]')) WHERE source='seadex-standing'`)
		}
		return &decide.Decision{Accepted: true, Version: v.Key, Release: rel.Title, Quality: "WEBDL-2160p"}, nil
	}
	sr, err := e.off.Sweep(e.ctx, true, 0)
	if err != nil || len(sr.Errors) > 0 {
		t.Fatalf("sweep: %+v %v", sr, err)
	}
	if len(ranks) != 2 || ranks[0] != 3 || ranks[1] != 3 {
		t.Fatalf("one sweep repinned standing between sources: %v", ranks)
	}
}

// A prefer-off version upgrades a held 1080p file to the 2160p file of the same torrent, as before
// standing existed; only a prefer-on version treats the same torrent as no upgrade.
func TestSameTorrentUpgradePreferOff(t *testing.T) {
	t.Parallel()
	for _, prefer := range []bool{false, true} {
		e := newEnv(t, true, policy.ModeActive)
		freshMovie(e)
		e.pol.Versions["movie"].PreferSeaDex = prefer
		eng, err := decide.New(e.pol, decide.Options{})
		if err != nil {
			t.Fatal(err)
		}
		e.r.SetProfiles(eng.Profile)
		id := e.link(1, 10, 0, "WEBDL-1080p", 0, 1000)
		e.exec(`UPDATE link SET file_metadata=? WHERE id=?`, `{"InfoHash":"`+h("a")+`","SceneName":"Movie.2024.1080p.WEB-DL-GRP"}`, id)
		c := cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP")
		res, err := e.r.Route(e.ctx, freshReq(c, "wanted"))
		if err != nil {
			t.Fatal(err)
		}
		if added := res.Outcome == OutcomeAdded; added == prefer {
			t.Fatalf("prefer=%v same-torrent upgrade outcome %s: %s", prefer, res.Outcome, steps(res))
		}
	}
}
