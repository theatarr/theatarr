package router

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

func orderedStep(res *Result) *Step {
	for i := range res.Steps {
		if res.Steps[i].Outcome == outcomeOrdered {
			return &res.Steps[i]
		}
	}
	return nil
}

func providersTried(res *Result) []string {
	var out []string
	for _, s := range res.Steps {
		if s.Provider != "" {
			out = append(out, s.Provider+":"+s.Code)
		}
	}
	return out
}

// Cache evidence moves the providers it names to the front, in the version's order; providers without evidence are
// still tried, as before; known-uncached and miss rules still skip.
func TestRouterOrdersProvidersByCacheEvidence(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)

	// Evidence on the release (a DMM AllDebrid feed in the same search): AllDebrid first, Real-Debrid untouched.
	c := cand(h("a"), "Movie.2023.2160p.web-dl")
	c.Release.CachedOn, c.Release.CachedVia = []string{"ad"}, map[string]string{"ad": "dmm-ad"}
	res, err := e.r.Route(e.ctx, grabReq(c))
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "ad" || len(e.ad["ad"].adds) != 1 || len(e.ad["rd"].adds) != 0 {
		t.Fatalf("evidence on ad: provider %q, steps %s", res.Provider, steps(res))
	}
	st := orderedStep(res)
	if st == nil || st.Code != codeCachedFirst || !strings.Contains(st.Message, "ad (dmm-ad)") ||
		!strings.Contains(st.Message, "order ad, rd, tb") {
		t.Fatalf("ordered step %+v", st)
	}

	// Stored evidence (provider_cache cached) puts TorBox first.
	now := e.clock
	e.exec(`INSERT INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at)
		VALUES (?, 'tb', 'cached', 'feed:dmm-tb', ?, ?)`, h("b"), store.Millis(now), store.Millis(now.Add(time.Hour)))
	res, err = e.r.Route(e.ctx, grabReq(cand(h("b"), "Movie.2023.2160p.web-dl")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "tb" || len(e.ad["rd"].adds) != 0 {
		t.Fatalf("stored evidence on tb: %q, steps %s", res.Provider, steps(res))
	}
	if st := orderedStep(res); st == nil || !strings.Contains(st.Message, "tb (feed:dmm-tb)") {
		t.Fatalf("ordered step %+v", st)
	}

	// Evidence that turns out wrong: TorBox refuses (not cached); the providers without evidence are still tried, in
	// the version's order.
	e.ad["tb"].add = refuse(adder.CodeNotCached)
	c = cand(h("c"), "Movie.2023.2160p.web-dl")
	c.Release.CachedOn = []string{"tb"}
	res, err = e.r.Route(e.ctx, grabReq(c))
	if err != nil {
		t.Fatal(err)
	}
	if got := providersTried(res); !slices.Equal(got, []string{"tb:torrent_not_cached", "rd:added"}) ||
		res.Provider != "rd" {
		t.Fatalf("tried %v", got)
	}

	// A live known-uncached row still skips the provider with cached_only, whatever the evidence says.
	e.ad["tb"].add = nil
	e.exec(`INSERT INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at)
		VALUES (?, 'tb', 'uncached', 'router:add', ?, ?)`, h("d"), store.Millis(now), store.Millis(now.Add(time.Hour)))
	c = cand(h("d"), "Movie.2023.2160p.web-dl")
	c.Release.Extras = map[string]string{"cached_on": "tb"} // a release that never went through ApplyCachedOn
	e.clock = e.clock.Add(2 * time.Minute)                  // rd's adds_per_minute
	res, err = e.r.Route(e.ctx, grabReq(c))
	if err != nil {
		t.Fatal(err)
	}
	if got := providersTried(res); !slices.Equal(got, []string{"tb:known_uncached", "rd:added"}) {
		t.Fatalf("known uncached: tried %v", got)
	}

	// No evidence: the version's order, no ordered step.
	e.clock = e.clock.Add(2 * time.Minute)
	res, err = e.r.Route(e.ctx, grabReq(cand(h("e"), "Movie.2023.2160p.web-dl")))
	if err != nil {
		t.Fatal(err)
	}
	if orderedStep(res) != nil || res.Provider != "rd" {
		t.Fatalf("no evidence: %s", steps(res))
	}
	// Evidence for the first provider only: the order is unchanged, so no step either.
	c = cand(h("f"), "Movie.2023.2160p.web-dl")
	c.Release.CachedOn = []string{"rd"}
	e.clock = e.clock.Add(2 * time.Minute)
	if res, err = e.r.Route(e.ctx, grabReq(c)); err != nil || orderedStep(res) != nil {
		t.Fatalf("unchanged order: %v %s", err, steps(res))
	}
}

// Every provider without evidence is still tried when the ones with evidence miss, and the plan summary leaves the
// ordering step out.
func TestCacheEvidenceNeverSkipsUnknownProviders(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false, policy.ModeActive)
	for _, a := range e.ad {
		a.add = refuse(adder.CodeBlocked)
	}
	c := cand(h("1"), "Movie.2023.2160p.web-dl")
	c.Release.CachedOn = []string{"tb"}
	res, err := e.r.Plan(e.ctx, movieReq(c))
	if err != nil {
		t.Fatal(err)
	}
	// Dry run: every provider would be tried, TorBox first.
	if got := providersTried(res); len(got) != 1 || got[0] != "tb:add" {
		t.Fatalf("dry run %v", got)
	}
	if !strings.Contains(res.Steps[len(res.Steps)-1].Message, "cache evidence says it is cached here") {
		t.Errorf("would-add message %q", res.Steps[len(res.Steps)-1].Message)
	}

	e = newEnv(t, true, policy.ModeActive)
	for _, a := range e.ad {
		a.add = refuse(adder.CodeBlocked)
	}
	res, err = e.r.Route(e.ctx, movieReq(c))
	if err != nil {
		t.Fatal(err)
	}
	if got := providersTried(res); !slices.Equal(got, []string{"tb:torrent_blocked", "rd:torrent_blocked",
		"ad:torrent_blocked"}) {
		t.Fatalf("tried %v", got)
	}
	if s := PlanSummary(string(res.Outcome), "", "", res.Steps); strings.Contains(s, codeCachedFirst) {
		t.Errorf("summary %q", s)
	}
}

// The search-time TorBox check runs on the accepted decisions, best first, and its positives reach every decision of
// the hash (the arr views' cachedOn and the candidates).
func TestCheckTopFillsDecisions(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false, policy.ModeShadow)
	e.pr["tb"].cache = map[string]bool{h("1"): true}
	svc, err := indexer.New(&policy.Policy{}, nil, indexer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetEvidence(indexer.NewEvidence(indexer.EvidenceConfig{Store: e.st, Policy: e.pol,
		Checkers: map[string]provider.Provider{"tb": e.pr["tb"], "rd": e.pr["rd"]}, Now: func() time.Time { return e.clock }}))
	acc, rej, acc2 := &decide.Decision{Accepted: true}, &decide.Decision{}, &decide.Decision{Accepted: true}
	byDecision := map[*decide.Decision]indexer.Release{
		acc:  cand(h("1"), "A").Release,
		rej:  cand(h("1"), "A rejected").Release,
		acc2: cand(h("2"), "B").Release,
	}
	checkTop(e.ctx, svc, e.pol.Versions["movie"], []*decide.Decision{acc, acc2, rej}, byDecision)
	if got := byDecision[acc].CachedOn; !slices.Equal(got, []string{"tb"}) {
		t.Errorf("accepted %v", got)
	}
	if got := byDecision[rej].CachedOn; !slices.Equal(got, []string{"tb"}) {
		t.Errorf("rejected, same hash %v", got)
	}
	if got := byDecision[acc2].CachedOn; got != nil {
		t.Errorf("uncached %v", got)
	}
	if len(e.pr["tb"].checked) != 1 || !slices.Equal(e.pr["tb"].checked[0].Hashes, []string{h("1"), h("2")}) ||
		len(e.pr["rd"].checked) != 0 {
		t.Errorf("checks tb %+v rd %+v", e.pr["tb"].checked, e.pr["rd"].checked)
	}
	// The negative is stored: with cached_only the router skips TorBox for it without asking again.
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND provider = 'tb' AND state = 'uncached'
		AND source = 'check:torbox'`, h("2")); n != 1 {
		t.Errorf("negative rows %d", n)
	}
}
