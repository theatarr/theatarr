package shadow

import (
	"fmt"
	"testing"
	"time"
)

// filler is n releases for titles we don't know, newest first, starting at minute from.
func filler(n, from int) []item {
	out := make([]item, n)
	for i := range out {
		out[i] = item{title: fmt.Sprintf("Unknown.Film.%04d.2024.1080p.WEB-DL-GRP", from+i),
			hash: fmt.Sprintf("%040x", 0x10000+from+i), pub: t0.Add(-time.Duration(from+i) * time.Minute)}
	}
	return out
}

func (e *env) cursor(t *testing.T) string {
	t.Helper()
	var c string
	_ = e.st.Reader().QueryRow(`SELECT item_key FROM shadow_rss_cursor WHERE indexer = 'prowlarr'`).Scan(&c)
	return c
}

// Like the arrs, every item of the fetched feed page is evaluated on each sync, not only the items above the
// previous head: an item rejected for a temporary reason is decided again while it stays in the feed.
func TestRSSReconsidersItemsBelowPreviousHead(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE title_release_date SET date = '2099-01-01' WHERE title_id IN (SELECT id FROM title WHERE tmdb_id = ?)`, alien.tmdb)
	head := filler(1, 0)[0]
	e.prow.setRSS(head, item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0.Add(-time.Hour)})
	if sum, err := e.r.RSS(e.ctx); err != nil || sum.Picks != 0 {
		t.Fatalf("unavailable: %+v %v", sum, err)
	}
	if c := e.cursor(t); c != "h:"+head.hash {
		t.Fatalf("cursor %q", c)
	}
	e.exec(t, `UPDATE title_release_date SET date = '2024-01-01' WHERE title_id IN (SELECT id FROM title WHERE tmdb_id = ?)`, alien.tmdb)
	e.advance(time.Hour) // past the indexer's result cache
	if sum, err := e.r.RSS(e.ctx); err != nil || sum.Picks != 2 {
		t.Fatalf("available again below the previous head: %+v %v", sum, err)
	}
}

// The cursor moves only once the items read have durable state: when routing fails after a catch-up, the next sync
// pages through the same catch-up pages again instead of stopping at the new head.
func TestRSSCursorAfterDurableState(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	_, _, _ = e.withRouter(t, "movie-web")
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	old := filler(1, 500)[0]
	e.exec(t, `INSERT INTO shadow_rss_cursor (indexer, item_key, updated_at) VALUES ('prowlarr', ?, ?)`, "h:"+old.hash,
		e.clock().UnixMilli())
	// 150 new items above the previous head; ours is on the second page.
	items := filler(150, 0)
	items = append(items, item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0.Add(-200 * time.Minute)}, old)
	e.prow.setRSS(items...)
	allow := e.refuseJobs(t, "router.route")
	if _, err := e.r.RSS(e.ctx); err == nil {
		t.Fatal("routing error swallowed")
	}
	if c := e.cursor(t); c != "h:"+old.hash {
		t.Fatalf("cursor moved before the items had durable state: %q", c)
	}
	allow()
	e.advance(time.Hour)
	rs, err := e.r.RSS(e.ctx)
	if err != nil || rs.Routed != 1 {
		t.Fatalf("after the failure: %+v %v", rs, err)
	}
	if c := e.cursor(t); c != "h:"+items[0].hash {
		t.Errorf("cursor %q after a clean sync", c)
	}
}

// At the catch-up page cap the cursor moves on at once and the gap is reported, as the arrs do: holding it
// re-read the newest ten pages every sync while new items pushed the gap further down, so it was never reached.
func TestRSSCatchupCapMovesCursor(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	old := filler(1, 5000)[0]
	e.exec(t, `INSERT INTO shadow_rss_cursor (indexer, item_key, updated_at) VALUES ('prowlarr', ?, ?)`, "h:"+old.hash,
		e.clock().UnixMilli())
	items := append(filler(rssCatchupPages*rssPageSize+50, 0), old)
	e.prow.setRSS(items...)
	sum, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p := rssIndexer(sum, "prowlarr"); len(sum.Indexers) != 2 || p == nil || p.Gap == "" || p.Requests < rssCatchupPages {
		t.Errorf("truncation not reported: %+v", sum.Indexers)
	}
	if c := e.cursor(t); c != "h:"+items[0].hash {
		t.Fatalf("cursor %q, want the new head %q", c, "h:"+items[0].hash)
	}
	// The next sync reads from the new head: one page, no gap.
	e.advance(15 * time.Minute)
	if sum, err = e.r.RSS(e.ctx); err != nil || rssIndexer(sum, "prowlarr").Gap != "" || rssIndexer(sum, "prowlarr").Requests > 2 {
		t.Fatalf("next sync: %+v %v", sum.Indexers, err)
	}
}

// A previous head the indexer removed still ends catch-up: the first page holding an item published before it, as
// the arrs compare the last release's publish date.
func TestRSSCatchupStopsAtOlderItem(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	items := filler(300, 0)
	gone := items[150]
	e.prow.setRSS(append(items[:150:150], items[151:]...)...)
	res, end, newest, err := e.r.rssPages(e.ctx, "prowlarr", []int{2000}, rssCursor{key: "h:" + gone.hash,
		published: gone.pub})
	if err != nil {
		t.Fatal(err)
	}
	if end != rssReached || newest == nil || newest.key != "h:"+items[0].hash || !newest.published.Equal(items[0].pub) ||
		len(res.Releases) != 200 {
		t.Fatalf("end=%v newest=%+v releases=%d", end, newest, len(res.Releases))
	}
}
