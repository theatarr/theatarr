package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

// A pinned generation's good check does not stand in for a newer one of the same egress path: after a trust change
// (another VPN set, same transport and check_url) the current generation's check that saw a WAN exit refuses the
// retired generation's requests too.
func TestNewerWANCheckRevokesRetiredProof(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("results"))
	g1 := e.gateway(testPolicy(), nil)
	g1.Activate()
	g1.CheckEgress(context.Background())
	pinned := pinnedTo(g1)
	if err := getBody(pinned, g1, dmm, "http://dmm.test/api"); err != nil {
		t.Fatal(err)
	}
	p2 := testPolicy()
	p2.Egress.VPNIPs = append(p2.Egress.VPNIPs, "45.0.0.2")
	g2 := e.nextGeneration(g1, p2)
	g2.Activate()
	// Not checked yet under the new settings: the retired generation's fresh good check still proves the route.
	if err := getBody(pinned, g1, dmm, "http://dmm.test/api"); err != nil {
		t.Fatalf("pinned request before the current generation checked: %v", err)
	}
	e.advance(time.Second)
	e.setEcho("8.8.8.8")
	g2.CheckEgress(context.Background())
	if err := getBody(context.Background(), g2, dmm, "http://dmm.test/api"); !errors.Is(err, ErrVPNRequired) {
		t.Fatalf("current request should be refused: %v", err)
	}
	if err := getBody(pinned, g1, dmm, "http://dmm.test/api"); !errors.Is(err, ErrVPNRequired) {
		t.Fatalf("retired request reached indexer after latest check saw WAN: %v", err)
	}
	// A later good check under the current settings proves the route again for both.
	e.advance(time.Second)
	e.setEcho(vpnIP)
	g2.CheckEgress(context.Background())
	if err := getBody(pinned, g1, dmm, "http://dmm.test/api"); err != nil {
		t.Fatalf("pinned request after a newer good check: %v", err)
	}
}

// A busy guarded connection a retired generation dialled on its own check is closed when the newer generation's
// check of the same path (under other trust settings) sees a WAN exit.
func TestNewerWANCheckClosesRetiredConnections(t *testing.T) {
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
	old := f.gateway(t, nil)
	old.Activate()
	f.setOther("2001:db8::1")
	ctx := context.Background()
	old.CheckEgress(ctx) // both families proven
	done := make(chan error, 1)
	go func() {
		_, err := getVia(ctx, old, v6onlyIx, "http://v6only.test/api?slow=1")
		done <- err
	}()
	<-started

	p2 := f.policy()
	p2.Egress.VPNIPs = append(p2.Egress.VPNIPs, "45.0.0.2") // a trust change: the check is not shared
	next, err := New(ctx, p2, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: f.e.clock,
		Previous: old, LookupIP: f.lookup, DialContext: f.dial})
	if err != nil {
		t.Fatal(err)
	}
	next.Activate()
	f.e.advance(time.Second)
	f.e.setEcho("8.8.8.8")
	next.CheckEgress(ctx)
	close(release)
	if err := <-done; err == nil {
		t.Error("the retired generation's busy connection outlived a newer check that saw a WAN exit")
	}
	if _, err := getVia(ctx, old, v6onlyIx, "http://v6only.test/api"); refusedReason(err) != ReasonVPN {
		t.Fatalf("retired generation after the newer WAN check: %v, want a VPN refusal", err)
	}
}
