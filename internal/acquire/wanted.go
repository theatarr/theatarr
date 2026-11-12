package acquire

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// WantedSummary is one round of the wanted loop.
type WantedSummary struct {
	// Off says why the round searched nothing at all ("" when it ran).
	Off string `json:"off,omitempty"`
	// Due are the items due a search; Queued those this round started (the hourly cap holds back the rest);
	// SearchedLastHour the loop's searches in the hour before the round.
	Due    int `json:"due"`
	Queued int `json:"queued"`
	// SearchNow are the Wanted page's queued searches the round started first (DrainSearchNow).
	SearchNow        int `json:"search_now,omitempty"`
	InFlight         int `json:"in_flight"`
	SearchedLastHour int `json:"searched_last_hour"`
}

// Settings is the loop's pacing in force.
func (a *Acquirer) Settings() (enabled bool, interval time.Duration, perHour int) {
	return a.set.Enabled, a.set.Interval, a.set.PerHour
}

// Wanted runs one round of the wanted search loop: the Live versions' missing and cutoff-unmet items (below the
// quality cutoff, or the full cutoff, per router.wanted.cutoff_unmet) not searched within again_after (a partial
// search: partialAgain), grouped by title and series season, attempted longest ago first, as many as the hourly cap leaves, skipping items with a download in flight. It does
// nothing while router.enabled is false or the loop is off. Queued Search now requests go first, under the same
// budget.
func (a *Acquirer) Wanted(ctx context.Context) (*WantedSummary, error) {
	ctx = a.captureRuntime(ctx)
	sum := &WantedSummary{}
	var err error
	if sum.SearchNow, err = a.DrainSearchNow(ctx); err != nil {
		return nil, err
	}
	active := a.pol.SearchVersions()
	switch {
	case !a.set.Enabled:
		sum.Off = "router.wanted is off"
	case !a.pol.RouterSettings().Enabled:
		sum.Off = "paused"
	case len(active) == 0:
		sum.Off = "no version is active"
	}
	if sum.Off != "" {
		return sum, nil
	}
	// Episode rows already know their air times; passing one needs no metadata fetch.
	if _, err := a.st.Writer().ExecContext(ctx, `UPDATE version SET state = 'wanted', updated_at = ?
		WHERE monitored = 1 AND state <> 'wanted' AND EXISTS (
			SELECT 1 FROM episode_version_state s JOIN episode e ON e.id = s.episode_id
			WHERE s.version_id = version.id AND s.monitored = 1 AND s.state = 'wanted'
			AND e.removed_at IS NULL AND e.air_date_utc_at <= ?)`, store.Millis(a.now()), store.Millis(a.now())); err != nil {
		return nil, err
	}
	// The round's own searches check and spend the hour under the lock the drains and Research take.
	a.drainMu.Lock()
	defer a.drainMu.Unlock()
	now := a.now()
	if sum.SearchedLastHour, err = a.wantedUsed(ctx, now); err != nil {
		return nil, err
	}
	if a.testHook != nil {
		a.testHook("round")
	}
	for _, key := range active {
		if a.pol.Versions[key].PreferSeaDex {
			ctx, err = decide.WithStoredSeaDex(ctx, a.st.Reader())
			if err != nil {
				return nil, err
			}
			break
		}
	}
	var ts []target
	for _, only := range []string{OnlyMissing, OnlyCutoff} {
		if (only == OnlyMissing && !a.set.Missing) || (only == OnlyCutoff && a.set.CutoffUnmet == policy.CutoffOff) {
			continue
		}
		got, err := a.targets(ctx, Selection{Versions: active, Only: only,
			QualityCutoff: a.set.CutoffUnmet == policy.CutoffQuality})
		if err != nil {
			return nil, err
		}
		ts = append(ts, got...)
	}
	last, err := a.lastSearched(ctx)
	if err != nil {
		return nil, err
	}
	again, partial := store.Millis(now.Add(-a.set.AgainAfter)), store.Millis(now.Add(-partialAgain))
	var due []target
	for _, t := range ts {
		if last[runKey(t.versionID, t.name)].searchedSince(again, partial) {
			continue
		}
		due = append(due, t)
	}
	if due, err = a.oldestFirst(ctx, due); err != nil {
		return nil, err
	}
	groups := groupTargets(due)
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
			ready = append(ready, t)
		}
		if len(ready) == 0 {
			continue
		}
		if err := a.startGroup(ctx, SourceWanted, 0, ready, ""); err != nil {
			return nil, err
		}
		sum.Queued++
		left--
	}
	a.log.Info("wanted round", "due", sum.Due, "queued", sum.Queued, "in_flight", sum.InFlight,
		"searched_last_hour", sum.SearchedLastHour, "per_hour", a.set.PerHour)
	return sum, nil
}

// Run enqueues a wanted round every interval until ctx ends; the first after delay. With the loop off it only logs
// why.
func (a *Acquirer) Run(ctx context.Context, delay time.Duration) {
	if !a.set.Enabled {
		a.log.Info("wanted search loop off", "active_versions", a.pol.ActiveVersions())
		return
	}
	a.log.Info("wanted search loop", "every", a.set.Interval.String(), "per_hour", a.set.PerHour,
		"again_after", a.set.AgainAfter.String(), "missing", a.set.Missing, "cutoff_unmet", string(a.set.CutoffUnmet),
		"router_enabled", a.pol.RouterSettings().Enabled, "active_versions", a.pol.ActiveVersions())
	t := time.NewTimer(delay)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			payload := wantedPayload{}
			if _, _, err := a.q.Enqueue(ctx, JobWanted, payload, jobs.EnqueueOptions{DedupeKey: JobWanted, MaxAttempts: 1}); err != nil {
				a.log.Warn("enqueue wanted round", "err", err)
			}
			t.Reset(a.set.Interval)
		}
	}
}

// WantedQuery selects a view of the automatic search backlog.
type WantedQuery struct {
	Only          string
	Version       string
	Waiting       string
	Limit, Offset int
}

type WantedRun struct {
	ID         int64     `json:"id"`
	At         time.Time `json:"at"`
	State      string    `json:"state"`
	Results    int       `json:"results"`
	Accepted   int       `json:"accepted"`
	Downloaded bool      `json:"downloaded"`
	Message    string    `json:"message"`
}

type WantedRow struct {
	TitleID  int64      `json:"title_id"`
	Version  string     `json:"version"`
	Targets  []string   `json:"targets"`
	Reason   string     `json:"reason"`
	Waiting  string     `json:"waiting"`
	NextAt   *time.Time `json:"next_at"`
	Position int        `json:"position,omitempty"`
	// Season is the row's season (absent for a movie); SearchQueued its 1-based place among the Search now requests
	// (0: not queued).
	Season       *int `json:"season,omitempty"`
	SearchQueued int  `json:"search_queued,omitempty"`
	// SearchBlocked says why Search now cannot search the row, independent of Waiting (which names the loop's
	// reason first): downloads_off (Pause is on) or in_flight; "" when it can.
	SearchBlocked string     `json:"search_blocked,omitempty"`
	LastRun       *WantedRun `json:"last_run"`
	DecisionID    *int64     `json:"decision_id"`
}

type WantedList struct {
	Items            []WantedRow       `json:"items"`
	Total            int               `json:"total"`
	Counts           map[string]int    `json:"counts"`
	Limit            int               `json:"limit"`
	Offset           int               `json:"offset"`
	Enabled          bool              `json:"enabled"`
	Interval         string            `json:"interval"`
	PerHour          int               `json:"per_hour"`
	AgainAfter       string            `json:"again_after"`
	Missing          bool              `json:"missing"`
	CutoffUnmet      policy.CutoffMode `json:"cutoff_unmet"`
	DownloadsEnabled bool              `json:"downloads_enabled"`
	SearchedLastHour int               `json:"searched_last_hour"`
	// Searchable counts the rows of the filtered list Search now can search (no SearchBlocked): what "Search all"
	// queues.
	Searchable int `json:"searchable"`
	// SearchNowPending counts the queued Search now requests; SearchNowNextAt is when the next of them can start (nil
	// while router.enabled is false: the queue waits).
	SearchNowPending int        `json:"search_now_pending"`
	SearchNowNextAt  *time.Time `json:"search_now_next_at"`
}

// WantedList uses the loop's selectors and recency rules without starting work or updating version state.
// A shared title/season search is displayed once per library so library-specific waits remain visible.
func (a *Acquirer) WantedList(ctx context.Context, q WantedQuery) (*WantedList, error) {
	ctx = a.captureRuntime(ctx)
	if q.Only != OnlyMissing && q.Only != OnlyCutoff {
		return nil, fmt.Errorf("only must be missing or cutoff")
	}
	if q.Limit <= 0 {
		q.Limit = 50
	}
	q.Limit = min(q.Limit, 200)
	q.Offset = max(q.Offset, 0)
	out := &WantedList{Items: []WantedRow{}, Counts: map[string]int{}, Limit: q.Limit, Offset: q.Offset,
		Enabled: a.set.Enabled, Interval: a.set.Interval.String(), PerHour: a.set.PerHour, AgainAfter: a.set.AgainAfter.String(),
		Missing: a.set.Missing, CutoffUnmet: a.set.CutoffUnmet, DownloadsEnabled: a.pol.RouterSettings().Enabled}
	var err error
	if out.SearchedLastHour, err = a.wantedUsed(ctx, a.now()); err != nil {
		return nil, err
	}
	rows, err := a.wantedRows(ctx, q, out)
	if err != nil {
		return nil, err
	}
	out.Total = len(rows)
	if q.Offset < len(rows) {
		out.Items = rows[q.Offset:min(q.Offset+q.Limit, len(rows))]
	}
	// The Search now queue: read once, for the page's rows only.
	reqs, err := a.searchRequests(ctx)
	if err != nil {
		return nil, err
	}
	if len(reqs) > 0 {
		place := map[string]int{}
		for i, r := range reqs {
			place[requestKey(r.titleID, r.key, r.reason, r.season)] = i + 1
		}
		for i := range out.Items {
			row := &out.Items[i]
			row.SearchQueued = place[requestKey(row.TitleID, row.Version, row.Reason, seasonOf(row.Season))]
		}
		if out.SearchNowPending, out.SearchNowNextAt, err = a.searchNowPending(ctx); err != nil {
			return nil, err
		}
	}
	if err := a.wantedRuns(ctx, out.Items); err != nil {
		return nil, err
	}
	return out, nil
}

// wantedRows are the rows the Wanted list shows for q's tab, library and waiting filter, in the list's order and
// without paging; out.Counts receives the waiting counts before the waiting filter. "Search all" queues exactly these.
func (a *Acquirer) wantedRows(ctx context.Context, q WantedQuery, out *WantedList) ([]WantedRow, error) {
	now := a.now()
	last, err := a.lastSearched(ctx)
	if err != nil {
		return nil, err
	}
	flight, err := a.wantedInFlight(ctx)
	if err != nil {
		return nil, err
	}
	again, partial := store.Millis(now.Add(-a.set.AgainAfter)), store.Millis(now.Add(-partialAgain))
	var due []target
	if a.set.Enabled && out.DownloadsEnabled {
		for _, only := range []string{OnlyMissing, OnlyCutoff} {
			if only == OnlyMissing && !a.set.Missing || only == OnlyCutoff && a.set.CutoffUnmet == policy.CutoffOff {
				continue
			}
			got, err := a.targets(ctx, Selection{Versions: a.pol.SearchVersions(), Only: only, QualityCutoff: a.set.CutoffUnmet == policy.CutoffQuality})
			if err != nil {
				return nil, err
			}
			for _, t := range got {
				if !last[runKey(t.versionID, t.name)].searchedSince(again, partial) {
					due = append(due, t)
				}
			}
		}
	}
	positions := map[string]int{}
	position := 0
	for _, group := range groupTargets(orderTargets(due, last)) {
		ready := false
		for _, t := range group {
			ready = ready || !flight.busy(t)
		}
		if !ready {
			continue
		}
		position++
		for _, t := range group {
			positions[runKey(t.versionID, t.name)] = position
		}
	}
	versions := a.pol.ActiveVersions()
	if q.Version != "" {
		if slices.Contains(versions, q.Version) {
			versions = []string{q.Version}
		} else {
			return nil, nil
		}
	}
	ts, err := a.targets(ctx, Selection{Versions: versions, Only: q.Only, QualityCutoff: a.set.CutoffUnmet == policy.CutoffQuality})
	if err != nil {
		return nil, err
	}
	ts = orderTargets(ts, last)
	var list []WantedRow
	for _, shared := range groupTargets(ts) {
		// Preserve the shared query's order, and its target order within each library.
		keys := []string{}
		byVersion := map[string][]target{}
		for _, t := range shared {
			if _, ok := byVersion[t.key]; !ok {
				keys = append(keys, t.key)
			}
			byVersion[t.key] = append(byVersion[t.key], t)
		}
		for _, key := range keys {
			group := byVersion[key]
			row := WantedRow{TitleID: group[0].titleID, Version: key, Reason: q.Only, Targets: []string{}, Season: group[0].season}
			recent, busy, allBusy := true, false, true
			var next int64
			for _, t := range group {
				row.Targets = append(row.Targets, t.name)
				allBusy = allBusy && flight.busy(t)
				if a.set.Enabled && out.DownloadsEnabled && (q.Only == OnlyMissing && a.set.Missing || q.Only == OnlyCutoff && a.set.CutoffUnmet != policy.CutoffOff) {
					busy = busy || flight.busy(t)
				}
				l := last[runKey(t.versionID, t.name)]
				recent = recent && l.searchedSince(again, partial)
				dueAt := max(l.complete+a.set.AgainAfter.Milliseconds(), l.partial+min(a.set.AgainAfter, partialAgain).Milliseconds())
				if next == 0 || dueAt < next {
					next = dueAt
				}
			}
			switch {
			case !a.set.Enabled || (q.Only == OnlyMissing && !a.set.Missing) || (q.Only == OnlyCutoff && a.set.CutoffUnmet == policy.CutoffOff):
				row.Waiting = "loop_off"
			case !out.DownloadsEnabled:
				row.Waiting = "downloads_off"
			case busy:
				row.Waiting = "in_flight"
			case recent:
				row.Waiting = "searched_recently"
				at := time.UnixMilli(next).UTC()
				row.NextAt = &at
			default:
				row.Waiting = "due"
				for _, t := range group {
					if p := positions[runKey(t.versionID, t.name)]; p > 0 {
						row.Position = p
						break
					}
				}
			}
			// What stops a search asked for by hand, whatever the loop's own state says.
			switch {
			case !out.DownloadsEnabled:
				row.SearchBlocked = "downloads_off"
			case allBusy:
				row.SearchBlocked = "in_flight"
			}
			out.Counts[row.Waiting]++
			if q.Waiting != "" && row.Waiting != q.Waiting {
				continue
			}
			if row.SearchBlocked == "" {
				out.Searchable++
			}
			list = append(list, row)
		}
	}
	return list, nil
}

// wantedFlights is a single snapshot of the router's queued-to-importing downloads.
type wantedFlights map[[2]int64]map[int64]bool

func (a *Acquirer) wantedInFlight(ctx context.Context) (wantedFlights, error) {
	rows, err := a.st.Reader().QueryContext(ctx, `SELECT title_id, version_id, episodes FROM download WHERE state IN ('queued', 'adding', 'added', 'downloading', 'ready', 'importing')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := wantedFlights{}
	for rows.Next() {
		var title, version int64
		var raw string
		if err := rows.Scan(&title, &version, &raw); err != nil {
			return nil, err
		}
		key := [2]int64{title, version}
		if out[key] == nil {
			out[key] = map[int64]bool{}
		}
		var eps []int64
		if json.Unmarshal([]byte(raw), &eps) != nil || len(eps) == 0 {
			out[key][0] = true
		}
		for _, id := range eps {
			out[key][id] = true
		}
	}
	return out, rows.Err()
}

func (f wantedFlights) busy(t target) bool {
	held := f[[2]int64{t.titleID, t.versionID}]
	if len(held) == 0 {
		return false
	}
	if len(t.episodes) == 0 || held[0] {
		return true
	}
	for _, id := range t.episodes {
		if !held[id] {
			return false
		}
	}
	return true
}

func (a *Acquirer) wantedRuns(ctx context.Context, items []WantedRow) error {
	if len(items) == 0 {
		return nil
	}
	// Each page row can cover several targets. Rank their runs together in SQL.
	values := []string{}
	args := []any{}
	for i, item := range items {
		for _, target := range item.Targets {
			values = append(values, "(?,?,?,?)")
			args = append(args, i, item.TitleID, item.Version, target)
		}
	}
	rows, err := a.st.Reader().QueryContext(ctx, `WITH page(idx,title,key,target) AS (VALUES `+strings.Join(values, ",")+`), ranked AS (
 SELECT p.idx, r.id, r.created_at, r.state, r.results, r.accepted, r.message,
 EXISTS(SELECT 1 FROM download WHERE download.source = 'acquire:' || r.id
   AND NOT (download.state = 'failed' AND `+activity.NotStartedSQL+`)) AS downloaded,
 ROW_NUMBER() OVER (PARTITION BY p.idx ORDER BY r.created_at DESC, r.id DESC) AS rank
 FROM page p JOIN acquire_run r ON r.title_id = p.title AND r.version_key = p.key AND r.target = p.target)
 SELECT idx,id,created_at,state,results,accepted,message,downloaded FROM ranked WHERE rank = 1`, args...)
	if err != nil {
		return err
	}
	ids := []any{}
	for rows.Next() {
		var i int
		var at int64
		run := &WantedRun{}
		if err := rows.Scan(&i, &run.ID, &at, &run.State, &run.Results, &run.Accepted, &run.Message, &run.Downloaded); err != nil {
			rows.Close()
			return err
		}
		run.At = time.UnixMilli(at).UTC()
		items[i].LastRun = run
		ids = append(ids, run.ID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err = a.st.Reader().QueryContext(ctx, `SELECT run_id, MAX(id) FROM journal_decision WHERE run_id IN (`+store.Placeholders(len(ids))+`) GROUP BY run_id`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var run, decision int64
		if err := rows.Scan(&run, &decision); err != nil {
			return err
		}
		for i := range items {
			if items[i].LastRun != nil && items[i].LastRun.ID == run {
				items[i].DecisionID = &decision
			}
		}
	}
	return rows.Err()
}
