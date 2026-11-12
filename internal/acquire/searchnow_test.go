package acquire

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// loopOff is a deployment with the wanted loop off: only Search now and its job start searches.
func loopOff(perHour int) *policy.Wanted {
	return &policy.Wanted{Enabled: new(bool), PerHour: perHour, CutoffUnmet: policy.CutoffScore}
}

// selectsNothing is a wanted loop that selects neither missing nor cutoff-unmet items itself.
func selectsNothing(perHour int) *policy.Wanted {
	off := false
	return &policy.Wanted{PerHour: perHour, Missing: &off, CutoffUnmet: policy.CutoffOff}
}

func (e *env) request(title int64, key, reason string, season int) {
	e.t.Helper()
	e.exec(`INSERT INTO search_request (title_id, version_key, reason, season, created_at) VALUES (?, ?, ?, ?, ?)`,
		title, key, reason, season, store.Millis(e.clock()))
}

func (e *env) searchNowJobs() int {
	return e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'`, JobSearchNow)
}

// A selection larger than the hourly budget starts what the budget leaves and keeps the rest; the scheduled job
// drains it window by window with the loop off, and every run counts as a wanted search.
func TestSearchNowAllPacedByTheHourlyBudget(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, loopOff(2))
	e.accept = false
	// Missing rows: movies 1 and 2, the series' special, season 1 and season 2.
	res, err := e.a.RequestSearchNowAll(e.ctx, WantedQuery{Only: OnlyMissing})
	if err != nil {
		t.Fatal(err)
	}
	if res.Queued != 5 || res.Started != 2 || res.Pending != 3 || len(res.Skipped) != 0 || res.NextAt == nil ||
		!res.NextAt.Equal(t0.Add(time.Hour+time.Second)) {
		t.Fatalf("request %+v next %v", res, res.NextAt)
	}
	if n := e.searchNowJobs(); n != 1 {
		t.Fatalf("%d drain jobs", n)
	}
	e.drain()
	if got := e.searches(); len(got) != 2 {
		t.Fatalf("first hour searches %+v", got)
	}
	list, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing})
	if err != nil || list.SearchedLastHour != 2 || list.SearchNowPending != 3 || list.SearchNowNextAt == nil ||
		!list.SearchNowNextAt.Equal(*res.NextAt) {
		t.Fatalf("list %+v %v", list, err)
	}
	e.advance(61 * time.Minute)
	e.drain()
	if got, left := e.searches(), e.count(`SELECT COUNT(*) FROM search_request`); len(got) != 4 || left != 1 {
		t.Fatalf("second hour: searches %+v, %d left", got, left)
	}
	if n := e.searchNowJobs(); n != 1 {
		t.Fatalf("%d drain jobs for the rest", n)
	}
	e.advance(61 * time.Minute)
	e.drain()
	if got, left := e.searches(), e.count(`SELECT COUNT(*) FROM search_request`); len(got) != 5 || left != 0 {
		t.Fatalf("third hour: searches %+v, %d left", got, left)
	}
	if n := e.searchNowJobs(); n != 0 {
		t.Fatalf("%d drain jobs with nothing queued", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE source <> ?`, SourceWanted); n != 0 {
		t.Fatalf("%d runs outside the wanted budget", n)
	}
}

// Search now ignores again_after but still counts: a row searched within it is searched again.
func TestSearchNowSearchesARecentlySearchedRow(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, loopOff(10))
	e.accept = false
	at := store.Millis(t0.Add(-30 * time.Minute))
	e.exec(`INSERT INTO acquire_run (source, title_id, version_id, version_key, target, reason, state, created_at,
		updated_at) VALUES ('wanted', 1, 101, 'movie', 'movie', 'missing', 'nothing', ?, ?)`, at, at)
	res, err := e.a.RequestSearchNow(e.ctx, OnlyMissing, []SearchNowItem{{TitleID: 1, Version: "movie"}})
	if err != nil || res.Queued != 1 || res.Started != 1 || res.Pending != 0 || res.NextAt != nil {
		t.Fatalf("request %+v %v", res, err)
	}
	e.drain()
	if got := e.searches(); len(got) != 1 || got[0].Title != "One" {
		t.Fatalf("searches %+v", got)
	}
	list, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing})
	if err != nil || list.SearchedLastHour != 2 {
		t.Fatalf("searched last hour %+v %v", list, err)
	}
}

// A cutoff request uses the cutoff selector; a season request starts one season search; a series named without a
// season is asked for season by season.
func TestSearchNowCutoffAndSeasons(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, loopOff(20))
	e.accept = false
	res, err := e.a.RequestSearchNow(e.ctx, OnlyCutoff, []SearchNowItem{{TitleID: 3, Version: "movie"},
		{TitleID: 1, Version: "movie"}})
	if err != nil || res.Started != 1 || len(res.Skipped) != 1 || res.Skipped[0].TitleID != 1 ||
		res.Skipped[0].Reason != skipNothing {
		t.Fatalf("cutoff %+v %v", res, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE title_id = 3 AND reason = 'cutoff' AND source = 'wanted'`); n != 1 {
		t.Fatalf("%d cutoff runs", n)
	}
	one := 1
	res, err = e.a.RequestSearchNow(e.ctx, OnlyMissing, []SearchNowItem{{TitleID: 10, Version: "series", Season: &one}})
	if err != nil || res.Queued != 1 || res.Started != 1 {
		t.Fatalf("season %+v %v", res, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE title_id = 10 AND target = 's01' AND season = 1`); n != 1 {
		t.Fatalf("%d season runs", n)
	}
	e.drain()
	got := e.searches()
	if last := got[len(got)-1]; last.Title != "Show" || last.Season != 1 || last.Episodes != 2 {
		t.Fatalf("season search %+v", got)
	}
	// A movie has no season; the whole series is the special and the two seasons.
	res, err = e.a.RequestSearchNow(e.ctx, OnlyMissing, []SearchNowItem{{TitleID: 2, Version: "movie", Season: &one},
		{TitleID: 10, Version: "series"}})
	if err != nil || res.Queued != 3 || res.Started != 3 || len(res.Skipped) != 1 || res.Skipped[0].TitleID != 2 {
		t.Fatalf("series %+v %v", res, err)
	}
}

// A request whose item has a download in flight, or was imported meanwhile, is dropped without a run.
func TestSearchNowDropsRequestsThatAskForNothing(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, loopOff(10))
	e.request(1, "movie", OnlyMissing, -1)
	e.request(2, "movie", OnlyMissing, -1)
	e.request(10, "series", OnlyMissing, 2)
	e.exec(`UPDATE version SET state = 'filled' WHERE id = 101`)
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (2, 102, 'movie', ?, 'x', 'pub', 'downloading', 0, 0)`, h("f"))
	e.pol.Versions["series"].Mode = policy.ModeShadow
	n, err := e.a.DrainSearchNow(e.ctx)
	if err != nil || n != 0 {
		t.Fatalf("drain %d %v", n, err)
	}
	if runs, left := e.count(`SELECT COUNT(*) FROM acquire_run`), e.count(`SELECT COUNT(*) FROM search_request`); runs != 0 || left != 0 {
		t.Fatalf("%d runs, %d requests left", runs, left)
	}
}

// Items of a library that is not active, and items with nothing wanted, are skipped with the reason.
func TestSearchNowSkipsWithReasons(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, loopOff(10))
	e.pol.Versions["series"].Mode = policy.ModeShadow
	res, err := e.a.RequestSearchNow(e.ctx, OnlyMissing, []SearchNowItem{{TitleID: 1, Version: "movie-shadow"},
		{TitleID: 10, Version: "series"}, {TitleID: 3, Version: "movie"}, {TitleID: 1, Version: "gone"}})
	if err != nil || res.Queued != 0 || res.Started != 0 || res.Pending != 0 {
		t.Fatalf("request %+v %v", res, err)
	}
	want := []SearchNowSkip{{1, "movie-shadow", skipNotActive}, {10, "series", skipNotActive}, {3, "movie", skipNothing},
		{1, "gone", skipNotActive}}
	if len(res.Skipped) != len(want) {
		t.Fatalf("skipped %+v", res.Skipped)
	}
	for i, s := range res.Skipped {
		if s != want[i] {
			t.Errorf("skip %d: %+v, want %+v", i, s, want[i])
		}
	}
	// "All" lists only the Live libraries' rows and queues them.
	all, err := e.a.RequestSearchNowAll(e.ctx, WantedQuery{Only: OnlyMissing})
	if err != nil || all.Queued != 2 || all.Started != 2 || len(all.Skipped) != 0 {
		t.Fatalf("all %+v %v", all, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE version_key = 'series'`); n != 0 {
		t.Fatalf("%d runs for a Watch-only library", n)
	}
}

// With new downloads off a request is refused as a whole, and what was queued before waits.
func TestSearchNowRefusedWhileDownloadsAreOff(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false, loopOff(10))
	if _, err := e.a.RequestSearchNow(e.ctx, OnlyMissing, []SearchNowItem{{TitleID: 1, Version: "movie"}}); !errors.Is(err, ErrDownloadsOff) {
		t.Fatalf("request: %v", err)
	}
	if _, err := e.a.RequestSearchNowAll(e.ctx, WantedQuery{Only: OnlyMissing}); !errors.Is(err, ErrDownloadsOff) {
		t.Fatalf("all: %v", err)
	}
	e.request(1, "movie", OnlyMissing, -1)
	if _, err := e.a.Wanted(e.ctx); err != nil {
		t.Fatal(err)
	}
	if runs, left := e.count(`SELECT COUNT(*) FROM acquire_run`), e.count(`SELECT COUNT(*) FROM search_request`); runs != 0 || left != 1 {
		t.Fatalf("%d runs, %d requests left", runs, left)
	}
}

// Pause (router.enabled false) keeps the queued Search now requests: drains start nothing and schedule nothing, and
// once downloads are on again they start, oldest first, under the hourly budget.
func TestPausedSearchesWaitAndStartOnResume(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false, loopOff(10))
	e.accept = false
	e.request(2, "movie", OnlyMissing, -1)
	e.advance(48 * time.Hour) // a long pause
	if _, err := e.a.Wanted(e.ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.a.HandleSearchNow(e.ctx); err != nil {
		t.Fatal(err)
	}
	if runs, reqs := e.count(`SELECT COUNT(*) FROM acquire_run`), e.count(`SELECT COUNT(*) FROM search_request`); runs != 0 ||
		reqs != 1 {
		t.Fatalf("paused: %d runs, %d requests", runs, reqs)
	}
	if n := e.searchNowJobs(); n != 0 {
		t.Fatalf("%d drain jobs while paused", n)
	}
	list, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing})
	if err != nil || list.SearchNowPending != 1 || list.SearchNowNextAt != nil {
		t.Fatalf("paused list %+v %v", list, err)
	}
	e.pol.Router.Enabled = true
	if err := e.a.ScheduleSearchNow(e.ctx); err != nil || e.searchNowJobs() != 1 {
		t.Fatalf("resume: %v, %d jobs", err, e.searchNowJobs())
	}
	sum, err := e.a.Wanted(e.ctx)
	if err != nil || sum.SearchNow != 1 {
		t.Fatalf("resumed round %+v %v", sum, err)
	}
	e.drain()
	if got := e.searches(); len(got) != 1 || got[0].Title != "Two" {
		t.Fatalf("searches %+v", got)
	}
	if reqs := e.count(`SELECT COUNT(*) FROM search_request`); reqs != 0 {
		t.Fatalf("%d requests left", reqs)
	}
	// Each ran once: the scheduled drain and a later round start nothing again.
	e.advance(61 * time.Minute)
	if _, err := e.a.Wanted(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if got := e.searches(); len(got) != 1 {
		t.Fatalf("searched again %+v", got)
	}
}

// A resume starts the waiting Search now request at once when the hour has budget left, instead of scheduling it for when the oldest search of the hour leaves the window.
func TestResumeStartsSearchNowWithBudgetLeft(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, loopOff(10))
	e.accept = false
	at := store.Millis(e.clock())
	for range 10 {
		e.exec(`INSERT INTO acquire_run (source, title_id, version_id, version_key, target, reason, state, created_at,
			updated_at) VALUES ('wanted', 3, 103, 'movie', 'movie', 'missing', 'nothing', ?, ?)`, at, at)
	}
	if res, err := e.a.RequestSearchNow(e.ctx, OnlyMissing, []SearchNowItem{{TitleID: 2, Version: "movie"}}); err != nil ||
		res.Pending != 1 {
		t.Fatalf("queue %+v %v", res, err)
	}
	e.pol.Router.Enabled = false
	e.advance(61 * time.Minute)
	e.drain() // the job queued before the pause runs while paused and schedules nothing
	if e.searchNowJobs() != 0 || e.count(`SELECT COUNT(*) FROM search_request`) != 1 {
		t.Fatal("paused queue")
	}
	e.pol.Router.Enabled = true
	if err := e.a.StartQueued(e.ctx); err != nil {
		t.Fatal(err)
	}
	if runs, left, jobs := e.count(`SELECT COUNT(*) FROM acquire_run WHERE title_id IN (1, 2)`),
		e.count(`SELECT COUNT(*) FROM search_request`), e.searchNowJobs(); runs != 1 || left != 0 || jobs != 0 {
		t.Fatalf("resume: %d runs, %d requests left, %d jobs", runs, left, jobs)
	}
	e.drain()
	if got := e.searches(); len(got) != 1 || got[0].Title != "Two" {
		t.Fatalf("searches %+v", got)
	}
}

// Asking again for a queued row keeps one request in its place; Cancel drops the queue and leaves started runs.
func TestSearchNowDuplicateAndClear(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, loopOff(1))
	e.accept = false
	if res, err := e.a.RequestSearchNow(e.ctx, OnlyMissing, []SearchNowItem{{TitleID: 1, Version: "movie"}}); err != nil || res.Started != 1 {
		t.Fatalf("first %+v %v", res, err)
	}
	two := 2
	items := []SearchNowItem{{TitleID: 2, Version: "movie"}, {TitleID: 10, Version: "series", Season: &two}}
	if res, err := e.a.RequestSearchNow(e.ctx, OnlyMissing, items); err != nil || res.Queued != 2 || res.Started != 0 || res.Pending != 2 {
		t.Fatalf("queued %+v %v", res, err)
	}
	if res, err := e.a.RequestSearchNow(e.ctx, OnlyMissing, items[:1]); err != nil || res.Queued != 0 || res.Pending != 2 {
		t.Fatalf("again %+v %v", res, err)
	}
	if first := e.count(`SELECT title_id FROM search_request ORDER BY created_at, id LIMIT 1`); first != 2 {
		t.Fatalf("first in the queue: title %d", first)
	}
	if n, err := e.a.ClearSearchNow(e.ctx); err != nil || n != 2 {
		t.Fatalf("clear %d %v", n, err)
	}
	if runs, left := e.count(`SELECT COUNT(*) FROM acquire_run`), e.count(`SELECT COUNT(*) FROM search_request`); runs != 1 || left != 0 {
		t.Fatalf("%d runs, %d requests left", runs, left)
	}
}

// One budget, in order: Search now, then the loop's own selection.
func TestSearchNowDrainsBeforeTheLoop(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 1})
	e.accept = false
	e.request(2, "movie", OnlyMissing, -1)
	sum, err := e.a.Wanted(e.ctx)
	if err != nil || sum.SearchNow != 1 || sum.Queued != 0 {
		t.Fatalf("first round %+v %v", sum, err)
	}
	e.drain()
	if got := e.searches(); len(got) != 1 || got[0].Title != "Two" {
		t.Fatalf("searches %+v", got)
	}
	e.advance(61 * time.Minute)
	if sum, err = e.a.Wanted(e.ctx); err != nil || sum.SearchNow != 0 || sum.Queued != 1 {
		t.Fatalf("second round %+v %v", sum, err)
	}
}

// A queue found at start gets its drain scheduled; with an unused hour, a minute from now.
func TestSearchNowScheduledAtStart(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, loopOff(10))
	e.accept = false
	if err := e.a.ScheduleSearchNow(e.ctx); err != nil || e.searchNowJobs() != 0 {
		t.Fatalf("empty queue: %v, %d jobs", err, e.searchNowJobs())
	}
	e.request(1, "movie", OnlyMissing, -1)
	if err := e.a.ScheduleSearchNow(e.ctx); err != nil || e.searchNowJobs() != 1 {
		t.Fatalf("queue: %v, %d jobs", err, e.searchNowJobs())
	}
	if at := e.count(`SELECT run_at FROM job WHERE kind = ?`, JobSearchNow); at != int(store.Millis(t0.Add(time.Minute))) {
		t.Fatalf("run at %d", at)
	}
	e.advance(2 * time.Minute)
	e.drain()
	if got := e.searches(); len(got) != 1 || got[0].Title != "One" || e.searchNowJobs() != 0 {
		t.Fatalf("searches %+v, %d jobs", got, e.searchNowJobs())
	}
}

// Every consumer of the hourly budget checks and spends it under one lock: a request that arrives while a wanted
// round is between its count and its searches waits for the round, so per_hour holds.
func TestSearchNowDuringARoundKeepsTheHourlyBudget(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 1})
	e.accept = false
	done := make(chan error, 1)
	e.a.testHook = func(point string) {
		if point != "round" {
			return
		}
		go func() {
			_, err := e.a.RequestSearchNow(e.ctx, OnlyMissing, []SearchNowItem{{TitleID: 2, Version: "movie"}})
			done <- err
		}()
		select {
		case err := <-done:
			t.Errorf("the request did not wait for the round: %v", err)
			done <- err
		case <-time.After(200 * time.Millisecond):
		}
	}
	sum, err := e.a.Wanted(e.ctx)
	if err != nil || sum.Queued != 1 {
		t.Fatalf("round %+v %v", sum, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if runs, left := e.count(`SELECT COUNT(*) FROM acquire_run`), e.count(`SELECT COUNT(*) FROM search_request`); runs != 1 || left != 1 {
		t.Fatalf("%d runs with per_hour 1, %d requests waiting", runs, left)
	}
}

// Cancel during a bulk drain stops every request whose search has not started; the one already started stays.
func TestSearchNowCancelDuringADrain(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, loopOff(10))
	e.accept = false
	e.request(1, "movie", OnlyMissing, -1)
	e.request(2, "movie", OnlyMissing, -1)
	e.request(10, "series", OnlyMissing, 2)
	seen := 0
	e.a.testHook = func(point string) {
		if seen++; point == "request" && seen == 2 {
			if n, err := e.a.ClearSearchNow(e.ctx); err != nil || n != 2 {
				t.Errorf("clear %d %v", n, err)
			}
		}
	}
	n, err := e.a.DrainSearchNow(e.ctx)
	if err != nil || n != 1 {
		t.Fatalf("drain started %d %v", n, err)
	}
	if runs, first := e.count(`SELECT COUNT(*) FROM acquire_run`), e.count(`SELECT COUNT(*) FROM acquire_run WHERE title_id = 1`); runs != 1 || first != 1 {
		t.Fatalf("%d runs, %d for the first request", runs, first)
	}
}

// With the loop off every row waits as loop_off; what stops a search by hand is reported apart from it, and "Search
// all" queues only the rows without such a blocker.
func TestWantedListSearchBlockedBehindLoopOff(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, loopOff(10))
	e.accept = false
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (2, 102, 'movie', ?, 'x', 'pub', 'downloading', 0, 0)`, h("f"))
	list, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"1/movie": "", "2/movie": "in_flight"}
	searchable := 0
	for _, row := range list.Items {
		if row.Waiting != "loop_off" {
			t.Errorf("row %+v", row)
		}
		if w, ok := want[fmt.Sprintf("%d/%s", row.TitleID, row.Version)]; ok && row.SearchBlocked != w {
			t.Errorf("%d/%s: blocked %q, want %q", row.TitleID, row.Version, row.SearchBlocked, w)
		}
		if row.SearchBlocked == "" {
			searchable++
		}
	}
	// Movie 1 and the series' three rows.
	if list.Searchable != 4 || searchable != 4 || list.Total != 5 {
		t.Fatalf("searchable %d (%d rows), total %d", list.Searchable, searchable, list.Total)
	}
	res, err := e.a.RequestSearchNowAll(e.ctx, WantedQuery{Only: OnlyMissing})
	if err != nil || res.Queued != 4 || res.Started != 4 || len(res.Skipped) != 1 {
		t.Fatalf("all %+v %v", res, err)
	}
	off := newEnv(t, false, loopOff(10))
	if list, err = off.a.WantedList(off.ctx, WantedQuery{Only: OnlyMissing}); err != nil || list.Searchable != 0 ||
		list.Items[0].SearchBlocked != "downloads_off" {
		t.Fatalf("downloads off %+v %v", list, err)
	}
}
