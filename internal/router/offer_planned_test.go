package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// plannedOffers imports n downloads of title 1 for movie-remux while grabbing is off, so each is planned for
// movie-web, and returns their hashes.
func plannedOffers(e *offerEnv, n int) []string {
	e.t.Helper()
	e.off.Policy.Router = &policy.Router{Enabled: false}
	var hashes []string
	for i := 1; i <= n; i++ {
		h := fmt.Sprintf("%040x", i)
		e.download(int64(i), 1, 10, "movie-remux", h, fmt.Sprintf("Movie.2001.2160p.WEB-DL.DDP5.1-G%d", i), "imported")
		hashes = append(hashes, h)
	}
	if sr, err := e.off.Sweep(e.ctx, false, 0); err != nil || sr.Outcomes[OfferPlanned] != 2*n {
		e.t.Fatalf("gate shut: %+v %v", sr, err)
	}
	e.off.Policy.Router = &policy.Router{Enabled: true}
	return hashes
}

// Offers planned while the gate was shut are not all routed at the first sweep after the flip. A pass
// routes at most OfferPlannedPerPass of them; the rest wait for the next sweep.
func TestPlannedOffersPacedPerSweep(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	n := OfferPlannedPerPass + 5
	hashes := plannedOffers(e, n)
	queued := len(e.enq.reqs)
	dry, err := e.off.Sweep(e.ctx, true, 0)
	if err != nil || dry.Outcomes[OfferRouted] != OfferPlannedPerPass || len(e.enq.reqs) != queued {
		t.Fatalf("dry run: %+v %v", dry.Outcomes, err)
	}
	sr, err := e.off.Sweep(e.ctx, false, 0)
	if err != nil || sr.Outcomes[OfferRouted] != OfferPlannedPerPass || len(e.enq.reqs) != queued+OfferPlannedPerPass {
		t.Fatalf("first sweep: %+v, %d routes, %v", sr.Outcomes, len(e.enq.reqs)-queued, err)
	}
	planned := 0
	for _, h := range hashes {
		if e.outcome("movie-web", 1, h) == OfferPlanned {
			planned++
		}
	}
	if planned != 5 {
		t.Fatalf("%d still planned", planned)
	}
	if sr, err = e.off.Sweep(e.ctx, false, 0); err != nil || sr.Outcomes[OfferRouted] != 5 {
		t.Fatalf("second sweep: %+v %v", sr.Outcomes, err)
	}
}

// A planned offer older than OfferPlannedExpiry is declined (planned_expired) when the gate opens, not routed.
func TestPlannedOffersExpire(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	hashes := plannedOffers(e, 2)
	e.now = e.now.Add(OfferPlannedExpiry + time.Hour)
	queued := len(e.enq.reqs)
	sr, err := e.off.Sweep(e.ctx, false, 0)
	if err != nil || sr.Outcomes[OfferRouted] != 0 || len(e.enq.reqs) != queued {
		t.Fatalf("sweep: %+v %v", sr.Outcomes, err)
	}
	for _, h := range hashes {
		var reasons string
		_ = e.st.Reader().QueryRow(`SELECT reasons FROM router_offer WHERE version_key = 'movie-web' AND info_hash = ?`,
			h).Scan(&reasons)
		if e.outcome("movie-web", 1, h) != OfferDeclined || !strings.Contains(reasons, "planned_expired") {
			t.Errorf("%s: %s %s", h, e.outcome("movie-web", 1, h), reasons)
		}
	}
	// The shadow target's planned offers stay planned (its gate is still shut).
	if e.outcome("movie-extra", 1, hashes[0]) != OfferPlanned {
		t.Errorf("shadow target %s", e.outcome("movie-extra", 1, hashes[0]))
	}
}
