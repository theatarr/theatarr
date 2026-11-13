package acquire

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// WantedSummary is one round of the search schedule (Round).
type WantedSummary struct {
	// Off says why the round searched nothing at all ("" when it ran).
	Off string `json:"off,omitempty"`
	// Due are the groups due a search; Queued those this round started (the hourly cap holds back the rest);
	// SearchedLastHour the loop's searches in the hour before the round.
	Due              int `json:"due"`
	Queued           int `json:"queued"`
	InFlight         int `json:"in_flight"`
	SearchedLastHour int `json:"searched_last_hour"`
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
	// Season is the row's season (absent for a movie); SearchQueued its 1-based place among the due Search now rows
	// (0: not asked for). Position is a due row's place in the order the schedule starts due rows.
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
	// SearchNowPending counts the due Search now rows. SearchNowNextAt is always nil: they start at the next rounds.
	SearchNowPending int        `json:"search_now_pending"`
	SearchNowNextAt  *time.Time `json:"search_now_next_at"`
}

// WantedList shows the search schedule for one tab without starting work or updating version state. A shared
// title/season search is displayed once per library so library-specific waits remain visible.
//
// Waiting, until the Wanted page reads the schedule's own states: loop_off for an item with no row (or only a regular
// one) while its backlog switch is off; downloads_off while paused; in_flight with a download in flight;
// searched_recently (with next_at) for a row due later, or an unscheduled item whose latest run is recent enough; due
// for a due row, or an unscheduled item the next full round takes.
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
		Enabled: a.set.Enabled, Interval: roundEvery.String(), PerHour: a.set.PerHour, AgainAfter: a.set.AgainAfter.String(),
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
	if out.SearchNowPending, err = a.askedDue(ctx); err != nil {
		return nil, err
	}
	if err := a.wantedRuns(ctx, out.Items); err != nil {
		return nil, err
	}
	return out, nil
}

// wantedRows are the rows the Wanted list shows for q's tab, library and waiting filter, in the list's order and
// without paging; out.Counts receives the waiting counts before the waiting filter. "Search all" asks for exactly
// these.
func (a *Acquirer) wantedRows(ctx context.Context, q WantedQuery, out *WantedList) ([]WantedRow, error) {
	now := store.Millis(a.now())
	last, err := a.lastSearched(ctx)
	if err != nil {
		return nil, err
	}
	flight, err := a.wantedInFlight(ctx)
	if err != nil {
		return nil, err
	}
	versions := a.pol.ActiveVersions()
	if q.Version != "" {
		if !slices.Contains(versions, q.Version) {
			return nil, nil
		}
		versions = []string{q.Version}
	}
	sched, err := a.scheduleRows(ctx, versions, nil)
	if err != nil {
		return nil, err
	}
	ts, err := a.targets(ctx, Selection{Versions: versions, Only: q.Only, QualityCutoff: a.set.CutoffUnmet == policy.CutoffQuality})
	if err != nil {
		return nil, err
	}
	ts = orderTargets(ts, last)
	on := a.switchOn(q.Only)
	type entry struct {
		row   WantedRow
		class int // the order Round starts due rows in: asked, other why, unscheduled, regular
		at    int64
		group int // the shared search's index: its rows share a position
	}
	var all []entry
	for g, shared := range groupTargets(ts) {
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
			e := entry{row: WantedRow{TitleID: group[0].titleID, Version: key, Reason: q.Only, Targets: []string{},
				Season: group[0].season}, group: g}
			row := &e.row
			allBusy := true
			for _, t := range group {
				row.Targets = append(row.Targets, t.name)
				allBusy = allBusy && flight.busy(t)
			}
			r, has := sched[targetKey(group[0])]
			scheduled := has && (r.why != "" || on)
			switch {
			case !scheduled && !on:
				row.Waiting = "loop_off"
			case !out.DownloadsEnabled:
				row.Waiting = "downloads_off"
			case allBusy:
				row.Waiting = "in_flight"
			case scheduled && r.nextAt > now:
				row.Waiting = "searched_recently"
				at := time.UnixMilli(r.nextAt).UTC()
				row.NextAt = &at
			case scheduled:
				row.Waiting, e.at, e.class = "due", r.nextAt, 3
				switch r.why {
				case whyAsked:
					e.class = 0
				case "":
				default:
					e.class = 1
				}
			default:
				at, _, _, err := a.fromLastRun(ctx, group[0], last[seasonKey(group[0].versionID, seasonOf(group[0].season))], now)
				if err != nil {
					return nil, err
				}
				if at > now {
					row.Waiting = "searched_recently"
					next := time.UnixMilli(at).UTC()
					row.NextAt = &next
				} else {
					row.Waiting, e.class = "due", 2
				}
			}
			// What stops a search asked for by hand, whatever the schedule says.
			switch {
			case !out.DownloadsEnabled:
				row.SearchBlocked = "downloads_off"
			case allBusy:
				row.SearchBlocked = "in_flight"
			}
			all = append(all, e)
		}
	}
	// Positions of the due rows, in the order a round starts them.
	var due []int
	for i, e := range all {
		if e.row.Waiting == "due" {
			due = append(due, i)
		}
	}
	sort.SliceStable(due, func(i, j int) bool {
		x, y := all[due[i]], all[due[j]]
		if x.class != y.class {
			return x.class < y.class
		}
		return x.at < y.at
	})
	place := map[int]int{}
	for _, i := range due {
		if place[all[i].group] == 0 {
			place[all[i].group] = len(place) + 1
		}
		all[i].row.Position = place[all[i].group]
		if all[i].class == 0 {
			all[i].row.SearchQueued = all[i].row.Position
		}
	}
	var list []WantedRow
	for _, e := range all {
		out.Counts[e.row.Waiting]++
		if q.Waiting != "" && e.row.Waiting != q.Waiting {
			continue
		}
		if e.row.SearchBlocked == "" {
			out.Searchable++
		}
		list = append(list, e.row)
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
