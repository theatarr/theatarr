package router

import (
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"testing"
	"time"
)

func TestEncodedMagnetConflict(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	c := cand(h("a"), "Movie.2024.2160p.BluRay-GRP")
	c.Release.Magnet = indexer.NewLink("magnet:?xt=urn%3Abtih%3A" + h("b"))
	res, err := e.r.Route(e.ctx, grabReq(c))
	if err != nil {
		t.Fatal(err)
	}
	if len(e.ad["rd"].adds) != 0 {
		t.Fatalf("conflicting encoded magnet was routed: adds=%v result=%+v", e.ad["rd"].adds, res)
	}
}

func TestEnqueueEncodedConflict(t *testing.T) {
	t.Parallel()
	for _, prefer := range []bool{false, true} {
		e := newEnv(t, true, policy.ModeActive)
		e.pol.Versions["movie"].PreferSeaDex = prefer
		q := jobs.NewQueue(e.st)
		e.r.RegisterJobs(jobs.NewWorker(q, jobs.WorkerOptions{}), q)
		c := cand(h("a"), "Movie.2024.2160p.BluRay-GRP")
		c.Release.Magnet = indexer.NewLink("magnet:?xt=urn%3Abtih%3A" + h("b"))
		if _, err := e.r.Enqueue(e.ctx, grabReq(c), time.Time{}); err == nil {
			t.Fatal("conflicting encoded magnet enqueued")
		}
	}
}
