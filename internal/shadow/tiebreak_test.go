package shadow

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
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

// The stored evidence RSS ranks by does not reorder the route's provider accounts: with two TorBox accounts and a
// cached row on the second only, the routed release is added there first.
func TestRSSKeepsAccountOrder(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.pol.Providers["tb2"] = &policy.Provider{Kind: provider.KindTorBox, Order: 3, URL: "https://api.torbox.app/v1",
		APIKeyEnv: "THEATARR_TB2_KEY", Pacing: policy.Pacing{AddsPerMinute: 100}}
	rt, _, w := e.withRouter(t, "movie-web")
	e.pol.Versions["movie-web"].Providers = []string{"tb", "tb2"}
	e.svc.SetEvidence(indexer.NewEvidence(indexer.EvidenceConfig{Store: e.st, Policy: e.pol, Now: e.clock}))
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at)
		VALUES (?, 'tb2', 'cached', 'router:add', ?, ?)`, hAlienWeb, store.Millis(t0), store.Millis(t0.Add(time.Hour)))
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0.Add(-10 * time.Minute)})
	if rs, err := e.r.RSS(e.ctx); err != nil || rs.Routed != 1 {
		t.Fatalf("rss %+v %v", rs, err)
	}
	if _, err := w.RunOne(e.ctx); err != nil {
		t.Fatal(err)
	}
	if tb, tb2 := rt.Adders["tb"].Adds(), rt.Adders["tb2"].Adds(); len(tb) != 0 || len(tb2) != 1 {
		t.Fatalf("adds tb %v tb2 %v, want the cached account tb2 first", tb, tb2)
	}
}

// The arr's release is ranked with our search results on the same cache evidence: at an equal score, the arr's
// release cached on the version's first provider beats a search result cached on the second, and is the pick.
func TestShadowGrabArrReleaseCacheEvidence(t *testing.T) {
	t.Parallel()
	const hOther = "9999999999999999999999999999999999999999"
	e := newEnv(t, false)
	e.withRouter(t)
	e.svc.SetEvidence(indexer.NewEvidence(indexer.EvidenceConfig{Store: e.st, Policy: e.pol, Now: e.clock}))
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][2]string{{hDuneRemux, "rd"}, {hOther, "tb"}} {
		e.exec(t, `INSERT INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at)
			VALUES (?, ?, 'cached', 'router:add', ?, ?)`, row[0], row[1], store.Millis(t0), store.Millis(t0.Add(time.Hour)))
	}
	e.prow.set(dune.imdb, item{title: duneRemux, hash: hOther, size: 40e9, pub: t0.Add(-48 * time.Hour)})
	e.zurg.add(grabEvent(2, dune, t0.Add(-time.Hour), duneRemux, hDuneRemux, "Remux-2160p", 31, 1950, "Search"))
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	res, err := e.r.GrabByID(e.ctx, "radarr_zurg", 2, false)
	if err != nil || !res.Searched || res.Results != 1 {
		t.Fatalf("grab %+v %v", res, err)
	}
	if res.Pick == nil || !res.Pick.Arr || res.Pick.InfoHash != hDuneRemux {
		t.Fatalf("pick %+v, want the arr's release (cached on rd)", res.Pick)
	}
}

// planAt is the provider and hash of the shadow plan recorded for a grab.
func planAt(t *testing.T, e *env, grabID int64) (string, string) {
	t.Helper()
	var prov, hash string
	if err := e.st.Reader().QueryRow(`SELECT p.provider, p.info_hash FROM shadow_grab g JOIN router_plan p
		ON p.id = g.router_plan_id WHERE g.id = ?`, grabID).Scan(&prov, &hash); err != nil {
		t.Fatal(err)
	}
	return prov, hash
}

// The cache evidence a shadow search ranks by does not reorder the plan's provider accounts: with two TorBox
// accounts and a cached row on the second only, the plan adds there, for the arr's release and for a search result.
func TestShadowGrabPlanKeepsAccountOrder(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, hash string
		search     bool
	}{{"arr", hDuneRemux, false}, {"search", hArgRemux, true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, false)
			e.pol.Providers["tb2"] = &policy.Provider{Kind: provider.KindTorBox, Order: 3, URL: "https://api.torbox.app/v1",
				APIKeyEnv: "THEATARR_TB2_KEY", Pacing: policy.Pacing{AddsPerMinute: 100}}
			e.withRouter(t)
			e.pol.Versions["movie-remux"].Providers = []string{"tb", "tb2"}
			e.svc.SetEvidence(indexer.NewEvidence(indexer.EvidenceConfig{Store: e.st, Policy: e.pol, Now: e.clock}))
			if _, err := e.r.Import(e.ctx); err != nil {
				t.Fatal(err)
			}
			e.exec(t, `INSERT INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at)
				VALUES (?, 'tb2', 'cached', 'router:add', ?, ?)`, tc.hash, store.Millis(t0), store.Millis(t0.Add(time.Hour)))
			id := 2
			if tc.search {
				e.prow.set(argylle.imdb, item{title: argRemux, hash: hArgRemux, size: 60e9, pub: t0.Add(-96 * time.Hour)})
				e.zurg.add(grabEvent(id, argylle, t0.Add(-time.Hour), argWeb, hArgWeb, "WEBDL-2160p", 18, 1700, "Search"))
			} else {
				e.zurg.add(grabEvent(id, dune, t0.Add(-time.Hour), duneRemux, hDuneRemux, "Remux-2160p", 31, 1950, "Search"))
			}
			if _, err := e.r.Import(e.ctx); err != nil {
				t.Fatal(err)
			}
			res, err := e.r.GrabByID(e.ctx, "radarr_zurg", id, !tc.search)
			if err != nil {
				t.Fatal(err)
			}
			if prov, hash := planAt(t, e, res.GrabID); prov != "tb2" || hash != tc.hash {
				t.Fatalf("plan %s on %s, want %s on tb2", hash, prov, tc.hash)
			}
		})
	}
}

// The arr's release counts only the providers its own indexer allows: cached on tb alone, behind an indexer that
// allows only rd, it has no cache standing, and an equal search result with more seeders is the pick.
func TestShadowGrabArrStandingIndexerAllowlist(t *testing.T) {
	t.Parallel()
	const hOther = "9999999999999999999999999999999999999999"
	e := newEnv(t, false)
	e.withRouter(t)
	e.pol.Indexers["prowlarr"].Providers = []string{"rd"}
	e.svc.SetEvidence(indexer.NewEvidence(indexer.EvidenceConfig{Store: e.st, Policy: e.pol, Now: e.clock}))
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at)
		VALUES (?, 'tb', 'cached', 'router:add', ?, ?)`, hDuneRemux, store.Millis(t0), store.Millis(t0.Add(time.Hour)))
	e.prow.set(dune.imdb, item{title: duneRemux, hash: hOther, size: 40e9, pub: t0.Add(-48 * time.Hour)})
	ev := grabEvent(2, dune, t0.Add(-time.Hour), duneRemux, hDuneRemux, "Remux-2160p", 31, 1950, "Search")
	ev["data"].(map[string]any)["indexer"] = "prowlarr"
	e.zurg.add(ev)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	res, err := e.r.GrabByID(e.ctx, "radarr_zurg", 2, false)
	if err != nil || res.Results != 1 {
		t.Fatalf("grab %+v %v", res, err)
	}
	if res.Pick == nil || res.Pick.Arr || res.Pick.InfoHash != hOther {
		t.Fatalf("pick %+v, want the search result: the arr's tb evidence is not allowed by its indexer", res.Pick)
	}
}
