package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// A passed egress check carries over to the next gateway generation only under the same check_url and VPN set; and
// vpnOK checks the recorded exit address against the generation's own set.
func TestEgressVerdictNeedsSameTrustConfig(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("ok"))
	old := e.gateway(testPolicy(), nil)
	old.CheckEgress(context.Background())
	if _, err := get(old, dmm, "http://dmm.test/"); err != nil {
		t.Fatalf("checked gateway refused: %v", err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	same, err := New(context.Background(), testPolicy(), Options{Logger: quiet, Previous: old, Generation: 2,
		DialContext: e.dial, now: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	same.Activate()
	if _, err := get(same, dmm, "http://dmm.test/"); err != nil {
		t.Fatalf("unchanged trust config lost the verdict: %v", err)
	}

	for name, change := range map[string]func(*policy.Egress){
		"vpn_ips":   func(eg *policy.Egress) { eg.VPNIPs = []string{"45.0.0.2"} },
		"check_url": func(eg *policy.Egress) { eg.CheckURL = "http://echo2.test/" },
		"vpn_cidrs": func(eg *policy.Egress) { eg.VPNCIDRs = []string{"10.0.0.0/8"} },
	} {
		p := testPolicy()
		change(p.Egress)
		next, err := New(context.Background(), p, Options{Logger: quiet, Previous: old, Generation: 3,
			DialContext: e.dial, now: e.clock})
		if err != nil {
			t.Fatal(err)
		}
		next.Activate()
		var r *Refused
		if _, err := get(next, dmm, "http://dmm.test/"); !errors.As(err, &r) || r.Reason != ReasonVPN {
			t.Fatalf("%s changed: guarded request not refused: %v", name, err)
		}
		if next.paths["direct"].check == old.paths["direct"].check {
			t.Fatalf("%s changed: check state still shared with the old generation", name)
		}
	}

	same.Activate()
	// A good verdict whose address the generation's set does not hold is refused.
	same.paths["direct"].check.mu.Lock()
	same.paths["direct"].check.state.IP = "203.0.113.9"
	same.paths["direct"].check.mu.Unlock()
	var r *Refused
	if _, err := get(same, dmm, "http://dmm.test/"); !errors.As(err, &r) || r.Reason != ReasonVPN {
		t.Fatalf("exit address outside the VPN set allowed: %v", err)
	}
}

// A guarded direct dial uses only the address family the egress check went out on; unguarded dials stay dual-stack.
func TestGuardedDialUsesCheckedFamily(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("ok"))
	e.serve("tmdb.test", okHandler("ok"))
	var mu sync.Mutex
	networks := map[string]string{}
	p := testPolicy()
	p.Egress.VPNIPs = append(p.Egress.VPNIPs, "2001:db8::1")
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	g, err := New(context.Background(), p, Options{Logger: quiet, now: e.clock,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			networks[addr] = network
			mu.Unlock()
			return e.dial(ctx, network, addr)
		}})
	if err != nil {
		t.Fatal(err)
	}
	seen := func(addr string) string { mu.Lock(); defer mu.Unlock(); return networks[addr] }

	g.CheckEgress(context.Background())
	if _, err := get(g, dmm, "http://dmm.test/"); err != nil {
		t.Fatal(err)
	}
	if n := seen("dmm.test:80"); n != "tcp4" {
		t.Fatalf("guarded dial after an IPv4 check used %q", n)
	}
	if _, err := get(g, Target{Class: ClassMetadata, Key: "tmdb"}, "http://tmdb.test/3/x"); err != nil {
		t.Fatal(err)
	}
	if n := seen("tmdb.test:80"); n != "tcp" {
		t.Fatalf("unguarded dial used %q", n)
	}

	e.setEcho("2001:db8::1")
	g.CheckEgress(context.Background())
	g.Get(context.Background(), dmm, "http://dmm.test/again") // the IPv6 dial to the IPv4 test server fails
	if n := seen("dmm.test:80"); n != "tcp6" {
		t.Fatalf("guarded dial after an IPv6 check used %q", n)
	}
}
