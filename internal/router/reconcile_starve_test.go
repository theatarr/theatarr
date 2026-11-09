package router

import (
	"context"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

// TestPollRecoveryDoesNotStarveProviders: a provider whose account listing used up the whole
// recovery pass had its result thrown away before poll_tried_at was written, so its interrupted add sorted first
// again on every pass and the adds on healthy providers were never looked at. Each listing has its share of the pass
// now, and an add whose lookup was cut is marked tried without being settled or noted.
func TestPollRecoveryDoesNotStarveProviders(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	old := reconcilePassLimit
	reconcilePassLimit = 150 * time.Millisecond
	t.Cleanup(func() { reconcilePassLimit = old })
	sent := e.clock.Add(-addSettle - time.Minute).UnixMilli()
	// Real-Debrid's adds come first (ids, never tried), and its listing never answers.
	for i, hash := range []string{h("a"), h("b")} {
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
			provider, created_at, updated_at) VALUES (?, 1, 10, 'movie', ?, 'R', 'pub', 'adding', 'rd', 0, ?)`,
			i+1, hash, sent)
	}
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		provider, created_at, updated_at) VALUES (3, 1, 10, 'movie', ?, 'R', 'pub', 'adding', 'ad', 0, ?)`, h("c"), sent)
	// An add naming a provider with no client is not the poller's: never selected.
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		provider, created_at, updated_at) VALUES (4, 1, 10, 'movie', ?, 'R', 'pub', 'adding', 'gone', 0, ?)`, h("d"), sent)
	e.pr["rd"].onList = func(ctx context.Context) { <-ctx.Done() }
	e.pr["rd"].listErr = context.DeadlineExceeded
	e.pr["ad"].list = []provider.Torrent{{ID: "ad-3", Hash: h("c")}}
	sum, err := e.r.Poll(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Reconciled != 1 || e.count(`SELECT COUNT(*) FROM download WHERE id = 3 AND state <> 'adding'`) != 1 {
		t.Fatalf("the healthy provider's add was not settled: %+v", sum)
	}
	if e.pr["rd"].listReads != 1 {
		t.Fatalf("rd listed %d times in one pass", e.pr["rd"].listReads)
	}
	// The cut lookups are marked tried, and nothing else is written on them.
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id IN (1, 2) AND state = 'adding' AND error IS NULL
		AND poll_tried_at IS NOT NULL`); n != 2 {
		t.Fatalf("%d of the cut lookups marked tried and untouched", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = 4 AND poll_tried_at IS NULL`); n != 1 {
		t.Fatal("an add on a provider with no client was looked up")
	}
}
