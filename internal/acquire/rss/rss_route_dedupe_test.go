package rss

import "testing"

// Two RSS reads of the same release before the worker opens a download queue one router.route job for that target
// (routeOptions dedupes new RSS routes).
func TestRSSPendingRouteNotDuplicated(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.withRouter(t, "movie-web")
	e.seed(t)
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0})
	for range 2 {
		if _, err := e.r.RSS(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM job WHERE kind = 'router.route' AND state = 'queued'`); n != 1 {
		t.Fatalf("%d queued routes for the same RSS target", n)
	}
}
