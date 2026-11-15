package rss

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

const (
	hArgWeb     = "3333333333333333333333333333333333333333"
	hAlienWeb   = "6666666666666666666666666666666666666666"
	hWonkaRemux = "abababababababababababababababababababab"
	argWeb      = "Argylle.2024.2160p.ATVP.WEB-DL.DDP5.1.Atmos.DV.H.265-FLUX"
	alienWebRel = "Alien.Romulus.2024.2160p.DSNP.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-FLUX"
	alien1080p  = "Alien.Romulus.2024.1080p.DSNP.WEB-DL.DDP5.1.Atmos.H.264-FLUX" // no profile takes 1080p
	wonkaRemux  = "Wonka.2023.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR"
)

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

// A budget backing off skips its indexer.
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

// A film added after the first RSS run is matched by the next run: RSS reloads the film index each time, and the
// engine's searches see it too.
func TestRSSSeesFilmAddedAfterFirstRun(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.seed(t, dune, argylle, wonka)
	if _, err := e.r.RSS(e.ctx); err != nil {
		t.Fatal(err)
	}

	e.seed(t, alien)
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0})
	rs, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Matched != 1 || rs.Routed != 2 {
		t.Fatalf("rss %+v", rs)
	}
	if e.eng.Library().ByIMDb(alien.imdb) == nil {
		t.Error("the engine's film index has no Alien")
	}
}

// The schedule registers the RSS task: a scheduled read runs as the task and records its row (last run, next run
// an interval on) without a job row; Run now (the arr RssSync command) reads again.
func TestScheduleRunsTheRSSTask(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.seed(t)
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0})
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { e.r.Schedule(ctx, 0); close(done) }()
	defer func() { cancel(); <-done }()
	rows := func() int {
		return one[int](t, e.st, `SELECT COUNT(*) FROM shadow_run WHERE kind = ? AND ok = 1`, KindRSS)
	}
	waitUntil(t, "scheduled read", func() bool { return rows() == 1 })
	waitUntil(t, "task row", func() bool {
		return one[int](t, e.st, `SELECT COUNT(*) FROM task WHERE key = ? AND last_finished IS NOT NULL AND next_run = ?`,
			KindRSS, e.clock().Add(e.r.set.RSSInterval).UnixMilli()) == 1
	})
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM job WHERE kind = ?`, KindRSS); n != 0 {
		t.Fatalf("%d RSS jobs", n)
	}
	if got := e.rec.candidates(); len(got) != 2 {
		t.Errorf("routed %v", got)
	}
	if err := e.r.QueueRSS(e.ctx); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "read run now", func() bool { return rows() == 2 })
}

// While downloads are paused (router.enabled false) a run, scheduled, queued or manual, reads and routes nothing and
// moves no cursor or seen mark; after the pause the same items are routed.
func TestRSSPausedRoutesNothing(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.seed(t)
	e.pol.Router.Enabled = false
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0})
	rs, err := e.r.RSS(e.ctx)
	if err != nil || rs.Skipped != skippedPaused || rs.Items != 0 || rs.Routed != 0 {
		t.Fatalf("paused %+v %v", rs, err)
	}
	if got := e.rec.candidates(); len(got) != 0 {
		t.Fatalf("routed while paused: %v", got)
	}
	if n := len(e.net.requests("prowlarr.test/1/api")); n != 0 {
		t.Errorf("%d feed reads while paused", n)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM shadow_rss_cursor`) + one[int](t, e.st, `SELECT COUNT(*) FROM shadow_rss_seen`); n != 0 {
		t.Fatalf("%d cursors or seen marks while paused", n)
	}
	if s := one[string](t, e.st, `SELECT summary FROM shadow_run WHERE kind = ?`, KindRSS); !strings.Contains(s, skippedPaused) {
		t.Errorf("run row %s", s)
	}

	e.pol.Router.Enabled = true
	if rs, err = e.r.RSS(e.ctx); err != nil || rs.Routed != 2 {
		t.Fatalf("resumed %+v %v", rs, err)
	}
	if c := one[string](t, e.st, `SELECT item_key FROM shadow_rss_cursor WHERE indexer = 'prowlarr'`); c != "h:"+hAlienWeb {
		t.Errorf("cursor %q", c)
	}
}

// Scheduling: nothing is queued while every library is Watch only.
func TestRSSSkipsWatchOnlySchedule(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	for _, v := range e.pol.Versions {
		v.Mode = ""
	}
	e.build(nil)
	ctx, cancel := context.WithTimeout(e.ctx, 1500*time.Millisecond)
	defer cancel()
	e.r.Schedule(ctx, 0) // runs until ctx ends, reading nothing: no Live library reads a feed
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM task WHERE key = ? AND last_started IS NOT NULL`, KindRSS); n != 0 {
		t.Fatalf("%d RSS reads", n)
	}
}

// The decision engine's film index does not depend on RSS running: it is loaded when the schedule starts and
// reloaded on each tick no read is queued for, with RSS off and with no Live library listing a feed.
func TestFilmIndexWithoutRSS(t *testing.T) {
	t.Parallel()
	for name, off := range map[string]func(e *env){
		"rss off": func(e *env) {
			no := false
			e.pol.Shadow = &policy.Shadow{RSS: &no}
		},
		"no feed": func(e *env) {
			for _, v := range e.pol.Versions {
				v.Indexers = nil
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, true)
			off(e)
			e.build(nil)
			e.seed(t, dune)
			ctx, cancel := context.WithCancel(e.ctx)
			done := make(chan struct{})
			go func() { e.r.Schedule(ctx, 0); close(done) }()
			defer func() { cancel(); <-done }()
			known := func(m movieDef) bool { return e.eng.Library().ByIMDb(m.imdb) != nil }
			waitUntil(t, "index loaded at start", func() bool { return known(dune) })
			e.seed(t, alien)
			e.advance(e.r.set.RSSInterval)
			waitUntil(t, "film added after start known after the tick", func() bool { return known(alien) })
			if n := one[int](t, e.st, `SELECT COUNT(*) FROM task WHERE key = ? AND last_started IS NOT NULL`, KindRSS); n != 0 {
				t.Errorf("%d RSS reads", n)
			}
		})
	}
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal(what)
}
