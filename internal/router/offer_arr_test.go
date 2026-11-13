package router

import (
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// An adopted link's stored source names an indexer by the arr's own name ("DMM RD (Prowlarr)"), never a
// policy key. The name is matched to a configured indexer by name; an unmatched one is declined (unknown_indexer)
// and decided again once an indexer of that name is configured. Its privacy label does not matter.
func TestOfferArrIndexerMatchedByName(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	hash := strings.Repeat("d", 40)
	e.sourceFile("adopted", hash)
	e.storeSource(hash, "DMM RD (Prowlarr)")
	sr, err := e.off.Sweep(e.ctx, false, 0)
	if err != nil || sr.Outcomes[OfferDeclined] != 1 || len(e.enq.reqs) != 0 {
		t.Fatalf("unmatched: %+v, routes %d, %v", sr, len(e.enq.reqs), err)
	}
	if e.outcome("movie-extra", 1, hash) != OfferDeclined {
		t.Fatal("decline not remembered")
	}
	e.off.Policy.Indexers["dmm-rd"] = &policy.Indexer{Kind: "dmm", Privacy: policy.PrivacyPrivate}
	sr, err = e.off.Sweep(e.ctx, false, 0)
	if err != nil || sr.Outcomes[OfferPlanned] != 1 || len(e.enq.reqs) != 1 {
		t.Fatalf("matched: %+v, routes %+v, %v", sr, e.enq.reqs, err)
	}
	for _, r := range e.enq.reqs {
		if c := r.Candidates[0].Release; c.Indexer != "dmm-rd" || c.Privacy != policy.PrivacyPrivate {
			t.Errorf("routed release %+v", c)
		}
	}
	if e.outcome("movie-extra", 1, hash) != OfferPlanned {
		t.Errorf("outcome %q", e.outcome("movie-extra", 1, hash))
	}
}

// An offer accepted for an active target while the router is off (or for a shadow target) is not used up: it
// is recorded planned, offered again (and routed) once the gate is open, and left alone while it is still shut.
func TestOfferWhileGateShutIsRetried(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	e.off.Policy.Router = &policy.Router{Enabled: false}
	h := strings.Repeat("a", 40)
	e.download(1, 1, 10, "movie-remux", h, "Movie.2001.2160p.WEB-DL.DDP5.1.Atmos-GRP", "imported")
	res, err := e.off.Offer(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if m := byVersion(res); m["movie-web"].Outcome != OfferPlanned || m["movie-extra"].Outcome != OfferPlanned {
		t.Fatalf("router off: %+v", res)
	}
	if e.outcome("movie-web", 1, h) != OfferPlanned {
		t.Fatalf("recorded %q", e.outcome("movie-web", 1, h))
	}
	// Still shut: nothing decided or queued again.
	asked, queued := len(e.asked), len(e.enq.reqs)
	if res, err := e.off.Offer(e.ctx, 1, false); err != nil || len(res) != 0 || len(e.asked) != asked ||
		len(e.enq.reqs) != queued {
		t.Fatalf("gate shut: %+v %v", res, err)
	}
	// Grabbing on: the active target gets it again and routes it; the shadow target still waits.
	e.off.Policy.Router = &policy.Router{Enabled: true}
	res, err = e.off.Offer(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if m := byVersion(res); len(res) != 1 || m["movie-web"].Outcome != OfferRouted {
		t.Fatalf("router on: %+v", res)
	}
	if e.outcome("movie-web", 1, h) != OfferRouted || e.outcome("movie-extra", 1, h) != OfferPlanned ||
		len(e.enq.reqs) != queued+1 {
		t.Errorf("outcomes %q %q, queued %d", e.outcome("movie-web", 1, h), e.outcome("movie-extra", 1, h), len(e.enq.reqs))
	}
}
