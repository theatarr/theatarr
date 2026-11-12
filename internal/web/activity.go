package web

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/auth"
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
}

// ActivityEvent is one history entry: a journal entry in plain words.
type ActivityEvent struct {
	ID      string        `json:"id"`
	At      time.Time     `json:"at"`
	Kind    string        `json:"kind"`
	Title   ActivityTitle `json:"title"`
	Version string        `json:"version,omitempty"`
	Words   string        `json:"words"`
	Tone    string        `json:"tone"` // ok, info, warn, bad

	// Admin only.
	Summary  string `json:"summary,omitempty"`
	Source   string `json:"source,omitempty"`
	Release  string `json:"release,omitempty"`
	Provider string `json:"provider,omitempty"`
	Suspect  bool   `json:"suspect,omitempty"`
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

// historyKinds are the journal kinds the history lists: what happened to downloads, not shadow decisions or plans.
var historyKinds = []string{journal.KindGrab, journal.KindUpgrade, journal.KindImport, journal.KindLink,
	journal.KindFailure, journal.KindStall, journal.KindBlocklist, journal.KindResearch}

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
		f := journal.Filter{Kinds: historyKinds, Cursor: q.Get("cursor"), Limit: limit,
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
			f.Kinds = nil
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
		jp, err := w.journal.List(ctx, f)
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
		bs, total, err := activity.BlockedReleases(ctx, w.st.Reader(), limit, offset)
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
			x := ActivityBlocked{Version: b.Version, Hash: b.Hash, Release: b.Release, Reason: b.Reason, Source: b.Source,
				Words: blockWords(b.Source), DownloadID: b.DownloadID, Created: b.Created,
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
	if admin {
		evidence, err = w.activityEvidence(ctx, ds)
		if err != nil {
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
		for j, k := range activity.Steps {
			s := ActivityStep{Key: k, Label: stepLabels[k], State: "todo"}
			switch {
			case j < step:
				s.State = "done"
			case j == step && stopped && notStarted == "":
				s.State = "failed"
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
			}
		}
		if admin {
			x.Quality = d.Quality
			score := d.Score
			x.Release, x.Indexer, x.Hash, x.Size, x.Score = d.Release, d.Indexer, d.Hash, d.Size, &score
			x.Provider, x.ProviderName, x.Client = d.Provider, w.providerName(d.Provider), d.Client
			by := x.ProviderName
			if by == "" && d.Client != "" {
				by = "client " + d.Client
			}
			x.Detail, x.Cached, x.Reused, x.Source = d.Words(by), d.Cached, d.Reused, d.Source
			x.Journal = "dl:" + strconv.FormatInt(d.ID, 10)
			x.Error = activityMessage(d.Error)
			if notStarted != "" {
				x.Error = activity.NotStartedMessage(notStarted)
			}
			if x.Error == "" {
				x.Error = activityMessage(d.ImportErr)
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

// nowDownloads are running downloads and undismissed failures, newest first.
func (w *Web) nowDownloads(ctx context.Context, admin bool, kind string, titleID int64,
	scope requestScope) ([]ActivityDownload, error) {
	f := activity.Filter{States: append(slices.Clone(activity.InHand), "failed"), Undismissed: true}
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

// historyEvents turns journal entries into plain-words history rows.
func (w *Web) historyEvents(ctx context.Context, es []*journal.Entry, admin bool) ([]ActivityEvent, error) {
	ids := make([]int64, 0, len(es))
	for _, e := range es {
		ids = append(ids, e.Title.ID)
	}
	posters, err := w.posters(ctx, ids)
	if err != nil {
		return nil, err
	}
	anime, err := animeTitles(ctx, w.st.Reader(), ids)
	if err != nil {
		return nil, err
	}
	out := []ActivityEvent{}
	for _, e := range es {
		t := activityTitle(e.Title.ID, e.Title.Name, e.Title.Year, e.Title.Kind, anime[e.Title.ID], posters)
		notStarted := historyNotStarted(e)
		words, tone := entryWords(e, notStarted)
		x := ActivityEvent{ID: e.ID, At: e.At, Kind: e.Kind, Title: t, Version: e.Version, Words: words, Tone: tone}
		if !admin {
			// Journal ids name the arr instance (arr:<instance>:<id>); users get an opaque one.
			x.ID = opaqueID(e.ID)
		}
		if admin {
			x.Summary, x.Source, x.Suspect = e.Summary, e.Source, e.Suspect
			if notStarted != "" {
				x.Summary = activity.NotStartedMessage(notStarted) // the journal entry keeps its own text
			}
			if e.Release != nil {
				x.Release = e.Release.Title
			}
			if e.Download != nil {
				x.Provider = e.Download.Provider
			}
		}
		out = append(out, x)
	}
	return out, nil
}

func animeTitles(ctx context.Context, db *sql.DB, ids []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.QueryContext(ctx, `SELECT id FROM title WHERE anime = 1 AND id IN (`+
		strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// historyNotStarted is the not-started reason of a failure entry (as activity.Download.NotStarted: a gate text and
// no attempt activity.Tried counts), "" for any other entry.
func historyNotStarted(e *journal.Entry) string {
	if e.Kind != journal.KindFailure || e.Download == nil {
		return ""
	}
	r := activity.NotStarted(e.Download.Error)
	if r == "" {
		return ""
	}
	for _, a := range e.Attempts {
		if activity.Tried(a.Outcome, a.Code) {
			return ""
		}
	}
	return r
}

// historyWords says what a journal entry means, without release names or providers.
func historyWords(e *journal.Entry) (words, tone string) { return entryWords(e, historyNotStarted(e)) }

// entryWords is historyWords with the entry's historyNotStarted reason already known.
func entryWords(e *journal.Entry, notStarted string) (words, tone string) {
	arr := strings.HasPrefix(e.Source, "arr:")
	switch e.Kind {
	case journal.KindGrab:
		if arr {
			return "Grabbed by the old manager", "info"
		}
		return "Found a release", "info"
	case journal.KindUpgrade:
		if arr {
			return "Upgrade grabbed by the old manager", "info"
		}
		return "Found a better release", "info"
	case journal.KindImport:
		if arr {
			return "Imported by the old manager", "ok"
		}
		state := ""
		if e.Import != nil {
			state = e.Import.State
		}
		switch state {
		case "linked":
			return "Linked into the library", "ok"
		case "partial":
			return "Partly imported (retrying)", "warn"
		case "rejected":
			return "Rejected by the library check", "bad"
		case "dry_run":
			return "Would be linked (the library is in dry-run mode)", "info"
		case "waiting_mount":
			return "Waiting for the mount", "warn"
		case "failed":
			return "Linking failed", "bad"
		}
		return "Imported", "ok"
	case journal.KindLink:
		if e.Import != nil && e.Import.State == "dry_run" {
			return "Would be linked (the library is in dry-run mode)", "info"
		}
		k := ""
		if e.Import != nil && len(e.Import.Links) > 0 {
			k = e.Import.Links[0].Kind
		}
		switch k {
		case "replace":
			return "Replaced by an upgrade", "info"
		case "relink":
			return "Link repaired", "ok"
		case "dead":
			return "A link broke and could not be repaired", "bad"
		}
		return "Linked into the library", "ok"
	case journal.KindFailure:
		if notStarted != "" {
			return activity.NotStartedHistory(notStarted), "info"
		}
		if arr {
			return "Download failed (old manager)", "bad"
		}
		return "Download failed", "bad"
	case journal.KindStall:
		return "Download stalled and was dropped", "bad"
	case journal.KindBlocklist:
		return "Release blocked; it won't be tried again", "warn"
	case journal.KindResearch:
		return "Searching again after a failure", "info"
	}
	return e.Kind, "info"
}

// blockWords says why a release is blocked, by the blocklist's source.
func blockWords(source string) string {
	switch source {
	case "library":
		return "Failed the library check (sample or runtime)"
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
	}
	return "Blocked"
}

// ContinueItem is one title on Home's "Continue" row: its most advanced download in hand.
type ContinueItem struct {
	ID       int64   `json:"id"` // the title id
	Name     string  `json:"name"`
	Year     int     `json:"year,omitempty"`
	Kind     string  `json:"kind"`
	Poster   string  `json:"poster"`
	Version  string  `json:"version"`
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
	Available bool          `json:"available"`
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
		if it.Kind == "episode" {
			s, n := it.Season, it.Number
			x.EpisodeID, x.Season, x.Number, x.Episode = it.EpisodeID, &s, &n, it.Episode
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
