package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// A guarded direct dial keeps to the address families the egress check proved, without breaking what cannot leave
// the LAN or what only has an address of a proven family: the direct path's check echoes over both
// families, a host whose addresses are all private is dialled as asked, and a public host with no address of a proven
// family is a VPN refusal, not a transport error that backs the indexer off.
func TestGuardedDialFamilies(t *testing.T) {
	e := newEnv(t)
	for _, h := range []string{"dmm.test", "lan.test", "v6only.test"} {
		e.serve(h, okHandler("ok"))
	}
	var mu sync.Mutex
	other := "" // the second echo's answer; "" fails it, as an echo or route of one family does
	e.serve("other.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		io.WriteString(w, other+"\n")
	}))
	networks := map[string]string{}
	addrs := map[string][]netip.Addr{
		"dmm.test":    {netip.MustParseAddr("203.0.113.5")},
		"lan.test":    {netip.MustParseAddr("192.168.1.5"), netip.MustParseAddr("fd00::5")},
		"v6only.test": {netip.MustParseAddr("2001:db8::5")},
		"echo.test":   {netip.MustParseAddr("203.0.113.1"), netip.MustParseAddr("2001:db8::9")},
	}
	p := testPolicy()
	p.Egress.VPNIPs = append(p.Egress.VPNIPs, "2001:db8::1")
	p.Indexers["lan"] = &policy.Indexer{Kind: "torznab", URL: "http://lan.test/api", Privacy: policy.PrivacyPublic}
	p.Indexers["v6only"] = &policy.Indexer{Kind: "torznab", URL: "http://v6only.test/api",
		Privacy: policy.PrivacyPublic}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	g, err := New(context.Background(), p, Options{Logger: quiet, now: e.clock,
		LookupIP: func(_ context.Context, host string) ([]netip.Addr, error) {
			if a, ok := addrs[host]; ok {
				return a, nil
			}
			return nil, fmt.Errorf("no such host %s", host)
		},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			networks[addr] = network
			answer := other
			mu.Unlock()
			if addr == "echo.test:80" && network != "tcp" {
				if answer == "" {
					return nil, errors.New("network is unreachable")
				}
				addr = "other.test:80"
			}
			for h, as := range addrs { // a guarded dial goes to the checked address, not the name
				for _, a := range as {
					if addr == net.JoinHostPort(a.String(), "80") {
						addr = h + ":80"
					}
				}
			}
			return e.dial(ctx, "tcp", addr) // the test servers listen on IPv4 loopback
		}})
	if err != nil {
		t.Fatal(err)
	}
	g.Activate()
	seen := func(addr string) string {
		mu.Lock()
		defer mu.Unlock()
		n := networks[addr]
		delete(networks, addr)
		return n
	}
	lan := Target{Class: ClassIndexer, Key: "lan"}
	v6only := Target{Class: ClassIndexer, Key: "v6only"}

	// A dual-stack VPN: both echoes exit inside the VPN set, so a guarded dial may use either family: an IPv4-only
	// and an IPv6-only host both work.
	mu.Lock()
	other = "2001:db8::1"
	mu.Unlock()
	g.CheckEgress(context.Background())
	if n := seen("echo.test:80"); n != "tcp6" {
		t.Fatalf("second echo used %q", n)
	}
	for _, c := range []struct {
		target  Target
		url     string
		addr    string
		network string
	}{{dmm, "http://dmm.test/", "203.0.113.5:80", "tcp4"}, {lan, "http://lan.test/api", "192.168.1.5:80", "tcp4"},
		{v6only, "http://v6only.test/api", "[2001:db8::5]:80", "tcp6"}} {
		if _, err := get(g, c.target, c.url); err != nil {
			t.Fatalf("%s with both families proven: %v", c.url, err)
		}
		if n := seen(c.addr); n != c.network {
			t.Errorf("%s with both families proven dialled %q", c.addr, n)
		}
	}

	// Only IPv6 proven (the first echo exits over IPv6; IPv4 has no route).
	e.setEcho("2001:db8::1")
	mu.Lock()
	other = ""
	mu.Unlock()
	g.CheckEgress(context.Background())
	if _, err := get(g, lan, "http://lan.test/api?x=1"); err != nil {
		t.Fatalf("LAN indexer with IPv6 proven: %v", err)
	}
	if n := seen("192.168.1.5:80"); n != "tcp4" {
		t.Errorf("LAN indexer dialled %q", n)
	}
	if _, err := get(g, v6only, "http://v6only.test/api?x=1"); err != nil {
		t.Fatalf("IPv6 host with IPv6 proven: %v", err)
	}
	if n := seen("[2001:db8::5]:80"); n != "tcp6" {
		t.Errorf("IPv6 host dialled %q", n)
	}
	var r *Refused
	if _, err := get(g, dmm, "http://dmm.test/x"); !errors.As(err, &r) || r.Reason != ReasonVPN {
		t.Fatalf("IPv4-only host with only IPv6 proven: %v, want a VPN refusal", err)
	}
	if n := seen("203.0.113.5:80"); n != "" {
		t.Errorf("IPv4-only host dialled %q", n)
	}

	// A second echo that exits outside the VPN does not prove its family.
	e.setEcho(vpnIP)
	mu.Lock()
	other = "2001:db8::77"
	mu.Unlock()
	g.CheckEgress(context.Background())
	if _, err := get(g, v6only, "http://v6only.test/api?x=2"); !errors.As(err, &r) || r.Reason != ReasonVPN {
		t.Fatalf("IPv6 host with IPv6 exiting outside the VPN: %v, want a VPN refusal", err)
	}
	if _, err := get(g, dmm, "http://dmm.test/y"); err != nil {
		t.Fatal(err)
	}
	if n := seen("203.0.113.5:80"); n != "tcp4" {
		t.Errorf("IPv4 host with only IPv4 proven dialled %q", n)
	}
}
