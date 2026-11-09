package router

import (
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"testing"
	"time"
)

func TestExplained451DoesNotStartAnotherHold(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	p := e.st.Writer()
	held, _, err := note451(e.ctx, p, "rd", h("a"), 1, e.clock, time.Hour, false)
	if err != nil || held {
		t.Fatalf("first refusal: %v %v", held, err)
	}
	held, _, err = note451(e.ctx, p, "rd", h("b"), 2, e.clock.Add(time.Minute), time.Hour, false)
	if err != nil || !held {
		t.Fatalf("second refusal: %v %v", held, err)
	}
	held, _, err = note451(e.ctx, p, "rd", h("c"), 3, e.clock.Add(7*time.Minute), time.Hour, false)
	if err != nil || held {
		t.Fatalf("explained refusals started another hold: %v %v", held, err)
	}
}

func TestRoute451MissLiftedWhenHoldExplainsIt(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie"].Providers = []string{"rd"}
	e.pol.Providers["rd"].Pacing = policy.Pacing{}
	e.ad["rd"].add = refuse(adder.CodeAccountHold)
	a := grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))
	res, err := e.r.Route(e.ctx, a)
	if err != nil || res.Outcome != OutcomeExhausted {
		t.Fatalf("first route: %+v %v", res, err)
	}
	if e.count(`SELECT COUNT(*) FROM provider_cache WHERE provider='rd' AND state='miss'`) != 1 {
		t.Fatal("first hash not missed")
	}
	e.clock = e.clock.Add(time.Minute)
	b := grabReq(cand(h("b"), "Other.2024.2160p.WEB.H265-GRP"))
	b.TitleID = 2
	res, err = e.r.Route(e.ctx, b)
	if err != nil || res.Outcome != OutcomeDeferred {
		t.Fatalf("held route: %+v %v", res, err)
	}
	if e.count(`SELECT COUNT(*) FROM provider_cache WHERE provider='rd' AND state='miss'`) != 0 {
		var source string
		_ = e.st.Reader().QueryRow(`SELECT source FROM provider_cache WHERE provider='rd' AND state='miss' LIMIT 1`).Scan(&source)
		t.Fatalf("a real route's 451 miss survived the explaining hold: %s", source)
	}
}

// A restore shares the same refusal memory: its account hold explains earlier router misses too.
func TestRestoreHoldLiftsRouter451Miss(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie"].Providers = []string{"rd"}
	e.ad["rd"].add = refuse(adder.CodeAccountHold)
	if _, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil {
		t.Fatal(err)
	}
	if err := RecordAddOutcome(e.ctx, e.st, "rd", policy.Pacing{}, e.clock.Add(time.Minute), AddOutcome{Hold: true, Hash: h("b")}); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT COUNT(*) FROM provider_cache WHERE provider='rd' AND state='miss'`) != 0 {
		t.Fatal("restore's hold did not lift the router's explained miss")
	}
}
