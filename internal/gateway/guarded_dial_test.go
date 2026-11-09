package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// famNet is a test network for guarded dials: names resolve through dns (which a test may change between lookups),
// a dial to a resolved address reaches the server of the name it belongs to, and the second (pinned) echo answers
// other, or fails when other is empty.
type famNet struct {
	e     *env
	mu    sync.Mutex
	dns   map[string][]netip.Addr
	owner map[string]string // "ip:port" -> "name:port"
	other string
	dials []string // "network addr"
	echos int      // dials of the first echo
}

func newFamNet(t *testing.T) *famNet {
	e := newEnv(t)
	f := &famNet{e: e, dns: map[string][]netip.Addr{}, owner: map[string]string{}}
	e.serve("other.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		io.WriteString(w, f.other+"\n")
	}))
	return f
}

// host serves name at addrs (port 80).
func (f *famNet) host(name string, h http.Handler, addrs ...string) {
	f.e.serve(name, h)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range addrs {
		ip := netip.MustParseAddr(a)
		f.dns[name] = append(f.dns[name], ip)
		f.owner[net.JoinHostPort(ip.String(), "80")] = name + ":80"
	}
}

func (f *famNet) setOther(ip string) {
	f.mu.Lock()
	f.other = ip
	f.mu.Unlock()
}

func (f *famNet) dialed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.dials
	f.dials = nil
	return d
}

func (f *famNet) lookup(_ context.Context, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.dns[host]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("no such host %s", host)
}

func (f *famNet) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	f.mu.Lock()
	f.dials = append(f.dials, network+" "+addr)
	other := f.other
	if name, ok := f.owner[addr]; ok {
		addr = name
	}
	if addr == "echo.test:80" && network == "tcp" {
		f.echos++
	}
	f.mu.Unlock()
	if addr == "echo.test:80" && network != "tcp" {
		if other == "" {
			return nil, errors.New("network is unreachable")
		}
		addr = "other.test:80"
	}
	return f.e.dial(ctx, "tcp", addr) // the test servers listen on IPv4 loopback
}

func (f *famNet) policy() *policy.Policy {
	p := testPolicy()
	p.Egress.VPNIPs = append(p.Egress.VPNIPs, "2001:db8::1")
	p.Indexers["lan"] = &policy.Indexer{Kind: "torznab", URL: "http://lan.test/api", Privacy: policy.PrivacyPublic}
	p.Indexers["v6only"] = &policy.Indexer{Kind: "torznab", URL: "http://v6only.test/api", Privacy: policy.PrivacyPublic}
	return p
}

func (f *famNet) gateway(t *testing.T, previous *Gateway) *Gateway {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	g, err := New(context.Background(), f.policy(), Options{Logger: quiet, now: f.e.clock, Previous: previous,
		LookupIP: f.lookup, DialContext: f.dial})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

var (
	lanIx    = Target{Class: ClassIndexer, Key: "lan"}
	v6onlyIx = Target{Class: ClassIndexer, Key: "v6only"}
)

// A guarded dial goes to the addresses the guard resolved and checked, never to the name again: with
// only IPv4 proven, a LAN host whose DNS answer turns into a public IPv6 address between the check and the dial
// still reaches only the private address, and a dual-stack public host only its IPv4 one. The request keeps its
// name: the Host header and the TLS server name are the host's.
func TestGuardedDialUsesCheckedAddresses(t *testing.T) {
	f := newFamNet(t)
	var hostSeen string
	f.host("lan.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		hostSeen = r.Host
		f.mu.Unlock()
		io.WriteString(w, "lan")
	}), "192.168.1.5")
	f.host("dmm.test", okHandler("dmm"), "203.0.113.5", "2001:db8::5")
	g := f.gateway(t, nil)
	g.Activate()
	g.CheckEgress(context.Background()) // IPv4 proven only: the pinned echo has no route
	f.dialed()

	// The guard's lookup sees the private address; any later lookup would see a public IPv6 one.
	var once sync.Once
	g.lookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
		ips, err := f.lookup(ctx, host)
		if host == "lan.test" {
			once.Do(func() {
				f.mu.Lock()
				f.dns["lan.test"] = []netip.Addr{netip.MustParseAddr("2001:db8::66")}
				f.mu.Unlock()
			})
		}
		return ips, err
	}
	if body, err := get(g, lanIx, "http://lan.test/api"); err != nil || body != "lan" {
		t.Fatalf("LAN host: %q %v", body, err)
	}
	if d := f.dialed(); len(d) != 1 || d[0] != "tcp4 192.168.1.5:80" {
		t.Fatalf("LAN host dialled %q, want only its checked address", d)
	}
	f.mu.Lock()
	if hostSeen != "lan.test" {
		t.Errorf("Host header %q", hostSeen)
	}
	f.mu.Unlock()
	if body, err := get(g, dmm, "http://dmm.test/"); err != nil || body != "dmm" {
		t.Fatalf("dual-stack host: %q %v", body, err)
	}
	if d := f.dialed(); len(d) != 1 || d[0] != "tcp4 203.0.113.5:80" {
		t.Fatalf("dual-stack host dialled %q, want its IPv4 address", d)
	}

	// TLS: the handshake to the checked address names the host.
	sni := make(chan string, 1)
	srv := httptest.NewUnstartedServer(okHandler("tls"))
	srv.TLS = &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		sni <- h.ServerName
		return nil, nil
	}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	f.e.mu.Lock()
	f.e.hosts["dmm.test:443"] = srv.Listener.Addr().String()
	f.e.mu.Unlock()
	f.mu.Lock()
	f.owner["203.0.113.5:443"] = "dmm.test:443"
	f.mu.Unlock()
	get(g, dmm, "https://dmm.test/") // the test certificate is not trusted: only the hello matters
	select {
	case name := <-sni:
		if name != "dmm.test" {
			t.Errorf("TLS server name %q", name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no TLS handshake")
	}
	if d := f.dialed(); len(d) != 1 || d[0] != "tcp4 203.0.113.5:443" {
		t.Fatalf("TLS dial %q", d)
	}
}

// A check that stops proving a family closes the guarded connections of that family in every generation sharing
// the check, so a retired generation's kept-alive connection is not reused.
func TestRevokeReachesRetiredGenerations(t *testing.T) {
	f := newFamNet(t)
	f.host("v6only.test", okHandler("v6"), "2001:db8::5")
	old := f.gateway(t, nil)
	old.Activate()
	f.setOther("2001:db8::1")
	old.CheckEgress(context.Background()) // both families proven
	ctx := context.Background()
	if _, err := getVia(ctx, old, v6onlyIx, "http://v6only.test/api"); err != nil {
		t.Fatal(err)
	}
	f.dialed()
	if _, err := getVia(ctx, old, v6onlyIx, "http://v6only.test/api"); err != nil {
		t.Fatal(err)
	}
	if d := f.dialed(); len(d) != 0 {
		t.Fatalf("the second request dialled %q; the test needs a kept-alive connection", d)
	}

	next := f.gateway(t, old)
	next.Activate()
	f.setOther("")
	next.CheckEgress(ctx) // only IPv4 proven now
	if _, err := getVia(ctx, old, v6onlyIx, "http://v6only.test/api"); refusedReason(err) != ReasonVPN {
		t.Fatalf("retired generation over IPv6 after the check stopped proving it: %v, want a VPN refusal", err)
	}
	if _, err := getVia(ctx, next, v6onlyIx, "http://v6only.test/api"); refusedReason(err) != ReasonVPN {
		t.Fatalf("current generation: %v", err)
	}
}

// A connection busy when its family loses proof is closed too, not left to go back to the pool (or, over HTTP/2, to
// take new streams) once its request ends.
func TestRevokeClosesBusyConnections(t *testing.T) {
	f := newFamNet(t)
	started, release := make(chan struct{}), make(chan struct{})
	f.host("v6only.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("slow") != "" {
			w.Write([]byte("part"))
			w.(http.Flusher).Flush()
			close(started)
			<-release
		}
		io.WriteString(w, "v6")
	}), "2001:db8::5")
	g := f.gateway(t, nil)
	g.Activate()
	f.setOther("2001:db8::1")
	ctx := context.Background()
	g.CheckEgress(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := get(g, v6onlyIx, "http://v6only.test/api?slow=1")
		done <- err
	}()
	<-started
	f.setOther("")
	g.CheckEgress(ctx) // IPv6 no longer proven while the request is in flight
	close(release)
	if err := <-done; err == nil {
		t.Error("the busy IPv6 connection outlived the check that stopped proving its family")
	}
	if _, err := get(g, v6onlyIx, "http://v6only.test/api"); refusedReason(err) != ReasonVPN {
		t.Fatalf("after the busy request: %v, want a VPN refusal", err)
	}
}

// Every egress echo opens a new connection: a kept-alive one could keep a route a change of the
// routing has since replaced and report a stale exit address.
func TestEchoNeverReusesConnections(t *testing.T) {
	f := newFamNet(t)
	g := f.gateway(t, nil)
	g.Activate()
	for range 3 {
		g.CheckEgress(context.Background())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.echos != 3 {
		t.Fatalf("3 checks dialled the echo %d times", f.echos)
	}
}

// A guarded request on the direct path never reuses a connection an unguarded request opened to the same host: that
// one was dialled with no family check.
func TestGuardedPoolSeparate(t *testing.T) {
	f := newFamNet(t)
	f.host("v6only.test", okHandler("v6"), "2001:db8::5")
	g := f.gateway(t, nil)
	g.pol.Metadata.Sources["tmdb"].URL = "http://v6only.test/3"
	g.allow["v6only.test"] = true
	g.Activate()
	g.CheckEgress(context.Background()) // IPv4 proven only
	tmdb := Target{Class: ClassMetadata, Key: "tmdb"}
	if _, err := get(g, tmdb, "http://v6only.test/3"); err != nil {
		t.Fatalf("unguarded request: %v", err)
	}
	if _, err := get(g, v6onlyIx, "http://v6only.test/api"); refusedReason(err) != ReasonVPN {
		t.Fatalf("guarded request after an unguarded one to the same IPv6-only host: %v, want a VPN refusal", err)
	}
}

// getVia sends through one generation, whichever is current.
func getVia(ctx context.Context, g *Gateway, t Target, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := g.do(ctx, t, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return strings.TrimSpace(string(b)), err
}
