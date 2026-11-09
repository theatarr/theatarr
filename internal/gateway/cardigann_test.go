package gateway

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// A cardigann indexer's requests come back unfollowed (the engine follows each hop itself, through Do again), keep
// the engine's User-Agent, and may reach the indexer's configured hosts besides its URL.
func TestCardigannTargetNoFollowUserAgentAndHosts(t *testing.T) {
	e := newEnv(t)
	var ua string
	e.serve("site.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		http.Redirect(w, r, "http://api.site.test/list", http.StatusFound)
	}))
	e.serve("api.site.test", okHandler("list"))
	e.serve("other.test", okHandler("other"))
	p := testPolicy()
	p.Indexers["site"] = &policy.Indexer{Kind: "cardigann", Definition: "site", URL: "http://site.test/", Privacy: policy.PrivacyPublic,
		Hosts: []string{"api.site.test"}}
	g := e.gateway(p, nil)
	g.CheckEgress(context.Background())
	site := Target{Class: ClassIndexer, Key: "site", NoFollow: true, UserAgent: "Mozilla/5.0 (test)"}
	req, _ := http.NewRequest(http.MethodGet, "http://site.test/search", nil)
	resp, err := g.Do(context.Background(), site, req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "http://api.site.test/list" {
		t.Fatalf("status %d location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if ua != "Mozilla/5.0 (test)" {
		t.Errorf("User-Agent %q", ua)
	}
	if e.dialCount("api.site.test:80") != 0 {
		t.Fatal("the redirect was followed")
	}
	if body, err := get(g, site, "http://api.site.test/list"); err != nil || body != "list" {
		t.Fatalf("configured host: %q %v", body, err)
	}
	if _, err := get(g, site, "http://other.test/"); refusedReason(err) != ReasonHost {
		t.Fatalf("unlisted host: %v", err)
	}
	// without NoFollow the gateway follows as before, with its own User-Agent
	resp, err = g.Get(context.Background(), Target{Class: ClassIndexer, Key: "site"}, "http://site.test/search")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "list" || ua != "theatarr/test" {
		t.Fatalf("followed body %q, User-Agent %q", b, ua)
	}
	// the VPN guard still applies
	e.advance(10 * time.Minute)
	if _, err := get(g, site, "http://site.test/search"); refusedReason(err) != ReasonVPN {
		t.Fatalf("stale VPN check: %v", err)
	}
}

// A probe of a cardigann candidate reaches its configured public hosts too, but not a private address the live
// allowlist lacks.
func TestProbeAllowsCardigannHosts(t *testing.T) {
	e, g, dns := probeEnv(t, nil)
	dns["cand.test"] = []netip.Addr{netip.MustParseAddr("203.0.113.20")}
	dns["api.cand.test"] = []netip.Addr{netip.MustParseAddr("203.0.113.21")}
	e.serve("203.0.113.21", okHandler("api"))
	pg, err := g.Probe(context.Background(), candidate("http://cand.test/", func(ix *policy.Indexer) {
		ix.Kind, ix.Definition, ix.Hosts = "cardigann", "cand", []string{"api.cand.test"}
	}), ProbeOptions{Target: cand, Candidate: true})
	if err != nil {
		t.Fatal(err)
	}
	if body, err := get(pg, cand, "http://api.cand.test/x"); err != nil || body != "api" {
		t.Fatalf("configured host: %q %v", body, err)
	}
	_, err = g.Probe(context.Background(), candidate("http://cand.test/", func(ix *policy.Indexer) {
		ix.Kind, ix.Definition, ix.Hosts = "cardigann", "cand", []string{"127.0.0.1:9876"}
	}), ProbeOptions{Target: cand, Candidate: true})
	if refusedReason(err) != ReasonHost {
		t.Fatalf("private host: %v", err)
	}
}

// The definitions fetch has a budget of its own: it does not drain the quality sync's.
func TestCardigannDefinitionsFetchBudget(t *testing.T) {
	e, g, dns := probeEnv(t, nil)
	dns["api.github.test"] = []netip.Addr{netip.MustParseAddr("203.0.113.8")}
	sha := strings.Repeat("cd", 20)
	e.serve("203.0.113.8", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "application/vnd.github.sha" {
			w.Write([]byte(sha))
			return
		}
		w.Write([]byte("archive"))
	}))
	const u = "http://api.github.test/repos/Prowlarr/Indexers/commits/master"
	for i := 0; i < 2; i++ {
		if got, err := g.CardigannCommit(context.Background(), testPolicy(), u); err != nil || got != sha {
			t.Fatalf("commit %d: %q %v", i, got, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := g.CardigannArchive(ctx, testPolicy(), "http://api.github.test/a.tar.gz", 1<<20); err == nil {
		t.Fatal("the definitions budget was not charged")
	}
	if raw, err := g.QualityArchive(context.Background(), testPolicy(), "http://api.github.test/a.tar.gz"); err != nil || string(raw) != "archive" {
		t.Fatalf("the quality budget was drained: %q %v", raw, err)
	}
}
