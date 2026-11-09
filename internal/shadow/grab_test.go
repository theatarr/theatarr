package shadow

import (
	"slices"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/router"
)

// An active version with grab off still gets RSS routes, as dry runs: the router records a "would add" plan and
// neither adds nor re-searches anything. The routed item is not marked seen, so the next sync decides it again.
func TestActiveRSSGrabOff(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	rt, _, w := e.withRouter(t, "movie-web")
	e.pol.Versions["movie-web"].Grab = false
	e.build(rt)
	if got := e.r.ActiveVersions(); !slices.Equal(got, []string{"movie-web"}) {
		t.Fatalf("active RSS versions with grab off: %v", got)
	}
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0.Add(-10 * time.Minute)})
	rs, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Routed != 1 {
		t.Fatalf("rss %+v", rs)
	}
	for {
		ran, err := w.RunOne(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			break
		}
	}
	if adds := rt.Adders["tb"].Adds(); len(adds) != 0 || rt.Writes() != 0 {
		t.Fatalf("grab off wrote: adds %v, writes %d", adds, rt.Writes())
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM download`); n != 0 {
		t.Errorf("%d downloads", n)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM router_plan WHERE version_key = 'movie-web' AND mode = 'disabled'`); n != 1 {
		t.Errorf("movie-web dry-run plans %d", n)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM job WHERE kind = ?`, router.JobResearch); n != 0 {
		t.Errorf("%d re-search jobs", n)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM shadow_rss_seen`); n != 0 {
		t.Errorf("%d items marked seen", n)
	}
	if rs, err = e.r.RSS(e.ctx); err != nil || rs.Routed > 1 {
		t.Errorf("second read %+v %v", rs, err)
	}
}
