package acquire

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// followUps lists the queued follow-ups: step and when.
func (e *env) followUps() map[int]time.Time {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT payload, run_at FROM job WHERE kind = ? AND state = 'queued'
		AND json_extract(payload, '$.reason') = ?`, router.JobResearch, router.ReasonFresh)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[int]time.Time{}
	for rows.Next() {
		var payload string
		var at int64
		if err := rows.Scan(&payload, &at); err != nil {
			e.t.Fatal(err)
		}
		var step int
		if err := e.st.Reader().QueryRow(`SELECT json_extract(?, '$.step')`, payload).Scan(&step); err != nil {
			e.t.Fatal(err)
		}
		out[step] = store.FromMillis(at)
	}
	return out
}

// runFollowUp moves the clock to the one queued follow-up and runs every ready job; it returns the step run.
func (e *env) runFollowUp() int {
	e.t.Helper()
	fs := e.followUps()
	if len(fs) != 1 {
		e.t.Fatalf("follow-ups queued %v, want one", fs)
	}
	for step, at := range fs {
		e.advance(at.Sub(e.clock()))
		e.drain()
		return step
	}
	return 0
}

// onlyNew makes episode 1004 the series' only new one: the other aired episodes aired 10 days ago.
func (e *env) onlyNew() {
	e.t.Helper()
	e.exec(`UPDATE episode SET air_date_utc_at = ? WHERE title_id = 10 AND id <> 1004 AND air_date_utc_at <= ?`,
		store.Millis(e.clock().Add(-10*24*time.Hour)), store.Millis(e.clock()))
}

// A miss everywhere on a new episode (aired a day ago) is followed up 1 h, 2 h, 4 h, ... after the miss, one at a
// time, until the episode is a week old. Old content (the 2024 movies) gets none.
func TestFollowUpsAfterMissOnNewContent(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 100})
	e.accept = false
	e.onlyNew()
	missed := e.clock()
	miss := router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1004},
		Reason: router.ReasonExhausted}
	e.research(miss)
	e.research(miss) // a second miss while one is pending queues nothing more
	if fs := e.followUps(); len(fs) != 1 || !fs[1].Equal(missed.Add(time.Hour)) {
		t.Fatalf("follow-ups %v", fs)
	}
	e.drain() // the re-search at once
	before := len(e.searches())
	var steps []int
	for len(e.followUps()) > 0 {
		steps = append(steps, e.runFollowUp())
		if n := len(e.searches()) - before; n != len(steps) {
			t.Fatalf("after step %d: %d follow-up searches", steps[len(steps)-1], n)
		}
		for step, at := range e.followUps() {
			if want := missed.Add(time.Hour << (step - 1)); !at.Equal(want) || step != len(steps)+1 {
				t.Fatalf("step %d at %v, want %v", step, at, want)
			}
		}
	}
	// Aired t0-24h: new until t0+6d; the 8th follow-up runs at the miss + 128 h, a 9th (256 h) would be too late.
	if len(steps) != 8 {
		t.Errorf("follow-ups run %v", steps)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ? AND target = 's02e01'`, router.ReasonFresh); n != 8 {
		t.Errorf("%d follow-up runs", n)
	}

	// The chain ran in this window: another miss on the still-new episode starts none.
	e.research(miss)
	if fs := e.followUps(); len(fs) != 0 {
		t.Errorf("a second chain: %v", fs)
	}

	// Old content: none.
	e.research(router.ResearchRequest{TitleID: 2, VersionID: 102, Version: "movie", Reason: router.ReasonExhausted})
	if fs := e.followUps(); len(fs) != 0 {
		t.Errorf("follow-ups for an old movie: %v", fs)
	}
}

// A follow-up checks again when it runs: an unmonitored or filled episode, a download in flight or a version no
// longer active stops the follow-ups; per_hour used skips one step only.
func TestFollowUpStops(t *testing.T) {
	t.Parallel()
	miss := router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1004},
		Reason: router.ReasonExhausted}
	for _, q := range []struct {
		name   string
		change func(e *env)
		next   bool
	}{
		{"unmonitored", func(e *env) {
			e.exec(`UPDATE episode_version_state SET monitored = 0 WHERE version_id = 201 AND episode_id = 1004`)
		}, false},
		{"series unmonitored", func(e *env) { e.exec(`UPDATE version SET monitored = 0 WHERE id = 201`) }, false},
		{"filled", func(e *env) {
			e.exec(`UPDATE episode_version_state SET state = 'filled' WHERE version_id = 201 AND episode_id = 1004`)
		}, false},
		{"download in flight", func(e *env) {
			e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer,
				provider, state, episodes, created_at, updated_at) VALUES (10, 201, 'series', ?, 'x', 'pub', 'rd',
				'added', '[1004]', 0, 0)`, h("f"))
		}, false},
		{"version off", func(e *env) { e.pol.Versions["series"].Mode = policy.ModeShadow }, false},
		{"per_hour used", func(e *env) {
			for range 2 {
				e.exec(`INSERT INTO acquire_run (source, version_id, version_key, title_id, target, state, created_at,
					updated_at) VALUES ('wanted', 102, 'movie', 2, 'movie', 'routed', ?, ?)`,
					store.Millis(e.clock()), store.Millis(e.clock()))
			}
		}, true},
	} {
		t.Run(q.name, func(t *testing.T) {
			e := newEnv(t, true, &policy.Wanted{PerHour: 2})
			e.accept = false
			e.onlyNew()
			e.research(miss)
			e.drain()
			before := len(e.searches())
			e.advance(time.Hour - time.Minute)
			q.change(e)
			if step := e.runFollowUp(); step != 1 {
				t.Fatalf("ran step %d", step)
			}
			if n := len(e.searches()) - before; n != 0 {
				t.Errorf("%d searches", n)
			}
			if fs := e.followUps(); (len(fs) == 1) != q.next {
				t.Errorf("next follow-up %v, want queued %v", fs, q.next)
			}
		})
	}
}

// A new episode: RSS routes it, RD has it uncached, every provider missed. An hour later the
// follow-up searches again, RD has it cached now, and the add is accepted.
func TestFollowUpAddsOnceCached(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 100})
	e.onlyNew()
	const hash = "0123456789abcdef0123456789abcdef01234567"
	rel := func() router.Candidate {
		return router.Candidate{Release: indexer.Release{Indexer: "pub", Kind: "torznab",
			Title: "Show.S02E01.2160p.WEB.H265-GRP", InfoHash: hash, Privacy: policy.PrivacyPublic, Size: 4 << 30}, Quality: "WEBDL-2160p", Score: 100, Episodes: []int64{1004}}
	}
	e.a.search = func(ctx context.Context, title decide.Title, version decide.Version, existing *decide.ExistingFile,
		ss *decide.SeriesSearch) (*router.Searched, error) {
		out, err := e.search(ctx, title, version, existing, ss)
		if err != nil {
			return nil, err
		}
		out.Request.Candidates = []router.Candidate{rel()}
		return out, nil
	}
	rd := e.rt.Adders["rd"]
	rd.SetUncached(true)
	res, err := e.rt.Route(e.ctx, router.Request{TitleID: 10, VersionID: 201, Version: "series",
		Candidates: []router.Candidate{rel()}, Source: "ui:grab:test"})
	if err != nil || res.Outcome != router.OutcomeExhausted {
		t.Fatalf("%+v %v", res, err)
	}
	e.drain() // the re-search at once: its route is a known miss (the hour's verdict), no add
	if got := len(rd.Adds()); got != 1 {
		t.Fatalf("rd adds %d after the miss", got)
	}
	if fs := e.followUps(); len(fs) != 1 {
		t.Fatalf("follow-ups %v", fs)
	}
	rd.SetUncached(false)
	e.runFollowUp()
	if got := rd.Adds(); len(got) != 2 || got[1] != hash {
		t.Fatalf("rd adds %v", got)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE info_hash = ? AND provider = 'rd' AND state = 'added'`, hash); n != 1 {
		t.Errorf("%d downloads added on rd", n)
	}
}

// An infra retry of a follow-up (no MissedAt) searches the failed run's own episodes, still only the new ones.
func TestFollowUpRetrySearchesItsEpisodes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 100})
	e.accept = false // 1001, 1003 (season 1) and 1004 (season 2) all aired a day ago
	e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1004},
		Reason: router.ReasonFresh})
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ?`, router.ReasonFresh); n != 1 {
		t.Errorf("%d follow-up runs, want only season 2", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ? AND target = 's02e01'`, router.ReasonFresh); n != 1 {
		t.Errorf("season 2 not searched")
	}
}

// One chain per title and version: misses on overlapping episode sets share it, and a follow-up searches only the
// title's episodes that are new when it runs, so an old episode that came in a batch with a new one is not followed.
func TestFollowUpOneChainPerTitle(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 100})
	e.accept = false
	e.onlyNew() // 1001 (s01e01) aired 10 days ago, 1004 (s02e01) a day ago
	e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1001, 1004},
		Reason: router.ReasonExhausted})
	e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1004},
		Reason: router.ReasonExhausted})
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'
		AND json_extract(payload, '$.reason') = ?`, router.JobResearch, router.ReasonFresh); n != 1 {
		t.Errorf("%d follow-ups queued, want one chain", n)
	}
	e.drain()
	e.advance(time.Hour)
	e.drain()
	rows, err := e.st.Reader().Query(`SELECT target FROM acquire_run WHERE research = ?`, router.ReasonFresh)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var targets []string
	for rows.Next() {
		var x string
		if err := rows.Scan(&x); err != nil {
			t.Fatal(err)
		}
		targets = append(targets, x)
	}
	if len(targets) != 1 || targets[0] != "s02e01" {
		t.Errorf("follow-up searched %v, want only the new s02e01", targets)
	}
}

// A release that stays uncached: the first add and one per follow-up (8 for an episode aired a day ago), and the
// last follow-up's miss starts no new chain.
func TestFollowUpPersistentMiss(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 100})
	e.onlyNew()
	const hash = "0123456789abcdef0123456789abcdef01234567"
	rel := func() router.Candidate {
		return router.Candidate{Release: indexer.Release{Indexer: "pub", Kind: "torznab",
			Title: "Show.S02E01.2160p.WEB.H265-GRP", InfoHash: hash, Privacy: policy.PrivacyPublic, Size: 4 << 30},
			Quality: "WEBDL-2160p", Score: 100, Episodes: []int64{1004}}
	}
	e.a.search = func(ctx context.Context, title decide.Title, version decide.Version, existing *decide.ExistingFile,
		ss *decide.SeriesSearch) (*router.Searched, error) {
		out, err := e.search(ctx, title, version, existing, ss)
		if err != nil {
			return nil, err
		}
		out.Request.Candidates = []router.Candidate{rel()}
		return out, nil
	}
	rd := e.rt.Adders["rd"]
	rd.SetUncached(true)
	if _, err := e.rt.Route(e.ctx, router.Request{TitleID: 10, VersionID: 201, Version: "series",
		Candidates: []router.Candidate{rel()}, Source: "ui:grab:test"}); err != nil {
		t.Fatal(err)
	}
	e.drain()
	for range 20 {
		if len(e.followUps()) == 0 {
			break
		}
		e.runFollowUp()
	}
	if fs := e.followUps(); len(fs) != 0 {
		t.Fatalf("follow-ups still queued: %v", fs)
	}
	if got := len(rd.Adds()); got != 9 {
		t.Errorf("rd adds %d, want 9 (the first and 8 follow-ups)", got)
	}
}

// deadEnv is newEnv with the library writing for the active versions, as a dead link's search needs.
func deadEnv(t *testing.T, enabled bool, wanted *policy.Wanted) *env {
	t.Helper()
	e := newEnv(t, enabled, wanted)
	e.pol.Library = &policy.Library{Enabled: true, Versions: map[string]*policy.LibraryVersion{
		"movie": {Roots: []string{"/media/movies"}}, "series": {Roots: []string{"/media/shows"}}}}
	return e
}

// researchJobs counts the queued re-search jobs of a reason.
func (e *env) researchJobs(reason string) int {
	e.t.Helper()
	return e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'
		AND json_extract(payload, '$.reason') = ?`, router.JobResearch, reason)
}

// withCandidate makes every search return one public release of the given episodes, accepted.
func (e *env) withCandidate(hash string, episodes ...int64) {
	e.a.search = func(ctx context.Context, title decide.Title, version decide.Version, existing *decide.ExistingFile,
		ss *decide.SeriesSearch) (*router.Searched, error) {
		out, err := e.search(ctx, title, version, existing, ss)
		if err != nil {
			return nil, err
		}
		out.Request.Candidates = []router.Candidate{{Release: indexer.Release{Indexer: "pub", Kind: "torznab",
			Title: title.Name + ".2160p.WEB.H265-GRP", InfoHash: hash, Privacy: policy.PrivacyPublic, Size: 4 << 30},
			Quality: "WEBDL-2160p", Score: 100, Episodes: episodes}}
		return out, nil
	}
}

// A dead link (relink's ReasonDeadLink) gets one search of its movie or its own episodes, old content too, whatever
// router.wanted.missing says, and no follow-up chain after it.
func TestDeadLinkSearchesOnce(t *testing.T) {
	t.Parallel()
	e := deadEnv(t, true, &policy.Wanted{PerHour: 100})
	e.accept = false
	e.onlyNew() // 1001 (s01e01) aired 10 days ago
	e.research(router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonDeadLink})
	e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1001},
		Reason: router.ReasonDeadLink})
	e.drain()
	rows, err := e.st.Reader().Query(`SELECT target FROM acquire_run WHERE research = ? ORDER BY id`, router.ReasonDeadLink)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var targets []string
	for rows.Next() {
		var x string
		if err := rows.Scan(&x); err != nil {
			t.Fatal(err)
		}
		targets = append(targets, x)
	}
	if len(targets) != 2 || targets[0] != "movie" || targets[1] != "s01e01" {
		t.Errorf("dead-link searches %v, want movie and s01e01 only", targets)
	}
	if n := len(e.searches()); n != 2 {
		t.Errorf("%d searches", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'`, router.JobResearch); n != 0 {
		t.Errorf("%d re-searches queued after a dead-link search, want none", n)
	}
}

// A dead-link search checks when it runs: grabbing off (router, grab, version not active, the library not writing
// for the version) ends it skipped with no search, no provider call and nothing queued to fire later; unmonitored,
// filled again or in flight stops it.
func TestDeadLinkStops(t *testing.T) {
	t.Parallel()
	movie := router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonDeadLink}
	series := router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1001},
		Reason: router.ReasonDeadLink}
	for _, q := range []struct {
		name   string
		router bool
		p      router.ResearchRequest
		change func(e *env)
	}{
		{"router off", false, movie, func(*env) {}},
		{"grab off", true, movie, func(e *env) { e.pol.Versions["movie"].Grab = false }},
		{"version not active", true, movie, func(e *env) { e.pol.Versions["movie"].Mode = policy.ModeShadow }},
		{"library disabled", true, movie, func(e *env) { e.pol.Library.Enabled = false }},
		{"library not listing the version", true, series, func(e *env) { delete(e.pol.Library.Versions, "series") }},
		{"movie unmonitored", true, movie, func(e *env) { e.exec(`UPDATE version SET monitored = 0 WHERE id = 101`) }},
		{"movie revived", true, movie, func(e *env) { e.exec(`UPDATE version SET state = 'filled' WHERE id = 101`) }},
		{"movie in flight", true, movie, func(e *env) {
			e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer,
				provider, state, created_at, updated_at) VALUES (1, 101, 'movie', ?, 'x', 'pub', 'rd', 'added', 0, 0)`, h("f"))
		}},
		{"episode unmonitored", true, series, func(e *env) {
			e.exec(`UPDATE episode_version_state SET monitored = 0 WHERE version_id = 201 AND episode_id = 1001`)
		}},
		{"episode replaced", true, series, func(e *env) {
			e.exec(`UPDATE episode_version_state SET state = 'filled' WHERE version_id = 201 AND episode_id = 1001`)
		}},
	} {
		t.Run(q.name, func(t *testing.T) {
			e := deadEnv(t, q.router, &policy.Wanted{PerHour: 10})
			q.change(e)
			e.research(q.p)
			e.drain()
			if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ?`, router.ReasonDeadLink); n != 0 ||
				len(e.searches()) != 0 {
				t.Errorf("%d dead-link runs, %d searches", n, len(e.searches()))
			}
			if n := len(e.rt.Adders["rd"].Adds()); n != 0 {
				t.Errorf("%d provider adds", n)
			}
			if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued'`, router.JobResearch); n != 0 {
				t.Errorf("%d re-searches queued to fire later", n)
			}
		})
	}
}

// A spent router.wanted.per_hour defers a dead link's search to when the hour's oldest search expires (it is not
// lost with the missing search off); it is checked again then.
func TestDeadLinkDeferredPastPerHour(t *testing.T) {
	t.Parallel()
	for _, filled := range []bool{false, true} {
		e := deadEnv(t, true, &policy.Wanted{PerHour: 1})
		e.accept = false
		e.exec(`INSERT INTO acquire_run (source, version_id, version_key, title_id, target, state, created_at,
			updated_at) VALUES ('wanted', 102, 'movie', 2, 'movie', 'routed', ?, ?)`,
			store.Millis(e.clock()), store.Millis(e.clock()))
		e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1001},
			Reason: router.ReasonDeadLink})
		e.drain()
		if len(e.searches()) != 0 || e.researchJobs(router.ReasonDeadLink) != 1 {
			t.Fatalf("filled %v: %d searches, %d deferred", filled, len(e.searches()), e.researchJobs(router.ReasonDeadLink))
		}
		var eps string
		if err := e.st.Reader().QueryRow(`SELECT json_extract(payload, '$.episodes') FROM job WHERE kind = ?
			AND state = 'queued'`, router.JobResearch).Scan(&eps); err != nil || eps != "[1001]" {
			t.Fatalf("deferred episodes %s %v", eps, err)
		}
		if filled {
			e.exec(`UPDATE episode_version_state SET state = 'filled' WHERE version_id = 201 AND episode_id = 1001`)
		}
		e.advance(time.Hour + 2*time.Second)
		e.drain()
		want := 1
		if filled {
			want = 0
		}
		if got := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ? AND target = 's01e01'`,
			router.ReasonDeadLink); got != want || len(e.searches()) != want {
			t.Errorf("filled %v: %d runs, %d searches, want %d", filled, got, len(e.searches()), want)
		}
		if n := e.researchJobs(router.ReasonDeadLink); n != 0 {
			t.Errorf("filled %v: %d still queued", filled, n)
		}
	}
}

// The search a re-search queues checks router.enabled, grab (and for a dead link the library) again when it
// runs: one turned off in between ends it skipped before any indexer or provider is asked. Fresh follow-ups too.
func TestResearchSearchRechecksSwitches(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{router.ReasonDeadLink, router.ReasonFresh} {
		for _, q := range []struct {
			name string
			off  func(e *env)
		}{
			{"router off", func(e *env) { e.pol.Router.Enabled = false }},
			{"grab off", func(e *env) { e.pol.Versions["series"].Grab = false }},
			{"library off", func(e *env) { e.pol.Library.Enabled = false }},
		} {
			if reason == router.ReasonFresh && q.name == "library off" {
				continue
			}
			t.Run(reason+"/"+q.name, func(t *testing.T) {
				e := deadEnv(t, true, &policy.Wanted{PerHour: 10})
				e.onlyNew()
				e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series",
					Episodes: []int64{1004}, Reason: reason})
				if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ? AND state = 'queued'`, reason); n != 1 {
					t.Fatalf("%d runs queued", n)
				}
				q.off(e)
				e.drain()
				if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ? AND state = 'skipped'`, reason); n != 1 ||
					len(e.searches()) != 0 || len(e.rt.Adders["rd"].Adds()) != 0 {
					t.Errorf("%d skipped runs, %d searches, %d adds", n, len(e.searches()), len(e.rt.Adders["rd"].Adds()))
				}
			})
		}
	}
}

// A dead link's search is one search. One no indexer answered is not asked for again; a route that misses everywhere starts no exhausted re-search and so no fresh follow-ups.
func TestDeadLinkNoChain(t *testing.T) {
	t.Parallel()
	t.Run("no answer", func(t *testing.T) {
		// One attempt: not asked for again after infraBackoff (a persistent outage made 9 in 8 h).
		e := deadEnv(t, true, &policy.Wanted{PerHour: 100})
		e.accept = false
		e.noAnswer = true
		e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1001},
			Reason: router.ReasonDeadLink})
		e.drain()
		for range 40 { // ten hours
			e.advance(15*time.Minute + time.Second)
			e.drain()
		}
		if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE version_id = 201`); n != 1 || len(e.searches()) != 1 {
			t.Errorf("%d runs, %d searches, want one", n, len(e.searches()))
		}
		if n := e.researchJobs(router.ReasonDeadLink); n != 0 {
			t.Errorf("%d dead-link searches queued again", n)
		}
	})
	t.Run("exhausted", func(t *testing.T) {
		e := deadEnv(t, true, &policy.Wanted{PerHour: 100})
		e.onlyNew() // 1004 is new: an exhausted re-search would start fresh follow-ups
		e.withCandidate(h("9"), 1004)
		e.rt.Adders["rd"].SetUncached(true)
		e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1004},
			Reason: router.ReasonDeadLink})
		e.drain()
		if n := len(e.rt.Adders["rd"].Adds()); n != 1 {
			t.Fatalf("%d adds, want the one uncached try", n)
		}
		if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research IS NOT NULL AND research <> ?`,
			router.ReasonDeadLink); n != 0 || e.researchJobs(router.ReasonExhausted) != 0 ||
			e.researchJobs(router.ReasonFresh) != 0 {
			t.Errorf("%d other re-search runs, %d exhausted, %d fresh queued", n,
				e.researchJobs(router.ReasonExhausted), e.researchJobs(router.ReasonFresh))
		}
	})
}

// A dead link's movie or episode filled again between the search and its route (revived, replaced): the
// route adds nothing, not even an upgrade of the new file.
func TestDeadLinkRouteStopsOnceFilled(t *testing.T) {
	t.Parallel()
	for _, q := range []struct {
		name string
		p    router.ResearchRequest
		eps  []int64
		fill string
	}{
		{"movie", router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonDeadLink},
			nil, `UPDATE version SET state = 'filled' WHERE id = 101`},
		{"episode", router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{1001},
			Reason: router.ReasonDeadLink}, []int64{1001},
			`UPDATE episode_version_state SET state = 'filled' WHERE version_id = 201 AND episode_id = 1001`},
	} {
		for _, fill := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/filled=%v", q.name, fill), func(t *testing.T) {
				e := deadEnv(t, true, &policy.Wanted{PerHour: 100})
				e.withCandidate(h("9"), q.eps...)
				e.research(q.p)
				if ran, err := e.w.RunOne(e.ctx); err != nil || !ran { // the search; its route is queued
					t.Fatalf("search ran %v: %v", ran, err)
				}
				if fill {
					e.exec(q.fill)
				}
				e.drain()
				want := 1
				if fill {
					want = 0
				}
				if n := len(e.rt.Adders["rd"].Adds()); n != want {
					t.Errorf("%d adds, want %d", n, want)
				}
			})
		}
	}
}

// Two dead links of one series deferred for the same window by a spent per_hour are two jobs; both are
// searched once the hour's searches expire.
func TestDeadLinkTwoDeferralsKeepTheirEpisodes(t *testing.T) {
	t.Parallel()
	e := deadEnv(t, true, &policy.Wanted{PerHour: 2})
	e.accept = false
	for range 2 {
		e.exec(`INSERT INTO acquire_run (source, version_id, version_key, title_id, target, state, created_at,
			updated_at) VALUES ('wanted', 102, 'movie', 2, 'movie', 'routed', ?, ?)`,
			store.Millis(e.clock()), store.Millis(e.clock()))
	}
	for _, ep := range []int64{1001, 1004} {
		e.research(router.ResearchRequest{TitleID: 10, VersionID: 201, Version: "series", Episodes: []int64{ep},
			Reason: router.ReasonDeadLink})
	}
	if n := e.researchJobs(router.ReasonDeadLink); n != 2 {
		t.Fatalf("%d deferred dead-link searches, want 2", n)
	}
	e.advance(time.Hour + 2*time.Second)
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE research = ? AND target IN ('s01e01', 's02e01')`,
		router.ReasonDeadLink); n != 2 {
		t.Errorf("%d dead-link searches, want 2", n)
	}
}

// A reconcile re-search whose search finds grabbing off by then waits for grabbing again (as its job
// does), and a run skipped that way counts as no search: again_after and the wanted loop do not hold the target.
func TestGatedResearchRunNotASearch(t *testing.T) {
	t.Parallel()
	t.Run("reconcile waits", func(t *testing.T) {
		e := newEnv(t, true, &policy.Wanted{PerHour: 10, AgainAfter: policy.Duration(24 * time.Hour)})
		e.accept = false
		e.research(router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonReconcile})
		e.pol.Router.Enabled = false
		e.drain()
		if n := e.researchJobs(router.ReasonReconcile); n != 1 || len(e.searches()) != 0 {
			t.Fatalf("%d reconcile re-searches waiting, %d searches", n, len(e.searches()))
		}
		e.pol.Router.Enabled = true
		e.advance(reconcileRecheck + time.Second)
		e.drain()
		if n := len(e.searches()); n != 1 {
			t.Errorf("%d searches once grabbing is back, want 1", n)
		}
	})
	t.Run("not counted", func(t *testing.T) {
		e := newEnv(t, true, &policy.Wanted{PerHour: 10, AgainAfter: policy.Duration(24 * time.Hour)})
		e.accept = false
		stalled := router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonStalled}
		e.research(stalled)
		e.pol.Versions["movie"].Grab = false
		e.drain()
		if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE state = 'skipped' AND infra = ?`, runGated); n != 1 {
			t.Fatalf("%d gated runs", n)
		}
		last, err := e.a.lastSearched(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if l := last[runKey(101, "movie")]; l.complete != 0 || l.partial != 0 {
			t.Errorf("gated run counted as searched: %+v", l)
		}
		e.pol.Versions["movie"].Grab = true
		e.research(stalled)
		e.drain()
		if n := len(e.searches()); n != 1 {
			t.Errorf("%d searches after grab came back, want 1 (not held by again_after)", n)
		}
	})
}

// A gated run (infra 3) is no search, so it does not use router.wanted.per_hour: with per_hour 1 the next
// re-search runs at once instead of being deferred.
func TestGatedRunNotInHourlyBudget(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 1, AgainAfter: policy.Duration(24 * time.Hour)})
	e.accept = false
	e.research(router.ResearchRequest{TitleID: 1, VersionID: 101, Version: "movie", Reason: router.ReasonStalled})
	e.pol.Versions["movie"].Grab = false
	e.drain()
	if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE infra = ?`, runGated); n != 1 {
		t.Fatalf("%d gated runs", n)
	}
	e.pol.Versions["movie"].Grab = true
	sum, err := e.a.Research(e.ctx, router.ResearchRequest{TitleID: 2, VersionID: 102, Version: "movie",
		Reason: router.ReasonStalled})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Queued != 1 || sum.Deferred != 0 {
		t.Errorf("re-search %+v, want queued at once", sum)
	}
	list, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing})
	if err != nil {
		t.Fatal(err)
	}
	if list.SearchedLastHour != 1 {
		t.Errorf("searched last hour %d, want 1 (the gated run left out)", list.SearchedLastHour)
	}
}
