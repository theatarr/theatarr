package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/theatarr/theatarr/internal/policy"
)

// A redirect into a VPN-required indexer must not ride on a request that was not checked for that indexer's path:
// neither the unguarded egress check nor a provider request may carry it there.
func TestRedirectIntoIndexerNeedsItsVPNPath(t *testing.T) {
	t.Run("echo to indexer", func(t *testing.T) {
		e := newEnv(t)
		var hits atomic.Int32
		e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); io.WriteString(w, vpnIP) }))
		e.serve("echo.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://dmm.test/api?q=x", http.StatusFound)
		}))
		g := e.gateway(testPolicy(), nil)
		states := g.CheckEgress(context.Background())
		if hits.Load() != 0 || states[0].OK {
			t.Fatalf("egress check followed a redirect into an indexer: hits=%d states=%+v", hits.Load(), states)
		}
	})
	t.Run("echo stays on its endpoint", func(t *testing.T) {
		e := newEnv(t)
		e.serve("echo.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				http.Redirect(w, r, "/ip", http.StatusFound)
				return
			}
			io.WriteString(w, vpnIP)
		}))
		g := e.gateway(testPolicy(), nil)
		if states := g.CheckEgress(context.Background()); !states[0].OK {
			t.Fatalf("redirect within the check endpoint: %+v", states)
		}
	})
	t.Run("provider to indexer", func(t *testing.T) {
		e := newEnv(t)
		var hits atomic.Int32
		e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); io.WriteString(w, "indexer") }))
		e.serve("rd.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://dmm.test/api?q=x", http.StatusFound)
		}))
		g := e.gateway(testPolicy(), nil)
		// No egress check has run: the indexer itself would be refused.
		_, err := get(g, Target{Class: ClassProvider, Key: "rd"}, "http://rd.test/rest/1.0/user")
		if !errors.Is(err, ErrVPNRequired) || hits.Load() != 0 {
			t.Fatalf("provider redirect reached the indexer: hits=%d err=%v", hits.Load(), err)
		}
		// Even with a good check, the provider's transport is not the indexer's checked route.
		g.CheckEgress(context.Background())
		if _, err := get(g, Target{Class: ClassProvider, Key: "rd"}, "http://rd.test/rest/1.0/user"); !errors.Is(err, ErrVPNRequired) || hits.Load() != 0 {
			t.Fatalf("provider redirect after a good check: hits=%d err=%v", hits.Load(), err)
		}
	})
	t.Run("provider to proxied indexer", func(t *testing.T) {
		e := newEnv(t)
		e.serve("proxy.test", okHandler(vpnIP))
		var hits atomic.Int32
		e.serve("torrentio.test", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
		e.serve("rd.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://torrentio.test/stream", http.StatusFound)
		}))
		p := testPolicy()
		p.Indexers["torrentio"] = &policy.Indexer{Kind: "torrentio", URL: "http://torrentio.test", Privacy: policy.PrivacyPublic,
			Proxy: "http://proxy.test"}
		g := e.gateway(p, nil)
		g.CheckEgress(context.Background())
		_, err := get(g, Target{Class: ClassProvider, Key: "rd"}, "http://rd.test/rest/1.0/user")
		if !errors.Is(err, ErrVPNRequired) || hits.Load() != 0 || e.dialCount("torrentio.test:80") != 0 {
			t.Fatalf("provider redirect went direct to a proxied indexer: hits=%d err=%v", hits.Load(), err)
		}
	})
	t.Run("indexer to another indexer on its path", func(t *testing.T) {
		e := newEnv(t)
		e.serve("tracker.test", okHandler("tracker"))
		e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://tracker.test/api", http.StatusFound)
		}))
		g := e.gateway(testPolicy(), nil)
		g.CheckEgress(context.Background())
		if body, err := get(g, dmm, "http://dmm.test/"); err != nil || body != "tracker" {
			t.Fatalf("guarded redirect on the same checked path: %q %v", body, err)
		}
	})
}

// An egress observation that started earlier must never replace a newer result: check A sees the VPN but answers
// late, check B sees the WAN address and fails; A's completion must not re-enable indexer traffic.
func TestOlderEgressCheckCannotReplaceNewerResult(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("ok"))
	var calls atomic.Int32
	firstArrived, releaseFirst := make(chan struct{}), make(chan struct{})
	e.serve("echo.test", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(firstArrived)
			<-releaseFirst
			io.WriteString(w, vpnIP)
			return
		}
		io.WriteString(w, "203.0.113.9")
	}))
	g := e.gateway(testPolicy(), nil)
	// A later runtime generation shares the path state; its checks must be ordered with the old gateway's.
	next, err := New(context.Background(), testPolicy(), Options{Previous: g, DialContext: e.dial, Timeout: 5 * time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	next.Activate()
	first := make(chan []EgressState, 1)
	go func() { first <- g.checkEgress(context.Background()) }()
	select {
	case <-firstArrived:
	case <-time.After(time.Second):
		t.Fatal("first check did not reach the echo endpoint")
	}
	if states := next.checkEgress(context.Background()); states[0].OK {
		t.Fatalf("newer check: %+v", states)
	}
	close(releaseFirst)
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("first check did not finish")
	}
	for _, gw := range []*Gateway{g, next} {
		if s := gw.paths["direct"].state(); s.OK || s.IP != "203.0.113.9" {
			t.Fatalf("older observation replaced the newer failure: %+v", s)
		}
	}
	if _, err := get(next, dmm, "http://dmm.test/"); !errors.Is(err, ErrVPNRequired) {
		t.Fatalf("indexer allowed after a newer failed check: %v", err)
	}
}

// A malformed redirect Location holding a DB-backed credential must not come back in errors or logs, and the
// redactor masks DB-backed credentials the runtime supplies.
func TestMalformedRedirectDoesNotLeakDBCredential(t *testing.T) {
	const secret = "invented-DB-secret-4471"
	e := newEnv(t)
	e.serve("rd.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/info") {
			io.WriteString(w, "ok")
			return
		}
		w.Header().Set("Location", "/"+secret+"%zz")
		w.WriteHeader(http.StatusFound)
	}))
	e.reg = prometheus.NewRegistry()
	log := slog.New(slog.NewJSONHandler(&e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	g, err := New(context.Background(), testPolicy(), Options{Registerer: e.reg, Logger: log, Version: "test",
		DialContext: e.dial, Timeout: 5 * time.Second, now: e.clock, Secrets: []string{secret}})
	if err != nil {
		t.Fatal(err)
	}
	rd := Target{Class: ClassProvider, Key: "rd"}
	_, err = get(g, rd, "http://rd.test/rest/1.0/torrents")
	if err == nil {
		t.Fatal("want a redirect error")
	}
	var ue interface{ Unwrap() error }
	if !errors.As(err, &ue) {
		t.Fatalf("transport error lost its url.Error shape: %T", err)
	}
	for _, s := range []string{err.Error(), e.logs.String()} {
		if strings.Contains(s, secret) || strings.Contains(s, "%zz") {
			t.Fatalf("server-supplied redirect text leaked: %s", s)
		}
	}
	// The credential in a request path is masked in the request log too.
	if _, err := get(g, rd, "http://rd.test/rest/1.0/"+secret+"/info"); err != nil {
		t.Fatal(err)
	}
	if logs := e.logs.String(); strings.Contains(logs, secret) || !strings.Contains(logs, "/rest/1.0/***/info") {
		t.Fatalf("DB credential not masked in request log:\n%s", logs)
	}
}

// Transport errors keep their cancellation and timeout meaning after their text is replaced.
func TestSafeTransportErrorKeepsCategories(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := newEnv(t)
	e.serve("rd.test", okHandler("ok"))
	g := e.gateway(testPolicy(), nil)
	_, err := g.Get(ctx, Target{Class: ClassProvider, Key: "rd"}, "http://rd.test/rest/1.0/user")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: %v", err)
	}
}
