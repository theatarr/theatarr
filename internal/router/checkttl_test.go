package router

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider/adder"
)

// A cache check's uncached answer (router:check, a read) lives 30 minutes, at most miss_ttl; an uncached add's
// (router:add) miss_ttl.
func TestCheckUncachedLivesThirtyMinutes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.r.set.CachedOnly = true
	e.pr["tb"].cache = map[string]bool{}
	e.ad["rd"].add = refuse(adder.CodeNotCached)
	t0 := e.clock
	// TorBox checks first and says uncached; AllDebrid takes it.
	e.pol.Versions["movie"].Providers = []string{"tb", "ad"}
	if res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil || res.Provider != "ad" {
		t.Fatalf("%v %s", err, steps(res))
	}
	// Real-Debrid's add is refused uncached; AllDebrid takes it.
	e.pol.Versions["movie"].Providers = []string{"rd", "ad"}
	if res, err := e.r.Route(e.ctx, grabReq(cand(h("b"), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil || res.Provider != "ad" {
		t.Fatalf("%v %s", err, steps(res))
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND provider = 'tb' AND state = 'uncached'
		AND source = 'router:check' AND expires_at = ?`, h("a"), t0.Add(30*time.Minute).UnixMilli()); n != 1 {
		t.Error("tb check not recorded uncached for 30 minutes")
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND provider = 'rd' AND state = 'uncached'
		AND source = 'router:add' AND expires_at = ?`, h("b"), t0.Add(e.r.set.MissTTL).UnixMilli()); n != 1 {
		t.Error("rd add not recorded uncached for miss_ttl")
	}
	for _, q := range []struct {
		hash, pk string
		after    time.Duration
		want     string
	}{{h("a"), "tb", 29 * time.Minute, "uncached"}, {h("a"), "tb", 31 * time.Minute, ""},
		{h("b"), "rd", 31 * time.Minute, "uncached"}} {
		if st, _ := e.r.cacheState(e.ctx, q.hash, q.pk, t0.Add(q.after)); st != q.want {
			t.Errorf("%s after %v: %q, want %q", q.pk, q.after, st, q.want)
		}
	}
	// A miss_ttl under 30 minutes caps the check's life too.
	e.r.set.MissTTL = 10 * time.Minute
	e.pol.Versions["movie"].Providers = []string{"tb", "ad"}
	e.clock = t0.Add(time.Hour)
	if res, err := e.r.Route(e.ctx, grabReq(cand(h("c"), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil || res.Provider != "ad" {
		t.Fatalf("%v %s", err, steps(res))
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND provider = 'tb' AND source = 'router:check'
		AND expires_at = ?`, h("c"), e.clock.Add(10*time.Minute).UnixMilli()); n != 1 {
		t.Error("tb check not capped at miss_ttl")
	}
}

// An all-provider miss leaves a live check verdict and its 30 minutes as they are.
func TestMissKeepsCheckVerdict(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.r.set.CachedOnly = true
	e.pr["tb"].cache = map[string]bool{}
	e.pol.Versions["movie"].Providers = []string{"tb"}
	t0 := e.clock
	if res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil ||
		res.Outcome != OutcomeExhausted {
		t.Fatalf("%v %s", err, steps(res))
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND provider = 'tb' AND state = 'uncached'
		AND source = 'router:check' AND expires_at = ?`, h("a"), t0.Add(30*time.Minute).UnixMilli()); n != 1 {
		t.Error("the check's verdict was replaced by the miss")
	}
}
