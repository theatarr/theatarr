package shadow

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// The RSS sync reads DMM's newly cached list on a path naming rd or ad, a full page of 100 whatever page_cap bounds a
// search (the test policy's dmm has page_cap 20), and skips a path whose only provider is tb.
func TestRSSReadsDMMNewlyCached(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.pol.Versions["movie-remux"].Indexers = append(e.pol.Versions["movie-remux"].Indexers, "dmm-tb")
	e.build(nil)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	feeds, skipped := e.r.rssIndexers(e.ctx)
	if !slices.Equal(feeds, []string{"dmm", "prowlarr"}) || len(skipped) != 0 {
		t.Fatalf("feeds %v skipped %v", feeds, skipped)
	}
	e.prow.setRSS()
	e.dmm.setRSS(item{title: alienWebRel, hash: hAlienWeb, imdb: alien.imdb, size: 20e9, pub: t0})
	sum, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := rssIndexer(sum, "dmm")
	if d == nil || d.Items != 1 || d.New != 1 || d.Requests != 1 || d.Error != "" || d.Skipped != "" {
		t.Fatalf("dmm feed %+v (all %+v)", d, sum.Indexers)
	}
	if rssIndexer(sum, "dmm-tb") != nil {
		t.Errorf("the tb-only DMM path was read: %+v", sum.Indexers)
	}
	reqs := e.net.requests("dmm.test/")
	if len(reqs) != 1 || !strings.HasPrefix(reqs[0], "dmm.test/api/torznab/rd/cached/api?") ||
		!strings.Contains(reqs[0], "limit=100") || !strings.Contains(reqs[0], "t=search") {
		t.Fatalf("DMM requests %v", reqs)
	}
	if c := one[string](t, e.st, `SELECT item_key FROM shadow_rss_cursor WHERE indexer = 'dmm'`); c != "h:"+hAlienWeb {
		t.Errorf("cursor %q", c)
	}
}

// DMM dates its newly cached list by the build that listed each release, so many items share one pubDate. Items
// sharing the previous head's pubDate do not end catch-up before the head's page, and the head's page is read whole:
// equal-date items start on the first page (index 50), the head is on the second (index 180).
func TestRSSCatchupWithSharedPubDates(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	build1, build2 := t0.Add(-time.Hour), t0
	items := filler(250, 0)
	for i := range items {
		items[i].pub = build1
		if i < 50 {
			items[i].pub = build2
		}
	}
	head := items[180] // the previous sync's head: listed in build1, on the second page
	e.prow.setRSS(items...)
	res, end, newest, err := e.r.rssPages(e.ctx, "prowlarr", []int{2000}, rssCursor{key: "h:" + head.hash,
		published: head.pub})
	if err != nil {
		t.Fatal(err)
	}
	if end != rssReached || newest == nil || newest.key != "h:"+items[0].hash || len(res.Releases) != 200 {
		t.Fatalf("end=%v newest=%+v releases=%d", end, newest, len(res.Releases))
	}
}

// A DMM path naming rd and ad is read as two feeds, each provider's own newly cached list: one request each per sync,
// each with its own cursor and seen marks, so the same GUID on both lists is decided once per list. A single-provider
// path stays one feed and one request.
func TestRSSReadsDMMListsPerProvider(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.pol.Versions["movie-remux"].Indexers = append(e.pol.Versions["movie-remux"].Indexers, "dmm-rdad")
	e.build(nil)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	const rdPath, adPath = "/api/torznab/rd/cached/api", "/api/torznab/ad/cached/api"
	rel := item{title: alienWebRel, hash: hAlienWeb, imdb: alien.imdb, size: 20e9, pub: t0}
	e.prow.setRSS()
	e.dmm.setRSS()
	e.dmm.mu.Lock()
	e.dmm.rssPath = map[string][]item{rdPath: {rel}, adPath: {rel}}
	e.dmm.mu.Unlock()
	sum, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"dmm-rdad#rd", "dmm-rdad#ad"} {
		if d := rssIndexer(sum, k); d == nil || d.Items != 1 || d.New != 1 || d.Requests != 1 || d.Error != "" {
			t.Errorf("%s: %+v (all %+v)", k, d, sum.Indexers)
		}
	}
	if d := rssIndexer(sum, "dmm"); d == nil || d.Requests != 1 {
		t.Errorf("single-provider dmm %+v", d)
	}
	cursor := func(k string) string {
		return one[string](t, e.st, `SELECT item_key FROM shadow_rss_cursor WHERE indexer = ?`, k)
	}
	if cursor("dmm-rdad#rd") != "h:"+hAlienWeb || cursor("dmm-rdad#ad") != "h:"+hAlienWeb {
		t.Fatalf("cursors %q %q", cursor("dmm-rdad#rd"), cursor("dmm-rdad#ad"))
	}

	// A new item on the rd list only: its cursor moves, ad's stays; nothing is new on ad.
	e.advance(15 * time.Minute)
	newer := item{title: "Alien.Romulus.2024.2160p.WEB-DL.DV.HDR.H265-OTHER", hash: strings.Repeat("e", 40),
		imdb: alien.imdb, size: 21e9, pub: t0.Add(10 * time.Minute)}
	e.dmm.mu.Lock()
	e.dmm.rssPath[rdPath] = []item{newer, rel}
	e.dmm.mu.Unlock()
	if sum, err = e.r.RSS(e.ctx); err != nil {
		t.Fatal(err)
	}
	if d := rssIndexer(sum, "dmm-rdad#rd"); d == nil || d.New != 1 {
		t.Errorf("rd second sync %+v", d)
	}
	if d := rssIndexer(sum, "dmm-rdad#ad"); d == nil || d.New != 0 {
		t.Errorf("ad second sync %+v", d)
	}
	if cursor("dmm-rdad#rd") != "h:"+newer.hash || cursor("dmm-rdad#ad") != "h:"+hAlienWeb {
		t.Errorf("cursors %q %q", cursor("dmm-rdad#rd"), cursor("dmm-rdad#ad"))
	}
	var rd, ad, other int
	for _, r := range e.net.requests("dmm.test/") {
		switch {
		case strings.HasPrefix(r, "dmm.test"+rdPath):
			rd++
		case strings.HasPrefix(r, "dmm.test"+adPath):
			ad++
		case !strings.Contains(r, "t=search"):
		default:
			other++
		}
	}
	// Two syncs: rd/cached/api serves both the single-provider dmm and dmm-rdad#rd.
	if rd != 4 || ad != 2 || other != 0 {
		t.Errorf("requests rd %d ad %d other %d: %v", rd, ad, other, e.net.requests("dmm.test/"))
	}
}

// The first sync after the split: a list without a cursor of its own catches up to the old combined cursor's
// publish date, as far as the page cap allows, instead of reading one page.
func TestRSSSplitListsStartAtCombinedCursor(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.pol.Versions["movie-remux"].Indexers = append(e.pol.Versions["movie-remux"].Indexers, "dmm-rdad")
	e.build(nil)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	const rdPath, adPath = "/api/torznab/rd/cached/api", "/api/torznab/ad/cached/api"
	e.exec(t, `INSERT INTO shadow_rss_cursor (indexer, item_key, updated_at, published_at) VALUES ('dmm-rdad', 'h:old', 0, ?)`,
		t0.Add(-180*time.Minute).UnixMilli())
	e.prow.setRSS()
	e.dmm.setRSS()
	e.dmm.mu.Lock()
	e.dmm.rssPath = map[string][]item{rdPath: filler(250, 0), adPath: filler(50, 1000)}
	e.dmm.mu.Unlock()
	sum, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	// rd: items newer than the watermark run to minute 180, on the second page.
	if d := rssIndexer(sum, "dmm-rdad#rd"); d == nil || d.Requests != 2 || d.Items != 200 || d.Gap != "" {
		t.Errorf("rd %+v", d)
	}
	// ad: its first item is already older than the watermark: one page.
	if d := rssIndexer(sum, "dmm-rdad#ad"); d == nil || d.Requests != 1 || d.Gap != "" {
		t.Errorf("ad %+v", d)
	}
}

// RSS outcomes count per list: one DMM list failing is not "every feed failed", both failing is.
func TestRSSFailuresCountPerList(t *testing.T) {
	t.Parallel()
	const rdPath, adPath = "/api/torznab/rd/cached/api", "/api/torznab/ad/cached/api"
	for _, q := range []struct {
		name    string
		fail    []string
		wantErr bool
	}{{"rd fails", []string{rdPath}, false}, {"both fail", []string{rdPath, adPath}, true}} {
		t.Run(q.name, func(t *testing.T) {
			e := newEnv(t, false)
			for _, v := range e.pol.Versions {
				v.Indexers = []string{"dmm-rdad"}
			}
			e.build(nil)
			if _, err := e.r.Import(e.ctx); err != nil {
				t.Fatal(err)
			}
			e.dmm.mu.Lock()
			e.dmm.rssPath = map[string][]item{rdPath: nil, adPath: nil}
			e.dmm.rssFail = map[string]bool{}
			for _, p := range q.fail {
				e.dmm.rssFail[p] = true
			}
			e.dmm.mu.Unlock()
			sum, err := e.r.RSS(e.ctx)
			if (err != nil) != q.wantErr {
				t.Errorf("err %v, want an error %v (%+v)", err, q.wantErr, sum.Indexers)
			}
			if d := rssIndexer(sum, "dmm-rdad#rd"); d == nil || d.Error == "" {
				t.Errorf("rd failure not in the summary: %+v", sum.Indexers)
			}
		})
	}
}
