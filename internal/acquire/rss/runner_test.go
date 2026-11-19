package rss

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/tasks"
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

// A feed whose page_cap asks for 50 a page is paged by what it sent: a full 50-item page is not the end of the
// feed, and the next page starts at item 50, so the previous head on page two is reached.
func TestRSSCatchupPagesByRequestedLimit(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.pol.Indexers["prowlarr"].PageCap = 50
	svc, err := indexer.New(e.pol, e.gw, indexer.Options{Logger: e.log, CacheTTL: -1})
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	e.build(nil)
	items := make([]item, 205)
	for i := range items {
		items[i] = item{title: fmt.Sprintf("Release.%03d.2024.1080p.WEB-DL-GRP", i),
			hash: fmt.Sprintf("%040x", i+1), pub: t0.Add(-time.Duration(i) * time.Minute)}
	}
	e.prow.setRSS(items...)
	res, end, newest, err := e.r.rssPages(e.ctx, "prowlarr", []int{2000}, rssCursor{key: "h:" + items[75].hash})
	if err != nil {
		t.Fatal(err)
	}
	if end != rssReached || newest == nil || newest.key != "h:"+items[0].hash || len(res.Releases) != 100 || res.Requests != 3 {
		t.Fatalf("catch-up end=%v newest=%+v releases=%d requests=%d", end, newest, len(res.Releases), res.Requests)
	}
	reqs := e.net.requests("prowlarr.test/1/api")
	if last := reqs[len(reqs)-1]; !strings.Contains(last, "offset=50") || !strings.Contains(last, "limit=50") {
		t.Errorf("second page %s", last)
	}
}

// An item the engine returns an error for is logged and counted, not remembered (the next run decides it again),
// and the run goes on: a good item beside it is routed, a permanently rejected one is remembered and the cursor moves.
func TestRSSReleaseErrorDoesNotEndTheRun(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.seed(t, dune, argylle, wonka, alien)
	const wonka1080p, hWonka1080p = "Wonka.2023.1080p.WEB-DL.DDP5.1.H.264-FLUX", "7777777777777777777777777777777777777777"
	decideFn := e.r.decideFn
	e.r.decideFn = func(ctx context.Context, rel decide.Release, tt decide.Title, v decide.Version,
		x *decide.ExistingFile) (*decide.Decision, error) {
		if rel.Title == alienWebRel {
			return nil, errors.New("adapt: aggregate languages: Remove(9, 4) on 10 characters")
		}
		return decideFn(ctx, rel, tt, v, x)
	}
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0},
		item{title: argWeb, hash: hArgWeb, size: 20e9, pub: t0.Add(-time.Minute)},
		item{title: wonka1080p, hash: hWonka1080p, size: 6e9, pub: t0.Add(-2 * time.Minute)})
	rs, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Errors != 2 || rs.Routed == 0 { // one per library of the erroring film
		t.Fatalf("rss %+v", rs)
	}
	for _, req := range e.rec.reqs {
		if req.Candidates[0].Release.InfoHash != hArgWeb {
			t.Errorf("routed %+v", req.Candidates[0].Release)
		}
	}
	seen := func(h string) int {
		return one[int](t, e.st, `SELECT COUNT(*) FROM shadow_rss_seen WHERE item_key LIKE ?`, "h:"+h+"%")
	}
	if seen(hAlienWeb) != 0 || seen(hWonka1080p) != 1 {
		t.Errorf("seen: erroring item %d, rejected item %d", seen(hAlienWeb), seen(hWonka1080p))
	}
	if c := one[string](t, e.st, `SELECT item_key FROM shadow_rss_cursor WHERE indexer = 'prowlarr'`); c != "h:"+hAlienWeb {
		t.Errorf("cursor %q", c)
	}
	e.mu.Lock()
	logged := e.logBuf.String()
	e.mu.Unlock()
	if !strings.Contains(logged, "could not be decided") {
		t.Error("the error was not logged")
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
	reads := func() int { return len(e.net.requests("prowlarr.test/1/api")) }
	waitUntil(t, "scheduled read", func() bool { return reads() > 0 })
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
	first := reads()
	if err := e.r.QueueRSS(e.ctx); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "read run now", func() bool { return reads() > first })
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

	e.pol.Router.Enabled = true
	if rs, err = e.r.RSS(e.ctx); err != nil || rs.Routed != 2 {
		t.Fatalf("resumed %+v %v", rs, err)
	}
	if c := one[string](t, e.st, `SELECT item_key FROM shadow_rss_cursor WHERE indexer = 'prowlarr'`); c != "h:"+hAlienWeb {
		t.Errorf("cursor %q", c)
	}
}

// Scheduling: nothing is queued while no library lists a feed.
func TestRSSSkipsScheduleWithoutFeeds(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	for _, v := range e.pol.Versions {
		v.Indexers = nil
	}
	e.build(nil)
	ctx, cancel := context.WithTimeout(e.ctx, 1500*time.Millisecond)
	defer cancel()
	e.r.Schedule(ctx, 0) // runs until ctx ends, reading nothing: no library reads a feed
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

// The RSS task is paused (no next run, no Run now) while Pause is on, RSS on or off, and while RSS is on with no Live
// library; with Pause off a read paused by Pause records no good run, and on-demand reads stay allowed.
func TestRSSTaskPaused(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		edit   func(*env)
		paused bool
	}{
		"pause on":          {func(e *env) { e.pol.Router.Enabled = false }, true},
		"rss off, pause on": {func(e *env) { no := false; e.pol.Shadow = &policy.Shadow{RSS: &no}; e.pol.Router.Enabled = false }, true},
		"live but no feed": {func(e *env) {
			for _, v := range e.pol.Versions {
				v.Indexers = nil
			}
		}, false}, // feeds come and go without a settings change: not part of the paused state
		"rss off, pause off":      {func(e *env) { no := false; e.pol.Shadow = &policy.Shadow{RSS: &no} }, false},
		"rss on, live with feeds": {func(*env) {}, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, true)
			e.seed(t)
			tc.edit(e)
			e.build(nil)
			ctx, cancel := context.WithCancel(e.ctx)
			done := make(chan struct{})
			go func() { e.r.Schedule(ctx, time.Hour); close(done) }()
			defer func() { cancel(); <-done }()
			var got []tasks.Status
			waitUntil(t, "registered", func() bool { got, _ = e.tasks.List(e.ctx); return len(got) == 1 })
			if got[0].Paused != tc.paused || (tc.paused && got[0].NextRun != nil) {
				t.Fatalf("task %+v, want paused %v", got[0], tc.paused)
			}
			if err := e.tasks.RunNow(KindRSS); tc.paused != errors.Is(err, tasks.ErrPaused) {
				t.Fatalf("run now: %v", err)
			}
			if !tc.paused { // let the read it started end before the store closes
				waitUntil(t, "run now ended", func() bool {
					got, _ = e.tasks.List(e.ctx)
					return got[0].Running == nil && got[0].LastFinished != nil
				})
			}
		})
	}
	e := newEnv(t, true)
	e.seed(t)
	e.pol.Router.Enabled = false
	e.build(nil)
	if err := e.r.read(e.ctx); !errors.Is(err, tasks.ErrSkip) {
		t.Fatalf("paused read: %v", err)
	}
}
