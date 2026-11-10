package web

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// The library views' per-copy reads and the copy-targeted bulk monitor (docs/design/ui.md, Library views). A copy is a
// version of a title. Its ownership is the version's control (followed: an arr still imports it, so it is
// read-only here; else Theatarr manages it), its coverage the episodes it holds against those aired, and its
// acquisition one summary of the downloads in hand for it (activity.Stage). Every read is batched over the page's
// titles; the live title and download events already invalidate the views that show them.

// CopyCoverage is a series copy's episodes, specials left out.
type CopyCoverage struct {
	// Have: episodes the copy holds; Total: episodes aired or held (what "of N" counts); Aired: episodes aired by
	// today; Upcoming: episodes not aired and not held.
	Have     int `json:"have"`
	Total    int `json:"total"`
	Aired    int `json:"aired"`
	Upcoming int `json:"upcoming"`
	// Missing: aired, monitored in the copy, not held; Monitored: episodes the copy monitors; InFlight: episodes with
	// a download in hand for the copy. For callers who see files.
	Missing   int `json:"missing,omitempty"`
	Monitored int `json:"monitored,omitempty"`
	InFlight  int `json:"in_flight,omitempty"`
}

// Acquisition is what is in hand for a copy (or one episode of it): the most advanced download's stage, or blocked
// when one is blocked, with its words and progress. For callers who see files.
type Acquisition struct {
	// Stage is one of activity.Stages: queued, starting, downloading, complete, waiting_mount, linking, blocked.
	Stage string `json:"stage"`
	// Words is the stage in plain words ("Downloading (37%)", "Waiting for the mount"); provider names left out.
	Words string `json:"words"`
	// Progress is the download's percentage while downloading.
	Progress *float64 `json:"progress,omitempty"`
	// DownloadID is the download the summary follows (the activity page's id); Downloads counts those in hand.
	DownloadID int64 `json:"download_id"`
	Downloads  int   `json:"downloads"`
	// Episodes is how many episodes the downloads in hand cover (series), Stages how many downloads are in each stage.
	Episodes int            `json:"episodes,omitempty"`
	Stages   map[string]int `json:"stages,omitempty"`
	// Upgrade: the copy (or episode) already has a file, so this download replaces it.
	Upgrade bool `json:"upgrade,omitempty"`
	// Quality is the release's quality.
	Quality   string    `json:"quality,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	// WaitingSince is when the current wait began (queued, waiting to start or to be linked), NextRetry the next
	// attempt (as the activity page has them).
	WaitingSince *time.Time `json:"waiting_since,omitempty"`
	NextRetry    *time.Time `json:"next_retry,omitempty"`
}

// NextUp is a title's next airing episode (series) or coming release (movies).
type NextUp struct {
	Date   string `json:"date"`
	Season *int64 `json:"season,omitempty"`
	Number *int64 `json:"number,omitempty"`
	Name   string `json:"name,omitempty"`
	// Type is a movie release's type (theatrical, digital, physical...).
	Type string `json:"type,omitempty"`
}

// stageRank orders stages for the summary: further along wins, blocked (the last of activity.Stages) most of all.
func stageRank(s string) int { return slices.Index(activity.Stages, s) }

// summarize folds the downloads in hand for one copy (or one episode) into an Acquisition; held says the copy or
// episode already has a file. nil without downloads.
func summarize(ds []*activity.Download, held bool, now time.Time) *Acquisition {
	var top *activity.Download
	a := &Acquisition{Stages: map[string]int{}}
	eps := map[int64]bool{}
	for _, d := range ds {
		s := d.Stage()
		if s == "" {
			continue
		}
		a.Downloads++
		a.Stages[s]++
		for _, e := range d.Episodes {
			eps[e] = true
		}
		if top == nil || stageRank(s) > stageRank(top.Stage()) ||
			(stageRank(s) == stageRank(top.Stage()) && d.Updated.After(top.Updated)) {
			top = d
		}
	}
	if top == nil {
		return nil
	}
	a.Stage, a.Words, a.DownloadID, a.Quality, a.UpdatedAt = top.Stage(), top.Words(""), top.ID, top.Quality, top.Updated
	if a.Stage == "downloading" {
		p := max(0, min(top.Progress, 100))
		a.Progress = &p
	}
	a.WaitingSince, a.NextRetry = waitTimes(top, now)
	a.Episodes, a.Upgrade = len(eps), held
	return a
}

// copyReads is what fillCopies and the episode grid read of downloads in hand: by version id, and by version id and
// episode id.
type copyReads struct {
	byVersion map[int64][]*activity.Download
	byEpisode map[int64]map[int64][]*activity.Download
}

func (w *Web) inHand(ctx context.Context, db *sql.DB, titleIDs []int64) (copyReads, error) {
	out := copyReads{byVersion: map[int64][]*activity.Download{}, byEpisode: map[int64]map[int64][]*activity.Download{}}
	if len(titleIDs) == 0 {
		return out, nil
	}
	ds, err := activity.Downloads(ctx, db, activity.Filter{States: activity.InHand, TitleIDs: titleIDs})
	if err != nil {
		return out, err
	}
	for i := range ds {
		d := &ds[i]
		out.byVersion[d.VersionID] = append(out.byVersion[d.VersionID], d)
		for _, e := range d.Episodes {
			if out.byEpisode[d.VersionID] == nil {
				out.byEpisode[d.VersionID] = map[int64][]*activity.Download{}
			}
			out.byEpisode[d.VersionID][e] = append(out.byEpisode[d.VersionID][e], d)
		}
	}
	return out, nil
}

// heldEpisodes are the (version, episode) pairs whose episode a current file or an active link of the version holds,
// for the versions given.
func heldEpisodes(ctx context.Context, db *sql.DB, versionIDs []any) (map[[2]int64]bool, error) {
	held := map[[2]int64]bool{}
	if len(versionIDs) == 0 {
		return held, nil
	}
	in := store.Placeholders(len(versionIDs))
	add := func(r *sql.Rows) error {
		var vid, eid int64
		if err := r.Scan(&vid, &eid); err != nil {
			return err
		}
		held[[2]int64{vid, eid}] = true
		return nil
	}
	if err := scanAll(ctx, db, `SELECT vf.version_id, fe.episode_id FROM version_file vf
		JOIN version_file_episode fe ON fe.version_file_id = vf.id LEFT JOIN version_source vs ON vs.version_id = vf.version_id
		WHERE vf.version_id IN (`+in+`) AND `+curFile, versionIDs, add); err != nil {
		return nil, err
	}
	err := scanAll(ctx, db, `SELECT lv.version_id, le.episode_id FROM link l JOIN link_version lv ON lv.link_id = l.id
		JOIN link_episode le ON le.link_id = l.id WHERE lv.version_id IN (`+in+`) AND l.state = 'active'`, versionIDs, add)
	return held, err
}

// fillCopies adds the library views' per-copy reads to list rows: each version's control (ownership), a series
// copy's coverage and the acquisition in hand, and each title's network and next episode or release. Every query
// covers the whole page.
func (w *Web) fillCopies(ctx context.Context, db *sql.DB, items []TitleRow) error {
	now := w.now()
	clock := newAirClock(now)
	var titleIDs []int64
	var tids, vids, seriesIDs []any
	byID := map[int64]*VersionBrief{}
	titleOf := map[int64]*TitleRow{}
	for i := range items {
		it := &items[i]
		titleIDs = append(titleIDs, it.ID)
		tids = append(tids, it.ID)
		titleOf[it.ID] = it
		if it.Kind == "series" {
			seriesIDs = append(seriesIDs, it.ID)
		}
		for j := range it.Versions {
			v := &it.Versions[j]
			byID[v.ID] = v
			vids = append(vids, v.ID)
		}
	}
	if len(items) == 0 {
		return nil
	}
	// Ownership.
	source := map[int64]string{}
	if len(vids) > 0 {
		if err := scanAll(ctx, db, `SELECT version_id, instance FROM version_source WHERE version_id IN (`+
			store.Placeholders(len(vids))+`)`, vids, func(r *sql.Rows) error {
			var id int64
			var inst string
			if err := r.Scan(&id, &inst); err != nil {
				return err
			}
			source[id] = inst
			return nil
		}); err != nil {
			return err
		}
	}
	for id, v := range byID {
		v.Control = w.control(v.Key, source[id])
	}
	hand, err := w.inHand(ctx, db, titleIDs)
	if err != nil {
		return err
	}
	// Series coverage: episodes (specials left out), per-copy monitoring, and the held pairs.
	var seriesVIDs []any
	for _, tid := range seriesIDs {
		for _, v := range titleOf[tid.(int64)].Versions {
			seriesVIDs = append(seriesVIDs, v.ID)
		}
	}
	type ep struct {
		id    int64
		aired bool
	}
	eps := map[int64][]ep{}
	mon := map[[2]int64]bool{}
	var held map[[2]int64]bool
	if len(seriesVIDs) > 0 {
		if err := scanAll(ctx, db, `SELECT title_id, id, air_date_utc_at, COALESCE(air_date, '') FROM episode WHERE season > 0 AND title_id IN (`+
			store.Placeholders(len(seriesIDs))+`)`, seriesIDs, func(r *sql.Rows) error {
			var tid, id int64
			var at sql.NullInt64
			var day string
			if err := r.Scan(&tid, &id, &at, &day); err != nil {
				return err
			}
			eps[tid] = append(eps[tid], ep{id, clock.aired(at, day)})
			return nil
		}); err != nil {
			return err
		}
		if err := scanAll(ctx, db, `SELECT version_id, episode_id FROM episode_version_state WHERE monitored = 1 AND
			version_id IN (`+store.Placeholders(len(seriesVIDs))+`)`, seriesVIDs, func(r *sql.Rows) error {
			var vid, eid int64
			if err := r.Scan(&vid, &eid); err != nil {
				return err
			}
			mon[[2]int64{vid, eid}] = true
			return nil
		}); err != nil {
			return err
		}
		if held, err = heldEpisodes(ctx, db, seriesVIDs); err != nil {
			return err
		}
	}
	for i := range items {
		it := &items[i]
		for j := range it.Versions {
			v := &it.Versions[j]
			if it.Kind == "series" {
				c := &CopyCoverage{}
				for _, e := range eps[it.ID] {
					k := [2]int64{v.ID, e.id}
					h := held[k]
					if e.aired {
						c.Aired++
					}
					switch {
					case h:
						c.Have++
						c.Total++
					case e.aired:
						c.Total++
						if mon[k] {
							c.Missing++
						}
					default:
						c.Upcoming++
					}
					if mon[k] {
						c.Monitored++
					}
					if len(hand.byEpisode[v.ID][e.id]) > 0 {
						c.InFlight++
					}
				}
				v.Coverage = c
			}
			// An upgrade replaces what the copy holds: a movie's file, or every episode the downloads cover.
			ds := hand.byVersion[v.ID]
			upgrade := v.HasFile
			if it.Kind == "series" {
				for _, d := range ds {
					for _, e := range d.Episodes {
						upgrade = upgrade && held[[2]int64{v.ID, e}]
					}
				}
			}
			v.Acquisition = summarize(ds, upgrade, now)
			if a := v.Acquisition; a != nil && a.Stage != "blocked" && !a.Upgrade {
				it.Acquiring = true
			}
		}
	}
	return w.fillNext(ctx, db, items, tids, seriesIDs, clock)
}

// fillNext sets each row's next episode (series: the first still to come by the clock's rule) or coming release
// (movies: the first release date today or later, else TMDb's release date when it is still to come), and each
// movie's home release.
func (w *Web) fillNext(ctx context.Context, db *sql.DB, items []TitleRow, tids, seriesIDs []any, clock airClock) error {
	today := clock.today
	idx := map[int64]*TitleRow{}
	var movieIDs []any
	for i := range items {
		idx[items[i].ID] = &items[i]
		if items[i].Kind != "series" {
			movieIDs = append(movieIDs, items[i].ID)
		}
	}
	if len(seriesIDs) > 0 {
		coming, comingArgs := clock.comingSQL("")
		if err := scanAll(ctx, db, `SELECT title_id, season, number, name, air_date FROM episode
			WHERE `+coming+` AND air_date IS NOT NULL AND title_id IN (`+store.Placeholders(len(seriesIDs))+`)
			ORDER BY title_id, air_date, season = 0, season, number`, append(comingArgs, seriesIDs...),
			func(r *sql.Rows) error {
				var tid, s, n int64
				var name, air string
				if err := r.Scan(&tid, &s, &n, &name, &air); err != nil {
					return err
				}
				if it := idx[tid]; it != nil && it.Next == nil {
					it.Next = &NextUp{Date: air, Season: &s, Number: &n, Name: name}
				}
				return nil
			}); err != nil {
			return err
		}
	}
	if len(movieIDs) > 0 {
		if err := scanAll(ctx, db, `SELECT title_id, date, type FROM title_release_date
			WHERE date >= ? AND title_id IN (`+store.Placeholders(len(movieIDs))+`) ORDER BY title_id, date`,
			append([]any{today}, movieIDs...), func(r *sql.Rows) error {
				var tid int64
				var date, typ string
				if err := r.Scan(&tid, &date, &typ); err != nil {
					return err
				}
				if it := idx[tid]; it != nil && it.Next == nil {
					it.Next = &NextUp{Date: date[:min(len(date), 10)], Type: typ}
				}
				return nil
			}); err != nil {
			return err
		}
		dates, err := availability.DatesFor(ctx, db, movieIDs)
		if err != nil {
			return err
		}
		for _, id := range movieIDs {
			it := idx[id.(int64)]
			if it.Next == nil && it.ReleaseDate >= today {
				it.Next = &NextUp{Date: it.ReleaseDate}
			}
			if d := availability.HomeRelease(dates[it.ID]); !d.IsZero() {
				it.HomeRelease = d.Format(time.DateOnly)
			}
		}
	}
	return nil
}

// fillEpisodeAcquisition adds the acquisition in hand to the episode grid: each cell's, and per season and copy the
// episodes in flight by stage.
func (w *Web) fillEpisodeAcquisition(ctx context.Context, db *sql.DB, out *EpisodeList) error {
	hand, err := w.inHand(ctx, db, []int64{out.TitleID})
	if err != nil {
		return err
	}
	if len(hand.byVersion) == 0 {
		return nil
	}
	now := w.now()
	idOf := map[string]int64{}
	for _, v := range out.Versions {
		idOf[v.Key] = v.ID
	}
	for i := range out.Episodes {
		e := &out.Episodes[i]
		for k, c := range e.Versions {
			c.Acquisition = summarize(hand.byEpisode[idOf[k]][e.ID], c.HasFile, now)
			e.Versions[k] = c
		}
		// An episode the copy has no row for yet can still be on its way (a season pack).
		for k, id := range idOf {
			if _, ok := e.Versions[k]; ok {
				continue
			}
			if a := summarize(hand.byEpisode[id][e.ID], false, now); a != nil {
				e.Versions[k] = EpisodeCell{State: "wanted", Monitored: true, Acquisition: a}
			}
		}
	}
	// Season counts cover every season, also when out.Episodes is one season's.
	seasonOf := map[int64]int64{}
	if err := scanAll(ctx, db, `SELECT id, season FROM episode WHERE title_id = ?`, []any{out.TitleID},
		func(r *sql.Rows) error {
			var id, s int64
			if err := r.Scan(&id, &s); err != nil {
				return err
			}
			seasonOf[id] = s
			return nil
		}); err != nil {
		return err
	}
	for i := range out.Seasons {
		s := &out.Seasons[i]
		for k, sc := range s.Versions {
			for eid, ds := range hand.byEpisode[idOf[k]] {
				if seasonOf[eid] != s.Season {
					continue
				}
				if a := summarize(ds, false, now); a != nil {
					sc.InFlight++
					if sc.Stages == nil {
						sc.Stages = map[string]int{}
					}
					sc.Stages[a.Stage]++
				}
			}
			s.Versions[k] = sc
		}
	}
	return nil
}

// fillVersionAcquisition adds the acquisition in hand to the title page's versions, and sets the title's Acquiring
// as the rows do (fillCopies), before any redaction: it carries no detail of the download.
func (w *Web) fillVersionAcquisition(ctx context.Context, db *sql.DB, d *TitleDetail) error {
	hand, err := w.inHand(ctx, db, []int64{d.ID})
	if err != nil {
		return err
	}
	var held map[[2]int64]bool
	if d.Kind == "series" {
		var vids []any
		for _, v := range d.Versions {
			vids = append(vids, v.ID)
		}
		if held, err = heldEpisodes(ctx, db, vids); err != nil {
			return err
		}
	}
	now := w.now()
	for i := range d.Versions {
		v := &d.Versions[i]
		ds := hand.byVersion[v.ID]
		// A series copy's episodes say for themselves whether they are upgrades (the episode grid).
		v.Acquisition = summarize(ds, v.HasFile && d.Kind == "movie", now)
		upgrade := v.HasFile
		if d.Kind == "series" {
			for _, x := range ds {
				for _, e := range x.Episodes {
					upgrade = upgrade && held[[2]int64{v.ID, e}]
				}
			}
		}
		if a := v.Acquisition; a != nil && a.Stage != "blocked" && !upgrade {
			d.Acquiring = true
		}
	}
	return nil
}

// qualityRank orders a quality name (col, an SQL expression) for the quality sort: resolution first, then source.
func qualityRank(col string) string {
	return `(CASE WHEN ` + col + ` LIKE '%2160%' THEN 400 WHEN ` + col + ` LIKE '%1080%' THEN 300 WHEN ` + col +
		` LIKE '%720%' THEN 200 WHEN ` + col + ` LIKE '%576%' OR ` + col + ` LIKE '%480%' THEN 100 ELSE 0 END +
		CASE WHEN ` + col + ` LIKE '%remux%' THEN 50 WHEN ` + col + ` LIKE '%bluray%' THEN 40 WHEN ` + col +
		` LIKE '%webdl%' OR ` + col + ` LIKE '%web-dl%' THEN 30 WHEN ` + col + ` LIKE '%webrip%' THEN 20 WHEN ` + col +
		` LIKE '%hdtv%' THEN 10 ELSE 0 END)`
}

// episodeHeld is true when episode e is held by version v: a current file or an active link.
const episodeHeld = `(EXISTS (SELECT 1 FROM version_file_episode fe JOIN version_file vf ON vf.id = fe.version_file_id
		LEFT JOIN version_source vs ON vs.version_id = vf.version_id
		WHERE fe.episode_id = e.id AND vf.version_id = v.id AND ` + curFile + `)
	OR EXISTS (SELECT 1 FROM link_episode le JOIN link_version lv ON lv.link_id = le.link_id
		JOIN link l ON l.id = le.link_id WHERE le.episode_id = e.id AND lv.version_id = v.id AND l.state = 'active'))`

// copySortExpr is the per-title expression of the library views' sorts (TitleGridFilter), and its arguments in text
// order. key narrows completion, quality and activity to one copy; without it they take the title's best copy (any
// copy's newest change for activity). clock counts aired and coming episodes, its day coming releases.
func copySortExpr(sort, key string, clock airClock) (string, []any) {
	today := clock.today
	var args []any
	versionCond := ""
	if key != "" {
		versionCond = ` AND v.policy_key = ?`
	}
	switch sort {
	case "completion":
		// A movie copy is done or not; a series copy is the share of its aired (or held) episodes it holds.
		aired, airedArgs := clock.airedSQL("e")
		args = append(args, airedArgs...)
		if key != "" {
			args = append(args, key)
		}
		return `(SELECT MAX(CASE WHEN t.kind = 'movie' THEN (CASE WHEN ` + hasFileSQL + ` THEN 1.0 ELSE 0.0 END) ELSE
			(SELECT CAST(SUM(CASE WHEN ` + episodeHeld + ` THEN 1 ELSE 0 END) AS REAL) / NULLIF(SUM(CASE WHEN
				` + aired + ` OR ` + episodeHeld + ` THEN 1 ELSE 0 END), 0) FROM episode e WHERE e.title_id = t.id
				AND e.season > 0) END) FROM version v WHERE v.title_id = t.id` + versionCond + `)`, args
	case "quality":
		if key != "" {
			args = append(args, key, key)
		}
		return `NULLIF(MAX(
			COALESCE((SELECT MAX(` + qualityRank("vf.quality") + `) FROM version v JOIN version_file vf ON vf.version_id = v.id
				LEFT JOIN version_source vs ON vs.version_id = v.id WHERE v.title_id = t.id AND ` + curFile + versionCond + `), -1),
			COALESCE((SELECT MAX(` + qualityRank("l.quality") + `) FROM link l JOIN link_version lv ON lv.link_id = l.id
				JOIN version v ON v.id = lv.version_id WHERE l.title_id = t.id AND l.state = 'active'` + versionCond + `), -1)), -1)`, args
	case "activity":
		cond := ""
		if key != "" {
			cond, args = ` AND d.version_key = ?`, append(args, key)
		}
		return `(SELECT MAX(d.updated_at) FROM download d WHERE d.title_id = t.id AND d.state IN ('` +
			strings.Join(activity.InHand, "', '") + `')` + cond + `)`, args
	case "next":
		coming, comingArgs := clock.comingSQL("e")
		return `CASE WHEN t.kind = 'series' THEN (SELECT MIN(e.air_date) FROM episode e WHERE e.title_id = t.id
			AND ` + coming + `) ELSE COALESCE((SELECT MIN(substr(rd.date, 1, 10)) FROM title_release_date rd
			WHERE rd.title_id = t.id AND rd.date >= ?), CASE WHEN m.release_date >= ? THEN m.release_date END) END`,
			append(comingArgs, today, today)
	}
	return "", nil
}

// POST titles/monitor: copy-targeted monitoring for many titles at once (the library's select mode). It sets the
// monitored flag of existing copies only: a copy the title lacks is reported missing, never added; a followed copy
// (an arr still imports it) is refused as read-only; nothing is searched. dry_run previews; expect (the preview's
// from values) makes an apply refuse items changed since, as stale.

// BulkMonitorRequest is the body of POST titles/monitor.
type BulkMonitorRequest struct {
	TitleIDs []int64 `json:"title_ids"`
	// Versions are the copies to change; absent means every copy each title has.
	Versions  []string     `json:"versions"`
	Monitored *bool        `json:"monitored"`
	DryRun    bool         `json:"dry_run"`
	Expect    []BulkExpect `json:"expect"`
}

// BulkExpect is one item's monitored state as the preview read it.
type BulkExpect struct {
	TitleID   int64  `json:"title_id"`
	Version   string `json:"version"`
	Monitored bool   `json:"monitored"`
}

// BulkItem is one title's copy and what the request does (or would do) to it: changed, unchanged, read_only
// (followed), missing (no such copy or title) or stale (changed since the preview).
type BulkItem struct {
	TitleID int64  `json:"title_id"`
	Title   string `json:"title"`
	Version string `json:"version"`
	Label   string `json:"label"`
	Outcome string `json:"outcome"`
	From    *bool  `json:"from,omitempty"`
	To      bool   `json:"to"`
	Reason  string `json:"reason,omitempty"`
}

// BulkMonitorResult is the answer of POST titles/monitor.
type BulkMonitorResult struct {
	DryRun    bool       `json:"dry_run"`
	Items     []BulkItem `json:"items"`
	Changed   int        `json:"changed"`
	Unchanged int        `json:"unchanged"`
	ReadOnly  int        `json:"read_only"`
	Missing   int        `json:"missing"`
	Stale     int        `json:"stale"`
	Message   string     `json:"message"`
}

// maxBulkTitles bounds one bulk request.
const maxBulkTitles = 500

func (w *Web) mountCopiesAPI(mux *http.ServeMux) {
	w.handle(mux, "POST "+APIBase+"/titles/monitor", SignedIn, auth.PermManage, w.apiBulkMonitor)
	w.handle(mux, "POST "+APIBase+"/titles/minimum-availability", SignedIn, auth.PermManage, w.apiMinimumAvailability)
	w.handle(mux, "POST "+APIBase+"/titles/series-type", SignedIn, auth.PermManage, w.apiSeriesType)
}

// MinimumAvailabilityRequest is the body of POST /titles/minimum-availability.
type MinimumAvailabilityRequest struct {
	TitleIDs []int64 `json:"title_ids"`
	// Versions are the libraries to set it for; absent means every library each movie is in.
	Versions            []string `json:"versions"`
	MinimumAvailability string   `json:"minimum_availability"`
}

// SeriesTypeRequest is the body of POST /titles/series-type.
type SeriesTypeRequest struct {
	TitleIDs   []int64 `json:"title_ids"`
	SeriesType string  `json:"series_type"`
}

// TitleChoiceResult is the answer to both: the library choices, or the series, written.
type TitleChoiceResult struct {
	Changed int `json:"changed"`
}

// What a movie's minimum availability and a series' type may be set to (POST /titles too).
var (
	minimumAvailabilities = []string{"announced", "inCinemas", "released"}
	seriesTypes           = []string{"standard", "daily", "anime"}
)

// choiceTitles checks a choice request's title ids: 1 to maxBulkTitles titles, every one of kind. It answers the
// refusal itself and returns the ids sorted, without repeats.
func (w *Web) choiceTitles(rw http.ResponseWriter, r *http.Request, titleIDs []int64, kind, field string) ([]int64, bool) {
	ids := slices.Clone(titleIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 || len(ids) > maxBulkTitles {
		badRequest(rw, fmt.Sprintf("title_ids must name 1 to %d titles", maxBulkTitles))
		return nil, false
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	kinds := map[int64]string{}
	err := scanAll(r.Context(), w.st.Reader(), `SELECT id, kind FROM title WHERE id IN (`+store.Placeholders(len(args))+`)`,
		args, func(rows *sql.Rows) error {
			var id int64
			var k string
			if err := rows.Scan(&id, &k); err != nil {
				return err
			}
			kinds[id] = k
			return nil
		})
	if err != nil {
		w.internal(rw, err)
		return nil, false
	}
	for _, id := range ids {
		switch k, ok := kinds[id]; {
		case !ok:
			notFound(rw)
			return nil, false
		case k != kind:
			badRequest(rw, fmt.Sprintf("%s is for a %s: title %d is a %s", field, kind, id, k))
			return nil, false
		}
	}
	return ids, true
}

// apiMinimumAvailability sets movies' minimum availability as an explicit choice, per library: the title page's
// control sends one movie and one library. Only libraries the movie is in get a choice.
func (w *Web) apiMinimumAvailability(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req MinimumAvailabilityRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	if !slices.Contains(minimumAvailabilities, req.MinimumAvailability) {
		badRequest(rw, "minimum_availability must be one of "+strings.Join(minimumAvailabilities, ", "))
		return
	}
	if req.Versions != nil && len(req.Versions) == 0 {
		badRequest(rw, "pick at least one library")
		return
	}
	if w.pol == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "no policy loaded"})
		return
	}
	if err := w.checkVersions(req.Versions, policy.KindMovie); err != nil {
		badRequest(rw, err.Error())
		return
	}
	ids, ok := w.choiceTitles(rw, r, req.TitleIDs, "movie", "minimum_availability")
	if !ok {
		return
	}
	var out TitleChoiceResult
	now := w.now()
	err := w.st.WriteTx(ctx, func(tx *sql.Tx) error {
		out.Changed = 0
		for _, id := range ids {
			// The libraries the movie is in: a choice for another one would wait there for a later add.
			var keys []string
			if err := queryEach(ctx, tx, `SELECT v.policy_key FROM version v WHERE v.title_id = ? AND NOT EXISTS
				(SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id) ORDER BY v.policy_key`, []any{id},
				func(rows *sql.Rows) error {
					var k string
					if err := rows.Scan(&k); err != nil {
						return err
					}
					if req.Versions == nil || slices.Contains(req.Versions, k) {
						keys = append(keys, k)
					}
					return nil
				}); err != nil {
				return err
			}
			if err := availability.Choose(ctx, tx, id, keys, req.MinimumAvailability, now); err != nil {
				return err
			}
			out.Changed += len(keys)
		}
		return nil
	})
	if err != nil {
		w.internal(rw, err)
		return
	}
	if out.Changed > 0 {
		w.log.Info("minimum availability set", "source", "ui", "titles", len(ids), "value", req.MinimumAvailability,
			"libraries", out.Changed)
	}
	writeJSON(rw, http.StatusOK, out)
}

// apiSeriesType sets series' type (catalog.SetSeriesType: the one value of the series, for every library).
func (w *Web) apiSeriesType(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req SeriesTypeRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	if !slices.Contains(seriesTypes, req.SeriesType) {
		badRequest(rw, "series_type must be one of "+strings.Join(seriesTypes, ", "))
		return
	}
	ids, ok := w.choiceTitles(rw, r, req.TitleIDs, "series", "series_type")
	if !ok {
		return
	}
	err := w.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if err := catalog.SetSeriesType(ctx, tx, id, req.SeriesType); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		w.internal(rw, err)
		return
	}
	w.log.Info("series type set", "source", "ui", "titles", len(ids), "value", req.SeriesType)
	writeJSON(rw, http.StatusOK, TitleChoiceResult{Changed: len(ids)})
}

func (w *Web) apiBulkMonitor(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req BulkMonitorRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	if req.Monitored == nil {
		badRequest(rw, "monitored is required")
		return
	}
	ids := slices.Clone(req.TitleIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 || len(ids) > maxBulkTitles {
		badRequest(rw, fmt.Sprintf("title_ids must name 1 to %d titles", maxBulkTitles))
		return
	}
	if req.Versions != nil && len(req.Versions) == 0 {
		badRequest(rw, "pick at least one copy")
		return
	}
	if w.pol == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "no policy loaded"})
		return
	}
	for _, k := range req.Versions {
		if w.pol.Versions[k] == nil {
			badRequest(rw, fmt.Sprintf("unknown library %q", k))
			return
		}
	}
	out := BulkMonitorResult{DryRun: req.DryRun}
	plan := func(q queryer) error {
		items, err := w.bulkPlan(ctx, q, req, ids)
		out.Items = items
		return err
	}
	var err error
	if req.DryRun {
		err = plan(w.st.Reader())
	} else {
		now := w.now()
		err = w.st.WriteTx(ctx, func(tx *sql.Tx) error {
			// Planned inside the write, so what is applied is what was read.
			if err := plan(tx); err != nil {
				return err
			}
			touched := map[int64]bool{}
			for _, it := range out.Items {
				if it.Outcome != "changed" {
					continue
				}
				if _, err := tx.ExecContext(ctx, `UPDATE version SET monitored = ?, updated_at = ? WHERE title_id = ?
					AND policy_key = ? AND monitored <> ?`, it.To, store.Millis(now), it.TitleID, it.Version, it.To); err != nil {
					return err
				}
				touched[it.TitleID] = true
			}
			for id := range touched {
				if err := catalog.SyncMonitored(ctx, tx, id, now); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	for _, it := range out.Items {
		switch it.Outcome {
		case "changed":
			out.Changed++
		case "unchanged":
			out.Unchanged++
		case "read_only":
			out.ReadOnly++
		case "missing":
			out.Missing++
		case "stale":
			out.Stale++
		}
	}
	out.Message = bulkWords(out, *req.Monitored)
	if out.Changed > 0 && !out.DryRun {
		w.log.Info("bulk monitor", "source", "ui", "monitored", *req.Monitored, "changed", out.Changed,
			"read_only", out.ReadOnly)
	}
	writeJSON(rw, http.StatusOK, out)
}

// queryer is what bulkPlan reads through: the reader pool for a preview, the write transaction for an apply.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func queryEach(ctx context.Context, q queryer, query string, args []any, fn func(*sql.Rows) error) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// bulkPlan reads the titles and their copies and decides each item, in the request's title order and the copies'
// key order.
func (w *Web) bulkPlan(ctx context.Context, q queryer, req BulkMonitorRequest, ids []int64) ([]BulkItem, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	names := map[int64]string{}
	if err := queryEach(ctx, q, `SELECT id, name FROM title WHERE id IN (`+store.Placeholders(len(ids))+`)`, args,
		func(r *sql.Rows) error {
			var id int64
			var name string
			if err := r.Scan(&id, &name); err != nil {
				return err
			}
			names[id] = name
			return nil
		}); err != nil {
		return nil, err
	}
	type copyRow struct {
		key       string
		monitored bool
		source    string
	}
	copies := map[int64][]copyRow{}
	if err := queryEach(ctx, q, `SELECT v.title_id, v.policy_key, v.monitored, COALESCE(vs.instance, '') FROM version v
		LEFT JOIN version_source vs ON vs.version_id = v.id WHERE v.title_id IN (`+store.Placeholders(len(ids))+`)
		AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id) ORDER BY v.policy_key`, args,
		func(r *sql.Rows) error {
			var tid int64
			var c copyRow
			if err := r.Scan(&tid, &c.key, &c.monitored, &c.source); err != nil {
				return err
			}
			copies[tid] = append(copies[tid], c)
			return nil
		}); err != nil {
		return nil, err
	}
	var held [][2]string
	for _, cs := range copies {
		for _, c := range cs {
			held = append(held, [2]string{c.key, c.source})
		}
	}
	labels := w.bulkLabels(req.Versions, held)
	expect := map[string]bool{}
	for _, e := range req.Expect {
		expect[fmt.Sprintf("%d/%s", e.TitleID, e.Version)] = e.Monitored
	}
	to := *req.Monitored
	var items []BulkItem
	seen := map[int64]bool{}
	for _, id := range req.TitleIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		name, ok := names[id]
		if !ok {
			items = append(items, BulkItem{TitleID: id, Outcome: "missing", To: to, Reason: "No such title"})
			continue
		}
		keys := req.Versions
		if keys == nil {
			for _, c := range copies[id] {
				keys = append(keys, c.key)
			}
			if len(keys) == 0 {
				items = append(items, BulkItem{TitleID: id, Title: name, Outcome: "missing", To: to,
					Reason: "The title is in no library"})
				continue
			}
		}
		for _, k := range keys {
			it := BulkItem{TitleID: id, Title: name, Version: k, Label: labels[k], To: to}
			i := slices.IndexFunc(copies[id], func(c copyRow) bool { return c.key == k })
			if i < 0 {
				it.Outcome, it.Reason = "missing", "The title is not in "+it.Label
				items = append(items, it)
				continue
			}
			c := copies[id][i]
			from := c.monitored
			it.From = &from
			want, expected := expect[fmt.Sprintf("%d/%s", id, k)]
			switch m := w.managedBy(c.source); {
			case m != "":
				it.Outcome, it.Reason = "read_only", "Followed: "+managedReason(m)
			case expected && want != from:
				it.Outcome, it.Reason = "stale", "Changed since the preview: now "+monitoredWord(from)
			case from == to:
				it.Outcome = "unchanged"
			default:
				it.Outcome = "changed"
			}
			items = append(items, it)
		}
	}
	return items, nil
}

// bulkLabels names the copies of a bulk request apart (distinctLabels) as the library's columns do: among every
// copy of the selected titles (copies, as key and source instance pairs) and the targeted keys. A key's ownership is
// its copies' when they agree, unknown when they are mixed or the titles lack the key.
func (w *Web) bulkLabels(keys []string, copies [][2]string) map[string]string {
	owners := map[string]string{}
	var order []string
	for _, c := range copies {
		k, source := c[0], c[1]
		o := ownerOf(w.managedBy(source))
		if prev, ok := owners[k]; !ok {
			owners[k] = o
			order = append(order, k)
		} else if prev != o {
			owners[k] = ""
		}
	}
	for _, k := range keys {
		if _, ok := owners[k]; !ok {
			owners[k] = ""
			order = append(order, k)
		}
	}
	cs := make([]copyName, len(order))
	for i, k := range order {
		cs[i] = copyName{key: k, label: w.versionLabel(k), owner: owners[k]}
	}
	return distinctLabels(cs)
}

func monitoredWord(m bool) string {
	if m {
		return "monitored"
	}
	return "not monitored"
}

// bulkWords says what a bulk monitor did (or would do): "Monitored 3 copies · 1 read-only (followed) · 2 already".
func bulkWords(o BulkMonitorResult, to bool) string {
	verb := "Monitored"
	if !to {
		verb = "Unmonitored"
	}
	if o.DryRun {
		verb = "Would " + strings.ToLower(strings.TrimSuffix(verb, "ed"))
	}
	n := func(k int, one, many string) string {
		if k == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", k, many)
	}
	parts := []string{verb + " " + n(o.Changed, "library", "libraries")}
	if o.Unchanged > 0 {
		parts = append(parts, fmt.Sprintf("%d already %s", o.Unchanged, monitoredWord(to)))
	}
	if o.ReadOnly > 0 {
		parts = append(parts, fmt.Sprintf("%d read-only (followed)", o.ReadOnly))
	}
	if o.Missing > 0 {
		parts = append(parts, fmt.Sprintf("%d missing", o.Missing))
	}
	if o.Stale > 0 {
		parts = append(parts, fmt.Sprintf("%d changed since the preview", o.Stale))
	}
	return strings.Join(parts, " · ")
}
