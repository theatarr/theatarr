package indexer

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
)

// The test network: host names under .test map to local httptest servers through the gateway's DialContext.
// Nothing leaves the machine, and every request goes through a real gateway (VPN guard, allowlist, budgets).

const vpnIP = "45.0.0.1"

type testNet struct {
	t     *testing.T
	mu    sync.Mutex
	hosts map[string]string
	dials map[string]int
	logs  syncBuffer
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

func newNet(t *testing.T) *testNet {
	n := &testNet{t: t, hosts: map[string]string{}, dials: map[string]int{}}
	n.serve("echo.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, vpnIP) }))
	return n
}

func (n *testNet) serve(name string, h http.Handler) {
	srv := httptest.NewServer(h)
	n.t.Cleanup(srv.Close)
	n.mu.Lock()
	n.hosts[name+":80"] = srv.Listener.Addr().String()
	n.mu.Unlock()
}

func (n *testNet) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	n.mu.Lock()
	n.dials[addr]++
	real, ok := n.hosts[addr]
	n.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("dial %s: no such host in the test network", addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, real)
}

func (n *testNet) dialCount(addr string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.dials[addr]
}

const (
	prowlarrKeyEnv = "THEATARR_TEST_PROWLARR_KEY"
	dmmKeyEnv      = "THEATARR_TEST_DMM_KEY"
	prowlarrKey    = "prowlarr-secret-0123456789"
	dmmKey         = "dmm-secret-9876543210"
)

func boolp(b bool) *bool { return &b }

func testPolicy() *policy.Policy {
	return &policy.Policy{
		Indexers: map[string]*policy.Indexer{
			"prowlarr":  {Kind: "torznab", URL: "http://prowlarr.test/1/api", APIKeyEnv: prowlarrKeyEnv, Privacy: policy.PrivacyPublic, Budget: "prowlarr"},
			"tpb":       {Kind: "torznab", URL: "http://prowlarr.test/9/api", APIKeyEnv: prowlarrKeyEnv, Privacy: policy.PrivacyPublic},
			"tracker":   {Kind: "torznab", URL: "http://prowlarr.test/7/api", APIKeyEnv: prowlarrKeyEnv, Privacy: policy.PrivacyPrivate},
			"dmm":       {Kind: "dmm", URL: "http://dmm.test/api/torznab/rd/cached", APIKeyEnv: dmmKeyEnv, Privacy: policy.PrivacyPublic, Budget: "dmm-key"},
			"torrentio": {Kind: "torrentio", URL: "http://torrentio.test/providers=yts,eztv|sort=qualitysize", Privacy: policy.PrivacyPublic},
			"zilean":    {Kind: "zilean", URL: "http://zilean.test", Privacy: policy.PrivacyPublic},
			"seadex":    {Kind: "seadex", URL: "http://seadex.test", Privacy: policy.PrivacyPublic},
		},
		Budgets: map[string]*policy.Budget{
			"prowlarr": {Requests: 600, Per: policy.Duration(time.Minute), Burst: 100},
			"dmm-key":  {Requests: 600, Per: policy.Duration(time.Minute), Burst: 100},
		},
		Egress: &policy.Egress{CheckURL: "http://echo.test/", VPNIPs: []string{vpnIP},
			Interval: policy.Duration(time.Minute), MaxAge: policy.Duration(3 * time.Minute),
			Timeout: policy.Duration(5 * time.Second), RequireVPNFor: []string{policy.EgressIndexers}},
		Versions: map[string]*policy.VersionPolicy{
			"movie-remux": {Kind: policy.KindMovie, Anime: boolp(false), Storage: policy.StorageDebrid,
				Indexers: []string{"zilean", "torrentio", "dmm", "prowlarr"}},
			"movie-anime": {Kind: policy.KindMovie, Anime: boolp(true), Storage: policy.StorageDebrid,
				Indexers: []string{"seadex", "prowlarr"}},
			"movie-seedbox": {Kind: policy.KindMovie, Storage: policy.StorageSeedbox,
				Indexers: []string{"tracker", "prowlarr"}},
			"series-web": {Kind: policy.KindSeries, Anime: boolp(false), Storage: policy.StorageDebrid,
				Indexers: []string{"torrentio", "zilean", "dmm"}},
		},
	}
}

type harness struct {
	net *testNet
	gw  *gateway.Gateway
	svc *Service
	now time.Time
	mu  sync.Mutex
}

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

type harnessOpt func(*gateway.Options, *Options)

// newHarness builds a gateway and service over the test network. The egress check runs (and passes) unless
// skipEgress.
func newHarness(t *testing.T, n *testNet, p *policy.Policy, skipEgress bool, opts ...harnessOpt) *harness {
	t.Helper()
	t.Setenv(prowlarrKeyEnv, prowlarrKey)
	t.Setenv(dmmKeyEnv, dmmKey)
	if err := os.Setenv(prowlarrKeyEnv, prowlarrKey); err != nil {
		t.Fatal(err)
	}
	h := &harness{net: n, now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
	log := slog.New(slog.NewJSONHandler(&n.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	gopts := gateway.Options{Logger: log, Version: "test", DialContext: n.dial, Timeout: 5 * time.Second,
		MaxWait: 2 * time.Second}
	iopts := Options{Logger: log, now: h.clock}
	for _, o := range opts {
		o(&gopts, &iopts)
	}
	gw, err := gateway.New(context.Background(), p, gopts)
	if err != nil {
		t.Fatal(err)
	}
	if !skipEgress {
		for _, s := range gw.CheckEgress(context.Background()) {
			if !s.OK {
				t.Fatalf("egress check in the test network: %+v", s)
			}
		}
	}
	svc, err := New(p, gw, iopts)
	if err != nil {
		t.Fatal(err)
	}
	h.gw, h.svc = gw, svc
	return h
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// recorder serves fixtures by a routing function and records each request's query.
type recorder struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (r *recorder) add(req *http.Request) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req.Clone(context.Background()))
	r.mu.Unlock()
}

func (r *recorder) all() []*http.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*http.Request(nil), r.reqs...)
}
