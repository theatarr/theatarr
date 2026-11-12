package rss

import (
	"slices"
	"testing"
)

// A Watch-only library gets nothing from RSS: with movie-web Live and movie-remux Watch only, both wanting Alien, one
// item gives one route for movie-web and no router plan for movie-remux.
func TestRSSSkipsWatchOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.pol.Versions["movie-remux"].Mode = ""
	e.build(nil)
	if got := e.r.ActiveVersions(); !slices.Equal(got, []string{"movie-web", "tv"}) {
		t.Fatalf("active %v", got)
	}
	e.seed(t)
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0})
	rs, err := e.r.RSS(e.ctx)
	if err != nil || rs.Routed != 1 || rs.Decided != 1 {
		t.Fatalf("rss %+v %v", rs, err)
	}
	if got, want := e.rec.candidates(), []string{"movie-web | prowlarr | " + alienWebRel}; !slices.Equal(got, want) {
		t.Fatalf("routed %v, want %v", got, want)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM router_plan`); n != 0 {
		t.Errorf("%d router plans", n)
	}
}
