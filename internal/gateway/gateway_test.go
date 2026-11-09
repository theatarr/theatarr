package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

const vpnIP = "45.0.0.1"

// env is a fake network: host names under .test map to local httptest servers through the gateway's DialContext.
// Nothing leaves the machine.
type env struct {
	t     *testing.T
	mu    sync.Mutex
	hosts map[string]string // "dmm.test:80" -> listener address
	dials map[string]int
	echo  string // what echo.test answers
	now   time.Time
	logs  syncBuffer
	reg   *prometheus.Registry
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newEnv(t *testing.T) *env {
	e := &env{t: t, hosts: map[string]string{}, dials: map[string]int{}, echo: vpnIP,
		now: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	e.serve("echo.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		io.WriteString(w, e.echo+"\n")
	}))
	return e
}

func (e *env) setEcho(ip string) {
	e.mu.Lock()
	e.echo = ip
	e.mu.Unlock()
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

func (e *env) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *env) dialCount(host string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dials[host]
}

// serve starts a server for name (port 80).
func (e *env) serve(name string, h http.Handler) *httptest.Server {
	srv := httptest.NewServer(h)
	e.t.Cleanup(srv.Close)
	e.mu.Lock()
	e.hosts[name+":80"] = srv.Listener.Addr().String()
	e.mu.Unlock()
	return srv
}

func (e *env) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	e.mu.Lock()
	e.dials[addr]++
	real, ok := e.hosts[addr]
	e.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("dial %s: no such host in the test network", addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, real)
}

func testPolicy() *policy.Policy {
	return &policy.Policy{
		Indexers: map[string]*policy.Indexer{
			"dmm":     {Kind: "dmm", URL: "http://dmm.test", Privacy: policy.PrivacyPublic},
			"tracker": {Kind: "torznab", URL: "http://tracker.test/api", Privacy: policy.PrivacyPrivate},
			"semi":    {Kind: "torznab", URL: "http://semi.test/api", Privacy: policy.PrivacySemiPrivate},
		},
		Providers: map[string]*policy.Provider{
			"rd": {Kind: "realdebrid", Order: 1, URL: "http://rd.test/rest/1.0", APIKeyEnv: "THEATARR_TEST_RD_KEY"},
		},
		Clients: map[string]*policy.Client{
			"decypharr": {Kind: "decypharr", URL: "http://decypharr.test"},
			"seedbox":   {Kind: "qbittorrent", URL: "http://seedbox.test"},
		},
		Metadata: &policy.Metadata{Sources: map[string]*policy.MetadataSource{
			"tmdb": {URL: "http://tmdb.test/3"},
		}},
		Budgets: map[string]*policy.Budget{},
		Egress: &policy.Egress{CheckURL: "http://echo.test/", VPNIPs: []string{vpnIP},
			Interval: policy.Duration(time.Minute), MaxAge: policy.Duration(3 * time.Minute),
			Timeout: policy.Duration(5 * time.Second), RequireVPNFor: []string{policy.EgressIndexers}},
	}
}

func (e *env) gateway(p *policy.Policy, st *store.Store) *Gateway {
	e.t.Helper()
	e.reg = prometheus.NewRegistry()
	log := slog.New(slog.NewJSONHandler(&e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	g, err := New(context.Background(), p, Options{Store: st, Registerer: e.reg, Logger: log, Version: "test",
		DialContext: e.dial, MaxWait: time.Minute, Timeout: 5 * time.Second, now: e.clock})
	if err != nil {
		e.t.Fatal(err)
	}
	g.sleep = func(ctx context.Context, d time.Duration) error { e.advance(d); return ctx.Err() }
	return g
}

func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, path), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

func okHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
}

func get(g *Gateway, t Target, url string) (string, error) {
	resp, err := g.Get(context.Background(), t, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

var dmm = Target{Class: ClassIndexer, Key: "dmm"}

func TestDoSendsWithUserAgent(t *testing.T) {
	e := newEnv(t)
	var ua string
	e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		io.WriteString(w, "hello")
	}))
	g := e.gateway(testPolicy(), nil)
	g.CheckEgress(context.Background())
	body, err := get(g, dmm, "http://dmm.test/api/search?q=x")
	if err != nil || body != "hello" {
		t.Fatalf("got %q, %v", body, err)
	}
	if ua != "theatarr/test" {
		t.Errorf("User-Agent %q", ua)
	}
	if n := testutil.ToFloat64(g.metrics.requests.WithLabelValues("dmm.test", "indexer:dmm", "2xx")); n != 1 {
		t.Errorf("requests metric %v", n)
	}
}

func TestVPNGuard(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("ok"))
	e.serve("rd.test", okHandler("ok"))
	g := e.gateway(testPolicy(), nil)
	ctx := context.Background()

	// No check yet: refused, and nothing dialed.
	if _, err := get(g, dmm, "http://dmm.test/"); !errors.Is(err, ErrVPNRequired) {
		t.Fatalf("before any check: %v", err)
	}
	if e.dialCount("dmm.test:80") != 0 {
		t.Error("dialed before the egress check")
	}
	// Providers are not in require_vpn_for here.
	if _, err := get(g, Target{Class: ClassProvider, Key: "rd"}, "http://rd.test/rest/1.0/user"); err != nil {
		t.Fatalf("provider: %v", err)
	}

	// The WAN address: refused.
	e.setEcho("203.0.113.9")
	g.CheckEgress(ctx)
	_, err := get(g, dmm, "http://dmm.test/")
	if !errors.Is(err, ErrVPNRequired) || !strings.Contains(err.Error(), "203.0.113.9 is not in the VPN set") {
		t.Fatalf("WAN exit: %v", err)
	}

	// The VPN address: allowed.
	e.setEcho(vpnIP)
	g.CheckEgress(ctx)
	if _, err := get(g, dmm, "http://dmm.test/"); err != nil {
		t.Fatalf("VPN exit: %v", err)
	}

	// Stale: refused.
	e.advance(4 * time.Minute)
	if _, err := get(g, dmm, "http://dmm.test/"); !errors.Is(err, ErrVPNRequired) || !strings.Contains(err.Error(), "old") {
		t.Fatalf("stale check: %v", err)
	}

	// The echo endpoint answering nonsense: refused.
	e.setEcho("<html>")
	g.CheckEgress(ctx)
	if _, err := get(g, dmm, "http://dmm.test/"); !errors.Is(err, ErrVPNRequired) {
		t.Fatalf("failed check: %v", err)
	}
	if !strings.Contains(e.logs.String(), `"msg":"egress changed"`) {
		t.Error("no log line on egress change")
	}
}

func TestVPNGuardForProviders(t *testing.T) {
	e := newEnv(t)
	e.serve("rd.test", okHandler("ok"))
	p := testPolicy()
	p.Egress.RequireVPNFor = []string{policy.EgressIndexers, policy.EgressProviders}
	g := e.gateway(p, nil)
	rd := Target{Class: ClassProvider, Key: "rd"}
	if _, err := get(g, rd, "http://rd.test/rest/1.0/user"); !errors.Is(err, ErrVPNRequired) {
		t.Fatalf("provider before check: %v", err)
	}
	g.CheckEgress(context.Background())
	if _, err := get(g, rd, "http://rd.test/rest/1.0/user"); err != nil {
		t.Fatal(err)
	}
	// Metadata stays unguarded.
	e.serve("tmdb.test", okHandler("{}"))
	e.setEcho("203.0.113.9")
	g.CheckEgress(context.Background())
	if _, err := get(g, Target{Class: ClassMetadata, Key: "tmdb"}, "http://tmdb.test/3/movie/1"); err != nil {
		t.Fatal(err)
	}
}

func TestNoEgressSectionGuardOff(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("ok"))
	e.serve("rd.test", okHandler("ok"))
	p := testPolicy()
	p.Egress = nil
	p.Budgets["capped"] = &policy.Budget{Requests: 100, Per: policy.Duration(time.Second), Burst: 10, Daily: 1}
	p.Indexers["dmm"].Budget = "capped"
	g := e.gateway(p, openStore(t, filepath.Join(t.TempDir(), "theatarr.db")))
	g.RunEgress(context.Background()) // returns at once
	if _, err := get(g, dmm, "http://dmm.test/"); err != nil {
		t.Fatalf("indexer: %v", err)
	}
	if _, err := get(g, Target{Class: ClassProvider, Key: "rd"}, "http://rd.test/rest/1.0/user"); err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := get(g, dmm, "http://evil.test/"); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("allowlist: %v", err)
	}
	if _, err := get(g, dmm, "http://dmm.test/"); !errors.Is(err, ErrBudget) {
		t.Fatalf("budget: %v", err)
	}
	if states := g.CheckEgress(context.Background()); len(states) != 0 || e.dialCount("echo.test:80") != 0 {
		t.Fatalf("checks while off: %v", states)
	}
	if s := g.EgressSummary(); s["mode"] != "off" || s["type"] != "warning" || s["message"] != "VPN guard off" {
		t.Fatalf("summary: %v", s)
	}
	if strings.Count(e.logs.String(), "VPN guard off") != 1 {
		t.Fatalf("startup warning: %s", e.logs.String())
	}
}

func TestEgressMonitorOnly(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("ok"))
	e.serve("rd.test", okHandler("ok"))
	p := testPolicy()
	p.Egress.RequireVPNFor = []string{}
	g := e.gateway(p, nil)
	checkRequests := func() {
		t.Helper()
		if _, err := get(g, dmm, "http://dmm.test/"); err != nil {
			t.Fatalf("indexer: %v", err)
		}
		if _, err := get(g, Target{Class: ClassProvider, Key: "rd"}, "http://rd.test/rest/1.0/user"); err != nil {
			t.Fatalf("provider: %v", err)
		}
	}
	checkRequests()
	for _, echo := range []string{vpnIP, "203.0.113.9", "<html>"} {
		e.setEcho(echo)
		states := g.CheckEgress(context.Background())
		if len(states) != 1 || states[0].CheckedAt.IsZero() || states[0].OK != (echo == vpnIP) {
			t.Fatalf("check %q: %v", echo, states)
		}
		e.advance(4 * time.Minute)
		checkRequests()
	}
	if s := g.EgressSummary(); s["mode"] != "monitor-only" || s["vpn_ok"] != false {
		t.Fatalf("summary: %v", s)
	}
}

func TestEgressProvidersOnly(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("ok"))
	p := testPolicy()
	p.Egress.RequireVPNFor = []string{policy.EgressProviders}
	g := e.gateway(p, nil)
	if _, err := get(g, dmm, "http://dmm.test/"); err != nil {
		t.Fatalf("got %v", err)
	}
	if _, err := get(g, Target{Class: ClassProvider, Key: "rd"}, "http://rd.test/rest/1.0/user"); !errors.Is(err, ErrVPNRequired) {
		t.Fatalf("provider: %v", err)
	}
}

func TestHostAllowlist(t *testing.T) {
	e := newEnv(t)
	var evilHits int
	e.serve("evil.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { evilHits++ }))
	e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/away":
			http.Redirect(w, r, "http://evil.test/steal", http.StatusFound)
		case "/home":
			http.Redirect(w, r, "http://rd.test/rest/1.0/x", http.StatusFound)
		default:
			io.WriteString(w, "ok")
		}
	}))
	e.serve("rd.test", okHandler("rd"))
	g := e.gateway(testPolicy(), nil)
	g.CheckEgress(context.Background())

	// An unlisted host is refused before dialing.
	if _, err := get(g, dmm, "http://evil.test/x"); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("unlisted host: %v", err)
	}
	if e.dialCount("evil.test:80") != 0 {
		t.Error("dialed an unlisted host")
	}
	// A listed host on another port is refused.
	if _, err := get(g, dmm, "http://dmm.test:8080/"); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("other port: %v", err)
	}
	if _, err := get(g, dmm, "ftp://dmm.test/"); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("ftp: %v", err)
	}
	// A redirect to an unlisted host is refused before dialing it.
	if _, err := get(g, dmm, "http://dmm.test/away"); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("redirect away: %v", err)
	}
	if evilHits != 0 || e.dialCount("evil.test:80") != 0 {
		t.Error("followed a redirect to an unlisted host")
	}
	// A redirect to a listed host is followed.
	if body, err := get(g, dmm, "http://dmm.test/home"); err != nil || body != "rd" {
		t.Fatalf("redirect to a listed host: %q %v", body, err)
	}
	if got := g.Hosts(); strings.Join(got, ",") != "decypharr.test,dmm.test,echo.test,rd.test,seedbox.test,semi.test,tmdb.test,tracker.test" {
		t.Errorf("hosts %v", got)
	}
}

func TestUnknownTarget(t *testing.T) {
	e := newEnv(t)
	g := e.gateway(testPolicy(), nil)
	if _, err := get(g, Target{Class: ClassIndexer, Key: "nope"}, "http://dmm.test/"); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("got %v", err)
	}
}

func TestBudgetPacing(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("ok"))
	p := testPolicy()
	p.Budgets["dmm-key"] = &policy.Budget{Requests: 10, Per: policy.Duration(time.Second)}
	p.Indexers["dmm"].Budget = "dmm-key"
	g := e.gateway(p, nil)
	g.CheckEgress(context.Background())
	start := e.clock()
	for i := 0; i < 5; i++ {
		if _, err := get(g, dmm, "http://dmm.test/"); err != nil {
			t.Fatal(err)
		}
	}
	// Burst 1: the first is free, the next four wait 100ms each.
	if waited := e.clock().Sub(start); waited < 390*time.Millisecond || waited > 410*time.Millisecond {
		t.Errorf("waited %v, want 400ms", waited)
	}
}

func TestRuntimeGatewayCarriesBudgetCounters(t *testing.T) {
	e := newEnv(t)
	p := testPolicy()
	p.Budgets["shared"] = &policy.Budget{Requests: 1, Per: policy.Duration(time.Hour), Burst: 1}
	p.Indexers["dmm"].Budget = "shared"
	previous := e.gateway(p, nil)
	if wait := previous.buckets["shared"].take(e.clock()); wait != 0 {
		t.Fatalf("first token wait %v", wait)
	}

	nextPolicy := testPolicy()
	nextPolicy.Budgets["shared"] = &policy.Budget{Requests: 2, Per: policy.Duration(time.Hour), Burst: 2}
	nextPolicy.Indexers["dmm"].Budget = "shared"
	next, err := New(context.Background(), nextPolicy, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Previous: previous, DialContext: e.dial, now: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	if next.buckets["shared"].state != previous.buckets["shared"].state {
		t.Fatal("runtime generation did not carry the budget counter")
	}
	if wait := next.buckets["shared"].take(e.clock()); wait <= 0 {
		t.Fatalf("second generation reset the token counter: wait %v", wait)
	}
}

func TestGatewayCallsUseCapturedRuntimeGeneration(t *testing.T) {
	e := newEnv(t)
	e.serve("old.test", okHandler("old"))
	e.serve("new.test", okHandler("new"))
	oldPolicy := testPolicy()
	oldPolicy.Indexers["dmm"].URL = "http://old.test"
	old, err := New(context.Background(), oldPolicy, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test", Generation: 1, DialContext: e.dial, now: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	old.CheckEgress(context.Background())
	newPolicy := testPolicy()
	newPolicy.Indexers["dmm"].URL = "http://new.test"
	next, err := New(context.Background(), newPolicy, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test", Previous: old, Generation: 2, DialContext: e.dial, now: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	next.Activate()
	ctx := settings.WithGeneration(context.Background(), 2)
	resp, err := old.Get(ctx, dmm, "http://new.test/search")
	if err != nil {
		t.Fatalf("startup gateway did not resolve generation 2: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "new" {
		t.Fatalf("response %q, %v", body, err)
	}
}

type gatewayRuntimeComponent struct{ gateway *Gateway }

func (c gatewayRuntimeComponent) EgressGateway() *Gateway { return c.gateway }

func TestBudgetRefusesLongWaits(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("ok"))
	p := testPolicy()
	p.Budgets["slow"] = &policy.Budget{Requests: 1, Per: policy.Duration(time.Hour)}
	p.Indexers["dmm"].Budget = "slow"
	g := e.gateway(p, nil)
	g.CheckEgress(context.Background())
	if _, err := get(g, dmm, "http://dmm.test/"); err != nil {
		t.Fatal(err)
	}
	_, err := get(g, dmm, "http://dmm.test/")
	var r *Refused
	if !errors.Is(err, ErrBudget) || !errors.As(err, &r) || r.RetryAt.Sub(e.clock()) != time.Hour {
		t.Fatalf("got %v", err)
	}
	if n := testutil.ToFloat64(g.metrics.budgetRefused.WithLabelValues("slow", "budget")); n != 1 {
		t.Errorf("budget refused metric %v", n)
	}
}

func TestDailyCapSurvivesRestart(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("ok"))
	p := testPolicy()
	p.Budgets["capped"] = &policy.Budget{Requests: 100, Per: policy.Duration(time.Second), Burst: 10, Daily: 2, Hourly: 2}
	p.Indexers["dmm"].Budget = "capped"
	dbPath := filepath.Join(t.TempDir(), "theatarr.db")
	st := openStore(t, dbPath)
	g := e.gateway(p, st)
	g.CheckEgress(context.Background())
	for i := 0; i < 2; i++ {
		if _, err := get(g, dmm, "http://dmm.test/"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := get(g, dmm, "http://dmm.test/"); !errors.Is(err, ErrBudget) || !strings.Contains(err.Error(), "cap 2 reached") {
		t.Fatalf("third request: %v", err)
	}

	// Restart: a new gateway on the same database still refuses.
	g2 := e.gateway(p, st)
	g2.CheckEgress(context.Background())
	if _, err := get(g2, dmm, "http://dmm.test/"); !errors.Is(err, ErrBudget) {
		t.Fatalf("after restart: %v", err)
	}
	// The next UTC day: allowed again.
	e.advance(13 * time.Hour)
	g2.CheckEgress(context.Background())
	if _, err := get(g2, dmm, "http://dmm.test/"); err != nil {
		t.Fatalf("next day: %v", err)
	}
	var rows int
	st.Reader().QueryRow(`SELECT COUNT(*) FROM gateway_budget_window WHERE budget = 'capped'`).Scan(&rows)
	if rows != 4 {
		t.Errorf("%d window rows, want 4 (hour and day, for two days)", rows)
	}
}

func Test429RetryAfter(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	limited := true
	e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if limited {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		io.WriteString(w, "ok")
	}))
	p := testPolicy()
	p.Budgets["dmm-key"] = &policy.Budget{Requests: 100, Per: policy.Duration(time.Second), Burst: 10}
	p.Indexers["dmm"].Budget = "dmm-key"
	st := openStore(t, filepath.Join(t.TempDir(), "theatarr.db"))
	g := e.gateway(p, st)
	g.CheckEgress(context.Background())
	resp, err := g.Get(context.Background(), dmm, "http://dmm.test/")
	if err != nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("first: %v %v", resp, err)
	}
	resp.Body.Close()
	// 120s is over MaxWait (60s): refused, with the retry time.
	_, err = get(g, dmm, "http://dmm.test/")
	var r *Refused
	if !errors.Is(err, ErrBackoff) || !errors.As(err, &r) || r.RetryAt.Sub(e.clock()) != 120*time.Second {
		t.Fatalf("during back-off: %v", err)
	}
	// Persisted: a restart still backs off.
	g2 := e.gateway(p, st)
	g2.CheckEgress(context.Background())
	if _, err := get(g2, dmm, "http://dmm.test/"); !errors.Is(err, ErrBackoff) {
		t.Fatalf("after restart: %v", err)
	}
	if n := testutil.ToFloat64(g.metrics.budgetBackoffs.WithLabelValues("dmm-key")); n != 1 {
		t.Errorf("budget back-off metric %v", n)
	}
	// Later, it passes and the back-off is cleared.
	mu.Lock()
	limited = false
	mu.Unlock()
	e.advance(121 * time.Second)
	g2.CheckEgress(context.Background())
	if _, err := get(g2, dmm, "http://dmm.test/"); err != nil {
		t.Fatal(err)
	}
	var left int
	st.Reader().QueryRow(`SELECT COUNT(*) FROM gateway_backoff`).Scan(&left)
	if left != 0 {
		t.Errorf("%d back-off rows left after a success", left)
	}
}

func TestDMMKeyRateLimitScopesEveryFeed(t *testing.T) {
	g := &Gateway{targets: map[string]*targetInfo{}, users: map[string][]string{}}
	for _, key := range []string{"dmm", "dmm-ad", "dmm-tb"} {
		ti := &targetInfo{class: ClassIndexer, key: key, budget: "dmm-key", endpoint: "https://dmm.test/" + key}
		ti.name = string(ti.class) + ":" + key
		g.targets[ti.name] = ti
	}
	g.scopeRateLimits()
	for _, ti := range g.targets {
		if !ti.limitBudget {
			t.Errorf("%s 429 should back off the shared DMM key", ti.name)
		}
	}
}

func Test429WithoutRetryAfterWaits(t *testing.T) {
	e := newEnv(t)
	n := 0
	e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		io.WriteString(w, "ok")
	}))
	p := testPolicy()
	p.Budgets["dmm-key"] = &policy.Budget{Requests: 100, Per: policy.Duration(time.Second), Burst: 10}
	p.Indexers["dmm"].Budget = "dmm-key"
	g := e.gateway(p, nil)
	g.CheckEgress(context.Background())
	resp, _ := g.Get(context.Background(), dmm, "http://dmm.test/")
	resp.Body.Close()
	start := e.clock()
	if _, err := get(g, dmm, "http://dmm.test/"); err != nil {
		t.Fatal(err)
	}
	// The first back-off is 30s, under MaxWait: the request waited instead of failing.
	if w := e.clock().Sub(start); w != 30*time.Second {
		t.Errorf("waited %v, want 30s", w)
	}
}

func TestFailureBackoff(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	g := e.gateway(testPolicy(), nil)
	g.CheckEgress(context.Background())
	for i := 0; i < failuresBeforeBackoff; i++ {
		resp, err := g.Get(context.Background(), dmm, "http://dmm.test/")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
	}
	_, err := get(g, dmm, "http://dmm.test/")
	var r *Refused
	if !errors.Is(err, ErrBackoff) || !errors.As(err, &r) || r.RetryAt.Sub(e.clock()) != firstFailureBackoff {
		t.Fatalf("after %d failures: %v", failuresBeforeBackoff, err)
	}
	if !strings.Contains(err.Error(), "3 failures in a row, last: HTTP 502") {
		t.Errorf("detail: %v", err)
	}
	// Caller-set back-off.
	rd := Target{Class: ClassProvider, Key: "rd"}
	g.BackOff(context.Background(), rd, e.clock().Add(time.Minute), "too_many_active_downloads")
	if _, err := get(g, rd, "http://rd.test/rest/1.0/x"); !errors.Is(err, ErrBackoff) {
		t.Fatalf("caller back-off: %v", err)
	}
}

func TestResponseTooLarge(t *testing.T) {
	e := newEnv(t)
	big := strings.Repeat("x", 100)
	e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chunked" {
			w.(http.Flusher).Flush() // no Content-Length
		}
		io.WriteString(w, big)
	}))
	g := e.gateway(testPolicy(), nil)
	g.CheckEgress(context.Background())
	small := Target{Class: ClassIndexer, Key: "dmm", MaxBytes: 50}
	if _, err := get(g, small, "http://dmm.test/"); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("Content-Length over the limit: %v", err)
	}
	if _, err := get(g, small, "http://dmm.test/chunked"); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("chunked over the limit: %v", err)
	}
	exact := Target{Class: ClassIndexer, Key: "dmm", MaxBytes: 100}
	if body, err := get(g, exact, "http://dmm.test/chunked"); err != nil || len(body) != 100 {
		t.Fatalf("exactly the limit: %d %v", len(body), err)
	}
}

func TestLogsAndErrorsAreRedacted(t *testing.T) {
	const secret = "s3cr3t-rd-key-123"
	t.Setenv("THEATARR_TEST_RD_KEY", secret)
	e := newEnv(t)
	e.serve("dmm.test", okHandler("ok"))
	g := e.gateway(testPolicy(), nil)
	g.CheckEgress(context.Background())
	if _, err := get(g, dmm, "http://dmm.test/realdebrid="+secret+"/stream?apikey=qwerty"); err != nil {
		t.Fatal(err)
	}
	// A transport error (rd.test has no server) carries the URL: redacted.
	_, err := get(g, Target{Class: ClassProvider, Key: "rd"}, "http://rd.test/rest/1.0/"+secret+"?token=qwerty")
	if err == nil {
		t.Fatal("want a transport error")
	}
	for _, s := range []string{e.logs.String(), err.Error()} {
		if strings.Contains(s, secret) || strings.Contains(s, "qwerty") {
			t.Errorf("secret leaked: %s", s)
		}
	}
	if !strings.Contains(e.logs.String(), "/realdebrid=***/stream") {
		t.Errorf("path not logged redacted:\n%s", e.logs.String())
	}
}

func TestProxyEgressPath(t *testing.T) {
	e := newEnv(t)
	const proxyIP = "45.0.0.2"
	var proxied []string
	var mu sync.Mutex
	// A forward proxy: it sees absolute-form requests. It answers the echo with its own exit address and relays
	// everything else as a fixed body.
	e.serve("proxy.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		proxied = append(proxied, r.Host)
		mu.Unlock()
		if r.Host == "echo.test" {
			io.WriteString(w, proxyIP)
			return
		}
		io.WriteString(w, "via proxy")
	}))
	e.serve("dmm.test", okHandler("direct"))
	p := testPolicy()
	p.Indexers["torrentio"] = &policy.Indexer{Kind: "torrentio", URL: "http://torrentio.test", Privacy: policy.PrivacyPublic,
		Proxy: "http://proxy.test"}
	p.Egress.VPNIPs = []string{proxyIP} // only the proxy exits through the VPN
	e.setEcho("203.0.113.9")            // the direct path is on the WAN
	g := e.gateway(p, nil)
	if got := strings.Join(g.EgressPaths(), ","); got != "direct,http://proxy.test" {
		t.Fatalf("paths %s", got)
	}
	states := g.CheckEgress(context.Background())
	if states[0].OK || !states[1].OK || states[1].IP != proxyIP {
		t.Fatalf("states %+v", states)
	}
	if body, err := get(g, Target{Class: ClassIndexer, Key: "torrentio"}, "http://torrentio.test/stream"); err != nil || body != "via proxy" {
		t.Fatalf("proxied indexer: %q %v", body, err)
	}
	if _, err := get(g, dmm, "http://dmm.test/"); !errors.Is(err, ErrVPNRequired) {
		t.Fatalf("direct indexer on the WAN: %v", err)
	}
	if e.dialCount("torrentio.test:80") != 0 {
		t.Error("proxied indexer dialed directly")
	}
	sum := g.EgressSummary()
	if sum["vpn_ok"] != false {
		t.Errorf("summary %v", sum)
	}
}

func TestMetricsAndSummary(t *testing.T) {
	e := newEnv(t)
	p := testPolicy()
	p.Budgets["capped"] = &policy.Budget{Requests: 1, Per: policy.Duration(time.Second), Daily: 10}
	g := e.gateway(p, nil)
	g.CheckEgress(context.Background())
	want := `
# HELP theatarr_egress_vpn_ok 1 if the path's latest egress check is fresh and saw a VPN address; 0 otherwise.
# TYPE theatarr_egress_vpn_ok gauge
theatarr_egress_vpn_ok{path="direct"} 1
# HELP theatarr_egress_observed_ip_info The exit address the latest egress check saw; always 1.
# TYPE theatarr_egress_observed_ip_info gauge
theatarr_egress_observed_ip_info{ip="45.0.0.1",path="direct"} 1
# HELP theatarr_gateway_budget_window_requests Requests counted in the current hourly or daily window of a capped budget.
# TYPE theatarr_gateway_budget_window_requests gauge
theatarr_gateway_budget_window_requests{budget="capped",period="day"} 0
`
	if err := testutil.GatherAndCompare(e.reg, strings.NewReader(want), "theatarr_egress_vpn_ok",
		"theatarr_egress_observed_ip_info", "theatarr_gateway_budget_window_requests"); err != nil {
		t.Error(err)
	}
	if n := testutil.ToFloat64(g.metrics.egressChecks.WithLabelValues("direct", "ok")); n != 1 {
		t.Errorf("checks metric %v", n)
	}
	sum := g.EgressSummary()
	if sum["vpn_ok"] != true {
		t.Errorf("summary %v", sum)
	}
	e.advance(10 * time.Minute)
	if sum := g.EgressSummary(); sum["vpn_ok"] != false {
		t.Errorf("stale summary %v", sum)
	}
}

func TestRunEgressChecksAtStart(t *testing.T) {
	e := newEnv(t)
	g := e.gateway(testPolicy(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.RunEgress(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for g.EgressStates()[0].CheckedAt.IsZero() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if s := g.EgressStates()[0]; !s.OK || s.IP != vpnIP {
		t.Fatalf("state %+v", s)
	}
}

func TestParseEcho(t *testing.T) {
	for in, want := range map[string]string{
		"45.0.0.1\n":                     "45.0.0.1",
		"2001:db8::1":                    "2001:db8::1",
		`{"ip": "45.0.0.1", "cc": "NL"}`: "45.0.0.1",
		"::ffff:45.0.0.1":                "45.0.0.1",
	} {
		a, err := parseEcho([]byte(in))
		if err != nil || a.String() != want {
			t.Errorf("parseEcho(%q) = %v, %v", in, a, err)
		}
	}
	for _, in := range []string{"", "<html>", `{"origin":"1.2.3.4"}`, `{"ip":"nope"}`} {
		if _, err := parseEcho([]byte(in)); err == nil {
			t.Errorf("parseEcho(%q) accepted", in)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"120":                           120 * time.Second,
		"0":                             0,
		"Sun, 25 Oct 2026 12:05:00 GMT": 5 * time.Minute,
		"Sun, 25 Oct 2026 11:00:00 GMT": 0,
	}
	for in, want := range cases {
		if d, ok := parseRetryAfter(in, now); !ok || d != want {
			t.Errorf("parseRetryAfter(%q) = %v %v", in, d, ok)
		}
	}
	for _, in := range []string{"", "-5", "soon"} {
		if _, ok := parseRetryAfter(in, now); ok {
			t.Errorf("parseRetryAfter(%q) accepted", in)
		}
	}
}

func TestAllowlistPorts(t *testing.T) {
	p := &policy.Policy{Indexers: map[string]*policy.Indexer{
		"a": {URL: "https://a.example"},
		"b": {URL: "http://b.example:9696/1/api"},
		"c": {URL: "http://[2001:db8::5]/x"},
	}}
	a, _ := newAllowlist(p)
	for raw, ok := range map[string]bool{
		"https://a.example/x":             true,
		"http://A.EXAMPLE/x":              true,
		"https://a.example:8443/":         false,
		"http://b.example:9696/":          true,
		"http://b.example/":               false,
		"http://[2001:db8::5]/y":          true,
		"http://[2001:db8::6]/y":          false,
		"https://a.example.evil/":         false,
		"https://evil.example/?a.example": false,
	} {
		u := mustURL(raw)
		if got := a.check(u) == nil; got != ok {
			t.Errorf("%s: allowed=%v, want %v", raw, got, ok)
		}
	}
}

// A 429 backs off the target when its budget is shared with targets on other endpoints (Prowlarr's per-indexer
// feeds), and the budget when the budget has one user, the users share an endpoint, or the target is a provider.
func Test429Scope(t *testing.T) {
	for _, tc := range []struct {
		name      string
		otherURL  string // URL of the second indexer sharing the budget; empty: no second user
		provider  bool   // the limited target is a provider sharing the budget
		wantScope string // "target" or "budget"
	}{
		{name: "shared, own endpoints", otherURL: "http://prowlarr.test/8/api", wantScope: "target"},
		{name: "shared, same endpoint", otherURL: "http://prowlarr.test/7/api/", wantScope: "budget"},
		{name: "one user", wantScope: "budget"},
		{name: "provider", otherURL: "http://prowlarr.test/8/api", provider: true, wantScope: "budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.serve("prowlarr.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/7/") {
					w.Header().Set("Retry-After", "300")
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				io.WriteString(w, "ok")
			}))
			e.serve("rd.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "300")
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			p := testPolicy()
			p.Budgets["prowlarr"] = &policy.Budget{Requests: 100, Per: policy.Duration(time.Second), Burst: 10}
			p.Indexers["therarbg"] = &policy.Indexer{Kind: "torznab", URL: "http://prowlarr.test/7/api",
				Privacy: policy.PrivacyPublic, Budget: "prowlarr"}
			if tc.otherURL != "" {
				p.Indexers["tpb"] = &policy.Indexer{Kind: "torznab", URL: tc.otherURL, Privacy: policy.PrivacyPublic,
					Budget: "prowlarr"}
			}
			limited, subject := Target{Class: ClassIndexer, Key: "therarbg"}, "indexer:therarbg"
			url := "http://prowlarr.test/7/api?t=search"
			if tc.provider {
				p.Providers["rd"].Budget = "prowlarr"
				limited, subject, url = Target{Class: ClassProvider, Key: "rd"}, "provider:rd", "http://rd.test/rest/1.0/user"
			}
			if tc.wantScope == "budget" {
				subject = "budget:prowlarr"
			}
			st := openStore(t, filepath.Join(t.TempDir(), "theatarr.db"))
			g := e.gateway(p, st)
			g.CheckEgress(context.Background())
			resp, err := g.Get(context.Background(), limited, url)
			if err != nil || resp.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("first: %v %v", resp, err)
			}
			resp.Body.Close()
			if _, err := get(g, limited, url); !errors.Is(err, ErrBackoff) {
				t.Fatalf("limited target again: %v", err)
			}
			var subj string
			if err := st.Reader().QueryRow(`SELECT subject FROM gateway_backoff`).Scan(&subj); err != nil || subj != subject {
				t.Errorf("persisted back-off %q (%v), want %q", subj, err, subject)
			}
			budgetN := testutil.ToFloat64(g.metrics.budgetBackoffs.WithLabelValues("prowlarr"))
			targetN := testutil.ToFloat64(g.metrics.targetBackoffs.WithLabelValues(limited.String()))
			if (tc.wantScope == "budget") != (budgetN == 1 && targetN == 0) {
				t.Errorf("metrics: budget %v, target %v", budgetN, targetN)
			}
			if tc.otherURL == "" {
				return
			}
			_, err = get(g, Target{Class: ClassIndexer, Key: "tpb"}, tc.otherURL)
			if tc.wantScope == "target" && err != nil {
				t.Errorf("other feed refused: %v", err)
			}
			if tc.wantScope == "budget" && !errors.Is(err, ErrBackoff) {
				t.Errorf("other feed during a budget back-off: %v", err)
			}
		})
	}
}

func TestState(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("ok"))
	e.serve("tracker.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	p := testPolicy()
	p.Budgets["dmm-key"] = &policy.Budget{Requests: 60, Per: policy.Duration(time.Minute), Burst: 4, Hourly: 2, Daily: 100}
	p.Indexers["dmm"].Budget = "dmm-key"
	p.Indexers["tracker"].Budget = "dmm-key"
	g := e.gateway(p, nil)
	g.CheckEgress(context.Background())
	for range 2 {
		if _, err := get(g, dmm, "http://dmm.test/"); err != nil {
			t.Fatal(err)
		}
	}
	tracker := Target{Class: ClassIndexer, Key: "tracker"}
	if resp, err := g.Get(context.Background(), tracker, "http://tracker.test/api"); err == nil {
		resp.Body.Close()
	}
	s := g.State()
	if s.Egress["vpn_ok"] != true || len(s.Paths) != 1 || s.Paths[0].Path != "direct" || !s.Paths[0].OK {
		t.Errorf("egress: %v %+v", s.Egress, s.Paths)
	}
	if len(s.Budgets) != 1 {
		t.Fatalf("budgets: %+v", s.Budgets)
	}
	b := s.Budgets[0]
	if b.Key != "dmm-key" || b.Requests != 60 || b.Per != time.Minute || b.Burst != 4 || b.UsedHour != 2 ||
		!b.HourCapped || b.UsedDay != 2 || b.DayCapped || b.Tokens != 2 || b.Backoff != nil ||
		strings.Join(b.UsedBy, ",") != "indexer:dmm,indexer:tracker" {
		t.Errorf("budget: %+v", b)
	}
	// The third request hit the hourly cap before sending, so the tracker has no failure row.
	if len(s.Backoffs) != 0 {
		t.Errorf("backoffs: %+v", s.Backoffs)
	}
	g.BackOff(context.Background(), tracker, e.clock().Add(time.Minute), "test")
	s = g.State()
	if len(s.Backoffs) != 1 || s.Backoffs[0].Subject != "indexer:tracker" || !s.Backoffs[0].Active ||
		s.Backoffs[0].UpdatedAt != e.clock() {
		t.Errorf("backoffs: %+v", s.Backoffs)
	}
	e.advance(2 * time.Minute)
	if s = g.State(); s.Backoffs[0].Active || s.Budgets[0].Tokens != 4 {
		t.Errorf("later: %+v %+v", s.Backoffs, s.Budgets)
	}
}

func TestVPNRecheckedAfterBudgetWait(t *testing.T) {
	for _, bad := range []bool{false, true} {
		t.Run(fmt.Sprintf("non-vpn=%t", bad), func(t *testing.T) {
			e := newEnv(t)
			sent := 0
			e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { sent++; io.WriteString(w, "ok") }))
			p := testPolicy()
			p.Egress.MaxAge = policy.Duration(time.Second)
			p.Budgets["wait"] = &policy.Budget{Requests: 1, Per: policy.Duration(2 * time.Second)}
			p.Indexers["dmm"].Budget = "wait"
			g := e.gateway(p, nil)
			g.CheckEgress(context.Background())
			if _, err := get(g, dmm, "http://dmm.test/"); err != nil {
				t.Fatal(err)
			}
			g.sleep = func(ctx context.Context, d time.Duration) error {
				e.advance(d)
				if bad {
					e.setEcho("192.0.2.1")
					g.CheckEgress(ctx)
				}
				return ctx.Err()
			}
			_, err := get(g, dmm, "http://dmm.test/")
			var refused *Refused
			if !errors.As(err, &refused) || refused.Reason != ReasonVPN || sent != 1 {
				t.Fatalf("send=%d err=%v", sent, err)
			}
		})
	}
}

func TestVPNRecheckedOnRedirect(t *testing.T) {
	e := newEnv(t)
	sent := 0
	e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent++
		e.advance(4 * time.Minute)
		http.Redirect(w, r, "http://dmm.test/next", http.StatusFound)
	}))
	g := e.gateway(testPolicy(), nil)
	g.CheckEgress(context.Background())
	_, err := get(g, dmm, "http://dmm.test/")
	var refused *Refused
	if !errors.As(err, &refused) || refused.Reason != ReasonVPN || sent != 1 {
		t.Fatalf("send=%d err=%v", sent, err)
	}
}

func TestVPNRecheckedOnTransportRetry(t *testing.T) {
	e := newEnv(t)
	var g *Gateway
	var requests atomic.Int32
	e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n == 1 {
			io.WriteString(w, "primed")
			return
		}
		if n == 2 {
			e.setEcho("192.0.2.1")
			g.CheckEgress(context.Background())
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		io.WriteString(w, "unsafe retry")
	}))
	g = e.gateway(testPolicy(), nil)
	g.CheckEgress(context.Background())
	if _, err := get(g, dmm, "http://dmm.test/"); err != nil {
		t.Fatal(err)
	}
	_, err := get(g, dmm, "http://dmm.test/")
	var refused *Refused
	if !errors.As(err, &refused) || refused.Reason != ReasonVPN || requests.Load() != 2 || e.dialCount("dmm.test:80") != 1 {
		t.Fatalf("requests=%d dials=%d err=%v", requests.Load(), e.dialCount("dmm.test:80"), err)
	}
}
