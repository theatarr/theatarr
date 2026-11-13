package acquire

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// Follow-ups (docs/design/router.md "Re-search after a failure"). A release of new content (availability.Fresh: an
// episode aired, or a movie's digital or physical release, within the last 7 days) is often not cached yet when RSS
// first sees it, and RSS lists it once. So when a route for new content misses everywhere (ReasonExhausted), besides
// the re-search at once, the title is searched again 1 h, 2 h, 4 h, 8 h, ... after the miss (the miss verdicts of new
// content last an hour, so each runs after they expire), while it has new content. One chain per title and version,
// one follow-up of it at a time: each queues the next before it searches. A follow-up searches what is new when it
// runs: for a series its monitored, wanted episodes aired within the last 7 days (an old episode that came in a batch
// with a new one is not followed), for a movie the movie. A title gets no second chain while the content that
// started one is new: a miss starts one only when no chain of the title is pending or was queued since the newest new
// date (so neither a follow-up's own miss nor the re-search it causes restarts it).
//
// A follow-up is a re-search: nothing for a version not active or while Pause is on, and it counts against
// router.wanted.per_hour (a spent hour skips that step; the next is queued already). It is not held back by
// again_after (the re-search at once used it), and stops when nothing is new any more, the title's version is
// unmonitored, or its new items are filled, unmonitored or have a download in flight.
//
// A dead link (router.ReasonDeadLink, queued by the library when relink marks an active version's link dead: no
// provider holds its file) gets one such search, not a chain: its movie, or its own episodes (old or new), with the
// same stops: nothing while router or grab is off or the version is not active (the job ends skipped, it does not
// wait), the version or episode unmonitored, filled again (revived or replaced) or a download in flight. It counts
// against router.wanted.per_hour (a spent hour defers it to when a search of the hour expires, as researchLater does
// for a re-search, and it is checked again then) and runs whatever router.wanted.missing says. Its search and route
// carry the reason (acquire_run.research): the search is skipped if grabbing or the library turned off meanwhile, one
// no indexer answered is not asked for again (retryInfraFailed), and
// the router adds only for a target still missing and starts no exhausted re-search after it (router "deadLinkRoute").
// The dead release is not excluded: found cached again, re-adding it is the repair.
//
// With router.wanted.retry_new on (on by default) a chain also starts without a route: when a missing search by the
// wanted loop or by Search now ends with nothing accepted and its target holds new content (retryNew). The first
// search of a just-aired episode is usually that one, and the loop would take the item again only after again_after.
// It has the same steps, stops and per_hour charge, but is a chain of its own. A manual search, a search on add and a
// re-search or follow-up's own search start none.
//
// The rule it is built to: nothing this chain writes is read by an ordinary path, and turning the switch off removes
// its queued work. An install that never had the switch on behaves, and reads the run history, as a build without
// it; the limits after the switch was on are in docs/design/router.md.
//   - Its jobs have their own key (followUpKey) and carry RetryNew; its searches' runs have research
//     researchRetryNew. Every reader of the run history that decides for an ordinary search leaves those runs out
//     (ordinaryRuns: again_after for the loop and for re-searches, the loop's order, a
//     Search now request's "already searched or queued", the retry budget after no answer), and the hourly count
//     holds them only while the switch is on (hourRuns). The chain after a route that missed everywhere starts by
//     its own key alone.
//   - Each of its requests and each of its searches looks at the switch first, before any other check (Research,
//     run): with the switch off the request does nothing and the run ends gated.
//   - The settings save that turns the switch off cancels what is queued (CancelRetryNew).
//
// It also gives way: it does not start, and ends at its next step without a search, once the exhausted chain's key
// was seen in the fresh window (exhaustedChain). So a title can get both kinds in one week, one after the other.

// retryNew starts the follow-ups of a marked search's target that ended with nothing accepted (run, finishFanout).
// The run is recorded already, so a failure here is logged, not returned.
func (a *Acquirer) retryNew(ctx context.Context, t target) {
	vp := a.pol.Versions[t.key]
	if !a.set.RetryNew || vp == nil || t.reason != OnlyMissing {
		return
	}
	if err := a.startFollowUps(ctx, vp, router.ResearchRequest{TitleID: t.titleID, VersionID: t.versionID,
		Version: t.key, Episodes: t.episodes, RetryNew: true}); err != nil {
		a.log.Warn("follow-up searches not queued", "title", t.titleID, "version", t.key, "target", t.name, "err", err)
	}
}

// followUpAfter is the wait from the miss to the nth follow-up: 1 h doubled each time.
func followUpAfter(n int) time.Duration { return time.Hour << (n - 1) }

// followUpKey is the dedupe key prefix of a title's follow-ups for a version; each step appends its number. A chain
// an empty search started (retry_new) has its own prefix, so that the exhausted route's never matches it.
func followUpKey(p router.ResearchRequest) string {
	if p.RetryNew {
		return fmt.Sprintf("%s/%s/%s/%d/", router.JobResearch, retryNewKey, p.Version, p.TitleID)
	}
	return fmt.Sprintf("%s/%s/%s/%d/", router.JobResearch, router.ReasonFresh, p.Version, p.TitleID)
}

// retryNewKey is ReasonFresh's place in the key of a retry_new chain.
const retryNewKey = "retry_new"

// retryNewOff is why a retry_new chain's request or search does nothing.
const retryNewOff = "router.wanted.retry_new is off"

// CancelRetryNew ends the queued work of the retry_new chains when the switch is off (it does nothing while it is
// on): their queued requests (steps, and searches asked for again after no answer) and the queued search jobs of
// their runs end as cancelled, and those runs end skipped and gated, in one transaction. A queued search job's run
// is queued, or still searching when a shutdown interrupted the search and handed the job back. A search that is
// running is left to end by itself. It runs with every published settings generation (serve's AfterPublish) and at
// start once the acquirer is built, so the save that turns the switch off cancels them, and a restart with the
// switch off cancels what a handler queued after that.
//
// It first looks for such a queued job (retryNewQueued: the queued re-search requests and the queued search jobs
// with their runs by id, no scan of the run table) and without one writes nothing and opens no transaction: that is
// all an install that never had the switch on does here. Not found that way, and so left as it is until a later
// call finds a queued job of a chain: a chain's run still queued or searching whose search job is neither queued
// nor running (the job ended without ending its run). No job is left to search it.
// A handler already running keeps the settings it started with: what it still queues does nothing when it runs
// (Research, run).
func (a *Acquirer) CancelRetryNew(ctx context.Context) (int, error) {
	if a.set.RetryNew || a.st == nil { // no store: an acquirer built only to judge releases
		return 0, nil
	}
	var queued bool
	if err := a.st.Reader().QueryRowContext(ctx, retryNewQueued, router.JobResearch, JobSearch).Scan(&queued); err != nil ||
		!queued {
		return 0, err
	}
	var n int64
	err := a.st.WriteTx(ctx, func(tx *sql.Tx) error {
		n = 0
		at := store.Millis(a.now())
		const why = "cancelled: " + retryNewOff
		const end = `UPDATE job SET state = 'done', last_error = ?, finished_at = ?, updated_at = ?
			WHERE state = 'queued' AND kind = ? AND `
		for _, q := range []struct {
			kind, where string
		}{
			{router.JobResearch, retryNewJobs},
			{JobSearch, `dedupe_key IN (SELECT '` + JobSearch + `/' || id FROM acquire_run
				WHERE state IN ('queued', 'searching') AND ` + retryNewRuns + `)`},
		} {
			res, err := tx.ExecContext(ctx, end+q.where, why, at, at, q.kind)
			if err != nil {
				return err
			}
			m, _ := res.RowsAffected()
			n += m
		}
		// The runs without a search job left to run them: not one whose search is running.
		_, err := tx.ExecContext(ctx, `UPDATE acquire_run SET state = 'skipped', message = ?, infra = ?, updated_at = ?
			WHERE state IN ('queued', 'searching') AND `+retryNewRuns+` AND NOT EXISTS (SELECT 1 FROM job j
			WHERE j.state IN ('queued', 'running') AND j.dedupe_key = '`+JobSearch+`/' || acquire_run.id)`,
			retryNewOff+": nothing searched", runGated, at)
		return err
	})
	if err == nil && n > 0 {
		a.log.Info("retry_new is off: its queued searches were cancelled", "jobs", n)
	}
	return int(n), err
}

// retryNewQueued says whether a queued job of a retry_new chain exists: a request (its kind is the first argument),
// or a search job (the second) of a chain's run that is queued or searching. These are the jobs CancelRetryNew
// ends. Both halves read the queued jobs of one kind by the job index, and a search job's run by its id.
const retryNewQueued = `SELECT EXISTS (SELECT 1 FROM job WHERE kind = ? AND state = 'queued' AND ` + retryNewJobs + `)
	OR EXISTS (SELECT 1 FROM job j WHERE j.kind = ? AND j.state = 'queued' AND EXISTS (SELECT 1 FROM acquire_run r
	WHERE r.id = CAST(substr(j.dedupe_key, length('` + JobSearch + `/') + 1) AS INTEGER)
	AND j.dedupe_key = '` + JobSearch + `/' || r.id AND r.state IN ('queued', 'searching')
	AND COALESCE(r.research, '') = '` + researchRetryNew + `'))`

// exhaustedChain says whether the title has a chain after a route that missed everywhere: one queued, or one that
// ran in the fresh window ending at until. A retry_new chain gives way to it.
func (a *Acquirer) exhaustedChain(ctx context.Context, p router.ResearchRequest, until time.Time) (bool, error) {
	p.RetryNew = false
	return a.q.Seen(ctx, router.JobResearch, followUpKey(p), until.Add(-availability.FreshWindow))
}

// startFollowUps queues the first follow-up of a title whose route missed everywhere, when the route held new content
// and no chain of the title is pending or ran in this fresh window (since the newest new date).
func (a *Acquirer) startFollowUps(ctx context.Context, vp *policy.VersionPolicy, p router.ResearchRequest) error {
	now := a.now()
	until, err := availability.Fresh(ctx, a.st.Reader(), vp.Kind, p.TitleID, p.Episodes, now)
	if err != nil || until.IsZero() {
		return err
	}
	next := router.ResearchRequest{TitleID: p.TitleID, VersionID: p.VersionID, Version: p.Version,
		Reason: router.ReasonFresh, MissedAt: now, RetryNew: p.RetryNew}
	if p.RetryNew {
		if seen, err := a.exhaustedChain(ctx, next, until); err != nil || seen {
			return err
		}
	}
	if seen, err := a.q.Seen(ctx, router.JobResearch, followUpKey(next), until.Add(-availability.FreshWindow)); err != nil ||
		seen {
		return err
	}
	return a.queueFollowUp(ctx, next, 1, until)
}

// queueFollowUp queues follow-up n of p, unless it would run after until (the content is no longer new then).
func (a *Acquirer) queueFollowUp(ctx context.Context, p router.ResearchRequest, n int, until time.Time) error {
	at := p.MissedAt.Add(followUpAfter(n))
	if at.After(until) {
		a.log.Info("follow-up searches done: the content is no longer new by then", "title", p.TitleID,
			"version", p.Version, "follow-ups", n-1)
		return nil
	}
	p.Step = n
	if _, _, err := a.q.Enqueue(ctx, router.JobResearch, p, jobs.EnqueueOptions{RunAt: at,
		DedupeKey: followUpKey(p) + strconv.Itoa(n), MaxAttempts: 1}); err != nil {
		return err
	}
	a.log.Info("follow-up search queued", "title", p.TitleID, "version", p.Version, "step", n, "at", at)
	return nil
}

// followUp runs one follow-up: the checks again, the next follow-up queued, then the search. One without MissedAt
// (asked for again after no indexer answered, or a dead link's) only searches.
func (a *Acquirer) followUp(ctx context.Context, vp *policy.VersionPolicy, p router.ResearchRequest,
	sum *ResearchSummary) error {
	now := a.now()
	skip := func(why string) error {
		sum.Skipped++
		a.log.Info("follow-up search stopped", "title", p.TitleID, "version", p.Version, "step", p.Step, "why", why)
		return nil
	}
	var monitored bool
	var state string
	if err := a.st.Reader().QueryRowContext(ctx, `SELECT monitored, state FROM version WHERE id = ?`,
		p.VersionID).Scan(&monitored, &state); err != nil {
		return err
	}
	if !monitored {
		return skip("the version is unmonitored")
	}
	var ts []target
	var until time.Time
	dead := p.Reason == router.ReasonDeadLink
	if vp.Kind == policy.KindSeries {
		var eps []int64
		if dead {
			eps = p.Episodes
		} else {
			e, u, err := availability.FreshEpisodes(ctx, a.st.Reader(), p.TitleID, now)
			if err != nil {
				return err
			}
			if len(e) == 0 {
				return skip("no episode is new any more")
			}
			eps, until = e, u
		}
		if len(eps) == 0 {
			return skip("no episode to search")
		}
		if !dead && p.MissedAt.IsZero() && len(p.Episodes) > 0 { // asked for again: the failed run's own episodes
			eps = slices.DeleteFunc(eps, func(id int64) bool { return !slices.Contains(p.Episodes, id) })
		}
		got, err := a.seriesTargets(ctx, a.st.Reader(), p.VersionID, p.TitleID, p.Version,
			Selection{EpisodeIDs: eps, Only: OnlyMissing}, store.Millis(now))
		if err != nil {
			return err
		}
		ts = bySeason(got)
	} else {
		if !dead {
			u, err := availability.Fresh(ctx, a.st.Reader(), vp.Kind, p.TitleID, nil, now)
			if err != nil {
				return err
			}
			if u.IsZero() {
				return skip("the movie is no longer new")
			}
			until = u
		}
		available, err := availability.Movie(ctx, a.st.Reader(), a.pol, p.TitleID, p.VersionID, p.Version, now)
		if err != nil {
			return err
		}
		if state == "wanted" && available {
			ts = []target{{titleID: p.TitleID, versionID: p.VersionID, key: p.Version, name: "movie"}}
		}
	}
	var todo []target
	for _, t := range ts {
		busy, err := a.inFlight(ctx, t)
		if err != nil {
			return err
		}
		if !busy {
			todo = append(todo, t)
		}
	}
	if len(todo) == 0 {
		return skip("nothing wanted: filled, unmonitored or a download in flight")
	}
	if p.RetryNew {
		if seen, err := a.exhaustedChain(ctx, p, until); err != nil {
			return err
		} else if seen {
			return skip("the title has a chain after a route that missed everywhere")
		}
	}
	if !p.MissedAt.IsZero() {
		if err := a.queueFollowUp(ctx, p, p.Step+1, until); err != nil {
			return err
		}
	}
	searched, err := a.wantedUsed(ctx, now)
	if err != nil {
		return err
	}
	// A retry_new chain's searches are marked in the run history (researchRetryNew), not in their job.
	research := p.Reason
	if p.RetryNew {
		research = researchRetryNew
	}
	var later []int64
	deferred := false
	for _, t := range todo {
		if searched >= a.set.PerHour && dead {
			// A dead link's search waits for the hour like a re-search after a failure (researchLater): with the
			// missing search off nothing else would search the item.
			sum.Deferred++
			deferred = true
			later = append(later, t.episodes...)
			a.log.Info("dead-link search deferred: router.wanted.per_hour used", "title", p.TitleID,
				"version", p.Version, "target", t.name)
			continue
		}
		if searched >= a.set.PerHour {
			sum.Skipped++
			a.log.Info("follow-up search skipped: router.wanted.per_hour used", "title", p.TitleID,
				"version", p.Version, "target", t.name, "step", p.Step)
			continue
		}
		if err := a.start(ctx, SourceWanted, 0, t, research); err != nil {
			return err
		}
		sum.Queued++
		searched++
		a.log.Info("follow-up search started", "title", p.TitleID, "version", p.Version, "target", t.name,
			"step", p.Step)
	}
	if deferred {
		at, err := a.researchLater(ctx, p, later, now)
		if err != nil {
			return err
		}
		sum.RetryAt = at
	}
	return nil
}
