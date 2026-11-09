package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// probeEnv is a live gateway over the fake network with a fake resolver, its egress check passed.
func probeEnv(t *testing.T, edit func(*policy.Policy)) (*env, *Gateway, map[string][]netip.Addr) {
	t.Helper()
	e := newEnv(t)
	// Start the fake clock at real time, so a context deadline and the gateway's budget arithmetic agree.
	e.now = time.Now()
	p := testPolicy()
	if edit != nil {
		edit(p)
	}
	g := e.gateway(p, nil)
	dns := map[string][]netip.Addr{}
	g.lookupIP = func(_ context.Context, host string) ([]netip.Addr, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if ips, ok := dns[host]; ok {
			return ips, nil
		}
		return nil, errors.New("no such host")
	}
	g.CheckEgress(context.Background())
	return e, g, dns
}

// candidate is the live test policy plus an indexer "cand" at url.
func candidate(url string, edit func(*policy.Indexer)) *policy.Policy {
	p := testPolicy()
	ix := &policy.Indexer{Kind: "torznab", URL: url, Privacy: policy.PrivacyPublic}
	if edit != nil {
		edit(ix)
	}
	p.Indexers["cand"] = ix
	return p
}

var cand = Target{Class: ClassIndexer, Key: "cand"}

func refusedReason(err error) Reason {
	var r *Refused
	if errors.As(err, &r) {
		return r.Reason
	}
	return ""
}

// TestProbeDestinations: a probe reaches the live allowlist and its own public host; private, loopback and
// link-local addresses only when already live, checked on the resolved addresses at dial time, redirects included.
func TestProbeDestinations(t *testing.T) {
	e, g, dns := probeEnv(t, func(p *policy.Policy) {
		p.Indexers["lan"] = &policy.Indexer{Kind: "torznab", URL: "http://10.1.2.3:9696/1/api", Privacy: policy.PrivacyPublic}
	})
	ctx := context.Background()
	pub := netip.MustParseAddr("203.0.113.7")
	e.mu.Lock()
	dns["new.test"] = []netip.Addr{pub}
	dns["rebind.test"] = []netip.Addr{netip.MustParseAddr("10.1.2.3")}
	dns["mixed.test"] = []netip.Addr{netip.MustParseAddr("203.0.113.8"), netip.MustParseAddr("127.0.0.1")}
	e.mu.Unlock()
	e.serve("203.0.113.7", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-rebind":
			http.Redirect(w, r, "http://rebind.test/x", http.StatusFound)
		case "/rebinding":
			// The second lookup of new.test answers a private address; close so the hop dials again.
			e.mu.Lock()
			dns["new.test"] = []netip.Addr{netip.MustParseAddr("192.168.1.10")}
			e.mu.Unlock()
			w.Header().Set("Connection", "close")
			http.Redirect(w, r, "http://new.test/after", http.StatusFound)
		default:
			w.Write([]byte("public"))
		}
	}))
	lan := e.serve("10.1.2.3", okHandler("lan"))
	e.mu.Lock()
	e.hosts["10.1.2.3:9696"] = lan.Listener.Addr().String()
	e.mu.Unlock()
	probe := func(url string, edit func(*policy.Indexer)) (*Gateway, error) {
		e.advance(time.Minute) // the shared probe budget refills
		g.CheckEgress(ctx)
		return g.Probe(ctx, candidate(url, edit), ProbeOptions{Target: cand, Candidate: true})
	}
	// A new public host: allowed, dialed at the address it resolved to.
	pg, err := probe("http://new.test/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	if body, err := get(pg, cand, "http://new.test/api"); err != nil || body != "public" {
		t.Fatalf("public candidate: %q %v", body, err)
	}
	// Its host does not join the live gateway's allowlist.
	if _, err := get(g, dmm, "http://new.test/api"); refusedReason(err) != ReasonHost {
		t.Fatalf("live gateway reached the candidate host: %v", err)
	}
	// A name that resolves to a private address, or to a mix: refused at dial time, nothing dialed.
	for _, host := range []string{"rebind.test", "mixed.test"} {
		pg, err := probe("http://"+host+"/api", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := get(pg, cand, "http://"+host+"/api"); refusedReason(err) != ReasonHost {
			t.Errorf("%s: %v", host, err)
		}
	}
	if e.dialCount("10.1.2.3:80")+e.dialCount("127.0.0.1:80")+e.dialCount("203.0.113.8:80") != 0 {
		t.Error("a private resolution was dialed")
	}
	// Private, loopback and link-local literals not in the live allowlist are refused before any request.
	for _, url := range []string{"http://10.1.2.3/api", "http://127.0.0.1:9876/api", "http://169.254.169.254/latest", "http://[::1]:8080/api",
		"http://100.100.1.1/api"} {
		if _, err := probe(url, nil); refusedReason(err) != ReasonHost {
			t.Errorf("%s: %v", url, err)
		}
	}
	// A private host:port the live allowlist holds (a LAN Prowlarr) can be tested on another path.
	pg, err = probe("http://10.1.2.3:9696/7/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	if body, err := get(pg, cand, "http://10.1.2.3:9696/7/api"); err != nil || body != "lan" {
		t.Fatalf("live LAN host: %q %v", body, err)
	}
	// Redirects: to a host outside the probe's allowlist, and a hop whose fresh lookup turned private.
	pg, _ = probe("http://new.test/api", nil)
	if _, err := get(pg, cand, "http://new.test/to-rebind"); refusedReason(err) != ReasonHost {
		t.Errorf("redirect off the allowlist: %v", err)
	}
	pg, _ = probe("http://new.test/api", nil)
	if _, err := get(pg, cand, "http://new.test/rebinding"); refusedReason(err) != ReasonHost {
		t.Errorf("redirect to a rebound host: %v", err)
	}
	if e.dialCount("192.168.1.10:80") != 0 {
		t.Error("the rebound private address was dialed")
	}
	// A candidate proxy must be a live egress path.
	if _, err := probe("http://new.test/api", func(ix *policy.Indexer) { ix.Proxy = "http://evil.test:3128" }); refusedReason(err) != ReasonHost {
		t.Errorf("candidate proxy: %v", err)
	}
}

// TestProbeChargingAndHealth: a probe is charged to the saved budget, or to the shared probe budget, never to none;
// candidate outcomes never touch live health, while live back-offs refuse candidates.
func TestProbeChargingAndHealth(t *testing.T) {
	e, g, _ := probeEnv(t, func(p *policy.Policy) {
		p.Budgets["dmm-key"] = &policy.Budget{Requests: 1, Per: policy.Duration(time.Minute), Burst: 1}
		p.Indexers["dmm"].Budget = "dmm-key"
	})
	var code, hits atomic.Int32
	code.Store(http.StatusOK)
	e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(int(code.Load()))
	}))
	short := func() context.Context {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		t.Cleanup(cancel)
		return ctx
	}
	ctx := context.Background()
	unbudgeted := func() *policy.Policy {
		p := testPolicy()
		p.Budgets["dmm-key"] = &policy.Budget{Requests: 1, Per: policy.Duration(time.Minute), Burst: 1}
		return p // dmm has no budget here
	}
	// The saved source is charged to dmm-key even when the tested configuration drops the budget.
	pg, err := g.Probe(ctx, unbudgeted(), ProbeOptions{Target: dmm, Budget: "dmm-key", Candidate: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := get(pg, dmm, "http://dmm.test/"); err != nil {
		t.Fatal(err)
	}
	pg, _ = g.Probe(ctx, unbudgeted(), ProbeOptions{Target: dmm, Budget: "dmm-key", Candidate: true})
	if _, err := pg.Get(short(), dmm, "http://dmm.test/"); refusedReason(err) != ReasonBudget {
		t.Fatalf("second candidate on the spent saved budget: %v", err)
	}
	if lvl := g.buckets["dmm-key"].level(e.clock()); lvl >= 1 {
		t.Fatalf("the live budget was not charged: %v", lvl)
	}
	// Brand-new candidates share the bounded probe budget.
	sent := 0
	for range probeBurst + 2 {
		pg, err := g.Probe(ctx, candidate("http://dmm.test/new", nil), ProbeOptions{Target: cand, Candidate: true})
		if err != nil {
			t.Fatal(err)
		}
		if resp, err := pg.Get(short(), cand, "http://dmm.test/new"); err == nil {
			resp.Body.Close()
			sent++
		} else if refusedReason(err) != ReasonBudget {
			t.Fatal(err)
		}
	}
	if sent != probeBurst {
		t.Fatalf("%d unbudgeted candidate requests sent, want the probe burst %d", sent, probeBurst)
	}
	// Candidate failures and rate limits stay out of the live health. (Each probe waits a minute for dmm-key.)
	candidateGet := func() error {
		g.CheckEgress(ctx)
		pg, err := g.Probe(ctx, unbudgeted(), ProbeOptions{Target: dmm, Budget: "dmm-key", Candidate: true})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := pg.Get(ctx, dmm, "http://dmm.test/")
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	e.advance(time.Hour)
	code.Store(http.StatusInternalServerError)
	before := hits.Load()
	for range failuresBeforeBackoff {
		e.advance(time.Minute)
		candidateGet()
	}
	if hits.Load() != before+failuresBeforeBackoff {
		t.Fatalf("%d failing candidates sent, want %d", hits.Load()-before, failuresBeforeBackoff)
	}
	e.advance(2 * time.Minute)
	code.Store(http.StatusTooManyRequests)
	before = hits.Load()
	candidateGet()
	if hits.Load() != before+1 {
		t.Fatal("the rate-limited candidate was not sent")
	}
	for _, b := range g.State().Backoffs {
		t.Errorf("live health changed by candidates: %+v", b)
	}
	// The candidate's own record refuses further candidates for a while.
	if err := candidateGet(); refusedReason(err) != ReasonBackoff {
		t.Errorf("candidate after its own failures: %v", err)
	}
	// A saved-source probe updates live health, and a live back-off refuses candidates.
	e.advance(time.Hour)
	code.Store(http.StatusInternalServerError)
	g.CheckEgress(ctx)
	pg, _ = g.Probe(ctx, g.pol, ProbeOptions{Target: dmm, Budget: "dmm-key"})
	if resp, err := pg.Get(ctx, dmm, "http://dmm.test/"); err == nil {
		resp.Body.Close()
	}
	if g.state.failures("indexer:dmm") != 1 {
		t.Errorf("saved probe failure not counted live: %d", g.state.failures("indexer:dmm"))
	}
	g.BackOff(ctx, dmm, e.clock().Add(time.Hour), "test")
	e.advance(time.Minute)
	if err := candidateGet(); refusedReason(err) != ReasonBackoff {
		t.Errorf("candidate during a live back-off: %v", err)
	}
	// Settled candidate records expire.
	e.advance(3 * time.Hour)
	g.Probe(ctx, unbudgeted(), ProbeOptions{Target: dmm, Budget: "dmm-key", Candidate: true})
	if n := len(g.runtime.probeHealth.backoffs); n != 0 {
		t.Errorf("%d expired candidate health records kept", n)
	}
}

// TestPublicAddr classifies the ranges a probe may not reach without the live allowlist.
func TestPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{"203.0.113.7": true, "8.8.8.8": true, "2606:4700::1": true,
		"10.1.2.3": false, "172.16.0.1": false, "192.168.0.1": false, "127.0.0.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "0.0.0.0": false, "::1": false, "fe80::1": false, "fd00::1": false, "::ffff:10.1.2.3": false,
		"64:ff9b::a00:1": false, "224.0.0.1": false, "255.255.255.255": false} {
		if got := publicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: %t, want %t", addr, got, want)
		}
	}
}
