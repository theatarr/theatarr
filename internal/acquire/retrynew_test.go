package acquire

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/importarr"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// retryNewEnv: router.wanted.retry_new as given, searches that accept nothing, and s02e01 (1004), aired an hour ago,
// the series' only new episode. The movies are from 2024.
func retryNewEnv(t *testing.T, w *policy.Wanted) *env {
	t.Helper()
	e := newEnv(t, true, w)
	e.accept = false
	e.onlyNew()
	e.exec(`UPDATE episode SET air_date_utc_at = ? WHERE id = 1004`, store.Millis(e.clock().Add(-time.Hour)))
	return e
}

func (e *env) round() {
	e.t.Helper()
	if _, err := e.a.Wanted(e.ctx); err != nil {
		e.t.Fatal(err)
	}
	e.drain()
}

// chains lists the queued follow-ups as "version/title": whether the chain is one retry_new started.
func (e *env) chains() map[string]bool {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT payload FROM job WHERE kind = ? AND state = 'queued'
		AND json_extract(payload, '$.reason') = ?`, router.JobResearch, router.ReasonFresh)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			e.t.Fatal(err)
		}
		var p router.ResearchRequest
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			e.t.Fatal(err)
		}
		key := p.Version + "/" + string(rune('0'+p.TitleID/10)) + string(rune('0'+p.TitleID%10))
		if _, twice := out[key]; twice {
			e.t.Fatalf("two follow-ups queued for %s", key)
		}
		out[key] = p.RetryNew
	}
	return out
}

func (e *env) wantChains(what string, want map[string]bool) {
	e.t.Helper()
	got := e.chains()
	if len(got) != len(want) {
		e.t.Fatalf("%s: follow-up chains %v, want %v", what, got, want)
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			e.t.Fatalf("%s: follow-up chains %v, want %v", what, got, want)
		}
	}
}

// With retry_new on, a wanted round's empty search of an episode aired an hour ago queues one follow-up an hour
// later; old content (the other episodes, the 2024 movies) gets none; a second empty search of it (Search now)
// starts no second chain; the follow-up searches only the new episode and its own empty search starts nothing.
func TestRetryNewWantedRoundStartsOneChain(t *testing.T) {
	t.Parallel()
	e := retryNewEnv(t, &policy.Wanted{PerHour: 100, RetryNew: true})
	missed := e.clock()
	e.round()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE state = 'nothing'`); n < 4 {
		t.Fatalf("%d empty searches, want the movies and the series' targets", n)
	}
	e.wantChains("after the round", map[string]bool{"series/10": true})
	if fs := e.followUps(); !fs[1].Equal(missed.Add(time.Hour)) {
		t.Fatalf("follow-ups %v, want step 1 an hour after the search", fs)
	}
	e.request(10, "series", OnlyMissing, 2)
	if _, err := e.a.DrainSearchNow(e.ctx); err != nil {
		t.Fatal(err)
	}
	before := len(e.searches())
	e.drain()
	if n := len(e.searches()) - before; n != 1 {
		t.Fatalf("search now ran %d searches", n)
	}
	e.wantChains("after a second empty search", map[string]bool{"series/10": true})

	before = len(e.searches())
	if step := e.runFollowUp(); step != 1 {
		t.Fatalf("ran step %d", step)
	}
	got := e.searches()[before:]
	if len(got) != 1 || got[0].Season != 2 || got[0].Ep != 1 {
		t.Fatalf("the follow-up searched %+v, want s02e01 only", got)
	}
	e.wantChains("after step 1", map[string]bool{"series/10": true})
	if fs := e.followUps(); !fs[2].Equal(missed.Add(2 * time.Hour)) {
		t.Fatalf("follow-ups %v, want step 2 two hours after the search", fs)
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND json_extract(payload, '$.reason') = ?`,
		router.JobResearch, router.ReasonFresh); n != 2 {
		t.Fatalf("%d follow-up jobs in all, want steps 1 and 2", n)
	}
}

// With the switch unset or off nothing is queued from an empty search and the search jobs carry no mark: the
// behaviour and the stored payloads are the ones from before the switch existed.
func TestRetryNewOffQueuesNothing(t *testing.T) {
	t.Parallel()
	for name, w := range map[string]*policy.Wanted{"unset": {PerHour: 100}, "off": {PerHour: 100, RetryNew: false},
		"no section": nil} {
		t.Run(name, func(t *testing.T) {
			e := retryNewEnv(t, w)
			e.round()
			e.request(10, "series", OnlyMissing, 2)
			if _, err := e.a.DrainSearchNow(e.ctx); err != nil {
				t.Fatal(err)
			}
			e.drain()
			if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE state = 'nothing' AND target = 's02e01'`); n != 2 {
				t.Fatalf("%d empty searches of s02e01, want the round's and search now's", n)
			}
			e.wantChains("switch off", nil)
			if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ?`, router.JobResearch); n != 0 {
				t.Fatalf("%d re-search jobs", n)
			}
			if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND payload LIKE '%retry_new%'`, JobSearch); n != 0 {
				t.Fatalf("%d search jobs carry retry_new", n)
			}
			var payload string
			if err := e.st.Reader().QueryRow(`SELECT payload FROM job WHERE kind = ? ORDER BY id LIMIT 1`, JobSearch).
				Scan(&payload); err != nil {
				t.Fatal(err)
			}
			var only map[string]any
			if err := json.Unmarshal([]byte(payload), &only); err != nil || len(only) != 1 || only["run_id"] == nil {
				t.Fatalf("search job payload %s", payload)
			}
		})
	}
}

// Search now starts a chain with the loop off too.
func TestRetryNewSearchNowStartsAChain(t *testing.T) {
	t.Parallel()
	w := loopOff(100)
	w.RetryNew = true
	e := retryNewEnv(t, w)
	e.request(10, "series", OnlyMissing, 2)
	if _, err := e.a.DrainSearchNow(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.drain()
	e.wantChains("search now", map[string]bool{"series/10": true})
}

// Only the wanted loop and the Search now queue start a chain. A manual search (an arr command), an add-time search
// and a re-search's own search start none with the switch on; the chain an exhausted route starts is not a
// retry_new one.
func TestRetryNewOtherSourcesStartNothing(t *testing.T) {
	t.Parallel()
	w := addTimeOnly(100) // the loop selects nothing itself
	w.RetryNew = true
	e := retryNewEnv(t, w)
	empty := func(what string, want int) {
		t.Helper()
		e.drain()
		if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE state = 'nothing' AND target = 's02e01'`); n != want {
			t.Fatalf("%s: %d empty searches of s02e01, want %d", what, n, want)
		}
	}
	if queued, _, err := e.a.Submit(e.ctx, 1, Selection{Versions: []string{"series"}, TitleIDs: []int64{10},
		EpisodeIDs: []int64{1004}}); err != nil || queued != 1 {
		t.Fatalf("submit: %d %v", queued, err)
	}
	empty("manual search", 1)
	e.wantChains("manual search", nil)

	e.advance(25 * time.Hour) // past again_after; 1004 is still new
	if err := e.a.EnqueueFollowSearch(e.ctx, []importarr.FollowSearchTarget{{TitleID: 10, VersionKey: "series",
		EpisodeIDs: []int64{1004}}}); err != nil {
		t.Fatal(err)
	}
	empty("add-time search", 2)
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE source = ? AND reason = ? AND research IS NULL
		AND target = 's02e01'`, SourceWanted, OnlyMissing); n != 1 {
		t.Fatalf("%d add-time runs that look like a wanted round's", n)
	}
	e.wantChains("add-time search", nil)
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND payload LIKE '%retry_new%'`, JobSearch); n != 0 {
		t.Fatalf("%d search jobs carry retry_new", n)
	}

	e.advance(25 * time.Hour)
	e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1004},
		Reason: router.ReasonExhausted})
	empty("re-search", 3)
	e.wantChains("exhausted route", map[string]bool{"series/10": false})
}

// The job's mark is what starts a chain: the same run without it (Run1) starts none, with it (RunJob, as theatarr
// serve runs a search job) it does.
func TestRetryNewMarkTravelsWithTheJob(t *testing.T) {
	t.Parallel()
	e := retryNewEnv(t, &policy.Wanted{PerHour: 100, RetryNew: true})
	e.request(10, "series", OnlyMissing, 2)
	if _, err := e.a.DrainSearchNow(e.ctx); err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := e.st.Reader().QueryRow(`SELECT payload FROM job WHERE kind = ?`, JobSearch).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var p SearchJob
	if err := json.Unmarshal([]byte(payload), &p); err != nil || !p.RetryNew || p.RunID == 0 {
		t.Fatalf("search job payload %s: %v", payload, err)
	}
	e.exec(`DELETE FROM job WHERE kind = ?`, JobSearch)
	if err := e.a.RunJob(e.ctx, p); err != nil {
		t.Fatal(err)
	}
	e.wantChains("RunJob", map[string]bool{"series/10": true})

	e2 := retryNewEnv(t, &policy.Wanted{PerHour: 100, RetryNew: true})
	e2.request(10, "series", OnlyMissing, 2)
	if _, err := e2.a.DrainSearchNow(e2.ctx); err != nil {
		t.Fatal(err)
	}
	e2.exec(`DELETE FROM job WHERE kind = ?`, JobSearch)
	if err := e2.a.Run1(e2.ctx, p.RunID); err != nil {
		t.Fatal(err)
	}
	if n := e2.count(`SELECT COUNT(*) FROM acquire_run WHERE state = 'nothing'`); n != 1 {
		t.Fatalf("%d empty searches", n)
	}
	e2.wantChains("Run1", nil)
}

// A running retry_new chain looks at the switch at each step: turned off, the next step searches nothing and queues
// nothing. A search marked while the switch was on starts no chain once it is off. A chain an exhausted route
// started does not look at the switch.
func TestRetryNewOffStopsARunningChain(t *testing.T) {
	t.Parallel()
	e := retryNewEnv(t, &policy.Wanted{PerHour: 100, RetryNew: true})
	e.round()
	e.wantChains("started", map[string]bool{"series/10": true})
	if step := e.runFollowUp(); step != 1 {
		t.Fatalf("ran step %d", step)
	}
	e.a.set.RetryNew = false // a save builds the acquirer from the new policy (policy.WantedSettings)
	before := len(e.searches())
	if step := e.runFollowUp(); step != 2 {
		t.Fatalf("ran step %d", step)
	}
	if n := len(e.searches()) - before; n != 0 {
		t.Fatalf("%d searches with the switch off", n)
	}
	e.wantChains("switch off", nil)

	// Marked while on, finished while off.
	e = retryNewEnv(t, &policy.Wanted{PerHour: 100, RetryNew: true})
	if _, err := e.a.Wanted(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.a.set.RetryNew = false
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE state = 'nothing' AND target = 's02e01'`); n != 1 {
		t.Fatalf("%d empty searches of s02e01", n)
	}
	e.wantChains("off before the search ended", nil)

	// An exhausted route's chain, with the switch off all along and turned off under it.
	for _, on := range []bool{false, true} {
		e = retryNewEnv(t, &policy.Wanted{PerHour: 100, Missing: new(bool), CutoffUnmet: policy.CutoffOff, RetryNew: on})
		e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1004},
			Reason: router.ReasonExhausted})
		e.drain()
		e.a.set.RetryNew = false
		before = len(e.searches())
		if step := e.runFollowUp(); step != 1 {
			t.Fatalf("ran step %d", step)
		}
		if n := len(e.searches()) - before; n != 1 {
			t.Fatalf("exhausted chain (switch was %v): %d searches at step 1", on, n)
		}
		e.wantChains("exhausted chain", map[string]bool{"series/10": false})
	}
}

// A step of a retry_new chain is charged to router.wanted.per_hour: a spent hour skips it (the next is queued
// already), and a filled episode stops the chain.
func TestRetryNewStepWithinPerHourAndStops(t *testing.T) {
	t.Parallel()
	for _, q := range []struct {
		name   string
		change func(e *env)
		next   bool
	}{
		{"per_hour used", func(e *env) {
			for range 20 {
				e.exec(`INSERT INTO acquire_run (source, version_id, version_key, title_id, target, state, created_at,
					updated_at) VALUES ('wanted', 102, 'movie', 2, 'movie', 'routed', ?, ?)`,
					store.Millis(e.clock()), store.Millis(e.clock()))
			}
		}, true},
		{"filled", func(e *env) {
			e.exec(`UPDATE episode_version_state SET state = 'filled' WHERE version_id = 201 AND episode_id = 1004`)
		}, false},
	} {
		t.Run(q.name, func(t *testing.T) {
			e := retryNewEnv(t, &policy.Wanted{PerHour: 20, RetryNew: true})
			e.round()
			e.wantChains("started", map[string]bool{"series/10": true})
			e.advance(time.Hour - time.Minute)
			q.change(e)
			before := len(e.searches())
			if step := e.runFollowUp(); step != 1 {
				t.Fatalf("ran step %d", step)
			}
			if n := len(e.searches()) - before; n != 0 {
				t.Errorf("%d searches", n)
			}
			if fs := e.followUps(); (len(fs) == 1) != q.next {
				t.Errorf("next follow-up %v, want queued %v", fs, q.next)
			}
			if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ?`, router.ReasonFresh); n != 0 {
				t.Errorf("%d follow-up runs", n)
			}
		})
	}
}

// A step that does search is a wanted search of the hour like any follow-up's.
func TestRetryNewStepCountsAgainstPerHour(t *testing.T) {
	t.Parallel()
	e := retryNewEnv(t, &policy.Wanted{PerHour: 100, RetryNew: true})
	e.round()
	e.advance(61 * time.Minute) // the round's searches left the hour
	used, err := e.a.wantedUsed(e.ctx, e.clock())
	if err != nil || used != 0 {
		t.Fatalf("used %d %v", used, err)
	}
	e.drain() // step 1, due an hour after the search
	if used, err = e.a.wantedUsed(e.ctx, e.clock()); err != nil || used != 1 {
		t.Fatalf("after step 1 the hour has %d searches (%v), want 1", used, err)
	}
}

// The shared search of two libraries (one job, a run per library): each library whose target got nothing starts its
// own chain; with the switch off, none.
func TestRetryNewFanout(t *testing.T) {
	t.Parallel()
	for _, on := range []bool{true, false} {
		e := retryNewEnv(t, &policy.Wanted{PerHour: 100, RetryNew: on})
		e.pol.Versions["series-copy"] = &policy.VersionPolicy{Mode: policy.ModeActive, Grab: true, Kind: policy.KindSeries,
			Storage: policy.StorageDebrid, Indexers: []string{"pub"}, Providers: []string{"rd"}}
		e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at)
			VALUES (221, 10, 'series-copy', 'wanted', 0, 0)`)
		e.exec(`INSERT INTO episode_version_state (version_id, episode_id, state, updated_at)
			SELECT 221, episode_id, state, 0 FROM episode_version_state WHERE version_id = 201`)
		e.round()
		if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE search_run IS NOT NULL AND state = 'nothing'
			AND target = 's02e01'`); n != 1 {
			t.Fatalf("%d served rows of a shared s02e01 search, want 1", n)
		}
		want := map[string]bool{"series/10": true, "series-copy/10": true}
		if !on {
			want = nil
		}
		e.wantChains("shared search", want)
	}
}

// A movie whose digital release was yesterday behaves the same; one from 2024 gets nothing.
func TestRetryNewMovie(t *testing.T) {
	t.Parallel()
	e := retryNewEnv(t, &policy.Wanted{PerHour: 100, RetryNew: true})
	e.exec(`UPDATE title_release_date SET date = ? WHERE title_id = 1`, e.clock().Add(-24*time.Hour).Format("2006-01-02"))
	e.exec(`UPDATE episode SET air_date_utc_at = ? WHERE id = 1004`, store.Millis(e.clock().Add(-10*24*time.Hour)))
	missed := e.clock()
	e.round()
	e.wantChains("after the round", map[string]bool{"movie/01": true})
	if fs := e.followUps(); !fs[1].Equal(missed.Add(time.Hour)) {
		t.Fatalf("follow-ups %v", fs)
	}
	before := len(e.searches())
	if step := e.runFollowUp(); step != 1 {
		t.Fatalf("ran step %d", step)
	}
	if got := e.searches()[before:]; len(got) != 1 || got[0].Title != "One" {
		t.Fatalf("the follow-up searched %+v", got)
	}
}

// An upgrade (cutoff) search that finds nothing starts no chain: the retry is for missing items.
func TestRetryNewNotForUpgradeSearches(t *testing.T) {
	t.Parallel()
	e := retryNewEnv(t, &policy.Wanted{PerHour: 100, Missing: new(bool), CutoffUnmet: policy.CutoffScore, RetryNew: true})
	e.exec(`UPDATE title_release_date SET date = ? WHERE title_id = 3`, e.clock().Add(-24*time.Hour).Format("2006-01-02"))
	e.round()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE title_id = 3 AND reason = ? AND state = 'nothing'`,
		OnlyCutoff); n != 1 {
		t.Fatalf("%d empty upgrade searches of movie 3", n)
	}
	e.wantChains("upgrade search", nil)
}

// queuedChains counts title 10's queued follow-up steps by the way their chain started.
func (e *env) queuedChains() (retryNew, exhausted int) {
	e.t.Helper()
	retryNew = e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'
		AND json_extract(payload, '$.reason') = ? AND json_extract(payload, '$.step') > 0
		AND json_extract(payload, '$.retry_new')`, router.JobResearch, router.ReasonFresh)
	exhausted = e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'
		AND json_extract(payload, '$.reason') = ? AND json_extract(payload, '$.step') > 0
		AND json_extract(payload, '$.retry_new') IS NULL`, router.JobResearch, router.ReasonFresh)
	return retryNew, exhausted
}

// runDue moves the clock to the earliest queued follow-up and runs every ready job.
func (e *env) runDue() {
	e.t.Helper()
	var at int64
	if err := e.st.Reader().QueryRow(`SELECT MIN(run_at) FROM job WHERE kind = ? AND state = 'queued'
		AND json_extract(payload, '$.reason') = ?`, router.JobResearch, router.ReasonFresh).Scan(&at); err != nil {
		e.t.Fatal(err)
	}
	if d := store.FromMillis(at).Sub(e.clock()); d > 0 {
		e.advance(d)
	}
	e.drain()
}

// An exhausted route's chain starts as it would without the switch whatever a retry_new chain of the title did: one
// still queued (the switch turned off, or left on), or one that ended (its episode filled, then wanted again). The
// retry_new chain then ends at its next step without a search, and starts no more while the exhausted chain's
// window lasts.
func TestRetryNewChainNeverBlocksAnExhaustedChain(t *testing.T) {
	t.Parallel()
	miss := router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1004},
		Reason: router.ReasonExhausted}
	for _, q := range []struct {
		name   string
		before func(e *env)
		on     bool
	}{
		{"switch turned off", func(e *env) { e.a.set.RetryNew = false }, false},
		{"switch on, chain pending", func(e *env) {}, true},
		{"switch on, chain ended", func(e *env) {
			e.exec(`UPDATE episode_version_state SET state = 'filled' WHERE version_id = 201 AND episode_id = 1004`)
			e.runDue()
			if r, x := e.queuedChains(); r != 0 || x != 0 {
				e.t.Fatalf("the filled episode's chain: %d retry_new, %d exhausted steps queued", r, x)
			}
			e.exec(`UPDATE episode_version_state SET state = 'wanted' WHERE version_id = 201 AND episode_id = 1004`)
		}, true},
	} {
		t.Run(q.name, func(t *testing.T) {
			e := retryNewEnv(t, &policy.Wanted{PerHour: 100, RetryNew: true})
			e.round()
			e.wantChains("started", map[string]bool{"series/10": true})
			q.before(e)
			pending, _ := e.queuedChains()
			missed := e.clock()
			e.research(miss)
			if r, x := e.queuedChains(); r != pending || x != 1 {
				t.Fatalf("after the route exhausted: %d retry_new and %d exhausted steps queued, want %d and 1", r, x,
					pending)
			}
			var key string
			var at int64
			if err := e.st.Reader().QueryRow(`SELECT dedupe_key, run_at FROM job WHERE kind = ? AND state = 'queued'
				AND json_extract(payload, '$.step') = 1 AND json_extract(payload, '$.retry_new') IS NULL`,
				router.JobResearch).Scan(&key, &at); err != nil {
				t.Fatal(err)
			}
			if key != "acquire.research/fresh/series/10/1" || !store.FromMillis(at).Equal(missed.Add(time.Hour)) {
				t.Fatalf("exhausted chain step 1: key %s at %v", key, store.FromMillis(at))
			}
			e.drain() // the re-search at once
			// Both chains' first steps are due together: the exhausted chain's searches, the retry_new chain's ends.
			before := len(e.searches())
			e.runDue()
			if n := len(e.searches()) - before; n != 1 {
				t.Fatalf("%d searches at the first steps, want the exhausted chain's one", n)
			}
			if r, x := e.queuedChains(); r != 0 || x != 1 {
				t.Fatalf("after the steps: %d retry_new and %d exhausted steps queued, want 0 and 1", r, x)
			}
			// While the exhausted chain's window lasts, an empty search starts no retry_new chain.
			e.a.set.RetryNew = true
			e.request(10, "series", OnlyMissing, 2)
			if _, err := e.a.DrainSearchNow(e.ctx); err != nil {
				t.Fatal(err)
			}
			e.drain()
			if r, x := e.queuedChains(); r != 0 || x != 1 {
				t.Fatalf("after another empty search: %d retry_new and %d exhausted steps queued, want 0 and 1", r, x)
			}
		})
	}
}

// lastSearchJob is the payload of the newest search job.
func (e *env) lastSearchJob() string {
	e.t.Helper()
	var payload string
	if err := e.st.Reader().QueryRow(`SELECT payload FROM job WHERE kind = ? ORDER BY id DESC LIMIT 1`, JobSearch).
		Scan(&payload); err != nil {
		e.t.Fatal(err)
	}
	return payload
}

// wantChainSearch: the newest search job holds its run's id and nothing else, as any re-search's does, and that run
// carries the chain's mark in the run history.
func (e *env) wantChainSearch(what string) {
	e.t.Helper()
	var id int64
	var research string
	if err := e.st.Reader().QueryRow(`SELECT id, COALESCE(research, '') FROM acquire_run ORDER BY id DESC LIMIT 1`).
		Scan(&id, &research); err != nil {
		e.t.Fatal(err)
	}
	if got, want := e.lastSearchJob(), `{"run_id":`+strconv.FormatInt(id, 10)+`}`; got != want || research != researchRetryNew {
		e.t.Fatalf("%s: job %s (want %s), run research %q", what, got, want, research)
	}
}

// hours runs the ready jobs every quarter of an hour for n hours.
func (e *env) hours(n int) {
	e.t.Helper()
	for range 4 * n {
		e.advance(15*time.Minute + time.Second)
		e.drain()
	}
}

// A retry_new chain's search no indexer answered is asked for again with the chain's mark, and the search asked for
// again is marked too. With the switch turned off after such a search, nothing more is searched or queued: not the
// searches asked for again, not the next step.
func TestRetryNewOffStopsUnansweredFollowUps(t *testing.T) {
	t.Parallel()
	for _, off := range []bool{false, true} {
		e := retryNewEnv(t, &policy.Wanted{PerHour: 100, RetryNew: true})
		e.round()
		e.wantChains("started", map[string]bool{"series/10": true})
		e.noAnswer = true
		if step := e.runFollowUp(); step != 1 {
			t.Fatalf("ran step %d", step)
		}
		e.wantChainSearch("the follow-up's search")
		var again string
		if err := e.st.Reader().QueryRow(`SELECT payload FROM job WHERE kind = ? AND state = 'queued'
			AND dedupe_key LIKE 'acquire.research/infra/%'`, router.JobResearch).Scan(&again); err != nil {
			t.Fatal(err)
		}
		if again != `{"title_id":10,"version_id":201,"version":"series","episodes":[1004],"reason":"fresh","retry_new":true}` {
			t.Fatalf("asked for again with %s", again)
		}
		e.a.set.RetryNew = !off
		before := len(e.searches())
		if !off {
			e.advance(infraRetry + time.Second)
			e.drain()
			if n := len(e.searches()) - before; n != 1 {
				t.Fatalf("switch on: %d searches asked for again", n)
			}
			e.wantChainSearch("the search asked for again")
			continue
		}
		e.hours(12)
		if n := len(e.searches()) - before; n != 0 {
			t.Fatalf("%d searches with the switch off", n)
		}
		if n := e.count(`SELECT COUNT(*) FROM job WHERE kind IN (?, ?) AND state = 'queued'`, router.JobResearch,
			JobSearch); n != 0 {
			t.Fatalf("%d jobs still queued with the switch off", n)
		}
	}
}

// The switch turned off between a retry_new chain's step and the search it queued: the search asks no indexer, its
// run ends skipped and does not count as a search.
func TestRetryNewOffBeforeAFollowUpsSearch(t *testing.T) {
	t.Parallel()
	e := retryNewEnv(t, &policy.Wanted{PerHour: 100, RetryNew: true})
	e.round()
	var payload string
	if err := e.st.Reader().QueryRow(`SELECT payload FROM job WHERE kind = ? AND state = 'queued'`,
		router.JobResearch).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var p router.ResearchRequest
	if err := json.Unmarshal([]byte(payload), &p); err != nil || !p.RetryNew || p.Step != 1 {
		t.Fatalf("queued follow-up %s: %v", payload, err)
	}
	e.exec(`DELETE FROM job WHERE kind = ?`, router.JobResearch)
	e.advance(time.Hour)
	e.research(p) // the step: it queues its search and step 2
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ? AND state = 'queued'`, researchRetryNew); n != 1 {
		t.Fatalf("%d follow-up runs queued", n)
	}
	e.a.set.RetryNew = false
	before := len(e.searches())
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ? AND state = 'skipped' AND infra = ?`,
		researchRetryNew, runGated); n != 1 || len(e.searches()) != before {
		t.Fatalf("%d gated runs, %d searches with the switch off", n, len(e.searches())-before)
	}
	e.hours(3)
	if n := len(e.searches()) - before; n != 0 {
		t.Fatalf("%d searches with the switch off", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind IN (?, ?) AND state = 'queued'`, router.JobResearch,
		JobSearch); n != 0 {
		t.Fatalf("%d jobs still queued with the switch off", n)
	}
}

// The chain after an exhausted route, its searches and the searches asked for again after no indexer answered have
// the payloads they had before the switch existed, whatever the switch says.
func TestRetryNewLeavesExhaustedChainPayloads(t *testing.T) {
	t.Parallel()
	for _, on := range []bool{false, true} {
		e := retryNewEnv(t, &policy.Wanted{PerHour: 100, Missing: new(bool), CutoffUnmet: policy.CutoffOff, RetryNew: on})
		e.noAnswer = true
		e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1004},
			Reason: router.ReasonExhausted})
		e.drain()
		missed := e.clock()
		if step := e.runFollowUp(); step != 1 {
			t.Fatalf("ran step %d", step)
		}
		e.advance(infraRetry + time.Second)
		e.drain() // the follow-up's search asked for again
		want := map[string]int{
			`{"title_id":10,"version_id":201,"version":"series","episodes":[1004],"reason":"fresh"}`: 0,
			`{"title_id":10,"version_id":201,"version":"series","reason":"fresh","step":2,"missed_at":"` +
				missed.Format(time.RFC3339Nano) + `"}`: 0,
		}
		rows, err := e.st.Reader().Query(`SELECT kind, dedupe_key, payload FROM job WHERE kind IN (?, ?)`,
			router.JobResearch, JobSearch)
		if err != nil {
			t.Fatal(err)
		}
		searches := 0
		for rows.Next() {
			var kind, key, payload string
			if err := rows.Scan(&kind, &key, &payload); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(payload, "retry_new") {
				t.Errorf("switch %v: job %s has payload %s", on, key, payload)
			}
			if _, ok := want[payload]; ok {
				want[payload]++
			}
			if kind == JobSearch {
				searches++
				if payload != `{"run_id":`+strings.TrimPrefix(key, JobSearch+"/")+`}` {
					t.Errorf("switch %v: search job %s has payload %s", on, key, payload)
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if searches < 3 {
			t.Errorf("switch %v: %d search jobs, want the re-search, the follow-up's and the one asked for again", on, searches)
		}
		for payload, n := range want {
			if n == 0 {
				t.Errorf("switch %v: no job with payload %s", on, payload)
			}
		}
	}
}
