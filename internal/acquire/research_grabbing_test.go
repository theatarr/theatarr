package acquire

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/router"
)

// A re-search queued by `router reconcile` waits while grabbing is off: the worker queues it again
// instead of consuming it, and it searches once router.enabled is on. An ordinary re-search while off is still
// dropped.
func TestReconcileSearchWaitsForGrabbing(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false, nil)
	e.exec(`INSERT INTO download (id,title_id,version_id,version_key,info_hash,release_title,indexer,state,episodes,source,
		release_data,created_at,updated_at) VALUES(7,1,101,'movie',?,'One 2024','pub','queued','[]','wanted','{}',0,0)`, h("a"))
	rep, err := router.ReconcileLegacyQueue(e.ctx, e.st, e.q, true, e.clock())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Queued != 1 {
		t.Fatalf("report %+v", rep)
	}
	e.drain()
	e.advance(reconcileRecheck + time.Minute)
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'`, router.JobResearch); n != 1 {
		t.Fatalf("reconcile re-search not waiting while grabbing is off: %d queued", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run`); n != 0 {
		t.Fatalf("searched while grabbing is off: %d runs", n)
	}
	e.pol.Router.Enabled = true
	e.advance(reconcileRecheck + time.Minute)
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = 'reconcile'`); n == 0 {
		t.Fatal("reconcile re-search did not run once grabbing was on")
	}

	o := newEnv(t, false, nil)
	if _, err := o.a.Research(o.ctx, router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie",
		Reason: router.ReasonExhausted}); err != nil {
		t.Fatal(err)
	}
	if n := o.count(`SELECT COUNT(*) FROM job WHERE kind = ?`, router.JobResearch); n != 0 {
		t.Fatalf("ordinary re-search queued again while off: %d", n)
	}
}
