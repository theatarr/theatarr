package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

var rdTarget = Target{Class: ClassProvider, Key: "rd"}

// A health check goes through the back-off that failures in a row set, one at a time, and its success ends it; a
// rate limit (Retry-After) or an out-of-band back-off still refuses it.
func TestHealthCheckGoesThroughFailureBackoff(t *testing.T) {
	e := newEnv(t)
	var retryAfter atomic.Bool
	e.serve("rd.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/1.0/user" && retryAfter.Load():
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(http.StatusServiceUnavailable)
		case r.URL.Path == "/rest/1.0/user", r.URL.Path == "/rest/1.0/torrents/info/good":
			io.WriteString(w, "{}")
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	g := e.gateway(testPolicy(), nil)
	broken := "http://rd.test/rest/1.0/torrents/info/broken"
	for range failuresBeforeBackoff {
		resp, err := g.Get(context.Background(), rdTarget, broken)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if _, err := get(g, rdTarget, "http://rd.test/rest/1.0/user"); !errors.Is(err, ErrBackoff) {
		t.Fatalf("plain request after %d failures: %v", failuresBeforeBackoff, err)
	}
	hc := HealthCheck(context.Background())
	resp, err := g.Get(hc, rdTarget, "http://rd.test/rest/1.0/user")
	if err != nil {
		t.Fatalf("health check refused by the failure back-off: %v", err)
	}
	resp.Body.Close()
	if body, err := get(g, rdTarget, "http://rd.test/rest/1.0/torrents/info/good"); err != nil || body != "{}" {
		t.Fatalf("the health check's success did not end the failure back-off: %q %v", body, err)
	}

	// Failing again: a failed health check extends the back-off like any failure.
	for range failuresBeforeBackoff {
		resp, err := g.Get(context.Background(), rdTarget, broken)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	resp, err = g.Get(hc, rdTarget, broken)
	if err != nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("failing health check: %v %v", resp, err)
	}
	resp.Body.Close()
	if n := g.state.failures(rdTarget.String()); n != failuresBeforeBackoff+1 {
		t.Fatalf("failures %d", n)
	}

	// Retry-After: a rate limit, which a health check never skips.
	retryAfter.Store(true)
	e.advance(time.Hour)
	resp, err = g.Get(hc, rdTarget, "http://rd.test/rest/1.0/user")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, err := get(g, rdTarget, "http://rd.test/rest/1.0/user"); !errors.Is(err, ErrBackoff) {
		t.Fatalf("after Retry-After: %v", err)
	}
	if _, err := g.Get(hc, rdTarget, "http://rd.test/rest/1.0/user"); !errors.Is(err, ErrBackoff) {
		t.Fatalf("health check went through a Retry-After back-off: %v", err)
	}

	// An out-of-band back-off (decypharr's too_many_active_downloads) is not skipped either.
	e.advance(time.Hour)
	g.BackOff(context.Background(), rdTarget, e.clock().Add(time.Minute), "too_many_active_downloads")
	if _, err := g.Get(hc, rdTarget, "http://rd.test/rest/1.0/user"); !errors.Is(err, ErrBackoff) {
		t.Fatalf("health check went through an out-of-band back-off: %v", err)
	}
}

// Only one health check per target goes through its failure back-off at a time.
func TestHealthCheckOneAtATime(t *testing.T) {
	e := newEnv(t)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.serve("rd.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/1.0/user" {
			once.Do(func() { close(started) })
			<-release
			io.WriteString(w, "{}")
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	g := e.gateway(testPolicy(), nil)
	for range failuresBeforeBackoff {
		resp, err := g.Get(context.Background(), rdTarget, "http://rd.test/rest/1.0/torrents/info/x")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	hc := HealthCheck(context.Background())
	done := make(chan error, 1)
	go func() {
		if _, err := get(g, rdTarget, "http://rd.test/rest/1.0/user"); !errors.Is(err, ErrBackoff) {
			done <- errors.New("plain request not refused by the back-off")
			close(release)
			return
		}
		resp, herr := g.Get(hc, rdTarget, "http://rd.test/rest/1.0/user")
		if herr == nil {
			resp.Body.Close()
		}
		done <- herr
	}()
	<-started
	if _, err := g.Get(hc, rdTarget, "http://rd.test/rest/1.0/user"); !errors.Is(err, ErrBackoff) {
		t.Fatalf("second health check in flight: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A request pinned to a retired generation keeps that generation's VPN verdict only for an exit address the current
// settings still trust: after the VPN set is narrowed, the old exit no longer passes there; an exit the
// new set keeps still does.
func TestRetiredGenerationFollowsTrustChange(t *testing.T) {
	for _, keep := range []bool{false, true} {
		e := newEnv(t)
		e.serve("dmm.test", okHandler("results"))
		g1 := e.gateway(testPolicy(), nil)
		g1.Activate()
		g1.CheckEgress(context.Background())
		if _, err := get(g1, dmm, "http://dmm.test/api"); err != nil {
			t.Fatalf("baseline: %v", err)
		}
		pinned := settings.WithRuntime(context.Background(), &settings.Runtime{Generation: 1,
			Components: gatewayRuntimeComponent{gateway: g1}})
		p2 := testPolicy()
		p2.Egress.VPNIPs = []string{"45.0.0.2"}
		if keep {
			p2.Egress.VPNIPs = append(p2.Egress.VPNIPs, vpnIP)
		}
		g2, err := New(context.Background(), p2, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Version: "test", DialContext: e.dial, now: e.clock, Previous: g1})
		if err != nil {
			t.Fatal(err)
		}
		// g2 prepared, not active yet: g1 is still current and keeps its own verdict.
		if _, err := g1.Get(pinned, dmm, "http://dmm.test/api"); err != nil {
			t.Fatalf("before activation: %v", err)
		}
		g2.Activate()
		e.advance(2 * time.Minute) // inside max_age
		resp, err := g1.Get(pinned, dmm, "http://dmm.test/api")
		if err == nil {
			resp.Body.Close()
		}
		switch {
		case keep && err != nil:
			t.Fatalf("exit still trusted, pinned request refused: %v", err)
		case !keep && !errors.Is(err, ErrVPNRequired):
			t.Fatalf("pinned request reached an indexer through exit %s, which the current VPN set no longer trusts: %v",
				vpnIP, err)
		}
	}
}

// A source probe resolves names like the live gateway: with the real dialer and no injected resolver,
// a guarded dial gets checked addresses instead of failing the lookup and dialling the name with a pinned family.
func TestProbeResolvesForGuardedDials(t *testing.T) {
	live, err := New(context.Background(), testPolicy(), Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	pg, err := live.Probe(context.Background(), testPolicy(), ProbeOptions{Target: dmm})
	if err != nil {
		t.Fatal(err)
	}
	ti := pg.targets[dmm.String()]
	ti.path.check.mu.Lock()
	ti.path.check.state = EgressState{Path: ti.path.name, OK: true, IP: vpnIP, CheckedAt: pg.now()}
	ti.path.check.mu.Unlock()
	_, addrs, err := pg.provenDial(context.Background(), proof{state: ti.path.state(), v4: true, v6: true}, ti, "tcp", "localhost:80") // /etc/hosts, no DNS query
	if err != nil || len(addrs) == 0 {
		t.Fatalf("probe guarded dial: addresses %v, %v", addrs, err)
	}
	for _, a := range addrs {
		if !a.IsLoopback() {
			t.Fatalf("unexpected address %s", a)
		}
	}
}

// Guarded dials stagger their attempts over the checked addresses: an address that does not answer
// does not hold back the next one for a whole dial timeout, the slow attempt is cancelled, and nothing but the given
// addresses is dialled.
func TestDialAddrsStaggers(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	var mu sync.Mutex
	var dialed []string
	slowCancelled := make(chan struct{})
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, network+" "+addr)
		mu.Unlock()
		if addr == "[2001:db8::1]:80" {
			<-ctx.Done() // an unreachable address: no answer until cancelled
			close(slowCancelled)
			return nil, ctx.Err()
		}
		return net.Dial("tcp4", ln.Addr().String())
	}
	start := time.Now()
	c, a, err := dialAddrs(context.Background(), dial,
		[]netip.Addr{netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("203.0.113.9")}, "80")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %v", took)
	}
	if a != netip.MustParseAddr("203.0.113.9") {
		t.Fatalf("connected to %s", a)
	}
	select {
	case <-slowCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow attempt was not cancelled")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) != 2 || dialed[0] != "tcp6 [2001:db8::1]:80" || dialed[1] != "tcp4 203.0.113.9:80" {
		t.Fatalf("dialled %q", dialed)
	}

	// All fail: every error comes back.
	_, _, err = dialAddrs(context.Background(), func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("refused")
	}, []netip.Addr{netip.MustParseAddr("203.0.113.1"), netip.MustParseAddr("203.0.113.2")}, "80")
	if err == nil {
		t.Fatal("no error")
	}
}

// A JSON credential value (a Cardigann passkey) is masked escaped for a path as well as for a query: a definition
// can put it in either.
func TestJSONCredentialPathEscapeMasked(t *testing.T) {
	const key = "secret/with spaces"
	r := newRedactor(&policy.Policy{}, []string{`{"passkey":"secret/with spaces"}`})
	for _, s := range []string{key, url.QueryEscape(key), url.PathEscape(key), (&url.URL{Path: key}).EscapedPath()} {
		if got := r.String("http://tracker.test/" + s + "/rss"); strings.Contains(got, "secret") {
			t.Fatalf("credential %q not masked: %s", s, got)
		}
	}
}
