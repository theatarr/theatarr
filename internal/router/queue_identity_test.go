package router

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
)

// A queued route loses the release's magnet (links never reach the payload). A release whose reported hash conflicts
// with its magnet is not queued at all; a release reporting no hash is queued and added by its magnet's,
// for prefer-off and prefer-on versions alike.
func TestQueuedRouteAddsMagnetHash(t *testing.T) {
	t.Parallel()
	for _, prefer := range []bool{false, true} {
		e := newEnv(t, true, policy.ModeActive)
		e.pol.Versions["movie"].PreferSeaDex = prefer
		q := jobs.NewQueue(e.st)
		q.SetClock(func() time.Time { return e.clock })
		w := jobs.NewWorker(q, jobs.WorkerOptions{})
		e.r.RegisterJobs(w, q)
		conflict := cand(h("a"), "Movie.2024.2160p.BluRay-GRP")
		conflict.Release.Magnet = indexer.BareMagnet(h("b"))
		if _, err := e.r.Enqueue(e.ctx, grabReq(conflict), time.Time{}); err == nil {
			t.Fatalf("prefer=%v a release whose reported hash conflicts with its magnet was queued", prefer)
		}
		hashless := cand("", "Movie.2024.1080p.BluRay-GRP")
		hashless.Release.Magnet = indexer.BareMagnet(h("c"))
		if _, err := e.r.Enqueue(e.ctx, grabReq(conflict, hashless), time.Time{}); err != nil {
			t.Fatal(err)
		}
		if ok, err := w.RunOne(e.ctx); err != nil || !ok {
			t.Fatalf("prefer=%v worker: %v %v", prefer, ok, err)
		}
		if adds := e.ad["rd"].adds; len(adds) != 1 || adds[0] != h("c") {
			t.Fatalf("prefer=%v queued route added %v, want only the hashless release's magnet hash %s", prefer, adds, h("c"))
		}
	}
}

// The synchronous route rejects a conflicting release too, with a step naming why, and adds neither hash.
func TestRouteRejectsConflictingIdentity(t *testing.T) {
	t.Parallel()
	for _, prefer := range []bool{false, true} {
		e := newEnv(t, true, policy.ModeActive)
		e.pol.Versions["movie"].PreferSeaDex = prefer
		c := cand(h("a"), "Movie.2024.2160p.BluRay-GRP")
		c.Release.Magnet = indexer.BareMagnet(h("b"))
		res, err := e.r.Route(e.ctx, grabReq(c))
		if err != nil {
			t.Fatal(err)
		}
		if len(e.ad["rd"].adds) != 0 || res.Outcome == OutcomeAdded {
			t.Fatalf("prefer=%v conflicting release added: %+v %v", prefer, res, e.ad["rd"].adds)
		}
		found := false
		for _, st := range res.Steps {
			found = found || st.Code == "hash_conflict"
		}
		if !found {
			t.Fatalf("prefer=%v no hash_conflict step: %+v", prefer, res.Steps)
		}
	}
}
