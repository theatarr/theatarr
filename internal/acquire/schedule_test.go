package acquire

import (
	"errors"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// row inserts a schedule row due at at.
func (e *env) row(title int64, key, reason string, season int, at time.Time, why string, tries int) {
	e.t.Helper()
	e.exec(`INSERT INTO search_request (title_id, version_key, reason, season, created_at, next_at, tries, why)
		VALUES (?, ?, ?, ?, 0, ?, ?, ?)`, title, key, reason, season, store.Millis(at), tries, nullString(why))
}

// rowOf reads a schedule row: next_at, tries, why ("-" when there is none).
func (e *env) rowOf(title int64, key, reason string, season int) (time.Time, int, string) {
	e.t.Helper()
	var at int64
	var tries int
	var why string
	err := e.st.Reader().QueryRow(`SELECT next_at, tries, COALESCE(why, '') FROM search_request WHERE title_id = ?
		AND version_key = ? AND reason = ? AND season = ?`, title, key, reason, season).Scan(&at, &tries, &why)
	if err != nil {
		return time.Time{}, 0, "-"
	}
	return store.FromMillis(at), tries, why
}

func (e *env) round() *WantedSummary {
	e.t.Helper()
	sum, err := e.a.Round(e.ctx, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return sum
}

func TestNextSearchRule(t *testing.T) {
	t.Parallel()
	day := 24 * time.Hour
	for _, tc := range []struct {
		name      string
		row       schedRow
		kind      int
		fresh, on bool
		wait      time.Duration
		tries     int
		why       string
		keep      bool
	}{
		{"new content, first empty search", schedRow{reason: OnlyMissing}, runComplete, true, false, time.Hour, 1, whyNew, true},
		{"new content, fourth", schedRow{reason: OnlyMissing, why: whyNew, tries: 3}, runComplete, true, true, 8 * time.Hour, 4, whyNew, true},
		{"new content, sixth: daily", schedRow{reason: OnlyMissing, why: whyNew, tries: 5}, runComplete, true, true, day, 6, whyNew, true},
		{"new content after a failure", schedRow{reason: OnlyMissing, why: "stalled", tries: 2}, runComplete, true, false, time.Hour, 1, whyNew, true},
		{"cutoff never uses the ladder", schedRow{reason: OnlyCutoff}, runComplete, true, true, day, 0, "", true},
		{"old missing, switch on", schedRow{reason: OnlyMissing, why: whyAsked}, runComplete, false, true, day, 0, "", true},
		{"partial answer", schedRow{reason: OnlyCutoff}, runPartial, false, true, partialAgain, 0, "", true},
		{"switch off", schedRow{reason: OnlyMissing, why: "exhausted"}, runComplete, false, false, 0, 0, "", false},
		{"no answer keeps why", schedRow{reason: OnlyMissing, why: "stalled", tries: 1}, runInfra, false, false, time.Hour, 2, "stalled", true},
		{"no answer three times: ordinary rule", schedRow{reason: OnlyMissing, why: "stalled", tries: 3}, runInfra, false, false, 0, 0, "", false},
		{"no answer on new content: the ladder waits", schedRow{reason: OnlyMissing, why: whyNew, tries: 2}, runInfra, true, false, time.Hour, 2, whyNew, true},
	} {
		wait, tries, why, keep := nextSearch(tc.row, tc.kind, tc.fresh, tc.on, day)
		if wait != tc.wait || tries != tc.tries || why != tc.why || keep != tc.keep {
			t.Errorf("%s: %v %d %q %v, want %v %d %q %v", tc.name, wait, tries, why, keep, tc.wait, tc.tries, tc.why,
				tc.keep)
		}
	}
}

// Under per_hour: asked first, then the other early rows, then the backlog; each run stores its own row's why; a
// started row is not started again while its search waits.
func TestRoundOrder(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 2})
	e.accept = false
	e.exec(`UPDATE version SET monitored = 0 WHERE title_id = 10`)
	e.row(3, "movie", OnlyCutoff, -1, t0, whyAsked, 0)
	e.row(2, "movie", OnlyMissing, -1, t0.Add(-time.Hour), router.ReasonExhausted, 0)
	if sum := e.round(); sum.Due != 3 || sum.Queued != 2 {
		t.Fatalf("round %+v", sum)
	}
	if got := e.runs(); got != "102:movie 103:movie" {
		t.Fatalf("started %s", got)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE (version_id = 103 AND research = 'asked')
		OR (version_id = 102 AND research = 'exhausted')`); n != 2 {
		t.Fatalf("whys not stored per run")
	}
	e.a.set.PerHour = 10
	if sum := e.round(); sum.Queued != 1 || e.count(`SELECT COUNT(*) FROM acquire_run`) != 3 {
		t.Fatalf("placeholders: %+v, runs %s", sum, e.runs())
	}
	e.drain()
	e.advance(time.Hour)
	if sum := e.round(); sum.Queued != 0 {
		t.Fatalf("searched again within again_after: %+v", sum)
	}
}

// The owner's install: backlog switches off. Nothing starts by itself, every item reads as not scheduled, a failure
// still searches its item; with a switch turned on, at most per_hour searches start in any hour.
func TestRoundBacklogOffStartsNothing(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{Missing: new(false), CutoffUnmet: policy.CutoffOff, PerHour: 10})
	for id := 100; id < 150; id++ {
		e.exec(`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (?, 'movie', 'M', 0, 0)`, id)
		e.exec(`INSERT INTO title_release_date (title_id, source, type, date) VALUES (?, 'fixture', 'digital', '2024-01-01')`, id)
		e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (?, ?, 'movie', 'wanted', 0, 0)`, id+1000, id)
	}
	for range 8 {
		if sum := e.round(); sum.Queued != 0 {
			t.Fatalf("round %+v", sum)
		}
		e.advance(15 * time.Minute)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run`) + e.count(`SELECT COUNT(*) FROM search_request`); n != 0 {
		t.Fatalf("%d runs and rows", n)
	}
	list, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing, Limit: 200})
	if err != nil || list.Total < 50 || list.Counts["loop_off"] != list.Total {
		t.Fatalf("list %+v %v", list.Counts, err)
	}
	e.research(router.ResearchRequest{TitleID: 120, VersionID: 1120, Version: "movie", Reason: router.ReasonStalled})
	if got := e.runs(); got != "1120:movie" {
		t.Fatalf("failure started %s", got)
	}
	e.a.set.Missing = true
	start := e.clock()
	for range 4 {
		e.round()
		e.drain()
		e.advance(15 * time.Minute)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE source = 'wanted' AND search_run IS NULL`); n != 10 {
		t.Fatalf("%d searches in the first hour, per_hour 10", n)
	}
	e.advance(start.Add(time.Hour + time.Minute).Sub(e.clock()))
	e.round()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE source = 'wanted' AND search_run IS NULL`); n != 20 {
		t.Fatalf("%d searches after the second hour began", n)
	}
}

// A failure makes its item due once; an empty search of new content goes onto the ladder (why new, +1 h); a second
// failure inside again_after does not reset it, but runs marked asked or new do not block a failure re-search; old
// content with its switch off loses its row; one library's why is not stamped on another's run of the same search.
func TestFailureThenLadder(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{Missing: new(false), CutoffUnmet: policy.CutoffOff})
	e.accept = false
	// Series 10 season 2: episode 1004 aired a day ago (new).
	e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1004},
		Reason: router.ReasonExhausted})
	e.drain()
	if got := e.runs(); got != "201:s02e01" {
		t.Fatalf("searched %s", got)
	}
	if at, tries, why := e.rowOf(10, "series", OnlyMissing, 2); !at.Equal(t0.Add(time.Hour)) || tries != 1 || why != whyNew {
		t.Fatalf("ladder row %v %d %q", at, tries, why)
	}
	e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1004},
		Reason: router.ReasonStalled})
	if at, _, why := e.rowOf(10, "series", OnlyMissing, 2); !at.Equal(t0.Add(time.Hour)) || why != whyNew {
		t.Fatalf("a second failure reset the row: %v %q", at, why)
	}
	// Season 1 on the ladder (a new run), then a failure: due and searched.
	e.row(10, "series", OnlyMissing, 1, t0, whyNew, 1)
	e.round()
	e.drain()
	e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1001},
		Reason: router.ReasonStalled})
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE version_id = 201 AND season = 1 AND research = 'stalled'`); n != 1 {
		t.Fatalf("a new run blocked the failure re-search: %s", e.runs())
	}
	// Movie 1 asked for, then failed: searched again; old content, switch off: its row goes.
	if _, err := e.a.RequestSearchNow(e.ctx, OnlyMissing, []SearchNowItem{{TitleID: 1, Version: "movie"}}); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if _, _, why := e.rowOf(1, "movie", OnlyMissing, -1); why != "-" {
		t.Fatalf("asked row kept with the switch off: %q", why)
	}
	e.research(router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonLibrary})
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE version_id = 101 AND research = 'library'`); n != 1 {
		t.Fatalf("an asked run blocked the failure re-search: %s", e.runs())
	}
	if _, _, why := e.rowOf(1, "movie", OnlyMissing, -1); why != "-" {
		t.Fatalf("old content kept its row with the switch off: %q", why)
	}
	// Two libraries of movie 2 in one search: a failure in one, an asked row in the other.
	vp := *e.pol.Versions["movie"]
	e.pol.Versions["movie-b"] = &vp
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (122, 2, 'movie-b', 'wanted', 0, 0)`)
	e.row(2, "movie-b", OnlyMissing, -1, e.clock(), whyAsked, 0)
	e.research(router.ResearchRequest{TitleID: 2, VersionID: 102, Version: "movie", Reason: router.ReasonExhausted})
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE (version_id = 102 AND research = 'exhausted')
		OR (version_id = 122 AND research = 'asked')`); n != 2 || e.count(`SELECT COUNT(*) FROM acquire_run
		WHERE title_id = 2 AND search_run IS NOT NULL`) != 1 {
		t.Fatalf("whys of a shared search: %d", n)
	}
}

// Pause: Search now is refused, due rows wait and start once it is off; a loop search overtaken by Pause ends gated
// with its row due.
func TestPauseHoldsDueItems(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{Missing: new(false), CutoffUnmet: policy.CutoffOff})
	e.accept = false
	e.row(1, "movie", OnlyMissing, -1, t0, whyAsked, 0)
	e.pol.Router.Enabled = false
	if _, err := e.a.RequestSearchNow(e.ctx, OnlyMissing, []SearchNowItem{{TitleID: 2, Version: "movie"}}); !errors.Is(err, ErrDownloadsOff) {
		t.Fatalf("search now while paused: %v", err)
	}
	if sum := e.round(); sum.Off != "paused" || e.count(`SELECT COUNT(*) FROM acquire_run`) != 0 {
		t.Fatalf("paused round %+v", sum)
	}
	e.pol.Router.Enabled = true
	if sum := e.round(); sum.Queued != 1 {
		t.Fatalf("after resume %+v", sum)
	}
	e.pol.Router.Enabled = false
	e.advance(time.Minute)
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE state = 'skipped' AND infra = ?`, runGated); n != 1 || len(e.searches()) != 0 {
		t.Fatalf("overtaken run not gated: %s", e.runs())
	}
	if at, _, why := e.rowOf(1, "movie", OnlyMissing, -1); !at.Equal(e.clock()) || why != whyAsked {
		t.Fatalf("row after the gated run %v %q", at, why)
	}
	e.pol.Router.Enabled = true
	if sum := e.round(); sum.Queued != 1 {
		t.Fatalf("not started again after resume %+v", sum)
	}
}

// A search by name counts as a search of its item: an unscheduled item is not started minutes later but gets a row
// from that run, and a scheduled one is rescheduled when the search ends.
func TestRoundCountsManualSearch(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	e.accept = false
	e.exec(`UPDATE version SET monitored = 0 WHERE title_id IN (3, 10)`)
	e.row(2, "movie", OnlyMissing, -1, t0.Add(20*time.Minute), "", 0)
	for _, id := range []int64{1, 2} {
		if _, _, err := e.a.Submit(e.ctx, 0, Selection{Versions: []string{"movie"}, TitleIDs: []int64{id}}); err != nil {
			t.Fatal(err)
		}
	}
	e.drain()
	e.advance(30 * time.Minute)
	if sum := e.round(); sum.Queued != 0 {
		t.Fatalf("searched again after a search by name: %+v %s", sum, e.runs())
	}
	for _, id := range []int64{1, 2} {
		if at, _, _ := e.rowOf(id, "movie", OnlyMissing, -1); !at.Equal(t0.Add(24 * time.Hour)) {
			t.Errorf("movie %d: next search %v", id, at)
		}
	}
}

// Search now on a due regular row with the hour spent and the backlog switch off: the row becomes asked, is
// reported pending and is searched in the next hour.
func TestSearchNowTakesDueRegularRow(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{Missing: new(false), CutoffUnmet: policy.CutoffOff, PerHour: 1})
	e.accept = false
	e.exec(`INSERT INTO acquire_run (source, title_id, version_id, version_key, target, reason, state, created_at,
		updated_at) VALUES ('wanted', 2, 102, 'movie', 'movie', 'missing', 'nothing', ?, ?)`, store.Millis(t0), store.Millis(t0))
	e.row(1, "movie", OnlyMissing, -1, t0, "", 0)
	res, err := e.a.RequestSearchNow(e.ctx, OnlyMissing, []SearchNowItem{{TitleID: 1, Version: "movie"}})
	if err != nil || res.Queued != 1 || res.Started != 0 || res.Pending != 1 {
		t.Fatalf("search now %+v %v", res, err)
	}
	e.advance(time.Hour + time.Minute)
	e.round()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE version_id = 101 AND research = 'asked'`); n != 1 {
		t.Fatalf("asked row not searched in the next hour: %s", e.runs())
	}
}

// Runs of the retry chains before the schedule (retry_new, fresh) do not block a failure re-search.
func TestLegacyRunsDoNotBlockFailure(t *testing.T) {
	t.Parallel()
	for i, marker := range []string{"retry_new", "fresh"} {
		e := newEnv(t, true, &policy.Wanted{Missing: new(false), CutoffUnmet: policy.CutoffOff})
		e.exec(`INSERT INTO acquire_run (source, title_id, version_id, version_key, target, reason, state, research,
			created_at, updated_at) VALUES ('wanted', 1, 101, 'movie', 'movie', 'missing', 'nothing', ?, ?, ?)`, marker,
			store.Millis(t0.Add(-time.Hour)), store.Millis(t0.Add(-time.Hour)))
		e.research(router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonStalled})
		if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = 'stalled'`); n != 1 {
			t.Fatalf("%d: a %s run blocked the failure re-search", i, marker)
		}
	}
}

// A search already queued is not started again: Search now twice before the worker runs, a round while a search
// by name waits, a failure event meanwhile.
func TestQueuedSearchNotDuplicated(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{Missing: new(false), CutoffUnmet: policy.CutoffOff})
	for range 2 {
		if _, err := e.a.RequestSearchNow(e.ctx, OnlyMissing, []SearchNowItem{{TitleID: 1, Version: "movie"}}); err != nil {
			t.Fatal(err)
		}
	}
	e.research(router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonStalled})
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE version_id = 101`); n != 1 {
		t.Fatalf("%d searches of movie 1: %s", n, e.runs())
	}
	if _, _, err := e.a.Submit(e.ctx, 0, Selection{Versions: []string{"movie"}, TitleIDs: []int64{2}}); err != nil {
		t.Fatal(err)
	}
	e.row(2, "movie", OnlyMissing, -1, t0, router.ReasonExhausted, 0)
	e.advance(10 * time.Minute)
	e.round()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE version_id = 102`); n != 1 {
		t.Fatalf("a round duplicated a queued search by name: %s", e.runs())
	}
	if _, _, why := e.rowOf(2, "movie", OnlyMissing, -1); why != router.ReasonExhausted {
		t.Fatalf("row changed: %q", why)
	}
}
