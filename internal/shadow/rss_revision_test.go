package shadow

import "testing"

// An RSS item remembered as decided (a profile rejection, or a pick) under one policy revision is
// decided again under another: a profile changed to accept it gets to see it while it is still in the feed.
func TestRSSSeenPerPolicyRevision(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.r.pol.Revision = "rev1"
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0})
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
