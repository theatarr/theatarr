package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

// nextGeneration prepares the settings generation after prev, as a settings save does.
func (e *env) nextGeneration(prev *Gateway, p *policy.Policy) *Gateway {
	e.t.Helper()
	g, err := New(context.Background(), p, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test", DialContext: e.dial, now: e.clock, Previous: prev})
	if err != nil {
		e.t.Fatal(err)
	}
	return g
}

func pinnedTo(g *Gateway) context.Context {
	return settings.WithRuntime(context.Background(), &settings.Runtime{Generation: 1,
		Components: gatewayRuntimeComponent{gateway: g}})
}

func getBody(ctx context.Context, g *Gateway, t Target, url string) error {
	resp, err := g.Get(ctx, t, url)
	if err == nil {
		resp.Body.Close()
	}
	return err
}

// A search pinned to a generation without VPN enforcement (guard off or monitor-only) follows the enforcement the
// current settings turned on: its indexer requests are refused until the current generation's check
// proves the route, and then go out through the pinned transport.
func TestRetiredGenerationFollowsEnforcement(t *testing.T) {
	for _, name := range []string{"off", "monitor-only"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.serve("dmm.test", okHandler("results"))
			p1 := testPolicy()
			if name == "off" {
				p1.Egress = nil
			} else {
				p1.Egress.RequireVPNFor = []string{}
			}
			e.setEcho("8.8.8.8") // outside the tunnel: allowed while nothing requires the VPN
			g1 := e.gateway(p1, nil)
			g1.Activate()
			g1.CheckEgress(context.Background())
			pinned := pinnedTo(g1)
			if err := getBody(pinned, g1, dmm, "http://dmm.test/api"); err != nil {
				t.Fatalf("baseline: %v", err)
			}
			g2 := e.nextGeneration(g1, testPolicy())
			g2.Activate()
			if err := getBody(context.Background(), g2, dmm, "http://dmm.test/api"); !errors.Is(err, ErrVPNRequired) {
				t.Fatalf("current generation without a good check: %v", err)
			}
			if err := getBody(pinned, g1, dmm, "http://dmm.test/api"); !errors.Is(err, ErrVPNRequired) {
				t.Fatalf("pinned request reached an indexer without the VPN enforcement now in force: %v", err)
			}
			// Unguarded classes stay unguarded.
			e.serve("tmdb.test", okHandler("{}"))
			if err := getBody(pinned, g1, Target{Class: ClassMetadata, Key: "tmdb"}, "http://tmdb.test/3/x"); err != nil {
				t.Fatalf("metadata request: %v", err)
			}
			e.setEcho(vpnIP)
			g2.CheckEgress(context.Background())
			if err := getBody(pinned, g1, dmm, "http://dmm.test/api"); err != nil {
				t.Fatalf("pinned request after the current check proved the route: %v", err)
			}
			e.setEcho("8.8.8.8")
			g2.CheckEgress(context.Background())
			if err := getBody(pinned, g1, dmm, "http://dmm.test/api"); !errors.Is(err, ErrVPNRequired) {
				t.Fatalf("pinned request after the current check saw a WAN exit: %v", err)
			}
		})
	}
}

// A request already under way without the guard is stopped at its next boundary (a redirect) once the current
// settings require the VPN for its target.
func TestPlainRequestStoppedWhenEnforcementStarts(t *testing.T) {
	e := newEnv(t)
	p1 := testPolicy()
	p1.Egress.RequireVPNFor = []string{}
	g1 := e.gateway(p1, nil)
	g1.Activate()
	g2 := e.nextGeneration(g1, testPolicy())
	e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			g2.Activate() // a settings save while the request is under way
			http.Redirect(w, r, "/next", http.StatusFound)
			return
		}
		io.WriteString(w, "results")
	}))
	g2.CheckEgress(context.Background())
	if err := getBody(pinnedTo(g1), g1, dmm, "http://dmm.test/start"); !errors.Is(err, ErrVPNRequired) {
		t.Fatalf("unguarded request followed a redirect after the VPN became required: %v", err)
	}
}

// A shortened max_age applies to a request pinned to the generation with the longer one.
func TestRetiredGenerationFollowsShorterMaxAge(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("results"))
	p1 := testPolicy()
	p1.Egress.MaxAge = policy.Duration(10 * time.Minute)
	g1 := e.gateway(p1, nil)
	g1.Activate()
	g1.CheckEgress(context.Background())
	pinned := pinnedTo(g1)
	g2 := e.nextGeneration(g1, testPolicy()) // max_age 3m, same trust: the check carries over
	g2.Activate()
	e.advance(4 * time.Minute)
	if err := getBody(context.Background(), g2, dmm, "http://dmm.test/api"); !errors.Is(err, ErrVPNRequired) {
		t.Fatalf("current generation past its max_age: %v", err)
	}
	if err := getBody(pinned, g1, dmm, "http://dmm.test/api"); !errors.Is(err, ErrVPNRequired) {
		t.Fatalf("pinned request used a check older than the current max_age: %v", err)
	}
	g2.CheckEgress(context.Background())
	if err := getBody(pinned, g1, dmm, "http://dmm.test/api"); err != nil {
		t.Fatalf("pinned request after a fresh check: %v", err)
	}
}

// A pinned request through an indexer proxy keeps its own proxy (source credentials and address) and is proven by
// the current generation's check of that same proxy path; a proxy the current settings dropped has no such check, so
// with a pinned check of another check_url nothing proves it.
func TestRetiredProxyPathProof(t *testing.T) {
	e := newEnv(t)
	p1 := testPolicy()
	p1.Egress.RequireVPNFor = []string{}
	p1.Indexers["dmm"].Proxy = "http://user:pw@proxy.test:3128"
	g1 := e.gateway(p1, nil)
	g1.Activate()
	ti := g1.targets[dmm.String()]

	p2 := testPolicy()
	p2.Indexers["dmm"].Proxy = "http://user:pw@proxy.test:3128"
	g2 := e.nextGeneration(g1, p2)
	g2.Activate()
	if err := g1.vpnOK(ti); err == nil {
		t.Fatal("pinned proxied request passed before any check of its proxy path")
	}
	cp := g2.paths[ti.path.name]
	cp.check.mu.Lock()
	cp.check.state = EgressState{Path: cp.name, OK: true, IP: vpnIP, CheckedAt: e.clock()}
	cp.check.mu.Unlock()
	if err := g1.vpnOK(ti); err != nil {
		t.Fatalf("pinned proxied request with a good current check of the same proxy: %v", err)
	}

	p3 := testPolicy() // the proxy is gone, and the check_url changed
	p3.Egress.CheckURL = "http://echo2.test/"
	g3 := e.nextGeneration(g2, p3)
	g3.Activate()
	if err := g1.vpnOK(ti); err == nil {
		t.Fatal("pinned proxied request passed with no check of its route under the current settings")
	}
}

// A pinned request of an unguarded target is not redirected into a host the current settings protect, even when its
// own generation protected none.
func TestRetiredRedirectIntoNewlyProtectedHost(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("results"))
	e.serve("tmdb.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://dmm.test/api", http.StatusFound)
	}))
	p1 := testPolicy()
	p1.Egress = nil
	g1 := e.gateway(p1, nil)
	g1.Activate()
	tmdb := Target{Class: ClassMetadata, Key: "tmdb"}
	if err := getBody(pinnedTo(g1), g1, tmdb, "http://tmdb.test/3/x"); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	g2 := e.nextGeneration(g1, testPolicy())
	g2.Activate()
	g2.CheckEgress(context.Background())
	if err := getBody(pinnedTo(g1), g1, tmdb, "http://tmdb.test/3/x"); !errors.Is(err, ErrVPNRequired) {
		t.Fatalf("pinned unguarded request redirected into a host the current settings protect: %v", err)
	}
}
