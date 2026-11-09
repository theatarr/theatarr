package router

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
)

// A provider's own stalled_after replaces router.stalled_after for its downloads; another provider keeps the
// router's value.
func TestProviderStalledAfterOverride(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	long := policy.Duration(30 * time.Minute)
	e.pol.Providers["tb"].StalledAfter = &long
	for _, row := range []struct {
		id       int64
		pk, hash string
	}{{1, "tb", h("a")}, {2, "rd", h("b")}} {
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
			provider_torrent_id, state, own_add, added_at, created_at, updated_at) VALUES (?, 1, 10, 'movie', ?, 'x', 'pub',
			?, ?, 'added', 0, ?, 0, 0)`, row.id, row.hash, row.pk, "t"+row.hash[:2], store.Millis(e.clock))
		e.pr[row.pk].set("t"+row.hash[:2], provider.Torrent{Hash: row.hash, Status: "queued", State: provider.StateQueued})
	}
	e.clock = e.clock.Add(11 * time.Minute)
	sum, err := e.r.Poll(e.ctx)
	if err != nil || sum.Stalled != 1 || e.count(`SELECT COUNT(*) FROM download WHERE id = 2 AND state = 'failed'`) != 1 {
		t.Fatalf("at 11m only the RD download stalls: %+v %v", sum, err)
	}
	e.clock = e.clock.Add(20 * time.Minute)
	sum, err = e.r.Poll(e.ctx)
	if err != nil || sum.Stalled != 1 || e.count(`SELECT COUNT(*) FROM download WHERE id = 1 AND state = 'failed'`) != 1 {
		t.Fatalf("at 31m the TorBox download stalls: %+v %v", sum, err)
	}
}
