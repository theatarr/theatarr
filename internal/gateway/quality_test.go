package gateway

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestQualityArchiveBudgetAndDestinations(t *testing.T) {
	e, g, dns := probeEnv(t, nil)
	dns["guides.test"] = []netip.Addr{netip.MustParseAddr("203.0.113.7")}
	e.serve("203.0.113.7", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("archive")) }))
	for i := 0; i < 2; i++ {
		raw, err := g.QualityArchive(context.Background(), testPolicy(), "http://guides.test/pinned.tar.gz")
		if err != nil || string(raw) != "archive" {
			t.Fatalf("archive %d: %q %v", i, raw, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := g.QualityArchive(ctx, testPolicy(), "http://guides.test/pinned.tar.gz"); err == nil {
		t.Fatal("new fetch reset shared budget")
	}
	// An ordinary source probe has its own tokens, independent of the archive budget.
	pg, err := g.Probe(context.Background(), candidate("http://guides.test/api", nil), ProbeOptions{Target: cand, Candidate: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := get(pg, cand, "http://guides.test/api"); err != nil {
		t.Fatal("archive drained probe budget", err)
	}
	e.advance(time.Minute)
	g.CheckEgress(context.Background())
	if _, err := g.QualityArchive(context.Background(), testPolicy(), "http://127.0.0.1:9876/pinned.tar.gz"); refusedReason(err) != ReasonHost {
		t.Fatal("private archive host accepted", err)
	}
	if e.dialCount("127.0.0.1:9876") != 0 {
		t.Fatal("private host dialed")
	}
}

// The tracked TRaSH source asks for the branch's newest commit id with the sha media type, through the archive
// budget and destination checks; an answer that is not a commit id is an error.
func TestQualityCommitShaMediaTypeAndBudget(t *testing.T) {
	e, g, dns := probeEnv(t, nil)
	dns["api.github.test"] = []netip.Addr{netip.MustParseAddr("203.0.113.8")}
	sha := strings.Repeat("ab", 20)
	answer := sha + "\n"
	e.serve("203.0.113.8", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github.sha" || r.URL.Path != "/repos/TRaSH-Guides/Guides/commits/master" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(answer))
	}))
	const u = "http://api.github.test/repos/TRaSH-Guides/Guides/commits/master"
	got, err := g.QualityCommit(context.Background(), testPolicy(), u)
	if err != nil || got != sha {
		t.Fatalf("commit: %q %v", got, err)
	}
	answer = "<html>"
	if _, err := g.QualityCommit(context.Background(), testPolicy(), u); err == nil {
		t.Fatal("non-commit answer accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := g.QualityArchive(ctx, testPolicy(), "http://api.github.test/pinned.tar.gz"); err == nil {
		t.Fatal("commit lookups are not charged to the archive budget")
	}
	e.advance(time.Minute)
	g.CheckEgress(context.Background())
	if _, err := g.QualityCommit(context.Background(), testPolicy(), "http://127.0.0.1:9876/repos/a/b/commits/master"); refusedReason(err) != ReasonHost {
		t.Fatal("private commit host accepted", err)
	}
}

// Quality sync fetches follow no redirect: GitHub's commits API and codeload answer directly, and a hop would
// otherwise reach any host of the live allowlist (an arr or Plex on the LAN) before the sync rejects the answer.
func TestQualityFetchFollowsNoRedirect(t *testing.T) {
	e, g, dns := probeEnv(t, nil)
	dns["guides.test"] = []netip.Addr{netip.MustParseAddr("203.0.113.7")}
	dns["other.test"] = []netip.Addr{netip.MustParseAddr("203.0.113.9")}
	var hits atomic.Int32
	e.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	e.serve("other.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	target := "http://dmm.test/api"
	e.serve("203.0.113.7", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	if _, err := g.QualityArchive(context.Background(), testPolicy(), "http://guides.test/pinned.tar.gz"); err == nil {
		t.Fatal("archive fetch followed a redirect to a live allowlisted host")
	}
	e.advance(time.Minute)
	g.CheckEgress(context.Background())
	target = "http://other.test/x"
	if _, err := g.QualityCommit(context.Background(), testPolicy(), "http://guides.test/repos/a/b/commits/master"); err == nil {
		t.Fatal("commit lookup followed a redirect")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("redirect target reached %d times", n)
	}
}
