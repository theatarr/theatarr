package shadow

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/store"
)

// RSS ranks with the stored cache evidence a search applies: of two equally scored releases for the same title,
// the one with a cached provider_cache row is routed first, whichever of the two it is.
func TestRSSStoredCacheBreaksTie(t *testing.T) {
	t.Parallel()
	const hAmzn = "8888888888888888888888888888888888888888"
	for _, cached := range []string{hAlienWeb, hAmzn} {
		t.Run(cached[:1], func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, false)
			rt, _, w := e.withRouter(t, "movie-web")
			e.svc.SetEvidence(indexer.NewEvidence(indexer.EvidenceConfig{Store: e.st, Policy: e.pol, Now: e.clock}))
			if _, err := e.r.Import(e.ctx); err != nil {
				t.Fatal(err)
			}
			e.exec(t, `INSERT INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at)
				VALUES (?, 'tb', 'cached', 'torbox:check', ?, ?)`, cached, store.Millis(t0), store.Millis(t0.Add(time.Hour)))
			e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0.Add(-10 * time.Minute)},
				item{title: "Alien.Romulus.2024.2160p.AMZN.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX", hash: hAmzn,
					size: 20e9, pub: t0.Add(-10 * time.Minute)})
			if rs, err := e.r.RSS(e.ctx); err != nil || rs.Routed != 1 {
				t.Fatalf("rss %+v %v", rs, err)
			}
			if _, err := w.RunOne(e.ctx); err != nil {
				t.Fatal(err)
			}
			if adds := rt.Adders["tb"].Adds(); len(adds) != 1 || adds[0] != cached {
				t.Fatalf("adds %v, want the cached %s first", adds, cached)
			}
		})
	}
}
