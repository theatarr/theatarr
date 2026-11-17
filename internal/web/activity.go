package web

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/metadata/art"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// Activity, the calendar and the Home "Continue" row (docs/design/ui.md). The downloads come from the
// shared reader in internal/activity (the arr views' queue reads the same rows), history from the grab journal,
// blocked releases from router_blocklist, the calendar from release dates and episode air dates.
//
// Who sees what (docs/design/ui.md): users see titles, versions, the resolution, progress and the state in plain words;
// managers and admins (see files) also see release names, providers, provider attempts, cache evidence, hashes and
// errors, and every title; users see the titles of their own requests only (docs/design/ui.md). What a caller sees
// comes from its permissions alone: ?as=user narrows an admin's view to the user fields (to check the filtering) and
// never widens anyone's.

func (w *Web) mountActivityAPI(mux *http.ServeMux) {
	b := APIBase
	w.handle(mux, "GET "+b+"/activity", SignedIn, auth.PermBrowse, w.apiActivity)
	w.handle(mux, "GET "+b+"/activity/downloads/{id}/attempts", SignedIn, auth.PermSeeFiles, w.apiActivityAttempts)
	w.handle(mux, "GET "+b+"/activity/continue", SignedIn, auth.PermBrowse, w.apiContinue)
	w.handle(mux, "GET "+b+"/calendar", SignedIn, auth.PermBrowse, w.apiCalendar)
}

// viewerAdmin is whether the caller gets the admin fields: see files, unless it asks for the user view.
func (w *Web) viewerAdmin(r *http.Request) bool {
	return w.permsOf(r).SeeFiles && r.URL.Query().Get("as") != "user"
}

// activityScope is what a caller's activity covers: nil for everything (see files), else the versions and seasons
// of the caller's requests (requestScope).
func (w *Web) activityScope(r *http.Request) (requestScope, error) {
	if w.permsOf(r).SeeFiles {
		return nil, nil
	}
	return w.requestScopeOf(r.Context(), auth.FromContext(r.Context()).UserID)
}

// downloadSeasons are the seasons of each download's episodes, by download id (none for a whole-title download).
func downloadSeasons(ctx context.Context, db *sql.DB, ds []activity.Download) (map[int64][]int, error) {
	out := map[int64][]int{}
	var ids []any
	for _, d := range ds {
		for _, e := range d.Episodes {
			ids = append(ids, e)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	season := map[int64]int{}
	if err := scanAll(ctx, db, `SELECT id, season FROM episode WHERE id IN (`+store.Placeholders(len(ids))+`)`, ids,
		func(rows *sql.Rows) error {
			var id int64
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				return err
			}
			season[id] = n
			return nil
		}); err != nil {
		return nil, err
	}
	for _, d := range ds {
		for _, e := range d.Episodes {
			if n, ok := season[e]; ok && !slices.Contains(out[d.ID], n) {
				out[d.ID] = append(out[d.ID], n)
			}
		}
	}
	return out, nil
}

// scopeDownloads keeps the downloads the scope covers: for a household account, the copies (versions and seasons)
// its requests ask for, not another copy of the same title.
func scopeDownloads(ctx context.Context, db *sql.DB, scope requestScope, ds []activity.Download) ([]activity.Download,
	error) {
	if scope == nil {
		return ds, nil
	}
	seasons, err := downloadSeasons(ctx, db, ds)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(ds, func(d activity.Download) bool {
		return !scope.covers(d.TitleID, d.Version, seasons[d.ID])
	}), nil
}

// ActivityTitle is the title of an activity row, with its poster.
type ActivityTitle struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Year   int    `json:"year,omitempty"`
	Kind   string `json:"kind"` // movie, series, anime
	Poster string `json:"poster"`
}

// ActivityStep is one step of a download: queued, adding, downloading, ready, linked.
type ActivityStep struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// State: done, current, failed (stopped here) or todo.
	State string `json:"state"`
}

var stepLabels = map[string]string{"queued": "Queued", "adding": "Adding", "downloading": "Downloading",
	"ready": "Download complete", "linked": "Linked"}

// ActivityDownload is one download in plain words, with the admin fields when the caller may see them.
type ActivityDownload struct {
	ID       int64          `json:"id"`
	Title    ActivityTitle  `json:"title"`
	Version  string         `json:"version"`
	State    string         `json:"state"`
	Step     string         `json:"step"`
	Steps    []ActivityStep `json:"steps"`
	Words    string         `json:"words"`
	Progress float64        `json:"progress"`
	Quality  string         `json:"quality,omitempty"`
	Episodes []string       `json:"episodes,omitempty"`
	Created  time.Time      `json:"created_at"`
	Updated  time.Time      `json:"updated_at"`
	Import   string         `json:"import_state,omitempty"` // the library's progress state, visible to all viewers
	// NotStarted: a failed download that ended before any provider add because downloads were off; not a failure.
	NotStarted bool `json:"not_started,omitempty"`
	// WaitingSince is when a waiting download (queued, added, complete but not linked) began its wait; NextRetry is
	// the next try Theatarr has scheduled (a deferred provider add, a partial import's retry). Unset when not known.
	WaitingSince *time.Time `json:"waiting_since,omitempty"`
	NextRetry    *time.Time `json:"next_retry,omitempty"`

	// Admin only.
	// EpisodeIDs are the ids of Episodes, in the same order (interactive search for one episode).
	EpisodeIDs   []int64            `json:"episode_ids,omitempty"`
	Release      string             `json:"release,omitempty"`
	Indexer      string             `json:"indexer,omitempty"`
	Hash         string             `json:"info_hash,omitempty"`
	Size         int64              `json:"size,omitempty"`
	Score        *int               `json:"score,omitempty"`
	Provider     string             `json:"provider,omitempty"`
	ProviderName string             `json:"provider_name,omitempty"`
	Client       string             `json:"client,omitempty"` // the download client a client-routed version sent it to
	Detail       string             `json:"detail,omitempty"` // the words with the provider named
	Error        string             `json:"error,omitempty"`
	Cached       *bool              `json:"cached,omitempty"`
	Reused       bool               `json:"reused,omitempty"`
	Source       string             `json:"source,omitempty"`
	Attempts     []journal.Attempt  `json:"attempts,omitempty"`
	AttemptsMore bool               `json:"attempts_more,omitempty"`
	Cache        []journal.Evidence `json:"cache,omitempty"`
	Journal      string             `json:"journal,omitempty"` // the journal entry id (dl:<id>)
	// Formats are the routed decision's matched custom formats with their scores (download.formats).
	Formats []decide.FormatScore `json:"formats,omitempty"`
	// Reason and ReasonDetail are a failed or refused download's reason in plain sentences (failureWords): the
	// headline, and what it rests on when that is stored. Error stays the stored text (Inspect's technical details).
	Reason       string `json:"reason,omitempty"`
	ReasonDetail string `json:"reason_detail,omitempty"`
}

// ActivityEvent is one History row (journal history.go) in plain words, worded like Radarr's.
type ActivityEvent struct {
	ID   string    `json:"id"`
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	// Event is the History event: grabbed, imported, adopted, deleted (for an upgrade), removed (a file, by hand),
	// missing, failed, blocked, copy_added, copy_not_cached, title_removed (the title left a library: Remove title or
	// Remove copy) (journal.HistoryEvents).
	Event   string        `json:"event"`
	Title   ActivityTitle `json:"title"`
	Version string        `json:"version,omitempty"`
	Words   string        `json:"words"`
	Tone    string        `json:"tone"` // ok, info, warn, bad
	// Episodes are the row's episodes (S01E02; an import's, those its links hold), for every account. Files (admin)
	// is how many files a library row stands for.
	Episodes []string `json:"episodes,omitempty"`
	Files    int      `json:"files,omitempty"`

	// Admin only.
	Summary  string `json:"summary,omitempty"`
	Source   string `json:"source,omitempty"`
	Release  string `json:"release,omitempty"`
	Provider string `json:"provider,omitempty"`
	Suspect  bool   `json:"suspect,omitempty"`
	// The release the row is about (journal.HistoryFacts.Release): a grab's, an import's, a failure's or a block's
	// download; an upgrade's new release; a removed file; an adopted file; a copied torrent. Release and Quality are empty
	// and Score left out when not known (rows from before these facts were recorded).
	Quality string               `json:"quality,omitempty"`
	Score   *int                 `json:"score,omitempty"`
	Indexer string               `json:"indexer,omitempty"`
	Size    int64                `json:"size,omitempty"`
	Formats []decide.FormatScore `json:"formats,omitempty"`
	// Reason: a failure's or a block's reason, a removal's, a partial import's, a copy's message.
	Reason string `json:"reason,omitempty"`
	// Replaced are the files an upgrade (one row per download and library, "Upgraded") or an adoption replaced; the
	// row's own release is the new one.
	Replaced []HistoryFile `json:"replaced,omitempty"`
	// Entry is the row's journal entry (the Journal link); none for a copy or a release unblocked since.
	Entry string `json:"entry,omitempty"`
	// A title_removed row: who removed the title from the library (Version), and what became of its files and,
	// once the debrid clean-up ran, its torrents (journal.Removal).
	By      string           `json:"by,omitempty"`
	Removal *journal.Removal `json:"removal,omitempty"`
}

// HistoryFile is one file an upgrade replaced: its path, its release (empty when not known) and its episodes.
type HistoryFile struct {
	Path     string   `json:"path"`
	Release  string   `json:"release,omitempty"`
	Quality  string   `json:"quality,omitempty"`
	Score    *int     `json:"score,omitempty"`
	Episodes []string `json:"episodes,omitempty"`
}

// ActivityBlocked is a release a version never routes again (admin).
type ActivityBlocked struct {
	Version    string        `json:"version"`
	Hash       string        `json:"info_hash"`
	Title      ActivityTitle `json:"title"`
	Release    string        `json:"release"`
	Reason     string        `json:"reason"`
	Source     string        `json:"source"`
	Words      string        `json:"words"`
	DownloadID int64         `json:"download_id,omitempty"`
	Created    time.Time     `json:"created_at"`
	Researched *time.Time    `json:"researched_at,omitempty"`
	// Quality and Indexer are the blocked download's, when it is known.
	Quality string `json:"quality,omitempty"`
	Indexer string `json:"indexer,omitempty"`
}

// ActivityPage is GET /activity: one of now, history or blocked.
type ActivityPage struct {
	State string `json:"state"`
	Admin bool   `json:"admin"`
	// Counts (now): downloads in hand per router state, and total.
	Counts     map[string]int     `json:"counts,omitempty"`
	Now        []ActivityDownload `json:"now,omitempty"`
	History    []ActivityEvent    `json:"history,omitempty"`
	Blocked    []ActivityBlocked  `json:"blocked,omitempty"`
	Total      int                `json:"total"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

// historyKinds are the journal kinds History's rows have (the kinds parameter; events is the finer filter).
var historyKinds = []string{journal.KindGrab, journal.KindUpgrade, journal.KindImport, journal.KindLink,
	journal.KindFailure, journal.KindStall, journal.KindBlocklist, journal.KindAction}

// userHistoryEvents are the History events a caller without "see files" gets: not the provider copies.
var userHistoryEvents = slices.DeleteFunc(slices.Clone(journal.HistoryEvents), func(ev string) bool {
	return ev == journal.EvCopyAdded || ev == journal.EvCopyAlready || ev == journal.EvCopyNotCached
})

var activityKinds = map[string]bool{"": true, "movie": true, "series": true, "anime": true}

func (w *Web) apiActivity(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		state = "now"
	}
	admin := w.viewerAdmin(r)
	scope, err := w.activityScope(r)
	if err != nil {
		w.internal(rw, err)
		return
	}
	kind := q.Get("kind")
	if !activityKinds[kind] {
		badRequest(rw, "kind must be movie, series or anime")
		return
	}
	var titleID int64
	if v := q.Get("title_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			badRequest(rw, "title_id must be a positive integer")
			return
		}
		titleID = n
	}
	limit, err := intParam(r, "limit", 50)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	limit = min(max(limit, 1), 200)
	page := ActivityPage{State: state, Admin: admin}
	ctx := r.Context()
	switch state {
	case "now":
		ds, err := w.nowDownloads(ctx, admin, kind, titleID, scope)
		if err != nil {
			w.internal(rw, err)
			return
		}
		page.Now, page.Total = ds, len(ds)
		page.Counts = map[string]int{"total": len(ds)}
		for _, d := range ds {
			page.Counts[d.State]++
		}
	case "history":
		f := journal.Filter{Cursor: q.Get("cursor"), Limit: limit,
			Title: strings.TrimSpace(q.Get("title")), Version: strings.TrimSpace(q.Get("version"))}
		if titleID > 0 {
			f.Title = strconv.FormatInt(titleID, 10)
		}
		// The journal pages over the caller's titles and the kind, so every page is full until the end.
		f.TitleKind = kind
		// History is scoped by version only: journal entries carry no seasons.
		if scope != nil {
			f.Restricted, f.WithinVersions = true, map[int64][]string{}
			for id, gs := range scope {
				f.Within = append(f.Within, id)
				var vs []string
				for _, g := range gs {
					if len(g.versions) == 0 {
						vs = nil
						break
					}
					vs = append(vs, g.versions...)
				}
				if vs != nil {
					slices.Sort(vs)
					f.WithinVersions[id] = slices.Compact(vs)
				}
			}
			slices.Sort(f.Within)
		}
		if v := q.Get("since"); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				t, err = time.Parse(time.DateOnly, v)
			}
			if err != nil {
				badRequest(rw, "since must be a date (YYYY-MM-DD) or an RFC 3339 time")
				return
			}
			f.Since = t
		}
		if v := q.Get("kinds"); v != "" {
			for _, k := range strings.Split(v, ",") {
				if k = strings.TrimSpace(k); slices.Contains(historyKinds, k) {
					f.Kinds = append(f.Kinds, k)
				}
			}
			if len(f.Kinds) == 0 {
				badRequest(rw, "kinds must be among "+strings.Join(historyKinds, ", "))
				return
			}
		}
		allowed := journal.HistoryEvents
		if !admin {
			allowed = userHistoryEvents
		}
		if v := q.Get("events"); v != "" {
			for _, ev := range strings.Split(v, ",") {
				if ev = strings.TrimSpace(ev); slices.Contains(allowed, ev) {
					f.Events = append(f.Events, ev)
				}
			}
			if len(f.Events) == 0 {
				badRequest(rw, "events must be among "+strings.Join(allowed, ", "))
				return
			}
		} else if !admin {
			f.Events = allowed
		}
		// Household rows have no release or library: one row per title and change, no Grabbed (journal.Filter).
		f.Household = !admin
		jp, err := w.journal.History(ctx, f)
		if errors.Is(err, journal.ErrBadFilter) {
			badRequest(rw, strings.TrimPrefix(err.Error(), "journal: "))
			return
		}
		if err != nil {
			w.internal(rw, err)
			return
		}
		evs, err := w.historyEvents(ctx, jp.Items, admin)
		if err != nil {
			w.internal(rw, err)
			return
		}
		page.History, page.Total, page.NextCursor = evs, len(evs), jp.NextCursor
	case "blocked":
		if !admin {
			writeJSON(rw, http.StatusForbidden, apiError{Error: "blocked releases are for admins"})
			return
		}
		offset, err := intParam(r, "offset", 0)
		if err != nil {
			badRequest(rw, err.Error())
			return
		}
		bf := activity.BlockedFilter{Title: strings.TrimSpace(q.Get("title")), Version: strings.TrimSpace(q.Get("version"))}
		bs, total, err := activity.BlockedReleases(ctx, w.st.Reader(), bf, limit, offset)
		if err != nil {
			w.internal(rw, err)
			return
		}
		ids := make([]int64, 0, len(bs))
		for _, b := range bs {
			ids = append(ids, b.TitleID)
		}
		posters, err := w.posters(ctx, ids)
		if err != nil {
			w.internal(rw, err)
			return
		}
		page.Blocked = []ActivityBlocked{}
		for _, b := range bs {
			x := ActivityBlocked{Version: b.Version, Hash: b.Hash, Release: b.Release, Reason: w.blockedReason(b), Source: b.Source,
				Words: blockWords(b.Source), DownloadID: b.DownloadID, Created: b.Created, Quality: b.Quality, Indexer: b.Indexer,
				Title: activityTitle(b.TitleID, b.TitleName, b.TitleYear, b.TitleKind, b.Anime, posters)}
			if !b.Researched.IsZero() {
				t := b.Researched
				x.Researched = &t
			}
			page.Blocked = append(page.Blocked, x)
		}
		page.Total = total
	default:
		badRequest(rw, "state must be now, history or blocked")
		return
	}
	writeJSON(rw, http.StatusOK, page)
}

func titleKind(kind string, anime bool) string {
	if anime {
		return "anime"
	}
	if kind == "" {
		return "movie"
	}
	return kind
}

func activityTitle(id int64, name string, year int, kind string, anime bool, posters map[int64]string) ActivityTitle {
	return ActivityTitle{ID: id, Name: name, Year: year, Kind: titleKind(kind, anime), Poster: posters[id]}
}

// posters are the poster URLs of the titles (generated art when a title has none).
func (w *Web) posters(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	metas, err := art.LoadMeta(ctx, w.st.Reader(), ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		var mp *art.Meta
		if m, ok := metas[id]; ok {
			mp = &m
		}
		out[id] = artFor(id, mp).Poster
	}
	return out, nil
}

// providerName names a provider key by its policy kind (rd → Real-Debrid).
func (w *Web) providerName(key string) string {
	if key == "" {
		return ""
	}
	if w.pol != nil {
		if p := w.pol.Providers[key]; p != nil {
			return activity.ProviderName(p.Kind)
		}
	}
	return activity.ProviderName(key)
}

// episodeLabels reads S01E02 labels for episode ids.
func episodeLabels(ctx context.Context, db *sql.DB, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.QueryContext(ctx, `SELECT id, season, number FROM episode WHERE id IN (`+
		strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var s, n int
		if err := rows.Scan(&id, &s, &n); err != nil {
			return nil, err
		}
		out[id] = fmt.Sprintf("S%02dE%02d", s, n)
	}
	return out, rows.Err()
}

// downloadViews turns downloads into activity rows; file viewers receive bounded attempts and cache evidence.
func (w *Web) downloadViews(ctx context.Context, ds []activity.Download, admin bool) ([]ActivityDownload, error) {
	ids, eps := make([]int64, 0, len(ds)), []int64{}
	for _, d := range ds {
		ids = append(ids, d.TitleID)
		eps = append(eps, d.Episodes...)
	}
	posters, err := w.posters(ctx, ids)
	if err != nil {
		return nil, err
	}
	labels, err := episodeLabels(ctx, w.st.Reader(), eps)
	if err != nil {
		return nil, err
	}
	var evidence map[int64]*downloadEvidence
	var extras map[int64]failureFacts
	if admin {
		evidence, err = w.activityEvidence(ctx, ds)
		if err != nil {
			return nil, err
		}
		// What the reason of a failed or refused download rests on (failureWords), for those only.
		var failed []int64
		for i := range ds {
			if d := &ds[i]; (d.State == "failed" && d.NotStarted() == "") || d.ImportState == "rejected" || d.ImportState == "failed" {
				failed = append(failed, d.ID)
			}
		}
		if extras, err = failureExtras(ctx, w.st.Reader(), failed); err != nil {
			return nil, err
		}
	}
	out := make([]ActivityDownload, 0, len(ds))
	for i := range ds {
		d := &ds[i]
		step, stopped := d.Step()
		notStarted := d.NotStarted()
		x := ActivityDownload{ID: d.ID, Version: d.Version, State: d.State, Step: activity.Steps[step],
			Words: d.Words(""), Progress: d.Progress, Quality: Resolution(d.Quality), Created: d.Created, Updated: d.Updated,
			Title: activityTitle(d.TitleID, d.TitleName, d.TitleYear, d.TitleKind, d.Anime, posters), Import: d.ImportState,
			NotStarted: notStarted != ""}
		x.WaitingSince, x.NextRetry = waitTimes(d, w.now())
		if stopped && notStarted == "" && step == len(activity.Steps)-2 {
			// Downloaded, then refused or failed by the library (or not imported in time): the download completed,
			// the cross is on the last step, "Not imported".
			step++
		}
		for j, k := range activity.Steps {
			s := ActivityStep{Key: k, Label: stepLabels[k], State: "todo"}
			switch {
			case j < step:
				s.State = "done"
			case j == step && stopped && notStarted == "":
				s.State = "failed"
				if k == "linked" {
					s.Label = "Not imported"
				}
			case j == step && stopped:
				s.State = "todo" // not started: nothing failed, the step was never begun
			case j == step && d.State == "imported":
				s.State = "done"
			case j == step:
				s.State = "current"
			}
			x.Steps = append(x.Steps, s)
		}
		for _, e := range d.Episodes {
			if l := labels[e]; l != "" {
				x.Episodes = append(x.Episodes, l)
				if admin {
					x.EpisodeIDs = append(x.EpisodeIDs, e)
				}
			}
		}
		if admin {
			x.Quality = d.Quality
			x.Release, x.Indexer, x.Hash, x.Size = d.Release, d.Indexer, d.Hash, d.Size
			// A score only with scoring evidence: stored formats ('[]' for a calculated 0) or a score; a download
			// with neither (formats NULL, score 0) was never scored.
			if score := d.Score; d.Formats != "" || score != 0 {
				x.Score = &score
			}
			x.Provider, x.ProviderName, x.Client = d.Provider, w.providerName(d.Provider), d.Client
			by := x.ProviderName
			if by == "" && d.Client != "" {
				by = "client " + d.Client
			}
			x.Detail, x.Cached, x.Reused, x.Source = d.Words(by), d.Cached, d.Reused, d.Source
			x.Journal = "dl:" + strconv.FormatInt(d.ID, 10)
			if d.Formats != "" {
				_ = json.Unmarshal([]byte(d.Formats), &x.Formats)
			}
			x.Error = activityMessage(d.Error)
			if notStarted != "" {
				x.Error = activity.NotStartedMessage(notStarted)
			}
			if x.Error == "" {
				x.Error = activityMessage(d.ImportErr)
			}
			if failed := d.State == "failed" && notStarted == ""; failed || d.ImportState == "rejected" || d.ImportState == "failed" {
				f := extras[d.ID]
				f.ImportState, f.ImportErr, f.Provider = d.ImportState, d.ImportErr, d.Provider
				if failed {
					f.Err = d.Error
				}
				if e := evidence[d.ID]; e != nil {
					f.MissedOn = w.attemptMisses(e.attempts)
				}
				h, dt := failureWords(f, w.providerTag)
				x.Reason, x.ReasonDetail = activityMessage(h), activityMessage(dt)
			}
			if e := evidence[d.ID]; e != nil {
				x.Attempts, x.AttemptsMore, x.Cache = e.attempts, e.more, e.cache
			}
		}
		out = append(out, x)
	}
	return out, nil
}

// waitTimes are when a waiting download began its wait and when Theatarr next retries it, each nil when not known.
// Active steps (adding, downloading, linking) have no wait.
func waitTimes(d *activity.Download, now time.Time) (since, retry *time.Time) {
	ptr := func(t time.Time) *time.Time {
		if t.IsZero() {
			return nil
		}
		return &t
	}
	switch d.State {
	case "queued":
		since = ptr(d.Created)
		if d.RetryAt.After(now) {
			retry = ptr(d.RetryAt)
		}
	case "added":
		since = ptr(cmp.Or(d.Added, d.Updated))
	case "ready", "importing":
		if d.State == "ready" || d.ImportState == "partial" {
			if d.ImportState == "waiting_mount" {
				since = ptr(cmp.Or(d.WaitingSince, d.Ready))
			} else {
				since = ptr(d.Ready)
			}
		}
	}
	mismatch := d.ImportState == "waiting_mount" && strings.HasPrefix(d.ImportErr, router.ImportMismatchPrefix)
	if (d.ImportState == "partial" || mismatch) && !d.ImportUpdated.IsZero() {
		if t := library.PartialRetryAt(d.ImportUpdated, d.ImportAttempts); t.After(now) {
			retry = &t
		}
	}
	return since, retry
}

// nowDownloads are running downloads, newest first; for callers who see files (admin) also the failures that
// still need a look (activity.NativeQueueFailureSQL). Household accounts and "View as a user" see downloads in
// progress only.
func (w *Web) nowDownloads(ctx context.Context, admin bool, kind string, titleID int64,
	scope requestScope) ([]ActivityDownload, error) {
	f := activity.Filter{States: activity.InHand, Undismissed: true}
	if admin {
		f.States, f.QueueAt = nil, w.now()
	}
	if titleID > 0 {
		f.TitleIDs = []int64{titleID}
	}
	ds, err := activity.Downloads(ctx, w.st.Reader(), f)
	if err != nil {
		return nil, err
	}
	if kind != "" {
		ds = slices.DeleteFunc(ds, func(d activity.Download) bool { return titleKind(d.TitleKind, d.Anime) != kind })
	}
	if ds, err = scopeDownloads(ctx, w.st.Reader(), scope, ds); err != nil {
		return nil, err
	}
	slices.Reverse(ds)
	return w.downloadViews(ctx, ds, admin)
}

// historyEvents turns History's rows (journal.History) into plain-words rows.
func (w *Web) historyEvents(ctx context.Context, hs []*journal.HistoryRow, admin bool) ([]ActivityEvent, error) {
	ids := make([]int64, 0, len(hs))
	for _, h := range hs {
		ids = append(ids, h.Title.ID)
	}
	posters, err := w.posters(ctx, ids)
	if err != nil {
		return nil, err
	}
	// A Failed row's reason rests on its download's import and blocklist facts, as the queue's (failureWords).
	var extras map[int64]failureFacts
	if admin {
		var ids []int64
		for _, h := range hs {
			if id := failedDownloadID(h); id > 0 {
				ids = append(ids, id)
			}
		}
		if extras, err = failureExtras(ctx, w.st.Reader(), ids); err != nil {
			return nil, err
		}
	}
	out := []ActivityEvent{}
	for _, h := range hs {
		t := activityTitle(h.Title.ID, h.Title.Name, h.Title.Year, h.Title.Kind, h.Anime, posters)
		notStarted := historyNotStarted(h)
		words, tone := entryWords(h, notStarted, w.providerTag)
		x := ActivityEvent{ID: h.ID, At: h.At, Kind: h.Kind, Event: h.Event, Title: t, Version: h.Version, Words: words,
			Tone: tone, Episodes: h.Episodes}
		if !admin {
			// Journal ids name their source rows; users get an opaque one.
			x.ID = opaqueID(h.ID)
		}
		if admin {
			x.Files, x.Provider = h.Files, h.Provider
			x.Reason = historyReason(h, notStarted, w.providerTag)
			if r := w.historyFailure(h, extras); r != "" && notStarted == "" {
				x.Reason = r // failureWords' headline (m-reasons), as the queue says it
			}
			if notStarted != "" {
				x.Summary = activity.NotStartedMessage(notStarted)
			}
			if r := h.Release; r != nil {
				x.Release, x.Quality, x.Score, x.Indexer, x.Size, x.Formats = r.Title, r.Quality, r.Score, r.Indexer,
					r.Size, r.Formats
			}
			for _, f := range h.Replaced {
				hf := HistoryFile{Path: f.Path, Episodes: f.Episodes}
				if r := f.Release; r != nil {
					hf.Release, hf.Quality, hf.Score = r.Title, r.Quality, r.Score
				}
				x.Replaced = append(x.Replaced, hf)
			}
			x.Entry = h.Entry
			x.By, x.Removal = h.By, h.Removal
			if h.Event == journal.EvAdopted {
				// The instance, for the "From Sonarr" line's tooltip.
				x.Source = h.Source
			}
		}
		out = append(out, x)
	}
	return out, nil
}

// heldPrefix starts a held import's History reason (journal history.go).
const heldPrefix = "Import held for review: "

// failedDownloadID is the download of a Failed History row (dl:<id>:fail, dl:<id>:held), 0 for any other row.
func failedDownloadID(h *journal.HistoryRow) int64 {
	if h.Event != journal.EvFailed {
		return 0
	}
	rest, ok := strings.CutPrefix(h.ID, "dl:")
	if !ok {
		return 0
	}
	id, _ := strconv.ParseInt(strings.SplitN(rest, ":", 2)[0], 10, 64)
	return id
}

// historyFailure is a Failed row's reason (failureWords' headline) from its stored text and its download's facts
// (failureExtras); a held import's row reads as the queue's held row.
func (w *Web) historyFailure(h *journal.HistoryRow, extras map[int64]failureFacts) string {
	if h.Event != journal.EvFailed || h.Reason == "" {
		return ""
	}
	f := extras[failedDownloadID(h)]
	f.Err, f.Provider, f.MissedOn = h.Reason, h.Provider, w.attemptMisses(h.Attempts)
	if held, ok := strings.CutPrefix(h.Reason, heldPrefix); ok {
		f.Err, f.ImportState, f.ImportErr = "", "rejected", held
	}
	r, _ := failureWords(f, w.providerTag)
	return r
}

// historyNotStarted is the not-started reason of a failed download's row (as activity.Download.NotStarted: a gate
// text and no attempt activity.Tried counts), "" for any other row.
func historyNotStarted(h *journal.HistoryRow) string {
	if h.Event != journal.EvFailed || h.Kind != journal.KindFailure {
		return ""
	}
	r := activity.NotStarted(h.Reason)
	if r == "" {
		return ""
	}
	for _, a := range h.Attempts {
		if activity.Tried(a.Outcome, a.Code) {
			return ""
		}
	}
	return r
}

// historyWords says what a History row means, without release names; providers by their tag (RD, AD, TB).
func historyWords(h *journal.HistoryRow) (words, tone string) {
	return entryWords(h, historyNotStarted(h), strings.ToUpper)
}

// entryWords is a History row's words, like Radarr's: Grabbed, Imported, Adopted, Upgraded (Re-downloaded for the same
// release fetched again, HistoryRow.Redownload), Removed,
// Failed, Blocked, Added to <provider>, Not cached on <provider>. notStarted is the row's historyNotStarted reason;
// tag names a provider account.
func entryWords(h *journal.HistoryRow, notStarted string, tag func(string) string) (words, tone string) {
	switch h.Event {
	case journal.EvGrabbed:
		return "Grabbed", "info"
	case journal.EvImported:
		if h.Import == "partial" {
			return "Imported", "warn"
		}
		return "Imported", "ok"
	case journal.EvAdopted:
		return "Adopted", "ok"
	case journal.EvDeleted:
		words := "Upgraded"
		if h.Redownload() {
			words = "Re-downloaded"
		}
		if h.Import == "partial" {
			return words, "warn"
		}
		return words, "ok"
	case journal.EvRemoved:
		return "Removed", "info"
	case journal.EvMissing:
		return "File missing", "bad"
	case journal.EvFailed:
		if notStarted != "" {
			return activity.NotStartedHistory(notStarted), "info"
		}
		return "Failed", "bad"
	case journal.EvBlocked:
		return "Blocked", "warn"
	case journal.EvCopyAdded:
		return "Added to " + tag(h.Provider), "ok"
	case journal.EvCopyAlready:
		return "Already on " + tag(h.Provider), "ok"
	case journal.EvCopyNotCached:
		return "Not cached on " + tag(h.Provider), "info"
	case journal.EvTitleRemoved:
		return "Removed from library", "info"
	}
	return h.Event, "info"
}

// historyReason is a History row's reason in plain words: why a download failed or stalled, why a release was blocked,
// why a file was removed, what is left of a partial import, a copy's message when it says more than the words.
func historyReason(h *journal.HistoryRow, notStarted string, tag func(string) string) string {
	switch h.Event {
	case journal.EvFailed:
		return "" // historyFailure says why (failureWords)
	case journal.EvBlocked:
		// The reason in the Blocklist's words (blockedReason).
		if r := blockedReasonWords(h.Source, h.Reason, h.Provider, tag); r != "" {
			return blockWords(h.Source) + ": " + r
		}
		return blockWords(h.Source)
	case journal.EvImported, journal.EvDeleted:
		// An upgrade's row carries its import's state.
		if h.Import == "partial" {
			return "Partly imported; the rest is tried again"
		}
	case journal.EvRemoved:
		return removedByWords(h.Reason)
	case journal.EvAdopted:
		// Taken over from Sonarr (a series) or Radarr (a movie); the instance is the row's Source.
		if h.Source == "" {
			return ""
		}
		if h.Title.Kind == "movie" {
			return "From Radarr"
		}
		return "From Sonarr"
	case journal.EvTitleRemoved:
		return removalWords(h)
	case journal.EvCopyAdded, journal.EvCopyAlready, journal.EvCopyNotCached:
		if w, _ := entryWords(h, "", tag); !strings.EqualFold(h.Reason, w) {
			return h.Reason
		}
	}
	return ""
}

// removedByWords is a removed file's reason with who removed it in words: "copy removed by key:<name>" (an API key,
// ops.go actor) is "Removed by <name> (API key)", "copy removed by <user>" "Removed by <user>"; any other as stored.
func removedByWords(reason string) string {
	who, ok := strings.CutPrefix(reason, "copy removed by ")
	if !ok {
		return reason
	}
	if key, ok := strings.CutPrefix(who, "key:"); ok {
		return "Removed by " + key + " (API key)"
	}
	return "Removed by " + who
}

// removalWords is a title_removed row's reason: who removed it, what became of the files, and the debrid
// clean-up's outcome once it ran ("By ann. Files kept.", "By ann. Files deleted. Debrid: deleted on RD.").
func removalWords(h *journal.HistoryRow) string {
	var out []string
	if h.By != "" {
		out = append(out, "By "+h.By+".")
	}
	if rm := h.Removal; rm != nil {
		left := ""
		if rm.Left > 0 {
			left = fmt.Sprintf(" (%d left in place: %s)", rm.Left, strings.TrimSuffix(rm.Reason, "."))
		}
		switch rm.Files {
		case journal.RemovalFilesKept:
			out = append(out, "Files kept"+left+".")
		case journal.RemovalFilesDeleted:
			out = append(out, "Files deleted.")
		case journal.RemovalFilesPartial:
			out = append(out, "Files partly deleted"+left+".")
		}
		if rm.Cleanup != "" {
			out = append(out, "Debrid: "+strings.TrimSuffix(rm.Cleanup, ".")+".")
		}
	}
	return strings.Join(out, " ")
}

// blockedReason is a blocklist row's reason in plain words (failureWords: headline and detail), under its words
// (blockWords); a row taken over from an arr's blocklist has none ("arr blocklist (<instance>)" says no more than its
// words).
func (w *Web) blockedReason(b activity.Blocked) string {
	return blockedReasonWords(b.Source, b.Reason, b.Provider, w.providerTag)
}

// blockedReasonWords is blockedReason from a block's source, stored reason and provider, for the Blocklist and
// History's Blocked rows alike.
func blockedReasonWords(source, reason, provider string, tag func(string) string) string {
	if strings.HasPrefix(source, "arr:") && strings.HasPrefix(reason, "arr blocklist") {
		return ""
	}
	h, d := failureWords(failureFacts{Blocked: reason, Provider: provider}, tag)
	if d != "" {
		h += ". " + d
	}
	return h
}

// blockWords says why a release is blocked, by the blocklist's source.
func blockWords(source string) string {
	switch source {
	case "library":
		return "Failed the library check"
	case "stalled":
		return "Stalled at the provider"
	case "torrent_files":
		return "Holds dangerous or executable files"
	case "queue":
		return "Removed from the queue with blocklist"
	case BlocklistUI:
		return "Removed in Activity with blocklist"
	case "journal":
		return "Judged wrong in the journal"
	case "provider":
		// A refusal, a torrent that disappeared or one with no video: the stored reason says which.
		return "Failed at the provider"
	case "ready_timeout":
		return "Timed out before import"
	}
	// Taken over from a Sonarr or Radarr instance's blocklist: arr:<instance key>.
	if inst, ok := strings.CutPrefix(source, "arr:"); ok {
		switch {
		case strings.Contains(inst, "sonarr"):
			return "Imported from Sonarr's blocklist"
		case strings.Contains(inst, "radarr"):
			return "Imported from Radarr's blocklist"
		}
		return "Imported from a Sonarr/Radarr blocklist"
	}
	return "Blocked"
}

// ContinueItem is one title on Home's "Continue" row: its most advanced download in hand.
type ContinueItem struct {
	ID      int64  `json:"id"` // the title id
	Name    string `json:"name"`
	Year    int    `json:"year,omitempty"`
	Kind    string `json:"kind"`
	Poster  string `json:"poster"`
	Version string `json:"version"`
	// Label names the library for people (its label; same-labelled libraries told apart as elsewhere).
	Label    string  `json:"label"`
	Download int64   `json:"download"`
	State    string  `json:"state"`
	Step     string  `json:"step"`
	Words    string  `json:"words"`
	Progress float64 `json:"progress"`
	// Downloads counts the title's downloads in hand.
	Downloads int `json:"downloads"`
}

// ContinueRow is GET /activity/continue, shaped like a discover row: items, total, more.
type ContinueRow struct {
	Items []ContinueItem `json:"items"`
	Total int            `json:"total"`
	More  string         `json:"more"`
}

// ContinueRow lists the titles with downloads in hand, most recently updated first, at most limit (≤ 50); the
// discover endpoint's continue row can call it.
func (w *Web) ContinueRow(ctx context.Context, limit int, scope requestScope) (ContinueRow, error) {
	row := ContinueRow{Items: []ContinueItem{}, More: "/activity"}
	all, err := activity.Downloads(ctx, w.st.Reader(), activity.Filter{States: activity.InHand})
	if err != nil {
		return row, err
	}
	ds, err := scopeDownloads(ctx, w.st.Reader(), scope, all)
	if err != nil {
		return row, err
	}
	byTitle := map[int64]int{}
	var order []activity.Download
	for _, d := range ds {
		i, ok := byTitle[d.TitleID]
		if !ok {
			byTitle[d.TitleID] = len(order)
			order = append(order, d)
			continue
		}
		cur := &order[i]
		s1, _ := d.Step()
		s0, _ := cur.Step()
		if s1 > s0 || s1 == s0 && d.Updated.After(cur.Updated) {
			*cur = d
		}
	}
	counts := map[int64]int{}
	for _, d := range ds {
		counts[d.TitleID]++
	}
	slices.SortStableFunc(order, func(a, b activity.Download) int { return b.Updated.Compare(a.Updated) })
	row.Total = len(order)
	if len(order) > limit {
		order = order[:limit]
	}
	ids := make([]int64, 0, len(order))
	for _, d := range order {
		ids = append(ids, d.TitleID)
	}
	posters, err := w.posters(ctx, ids)
	if err != nil {
		return row, err
	}
	for _, d := range order {
		step, _ := d.Step()
		row.Items = append(row.Items, ContinueItem{ID: d.TitleID, Name: d.TitleName, Year: d.TitleYear,
			Kind: titleKind(d.TitleKind, d.Anime), Poster: posters[d.TitleID], Version: d.Version, Download: d.ID,
			State: d.State, Step: activity.Steps[step], Words: d.Words(""), Progress: d.Progress,
			Downloads: counts[d.TitleID]})
	}
	return row, nil
}

func (w *Web) apiContinue(rw http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit", 20)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	scope, err := w.activityScope(r)
	if err != nil {
		w.internal(rw, err)
		return
	}
	row, err := w.ContinueRow(r.Context(), min(max(limit, 1), 50), scope)
	if err != nil {
		w.internal(rw, err)
		return
	}
	keys := make([]string, len(row.Items))
	for i, it := range row.Items {
		keys[i] = it.Version
	}
	names := w.keyLabels(keys)
	for i := range row.Items {
		row.Items[i].Label = names[row.Items[i].Version]
	}
	writeJSON(rw, http.StatusOK, row)
}

// CalendarEntry is one release date or episode air date.
type CalendarEntry struct {
	Date      string        `json:"date"`
	At        *time.Time    `json:"at,omitempty"`
	Kind      string        `json:"kind"` // movie, episode
	Type      string        `json:"type"` // theatrical, digital, physical, episode
	Title     ActivityTitle `json:"title"`
	EpisodeID int64         `json:"episode_id,omitempty"`
	Season    *int          `json:"season,omitempty"`
	Number    *int          `json:"number,omitempty"`
	Episode   string        `json:"episode,omitempty"`
	Monitored bool          `json:"monitored"`
	// Available: a file is held. For a movie, the title's status words (titleAvailabilitySQL): available or partly
	// available, and Partial for the latter (a monitored library has no file yet); for an episode, some library in the
	// library holds it.
	Available bool `json:"available"`
	Partial   bool `json:"partial,omitempty"`
	// Acquiring: a download is in hand (as on the list rows) for the movie, or for this very episode; Stage is the
	// furthest of their stages (activity.Stages), for callers who see files.
	Acquiring bool   `json:"acquiring"`
	Stage     string `json:"stage,omitempty"`
}

// CalendarPage is GET /calendar.
type CalendarPage struct {
	From  string          `json:"from"`
	To    string          `json:"to"`
	Items []CalendarEntry `json:"items"`
}

// maxCalendarDays bounds one calendar request (a month view with its edge weeks fits easily).
const maxCalendarDays = 120

func (w *Web) apiCalendar(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	today := w.now().UTC().Truncate(24 * time.Hour)
	from, to := today, today.AddDate(0, 0, 30)
	var err error
	if v := q.Get("from"); v != "" {
		if from, err = time.Parse(time.DateOnly, v); err != nil {
			badRequest(rw, "from must be YYYY-MM-DD")
			return
		}
	}
	if v := q.Get("to"); v != "" {
		if to, err = time.Parse(time.DateOnly, v); err != nil {
			badRequest(rw, "to must be YYYY-MM-DD")
			return
		}
	}
	if to.Before(from) {
		badRequest(rw, "to is before from")
		return
	}
	if to.Sub(from) > maxCalendarDays*24*time.Hour {
		badRequest(rw, fmt.Sprintf("at most %d days per request", maxCalendarDays))
		return
	}
	unmon := q.Get("unmonitored") == "true" || q.Get("unmonitored") == "1"
	kind := q.Get("kind")
	if !activityKinds[kind] {
		badRequest(rw, "kind must be movie, series or anime")
		return
	}
	items, err := activity.Calendar(r.Context(), w.st.Reader(), from.Format(time.DateOnly), to.Format(time.DateOnly), unmon)
	if err != nil {
		w.internal(rw, err)
		return
	}
	ids := make([]int64, 0, len(items))
	seen := map[int64]bool{}
	for _, it := range items {
		if !seen[it.TitleID] {
			seen[it.TitleID] = true
			ids = append(ids, it.TitleID)
		}
	}
	posters, err := w.posters(r.Context(), ids)
	if err != nil {
		w.internal(rw, err)
		return
	}
	// One read of the downloads in hand for the page: movies by title, episodes by episode.
	hand, err := w.inHand(r.Context(), w.st.Reader(), ids)
	if err != nil {
		w.internal(rw, err)
		return
	}
	// The furthest stage in hand, movies by title, episodes by episode ("" for a download already done).
	movieStage, episodeStage := map[int64]string{}, map[int64]string{}
	further := func(m map[int64]string, id int64, s string) {
		if cur, ok := m[id]; !ok || stageRank(s) > stageRank(cur) {
			m[id] = s
		}
	}
	for _, ds := range hand.byVersion {
		for _, d := range ds {
			further(movieStage, d.TitleID, d.Stage())
			for _, ep := range d.Episodes {
				further(episodeStage, ep, d.Stage())
			}
		}
	}
	var movieIDs []any
	for _, it := range items {
		if it.Kind == "movie" && !slices.Contains(movieIDs, any(it.TitleID)) {
			movieIDs = append(movieIDs, it.TitleID)
		}
	}
	avail, err := w.titleAvailability(r.Context(), w.st.Reader(), movieIDs)
	if err != nil {
		w.internal(rw, err)
		return
	}
	seeFiles := w.permsOf(r).SeeFiles
	page := CalendarPage{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly), Items: []CalendarEntry{}}
	for _, it := range items {
		tk := "movie"
		if it.Series {
			tk = "series"
		}
		x := CalendarEntry{Date: it.Date, Kind: it.Kind, Type: it.Type, Monitored: it.Monitored, Available: it.Available,
			Title: activityTitle(it.TitleID, it.TitleName, it.TitleYear, tk, it.Anime, posters)}
		if kind != "" && x.Title.Kind != kind {
			continue
		}
		if !it.At.IsZero() {
			t := it.At
			x.At = &t
		}
		stage, inHand := "", false
		if it.Kind == "episode" {
			s, n := it.Season, it.Number
			x.EpisodeID, x.Season, x.Number, x.Episode = it.EpisodeID, &s, &n, it.Episode
			stage, inHand = episodeStage[it.EpisodeID]
		} else {
			stage, inHand = movieStage[it.TitleID]
			a := avail[it.TitleID]
			x.Available, x.Partial = a == "available" || a == "partial", seeFiles && a == "partial"
		}
		x.Acquiring = inHand
		if seeFiles {
			x.Stage = stage
		}
		page.Items = append(page.Items, x)
	}
	writeJSON(rw, http.StatusOK, page)
}

// opaqueID stands for a journal id in a user's history: stable, unique enough for a list key, and naming nothing.
func opaqueID(id string) string {
	h := sha256.Sum256([]byte("activity:" + id))
	return "h:" + hex.EncodeToString(h[:9])
}
