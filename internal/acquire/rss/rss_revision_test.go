package rss

import "testing"

// An RSS item remembered as decided (a profile rejection) under one policy revision is decided again under another: a
// profile changed to accept it gets to see it while it is still in the feed.
func TestRSSSeenPerPolicyRevision(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.seed(t)
	e.r.pol.Revision = "rev1"
	e.prow.setRSS(item{title: alien1080p, hash: hAlienWeb, size: 8e9, pub: t0})
	if sum, err := e.r.RSS(e.ctx); err != nil || sum.New != 1 {
		t.Fatalf("first read: %+v %v", sum, err)
	}
	if sum, err := e.r.RSS(e.ctx); err != nil || sum.New != 0 {
		t.Fatalf("same revision: %+v %v", sum, err)
	}
	e.r.pol.Revision = "rev2"
	if sum, err := e.r.RSS(e.ctx); err != nil || sum.New != 1 || sum.Decided == 0 {
		t.Fatalf("new revision: %+v %v", sum, err)
	}
}
