package acquire

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/availability"
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

// WantedQuery selects a view of the automatic search backlog. TitleID (0: every title) narrows it to one title;
// Sort orders it (WantedSorts; "" is the schedule's order).
type WantedQuery struct {
	Only          string
	Version       string
	Waiting       string
	TitleID       int64
	Sort          string
	Limit, Offset int
}

// WantedSorts are the orders of the Wanted list besides the schedule's: by title, by air or release date (the
// newest of the row's episodes; a film's home release) and by last search, each ascending or ("-") descending.
// Rows without a date or never searched go last either way.
var WantedSorts = []string{"title", "-title", "date", "-date", "last", "-last"}

type WantedRun struct {
	ID         int64     `json:"id"`
	At         time.Time `json:"at"`
	State      string    `json:"state"`
	Results    int       `json:"results"`
	Accepted   int       `json:"accepted"`
	Downloaded bool      `json:"downloaded"`
	// DownloadID and DownloadState sum up the downloads the run started (with Downloaded): the newest one in flight,
	// else the newest failed one, else the newest imported one.
	DownloadID    *int64 `json:"download_id,omitempty"`
	DownloadState string `json:"download_state,omitempty"`
	Message       string `json:"message"`
}

// Waiting states of a Wanted row.
const (
	// WaitDue: the row may be searched now; a round starts it as the hourly limit allows.
	WaitDue = "due"
	// WaitScheduled: the row is searched from NextAt.
	WaitScheduled = "scheduled"
	// WaitInFlight: a download for it is in progress.
	WaitInFlight = "in_flight"
	// WaitNotScheduled: its backlog switch is off and nothing asked for it.
	WaitNotScheduled = "not_scheduled"
	// WaitDownloadsOff: Pause is on; nothing starts until it is off.
	WaitDownloadsOff = "downloads_off"
)

type WantedRow struct {
	TitleID int64    `json:"title_id"`
	Version string   `json:"version"`
	Targets []string `json:"targets"`
	Reason  string   `json:"reason"`
	// Waiting is one of the Wait states. NextAt is set for due (from when it may be searched, never later than now)
	// and scheduled rows. Why is why the row is due early ("asked": Search now, "new": a retry of a new release,
	// else the failure the router reported; absent on the regular schedule); Tries counts the retries of a new
	// release (or the searches no indexer answered).
	Waiting string `json:"waiting"`
	// Date is the row's air or release date, YYYY-MM-DD: the newest stored air date of its episodes, a film's home
	// release (availability.HomeRelease); absent when unknown. Episodes counts a season row's episodes.
	Date string `json:"date,omitempty"`
	// DateEstimate: a film's Date is its home release worked out as the cinema date plus 90 days (no digital or disc
	// date known), as the title pages mark it.
	DateEstimate bool       `json:"date_estimate,omitempty"`
	Episodes     int        `json:"episodes,omitempty"`
	NextAt       *time.Time `json:"next_at"`
	Why          string     `json:"why,omitempty"`
	Tries        int        `json:"tries,omitempty"`
	// Season is the row's season (absent for a movie).
	Season *int `json:"season,omitempty"`
	// versionID keys the row's latest runs (sortWanted).
	versionID int64
	// SearchBlocked says why Search now cannot search the row, independent of Waiting (which names the loop's
	// reason first): downloads_off (Pause is on) or in_flight; "" when it can.
	SearchBlocked string     `json:"search_blocked,omitempty"`
	LastRun       *WantedRun `json:"last_run"`
	DecisionID    *int64     `json:"decision_id"`
	// EpisodeList are a series row's episodes (both tabs) with their air date and, on Cutoff unmet, the quality each
	// holds; Cutoff (Cutoff unmet) is the library's cutoff quality: what Sonarr's Wanted lists episode by episode.
	EpisodeList []WantedEpisode `json:"episode_list,omitempty"`
	Cutoff      string          `json:"cutoff,omitempty"`
	// episodeIDs are a series row's episodes.
	episodeIDs []int64
}

// WantedEpisode is one episode of a series row: its air date and, on Cutoff unmet, the quality of its file.
type WantedEpisode struct {
	EpisodeID int64  `json:"episode_id"`
	Season    int    `json:"season"`
	Number    int    `json:"number"`
	Name      string `json:"name,omitempty"`
	AirDate   string `json:"air_date,omitempty"`
	Quality   string `json:"quality,omitempty"`
}

type WantedList struct {
	Items            []WantedRow       `json:"items"`
	Total            int               `json:"total"`
	Counts           map[string]int    `json:"counts"`
	Limit            int               `json:"limit"`
	Offset           int               `json:"offset"`
	PerHour          int               `json:"per_hour"`
	AgainAfter       string            `json:"again_after"`
	Missing          bool              `json:"missing"`
	CutoffUnmet      policy.CutoffMode `json:"cutoff_unmet"`
	DownloadsEnabled bool              `json:"downloads_enabled"`
	SearchedLastHour int               `json:"searched_last_hour"`
	// Searchable counts the rows of the filtered list Search now can search (no SearchBlocked): what "Search all"
	// queues.
	Searchable int `json:"searchable"`
	// SearchNowPending counts the due Search now rows.
	SearchNowPending int `json:"search_now_pending"`
}

// WantedList shows the search schedule for one tab without starting work or updating version state. A shared
// title/season search is displayed once per library so library-specific waits remain visible.
//
// Waiting, first match: downloads_off while paused; in_flight with a download in flight; not_scheduled for an item
// with no row (or only a regular one) while its backlog switch is off; scheduled (with next_at) for a row due later,
// or an unscheduled item whose latest run is recent enough; due for a due row, or an unscheduled item the next full
// round takes.
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
		PerHour: a.set.PerHour, AgainAfter: a.set.AgainAfter.String(),
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
	if err := a.wantedEpisodes(ctx, out.Items); err != nil {
		return nil, err
	}
	// A date sort has read the films' dates for every row already.
	if strings.TrimPrefix(q.Sort, "-") != "date" {
		if err := a.movieDates(ctx, out.Items); err != nil {
			return nil, err
		}
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
	versions := a.pol.VersionKeys()
	if q.Version != "" {
		if !slices.Contains(versions, q.Version) {
			return nil, nil
		}
		versions = []string{q.Version}
	}
	v, err := a.wantedView(ctx, versions, q.TitleID)
	if err != nil {
		return nil, err
	}
	sel := Selection{Versions: versions, Only: q.Only, QualityCutoff: a.qualityCutoff()}
	if q.TitleID != 0 {
		sel.TitleIDs = []int64{q.TitleID}
	}
	ts, err := a.targets(ctx, sel)
	if err != nil {
		return nil, err
	}
	var list []WantedRow
	for _, group := range wantedGroups(orderTargets(ts, v.last)) {
		row, err := a.wantedRow(ctx, v, q.Only, group)
		if err != nil {
			return nil, err
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
	if strings.TrimPrefix(q.Sort, "-") == "date" {
		if err := a.movieDates(ctx, list); err != nil {
			return nil, err
		}
	}
	return a.sortWanted(ctx, list, v.last, q.Sort)
}

// wantedEpisodes fills the series rows' EpisodeList, in season and number order: each episode with its air date and, on
// Cutoff unmet, the quality of its file (the newest active link of the row's library, as decide judges it) and the
// row's Cutoff.
func (a *Acquirer) wantedEpisodes(ctx context.Context, list []WantedRow) error {
	var eps, vids []int64
	for _, r := range list {
		if len(r.episodeIDs) > 0 {
			eps = append(eps, r.episodeIDs...)
			if r.Reason == OnlyCutoff {
				vids = append(vids, r.versionID)
			}
		}
	}
	if len(eps) == 0 {
		return nil
	}
	be, _ := json.Marshal(eps)
	db := a.st.Reader()
	info := map[int64]WantedEpisode{}
	rows, err := db.QueryContext(ctx, `SELECT id, season, number, name, COALESCE(air_date, ''), air_date_utc_at FROM episode
		WHERE id IN (SELECT value FROM json_each(?))`, string(be))
	if err != nil {
		return err
	}
	for rows.Next() {
		var e WantedEpisode
		var aired sql.NullInt64
		if err := rows.Scan(&e.EpisodeID, &e.Season, &e.Number, &e.Name, &e.AirDate, &aired); err != nil {
			rows.Close()
			return err
		}
		// The stored air date; the instant's UTC day without one (as the row's Date).
		if e.AirDate == "" && aired.Valid {
			e.AirDate = time.UnixMilli(aired.Int64).UTC().Format(time.DateOnly)
		}
		info[e.EpisodeID] = e
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	quality := map[[2]int64]string{} // by version and episode; the newest link last
	// The files' quality is read for Cutoff unmet rows only.
	if len(vids) > 0 {
		bv, _ := json.Marshal(vids)
		rows, err = db.QueryContext(ctx, `SELECT lv.version_id, le.episode_id, COALESCE(l.quality, '') FROM link l
			JOIN link_version lv ON lv.link_id = l.id JOIN link_episode le ON le.link_id = l.id
			WHERE lv.version_id IN (SELECT value FROM json_each(?)) AND le.episode_id IN (SELECT value FROM json_each(?))
			AND l.state = 'active' ORDER BY l.created_at, l.id`, string(bv), string(be))
		if err != nil {
			return err
		}
		for rows.Next() {
			var v, e int64
			var q string
			if err := rows.Scan(&v, &e, &q); err != nil {
				rows.Close()
				return err
			}
			quality[[2]int64{v, e}] = q
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	for i := range list {
		r := &list[i]
		if len(r.episodeIDs) == 0 {
			continue
		}
		if vp := a.pol.Versions[r.Version]; vp != nil && r.Reason == OnlyCutoff {
			if pr := a.pol.Profiles[vp.Profile]; pr != nil && len(pr.Qualities) > 0 {
				r.Cutoff = pr.Cutoff
			}
		}
		for _, id := range r.episodeIDs {
			e := info[id]
			if r.Reason == OnlyCutoff {
				e.Quality = quality[[2]int64{r.versionID, id}]
			}
			r.EpisodeList = append(r.EpisodeList, e)
		}
		slices.SortFunc(r.EpisodeList, func(x, y WantedEpisode) int {
			return cmp.Or(cmp.Compare(x.Season, y.Season), cmp.Compare(x.Number, y.Number))
		})
	}
	return nil
}

// movieDates sets the Date of the film rows that have none: the home release shown (availability.ShownHomeRelease).
func (a *Acquirer) movieDates(ctx context.Context, list []WantedRow) error {
	var ids []any
	for _, r := range list {
		if r.Season == nil && r.Date == "" {
			ids = append(ids, r.TitleID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	dates, err := availability.DatesFor(ctx, a.st.Reader(), ids)
	if err != nil {
		return err
	}
	for i := range list {
		ds := dates[list[i].TitleID]
		if d := availability.ShownHomeRelease(ds); list[i].Season == nil && !d.IsZero() {
			list[i].Date = d.Format(time.DateOnly)
			list[i].DateEstimate = ds["digital"].IsZero() && ds["physical"].IsZero()
		}
	}
	return nil
}

// sortWanted orders the rows by q's sort (WantedSorts), keeping the schedule's order among equals.
func (a *Acquirer) sortWanted(ctx context.Context, list []WantedRow, last map[string]lastRuns, by string) ([]WantedRow,
	error) {
	if by == "" || len(list) < 2 {
		return list, nil
	}
	desc := strings.HasPrefix(by, "-")
	key := strings.TrimPrefix(by, "-")
	names := map[int64]string{}
	if key == "title" {
		var ids []any
		for _, r := range list {
			if _, ok := names[r.TitleID]; !ok {
				names[r.TitleID] = ""
				ids = append(ids, r.TitleID)
			}
		}
		for chunk := range slices.Chunk(ids, 500) {
			rows, err := a.st.Reader().QueryContext(ctx, `SELECT id, CASE WHEN sort_name = '' THEN lower(name) ELSE sort_name END
				FROM title WHERE id IN (`+store.Placeholders(len(chunk))+`)`, chunk...)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id int64
				var name string
				if err := rows.Scan(&id, &name); err != nil {
					rows.Close()
					return nil, err
				}
				names[id] = name
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
		}
	}
	// Each row's sort value: the title's sort name or the date (YYYY-MM-DD) in v, the last search in at; an empty date
	// or a row never searched goes last either way.
	type entry struct {
		row WantedRow
		v   string
		at  int64
	}
	es := make([]entry, len(list))
	for i, r := range list {
		e := entry{row: r}
		switch key {
		case "title":
			e.v = names[r.TitleID]
		case "date":
			e.v = r.Date
		case "last":
			e.at = last[seasonKey(r.versionID, seasonOf(r.Season))].any
		}
		es[i] = e
	}
	slices.SortStableFunc(es, func(x, y entry) int {
		xNone, yNone := key == "date" && x.v == "" || key == "last" && x.at == 0, key == "date" && y.v == "" || key == "last" && y.at == 0
		switch {
		case xNone || yNone:
			return cmp.Compare(boolInt(xNone), boolInt(yNone))
		case desc:
			x, y = y, x
		}
		if key == "last" {
			return cmp.Compare(x.at, y.at)
		}
		return strings.Compare(x.v, y.v)
	})
	for i := range es {
		list[i] = es[i].row
	}
	return list, nil
}

// wantedView is what the waiting state of a Wanted row is read from: the schedule rows, the latest runs and the
// downloads in flight, of every title or of one.
type wantedView struct {
	now         int64
	downloadsOn bool
	sched       map[string]schedRow
	last        map[string]lastRuns
	flight      wantedFlights
}

func (a *Acquirer) wantedView(ctx context.Context, versions []string, titleID int64) (*wantedView, error) {
	v := &wantedView{now: store.Millis(a.now()), downloadsOn: a.pol.RouterSettings().Enabled}
	var scope []int64
	if titleID != 0 {
		scope = []int64{titleID}
	}
	var err error
	if v.sched, err = a.scheduleRows(ctx, versions, scope); err != nil {
		return nil, err
	}
	if titleID == 0 {
		v.last, err = a.lastSearched(ctx)
	} else {
		v.last, err = a.lastSearchedTitle(ctx, titleID)
	}
	if err != nil {
		return nil, err
	}
	if v.flight, err = a.wantedInFlight(ctx, titleID); err != nil {
		return nil, err
	}
	return v, nil
}

// wantedGroups splits targets into the Wanted rows: one per shared search (a film, or one season) and library, in
// the shared query's order and with its target order within each library.
func wantedGroups(ts []target) [][]target {
	var out [][]target
	for _, shared := range groupTargets(ts) {
		keys := []string{}
		byVersion := map[string][]target{}
		for _, t := range shared {
			if _, ok := byVersion[t.key]; !ok {
				keys = append(keys, t.key)
			}
			byVersion[t.key] = append(byVersion[t.key], t)
		}
		for _, key := range keys {
			out = append(out, byVersion[key])
		}
	}
	return out
}

// wantedRow is one Wanted row (one library's targets of a film or of one season) with its waiting state: the one
// place that state is decided, for the Wanted page and the title page (NextSearches).
func (a *Acquirer) wantedRow(ctx context.Context, v *wantedView, only string, group []target) (WantedRow, error) {
	now := v.now
	on := a.switchOn(only)
	row := WantedRow{TitleID: group[0].titleID, Version: group[0].key, Reason: only, Targets: []string{},
		Season: group[0].season}
	allBusy := true
	episodes := map[int64]bool{}
	for _, t := range group {
		row.Targets = append(row.Targets, t.name)
		allBusy = allBusy && v.flight.busy(t)
		for _, id := range t.episodes {
			episodes[id] = true
		}
	}
	if row.Season != nil {
		row.Episodes = len(episodes)
	}
	// A series row lists its episodes (wantedEpisodes).
	for id := range episodes {
		row.episodeIDs = append(row.episodeIDs, id)
	}
	row.versionID = group[0].versionID
	for _, t := range group {
		row.Date = max(row.Date, t.airDate)
	}
	r, has := v.sched[targetKey(group[0])]
	scheduled := has && (r.why != "" || on)
	if scheduled {
		row.Why, row.Tries = r.why, r.tries
	}
	at := int64(0)
	switch {
	case !v.downloadsOn:
		row.Waiting = WaitDownloadsOff
	case allBusy:
		row.Waiting = WaitInFlight
	case !scheduled && !on:
		row.Waiting = WaitNotScheduled
	case scheduled:
		at = r.nextAt
	default:
		var tries int
		var why string
		var err error
		at, tries, why, err = a.fromLastRun(ctx, group[0], v.last[seasonKey(group[0].versionID, seasonOf(group[0].season))], now)
		if err != nil {
			return row, err
		}
		if at > now {
			row.Why, row.Tries = why, tries
		}
	}
	switch {
	case row.Waiting != "":
	case at > now:
		row.Waiting = WaitScheduled
		next := time.UnixMilli(at).UTC()
		row.NextAt = &next
	default:
		row.Waiting = WaitDue
		next := time.UnixMilli(now).UTC()
		if at > 0 {
			next = time.UnixMilli(at).UTC()
		}
		row.NextAt = &next
	}
	// What stops a search asked for by hand, whatever the schedule says.
	switch {
	case !v.downloadsOn:
		row.SearchBlocked = WaitDownloadsOff
	case allBusy:
		row.SearchBlocked = WaitInFlight
	}
	return row, nil
}

// NextSearch is when a title's next automatic search in one library and season (-1: a film) may start (now or
// earlier: due), and why it is early.
type NextSearch struct {
	Version string
	Season  int
	At      time.Time
	Why     string
}

// NextSearches is the title page's next search: per library and season, the earliest time the title's Wanted rows of
// both tabs give (wantedRow, so it says what the Wanted page says). Rows without a time (not scheduled, a download in
// flight, paused) give none. It reads only that title's schedule rows, runs and downloads.
func (a *Acquirer) NextSearches(ctx context.Context, titleID int64) ([]NextSearch, error) {
	ctx = a.captureRuntime(ctx)
	if !a.pol.RouterSettings().Enabled {
		return nil, nil
	}
	versions := a.pol.VersionKeys()
	if len(versions) == 0 {
		return nil, nil
	}
	v, err := a.wantedView(ctx, versions, titleID)
	if err != nil {
		return nil, err
	}
	type key struct {
		version string
		season  int
	}
	best := map[key]int{}
	var out []NextSearch
	for _, only := range []string{OnlyMissing, OnlyCutoff} {
		ts, err := a.targets(ctx, Selection{Versions: versions, TitleIDs: []int64{titleID}, Only: only,
			QualityCutoff: a.qualityCutoff()})
		if err != nil {
			return nil, err
		}
		for _, group := range wantedGroups(ts) {
			row, err := a.wantedRow(ctx, v, only, group)
			if err != nil {
				return nil, err
			}
			if row.NextAt == nil {
				continue
			}
			n := NextSearch{Version: row.Version, Season: seasonOf(row.Season), At: *row.NextAt, Why: row.Why}
			k := key{n.Version, n.Season}
			if i, ok := best[k]; !ok {
				best[k] = len(out)
				out = append(out, n)
			} else if n.At.Before(out[i].At) {
				out[i] = n
			}
		}
	}
	return out, nil
}

// lastSearchedTitle is lastSearched for one title's versions (acquire_run_target's version_id).
func (a *Acquirer) lastSearchedTitle(ctx context.Context, titleID int64) (map[string]lastRuns, error) {
	rows, err := a.st.Reader().QueryContext(ctx, `SELECT r.version_id, COALESCE(r.season, -1),
		COALESCE(MAX(CASE WHEN r.infra = 0 THEN r.created_at END), 0), COALESCE(MAX(CASE WHEN r.infra = 2 THEN r.created_at END), 0),
		MAX(r.created_at) FROM version v JOIN acquire_run r ON r.version_id = v.id WHERE v.title_id = ?
		GROUP BY r.version_id, COALESCE(r.season, -1)`, titleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]lastRuns{}
	for rows.Next() {
		var vid int64
		var season int
		var l lastRuns
		if err := rows.Scan(&vid, &season, &l.complete, &l.partial, &l.any); err != nil {
			return nil, err
		}
		out[seasonKey(vid, season)] = l
	}
	return out, rows.Err()
}

// wantedFlights is a single snapshot of the router's queued-to-importing downloads.
type wantedFlights map[[2]int64]map[int64]bool

// wantedInFlight reads every title's downloads in flight, or one title's (titleID not 0).
func (a *Acquirer) wantedInFlight(ctx context.Context, titleID int64) (wantedFlights, error) {
	q := `SELECT title_id, version_id, episodes FROM download WHERE state IN ('queued', 'adding', 'added', 'downloading', 'ready', 'importing')`
	var args []any
	if titleID != 0 {
		q += ` AND title_id = ?`
		args = append(args, titleID)
	}
	rows, err := a.st.Reader().QueryContext(ctx, q, args...)
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
	// A row's last run is its newest run of the same library and season (-1 for a film), whatever the run's target
	// text (a season search writes s01, an episode search s01e01): the key the schedule uses.
	values := []string{}
	args := []any{}
	for i, item := range items {
		values = append(values, "(?,?,?,?)")
		args = append(args, i, item.TitleID, item.Version, seasonOf(item.Season))
	}
	rows, err := a.st.Reader().QueryContext(ctx, `WITH page(idx,title,key,season) AS (VALUES `+strings.Join(values, ",")+`), ranked AS (
 SELECT p.idx, r.id, r.created_at, r.state, r.results, r.accepted, r.message,
 (SELECT download.id FROM download WHERE download.source = 'acquire:' || r.id
   AND NOT (download.state = 'failed' AND `+activity.NotStartedSQL+`)
   ORDER BY CASE WHEN download.state = 'imported' THEN 2 WHEN download.state = 'failed' THEN 1 ELSE 0 END, download.id DESC
   LIMIT 1) AS downloaded,
 ROW_NUMBER() OVER (PARTITION BY p.idx ORDER BY r.created_at DESC, r.id DESC) AS rank
 FROM page p JOIN version v ON v.title_id = p.title AND v.policy_key = p.key
 JOIN acquire_run r ON r.version_id = v.id AND COALESCE(r.season, -1) = p.season)
 SELECT idx,id,created_at,state,results,accepted,message,downloaded,
 COALESCE((SELECT state FROM download WHERE download.id = downloaded), '') FROM ranked WHERE rank = 1`, args...)
	if err != nil {
		return err
	}
	ids := []any{}
	for rows.Next() {
		var i int
		var at int64
		var download sql.NullInt64
		run := &WantedRun{}
		var downloadState string
		if err := rows.Scan(&i, &run.ID, &at, &run.State, &run.Results, &run.Accepted, &run.Message, &download,
			&downloadState); err != nil {
			rows.Close()
			return err
		}
		if download.Valid {
			run.Downloaded, run.DownloadID, run.DownloadState = true, &download.Int64, downloadState
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

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
