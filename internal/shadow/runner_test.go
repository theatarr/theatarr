package shadow

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	hDuneRemux  = "1111111111111111111111111111111111111111"
	hDuneWeb    = "2222222222222222222222222222222222222222"
	hArgWeb     = "3333333333333333333333333333333333333333"
	hArgRemux   = "4444444444444444444444444444444444444444"
	hWonkaYTS   = "5555555555555555555555555555555555555555"
	hAlienWeb   = "6666666666666666666666666666666666666666"
	duneRemux   = "Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR"
	argRemux    = "Argylle.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR"
	argWeb      = "Argylle.2024.2160p.ATVP.WEB-DL.DDP5.1.Atmos.DV.H.265-FLUX"
	alienWebRel = "Alien.Romulus.2024.2160p.DSNP.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX"
)

// The whole runner: the first import only sets the cursors; three new grabs land in the three buckets; RSS picks
// for a wanted movie, one linked by a later arr grab and one expiring into "we'd grab, arr didn't"; the summary,
// the diffs and the metrics.
func TestRunner(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	if e.r.set.RSSInterval != 15*time.Minute {
		t.Fatalf("default RSS interval %s", e.r.set.RSSInterval)
	}
	if got := e.r.Versions(); strings.Join(got, ",") != "movie-remux,movie-web,tv" {
		t.Fatalf("versions %v", got)
	}
	if len(e.warns) != 0 {
		t.Errorf("warnings %v", e.warns)
	}

	sum, err := e.r.Import(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Grabs != 0 || !sum.Instances[0].FirstRun || sum.Instances[1].Cursor != 1 {
		t.Fatalf("first import %+v", sum)
	}

	// Our sources for the three movies.
	e.prow.set(dune.imdb,
		item{title: duneRemux, hash: hDuneRemux, size: 70e9, pub: t0.Add(-48 * time.Hour)},
		item{title: "Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-FLUX", hash: hDuneWeb, size: 25e9, pub: t0.Add(-60 * 24 * time.Hour)})
	e.prow.set(argylle.imdb, item{title: argWeb, hash: hArgWeb, size: 20e9, pub: t0.Add(-72 * time.Hour)})
	e.dmm.set(argylle.imdb, item{title: argRemux, hash: hArgRemux, size: 60e9, pub: t0.Add(-96 * time.Hour)})

	e.zurg.add(grabEvent(2, dune, t0.Add(-time.Hour), duneRemux, hDuneRemux, "Remux-2160p", 31, 1950, "Rss"))
	e.zurg.add(grabEvent(3, argylle, t0.Add(-50*time.Minute), argWeb, hArgWeb, "WEBDL-2160p", 18, 1700, "Search"))
	e.zurg.add(grabEvent(4, wonka, t0.Add(-40*time.Minute), "Wonka.2023.2160p.WEB-DL.DDP5.1-YTS", hWonkaYTS,
		"WEBDL-2160p", 18, -10000, "InteractiveSearch"))
	sum, err = e.r.Import(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Grabs != 3 || sum.Instances[1].Grabs != 3 || sum.Instances[1].Cursor != 4 {
		t.Fatalf("second import %+v", sum)
	}

	diffs := map[int]DiffRow{}
	rows, total, err := ListDiffs(e.ctx, e.st.Reader(), DiffFilter{})
	if err != nil || total != 3 {
		t.Fatalf("diffs %d %v", total, err)
	}
	for _, d := range rows {
		g, err := LoadDiff(e.ctx, e.st.Reader(), d.ID)
		if err != nil {
			t.Fatal(err)
		}
		diffs[g.Grab.HistoryID] = d
	}
	check := func(id int, bucket, reason string) {
		t.Helper()
		if d := diffs[id]; d.Bucket != bucket || d.Reason != reason || !d.Searched {
			t.Errorf("grab %d: %s/%s searched %v (%s), want %s/%s", id, d.Bucket, d.Reason, d.Searched, d.Detail, bucket, reason)
		}
	}
	check(2, BucketAgree, ReasonRSSAccepted) // an RSS grab with no RSS pick of ours near it
	check(3, BucketAnother, ReasonBetterQuality)
	check(4, BucketWouldnt, ReasonManualRejected)
	if d := diffs[3]; d.OurRelease != argRemux || d.ArrRelease != argWeb || d.Instance != "radarr_zurg" {
		t.Errorf("another: %+v", d)
	}
	if d := diffs[4]; !strings.Contains(d.Detail, "by hand") || !strings.Contains(d.Detail, "CustomFormatMinimumScore") ||
		!strings.Contains(d.Detail, "category below_min_score") || d.Basis != BasisManual {
		t.Errorf("wouldn't: %s", d.Detail)
	}

	// The Dune grab was decided against the WEB-DL file the version had then (an upgrade), from version_file.
	det, err := LoadDiff(e.ctx, e.st.Reader(), diffs[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	g := det.Grab
	if g.ExistingSource != "version_file" || !g.ArrDecision.Upgrade || g.ArrDecision.Existing == nil ||
		g.ArrDecision.Existing.Quality != "WEBDL-2160p" || !g.FoundArr || g.ResultCount != 2 || g.Pick == nil ||
		g.Pick.Release != duneRemux || len(g.Results) != 2 || g.ArrScore == nil || *g.ArrScore != 1950 {
		t.Errorf("dune grab %+v", g)
	}
	// One search per title across the two indexers: Dune asked prowlarr and dmm once each.
	if n := len(e.net.requests("prowlarr.test/1/api?cat=2000&imdbid=15239678")); n != 1 {
		t.Errorf("prowlarr dune searches %d: %v", n, e.net.requests("prowlarr.test"))
	}

	// RSS: an Alien release for both wanted versions, and a Dune WEB-DL: no upgrade for movie-remux (it has a
	// WEB-DL of the same score), a pick for movie-web (wanted).
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0.Add(-10 * time.Minute)},
		item{title: "Dune.Part.Two.2024.2160p.MA.WEB-DL.DDP5.1-NTb", hash: "7777777777777777777777777777777777777777",
			size: 25e9, pub: t0})
	rs, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Two feeds: Prowlarr's and DMM's newly cached list (empty here), one request each.
	if rs.Items != 2 || rs.New != 2 || rs.Picks != 3 || rs.Requests != 2 || len(rs.Indexers) != 2 {
		t.Fatalf("rss %+v", rs)
	}
	if got := one[string](t, e.st, `SELECT item_key FROM shadow_rss_cursor WHERE indexer = 'prowlarr'`); got != "h:6666666666666666666666666666666666666666" {
		t.Errorf("RSS cursor %q", got)
	}
	// The Dune item's existing-file rejection can change, so it is decided again although it is below the feed head
	// (New 1); its existing shadow pick is not inserted twice.
	if rs2, err := e.r.RSS(e.ctx); err != nil || rs2.New != 1 || rs2.Picks != 0 {
		t.Fatalf("rss again %+v %v", rs2, err)
	}

	// The web arr grabs Alien: the movie-web pick is linked to it.
	e.prow.set(alien.imdb, item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0.Add(-10 * time.Minute)})
	e.advance(30 * time.Minute)
	e.web.add(grabEvent(2, alien, e.clock().Add(-5*time.Minute), alienWebRel, hAlienWeb, "WEBDL-2160p", 18, 1700, "Rss"))
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM shadow_rss_pick WHERE status = 'linked' AND version_key = 'movie-web'`); n != 1 {
		t.Errorf("linked picks %d", n)
	}
	if rid := one[int64](t, e.st, `SELECT COALESCE(rss_pick_id, 0) FROM shadow_diff d JOIN shadow_grab g ON g.id = d.grab_id
		WHERE g.instance = 'radarr_web'`); rid == 0 {
		t.Error("the web grab's diff names no RSS pick")
	}
	// An RSS grab of the release our RSS read picked 25 minutes before: same_rss_pick.
	if s := one[string](t, e.st, `SELECT bucket || '/' || reason FROM shadow_diff d JOIN shadow_grab g ON g.id = d.grab_id
		WHERE g.instance = 'radarr_web'`); s != "agree/same_rss_pick" {
		t.Errorf("web grab %s", s)
	}

	// A day later the movie-remux pick has no arr grab: we'd grab, arr didn't.
	e.advance(25 * time.Hour)
	rs, err = e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rs.ExpiredRows != 2 || rs.Expired != 2 {
		t.Fatalf("expiry %+v", rs)
	}
	rows, _, err = ListDiffs(e.ctx, e.st.Reader(), DiffFilter{Bucket: BucketRSSOnly, Version: "movie-remux"})
	if err != nil || len(rows) != 1 || rows[0].Version != "movie-remux" || rows[0].OurRelease != alienWebRel {
		t.Fatalf("rss-only rows %+v %v", rows, err)
	}
	det, err = LoadDiff(e.ctx, e.st.Reader(), rows[0].ID)
	if err != nil || det.Pick == nil || det.Pick.Status != "expired" || !det.Pick.Decision.Accepted {
		t.Fatalf("rss-only detail %+v %v", det, err)
	}

	s, err := LoadSummary(e.ctx, e.st.Reader(), e.clock())
	if err != nil {
		t.Fatal(err)
	}
	// The hand grab (manual_grab_rejected) is listed but left out of the ratio: 2 agree of 3 grabs.
	if s.Grabs != 3 || s.Buckets[BucketAgree] != 2 || s.Buckets[BucketRSSOnly] != 2 || s.Agreement == nil ||
		*s.Agreement != 2.0/3 || s.Searched != 3 || s.HandGrabs != (HandGrabs{Grabs: 1, Rejected: 1}) || len(s.Versions) != 2 || len(s.Cursors) != 3 || len(s.LastRuns) != 2 {
		b, _ := json.Marshal(s)
		t.Errorf("summary %s", b)
	}
	if v := testutil.ToFloat64(e.r.m.agreement.WithLabelValues("14d")); v != 2.0/3 {
		t.Errorf("agreement metric %v", v)
	}
	if v := testutil.ToFloat64(e.r.m.diffs.WithLabelValues(BucketWouldnt, "14d")); v != 1 {
		t.Errorf("wouldnt metric %v", v)
	}

	// Filters.
	if rows, n, err := ListDiffs(e.ctx, e.st.Reader(), DiffFilter{Version: "movie-web"}); err != nil || n != 2 || len(rows) != 2 {
		t.Errorf("version filter %d %v", n, err)
	}
	if _, n, _ := ListDiffs(e.ctx, e.st.Reader(), DiffFilter{Reason: ReasonManualRejected}); n != 1 {
		t.Errorf("reason filter %d", n)
	}
	if _, err := LoadDiff(e.ctx, e.st.Reader(), 999); err != ErrNotFound {
		t.Errorf("missing diff: %v", err)
	}

	// run-once on a grab replaces its rows; without a search it is decided alone.
	res, err := e.r.GrabByID(e.ctx, "radarr_zurg", 3, true)
	if err != nil || res.Searched || res.Outcome.Bucket != BucketAgree || res.Outcome.Reason != ReasonNotSearched {
		t.Fatalf("run-once %+v %v", res, err)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM shadow_diff WHERE grab_id IS NOT NULL`); n != 4 {
		t.Errorf("grab diffs after a re-run: %d", n)
	}
	if _, err := e.r.GrabByID(e.ctx, "", 2, true); err == nil || !strings.Contains(err.Error(), "-instance") {
		t.Errorf("ambiguous history id: %v", err)
	}
}

func TestRSSCatchupPagesThroughPreviousCursor(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	items := make([]item, 205)
	for i := range items {
		items[i] = item{title: fmt.Sprintf("Release.%03d.2024.1080p.WEB-DL-GRP", i),
			hash: fmt.Sprintf("%040x", i+1), pub: t0.Add(-time.Duration(i) * time.Minute)}
	}
	e.prow.setRSS(items...)
	res, end, newest, err := e.r.rssPages(e.ctx, "prowlarr", []int{2000}, rssCursor{key: "h:" + items[150].hash})
	if err != nil {
		t.Fatal(err)
	}
	// The whole page holding the previous head is read, the items below it too (200, not 151).
	if end != rssReached || newest == nil || newest.key != "h:"+items[0].hash || len(res.Releases) != 200 || res.Requests != 3 {
		t.Fatalf("catch-up end=%v newest=%+v releases=%d requests=%d", end, newest, len(res.Releases), res.Requests)
	}
}

// With a queue, grabs are jobs; over the hourly cap a grab waits for the next hour, and after a day it is decided
// without a search.
func TestGrabDeferredByCap(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.r.set.SearchesPerHour = 1
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.zurg.add(grabEvent(2, dune, t0.Add(-time.Hour), duneRemux, hDuneRemux, "Remux-2160p", 31, 1950, "Rss"))
	e.zurg.add(grabEvent(3, argylle, t0.Add(-50*time.Minute), argWeb, hArgWeb, "WEBDL-2160p", 18, 1700, "Search"))
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM job WHERE kind = 'shadow.grab' AND priority = -10`); n != 2 {
		t.Fatalf("grab jobs %d", n)
	}
	r1, err := e.r.grab(e.ctx, grabPayload{Instance: "radarr_zurg", HistoryID: 2}, false)
	if err != nil || !r1.Searched {
		t.Fatalf("first %+v %v", r1, err)
	}
	r2, err := e.r.grab(e.ctx, grabPayload{Instance: "radarr_zurg", HistoryID: 3}, false)
	if err != nil || !r2.Deferred {
		t.Fatalf("second %+v %v", r2, err)
	}
	if st := one[string](t, e.st, `SELECT state FROM shadow_grab WHERE history_id = 3`); st != "pending" {
		t.Errorf("state %s", st)
	}
	runAt := one[int64](t, e.st, `SELECT run_at FROM job WHERE dedupe_key = 'shadow.grab:radarr_zurg:3:1'`)
	if at := time.UnixMilli(runAt).UTC(); !at.After(t0.Truncate(time.Hour).Add(time.Hour)) || at.After(t0.Add(2*time.Hour)) {
		t.Errorf("deferred to %v", at)
	}
	// A repeat of the first grab's job is a no-op.
	if r, err := e.r.grab(e.ctx, grabPayload{Instance: "radarr_zurg", HistoryID: 2}, false); err != nil || r.Skipped != "already done" {
		t.Errorf("repeat %+v %v", r, err)
	}
	// A day on, still over the cap (same hour of a later day counts afresh, so fill it): decided without a search.
	e.advance(25 * time.Hour)
	if _, err := e.st.Writer().Exec(`UPDATE shadow_grab SET searched_at = ? WHERE history_id = 2`,
		e.clock().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	r3, err := e.r.grab(e.ctx, grabPayload{Instance: "radarr_zurg", HistoryID: 3, Defers: 1}, false)
	if err != nil || r3.Deferred || r3.Searched || !strings.Contains(r3.Note, "cap") {
		t.Fatalf("late %+v %v", r3, err)
	}
	s, err := LoadSummary(e.ctx, e.st.Reader(), e.clock())
	if err != nil || s.Pending != 0 || s.Grabs != 2 || s.Searched != 1 {
		t.Errorf("summary %+v %v", s, err)
	}
}

// A budget backing off skips its indexer; with every indexer skipped the search is empty, not failed.
func TestBudgetBlocked(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	if why := e.r.budgetBlocked(e.ctx, "prowlarr"); why != "" {
		t.Fatalf("blocked at start: %s", why)
	}
	e.gw.BackOff(e.ctx, gatewayTarget("prowlarr"), time.Now().Add(time.Hour), "test")
	if why := e.r.budgetBlocked(e.ctx, "prowlarr"); !strings.Contains(why, "backing off") {
		t.Errorf("why %q", why)
	}
}

func TestMetricsNaNWithoutGrabs(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	if err := e.r.RefreshMetrics(e.ctx); err != nil {
		t.Fatal(err)
	}
	if v := testutil.ToFloat64(e.r.m.agreement.WithLabelValues("14d")); !math.IsNaN(v) {
		t.Errorf("agreement %v", v)
	}
}
