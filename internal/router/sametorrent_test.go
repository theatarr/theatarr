package router

import (
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
)

// A prefer-off version upgrades a held 1080p file to the 2160p file of the same torrent, as before
// standing existed; only a prefer-on version treats the same torrent as no upgrade.
func TestSameTorrentUpgradePreferOff(t *testing.T) {
	t.Parallel()
	for _, prefer := range []bool{false, true} {
		e := newEnv(t, true)
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
