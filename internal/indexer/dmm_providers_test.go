package indexer

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// A DMM path may name several providers (rd, ad, tb), each once, any order, with /cached last. The RSS sync asks the
// path's rd and ad providers only (DMM serves its newly cached list there; with tb in the path it does not).
func TestDMMProviderPaths(t *testing.T) {
	const host = "https://debridmediamanager.com/api/torznab/"
	for _, c := range []struct {
		path      string
		providers []string
		rss       string // "" when the indexer has no RSS feed
	}{
		{"rd/ad/tb/cached", []string{"rd", "ad", "tb"}, host + "rd/ad/cached/api"},
		{"rd/ad/tb/cached/api", []string{"rd", "ad", "tb"}, host + "rd/ad/cached/api"},
		{"tb/rd/cached", []string{"tb", "rd"}, host + "rd/cached/api"},
		{"tb/rd/cached/", []string{"tb", "rd"}, host + "rd/cached/api"},
		{"ad/rd/cached", []string{"ad", "rd"}, host + "ad/rd/cached/api"},
		{"rd/cached", []string{"rd"}, host + "rd/cached/api"},
		{"rd/cached/api", []string{"rd"}, host + "rd/cached/api"},
		{"cached", nil, host + "cached/api"},
		{"cached/api", nil, host + "cached/api"},
		{"tb/cached", []string{"tb"}, ""},
		{"tb/cached/api", []string{"tb"}, ""},
	} {
		ix := &policy.Indexer{Kind: "dmm", URL: host + c.path, Privacy: policy.PrivacyPublic}
		tz, err := newTorznab(base{key: "dmm", kind: "dmm", ix: ix}, true, time.Now)
		if err != nil {
			t.Errorf("%s: %v", c.path, err)
			continue
		}
		if !slices.Equal(tz.providers, c.providers) {
			t.Errorf("%s: providers %v, want %v", c.path, tz.providers, c.providers)
		}
		rss := ""
		if tz.rssEndpoint != nil {
			rss = tz.rssEndpoint.String()
		}
		if rss != c.rss {
			t.Errorf("%s: RSS endpoint %q, want %q", c.path, rss, c.rss)
		}
		if got := tz.Supports(Query{Type: QueryRSS}); got != (c.rss != "") {
			t.Errorf("%s: Supports(RSS) %v", c.path, got)
		}
	}
	for _, path := range []string{"rd/rd/cached", "cached/rd", "rd/dl/cached", "rd/ad", "rd/ad/tb/tb/cached",
		"rd/ad/cachedapi", "cachedapi"} {
		ix := &policy.Indexer{Kind: "dmm", URL: host + path, Privacy: policy.PrivacyPublic}
		_, err := newTorznab(base{key: "dmm", kind: "dmm", ix: ix}, true, time.Now)
		if err == nil {
			t.Errorf("%s accepted", path)
		} else if !strings.Contains(err.Error(), "rd/ad/tb/cached") {
			t.Errorf("%s: the refusal does not show the multi-provider form: %v", path, err)
		}
	}
}

// dmmItem is one DMM item with an optional cachedon attribute.
type dmmItem struct {
	hash, cachedOn, pub string
}

func dmmXML(items ...dmmItem) string { return dmmXMLTotal(len(items), items...) }

// dmmXMLTotal is a DMM page reporting total results in all.
func dmmXMLTotal(total int, items ...dmmItem) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`)
	fmt.Fprintf(&b, `<torznab:response offset="0" total="%d"/>`, total)
	for i, it := range items {
		pub := it.pub
		if pub == "" {
			pub = "Fri, 06 Nov 2026 10:00:00 +0000"
		}
		fmt.Fprintf(&b, `<item><title>DMM.Release.%d.2160p {imdb-tt0133093}</title><guid>d%s</guid><size>%d</size>`+
			`<pubDate>%s</pubDate><torznab:attr name="infohash" value="%s"/><torznab:attr name="seeders" value="100"/>`,
			i, it.hash[:4], 1000+i, pub, it.hash)
		if it.cachedOn != "" {
			fmt.Fprintf(&b, `<torznab:attr name="cachedon" value="%s"/>`, it.cachedOn)
		}
		b.WriteString(`</item>`)
	}
	b.WriteString(`</channel></rss>`)
	return b.String()
}

// On a path naming several providers each item's cached_on is its own cachedon attribute, kept to the providers the
// path names; an item without the attribute has none. A search keeps the full path.
func TestDMMMultiProviderCachedOn(t *testing.T) {
	n := newNet(t)
	rec := &recorder{}
	n.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		fmt.Fprint(w, dmmXML(
			dmmItem{hash: hx("1"), cachedOn: "rd,tb"},
			dmmItem{hash: hx("2"), cachedOn: "tb"},
			dmmItem{hash: hx("3")},
			dmmItem{hash: hx("4"), cachedOn: "ad,tb"},
			dmmItem{hash: hx("5"), cachedOn: "ad"},
		))
	}))
	p := testPolicy()
	p.Indexers["dmm"].URL = "http://dmm.test/api/torznab/rd/tb/cached"
	h := newHarness(t, n, p, false)
	res := search(t, h, "dmm", Query{Type: QueryMovie, IMDb: "tt0133093", Title: "The Matrix"})
	if reqs := rec.all(); len(reqs) != 1 || reqs[0].URL.Path != "/api/torznab/rd/tb/cached/api" {
		t.Fatalf("requests %v", reqs)
	}
	want := map[string]string{hx("1"): "rd,tb", hx("2"): "tb", hx("3"): "", hx("4"): "tb", hx("5"): ""}
	if len(res.Releases) != len(want) {
		t.Fatalf("releases %d", len(res.Releases))
	}
	for _, r := range res.Releases {
		got, ok := r.Extras["cached_on"]
		if got != want[r.InfoHash] || ok != (want[r.InfoHash] != "") {
			t.Errorf("%s: cached_on %q (set %v), want %q", r.InfoHash[:4], got, ok, want[r.InfoHash])
		}
	}
}

// A single-provider path tags every item with its provider and plain /cached with "rd,ad", whatever the item says.
func TestDMMSingleProviderCachedOnIsThePath(t *testing.T) {
	for path, want := range map[string][]string{
		"tb/cached": {"1111=tb", "2222=tb"},
		"cached":    {"1111=rd,ad", "2222=rd,ad"},
	} {
		n := newNet(t)
		n.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, dmmXML(dmmItem{hash: hx("1"), cachedOn: "rd"}, dmmItem{hash: hx("2")}))
		}))
		p := testPolicy()
		p.Indexers["dmm"].URL = "http://dmm.test/api/torznab/" + path
		h := newHarness(t, n, p, false)
		res := search(t, h, "dmm", Query{Type: QueryMovie, IMDb: "tt0133093", Title: "The Matrix"})
		var got []string
		for _, r := range res.Releases {
			got = append(got, r.InfoHash[:4]+"="+r.Extras["cached_on"])
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: releases %v, want %v", path, got, want)
		}
	}
}

// The RSS sync on a DMM path asks the rd/ad endpoint for a full page of 100 (DMM's default is 10), whatever page_cap
// bounds a search; its items' cached_on follows that endpoint's provider count, and recordFeed stores the evidence.
func TestDMMRSSUsesRdAdEndpoint(t *testing.T) {
	n := newNet(t)
	rec := &recorder{}
	n.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		switch r.URL.Path {
		case "/api/torznab/rd/ad/cached/api":
			fmt.Fprint(w, dmmXML(
				dmmItem{hash: hx("1"), cachedOn: "rd,ad"},
				dmmItem{hash: hx("2"), cachedOn: "ad"},
				dmmItem{hash: hx("3")},
				dmmItem{hash: hx("5"), cachedOn: "rd,tb"},
			))
		case "/api/torznab/rd/cached/api":
			fmt.Fprint(w, dmmXML(dmmItem{hash: hx("4"), cachedOn: "ad"}))
		default:
			http.NotFound(w, r)
		}
	}))
	p := evidencePolicy()
	p.Indexers["dmm"].URL = "http://dmm.test/api/torznab/tb/rd/ad/cached"
	p.Indexers["dmm"].PageCap = 50
	p.Indexers["dmm-ad"].URL = "http://dmm.test/api/torznab/tb/rd/cached"
	p.Indexers["dmm-tb"] = &policy.Indexer{Kind: "dmm", URL: "http://dmm.test/api/torznab/tb/cached",
		APIKeyEnv: dmmKeyEnv, Privacy: policy.PrivacyPublic, Budget: "dmm-key"}
	h := newHarness(t, n, p, false)
	st := openStore(t)
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: p, Now: h.clock}))

	if why := h.svc.RSSProblem(context.Background(), "dmm-tb"); why == "" {
		t.Error("a tb-only DMM path offers an RSS feed")
	}
	if why := h.svc.RSSProblem(context.Background(), "dmm"); why != "" {
		t.Errorf("dmm RSS: %s", why)
	}

	res := search(t, h, "dmm", Query{Type: QueryRSS, Categories: []int{2000}, RSSLimit: 100})
	reqs := rec.all()
	if len(reqs) != 1 {
		t.Fatalf("requests %d", len(reqs))
	}
	q := reqs[0].URL.Query()
	if reqs[0].URL.Path != "/api/torznab/rd/ad/cached/api" || q.Get("t") != "search" || q.Get("limit") != "100" ||
		q.Get("offset") != "" || q.Get("q") != "" || q.Get("apikey") != dmmKey {
		t.Fatalf("RSS request %s %v", reqs[0].URL.Path, q)
	}
	// tb is in the indexer's path but not in the RSS endpoint's: an item's tb is not kept.
	want := map[string]string{hx("1"): "rd,ad", hx("2"): "ad", hx("3"): "", hx("5"): "rd"}
	if len(res.Releases) != 4 || len(res.RSSPage) != 4 {
		t.Fatalf("releases %d page %d", len(res.Releases), len(res.RSSPage))
	}
	for _, r := range res.Releases {
		if r.Extras["cached_on"] != want[r.InfoHash] {
			t.Errorf("%s: cached_on %q, want %q", r.InfoHash[:4], r.Extras["cached_on"], want[r.InfoHash])
		}
	}

	// A second page, and a request that names no limit, still send one.
	search(t, h, "dmm", Query{Type: QueryRSS, Categories: []int{2000}, RSSOffset: 100, RSSLimit: 100})
	search(t, h, "dmm", Query{Type: QueryRSS, Categories: []int{2000}})
	reqs = rec.all()
	if q := reqs[1].URL.Query(); q.Get("offset") != "100" || q.Get("limit") != "100" {
		t.Errorf("second page %v", q)
	}
	if q := reqs[2].URL.Query(); q.Get("limit") != "100" {
		t.Errorf("no RSSLimit: %v", q)
	}

	// One rd/ad provider in the path: the RSS endpoint names one, so cached_on is that provider.
	one := search(t, h, "dmm-ad", Query{Type: QueryRSS, Categories: []int{2000}, RSSLimit: 100})
	if reqs = rec.all(); reqs[len(reqs)-1].URL.Path != "/api/torznab/rd/cached/api" {
		t.Fatalf("dmm-ad RSS path %s", reqs[len(reqs)-1].URL.Path)
	}
	if len(one.Releases) != 1 || one.Releases[0].Extras["cached_on"] != "rd" {
		t.Fatalf("dmm-ad RSS releases %+v", one.Releases)
	}

	wantRows := map[string]string{"1111/rd": "cached feed:dmm", "1111/ad": "cached feed:dmm",
		"2222/ad": "cached feed:dmm", "4444/rd": "cached feed:dmm-ad", "5555/rd": "cached feed:dmm"}
	if got := cacheRows(t, st); fmt.Sprint(got) != fmt.Sprint(wantRows) {
		t.Fatalf("provider_cache %v, want %v", got, wantRows)
	}
}

// A release that comes back on a later page with more providers (DMM's TorBox probe answers per request) keeps one
// copy, with the union of the providers in path order.
func TestDMMPagingMergesCachedOn(t *testing.T) {
	n := newNet(t)
	rec := &recorder{}
	n.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		if r.URL.Query().Get("offset") == "" {
			fmt.Fprint(w, dmmXMLTotal(3, dmmItem{hash: hx("1"), cachedOn: "rd"}))
			return
		}
		fmt.Fprint(w, dmmXMLTotal(3, dmmItem{hash: hx("1"), cachedOn: "tb,rd"}, dmmItem{hash: hx("2"), cachedOn: "tb"}))
	}))
	p := testPolicy()
	p.Indexers["dmm"].URL = "http://dmm.test/api/torznab/rd/tb/cached"
	h := newHarness(t, n, p, false)
	res := search(t, h, "dmm", Query{Type: QueryMovie, IMDb: "tt0133093", Title: "The Matrix"})
	if len(rec.all()) != 2 {
		t.Fatalf("requests %d", len(rec.all()))
	}
	var got []string
	for _, r := range res.Releases {
		got = append(got, r.InfoHash[:4]+"="+r.Extras["cached_on"])
	}
	if want := []string{"1111=rd,tb", "2222=tb"}; !slices.Equal(got, want) {
		t.Fatalf("releases %v, want %v", got, want)
	}
}

// A DMM path naming rd and ad has one newly cached list per provider, which the RSS sync reads with Query.RSSFeed:
// DMM lists a hash once on the combined list, at its first listing on either provider. A hash first listed on AD and
// later cached on RD shows on the RD list with the later pubDate, which reopens our older RD verdict. Paths with one
// rd/ad provider keep their single feed.
func TestDMMRSSPerProviderLists(t *testing.T) {
	const host = "http://dmm.test/api/torznab/"
	for path, want := range map[string][]string{"tb/rd/ad/cached": {"rd", "ad"}, "ad/rd/cached": {"ad", "rd"},
		"rd/cached": nil, "tb/rd/cached": nil, "cached": nil, "tb/cached": nil} {
		ix := &policy.Indexer{Kind: "dmm", URL: host + path, Privacy: policy.PrivacyPublic}
		tz, err := newTorznab(base{key: "dmm", kind: "dmm", ix: ix}, true, time.Now)
		if err != nil || !slices.Equal(tz.RSSFeeds(), want) {
			t.Errorf("%s: feeds %v (%v), want %v", path, tz.RSSFeeds(), err, want)
		}
	}

	n := newNet(t)
	rec := &recorder{}
	n.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		switch r.URL.Path {
		case "/api/torznab/ad/cached/api":
			fmt.Fprint(w, dmmXML(dmmItem{hash: hx("1"), pub: "Mon, 26 Oct 2026 10:00:00 +0000"}))
		case "/api/torznab/rd/cached/api":
			fmt.Fprint(w, dmmXML(dmmItem{hash: hx("1"), pub: "Mon, 26 Oct 2026 12:10:00 +0000"}))
		default:
			http.NotFound(w, r)
		}
	}))
	p := evidencePolicy()
	p.Indexers["dmm"].URL = host + "tb/rd/ad/cached"
	h := newHarness(t, n, p, false)
	st := openStore(t)
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: p, Now: h.clock}))
	if got := h.svc.RSSFeeds("dmm"); !slices.Equal(got, []string{"rd", "ad"}) {
		t.Fatalf("service feeds %v", got)
	}
	// Our RD add missed at 12:00; DMM first listed the hash on AD at 10:00.
	if _, err := st.Writer().Exec(`INSERT INTO provider_cache (info_hash, provider, state, source, checked_at,
		expires_at) VALUES (?, 'rd', 'uncached', 'router:add', ?, ?)`, hx("1"), store.Millis(h.clock()),
		store.Millis(h.clock().Add(6*time.Hour))); err != nil {
		t.Fatal(err)
	}
	h.advance(20 * time.Minute)
	ad := search(t, h, "dmm", Query{Type: QueryRSS, Categories: []int{2000}, RSSLimit: 100, RSSFeed: "ad"})
	if len(ad.Releases) != 1 || ad.Releases[0].Extras["cached_on"] != "ad" {
		t.Fatalf("ad list %+v", ad.Releases)
	}
	if got := cacheRows(t, st)["1111/rd"]; got != "uncached router:add" {
		t.Fatalf("rd after the ad list: %q", got)
	}
	rd := search(t, h, "dmm", Query{Type: QueryRSS, Categories: []int{2000}, RSSLimit: 100, RSSFeed: "rd"})
	if len(rd.Releases) != 1 || rd.Releases[0].Extras["cached_on"] != "rd" {
		t.Fatalf("rd list %+v", rd.Releases)
	}
	want := map[string]string{"1111/rd": "cached feed:dmm", "1111/ad": "cached feed:dmm"}
	if got := cacheRows(t, st); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("provider_cache %v, want %v", got, want)
	}
	reqs := rec.all()
	if len(reqs) != 2 || reqs[0].URL.Path != "/api/torznab/ad/cached/api" || reqs[1].URL.Path != "/api/torznab/rd/cached/api" ||
		reqs[1].URL.Query().Get("limit") != "100" {
		t.Fatalf("requests %v", reqs)
	}
	if _, err := h.svc.Search(context.Background(), "dmm", Query{Type: QueryRSS, RSSFeed: "tb"}); err == nil {
		t.Error("a tb list was read")
	}
}
