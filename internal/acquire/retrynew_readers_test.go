package acquire

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// The rule router.wanted.retry_new is built to: nothing its chains write is read by an ordinary path, and turning
// the switch off removes their queued work. One test per reader of the run history, then the cancellation.

// chainRun records a search of a retry_new chain as it stands in the run history: research retry_new, created ago,
// and the finished request of the chain that started it (the job table keeps it 14 days).
func (e *env) chainRun(title, version int64, key, name, state string, infra int, ago time.Duration) {
	e.t.Helper()
	at := store.Millis(e.clock().Add(-ago))
	e.prunedChainRun(title, version, key, name, state, infra, ago)
	e.exec(`INSERT INTO job (kind, payload, state, run_at, created_at, updated_at, finished_at)
		VALUES (?, json_object('title_id', ?, 'version_id', ?, 'version', ?, 'reason', ?, 'retry_new', json('true')),
		'done', ?, ?, ?, ?)`, router.JobResearch, title, version, key, router.ReasonFresh, at, at, at, at)
}

// prunedChainRun is chainRun without the request: what is left once the job table has pruned it.
func (e *env) prunedChainRun(title, version int64, key, name, state string, infra int, ago time.Duration) {
	e.t.Helper()
	at := store.Millis(e.clock().Add(-ago))
	e.exec(`INSERT INTO acquire_run (source, title_id, version_id, version_key, target, reason, state, research, infra,
		created_at, updated_at) VALUES ('wanted', ?, ?, ?, ?, 'missing', ?, ?, ?, ?, ?)`, title, version, key, name,
		state, researchRetryNew, infra, at, at)
}

// chainRuns records, for movie 1, every kind of run a chain can leave: searched, partial, no answer, gated, and one
// still queued.
func (e *env) chainRuns() {
	e.t.Helper()
	e.chainRun(1, 101, "movie", "movie", "nothing", runComplete, 50*time.Minute)
	e.chainRun(1, 101, "movie", "movie", "nothing", runPartial, 40*time.Minute)
	e.chainRun(1, 101, "movie", "movie", "failed", runInfra, 30*time.Minute)
	e.chainRun(1, 101, "movie", "movie", "failed", runInfra, 20*time.Minute)
	e.chainRun(1, 101, "movie", "movie", "skipped", runGated, 10*time.Minute)
	e.chainRun(1, 101, "movie", "movie", "queued", runComplete, 5*time.Minute)
}

// Reader: a re-search's again_after (researchBlocked). A chain's searches of the target do not hold a re-search
// after a failure back; the re-search's own run then does.
func TestRetryNewRunsDoNotHoldBackAReSearch(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, addTimeOnly(100))
	e.accept = false
	e.chainRuns()
	p := router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonStalled}
	e.research(p)
	e.drain()
	if got := e.searches(); len(got) != 1 || got[0].Title != "One" {
		t.Fatalf("searches %+v, want the re-search of One after a chain's searches", got)
	}
	e.research(p)
	e.drain()
	if got := e.searches(); len(got) != 1 {
		t.Fatalf("searches %+v, want the second re-search held back by the first", got)
	}
}

// infraAgain is when the search asked for again after no answer is due.
func (e *env) infraAgain() time.Time {
	e.t.Helper()
	var at int64
	if err := e.st.Reader().QueryRow(`SELECT run_at FROM job WHERE kind = ? AND state = 'queued'
		AND dedupe_key LIKE 'acquire.research/infra/%'`, router.JobResearch).Scan(&at); err != nil {
		e.t.Fatalf("no search asked for again: %v", err)
	}
	return store.FromMillis(at)
}

// Reader: the retry budget after no answer (infraRuns). A chain's unanswered searches do not count in a re-search's
// budget (its first is asked for again after infraBackoff[0]), nor an ordinary one's in the chain's.
func TestRetryNewRunsHaveTheirOwnRetryBudget(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, addTimeOnly(100))
	e.noAnswer = true
	e.chainRuns()
	e.research(router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonStalled})
	e.drain()
	if at := e.infraAgain(); !at.Equal(e.clock().Add(infraBackoff[0])) {
		t.Fatalf("re-search asked for again at %v, want %v", at, e.clock().Add(infraBackoff[0]))
	}

	e = retryNewEnv(t, &policy.Wanted{PerHour: 100, RetryNew: true})
	e.round()
	at := store.Millis(e.clock())
	for range len(infraBackoff) { // a re-search after a failure, its budget spent
		e.exec(`INSERT INTO acquire_run (source, title_id, version_id, version_key, target, reason, season, episodes,
			state, research, infra, created_at, updated_at) VALUES ('wanted', 10, 201, 'series', 's02e01', 'missing', 2,
			'[1004]', 'failed', ?, ?, ?, ?)`, router.ReasonStalled, runInfra, at, at)
	}
	e.noAnswer = true
	if step := e.runFollowUp(); step != 1 {
		t.Fatalf("ran step %d", step)
	}
	if at := e.infraAgain(); !at.Equal(e.clock().Add(infraBackoff[0])) {
		t.Fatalf("the chain's search asked for again at %v, want %v", at, e.clock().Add(infraBackoff[0]))
	}
}

// Reader: an add-time intent's "searched within again_after, or a search of it queued" (drainIntent). A chain's
// searches do not settle the intent: its own search starts.
func TestRetryNewRunsDoNotSettleAnAddTimeIntent(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, addTimeOnly(100))
	e.accept = false
	at := store.Millis(e.clock().Add(-2 * time.Hour))
	e.exec(`INSERT INTO follow_search_intent (title_id, version_key, all_items, created_at, updated_at)
		VALUES (1, 'movie', 1, ?, ?)`, at, at)
	e.chainRuns()
	if n, err := e.a.DrainFollowSearches(e.ctx); err != nil || n != 1 {
		t.Fatalf("add-time searches started %d: %v", n, err)
	}
	e.drain()
	if got := e.searches(); len(got) != 1 || got[0].Title != "One" {
		t.Fatalf("searches %+v, want the add-time search of One", got)
	}
}

// Reader: the wanted loop's again_after (lastSearched, complete and partial). A chain's searches do not count as the
// loop's: the item is selected as if they had not run, while an ordinary search of another item holds that one back.
func TestRetryNewRunsDoNotCountAsTheLoopsSearch(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 100})
	e.accept = false
	e.chainRuns()
	at := store.Millis(e.clock().Add(-time.Hour))
	e.exec(`INSERT INTO acquire_run (source, title_id, version_id, version_key, target, reason, state, created_at,
		updated_at) VALUES ('wanted', 2, 102, 'movie', 'movie', 'missing', 'nothing', ?, ?)`, at, at)
	e.round()
	var one, two bool
	for _, s := range e.searches() {
		one, two = one || s.Title == "One", two || s.Title == "Two"
	}
	if !one || two {
		t.Fatalf("searched One %v, Two %v; want One (only a chain searched it) and not Two", one, two)
	}
}

// firstRun is the target of the first run after id.
func (e *env) firstRun(after int64) (version int64, name string) {
	e.t.Helper()
	if err := e.st.Reader().QueryRow(`SELECT version_id, target FROM acquire_run WHERE id > ? ORDER BY id LIMIT 1`,
		after).Scan(&version, &name); err != nil {
		e.t.Fatal(err)
	}
	return version, name
}

// Reader: the wanted loop's order (lastSearched, the latest attempt of any kind). A chain's runs of a target, a gated
// one too, do not move it back in the order: with one search an hour the loop takes the item it takes without them.
func TestRetryNewRunsDoNotChangeTheLoopsOrder(t *testing.T) {
	t.Parallel()
	base := newEnv(t, true, &policy.Wanted{PerHour: 1})
	base.accept = false
	base.round()
	version, name := base.firstRun(0)
	var title int64
	var key string
	if err := base.st.Reader().QueryRow(`SELECT title_id, version_key FROM acquire_run ORDER BY id LIMIT 1`).
		Scan(&title, &key); err != nil {
		t.Fatal(err)
	}

	e := newEnv(t, true, &policy.Wanted{PerHour: 1})
	e.accept = false
	// Older than the hour: they are not in the hourly count either way.
	e.chainRun(title, version, key, name, "skipped", runGated, 2*time.Hour)
	e.chainRun(title, version, key, name, "failed", runInfra, 3*time.Hour)
	last := int64(e.count(`SELECT MAX(id) FROM acquire_run`))
	e.round()
	if v, n := e.firstRun(last); v != version || n != name {
		t.Fatalf("the loop took %d/%s first, want %d/%s as without the chain's runs", v, n, version, name)
	}
}

// Reader: a Search now request's "a search of it is already queued or running" (searchNowReady). A chain's queued
// search does not drop the request: its search starts.
func TestRetryNewQueuedSearchNeverDropsASearchNowRequest(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, loopOff(100))
	e.accept = false
	e.chainRuns()
	e.request(1, "movie", OnlyMissing, -1)
	if n, err := e.a.DrainSearchNow(e.ctx); err != nil || n != 1 {
		t.Fatalf("search now started %d: %v", n, err)
	}
	e.drain()
	if got := e.searches(); len(got) != 1 || got[0].Title != "One" {
		t.Fatalf("searches %+v, want the requested search of One", got)
	}

	// The same with a real chain: its step queued a search of s02e01, the switch is turned off, the user asks.
	e = retryNewEnv(t, &policy.Wanted{PerHour: 100, RetryNew: true})
	e.round()
	p := router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Reason: router.ReasonFresh,
		Step: 1, MissedAt: e.clock(), RetryNew: true}
	e.exec(`DELETE FROM job WHERE kind = ?`, router.JobResearch)
	e.advance(time.Hour)
	e.research(p)
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ? AND state = 'queued' AND target = 's02e01'`,
		researchRetryNew); n != 1 {
		t.Fatalf("%d chain searches queued", n)
	}
	e.a.set.RetryNew = false
	before := len(e.searches())
	e.request(10, "series", OnlyMissing, 2)
	if n, err := e.a.DrainSearchNow(e.ctx); err != nil || n != 1 {
		t.Fatalf("search now started %d with a chain's search queued: %v", n, err)
	}
	e.drain()
	if got := e.searches()[before:]; len(got) != 1 || got[0].Season != 2 || got[0].Ep != 1 {
		t.Fatalf("searches %+v, want the requested search of s02e01 only", got)
	}
}

// Reader: router.wanted.per_hour (wantedUsed, budgetFreeAt; the follow-up's count is wantedUsed). With the switch
// off a chain's searches of the hour take no slot and do not set when the hour frees; while it is on the hour is one
// budget.
func TestRetryNewRunsLeaveTheHourWhenTheSwitchIsOff(t *testing.T) {
	t.Parallel()
	for _, on := range []bool{false, true} {
		e := newEnv(t, true, loopOff(1))
		e.accept = false
		e.a.set.RetryNew = on
		e.chainRun(2, 102, "movie", "movie", "nothing", runComplete, 10*time.Minute)
		res, err := e.a.RequestSearchNow(e.ctx, OnlyMissing, []SearchNowItem{{TitleID: 1, Version: "movie"},
			{TitleID: 2, Version: "movie"}})
		if err != nil {
			t.Fatal(err)
		}
		started, next := 1, e.clock().Add(time.Hour+time.Second) // the request's own search leaves the hour
		if on {
			started, next = 0, e.clock().Add(50*time.Minute+time.Second) // the chain's does
		}
		if res.Started != started || res.Pending != 2-started || res.NextAt == nil || !res.NextAt.Equal(next) {
			t.Fatalf("switch %v: %+v next %v, want %d started and next %v", on, res, res.NextAt, started, next)
		}
		if used, err := e.a.wantedUsed(e.ctx, e.clock()); err != nil || used != 1 {
			t.Fatalf("switch %v: %d searches in the hour: %v", on, used, err)
		}
	}

	// A step of the chain after an exhausted route, the hour holding only a retry_new chain's search: it searches.
	e := retryNewEnv(t, &policy.Wanted{PerHour: 1, Missing: new(bool), CutoffUnmet: policy.CutoffOff})
	e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1004},
		Reason: router.ReasonExhausted})
	e.drain()
	e.advance(50 * time.Minute)
	e.chainRun(2, 102, "movie", "movie", "nothing", runComplete, time.Minute)
	before := len(e.searches())
	if step := e.runFollowUp(); step != 1 {
		t.Fatalf("ran step %d", step)
	}
	if n := len(e.searches()) - before; n != 1 {
		t.Fatalf("the exhausted chain's step ran %d searches, want 1", n)
	}
}

// chainStep runs the first step of the chain the round started, leaving its search queued.
func (e *env) chainStep() {
	e.t.Helper()
	p := router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Reason: router.ReasonFresh,
		Step: 1, MissedAt: e.clock(), RetryNew: true}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'
		AND json_extract(payload, '$.retry_new') = 1 AND json_extract(payload, '$.step') = 1`, router.JobResearch); n != 1 {
		e.t.Fatalf("%d first steps queued", n)
	}
	e.exec(`DELETE FROM job WHERE kind = ?`, router.JobResearch)
	e.advance(time.Hour)
	e.research(p)
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ? AND state = 'queued'`, researchRetryNew); n != 1 {
		e.t.Fatalf("%d chain searches queued", n)
	}
}

// The switch is looked at before the version: with the switch and the library both turned off while a chain's search
// is queued, its run ends gated (no search, no slot of the hour), not as an ordinary skipped search.
func TestRetryNewGateComesBeforeTheInactiveLibrary(t *testing.T) {
	t.Parallel()
	e := retryNewEnv(t, &policy.Wanted{PerHour: 100, RetryNew: true})
	e.round()
	e.chainStep()
	e.a.set.RetryNew = false
	e.pol.Versions["series"].Mode = policy.ModeShadow
	before := len(e.searches())
	e.drain()
	var state, message string
	var infra int
	if err := e.st.Reader().QueryRow(`SELECT state, infra, message FROM acquire_run WHERE research = ?`,
		researchRetryNew).Scan(&state, &infra, &message); err != nil {
		t.Fatal(err)
	}
	if state != "skipped" || infra != runGated || !strings.Contains(message, "retry_new") || len(e.searches()) != before {
		t.Fatalf("run %s infra %d (%s), %d searches; want it gated by the switch", state, infra, message,
			len(e.searches())-before)
	}
}

// queuedJobs counts the queued re-search requests and search jobs.
func (e *env) queuedJobs() int {
	e.t.Helper()
	return e.count(`SELECT COUNT(*) FROM job WHERE kind IN (?, ?) AND state = 'queued'`, router.JobResearch, JobSearch)
}

// Turning the switch off cancels the chains' queued work: the next step, a queued search with its run, and a search
// asked for again after no answer. Ordinary work queued beside it (a re-search, its search, the chain after an
// exhausted route) is not touched and runs. While the switch is on nothing is cancelled.
func TestRetryNewOffCancelsQueuedWork(t *testing.T) {
	t.Parallel()
	for _, what := range []string{"asked for again", "search"} {
		e := retryNewEnv(t, &policy.Wanted{PerHour: 100, RetryNew: true})
		e.round()
		e.noAnswer = true
		if step := e.runFollowUp(); step != 1 { // step 2 and the search asked for again are queued
			t.Fatalf("ran step %d", step)
		}
		if what == "search" {
			e.advance(infraBackoff[0] + time.Second)
			if ran, err := e.w.RunOne(e.ctx); err != nil || !ran { // the request asked for again queues its search
				t.Fatalf("ran=%v err=%v", ran, err)
			}
			if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ? AND state = 'queued'`,
				researchRetryNew); n != 1 {
				t.Fatalf("%d chain searches queued", n)
			}
		}
		e.noAnswer = false
		// Ordinary work: a re-search of the episode (its search queued) and the chain after the exhausted route.
		e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1004},
			Reason: router.ReasonExhausted})
		if n := e.queuedJobs(); n != 4 {
			t.Fatalf("%s: %d jobs queued, want 2 of the chain and 2 ordinary", what, n)
		}
		if n, err := e.a.CancelRetryNew(e.ctx); err != nil || n != 0 || e.queuedJobs() != 4 {
			t.Fatalf("%s: switch on: %d cancelled, %d queued: %v", what, n, e.queuedJobs(), err)
		}
		e.a.set.RetryNew = false
		if n, err := e.a.CancelRetryNew(e.ctx); err != nil || n != 2 {
			t.Fatalf("%s: %d cancelled: %v", what, n, err)
		}
		if n := e.count(`SELECT COUNT(*) FROM job WHERE state IN ('queued', 'running')
			AND json_extract(payload, '$.retry_new') = 1`); n != 0 {
			t.Fatalf("%s: %d chain requests left", what, n)
		}
		if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ? AND (state IN ('queued', 'searching')
			OR state = 'skipped' AND infra <> ?)`, researchRetryNew, runGated); n != 0 {
			t.Fatalf("%s: %d chain runs left queued or not gated", what, n)
		}
		if r, x := e.queuedChains(); r != 0 || x != 1 || e.queuedJobs() != 2 {
			t.Fatalf("%s: %d retry_new and %d exhausted steps, %d jobs queued; want the ordinary two", what, r, x,
				e.queuedJobs())
		}
		chain := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ?`, researchRetryNew)
		before := len(e.searches())
		e.hours(3)
		if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ?`, researchRetryNew); n != chain {
			t.Fatalf("%s: %d chain runs after the switch was off, %d before", what, n, chain)
		}
		// The re-search at once and the exhausted chain's steps 1 and 2.
		if n := len(e.searches()) - before; n != 3 {
			t.Fatalf("%s: %d ordinary searches in three hours, want 3", what, n)
		}
	}

	// An install that never had the switch on: nothing to cancel, nothing touched.
	e := retryNewEnv(t, &policy.Wanted{PerHour: 100, Missing: new(bool), CutoffUnmet: policy.CutoffOff})
	e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1004},
		Reason: router.ReasonExhausted})
	if n, err := e.a.CancelRetryNew(e.ctx); err != nil || n != 0 || e.queuedJobs() != 2 {
		t.Fatalf("never on: %d cancelled, %d queued: %v", n, e.queuedJobs(), err)
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE state = 'done'`); n != 0 {
		t.Fatalf("never on: %d jobs ended", n)
	}
}

// What a handler that started with the switch on still queues after the cancellation (a step, a search, a search
// asked for again) ends without searching once it runs with the switch off, and its runs are gated.
func TestRetryNewLateEnqueueSearchesNothing(t *testing.T) {
	t.Parallel()
	e := retryNewEnv(t, &policy.Wanted{PerHour: 100, RetryNew: true})
	e.round()
	e.a.set.RetryNew = false
	if n, err := e.a.CancelRetryNew(e.ctx); err != nil || n != 1 || e.queuedJobs() != 0 {
		t.Fatalf("%d cancelled, %d queued: %v", n, e.queuedJobs(), err)
	}
	// The old runtime: it captured the switch on.
	e.a.set.RetryNew = true
	season := 2
	late := router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Reason: router.ReasonFresh,
		MissedAt: e.clock(), RetryNew: true}
	if err := e.a.queueFollowUp(e.ctx, late, 1, e.clock().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := e.a.start(e.ctx, SourceWanted, 0, target{titleID: 10, versionID: 201, key: "series", name: "s02e01",
		reason: OnlyMissing, season: &season, episodes: []int64{1004}}, researchRetryNew); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE acquire_run SET state = 'failed', infra = ? WHERE research = ?`, runInfra, researchRetryNew)
	e.exec(`UPDATE job SET state = 'done' WHERE kind = ?`, JobSearch)
	run := int64(e.count(`SELECT MAX(id) FROM acquire_run`))
	if err := e.a.retryInfraFailed(e.ctx, []int64{run}); err != nil {
		t.Fatal(err)
	}
	if err := e.a.start(e.ctx, SourceWanted, 0, target{titleID: 10, versionID: 201, key: "series", name: "s02e01",
		reason: OnlyMissing, season: &season, episodes: []int64{1004}}, researchRetryNew); err != nil {
		t.Fatal(err)
	}
	if n := e.queuedJobs(); n != 3 {
		t.Fatalf("%d jobs queued late, want a step, a search and a search asked for again", n)
	}
	// The published runtime: the switch is off.
	e.a.set.RetryNew = false
	before := len(e.searches())
	e.hours(3)
	if n := len(e.searches()) - before; n != 0 {
		t.Fatalf("%d searches with the switch off", n)
	}
	if n := e.queuedJobs(); n != 0 {
		t.Fatalf("%d jobs still queued", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ? AND state = 'skipped' AND infra = ?`,
		researchRetryNew, runGated); n != 1 {
		t.Fatalf("%d gated runs, want the late search", n)
	}
}

// plan is the query plan of q, one step a line.
func (e *env) plan(q string, args ...any) string {
	e.t.Helper()
	rows, err := e.st.Reader().QueryContext(e.ctx, `EXPLAIN QUERY PLAN `+q, args...)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, detail)
	}
	return strings.Join(out, "\n")
}

// An install that never had the switch on reads the run history with the queries, and the query plans, it had
// without the switch: the loop's "last searched" is one scan the index answers alone, and an add-time intent's retry
// budget reads the index alone. The queries written out here are the ones before the switch existed.
func TestRetryNewNeverOnKeepsTheQueryPlans(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 100})
	e.accept = false
	e.round() // ordinary runs, re-searches and their jobs
	e.research(router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonStalled})
	e.drain()

	const last = `SELECT version_id, target,
		COALESCE(MAX(CASE WHEN infra = 0 THEN created_at END), 0), COALESCE(MAX(CASE WHEN infra = 2 THEN created_at END), 0),
		MAX(created_at) FROM acquire_run GROUP BY version_id, target`
	reads, chained, err := e.a.lastSearchedReads(e.ctx)
	if err != nil || len(reads) != 1 || len(chained) != 0 {
		t.Fatalf("never on: %d reads, chained %v: %v", len(reads), chained, err)
	}
	want := e.plan(last)
	if !strings.Contains(want, "SCAN acquire_run USING COVERING INDEX acquire_run_target") {
		t.Fatalf("the plan without the switch is not the covering scan:\n%s", want)
	}
	if got := e.plan(reads[0]); got != want {
		t.Fatalf("never on: last searched plan\n%s\nwant\n%s", got, want)
	}
	if got := e.plan(lastSearchedSQL(ordinaryRuns)); got == want {
		t.Fatalf("the filtered query has the same plan: this test tells nothing\n%s", got)
	}

	const infra = `SELECT COUNT(*), COALESCE(MAX(created_at), 0) FROM acquire_run WHERE version_id = ? AND target = ?
		AND infra = 1 AND created_at > MAX(?, COALESCE((SELECT MAX(created_at) FROM acquire_run WHERE version_id = ?
		AND target = ? AND infra NOT IN (1, 3) AND state NOT IN ('queued', 'searching')), 0))`
	own, err := e.a.ordinaryOf(e.ctx, 101)
	if err != nil || own != "" {
		t.Fatalf("never on: an intent's retry budget reads %q: %v", own, err)
	}
	args := []any{101, "movie", 0, 101, "movie"}
	want = e.plan(infra, args...)
	if !strings.Contains(want, "SEARCH acquire_run USING COVERING INDEX acquire_run_target") {
		t.Fatalf("the plan without the switch does not read the index alone:\n%s", want)
	}
	if got := e.plan(infraRunsSQL(own), args...); got != want {
		t.Fatalf("never on: retry budget plan\n%s\nwant\n%s", got, want)
	}
	if got := e.plan(infraRunsSQL(ordinaryRuns), args...); got == want {
		t.Fatalf("the filtered query has the same plan: this test tells nothing\n%s", got)
	}

	// A chain's request in the job table: its version alone is read again without the chain's runs.
	e.chainRun(1, 101, "movie", "movie", "nothing", runComplete, time.Minute)
	reads, chained, err = e.a.lastSearchedReads(e.ctx)
	if err != nil || len(reads) != 2 || len(chained) != 1 || !chained[101] || e.plan(reads[0]) != e.plan(last) {
		t.Fatalf("after a chain: %d reads, chained %v: %v", len(reads), chained, err)
	}
	if own, err := e.a.ordinaryOf(e.ctx, 101); err != nil || own != ordinaryRuns {
		t.Fatalf("after a chain: its version reads %q: %v", own, err)
	}
	if own, err := e.a.ordinaryOf(e.ctx, 102); err != nil || own != "" {
		t.Fatalf("after a chain: another version reads %q: %v", own, err)
	}
}

// With the switch off, lastSearched leaves a chain's runs out for the versions a chain has a request for: a target
// only a chain searched is not in it, one with an ordinary run too keeps the ordinary run's times, and other versions
// read as without the switch. A chain's run whose requests the job table has pruned (after 14 days) reads as an
// ordinary run.
func TestRetryNewLastSearchedWithTheSwitchOff(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 100})
	ordinary := func(title, version int64, ago time.Duration) int64 {
		at := store.Millis(e.clock().Add(-ago))
		e.exec(`INSERT INTO acquire_run (source, title_id, version_id, version_key, target, reason, state, created_at,
			updated_at) VALUES ('wanted', ?, ?, 'movie', 'movie', 'missing', 'nothing', ?, ?)`, title, version, at, at)
		return at
	}
	e.chainRuns() // movie 1: chain runs only
	two := ordinary(2, 102, 3*time.Hour)
	e.chainRun(2, 102, "movie", "movie", "nothing", runComplete, time.Hour)
	three := ordinary(3, 103, 2*time.Hour)
	e.prunedChainRun(1, 111, "movie-shadow", "movie", "nothing", runComplete, 30*24*time.Hour)
	got, err := e.a.lastSearched(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[runKey(101, "movie")]; ok {
		t.Fatalf("a target only a chain searched is in last searched: %+v", got)
	}
	if l := got[runKey(102, "movie")]; l != (lastRuns{complete: two, any: two}) {
		t.Fatalf("version 102: %+v, want its ordinary run at %d", l, two)
	}
	if l := got[runKey(103, "movie")]; l != (lastRuns{complete: three, any: three}) {
		t.Fatalf("version 103: %+v, want its run at %d", l, three)
	}
	if l := got[runKey(111, "movie")]; l.complete == 0 {
		t.Fatalf("a chain's run without a request left is not read as a run: %+v", got)
	}
	// The same with the switch on, except that it needs no request to tell a chain's run.
	e.a.set.RetryNew = true
	on, err := e.a.lastSearched(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	delete(got, runKey(111, "movie"))
	if len(on) != len(got) || on[runKey(102, "movie")] != got[runKey(102, "movie")] ||
		on[runKey(103, "movie")] != got[runKey(103, "movie")] {
		t.Fatalf("switch on: %+v, want %+v", on, got)
	}
}

// A chain's search that a shutdown interrupted has its job queued again while its run still says searching: turning
// the switch off cancels that job and ends the run gated, so that nothing of the chain is left for another build
// to run. A chain's search that is running (its job running) is left to end by itself.
func TestRetryNewOffCancelsAnInterruptedSearch(t *testing.T) {
	t.Parallel()
	e := retryNewEnv(t, &policy.Wanted{PerHour: 100, RetryNew: true})
	job := func(run int64, state string) {
		e.exec(`INSERT INTO job (kind, payload, dedupe_key, state, run_at, created_at, updated_at)
			VALUES (?, json_object('run_id', ?), ?, ?, 0, 0, 0)`, JobSearch, run, JobSearch+"/"+strconv.FormatInt(run, 10),
			state)
	}
	run := func(research any) int64 {
		e.exec(`INSERT INTO acquire_run (source, title_id, version_id, version_key, target, reason, state, research,
			claim, created_at, updated_at) VALUES ('wanted', 10, 201, 'series', 's02e01', 'missing', 'searching', ?, 'job:1/1',
			0, 0)`, research)
		return int64(e.count(`SELECT MAX(id) FROM acquire_run`))
	}
	interrupted, running, ordinary := run(researchRetryNew), run(researchRetryNew), run(nil)
	job(interrupted, "queued")
	job(running, "running")
	job(ordinary, "queued")
	if n, err := e.a.CancelRetryNew(e.ctx); err != nil || n != 0 {
		t.Fatalf("switch on: %d cancelled: %v", n, err)
	}
	e.a.set.RetryNew = false
	if n, err := e.a.CancelRetryNew(e.ctx); err != nil || n != 1 {
		t.Fatalf("%d cancelled, want the interrupted search's job: %v", n, err)
	}
	for id, want := range map[int64][2]string{interrupted: {"skipped", "done"}, running: {"searching", "running"},
		ordinary: {"searching", "queued"}} {
		var state, jobState string
		var infra int
		if err := e.st.Reader().QueryRow(`SELECT r.state, r.infra, j.state FROM acquire_run r JOIN job j
			ON j.dedupe_key = ? || '/' || r.id WHERE r.id = ?`, JobSearch, id).Scan(&state, &infra, &jobState); err != nil {
			t.Fatal(err)
		}
		if state != want[0] || jobState != want[1] || (id == interrupted) != (infra == runGated) {
			t.Fatalf("run %d: %s (infra %d), job %s; want %s, job %s", id, state, infra, jobState, want[0], want[1])
		}
	}
	// The ordinary interrupted search runs; nothing of the chain does.
	before := len(e.searches())
	e.drain()
	if got := e.searches()[before:]; len(got) != 1 {
		t.Fatalf("%d searches after the cancellation, want the ordinary one", len(got))
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE id = ? AND state = 'skipped'`, interrupted); n != 1 {
		t.Fatal("the cancelled run did not stay skipped")
	}
}
