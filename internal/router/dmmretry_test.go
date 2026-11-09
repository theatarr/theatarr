package router

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// Our add finds a release uncached on RD (the version's only provider) and deletes it; DMM's newly cached RSS list then lists
// it on RD. That listing reopens our verdict once: the next route asks RD again (the normal cached-only add and
// check). The retry misses too, so the same listing read again changes nothing: no third request.
func TestDMMListingRetriesUncachedOnce(t *testing.T) {
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	t0 := e.clock
	listed := t0.Add(15 * time.Minute)
	const title = "Movie.2024.2160p.WEB.H265-GRP"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`+
			`<torznab:response offset="0" total="1"/><item><title>%s</title><guid>d1</guid><size>%d</size><pubDate>%s</pubDate>`+
			`<torznab:attr name="infohash" value="%s"/><torznab:attr name="seeders" value="1"/></item></channel></rss>`,
			title, 10<<30, listed.Format(time.RFC1123), h("a"))
	}))
	defer srv.Close()
	t.Setenv("THEATARR_TEST_DMM_KEY", "dmm-test-key")
	e.pol.Indexers["dmm"] = &policy.Indexer{Kind: "dmm", URL: srv.URL + "/api/torznab/rd/cached", APIKeyEnv: "THEATARR_TEST_DMM_KEY",
		Privacy: policy.PrivacyPublic}
	e.pol.Versions["movie"].Indexers = []string{"dmm"}
	e.pol.Versions["movie"].Providers = []string{"rd"}
	idx, err := indexer.New(e.pol, &searchGateway{}, indexer.Options{CacheTTL: -1, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	idx.SetEvidence(indexer.NewEvidence(indexer.EvidenceConfig{Store: e.st, Policy: e.pol, Now: func() time.Time { return e.clock },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	route := func() *Result {
		t.Helper()
		c := cand(h("a"), title)
		c.Release.Indexer, c.Release.Kind = "dmm", "dmm"
		// A grab: the test movie has no availability for the freshness check an RSS route runs first.
		res, err := e.r.Route(e.ctx, grabReq(c))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	rss := func() {
		t.Helper()
		res, err := idx.Search(e.ctx, "dmm", indexer.Query{Type: indexer.QueryRSS, RSSLimit: 100})
		if err != nil || len(res.Releases) != 1 || !res.Releases[0].PublishDate.Equal(listed) {
			t.Fatalf("rss: %v %+v", err, res)
		}
	}
	const adds, deletes = "rd.test POST /rest/1.0/torrents/addMagnet", "rd.test DELETE /rest/1.0/torrents/delete/"

	// Our add: uncached on RD, deleted; RD was the only provider, so the verdict is the all-provider miss (router:miss).
	res := route()
	if f.count(adds) != 1 || f.count(deletes) != 1 {
		t.Fatalf("first route: adds %d, deletes %d: %s", f.count(adds), f.count(deletes), steps(res))
	}
	// DMM lists it cached on RD 15 minutes later; the next RSS sync reads it, then routes it: RD is asked again.
	e.clock = t0.Add(20 * time.Minute)
	rss()
	if st, _ := e.r.cacheState(e.ctx, h("a"), "rd", e.clock); st != "cached" {
		t.Fatalf("rd state after the listing %q", st)
	}
	res = route()
	if f.count(adds) != 2 || f.count(deletes) != 2 {
		t.Fatalf("retry: adds %d, deletes %d: %s", f.count(adds), f.count(deletes), steps(res))
	}
	// RD was the only provider: the retry's miss is the all-provider miss, newer than the listing.
	if st, _ := e.r.cacheState(e.ctx, h("a"), "rd", e.clock); st != "miss" {
		t.Fatalf("rd state after the retry %q", st)
	}
	// The same listing on the next sync: a known miss, no third request.
	e.clock = t0.Add(35 * time.Minute)
	before := f.requests()
	rss()
	res = route()
	if f.requests() != before || !hasStep(res, "known_miss") {
		t.Fatalf("same listing again: %d requests, %s", f.requests()-before, steps(res))
	}
}
