package gateway

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

// A Target with Wait gets its answer although the headers come later than the usual header timeout (a FlareSolverr
// solve), and a Wait that runs out is not counted as the target's failure.
func TestTargetWaitOutlastsHeaderTimeout(t *testing.T) {
	old := responseHeaderTimeout
	responseHeaderTimeout = 100 * time.Millisecond
	t.Cleanup(func() { responseHeaderTimeout = old })
	e := newEnv(t)
	e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		io.WriteString(w, "ok")
	}))
	g := e.gateway(testPolicy(), nil)
	g.CheckEgress(context.Background())
	if _, err := get(g, dmm, "http://dmm.test/v1"); err == nil {
		t.Fatal("a slow answer passed the 100 ms header timeout without Wait")
	}
	g.state.success(context.Background(), dmm.String(), g.state.mark())
	slow := dmm
	slow.Wait = 5 * time.Second
	body, err := get(g, slow, "http://dmm.test/v1")
	if err != nil || body != "ok" {
		t.Fatalf("with Wait: %q %v", body, err)
	}
	slow.Wait = 150 * time.Millisecond
	for range failuresBeforeBackoff + 1 {
		if _, err := get(g, slow, "http://dmm.test/v1"); err == nil {
			t.Fatal("a Wait of 150 ms passed a 400 ms answer")
		}
	}
	if n := g.state.failures(dmm.String()); n != 0 {
		t.Errorf("%d failures counted for Wait timeouts", n)
	}
}
