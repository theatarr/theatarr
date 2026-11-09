package router

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// Real-Debrid's min_gap default is 20s ("Two adds on one account need 20 seconds between them",
// https://notes.debridmediamanager.com/zurg/providers/realdebrid/); AllDebrid and TorBox have none.
func TestMinGapDefaults(t *testing.T) {
	t.Parallel()
	for kind, want := range map[string]time.Duration{"realdebrid": 20 * time.Second, "alldebrid": 0, "torbox": 0} {
		pv := &policy.Provider{Kind: kind}
		if got := pv.EffectivePacing().Gap(); got != want {
			t.Errorf("%s: min_gap %v, want %v", kind, got, want)
		}
	}
	off := policy.Duration(0)
	pv := &policy.Provider{Kind: "realdebrid", Pacing: policy.Pacing{MinGap: &off}}
	if got := pv.EffectivePacing().Gap(); got != 0 {
		t.Errorf("explicit 0s: %v", got)
	}
}

func TestMinGapBlocks(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	pc := (&policy.Provider{Kind: "realdebrid"}).EffectivePacing()
	var p pace
	if until, _ := p.blocked(now, pc); !until.IsZero() {
		t.Fatalf("first add blocked until %v", until)
	}
	p.added(now, pc)
	until, why := p.blocked(now.Add(10*time.Second), pc)
	if !until.Equal(now.Add(20*time.Second)) || why == "" {
		t.Fatalf("10s after an add: until %v (%s)", until, why)
	}
	if until, _ := p.blocked(now.Add(20*time.Second), pc); !until.IsZero() {
		t.Fatalf("20s after an add still blocked until %v", until)
	}
}

// A season route that places single-episode releases on Real-Debrid alone spaces them min_gap apart: the later ones
// wait for their turn (deferred and resumed), none is failed or dropped.
func TestMinGapSpacesSeasonBatch(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["series"].Providers = []string{"rd"}
	e.pol.Providers["rd"].Pacing = policy.Pacing{} // min_gap unset: Real-Debrid's 20s default
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.clock })
	w := jobs.NewWorker(q, jobs.WorkerOptions{})
	e.r.RegisterJobs(w, q)
	var want []string
	var cs []Candidate
	for i, c := range []string{"a", "b", "c", "d", "e", "f"} {
		cs = append(cs, episodeCandidate(c, int64(101+i)))
		want = append(want, h(c))
	}
	start := e.clock
	var addTimes []time.Time
	e.ad["rd"].observe = func(context.Context) { addTimes = append(addTimes, e.clock) }
	if _, err := e.r.Enqueue(e.ctx, seriesReq(cs...), time.Time{}); err != nil {
		t.Fatal(err)
	}
	// Ten-second ticks: a resumed route may only run once its 20s are up.
	for i := 0; i < 20 && len(e.ad["rd"].adds) < len(want); i++ {
		if _, err := w.RunOne(e.ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		e.clock = e.clock.Add(10 * time.Second)
	}
	// Every release is placed; the resumed routes may take the waiting ones in any order.
	if got := slices.Sorted(slices.Values(e.ad["rd"].adds)); !slices.Equal(got, want) {
		t.Fatalf("adds %v, want %v", e.ad["rd"].adds, want)
	}
	for i := 1; i < len(addTimes); i++ {
		if gap := addTimes[i].Sub(addTimes[i-1]); gap < 20*time.Second {
			t.Fatalf("adds %d and %d only %v apart (from %v): %v", i-1, i, gap, start, addTimes)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'failed'`); n != 0 {
		t.Fatalf("%d downloads failed", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'added'`); n != len(want) {
		t.Fatalf("%d downloads added, want %d", n, len(want))
	}
}

// Real-Debrid's account hold escalates while it repeats (5, 15, 60 min) and starts again after a quiet hour:
// "Once tripped it keeps refusing through minutes of quiet" (https://notes.debridmediamanager.com/zurg/providers/realdebrid/).
func TestAccountHoldEscalates(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	pc := policy.Pacing{}
	var p pace
	step := func(want time.Duration) {
		t.Helper()
		until := p.hold(now, pc, time.Time{})
		if got := until.Sub(now); got != want {
			t.Fatalf("hold at %v: %v, want %v (level %d)", now, got, want, p.HoldLevel)
		}
		if !p.CooldownUntil.Equal(until) {
			t.Fatalf("cooldown %v, hold %v", p.CooldownUntil, until)
		}
	}
	step(5 * time.Minute)
	now = now.Add(6 * time.Minute) // right after the first hold ended
	step(15 * time.Minute)
	now = now.Add(5 * time.Minute) // inside the second hold
	step(time.Hour)
	now = now.Add(61 * time.Minute)
	step(time.Hour) // the top step stays
	now = p.HoldUntil.Add(holdQuiet + time.Minute)
	step(5 * time.Minute) // a quiet hour resets it
	// The provider's cooldown is the floor of every step.
	p = pace{}
	pc.Cooldown = policy.Duration(10 * time.Minute)
	step(10 * time.Minute)
	now = now.Add(11 * time.Minute)
	step(15 * time.Minute)
	// A later Retry-After wins.
	if until := p.hold(now, pc, now.Add(3*time.Hour)); !until.Equal(now.Add(3 * time.Hour)) {
		t.Fatalf("retry-after: %v", until)
	}
}

// Through a route: the second 451 after the first cooldown ended gets the 15-minute step. Every 451 here is on a
// different hash from a fresh unexplained refusal within holdWindow, so each holds the account. Refusals already
// explained by the first hold cannot justify the second.
func TestAccountHoldEscalatesInRoute(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie"].Providers = []string{"rd"}
	e.pol.Providers["rd"].Pacing = policy.Pacing{}
	e.ad["rd"].add = refuse(adder.CodeAccountHold)
	e.exec(`INSERT INTO router_refusal_451 (provider, info_hash, refused_at) VALUES ('rd', ?, ?)`, h("z"),
		store.Millis(e.clock))
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || res.Outcome != OutcomeDeferred || !res.RetryAt.Equal(e.clock.Add(5*time.Minute)) {
		t.Fatalf("first hold: %+v %v", res, err)
	}
	e.clock = e.clock.Add(6 * time.Minute)
	// Another unexplained refusal is needed after the previous hold ends.
	e.exec(`INSERT INTO router_refusal_451 (provider, info_hash, refused_at, title_id) VALUES ('rd', ?, ?, 2)`, h("y"), store.Millis(e.clock))
	res, err = e.r.Route(e.ctx, grabReq(cand(h("b"), "Movie.2024.1080p.WEB.H265-GRP")))
	if err != nil || res.Outcome != OutcomeDeferred || !res.RetryAt.Equal(e.clock.Add(15*time.Minute)) {
		t.Fatalf("second hold: %+v %v", res, err)
	}
	p, err := loadPace(e.ctx, e.st.Reader(), "rd")
	if err != nil || p.HoldLevel != 1 || !p.HoldUntil.Equal(e.clock.Add(15*time.Minute)) {
		t.Fatalf("pacing %+v %v", p, err)
	}
}
