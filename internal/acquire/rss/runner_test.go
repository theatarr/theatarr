package rss

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
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

// The worker handles shadow.rss: a queued read runs and leaves its run row.
func TestRegisterJobsHandlesRSS(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.seed(t)
	e.prow.setRSS(item{title: alienWebRel, hash: hAlienWeb, size: 20e9, pub: t0})
	w := jobs.NewWorker(e.q, jobs.WorkerOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	e.r.RegisterJobs(w)
	if err := e.r.QueueRSS(e.ctx); err != nil {
		t.Fatal(err)
	}
	if ran, err := w.RunOne(e.ctx); err != nil || !ran {
		t.Fatalf("ran %v err %v", ran, err)
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM shadow_run WHERE kind = ? AND ok = 1`, KindRSS); n != 1 {
		t.Fatalf("%d run rows", n)
	}
	if got := e.rec.candidates(); len(got) != 2 {
		t.Errorf("routed %v", got)
	}
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
	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Second)
	defer cancel()
	e.r.Schedule(ctx, 0) // returns at once: no Live version reads a feed
	if ctx.Err() != nil {
		t.Fatal("Schedule ran with only Watch-only libraries")
	}
	if n := one[int](t, e.st, `SELECT COUNT(*) FROM job WHERE kind = ?`, KindRSS); n != 0 {
		t.Fatalf("%d RSS jobs", n)
	}
}
