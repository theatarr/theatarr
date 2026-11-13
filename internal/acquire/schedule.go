package acquire

import (
	"context"
	"database/sql"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// The search schedule. Every automatic search starts from one place: Round, which starts the wanted items that are
// due. An item is one row of the Wanted page (one library's missing or cutoff-unmet part of a film or of one series
// season), and its next search lives in search_request (next_at, tries, why). An item without a row is unscheduled:
// a full round takes it while its backlog switch (router.wanted.missing, cutoff_unmet) is on, unless its latest run
// of any source is recent enough (then the row is written with the time that run gives).
//
// what sets a row due early (why): Search now (asked), a failure the router reported (Research: the router's reason),
// and the retries of new content (new). A row with a why is searched whatever the backlog switches say. When a
// search of an item ends, whatever started it, its row is rescheduled by nextSearch; a search the loop started
// reschedules its own row, any other search the item's regular or new rows. A loop search overtaken by Pause leaves
// its row due.
//
// All loop searches share router.wanted.per_hour: asked first, then the other rows with a why, then the backlog
// (unscheduled items newest added first, then regular rows); inside each, the earliest due first. Due times say from
// when an item may be searched, not when it will be.

// Why values that are not a failure reason.
const (
	whyAsked = "asked"
	whyNew   = "new"
)

// roundEvery is the time between full rounds.
const roundEvery = 15 * time.Minute

// noAnswerTries is how many times in a row a row that is not new is tried again an hour after no indexer answered.
const noAnswerTries = 3

// schedRow is one search_request row.
type schedRow struct {
	id      int64
	titleID int64
	key     string
	reason  string
	season  int // -1: a movie
	nextAt  int64
	tries   int
	why     string // "": the regular schedule
}

func seasonOf(s *int) int {
	if s == nil {
		return -1
	}
	return *s
}

func requestKey(titleID int64, key, reason string, season int) string {
	return strconv.FormatInt(titleID, 10) + "/" + key + "/" + reason + "/" + strconv.Itoa(season)
}

func targetKey(t target) string { return requestKey(t.titleID, t.key, t.reason, seasonOf(t.season)) }

// seasonKey keys a version's runs by season (-1 for a film), whatever the run's target text.
func seasonKey(versionID int64, season int) string {
	return strconv.FormatInt(versionID, 10) + "/" + strconv.Itoa(season)
}

// switchOn says whether the backlog switch of reason is on. router.wanted.enabled false turns both off.
func (a *Acquirer) switchOn(reason string) bool {
	if !a.set.Enabled {
		return false
	}
	if reason == OnlyMissing {
		return a.set.Missing
	}
	return a.set.CutoffUnmet != policy.CutoffOff
}

// nextSearch is the back-off rule: how long after a search of row ends it is searched again, and the row's new
// tries and why; keep is false when the row goes (not scheduled). kind is the run's acquire_run.infra; fresh says
// whether the row's content is new (availability.Fresh), on whether its backlog switch is on. When no indexer
// answered, a new row is tried again an hour later as it is (an outage does not advance the ladder); another row an
// hour later at most noAnswerTries times in a row, then by the ordinary rule.
func nextSearch(r schedRow, kind int, fresh, on bool, againAfter time.Duration) (wait time.Duration, tries int,
	why string, keep bool) {
	if kind == runInfra && r.why == whyNew {
		return time.Hour, r.tries, r.why, true
	}
	if kind == runInfra && r.tries < noAnswerTries {
		return time.Hour, r.tries + 1, r.why, true
	}
	if fresh && r.reason == OnlyMissing {
		n := 0
		if r.why == whyNew {
			n = r.tries
		}
		wait = againAfter
		if n < 16 {
			wait = min(time.Hour<<n, againAfter)
		}
		return wait, n + 1, whyNew, true
	}
	if !on {
		return 0, 0, "", false
	}
	if kind == runPartial {
		return min(againAfter, partialAgain), 0, "", true
	}
	return againAfter, 0, "", true
}

// fresh says whether t's content is new now (missing targets only).
func (a *Acquirer) fresh(ctx context.Context, t target) (bool, error) {
	if t.reason != OnlyMissing {
		return false, nil
	}
	kind := policy.KindMovie
	if vp := a.pol.Versions[t.key]; vp != nil {
		kind = vp.Kind
	}
	until, err := availability.Fresh(ctx, a.st.Reader(), kind, t.titleID, t.episodes, a.now())
	return !until.IsZero(), err
}

// reschedule applies nextSearch to the rows a finished run of t searched, in the run's transaction: for a loop run
// (source wanted) its own row while it still has the why the run started for (a failure event or Search now that
// marked it again meanwhile keeps it due), for any other its item's regular and new rows. kind runGated (Pause
// overtook a loop run) leaves the row due.
func (a *Acquirer) reschedule(ctx context.Context, tx *sql.Tx, source string, t target, kind int) error {
	now := store.Millis(a.now())
	season := seasonOf(t.season)
	if kind == runGated {
		if source == SourceWanted {
			_, err := tx.ExecContext(ctx, `UPDATE search_request SET next_at = ? WHERE title_id = ? AND version_key = ?
				AND reason = ? AND season = ? AND COALESCE(why, '') = ?`, now, t.titleID, t.key, t.reason, season, t.why)
			return err
		}
		return nil
	}
	q := `SELECT id, reason, tries, COALESCE(why, '') FROM search_request WHERE title_id = ? AND version_key = ?
		AND season = ? AND reason = ? AND COALESCE(why, '') = ?`
	args := []any{t.titleID, t.key, season, t.reason, t.why}
	if source != SourceWanted {
		q = `SELECT id, reason, tries, COALESCE(why, '') FROM search_request WHERE title_id = ? AND version_key = ?
			AND season = ? AND (reason = ? OR ? = '') AND COALESCE(why, 'new') = 'new'`
		args = []any{t.titleID, t.key, season, t.reason, t.reason}
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	var rs []schedRow
	for rows.Next() {
		var r schedRow
		if err := rows.Scan(&r.id, &r.reason, &r.tries, &r.why); err != nil {
			rows.Close()
			return err
		}
		rs = append(rs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range rs {
		rt := t
		rt.reason = r.reason
		fresh, err := a.fresh(ctx, rt)
		if err != nil {
			return err
		}
		wait, tries, why, keep := nextSearch(r, kind, fresh, a.switchOn(r.reason), a.set.AgainAfter)
		if !keep {
			if _, err := tx.ExecContext(ctx, `DELETE FROM search_request WHERE id = ?`, r.id); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE search_request SET next_at = ?, tries = ?, why = ? WHERE id = ?`,
			now+wait.Milliseconds(), tries, nullString(why), r.id); err != nil {
			return err
		}
	}
	return nil
}

// busySet keys (requestKey) the items a loop search started within the last hour is queued or running for: a
// round and Search now do not start them again. Searches by name never count.
func (a *Acquirer) busySet(ctx context.Context) (map[string]bool, error) {
	rows, err := a.st.Reader().QueryContext(ctx, `SELECT title_id, version_key, reason, COALESCE(season, -1)
		FROM acquire_run WHERE source = ? AND state IN ('queued', 'searching') AND created_at > ?`, SourceWanted,
		store.Millis(a.now().Add(-time.Hour)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var title int64
		var key, reason string
		var season int
		if err := rows.Scan(&title, &key, &reason, &season); err != nil {
			return nil, err
		}
		out[requestKey(title, key, reason, season)] = true
	}
	return out, rows.Err()
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// scheduleRows reads the rows of the active versions, of the titles in scope (nil: all), by requestKey.
func (a *Acquirer) scheduleRows(ctx context.Context, active []string, scope []int64) (map[string]schedRow, error) {
	q := `SELECT id, title_id, version_key, reason, season, next_at, tries, COALESCE(why, '') FROM search_request`
	var args []any
	if scope != nil {
		q += ` WHERE title_id IN (` + store.Placeholders(len(scope)) + `)`
		for _, id := range scope {
			args = append(args, id)
		}
	}
	rows, err := a.st.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]schedRow{}
	for rows.Next() {
		var r schedRow
		if err := rows.Scan(&r.id, &r.titleID, &r.key, &r.reason, &r.season, &r.nextAt, &r.tries, &r.why); err != nil {
			return nil, err
		}
		if slices.Contains(active, r.key) {
			out[requestKey(r.titleID, r.key, r.reason, r.season)] = r
		}
	}
	return out, rows.Err()
}

// fromLastRun is when an unscheduled target is due by its latest run (l): the rule applied to that run, or 0 (due
// now) for none or one long enough ago; tries and why are the row's then.
func (a *Acquirer) fromLastRun(ctx context.Context, t target, l lastRuns, now int64) (at int64, tries int, why string,
	err error) {
	last, kind := l.complete, runComplete
	if l.partial > last {
		last, kind = l.partial, runPartial
	}
	if last == 0 || last+a.set.AgainAfter.Milliseconds() <= now {
		return 0, 0, "", nil
	}
	fresh, err := a.fresh(ctx, t)
	if err != nil {
		return 0, 0, "", err
	}
	wait, tries, why, keep := nextSearch(schedRow{reason: t.reason}, kind, fresh, true, a.set.AgainAfter)
	if !keep || last+wait.Milliseconds() <= now {
		return 0, 0, "", nil
	}
	return last + wait.Milliseconds(), tries, why, nil
}

// roundSelection is the targets a round looks at for one reason: cutoff with the configured cutoff_unmet mode.
func (a *Acquirer) roundSelection(active []string, only string, titles []int64) Selection {
	return Selection{Versions: active, TitleIDs: titles, Only: only, QualityCutoff: a.set.CutoffUnmet == policy.CutoffQuality}
}

// Round starts the due items of the titles in scope (nil: a full round, which also takes the unscheduled items of
// the backlog switches that are on), as many groups as router.wanted.per_hour leaves, skipping items with a download
// in flight (their rows stay due). It does nothing while paused or with no Live library.
func (a *Acquirer) Round(ctx context.Context, scope []int64) (*WantedSummary, error) {
	ctx = a.captureRuntime(ctx)
	sum := &WantedSummary{}
	active := a.pol.SearchVersions()
	switch {
	case !a.pol.RouterSettings().Enabled:
		sum.Off = "paused"
	case len(active) == 0:
		sum.Off = "no version is active"
	}
	if sum.Off != "" {
		return sum, nil
	}
	full := scope == nil
	if full {
		// Episode rows already know their air times; passing one needs no metadata fetch.
		if _, err := a.st.Writer().ExecContext(ctx, `UPDATE version SET state = 'wanted', updated_at = ?
			WHERE monitored = 1 AND state <> 'wanted' AND EXISTS (
				SELECT 1 FROM episode_version_state s JOIN episode e ON e.id = s.episode_id
				WHERE s.version_id = version.id AND s.monitored = 1 AND s.state = 'wanted'
				AND e.removed_at IS NULL AND e.air_date_utc_at <= ?)`, store.Millis(a.now()), store.Millis(a.now())); err != nil {
			return nil, err
		}
	}
	a.drainMu.Lock()
	defer a.drainMu.Unlock()
	now := store.Millis(a.now())
	var err error
	if sum.SearchedLastHour, err = a.wantedUsed(ctx, a.now()); err != nil {
		return nil, err
	}
	for _, key := range active {
		if a.pol.Versions[key].PreferSeaDex {
			if ctx, err = decide.WithStoredSeaDex(ctx, a.st.Reader()); err != nil {
				return nil, err
			}
			break
		}
	}
	rows, err := a.scheduleRows(ctx, active, scope)
	if err != nil {
		return nil, err
	}
	running, err := a.busySet(ctx)
	if err != nil {
		return nil, err
	}
	type due struct {
		t  target
		at int64
	}
	var asked, early, regular []due
	var unscheduled []target
	var dueTitles []int64
	for _, r := range rows {
		if r.nextAt <= now && !slices.Contains(dueTitles, r.titleID) {
			dueTitles = append(dueTitles, r.titleID)
		}
	}
	taken := map[string]bool{}
	if len(dueTitles) > 0 {
		for _, only := range []string{OnlyMissing, OnlyCutoff} {
			ts, err := a.targets(ctx, a.roundSelection(active, only, dueTitles))
			if err != nil {
				return nil, err
			}
			for _, t := range ts {
				r, ok := rows[targetKey(t)]
				if !ok || r.nextAt > now || r.why == "" && !a.switchOn(only) {
					continue
				}
				taken[targetKey(t)] = true
				t.why = r.why
				switch r.why {
				case whyAsked:
					asked = append(asked, due{t, r.nextAt})
				case "":
					regular = append(regular, due{t, r.nextAt})
				default:
					early = append(early, due{t, r.nextAt})
				}
			}
		}
	}
	// A due row with nothing to search any more, or a regular one whose switch is off, goes.
	var drop []int64
	for k, r := range rows {
		if r.nextAt <= now && !taken[k] {
			drop = append(drop, r.id)
		}
	}
	type later struct {
		t     target
		at    int64
		tries int
		why   string
	}
	var wait []later
	var last map[string]lastRuns
	if full && (a.switchOn(OnlyMissing) || a.switchOn(OnlyCutoff)) {
		if last, err = a.lastSearched(ctx); err != nil {
			return nil, err
		}
		for _, only := range []string{OnlyMissing, OnlyCutoff} {
			if !a.switchOn(only) {
				continue
			}
			ts, err := a.targets(ctx, a.roundSelection(active, only, nil))
			if err != nil {
				return nil, err
			}
			for _, t := range ts {
				if _, ok := rows[targetKey(t)]; ok {
					continue
				}
				at, tries, why, err := a.fromLastRun(ctx, t, last[seasonKey(t.versionID, seasonOf(t.season))], now)
				if err != nil {
					return nil, err
				}
				if at > now {
					wait = append(wait, later{t, at, tries, why})
					continue
				}
				unscheduled = append(unscheduled, t)
			}
		}
	}
	if len(drop) > 0 || len(wait) > 0 {
		if err := a.st.WriteTx(ctx, func(tx *sql.Tx) error {
			for _, id := range drop {
				if _, err := tx.ExecContext(ctx, `DELETE FROM search_request WHERE id = ?`, id); err != nil {
					return err
				}
			}
			for _, w := range wait {
				if _, err := tx.ExecContext(ctx, `INSERT INTO search_request (title_id, version_key, reason, season,
					created_at, next_at, tries, why) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, w.t.titleID,
					w.t.key, w.t.reason, seasonOf(w.t.season), now, w.at, w.tries, nullString(w.why)); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	byTime := func(ds []due) []target {
		sort.SliceStable(ds, func(i, j int) bool { return ds[i].at < ds[j].at })
		out := make([]target, 0, len(ds))
		for _, d := range ds {
			out = append(out, d.t)
		}
		return out
	}
	ordered := byTime(asked)
	ordered = append(ordered, byTime(early)...)
	ordered = append(ordered, orderTargets(unscheduled, last)...)
	ordered = append(ordered, byTime(regular)...)
	groups := groupTargets(ordered)
	sum.Due = len(groups)
	left := a.set.PerHour - sum.SearchedLastHour
	for _, group := range groups {
		if left <= 0 {
			break
		}
		var ready []target
		for _, t := range group {
			busy, err := a.inFlight(ctx, t)
			if err != nil {
				return nil, err
			}
			if busy {
				sum.InFlight++
				continue
			}
			// A loop search of it already queued or running: its row stays as it is (due).
			if running[targetKey(t)] {
				continue
			}
			ready = append(ready, t)
		}
		if len(ready) == 0 {
			continue
		}
		if err := a.startRuns(ctx, SourceWanted, 0, ready); err != nil {
			return nil, err
		}
		sum.Queued++
		left--
	}
	if sum.Due > 0 || full {
		a.log.Info("search round", "full", full, "due", sum.Due, "queued", sum.Queued, "in_flight", sum.InFlight,
			"searched_last_hour", sum.SearchedLastHour, "per_hour", a.set.PerHour)
	}
	return sum, nil
}

// Run enqueues a full round every roundEvery until ctx ends; the first after delay. The serve worker runs it while
// a library is Live and Pause is off.
func (a *Acquirer) Run(ctx context.Context, delay time.Duration) {
	a.log.Info("search schedule", "per_hour", a.set.PerHour, "again_after", a.set.AgainAfter.String(),
		"missing", a.switchOn(OnlyMissing), "cutoff_unmet", a.switchOn(OnlyCutoff), "active_versions", a.pol.ActiveVersions())
	t := time.NewTimer(delay)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, _, err := a.q.Enqueue(ctx, JobWanted, struct{}{}, jobs.EnqueueOptions{DedupeKey: JobWanted, MaxAttempts: 1}); err != nil {
				a.log.Warn("enqueue search round", "err", err)
			}
			t.Reset(roundEvery)
		}
	}
}
