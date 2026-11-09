package acquire

import (
	"context"
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
// A follow-up is a re-search: nothing for a version not active, router.enabled off or grab off, and it counts against
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

// followUpAfter is the wait from the miss to the nth follow-up: 1 h doubled each time.
func followUpAfter(n int) time.Duration { return time.Hour << (n - 1) }

// followUpKey is the dedupe key prefix of a title's follow-ups for a version; each step appends its number.
func followUpKey(p router.ResearchRequest) string {
	return fmt.Sprintf("%s/%s/%s/%d/", router.JobResearch, router.ReasonFresh, p.Version, p.TitleID)
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
		Reason: router.ReasonFresh, MissedAt: now}
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
	if !p.MissedAt.IsZero() {
		if err := a.queueFollowUp(ctx, p, p.Step+1, until); err != nil {
			return err
		}
	}
	var searched int
	if err := a.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM acquire_run WHERE source = ?
		AND search_run IS NULL AND infra <> 3 AND created_at > ?`, SourceWanted, store.Millis(now.Add(-time.Hour))).Scan(&searched); err != nil {
		return err
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
		if err := a.start(ctx, SourceWanted, 0, t, p.Reason); err != nil {
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
