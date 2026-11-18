package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// Problem reports (docs/design/users.md "Report a problem", migration 00147). Anyone who may request reports a
// movie or an episode that has a file: picture, sound, subtitles, won't play or something else, with an optional
// note. One open report per account, title and episode; a second one updates it. The reporter sees their reports
// with the status and the reply under Requests › Problems; managers (manage_requests) see every open report there,
// with, for those who see files, the files the title's libraries hold for it now. A manager replaces a file (unlink,
// blocklist its release in each library that loses it, search those libraries again) or marks the report fixed.
// Replace is offered only when the search can run: a file is never removed without a search after it, and nothing
// here touches a debrid torrent (the unlink moves our symlink to the trash). A new report goes to the message and
// native notification targets that get request messages; "fixed" reaches the reporter in the app.

// Report states.
const (
	ReportOpen  = "open"
	ReportFixed = "fixed"
)

// reportKinds are what can be wrong, with the words the screens and messages use.
var reportKinds = map[string]string{"picture": "Picture", "sound": "Sound", "subtitles": "Subtitles",
	"wont_play": "Won't play", "other": "Something else"}

// reportTextMax caps a report's note and a manager's reply, in characters.
const reportTextMax = 500

func (w *Web) mountReportsAPI(mux *http.ServeMux) {
	b := APIBase + "/reports"
	w.handle(mux, "GET "+b, SignedIn, auth.PermBrowse, w.apiReports)
	w.handle(mux, "POST "+b, SignedIn, auth.PermRequest, w.apiCreateReport)
	w.handle(mux, "POST "+b+"/{id}/fix", SignedIn, auth.PermManageRequests, w.apiFixReport)
	w.handle(mux, "POST "+b+"/{id}/replace", SignedIn, auth.PermManage, w.apiReplaceReported)
	w.handle(mux, "POST "+APIBase+"/links/{id}/replace", SignedIn, auth.PermManage, w.apiReplaceLink)
}

// ProblemReport is one report as the API returns it.
type ProblemReport struct {
	ID        int64  `json:"id"`
	TitleID   int64  `json:"title_id"`
	TitleKind string `json:"title_kind"`
	Name      string `json:"name"`
	Year      *int64 `json:"year,omitempty"`
	Poster    string `json:"poster,omitempty"`
	// EpisodeID is the episode reported (0 for a movie); Episode its code (S01E02) and EpisodeName its name.
	EpisodeID   int64  `json:"episode_id"`
	Episode     string `json:"episode,omitempty"`
	EpisodeName string `json:"episode_name,omitempty"`
	Kind        string `json:"kind"`
	KindLabel   string `json:"kind_label"`
	Note        string `json:"note,omitempty"`
	Status      string `json:"status"`
	Reply       string `json:"reply,omitempty"`
	Mine        bool   `json:"mine"`
	// User is who reported it, and ResolvedBy who marked it fixed (managers only).
	User       *RequestUser `json:"user,omitempty"`
	ResolvedBy *RequestUser `json:"resolved_by,omitempty"`
	ResolvedAt *time.Time   `json:"resolved_at,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
	// Files are the files the title's libraries hold for the movie or episode now (managers who see files).
	Files []ReportFile `json:"files,omitempty"`

	userID, resolvedBy int64
}

// ReportFile is one file a reported movie or episode has now, and whether Replace file can run for it.
type ReportFile struct {
	LinkID       int64  `json:"link_id"`
	Path         string `json:"path"`
	ReleaseTitle string `json:"release_title,omitempty"`
	Quality      string `json:"quality,omitempty"`
	// Versions are the libraries that hold the file (keys): unlinking takes it out of all.
	Versions []string `json:"versions"`
	// The scope: the libraries' names, every episode the file holds, pack, and whether Replace may run now.
	ReplaceScope

	episodeIDs []int64
	hash       string
	downloadID int64
}

// ProblemEvent is the SSE "problem" event: a report was made, changed or fixed. It goes to managers and to the
// reporter (mine marks the reporter's copy).
type ProblemEvent struct {
	ID      int64  `json:"id"`
	TitleID int64  `json:"title_id"`
	Name    string `json:"name"`
	Episode string `json:"episode,omitempty"`
	Status  string `json:"status"`
	// Change is created, updated or fixed.
	Change string `json:"change"`
	Reply  string `json:"reply,omitempty"`
	Mine   bool   `json:"mine,omitempty"`
}

const reportSelect = `SELECT p.id, p.title_id, t.kind, t.name, t.year, p.episode_id, COALESCE(e.season, 0),
	COALESCE(e.number, 0), COALESCE(e.name, ''), p.kind, p.note, p.status, p.reply, COALESCE(p.user_id, 0),
	COALESCE(u.username, ''), COALESCE(u.display_name, ''), COALESCE(p.resolved_by, 0), COALESCE(r.username, ''),
	COALESCE(r.display_name, ''), p.resolved_at, p.created_at, p.updated_at
	FROM problem_report p JOIN title t ON t.id = p.title_id
	LEFT JOIN episode e ON e.id = p.episode_id AND p.episode_id > 0
	LEFT JOIN web_user u ON u.id = p.user_id LEFT JOIN web_user r ON r.id = p.resolved_by`

func scanReport(rows interface{ Scan(...any) error }) (ProblemReport, error) {
	var p ProblemReport
	var year, resolvedAt sql.NullInt64
	var season, number int64
	var uname, udisplay, rname, rdisplay string
	var created, updated int64
	err := rows.Scan(&p.ID, &p.TitleID, &p.TitleKind, &p.Name, &year, &p.EpisodeID, &season, &number, &p.EpisodeName,
		&p.Kind, &p.Note, &p.Status, &p.Reply, &p.userID, &uname, &udisplay, &p.resolvedBy, &rname, &rdisplay,
		&resolvedAt, &created, &updated)
	if err != nil {
		return p, err
	}
	if year.Valid {
		p.Year = &year.Int64
	}
	if p.EpisodeID > 0 {
		p.Episode = episodeCode(season, number)
	}
	p.KindLabel = reportKinds[p.Kind]
	if p.userID > 0 {
		p.User = &RequestUser{ID: p.userID, Username: uname, DisplayName: udisplay}
	}
	if p.resolvedBy > 0 {
		p.ResolvedBy = &RequestUser{ID: p.resolvedBy, Username: rname, DisplayName: rdisplay}
	}
	if resolvedAt.Valid {
		t := store.FromMillis(resolvedAt.Int64)
		p.ResolvedAt = &t
	}
	p.CreatedAt, p.UpdatedAt = store.FromMillis(created), store.FromMillis(updated)
	return p, nil
}

// getReport reads one report (nil when there is none).
func (w *Web) getReport(ctx context.Context, id int64) (*ProblemReport, error) {
	p, err := scanReport(w.st.Reader().QueryRowContext(ctx, reportSelect+` WHERE p.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ReportPage is GET reports.
type ReportPage struct {
	Items []ProblemReport `json:"items"`
}

// reportListMax caps GET reports.
const reportListMax = 200

// GET /reports[?mine=1]: managers get every open report (oldest first, the order to work through), others (and
// mine=1) their own, open and fixed, newest first.
func (w *Web) apiReports(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	viewer := auth.FromContext(ctx)
	perms := w.permsOf(r)
	manager := perms.Has(auth.PermManageRequests) && r.URL.Query().Get("mine") == ""
	q, args := reportSelect+` WHERE p.user_id = ? ORDER BY p.updated_at DESC, p.id DESC LIMIT ?`,
		[]any{viewer.UserID, reportListMax}
	if manager {
		q, args = reportSelect+` WHERE p.status = 'open' ORDER BY p.created_at, p.id LIMIT ?`, []any{reportListMax}
	}
	out := ReportPage{Items: []ProblemReport{}}
	err := scanAll(ctx, w.st.Reader(), q, args, func(rows *sql.Rows) error {
		p, err := scanReport(rows)
		if err == nil {
			out.Items = append(out.Items, p)
		}
		return err
	})
	if err != nil {
		w.internal(rw, err)
		return
	}
	var ids []int64
	for _, p := range out.Items {
		ids = append(ids, p.TitleID)
	}
	posters, err := w.posters(ctx, ids)
	if err != nil {
		w.internal(rw, err)
		return
	}
	for i := range out.Items {
		p := &out.Items[i]
		p.Poster = posters[p.TitleID]
		p.Mine = p.userID == viewer.UserID
		if !perms.Has(auth.PermManageRequests) {
			p.User, p.ResolvedBy = nil, nil
		}
		if manager && perms.SeeFiles {
			if p.Files, err = w.reportFiles(ctx, p.TitleID, p.EpisodeID); err != nil {
				w.internal(rw, err)
				return
			}
		}
	}
	writeJSON(rw, http.StatusOK, out)
}

// CreateReport is POST reports.
type CreateReport struct {
	TitleID int64 `json:"title_id"`
	// EpisodeID is the episode (series); 0 or absent for a movie.
	EpisodeID int64  `json:"episode_id"`
	Kind      string `json:"kind"`
	Note      string `json:"note"`
}

// POST /reports: report a movie or an episode that has a file. A report of the same title and episode the caller
// still has open is updated (200); a new one is 201 and notifies the message and native targets.
func (w *Web) apiCreateReport(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	viewer := auth.FromContext(ctx)
	var body CreateReport
	if err := decodeJSON(r, &body); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	body.Note = strings.TrimSpace(body.Note)
	if reportKinds[body.Kind] == "" {
		badRequest(rw, "kind must be picture, sound, subtitles, wont_play or other")
		return
	}
	if utf8.RuneCountInString(body.Note) > reportTextMax {
		badRequest(rw, "note: at most 500 characters")
		return
	}
	db := w.st.Reader()
	var kind string
	if err := db.QueryRowContext(ctx, `SELECT kind FROM title WHERE id = ?`, body.TitleID).Scan(&kind); errors.Is(err,
		sql.ErrNoRows) {
		notFound(rw)
		return
	} else if err != nil {
		w.internal(rw, err)
		return
	}
	switch {
	case kind == "series" && body.EpisodeID <= 0:
		badRequest(rw, "a series report names its episode (episode_id)")
		return
	case kind != "series" && body.EpisodeID != 0:
		badRequest(rw, "a movie report names no episode")
		return
	}
	// Only something with a file can be reported; without one it is a request.
	q, args := `SELECT EXISTS (SELECT 1 FROM link l WHERE l.title_id = ? AND l.state = 'active'
		AND EXISTS (SELECT 1 FROM link_version x WHERE x.link_id = l.id))`, []any{body.TitleID}
	if body.EpisodeID > 0 {
		q, args = `SELECT EXISTS (SELECT 1 FROM link l JOIN link_episode x ON x.link_id = l.id AND x.episode_id = ?
			JOIN episode e ON e.id = x.episode_id AND e.title_id = ? WHERE l.state = 'active'
			AND EXISTS (SELECT 1 FROM link_version v WHERE v.link_id = l.id))`, []any{body.EpisodeID, body.TitleID}
	}
	var has bool
	if err := db.QueryRowContext(ctx, q, args...).Scan(&has); err != nil {
		w.internal(rw, err)
		return
	}
	if !has {
		writeJSON(rw, http.StatusConflict, apiError{Error: "There is no file to report yet: request it instead"})
		return
	}
	now := store.Millis(w.now())
	var id int64
	created := false
	err := w.st.WriteTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT id FROM problem_report WHERE user_id = ? AND title_id = ?
			AND episode_id = ? AND status = 'open'`, viewer.UserID, body.TitleID, body.EpisodeID).Scan(&id)
		switch {
		case err == nil:
			_, err = tx.ExecContext(ctx, `UPDATE problem_report SET kind = ?, note = ?, updated_at = ? WHERE id = ?`,
				body.Kind, body.Note, now, id)
			return err
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		created = true
		return tx.QueryRowContext(ctx, `INSERT INTO problem_report (user_id, title_id, episode_id, kind, note,
			created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`, nullable(viewer.UserID), body.TitleID,
			body.EpisodeID, body.Kind, body.Note, now, now).Scan(&id)
	})
	if err != nil {
		w.internal(rw, err)
		return
	}
	p, err := w.getReport(ctx, id)
	if err != nil || p == nil {
		w.internal(rw, fmt.Errorf("reading report %d: %v", id, err))
		return
	}
	p.Mine = true
	change := "updated"
	if created {
		change = "created"
		w.problemNotification(ctx, p)
	}
	w.problemChanged(*p, change)
	w.log.Info("problem report "+change, "report", id, "title", p.TitleID, "episode", p.EpisodeID, "kind", p.Kind,
		"by", viewer.UserID)
	if !w.permsOf(r).Has(auth.PermManageRequests) {
		p.User, p.ResolvedBy = nil, nil
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(rw, code, p)
}

// problemNotification sends a new report to the notification targets (OnProblem), with the libraries whose file of
// the movie or episode was reported, so a target's library filter applies as it does to request messages.
func (w *Web) problemNotification(ctx context.Context, p *ProblemReport) {
	if w.onProblem == nil {
		return
	}
	q, args := `SELECT DISTINCT v.policy_key FROM link l JOIN link_version x ON x.link_id = l.id
		JOIN version v ON v.id = x.version_id WHERE l.title_id = ? AND l.state = 'active'`, []any{p.TitleID}
	if p.EpisodeID > 0 {
		q += ` AND EXISTS (SELECT 1 FROM link_episode e WHERE e.link_id = l.id AND e.episode_id = ?)`
		args = append(args, p.EpisodeID)
	}
	versions := []string{}
	if err := scanAll(ctx, w.st.Reader(), q+` ORDER BY v.policy_key`, args, func(rows *sql.Rows) error {
		var k string
		err := rows.Scan(&k)
		versions = append(versions, k)
		return err
	}); err != nil {
		w.log.Warn("problem report notification", "report", p.ID, "err", err)
		return
	}
	name := p.Name
	if p.Year != nil && *p.Year > 0 {
		name += " (" + strconv.FormatInt(*p.Year, 10) + ")"
	}
	who := notify.RequestUser{Username: "someone"}
	if p.User != nil {
		who = notify.RequestUser{ID: p.User.ID, Username: p.User.Username, DisplayName: p.User.DisplayName}
	}
	ev := notify.Event{Kind: notify.ProblemReported, At: p.CreatedAt, TitleID: p.TitleID,
		Problem: &notify.Problem{ID: p.ID, Name: name, Episode: p.Episode, Kind: p.Kind, Label: p.KindLabel,
			Note: p.Note, Reporter: who, Versions: versions}}
	if p.EpisodeID > 0 {
		ev.Episodes = []int64{p.EpisodeID}
	}
	w.onProblem(ev)
}

// problemChanged tells managers' screens and the reporter that a report changed.
func (w *Web) problemChanged(p ProblemReport, change string) {
	ev := ProblemEvent{ID: p.ID, TitleID: p.TitleID, Name: p.Name, Episode: p.Episode, Status: p.Status,
		Change: change}
	if change == "fixed" {
		ev.Reply = p.Reply
	}
	data, _ := json.Marshal(ev)
	ev.Mine = true
	mine, _ := json.Marshal(ev)
	users := []int64{}
	if p.userID > 0 {
		users = append(users, p.userID)
	}
	w.hub.publish(event{name: "problem", data: data, mine: mine, need: auth.PermBrowse, toUsers: true, users: users,
		orPerm: auth.PermManageRequests})
}

// FixReport is POST reports/{id}/fix.
type FixReport struct {
	Reply string `json:"reply"`
}

// POST /reports/{id}/fix {reply}: mark an open report fixed, with an optional reply the reporter sees.
func (w *Web) apiFixReport(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var body FixReport
	if err := decodeJSON(r, &body); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	body.Reply = strings.TrimSpace(body.Reply)
	if utf8.RuneCountInString(body.Reply) > reportTextMax {
		badRequest(rw, "reply: at most 500 characters")
		return
	}
	viewer := auth.FromContext(ctx)
	now := store.Millis(w.now())
	res, err := w.st.Writer().ExecContext(ctx, `UPDATE problem_report SET status = 'fixed', reply = ?, resolved_by = ?,
		resolved_at = ?, updated_at = ? WHERE id = ? AND status = 'open'`, body.Reply, nullable(viewer.UserID), now, now, id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	p, err := w.getReport(ctx, id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if p == nil {
		notFound(rw)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(rw, http.StatusConflict, apiError{Error: "The report is already marked fixed"})
		return
	}
	p.Mine = p.userID == viewer.UserID
	w.problemChanged(*p, "fixed")
	w.log.Info("problem report fixed", "report", id, "by", viewer.UserID)
	writeJSON(rw, http.StatusOK, p)
}

// reportFiles are the files the title's libraries hold for a movie (episodeID 0) or an episode now: Theatarr's
// active links, each with the libraries and episodes that lose it on an unlink, and whether Replace file can run.
func (w *Web) reportFiles(ctx context.Context, titleID, episodeID int64) ([]ReportFile, error) {
	return w.linkFiles(ctx, titleID, episodeID, 0)
}

// linkFiles is reportFiles for the title's active links (those holding episodeID when it is not 0; only linkID when
// it is not 0), read in a few queries for the whole title: the Problems page, the title page's file rows and the
// episode window use it alike (Replace file, linkreplace.go).
func (w *Web) linkFiles(ctx context.Context, titleID, episodeID, linkID int64) ([]ReportFile, error) {
	db := w.st.Reader()
	q := `SELECT l.id, l.path, l.kind, COALESCE(NULLIF(l.release_title, ''), d.release_title, ''),
		COALESCE(l.quality, ''), COALESCE(l.download_id, 0), COALESCE(` + router.LinkStoredHashSQL + `, ''),
		COALESCE(json_array_length(d.episodes), 0) > (SELECT COUNT(*) FROM link_episode x WHERE x.link_id = l.id)
		OR EXISTS (SELECT 1 FROM content_file a JOIN content_file b ON b.content_id = a.content_id AND b.id <> a.id
			WHERE a.id = l.content_file_id)
		FROM link l LEFT JOIN download d ON d.id = l.download_id
		WHERE l.title_id = ? AND l.state = 'active'`
	args := []any{titleID}
	if episodeID > 0 {
		q += ` AND EXISTS (SELECT 1 FROM link_episode x WHERE x.link_id = l.id AND x.episode_id = ?)`
		args = append(args, episodeID)
	}
	if linkID > 0 {
		q += ` AND l.id = ?`
		args = append(args, linkID)
	}
	q += ` ORDER BY l.id`
	var out []ReportFile
	kinds := map[int64]string{}
	err := scanAll(ctx, db, q, args, func(rows *sql.Rows) error {
		var f ReportFile
		var kind string
		if err := rows.Scan(&f.LinkID, &f.Path, &kind, &f.ReleaseTitle, &f.Quality, &f.downloadID, &f.hash,
			&f.Pack); err != nil {
			return err
		}
		kinds[f.LinkID] = kind
		out = append(out, f)
		return nil
	})
	if err != nil || len(out) == 0 {
		return nil, err
	}
	on, err := w.versionsOn(ctx, titleID)
	if err != nil {
		return nil, err
	}
	// Anime as the search reads it (acquire): the anime flag or the anime series type.
	var anime bool
	if err := db.QueryRowContext(ctx, `SELECT t.anime = 1 OR `+store.SeriesTypeSQL("t")+` = 'anime' FROM title t
		WHERE t.id = ?`, titleID).Scan(&anime); err != nil {
		return nil, err
	}
	// The libraries and episodes of those links, in one read each.
	ids := make([]int64, len(out))
	for i, f := range out {
		ids[i] = f.LinkID
	}
	idList, _ := json.Marshal(ids)
	active := `SELECT value FROM json_each(?)`
	versions := map[int64][]string{}
	if err := scanAll(ctx, db, `SELECT x.link_id, v.policy_key FROM link_version x JOIN version v ON v.id = x.version_id
		WHERE x.link_id IN (`+active+`) ORDER BY x.link_id, v.policy_key`, []any{string(idList)}, func(rows *sql.Rows) error {
		var id int64
		var k string
		err := rows.Scan(&id, &k)
		versions[id] = append(versions[id], k)
		return err
	}); err != nil {
		return nil, err
	}
	type ep struct {
		id   int64
		code string
	}
	episodes := map[int64][]ep{}
	if err := scanAll(ctx, db, `SELECT x.link_id, e.id, e.season, e.number FROM link_episode x
		JOIN episode e ON e.id = x.episode_id WHERE x.link_id IN (`+active+`) ORDER BY e.season, e.number`,
		[]any{string(idList)}, func(rows *sql.Rows) error {
			var link, id, s, n int64
			err := rows.Scan(&link, &id, &s, &n)
			episodes[link] = append(episodes[link], ep{id, episodeCode(s, n)})
			return err
		}); err != nil {
		return nil, err
	}
	// Each episode a link holds is monitored per library: the episode's own switch, else its default.
	monitored := map[episodeKey]bool{}
	if len(episodes) > 0 {
		if err := scanAll(ctx, db, `SELECT e.id, v.policy_key FROM episode e JOIN version v ON v.title_id = e.title_id
			LEFT JOIN episode_version_state s ON s.version_id = v.id AND s.episode_id = e.id
			WHERE e.title_id = ? AND COALESCE(s.monitored, e.monitored) = 1 AND e.id IN (SELECT x.episode_id
				FROM link_episode x WHERE x.link_id IN (`+active+`))`, []any{titleID, string(idList)}, func(rows *sql.Rows) error {
			var k episodeKey
			err := rows.Scan(&k.id, &k.version)
			monitored[k] = true
			return err
		}); err != nil {
			return nil, err
		}
	}
	kept := out[:0]
	for _, f := range out {
		f.Versions = versions[f.LinkID]
		if len(f.Versions) == 0 {
			continue // a link of no library: nothing to replace there
		}
		labels := w.keyLabels(f.Versions)
		for _, k := range f.Versions {
			f.Libraries = append(f.Libraries, labels[k])
		}
		for _, e := range episodes[f.LinkID] {
			f.episodeIDs = append(f.episodeIDs, e.id)
			f.Episodes = append(f.Episodes, e.code)
		}
		f.Why = w.replaceBlocked(f, kinds[f.LinkID], on, monitored, anime)
		f.Replace = f.Why == ""
		kept = append(kept, f)
	}
	return kept, nil
}

// episodeKey is an episode in one library (linkFiles).
type episodeKey struct {
	id      int64
	version string
}

// replaceBlocked says why Replace file cannot run for f ("" when it can): it runs only when the search after it can,
// in every library that loses the file, for the movie or every episode the file holds (monitored: the episodes
// monitored per library; anime: the title is anime as the search reads it, which skips a library whose anime
// setting does not match).
func (w *Web) replaceBlocked(f ReportFile, kind string, on []string, monitored map[episodeKey]bool, anime bool) string {
	if kind != "symlink" {
		return "This file is not one of Theatarr's links, so Theatarr cannot replace it"
	}
	if router.BlocklistKey(f.hash, f.ReleaseTitle) == "" {
		return "Theatarr does not know this file's release, so it cannot keep it from coming back"
	}
	if w.ops == nil || w.add.Acquirer == nil {
		return "Searching is not running, so no new file would be searched"
	}
	if _, skipped := catalog.Searchable(w.pol, f.Versions); len(skipped) > 0 {
		// skipWords names the libraries (it takes their keys).
		return "Not searched now: " + w.skipWords(skipped)
	}
	var unfit []string
	for _, k := range f.Versions {
		if vp := w.pol.Versions[k]; vp.Anime != nil && *vp.Anime != anime {
			unfit = append(unfit, k)
		}
	}
	if len(unfit) > 0 {
		verb, why := " does not", "it is for anime only"
		if len(unfit) > 1 {
			verb, why = " do not", "they are for anime only"
		}
		if anime {
			why = strings.Replace(why, "for anime only", "not for anime", 1)
		}
		return w.labelList(unfit) + verb + " search this title (" + why + "), so no new file would be searched"
	}
	var off []string
	for _, k := range f.Versions {
		if !slices.Contains(on, k) {
			off = append(off, k)
		}
	}
	if len(off) > 0 {
		return w.labelList(off) + " is not monitored, so no new file would be searched"
	}
	var codes []string
	for i, eid := range f.episodeIDs {
		for _, k := range f.Versions {
			if !monitored[episodeKey{eid, k}] {
				codes = append(codes, f.Episodes[i])
				break
			}
		}
	}
	if len(codes) > 0 {
		return strings.Join(codes, ", ") + " is not monitored in every library that has this file, so no new file " +
			"would be searched"
	}
	return ""
}

// ReplaceReported is POST reports/{id}/replace.
type ReplaceReported struct {
	LinkID int64 `json:"link_id"`
}

// ReplaceResult is the answer of Replace file.
type ReplaceResult struct {
	Message string         `json:"message"`
	Search  *SearchOutcome `json:"search"`
}

// POST /reports/{id}/replace {link_id}: replace a file a report names: unlink it (to the trash; the debrid torrent
// and other files are untouched), blocklist its release in each library that loses it, and search those libraries
// for the movie or every episode the file held. 409 with the reason when the search could not run, before anything
// changes. The report stays open.
func (w *Web) apiReplaceReported(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var body ReplaceReported
	if err := decodeJSON(r, &body); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	p, err := w.getReport(ctx, id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if p == nil {
		notFound(rw)
		return
	}
	if p.Status != ReportOpen {
		writeJSON(rw, http.StatusConflict, apiError{Error: "The report is already marked fixed"})
		return
	}
	files, err := w.reportFiles(ctx, p.TitleID, p.EpisodeID)
	if err != nil {
		w.internal(rw, err)
		return
	}
	i := slices.IndexFunc(files, func(f ReportFile) bool { return f.LinkID == body.LinkID })
	if i < 0 {
		writeJSON(rw, http.StatusConflict, apiError{Error: "That file is no longer in the library for this report"})
		return
	}
	by := actor(r)
	w.replaceFile(rw, r, p.TitleID, files[i], "replaced for a problem report by "+orSomeone(by),
		"replaced for problem report "+strconv.FormatInt(p.ID, 10), "file replaced for a problem report",
		"report", p.ID)
}

// replaceFile runs Replace file for f, one of the title's files (linkFiles), and answers it: 409 with the reason
// when the search after it could not run, before anything changes; else unlink it (reason), blocklist its release in
// each library that loses it, search those libraries for the movie or every episode the file held, and record it in
// the journal (note first in the detail) and the log (logMsg, with logArgs).
func (w *Web) replaceFile(rw http.ResponseWriter, r *http.Request, titleID int64, f ReportFile, reason, note,
	logMsg string, logArgs ...any) {
	if !f.Replace {
		writeJSON(rw, http.StatusConflict, apiError{Error: f.Why})
		return
	}
	ctx := r.Context()
	by := actor(r)
	u, err := w.ops.UnlinkLink(ctx, f.LinkID, reason)
	if err != nil {
		w.opsError(rw, err)
		return
	}
	// The file is gone: the blocklist and the search finish even when the tab closes now.
	ctx = context.WithoutCancel(ctx)
	at := store.Millis(w.now())
	if err := w.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, k := range f.Versions {
			if err := router.Blocklist(ctx, tx, k, f.hash, titleID, f.downloadID, f.ReleaseTitle, reason, BlocklistUI,
				at); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		w.internal(rw, err)
		return
	}
	s, err := w.searchReplaced(ctx, titleID, f)
	if err != nil {
		w.internal(rw, err)
		return
	}
	out := ReplaceResult{Search: s, Message: "Removed " + path.Base(f.Path) + "; the release is blocklisted in " +
		w.labelList(f.Versions) + ". " + s.Message}
	w.record(ctx, journal.Action{Action: journal.ActUnlinkFile, TitleID: titleID, Version: strings.Join(f.Versions, ","),
		InfoHash: f.hash, Release: f.ReleaseTitle, DownloadID: f.downloadID, Target: u.Path,
		Detail: note + "; moved to " + orDash(u.Trash) + "; blocklisted; search: " + s.Message, By: by})
	w.log.Info(logMsg, append(logArgs, "link", f.LinkID, "versions", strings.Join(f.Versions, ","), "queued", s.Queued,
		"by", by)...)
	writeJSON(rw, http.StatusOK, out)
}

// searchReplaced asks the search for the movie, or every episode the replaced file held, in its libraries.
// replaceBlocked has already found every library searchable in this request.
func (w *Web) searchReplaced(ctx context.Context, titleID int64, f ReportFile) (*SearchOutcome, error) {
	if len(f.episodeIDs) == 0 {
		return w.searchNow(ctx, titleID, f.Versions)
	}
	queued, found, err := w.add.Acquirer.Submit(ctx, 0, acquire.Selection{Versions: f.Versions,
		TitleIDs: []int64{titleID}, EpisodeIDs: f.episodeIDs})
	if err != nil {
		return nil, err
	}
	out := &SearchOutcome{Versions: f.Versions, Skipped: []catalog.Skip{}, Queued: queued, Found: found,
		Started: queued > 0, Message: "Searching " + strings.Join(f.Episodes, ", ") + " in " + w.labelList(f.Versions)}
	if queued == 0 {
		out.Message = "Nothing to search: no monitored episode in " + strings.Join(f.Episodes, ", ")
	}
	return out, nil
}

// openReports counts the open problem reports (the managers' Requests badge and System › Status).
func (w *Web) openReports(ctx context.Context) (int, error) {
	var n int
	err := w.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM problem_report WHERE status = 'open'`).Scan(&n)
	return n, err
}
