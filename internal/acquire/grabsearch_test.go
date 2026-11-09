package acquire

import (
	"testing"

	"github.com/theatarr/theatarr/internal/importarr"
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

// An add-time search for an active version with grab off waits, uncounted, until grab is on; then it runs once.
func TestAddTimeSearchWaitsForGrab(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, addTimeOnly(5))
	e.pol.Versions["movie"].Grab = false
	if err := e.a.EnqueueFollowSearch(e.ctx, []importarr.FollowSearchTarget{{TitleID: 1, VersionKey: "movie",
		All: true}}); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if _, err := e.a.Wanted(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.drain()
	}
	if got := e.searches(); len(got) != 0 {
		t.Fatalf("searched with grab off: %+v", got)
	}
	if n := e.count(`SELECT COUNT(*) FROM follow_search_intent WHERE attempts = 0`); n != 1 {
		t.Fatalf("%d pending intents with no attempt, want 1", n)
	}
	e.pol.Versions["movie"].Grab = true
	if _, err := e.a.Wanted(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if got := e.searches(); len(got) != 1 || got[0].Title != "One" {
		t.Fatalf("searches once grab is on %+v", got)
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
