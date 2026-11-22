package profile

import "testing"

// TestExplain: the words come from the step that decided, with its own operands: a revision when the quality is
// equal, the provider that broke the cache tie, peers when the seeders tie, and a merged search's own steps.
func TestExplain(t *testing.T) {
	t.Parallel()
	pair := func() (*Engine, *Subject, *Subject) {
		e, x := radarrSubject(t)
		_, y := radarrSubject(t)
		y.Profile = x.Profile
		return e, x, y
	}
	e, x, y := pair()
	x.Release.Quality.Revision.Version = 2
	if got := e.Explain(x, y); got != "a newer revision, a proper or repack (v2 over v1)" {
		t.Errorf("revision: %q", got)
	}
	e, x, y = pair()
	x.Cached = CacheStanding{First: 1, Count: 1, FirstKind: "alldebrid"}
	y.Cached = CacheStanding{First: 2, Count: 2, FirstKind: "torbox"}
	if got := e.Explain(x, y); got != "cached on AllDebrid" {
		t.Errorf("cache order: %q", got)
	}
	e, x, y = pair()
	x.Release.Protocol, y.Release.Protocol = ProtocolTorrent, ProtocolTorrent
	s1, s2, p1, p2 := 10, 20, 90, 30
	x.Release.Seeders, y.Release.Seeders, x.Release.Peers, y.Release.Peers = &s1, &s2, &p1, &p2
	if got := e.Explain(x, y); got != "more peers (90 over 30)" {
		t.Errorf("peers: %q", got)
	}
	if got := e.Explain(y, x); got != "" {
		t.Errorf("the other way: %q", got)
	}
	// A merged search: a cached pack of several seasons over a cached one-season pack, by the searched episodes.
	e, x, y = pair()
	x.Release.FullSeason, y.Release.FullSeason = true, true
	x.Cached, y.Cached = CacheStanding{Count: 1}, CacheStanding{Count: 1}
	scoped := map[*Subject]int{x: 20, y: 10}
	if got := e.ExplainMerged(x, y, func(s *Subject) int { return scoped[s] }); got != "more of the searched episodes" {
		t.Errorf("merged: %q", got)
	}
	// Size decides by its name only: the walk that ranks says it, whatever the sizes are.
	e, x, y = pair()
	x.Release.Size, y.Release.Size = 4<<30, 1<<30
	if c := e.Compare(x, y); c > 0 {
		if got := e.Explain(x, y); got != "its size" {
			t.Errorf("size: %q", got)
		}
	} else if got := e.Explain(y, x); got != "its size" {
		t.Errorf("size the other way: %q", got)
	}
}
