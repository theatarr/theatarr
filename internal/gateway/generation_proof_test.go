package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

// An intermediate generation's WAN observation is not lost when a third generation activates before checking: the
// request pinned to the first generation's good proof stays refused.
func TestIntermediateFailedProofSurvivesNextGeneration(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("results"))
	g1 := e.gateway(testPolicy(), nil)
	g1.Activate()
	g1.CheckEgress(context.Background())
	pinned := pinnedTo(g1)
	p2 := testPolicy()
	p2.Egress.VPNIPs = append(p2.Egress.VPNIPs, "45.0.0.2")
	g2 := e.nextGeneration(g1, p2)
	g2.Activate()
	e.advance(time.Second)
	e.setEcho("8.8.8.8")
	g2.CheckEgress(context.Background())
	if err := getBody(pinned, g1, dmm, "http://dmm.test/api"); !errors.Is(err, ErrVPNRequired) {
		t.Fatalf("expected refusal after middle generation observed WAN: %v", err)
	}
	p3 := testPolicy()
	p3.Egress.VPNIPs = append(p3.Egress.VPNIPs, "45.0.0.3")
	g3 := e.nextGeneration(g2, p3)
	g3.Activate()
	if err := getBody(pinned, g1, dmm, "http://dmm.test/api"); !errors.Is(err, ErrVPNRequired) {
		t.Fatalf("old pinned request reached indexer despite latest observation being WAN: %v", err)
	}
}

// A dial that started on a retired generation's proof is judged again, under the rule in force at registration,
// after a generation activated during the dial observed a WAN exit.
func TestDialRechecksGenerationPublishedWhileConnecting(t *testing.T) {
	f := newFamNet(t)
	f.host("dmm.test", okHandler("results"), "203.0.113.5")
	started, resume := make(chan struct{}), make(chan struct{})
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == "203.0.113.5:80" {
			close(started)
			<-resume
		}
		return f.dial(ctx, network, addr)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	old, err := New(context.Background(), f.policy(), Options{Logger: quiet, now: f.e.clock, LookupIP: f.lookup, DialContext: dial})
	if err != nil {
		t.Fatal(err)
	}
	old.CheckEgress(context.Background())
	done := make(chan error, 1)
	go func() { done <- getBody(pinnedTo(old), old, dmm, "http://dmm.test/api") }()
	<-started
	p2 := f.policy()
	p2.Egress.VPNIPs = append(p2.Egress.VPNIPs, "45.0.0.2")
	next, err := New(context.Background(), p2, Options{Logger: quiet, now: f.e.clock, Previous: old, LookupIP: f.lookup, DialContext: f.dial})
	if err != nil {
		close(resume)
		<-done
		t.Fatal(err)
	}
	next.Activate()
	f.e.advance(time.Second)
	f.e.setEcho("8.8.8.8")
	next.CheckEgress(context.Background())
	close(resume)
	if err := <-done; !errors.Is(err, ErrVPNRequired) {
		t.Fatalf("in-progress dial sent indexer request after new generation observed WAN: %v", err)
	}
}

// An older generation's check that observes the path after the current one (an operation pinned to it ran a check)
// is authoritative too: its WAN observation refuses the current generation's requests and closes the guarded
// connections dialled on the current proof.
func TestOlderGenerationWANObservationGovernsCurrent(t *testing.T) {
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
	old.CheckEgress(ctx)
	p2 := f.policy()
	p2.Egress.VPNIPs = append(p2.Egress.VPNIPs, "45.0.0.2") // a trust change: the check is not shared
	next, err := New(ctx, p2, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: f.e.clock,
		Previous: old, LookupIP: f.lookup, DialContext: f.dial})
	if err != nil {
		t.Fatal(err)
	}
	next.Activate()
	f.e.advance(time.Second)
	next.CheckEgress(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := getVia(ctx, next, v6onlyIx, "http://v6only.test/api?slow=1")
		done <- err
	}()
	<-started
	f.e.advance(time.Second)
	f.e.setEcho("8.8.8.8")
	old.CheckEgress(pinnedTo(old)) // the retired generation's own check, as a pinned operation runs it
	close(release)
	if err := <-done; err == nil {
		t.Error("the current generation's busy connection outlived an older generation's newer WAN check")
	}
	if _, err := getVia(ctx, next, v6onlyIx, "http://v6only.test/api"); refusedReason(err) != ReasonVPN {
		t.Fatalf("current generation after the older generation's WAN check: %v, want a VPN refusal", err)
	}
	// The current generation's own newer good check proves the route again.
	f.e.advance(time.Second)
	f.e.setEcho(vpnIP)
	next.CheckEgress(ctx)
	if _, err := getVia(ctx, next, v6onlyIx, "http://v6only.test/api"); err != nil {
		t.Fatalf("after a newer good check: %v", err)
	}
}
