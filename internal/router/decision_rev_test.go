package router

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// The route jobs the router writes itself (a deferred route's continuation, a stray queued download's
// resume) carry the policy revision their candidates were decided under, so a later run under another revision
// decides them again.
func TestRouteJobsCarryDecisionRev(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	withQueue(e)
	e.r.pol.Revision = "rev-1"
	paceAll(e, 3*time.Minute)
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "A")))
	if err != nil || res.Outcome != OutcomeDeferred {
		t.Fatalf("%+v %v", res, err)
	}
	if next := routeJobs(e, res.DownloadID); len(next) != 1 || next[0].DecisionRev != "rev-1" {
		t.Errorf("continuation %+v, want decision_rev rev-1", next)
	}
	old := store.Millis(e.clock.Add(-time.Hour))
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, episodes, state, source, policy_rev, created_at, updated_at) VALUES (7, 1, 10, 'movie', ?,
		'Movie.2024.2160p.WEB', 'pub', 'WEBDL-2160p', 100, '[]', 'queued', 'rss', 'rev-0', ?, ?)`, h("b"), old, old)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := routeJobs(e, 7); len(got) != 1 || got[0].DecisionRev != "rev-0" {
		t.Errorf("stray resume %+v, want decision_rev rev-0", got)
	}
}
