package acquire

import (
	"testing"

	"github.com/theatarr/theatarr/internal/router"
)

// With router.enabled true but every active version's grab switch off, the wanted loop spends no hourly budget (and
// marks nothing searched) on searches that could only produce dry runs.
func TestWantedSkipsGrabOff(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	e.pol.Versions["movie"].Grab = false
	e.pol.Versions["series"].Grab = false
	sum, err := e.a.Wanted(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run`); n != 0 || sum.Off == "" {
		t.Fatalf("wanted started %d acquire runs for active versions with grab off (summary %+v)", n, *sum)
	}
}

// A re-search for a version with grab off is skipped.
func TestResearchSkipsGrabOff(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	e.pol.Versions["movie"].Grab = false
	sum, err := e.a.Research(e.ctx, router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie",
		Reason: router.ReasonStalled})
	if err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run`); n != 0 || sum.Off == "" {
		t.Fatalf("re-search with grab off: %d runs, summary %+v", n, *sum)
	}
}
