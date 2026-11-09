package router

import (
	"errors"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

func TestRuntimeSwitchOffStopsRouterAdds(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	off, err := e.pol.Clone()
	if err != nil {
		t.Fatal(err)
	}
	off.Router.Enabled = false
	next, err := New(Config{Store: e.st, Policy: off, Adders: e.r.adders, Providers: e.r.provs, Previous: e.r})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetCurrentRuntime(func() *Router { return next })
	if err := e.r.mayWrite("movie"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("old operation may add: %v", err)
	}
	res, err := next.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeDisabled || e.writes() != 0 {
		t.Fatalf("new adds not stopped: %+v, writes %d", res, e.writes())
	}
}
