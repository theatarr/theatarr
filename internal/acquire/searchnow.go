package acquire

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Search now (the Wanted page's buttons). A request is one row of the page: one library's missing or cutoff-unmet
// targets of a movie, or of one series season. Requests are kept in search_request and started oldest first under
// the wanted loop's hourly budget (router.wanted.per_hour): their runs have source wanted, so they are counted with
// the loop's searches and the re-searches after a failure. There is no path around the
// budget, for one row or for many; what the hour does not allow waits, and JobSearchNow drains it as searches leave
// the hour window, with the loop on or off.
//
// Order under the one budget: Search now, then the loop's own selection. A request does not consult again_after: the user asked. It is deleted when its search starts, and when
// it asks for nothing any more (imported meanwhile, a download in flight, a search of it already queued or running,
// unmonitored, the library not active). A search of router.wanted.retry_new's chain is not such a
// search: a request is never dropped for one. A search that fails is recorded on its run and not repeated
// from the request. While router.enabled is false (Pause) nothing new is accepted and what is queued waits: a drain
// starts nothing and no drain is scheduled; once it is true again the drains pick the queue up, oldest first.
//
// The order above holds inside a wanted round, which runs the drain before its own selection, and in JobSearchNow
// (StartQueued). A re-search after a failure may take a slot of the hour that frees while
// requests wait: every consumer checks and spends the hour under one lock (drainMu), but nothing arbitrates between
// them. Cancel (ClearSearchNow) stops every request whose search has not started; a drain claims each request by
// deleting it just before it starts that search, and a search that has started stays started.

// JobSearchNow drains the Search now requests and schedules itself again while some remain (StartQueued).
const JobSearchNow = "acquire.searchnow"

// ErrDownloadsOff is RequestSearchNow's refusal while Pause is on (router.enabled is false).
var ErrDownloadsOff = errors.New("nothing can be searched while Theatarr is paused")

// SearchNowItem names one row of the Wanted list. Season is nil for a movie; for a series without one, every season
// with something wanted is asked for.
type SearchNowItem struct {
	TitleID int64  `json:"title_id"`
	Version string `json:"version"`
	Season  *int   `json:"season,omitempty"`
}

// SearchNowSkip is an item that was not queued, and why.
type SearchNowSkip struct {
	TitleID int64  `json:"title_id"`
	Version string `json:"version"`
	Reason  string `json:"reason"`
}

// SearchNowResult is what a request did: Queued are the searches it added to the queue (one already queued is not
// added again), Started those the hourly budget let start at once (of the whole queue, oldest first), Pending those
// still queued, and NextAt when the next of them can start.
type SearchNowResult struct {
	Queued  int             `json:"queued"`
	Started int             `json:"started"`
	Pending int             `json:"pending"`
	Skipped []SearchNowSkip `json:"skipped"`
	NextAt  *time.Time      `json:"next_at"`
}

// Reasons an item is skipped.
const (
	skipNotActive = "the library is not active"
	skipNothing   = "nothing of it is wanted now"
	skipInFlight  = "a download for it is in progress"
)

type searchRequest struct {
	id      int64
	titleID int64
	key     string
	reason  string
	season  int // -1: a movie
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

// RequestSearchNow queues a search of the named rows of the Wanted list (only: missing or cutoff) and starts what
// the hourly budget allows.
func (a *Acquirer) RequestSearchNow(ctx context.Context, only string, items []SearchNowItem) (*SearchNowResult, error) {
	ctx = a.captureRuntime(ctx)
	if only != OnlyMissing && only != OnlyCutoff {
		return nil, fmt.Errorf("only must be missing or cutoff")
	}
	if !a.pol.RouterSettings().Enabled {
		return nil, ErrDownloadsOff
	}
	out := &SearchNowResult{Skipped: []SearchNowSkip{}}
	var reqs []searchRequest
	for _, it := range items {
		skip := ""
		vp := a.pol.Versions[it.Version]
		switch {
		case vp == nil || vp.Mode != policy.ModeActive:
			skip = skipNotActive
		default:
			ts, err := a.targets(ctx, a.searchNowSelection(it.TitleID, it.Version, only, seasonOf(it.Season)))
			if err != nil {
				return nil, err
			}
			if len(ts) == 0 {
				skip = skipNothing
			}
			for _, group := range groupTargets(ts) {
				reqs = append(reqs, searchRequest{titleID: it.TitleID, key: it.Version, reason: only,
					season: seasonOf(group[0].season)})
			}
		}
		if skip != "" {
			out.Skipped = append(out.Skipped, SearchNowSkip{TitleID: it.TitleID, Version: it.Version, Reason: skip})
		}
	}
	return a.queueSearchNow(ctx, reqs, out)
}

// RequestSearchNowAll queues every row the Wanted list shows for q (its tab, and its library and waiting filters when
// set), without paging and without a cap: the hourly budget paces it.
func (a *Acquirer) RequestSearchNowAll(ctx context.Context, q WantedQuery) (*SearchNowResult, error) {
	ctx = a.captureRuntime(ctx)
	if q.Only != OnlyMissing && q.Only != OnlyCutoff {
		return nil, fmt.Errorf("only must be missing or cutoff")
	}
	if !a.pol.RouterSettings().Enabled {
		return nil, ErrDownloadsOff
	}
	rows, err := a.wantedRows(ctx, q, &WantedList{Counts: map[string]int{}, DownloadsEnabled: true})
	if err != nil {
		return nil, err
	}
	out := &SearchNowResult{Skipped: []SearchNowSkip{}}
	var reqs []searchRequest
	for _, row := range rows {
		if row.SearchBlocked != "" {
			why := skipInFlight
			if row.SearchBlocked == "downloads_off" {
				why = ErrDownloadsOff.Error()
			}
			out.Skipped = append(out.Skipped, SearchNowSkip{TitleID: row.TitleID, Version: row.Version, Reason: why})
			continue
		}
		reqs = append(reqs, searchRequest{titleID: row.TitleID, key: row.Version, reason: q.Only, season: seasonOf(row.Season)})
	}
	return a.queueSearchNow(ctx, reqs, out)
}

// queueSearchNow records the requests (one already queued keeps its place), drains, and schedules the next drain
// while requests remain.
func (a *Acquirer) queueSearchNow(ctx context.Context, reqs []searchRequest, out *SearchNowResult) (*SearchNowResult, error) {
	if len(reqs) > 0 {
		now := store.Millis(a.now())
		if err := a.st.WriteTx(ctx, func(tx *sql.Tx) error {
			for _, r := range reqs {
				res, err := tx.ExecContext(ctx, `INSERT INTO search_request (title_id, version_key, reason, season, created_at)
					VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, r.titleID, r.key, r.reason, r.season, now)
				if err != nil {
					return err
				}
				n, _ := res.RowsAffected()
				out.Queued += int(n)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	// The requests are stored: draining and scheduling them no longer depend on the caller (a cancelled HTTP request
	// must not leave a queue without its job).
	ctx = context.WithoutCancel(ctx)
	var err error
	if out.Started, err = a.DrainSearchNow(ctx); err != nil {
		return nil, err
	}
	if out.Pending, out.NextAt, err = a.searchNowPending(ctx); err != nil {
		return nil, err
	}
	if out.Pending > 0 {
		if err := a.ScheduleSearchNow(ctx); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (a *Acquirer) searchNowSelection(titleID int64, key, reason string, season int) Selection {
	sel := Selection{Versions: []string{key}, TitleIDs: []int64{titleID}, Only: reason,
		QualityCutoff: a.set.CutoffUnmet == policy.CutoffQuality}
	if season >= 0 {
		sel.Season = &season
	}
	return sel
}

// searchRequests are the queued requests, oldest first.
func (a *Acquirer) searchRequests(ctx context.Context) ([]searchRequest, error) {
	rows, err := a.st.Reader().QueryContext(ctx, `SELECT id, title_id, version_key, reason, season FROM search_request
		ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []searchRequest
	for rows.Next() {
		var r searchRequest
		if err := rows.Scan(&r.id, &r.titleID, &r.key, &r.reason, &r.season); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DrainSearchNow starts the queued Search now requests the hourly budget allows, oldest first, and reports how many
// searches it started.
func (a *Acquirer) DrainSearchNow(ctx context.Context) (int, error) {
	ctx = a.captureRuntime(ctx)
	a.drainMu.Lock()
	defer a.drainMu.Unlock()
	if !a.pol.RouterSettings().Enabled {
		return 0, nil // paused: the requests wait and start once downloads are on again
	}
	reqs, err := a.searchRequests(ctx)
	if err != nil || len(reqs) == 0 {
		return 0, err
	}
	used, err := a.wantedUsed(ctx, a.now())
	if err != nil {
		return 0, err
	}
	started := 0
	for _, r := range reqs {
		if used+started >= a.set.PerHour {
			break
		}
		if a.testHook != nil {
			a.testHook("request")
		}
		ready, err := a.searchNowReady(ctx, r)
		if err != nil && ctx.Err() != nil {
			return started, ctx.Err()
		}
		// Claim the request by deleting it: one cancelled since the queue was read is gone and does not start.
		res, derr := a.st.Writer().ExecContext(ctx, `DELETE FROM search_request WHERE id = ?`, r.id)
		if derr != nil {
			return started, derr
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		if err == nil && len(ready) > 0 {
			if err = a.startGroup(ctx, SourceWanted, 0, ready, ""); err == nil {
				started++
			}
		}
		if err != nil {
			// Not kept: the row shows no search and the user can ask again.
			a.log.Warn("search now request dropped", "title", r.titleID, "version", r.key, "reason", r.reason, "err", err)
		}
	}
	if started > 0 {
		a.log.Info("search now searches started", "started", started, "requests", len(reqs), "per_hour", a.set.PerHour)
	}
	return started, nil
}

// searchNowReady are the targets a request still asks for: none when its library is not active, when Pause is on, when
// nothing of it is wanted any more, or when a download for it is in flight or a search of it is already queued or
// running.
func (a *Acquirer) searchNowReady(ctx context.Context, r searchRequest) ([]target, error) {
	if !a.pol.Grabs(r.key) {
		return nil, nil
	}
	ts, err := a.targets(ctx, a.searchNowSelection(r.titleID, r.key, r.reason, r.season))
	if err != nil {
		return nil, err
	}
	var ready []target
	for _, t := range ts {
		busy, err := a.inFlight(ctx, t)
		if err != nil {
			return nil, err
		}
		if busy {
			continue
		}
		var active int
		if err := a.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM acquire_run WHERE version_id = ? AND target = ?
			AND state IN ('queued', 'searching') AND `+ordinaryRuns, t.versionID, t.name).Scan(&active); err != nil {
			return nil, err
		}
		if active == 0 {
			ready = append(ready, t)
		}
	}
	return ready, nil
}

// hourRuns is the acquire_run condition of the searches that draw on router.wanted.per_hour (its one parameter is
// SourceWanted). The searches of router.wanted.retry_new's chains are among them only while the switch is on: the
// hour is one budget then, and with the switch off what it searched before takes no slot from anything else.
func (a *Acquirer) hourRuns() string {
	q := `source = ? AND search_run IS NULL AND infra <> 3`
	if !a.set.RetryNew {
		q += ` AND ` + ordinaryRuns
	}
	return q
}

// wantedUsed counts the searches of the last hour that draw on router.wanted.per_hour.
func (a *Acquirer) wantedUsed(ctx context.Context, now time.Time) (int, error) {
	var used int
	err := a.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM acquire_run WHERE `+a.hourRuns()+`
		AND created_at > ?`, SourceWanted, store.Millis(now.Add(-time.Hour))).Scan(&used)
	return used, err
}

// budgetFreeAt is when the oldest counted search of the hour leaves the window; ok is false when the hour has none.
func (a *Acquirer) budgetFreeAt(ctx context.Context, now time.Time) (at time.Time, ok bool, err error) {
	var oldest sql.NullInt64
	if err := a.st.Reader().QueryRowContext(ctx, `SELECT MIN(created_at) FROM acquire_run WHERE `+a.hourRuns()+`
		AND created_at > ?`, SourceWanted, store.Millis(now.Add(-time.Hour))).Scan(&oldest); err != nil {
		return time.Time{}, false, err
	}
	if !oldest.Valid {
		return time.Time{}, false, nil
	}
	return store.FromMillis(oldest.Int64).Add(time.Hour + time.Second), true, nil
}

// searchNowPending counts the queued requests and says when the next drain is due: when the oldest counted search
// of the hour leaves the window, and never sooner than a minute from now. While router.enabled is false none is due
// (nil): the requests wait for downloads to be on again.
func (a *Acquirer) searchNowPending(ctx context.Context) (int, *time.Time, error) {
	var n int
	if err := a.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM search_request`).Scan(&n); err != nil || n == 0 {
		return 0, nil, err
	}
	if !a.pol.RouterSettings().Enabled {
		return n, nil, nil
	}
	now := a.now()
	at, ok, err := a.budgetFreeAt(ctx, now)
	if err != nil {
		return 0, nil, err
	}
	if floor := now.Add(time.Minute); !ok || at.Before(floor) {
		at = floor
	}
	at = at.UTC()
	return n, &at, nil
}

// ScheduleSearchNow queues the next drain of the queue JobSearchNow drains while Search now requests exist (after a
// request, after a drain that left some, and at start, so a queue survives a restart). The job's key carries its run
// time: an enqueue is a no-op while a job with the same key
// is queued or running, so a handler queueing a constant key from inside its own run would end the chain. While
// router.enabled is false it queues nothing: the chain ends, and the serve worker that drains at start runs again
// when the switch is turned back on.
func (a *Acquirer) ScheduleSearchNow(ctx context.Context) error {
	_, at, err := a.searchNowPending(ctx)
	if err != nil {
		return err
	}
	if at == nil {
		return nil
	}
	_, _, err = a.q.Enqueue(ctx, JobSearchNow, struct{}{}, jobs.EnqueueOptions{RunAt: *at,
		DedupeKey: fmt.Sprintf("%s/%d", JobSearchNow, store.Millis(*at)), MaxAttempts: 1})
	return err
}

// StartQueued drains the Search now queue (DrainSearchNow) and schedules the next drain for what is left. It is
// JobSearchNow's body, and the serve worker runs it at start and when router.enabled is turned back on, so what
// waited while paused starts with the wanted loop off too.
func (a *Acquirer) StartQueued(ctx context.Context) error {
	_, serr := a.DrainSearchNow(ctx)
	return errors.Join(serr, a.ScheduleSearchNow(ctx))
}

// HandleSearchNow is JobSearchNow's body (StartQueued).
func (a *Acquirer) HandleSearchNow(ctx context.Context) error {
	return a.StartQueued(ctx)
}

// ClearSearchNow drops every queued request and reports how many. A drain in progress starts none of them (it claims
// each request before its search); searches already started are not touched.
func (a *Acquirer) ClearSearchNow(ctx context.Context) (int, error) {
	res, err := a.st.Writer().ExecContext(ctx, `DELETE FROM search_request`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
