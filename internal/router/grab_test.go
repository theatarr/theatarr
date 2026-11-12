package router

import (
	"errors"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

// An active version grabs only with its grab switch on. With router.enabled true and grab off, a
// route is a recorded dry-run plan (no provider write), the status says no writes and an
// offer is planned, not routed.
func TestActiveVersionWithoutGrabNeverWrites(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie"].Grab = false
	e.pr["rd"].set("rd-aaaa", provider.Torrent{Status: "downloading", State: provider.StateDownloading})
	if _, err := e.r.Gate("movie"); !errors.Is(err, ErrNoGrab) {
		t.Fatalf("gate: %v", err)
	}
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if e.writes() != 0 || res.Mode != ModeDisabled || res.Outcome != OutcomeWouldAdd || res.PlanID == 0 ||
		!strings.Contains(res.Why, "grab") {
		t.Fatalf("writes %d, result %+v", e.writes(), res)
	}
	if n := e.count(`SELECT COUNT(*) FROM download`); n != 0 {
		t.Errorf("%d download rows without grab", n)
	}
	s, err := ReadStatus(e.ctx, e.st, e.pol, nil, e.clock, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range s.Versions {
		if v.Key == "movie" && (v.Writes || v.Grab) {
			t.Errorf("status %+v", v)
		}
		if v.Key == "series" && (!v.Writes || !v.Grab) {
			t.Errorf("status %+v", v)
		}
	}
	if !e.pol.Grabs("series") || e.pol.Grabs("movie") || strings.Join(e.pol.GrabbingVersions(), ",") != "seed,series" {
		t.Errorf("grabbing %v", e.pol.GrabbingVersions())
	}
	// Grab on: the same route writes.
	e.pol.Versions["movie"].Grab = true
	if _, err := e.r.Gate("movie"); err != nil {
		t.Fatalf("gate with grab: %v", err)
	}
}

// The grab gate for offers: an accepted offer to an active target without grab is planned, and routed once grab is on.
func TestOfferWithoutGrabIsPlanned(t *testing.T) {
	t.Parallel()
	e := newOfferEnv(t)
	e.off.Policy.Versions["movie-web"].Grab = false
	h := strings.Repeat("a", 40)
	e.download(1, 1, 10, "movie-remux", h, "Movie.2001.2160p.WEB-DL.DDP5.1.Atmos-GRP", "imported")
	res, err := e.off.Offer(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if m := byVersion(res); m["movie-web"].Outcome != OfferPlanned {
		t.Fatalf("grab off: %+v", res)
	}
	e.off.Policy.Versions["movie-web"].Grab = true
	res, err = e.off.Offer(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if m := byVersion(res); m["movie-web"].Outcome != OfferRouted {
		t.Fatalf("grab on: %+v", res)
	}
}
