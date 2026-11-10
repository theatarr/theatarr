package acquire

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// Re-search after a failure (router.JobResearch, docs/design/router.md "Re-search after a failure"): the router asks for
// a search of a title's version right away when a download failed for a reason the arrs and decypharr answer with
// "blocklist and search again" (every candidate missed, stalled, dangerous files, the library's guard). It is a
// wanted search started early, so it obeys the same rules:
//
//   - nothing for a version that is not active, or while router.enabled is false; a re-search queued by
//     `router reconcile` (reason reconcile) is queued again an hour later instead, until both are on;
//   - a target with a download in flight is skipped;
//   - runs have source wanted and count against router.wanted.per_hour; when the hour's searches are used, the
//     re-search is queued again for when the oldest search of the hour leaves the window (the
//     wanted loop may not search the item at all, with missing or cutoff searches off);
//   - one re-search per target within router.wanted.again_after (partialAgain after a partial answer), so a release
//     that keeps failing cannot loop; one that got no answer is asked for again after infraBackoff, a few times.
//
// After a miss everywhere on new content, follow-up searches come later as well, and a link relink marked dead gets one
// search (followup.go).
//
// A movie is searched whole; series episodes as one season search per season (several episodes) or an episode
// search (one), like the wanted loop.

// ResearchSummary is what one re-search request did.
type ResearchSummary struct {
	Queued  int    `json:"queued"`
	Skipped int    `json:"skipped"`
	Off     string `json:"off,omitempty"`
	// Deferred counts the targets held back by router.wanted.per_hour; they are asked for again at RetryAt.
	Deferred int       `json:"deferred,omitempty"`
	RetryAt  time.Time `json:"retry_at,omitzero"`
}

func (a *Acquirer) handleResearch(ctx context.Context, j *jobs.Job) error {
	var p router.ResearchRequest
	if err := j.Decode(&p); err != nil {
		return jobs.Permanent(err)
	}
	ctx = a.captureRuntime(ctx)
	_, err := a.Research(ctx, p)
	return err
}

// Research starts the searches a router.ResearchRequest asks for, within the wanted loop's pacing.
func (a *Acquirer) Research(ctx context.Context, p router.ResearchRequest) (*ResearchSummary, error) {
	// The hour is checked and spent under the lock the wanted round and the drains take.
	a.drainMu.Lock()
	defer a.drainMu.Unlock()
	// A request of a retry_new chain (a step, or its search asked for again) does nothing once the switch is off:
	// looked at before any other check.
	if p.RetryNew && !a.set.RetryNew {
		a.log.Info("re-search skipped", "title", p.TitleID, "version", p.Version, "reason", p.Reason, "why", retryNewOff)
		return &ResearchSummary{Off: retryNewOff}, nil
	}
	sum := &ResearchSummary{Off: a.researchOff(p.Version, p.Reason)}
	vp := a.pol.Versions[p.Version]
	if sum.Off != "" && p.Reason == router.ReasonReconcile {
		at, err := a.waitForGrabbing(ctx, p, sum.Off)
		if err != nil {
			return nil, err
		}
		sum.RetryAt = at
		return sum, nil
	}
	if sum.Off != "" {
		a.log.Info("re-search skipped", "title", p.TitleID, "version", p.Version, "reason", p.Reason, "why", sum.Off)
		return sum, nil
	}
	switch p.Reason {
	case router.ReasonFresh, router.ReasonDeadLink:
		return sum, a.followUp(ctx, vp, p, sum)
	case router.ReasonExhausted:
		// New content is searched again later too (followup.go).
		if err := a.startFollowUps(ctx, vp, p); err != nil {
			return nil, err
		}
	}
	var ts []target
	if vp.Kind != policy.KindSeries {
		available, err := availability.Movie(ctx, a.st.Reader(), a.pol, p.TitleID, p.VersionID, p.Version, a.now())
		if err != nil {
			return nil, err
		}
		if !available {
			sum.Skipped++
			return sum, nil
		}
		ts = []target{{titleID: p.TitleID, versionID: p.VersionID, key: p.Version, name: "movie"}}
	} else if len(p.Episodes) > 0 {
		got, err := a.seriesTargets(ctx, a.st.Reader(), p.VersionID, p.TitleID, p.Version,
			Selection{EpisodeIDs: p.Episodes}, store.Millis(a.now()))
		if err != nil {
			return nil, err
		}
		ts = bySeason(got)
	}
	now := a.now()
	searched, err := a.wantedUsed(ctx, now)
	if err != nil {
		return nil, err
	}
	var later []int64
	deferred := false
	for _, t := range ts {
		why, err := a.researchBlocked(ctx, t, now, searched)
		if err != nil {
			return nil, err
		}
		if why == errPerHour {
			sum.Deferred++
			deferred = true
			later = append(later, t.episodes...)
			why = fmt.Sprintf("router.wanted.per_hour %d used; asked for again when a search of the hour expires",
				a.set.PerHour)
		}
		if why != "" {
			sum.Skipped++
			a.log.Info("re-search skipped", "title", p.TitleID, "version", p.Version, "target", t.name,
				"reason", p.Reason, "why", why)
			continue
		}
		if err := a.start(ctx, SourceWanted, 0, t, p.Reason); err != nil {
			return nil, err
		}
		sum.Queued++
		searched++
		a.log.Info("re-search started", "title", p.TitleID, "version", p.Version, "target", t.name,
			"reason", p.Reason, "download", p.DownloadID)
	}
	if deferred {
		at, err := a.researchLater(ctx, p, later, now)
		if err != nil {
			return nil, err
		}
		sum.RetryAt = at
	}
	return sum, nil
}

// researchOff says why a re-search of a version may not search now, or "": the version not active, router.enabled
// or grab off, and for a dead link the library not writing for the version (disabled, followed, no roots). The job
// checks it when it runs and its search again before it starts (run), so a switch turned off in between ends both.
func (a *Acquirer) researchOff(version, reason string) string {
	vp := a.pol.Versions[version]
	switch {
	case vp == nil || vp.Mode != policy.ModeActive:
		return "version " + version + " is not active"
	case !a.pol.RouterSettings().Enabled:
		return "router.enabled is false"
	case !a.pol.Grabs(version):
		return "version " + version + " has grab off"
	case reason == router.ReasonDeadLink && !library.NewGate(a.pol).Active(version):
		return "the library does not write for version " + version
	}
	return ""
}

// waitForGrabbing queues a reconcile re-search again reconcileRecheck from now: it replaces a row it ended, so it
// waits for grabbing instead of being dropped (checked when its job runs, and again when its search runs).
func (a *Acquirer) waitForGrabbing(ctx context.Context, p router.ResearchRequest, why string) (time.Time, error) {
	at := a.now().Add(reconcileRecheck)
	key := fmt.Sprintf("%s/%s/%s/%d/off/%d", router.JobResearch, p.Reason, p.Version, p.TitleID, store.Millis(at))
	if _, _, err := a.q.Enqueue(ctx, router.JobResearch, p, jobs.EnqueueOptions{RunAt: at, DedupeKey: key,
		MaxAttempts: 1}); err != nil {
		return time.Time{}, err
	}
	a.log.Info("re-search waiting for grabbing", "title", p.TitleID, "version", p.Version, "why", why, "at", at)
	return at, nil
}

// reconcileRecheck is how long a reconcile re-search waits before it looks again whether grabbing is on.
const reconcileRecheck = time.Hour

// errPerHour is researchBlocked's answer when router.wanted.per_hour is used.
const errPerHour = "per_hour"

// researchLater queues the re-search of the targets per_hour held back (their episodes; the movie when none) for
// when the oldest wanted search of the hour leaves the window.
func (a *Acquirer) researchLater(ctx context.Context, p router.ResearchRequest, episodes []int64, now time.Time) (
	time.Time, error) {
	at := now.Add(time.Hour)
	free, ok, err := a.budgetFreeAt(ctx, now)
	if err != nil {
		return time.Time{}, err
	}
	if ok {
		at = free
	}
	p.Episodes = episodes
	// The episodes are part of the key: two deferrals of one title for the same window (two dead links of a series)
	// are two jobs, not one that drops the other's episodes.
	key := fmt.Sprintf("%s/later/%s/%d/%s/%d/%d/%s", router.JobResearch, p.Version, p.TitleID, p.Reason, p.DownloadID,
		store.Millis(at), episodeKey(episodes))
	if _, _, err := a.q.Enqueue(ctx, router.JobResearch, p, jobs.EnqueueOptions{RunAt: at, DedupeKey: key,
		MaxAttempts: 1}); err != nil {
		return time.Time{}, err
	}
	a.log.Info("re-search deferred", "title", p.TitleID, "version", p.Version, "reason", p.Reason, "at", at)
	return at, nil
}

// episodeKey is a dedupe key part for a set of episodes: sorted, comma-separated ("" for none).
func episodeKey(eps []int64) string {
	ids := slices.Clone(eps)
	slices.Sort(ids)
	var b strings.Builder
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(id, 10))
	}
	return b.String()
}

// researchBlocked says why a target is not searched again now, or "".
func (a *Acquirer) researchBlocked(ctx context.Context, t target, now time.Time, searched int) (string, error) {
	if searched >= a.set.PerHour {
		return errPerHour, nil
	}
	busy, err := a.inFlight(ctx, t)
	if err != nil {
		return "", err
	}
	if busy {
		return "a download for it is in flight", nil
	}
	// A partial re-search counts for partialAgain only (again_after when shorter); one without an answer not at all.
	var n int
	again := store.Millis(now.Add(-a.set.AgainAfter))
	if err := a.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM acquire_run WHERE version_id = ?
		AND target = ? AND research IS NOT NULL AND `+ordinaryRuns+`
		AND ((infra = 0 AND created_at > ?) OR (infra = 2 AND created_at > ?))`,
		t.versionID, t.name, again, max(again, store.Millis(now.Add(-partialAgain)))).Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		return "searched again after a failure within router.wanted.again_after", nil
	}
	return "", nil
}

// bySeason merges episode targets into one season target per season with several episodes.
func bySeason(ts []target) []target {
	var out []target
	idx := map[int]int{}
	for _, t := range ts {
		if t.season == nil {
			out = append(out, t)
			continue
		}
		i, ok := idx[*t.season]
		if !ok {
			idx[*t.season] = len(out)
			out = append(out, t)
			continue
		}
		m := &out[i]
		m.episodes = append(slices.Clone(m.episodes), t.episodes...)
		m.name = fmt.Sprintf("s%02d", *t.season)
	}
	return out
}
