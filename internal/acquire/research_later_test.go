package acquire

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
)

// A re-search held back by router.wanted.per_hour is asked for again when the hour frees, not
// dropped (the wanted loop may never search the item, with missing or cutoff searches off).
func TestResearchPastPerHourRunsLater(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 1, AgainAfter: policy.Duration(6 * time.Hour)})
	e.research(router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonExhausted})
	e.research(router.ResearchRequest{TitleID: 2, VersionID: 102, Version: "movie", Reason: router.ReasonLibrary})
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE version_id = 102`); n != 0 {
		t.Fatal("searched past per_hour")
	}
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE version_id = 102`); n != 0 {
		t.Fatal("the deferred re-search ran inside the hour")
	}
	e.advance(61 * time.Minute)
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE version_id = 102 AND research IS NOT NULL`); n != 1 {
		t.Errorf("%d re-search runs for the deferred title after the hour, want 1", n)
	}
}
