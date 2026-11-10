package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Lists (docs/design/arrview.md, Lists): the movie_import_list rows movie intake syncs (internal/catalog MovieIntake),
// for the Lists tab of the Collections page. TMDb movie lists only. A list made here has no view, so no arr view
// lists it; a list an arr client made keeps its view, profile and root whatever is changed here, so that client still
// sees it. An enabled list adds its new movies at every intake refresh (metadata.lists.interval) and on "Sync now".

// MovieList is one import list as movie intake keeps it.
type MovieList struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	ListType string `json:"list_type"`
	TmdbID   int64  `json:"tmdb_id"`
	// Enabled: the scheduled sync imports this list. For a list an arr client made, it also needs EnableAuto, which
	// only that client changes.
	Enabled    bool `json:"enabled"`
	EnableAuto bool `json:"enable_auto"`
	// Monitor: movieOnly, movieAndCollection (also monitors the movies' collections) or none (added unmonitored).
	Monitor             string     `json:"monitor"`
	SearchOnAdd         bool       `json:"search_on_add"`
	Versions            []string   `json:"versions"`
	MinimumAvailability string     `json:"minimum_availability"`
	LastSyncAt          *time.Time `json:"last_sync_at,omitempty"`
	// View is the arr view whose client made the list; empty for a list made here.
	View string `json:"view,omitempty"`
	// Libraries are the libraries this list may add to: every movie library, or, with a view, that view's.
	Libraries []string `json:"libraries"`
}

// ListsPage is GET /lists: every import list, and the movie libraries a list may add to.
type ListsPage struct {
	Lists     []MovieList       `json:"lists"`
	Libraries map[string]string `json:"libraries"`
	// CanSync: TMDb is set up, so lists can be looked up and synced.
	CanSync bool `json:"can_sync"`
}

// ListAdd is POST /lists.
type ListAdd struct {
	TmdbID   int64    `json:"tmdb_id"`
	Name     string   `json:"name"`
	Versions []string `json:"versions"`
	// Monitor defaults to movieOnly, MinimumAvailability to released, Enabled to true.
	Monitor             string `json:"monitor"`
	SearchOnAdd         bool   `json:"search_on_add"`
	MinimumAvailability string `json:"minimum_availability"`
	Enabled             *bool  `json:"enabled"`
}

// ListUpdate is PUT /lists/{id}; absent fields keep their value.
type ListUpdate struct {
	Name                *string   `json:"name"`
	Enabled             *bool     `json:"enabled"`
	Monitor             *string   `json:"monitor"`
	SearchOnAdd         *bool     `json:"search_on_add"`
	MinimumAvailability *string   `json:"minimum_availability"`
	Versions            *[]string `json:"versions"`
}

// ListLookup is GET /lists/lookup: a TMDb list as TMDb describes it.
type ListLookup struct {
	TmdbID      int64  `json:"tmdb_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Movies are the list's movies; other items (series) are never imported.
	Movies int `json:"movies"`
	// Added: this TMDb list is already an import list.
	Added bool `json:"added"`
}

// ListSynced answers POST /lists/{id}/sync.
type ListSynced struct {
	List MovieList `json:"list"`
	// Added are the movies added now; Skipped the items left alone (already in the library, excluded, not a movie).
	Added   int `json:"added"`
	Skipped int `json:"skipped"`
	// SyncError: the sync failed, in whole or in part; the scheduled sync of an enabled list retries.
	SyncError string `json:"sync_error,omitempty"`
}

func (w *Web) mountListsAPI(mux *http.ServeMux) {
	b := APIBase + "/lists"
	w.handle(mux, "GET "+b, SignedIn, auth.PermManage, w.apiLists)
	w.handle(mux, "POST "+b, SignedIn, auth.PermManage, w.apiAddList)
	w.handle(mux, "GET "+b+"/lookup", SignedIn, auth.PermManage, w.apiLookupList)
	w.handle(mux, "PUT "+b+"/{id}", SignedIn, auth.PermManage, w.apiSetList)
	w.handle(mux, "DELETE "+b+"/{id}", SignedIn, auth.PermManage, w.apiDeleteList)
	w.handle(mux, "POST "+b+"/{id}/sync", SignedIn, auth.PermManage, w.apiSyncList)
}

// movieLibraries are the libraries a list made here may add to: every movie version, as an add offers them
// (apiAddOptions); intake then picks the ones that fit each movie.
func (w *Web) movieLibraries() []string {
	out := []string{}
	if w.pol == nil {
		return out
	}
	for k, vp := range w.pol.Versions {
		if vp.Kind == policy.KindMovie {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// listLibraries are the libraries a list of view may add to: view's movie versions, or all of them without a view.
func (w *Web) listLibraries(view string) []string {
	all := w.movieLibraries()
	if view == "" || w.pol == nil {
		return all
	}
	var served []string
	if k, ok := strings.CutPrefix(view, "v/"); ok {
		served = []string{k}
	} else if v := w.pol.Views[view]; v != nil {
		served = v.Versions
	}
	out := []string{}
	for _, k := range all {
		if slices.Contains(served, k) {
			out = append(out, k)
		}
	}
	return out
}

const listColumns = `id, name, list_type, tmdb_id, enabled, enable_auto, monitor, search_on_add, versions,
	minimum_availability, last_sync_at, view`

func (w *Web) scanList(row interface{ Scan(...any) error }) (MovieList, error) {
	var l MovieList
	var versions string
	var synced sql.NullInt64
	if err := row.Scan(&l.ID, &l.Name, &l.ListType, &l.TmdbID, &l.Enabled, &l.EnableAuto, &l.Monitor, &l.SearchOnAdd,
		&versions, &l.MinimumAvailability, &synced, &l.View); err != nil {
		return l, err
	}
	if err := json.Unmarshal([]byte(versions), &l.Versions); err != nil {
		return l, err
	}
	if l.Versions == nil {
		l.Versions = []string{}
	}
	if synced.Valid {
		t := store.FromMillis(synced.Int64)
		l.LastSyncAt = &t
	}
	l.Libraries = w.listLibraries(l.View)
	return l, nil
}

// list reads one import list; sql.ErrNoRows when there is none.
func (w *Web) list(ctx context.Context, id int64) (MovieList, error) {
	return w.scanList(w.st.Reader().QueryRowContext(ctx, `SELECT `+listColumns+` FROM movie_import_list WHERE id = ?`, id))
}

func (w *Web) apiLists(rw http.ResponseWriter, r *http.Request) {
	out := ListsPage{Lists: []MovieList{}, Libraries: w.keyLabels(w.movieLibraries()), CanSync: w.add.Collections != nil}
	err := scanAll(r.Context(), w.st.Reader(), `SELECT `+listColumns+` FROM movie_import_list ORDER BY name COLLATE NOCASE, id`,
		nil, func(rows *sql.Rows) error {
			l, err := w.scanList(rows)
			if err == nil {
				out.Lists = append(out.Lists, l)
			}
			return err
		})
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, out)
}

// listExists reports whether the TMDb list is already an import list (of any view): a TMDb list is added once.
func (w *Web) listExists(ctx context.Context, tmdbID int64) (bool, error) {
	var exists bool
	err := w.st.Reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM movie_import_list WHERE list_type = 'tmdb'
		AND tmdb_id = ?)`, tmdbID).Scan(&exists)
	return exists, err
}

// GET /lists/lookup?tmdb_id=: the list's name, description and movie count on TMDb, to confirm it before adding.
func (w *Web) apiLookupList(rw http.ResponseWriter, r *http.Request) {
	if w.noTMDb(rw) {
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("tmdb_id"), 10, 64)
	if err != nil || id <= 0 {
		badRequest(rw, "tmdb_id must be a TMDb list id")
		return
	}
	ctx := r.Context()
	l, err := w.add.Collections.List(ctx, int(id))
	if errors.Is(err, tmdb.ErrNotFound) {
		writeJSON(rw, http.StatusNotFound, apiError{Error: "TMDb has no public list " + strconv.FormatInt(id, 10)})
		return
	}
	if err != nil {
		w.log.Warn("lists: TMDb list", "list", id, "err", err)
		writeJSON(rw, http.StatusBadGateway, apiError{Error: "TMDb did not answer; try again in a moment"})
		return
	}
	out := ListLookup{TmdbID: id, Name: l.Name, Description: shortText(l.Description, overviewShort)}
	for _, m := range l.Items {
		if m.ID > 0 && (m.MediaType == "" || m.MediaType == "movie") {
			out.Movies++
		}
	}
	if out.Added, err = w.listExists(ctx, id); err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, out)
}

func validListMonitor(v string) bool {
	return slices.Contains([]string{"movieOnly", "movieAndCollection", "none"}, v)
}

// checkListVersions refuses keys outside allowed, and an empty choice: a list names the libraries it adds to.
func checkListVersions(keys, allowed []string, view string) (string, error) {
	for _, k := range keys {
		if !slices.Contains(allowed, k) {
			if view != "" {
				return "", errors.New(strconv.Quote(k) + " is not a movie library of the " + view + " view this list belongs to")
			}
			return "", errors.New(strconv.Quote(k) + " is not a movie library")
		}
	}
	if len(keys) == 0 {
		return "", errors.New("a list needs at least one library")
	}
	keys = slices.Clone(keys)
	slices.Sort(keys)
	b, err := json.Marshal(slices.Compact(keys))
	return string(b), err
}

// POST /lists: add a TMDb movie list. It belongs to no arr view.
func (w *Web) apiAddList(rw http.ResponseWriter, r *http.Request) {
	var in ListAdd
	if err := decodeJSON(r, &in); err != nil {
		badRequest(rw, "invalid body: "+err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Monitor == "" {
		in.Monitor = "movieOnly"
	}
	if in.MinimumAvailability == "" {
		in.MinimumAvailability = "released"
	}
	switch {
	case in.TmdbID <= 0:
		badRequest(rw, "tmdb_id is required")
		return
	case in.Name == "":
		badRequest(rw, "name is required")
		return
	case !validListMonitor(in.Monitor):
		badRequest(rw, "monitor must be movieOnly, movieAndCollection or none")
		return
	case !validAvailability(in.MinimumAvailability):
		badRequest(rw, "minimum_availability must be tba, announced, inCinemas or released")
		return
	}
	versions, err := checkListVersions(in.Versions, w.movieLibraries(), "")
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	ctx := r.Context()
	if exists, err := w.listExists(ctx, in.TmdbID); err != nil {
		w.internal(rw, err)
		return
	} else if exists {
		writeJSON(rw, http.StatusConflict, apiError{Error: "this TMDb list is already added"})
		return
	}
	enabled := in.Enabled == nil || *in.Enabled
	now := store.Millis(w.now())
	res, err := w.st.Writer().ExecContext(ctx, `INSERT INTO movie_import_list (name, list_type, tmdb_id, enabled, enable_auto,
		monitor, search_on_add, versions, minimum_availability, created_at, updated_at) VALUES (?, 'tmdb', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.TmdbID, enabled, enabled, in.Monitor, in.SearchOnAdd, versions, in.MinimumAvailability, now, now)
	if err != nil {
		w.internal(rw, err)
		return
	}
	id, _ := res.LastInsertId()
	w.writeList(rw, r, id, http.StatusCreated)
}

func (w *Web) writeList(rw http.ResponseWriter, r *http.Request, id int64, status int) {
	l, err := w.list(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, status, l)
}

// PUT /lists/{id}: change a list's name, enabled, monitor, search on add, minimum availability or libraries. The
// view, profile and root an arr client stored are never written, so its view keeps listing the list; on such a list
// enable_auto stays the client's, and on a list made here it follows enabled.
func (w *Web) apiSetList(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var in ListUpdate
	if err := decodeJSON(r, &in); err != nil {
		badRequest(rw, "invalid body: "+err.Error())
		return
	}
	ctx := r.Context()
	cur, err := w.list(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	set, args := []string{}, []any{}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			badRequest(rw, "name is required")
			return
		}
		set, args = append(set, "name = ?"), append(args, name)
	}
	if in.Enabled != nil {
		set, args = append(set, "enabled = ?"), append(args, *in.Enabled)
		if cur.View == "" {
			set, args = append(set, "enable_auto = ?"), append(args, *in.Enabled)
		}
	}
	if in.Monitor != nil {
		if !validListMonitor(*in.Monitor) {
			badRequest(rw, "monitor must be movieOnly, movieAndCollection or none")
			return
		}
		set, args = append(set, "monitor = ?"), append(args, *in.Monitor)
	}
	if in.SearchOnAdd != nil {
		set, args = append(set, "search_on_add = ?"), append(args, *in.SearchOnAdd)
	}
	if in.MinimumAvailability != nil {
		if !validAvailability(*in.MinimumAvailability) {
			badRequest(rw, "minimum_availability must be tba, announced, inCinemas or released")
			return
		}
		set, args = append(set, "minimum_availability = ?"), append(args, *in.MinimumAvailability)
	}
	if in.Versions != nil {
		versions, err := checkListVersions(*in.Versions, cur.Libraries, cur.View)
		if err != nil {
			badRequest(rw, err.Error())
			return
		}
		set, args = append(set, "versions = ?"), append(args, versions)
	}
	if len(set) > 0 {
		set, args = append(set, "updated_at = ?"), append(args, store.Millis(w.now()), id)
		if _, err := w.st.Writer().ExecContext(ctx, `UPDATE movie_import_list SET `+strings.Join(set, ", ")+
			` WHERE id = ?`, args...); err != nil {
			w.internal(rw, err)
			return
		}
	}
	w.writeList(rw, r, id, http.StatusOK)
}

// DELETE /lists/{id}: forget the list. The movies it added stay.
func (w *Web) apiDeleteList(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	res, err := w.st.Writer().ExecContext(r.Context(), `DELETE FROM movie_import_list WHERE id = ?`, id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		notFound(rw)
		return
	}
	rw.WriteHeader(http.StatusNoContent)
}

// POST /lists/{id}/sync: import the list now, enabled or not, as the scheduled sync does.
func (w *Web) apiSyncList(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	if w.noTMDb(rw) {
		return
	}
	ctx := r.Context()
	var out ListSynced
	res, err := w.add.Collections.SyncList(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.log.Warn("lists: sync", "list", id, "err", err)
		out.SyncError = "The list could not be synced in full now."
	}
	out.Added, out.Skipped = res.Added, res.Skipped
	if out.List, err = w.list(ctx, id); errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	} else if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, out)
}
