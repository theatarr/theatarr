package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Lists (docs/design/arrview.md, Lists): the import_list rows movie intake syncs (internal/catalog MovieIntake), for
// the Lists tab of the Collections page. A list is a TMDb movie list, or the titles of a Radarr or Sonarr instance.
// A list made here has no view, so no arr view lists it; a list an arr client made (TMDb only) keeps its view, profile
// and root whatever is changed here, so that client still sees it. An enabled TMDb list adds its new movies at every
// intake refresh (metadata.lists.interval), a Radarr or Sonarr list every catalog.ArrListInterval, and any list on
// "Sync now". The API key of an instance is stored on its row and never answered.

// MovieList is one import list as movie intake keeps it.
type MovieList struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// ListType: tmdb, radarr or sonarr.
	ListType string `json:"list_type"`
	// Kind is what the list adds: movie, or series for a Sonarr list.
	Kind   string `json:"kind"`
	TmdbID int64  `json:"tmdb_id"`
	// URL is a Radarr or Sonarr list's instance address; APIKeySet says its API key is stored (it is never answered).
	URL       string `json:"url,omitempty"`
	APIKeySet bool   `json:"api_key_set"`
	// Enabled: the scheduled sync imports this list. For a list an arr client made, it also needs EnableAuto, which
	// only that client changes.
	Enabled    bool `json:"enabled"`
	EnableAuto bool `json:"enable_auto"`
	// Monitor: movieOnly, movieAndCollection (also monitors the movies' collections; TMDb lists) or none (added
	// unmonitored); a Sonarr list all (added monitored, seasons as Theatarr monitors a new series) or none.
	Monitor             string     `json:"monitor"`
	SearchOnAdd         bool       `json:"search_on_add"`
	Versions            []string   `json:"versions"`
	MinimumAvailability string     `json:"minimum_availability"`
	LastSyncAt          *time.Time `json:"last_sync_at,omitempty"`
	// View is the arr view whose client made the list; empty for a list made here.
	View string `json:"view,omitempty"`
	// Libraries are the libraries this list may add to: every movie library (every series library for a Sonarr
	// list), or, with a view, that view's.
	Libraries []string `json:"libraries"`
}

// ListsPage is GET /lists: every import list, and the movie libraries a list may add to.
type ListsPage struct {
	Lists     []MovieList       `json:"lists"`
	Libraries map[string]string `json:"libraries"`
	// SeriesLibraries are the series libraries a Sonarr list may add to.
	SeriesLibraries map[string]string `json:"series_libraries"`
	// CanSync: TMDb is set up, so lists can be looked up and synced.
	CanSync bool `json:"can_sync"`
}

// ListAdd is POST /lists.
type ListAdd struct {
	// ListType: tmdb (default), radarr or sonarr. A TMDb list takes TmdbID; a Radarr or Sonarr list URL and APIKey.
	ListType string   `json:"list_type"`
	URL      string   `json:"url"`
	APIKey   string   `json:"api_key"`
	TmdbID   int64    `json:"tmdb_id"`
	Name     string   `json:"name"`
	Versions []string `json:"versions"`
	// Monitor defaults to movieOnly (all for a Sonarr list), MinimumAvailability to released, Enabled to true.
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
	// URL changes a Radarr or Sonarr list's address; a new address needs APIKey too. An empty APIKey keeps the stored
	// one.
	URL    *string `json:"url"`
	APIKey *string `json:"api_key"`
}

// ListCheck is POST /lists/check: an instance by list_type, url and api_key, or a stored list by id.
type ListCheck struct {
	ID       int64  `json:"id"`
	ListType string `json:"list_type"`
	URL      string `json:"url"`
	APIKey   string `json:"api_key"`
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
	w.handle(mux, "POST "+b+"/check", SignedIn, auth.PermManage, w.apiCheckList)
	w.handle(mux, "POST "+b+"/{id}/preview", SignedIn, auth.PermManage, w.apiPreviewList)
}

// movieLibraries are the libraries a list made here may add to: every movie version, as an add offers them
// (apiAddOptions); intake then picks the ones that fit each movie.
func (w *Web) movieLibraries() []string { return w.kindLibraries(policy.KindMovie) }

func (w *Web) kindLibraries(kind policy.Kind) []string {
	out := []string{}
	if w.pol == nil {
		return out
	}
	for k, vp := range w.pol.Versions {
		if vp.Kind == kind {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// listLibraries are the libraries a list of view may add to: view's movie versions, or all of them without a view;
// every series library for a Sonarr list.
func (w *Web) listLibraries(view, listType string) []string {
	if listType == "sonarr" {
		return w.kindLibraries(policy.KindSeries)
	}
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

const listColumns = `id, name, list_type, tmdb_id, url, api_key <> '', enabled, enable_auto, monitor, search_on_add,
	versions, minimum_availability, last_sync_at, view`

func (w *Web) scanList(row interface{ Scan(...any) error }) (MovieList, error) {
	var l MovieList
	var versions string
	var synced sql.NullInt64
	if err := row.Scan(&l.ID, &l.Name, &l.ListType, &l.TmdbID, &l.URL, &l.APIKeySet, &l.Enabled, &l.EnableAuto, &l.Monitor, &l.SearchOnAdd,
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
	l.Kind = string(policy.KindMovie)
	if l.ListType == "sonarr" {
		l.Kind = string(policy.KindSeries)
	}
	l.Libraries = w.listLibraries(l.View, l.ListType)
	return l, nil
}

// list reads one import list; sql.ErrNoRows when there is none.
func (w *Web) list(ctx context.Context, id int64) (MovieList, error) {
	return w.scanList(w.st.Reader().QueryRowContext(ctx, `SELECT `+listColumns+` FROM import_list WHERE id = ?`, id))
}

func (w *Web) apiLists(rw http.ResponseWriter, r *http.Request) {
	out := ListsPage{Lists: []MovieList{}, Libraries: w.keyLabels(w.movieLibraries()),
		SeriesLibraries: w.keyLabels(w.kindLibraries(policy.KindSeries)), CanSync: w.add.Collections != nil}
	err := scanAll(r.Context(), w.st.Reader(), `SELECT `+listColumns+` FROM import_list ORDER BY name COLLATE NOCASE, id`,
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

// listExists reports whether the TMDb list (of any view), or the Radarr or Sonarr instance at the normalised
// address u, is already an import list: each is added once.
func (w *Web) listExists(ctx context.Context, listType string, tmdbID int64, u string) (bool, error) {
	var exists bool
	q, args := `SELECT EXISTS(SELECT 1 FROM import_list WHERE list_type = 'tmdb' AND tmdb_id = ?)`, []any{tmdbID}
	if listType != "tmdb" {
		q, args = `SELECT EXISTS(SELECT 1 FROM import_list WHERE list_type = ? AND url = ?)`, []any{listType, u}
	}
	err := w.st.Reader().QueryRowContext(ctx, q, args...).Scan(&exists)
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
	if out.Added, err = w.listExists(ctx, "tmdb", id, ""); err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, out)
}

// listMonitors are the monitor values of each list type.
var listMonitors = map[string][]string{
	"tmdb":   {"movieOnly", "movieAndCollection", "none"},
	"radarr": {"movieOnly", "none"},
	"sonarr": {"all", "none"},
}

func checkListMonitor(listType, v string) error {
	if !slices.Contains(listMonitors[listType], v) {
		return errors.New("monitor must be one of " + strings.Join(listMonitors[listType], ", ") + " for a " + listType + " list")
	}
	return nil
}

// normalListURL checks a Radarr or Sonarr list's address and writes it one way (no surrounding space or trailing
// slash, lower-case scheme and host), so the same instance is the same address. An IP-literal host must be a LAN
// address, as for import.arr: the list's key goes only there.
func normalListURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) {
		return "", errors.New("url must be an http or https address")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("url must not carry credentials, a query or a fragment")
	}
	if err := arr.CheckLiteral(u.Hostname()); err != nil {
		return "", errors.New("url: " + err.Error())
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	u.Path, u.RawPath = strings.TrimRight(u.Path, "/"), ""
	return u.String(), nil
}

// checkListVersions refuses keys outside allowed, and an empty choice: a list names the libraries it adds to.
func checkListVersions(keys, allowed []string, view, kind string) (string, error) {
	for _, k := range keys {
		if !slices.Contains(allowed, k) {
			if view != "" {
				return "", errors.New(strconv.Quote(k) + " is not a " + kind + " library of the " + view + " view this list belongs to")
			}
			return "", errors.New(strconv.Quote(k) + " is not a " + kind + " library")
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

// POST /lists: add a TMDb movie list, or a Radarr or Sonarr instance as a list. It belongs to no arr view.
func (w *Web) apiAddList(rw http.ResponseWriter, r *http.Request) {
	var in ListAdd
	if err := decodeJSON(r, &in); err != nil {
		badRequest(rw, "invalid body: "+err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.ListType == "" {
		in.ListType = "tmdb"
	}
	if in.Monitor == "" {
		in.Monitor = "movieOnly"
		if in.ListType == "sonarr" {
			in.Monitor = "all"
		}
	}
	if in.MinimumAvailability == "" {
		in.MinimumAvailability = "released"
	}
	arrList := in.ListType == "radarr" || in.ListType == "sonarr"
	switch {
	case !arrList && in.ListType != "tmdb":
		badRequest(rw, "list_type must be tmdb, radarr or sonarr")
		return
	case !arrList && in.TmdbID <= 0:
		badRequest(rw, "tmdb_id is required")
		return
	case !arrList && (in.URL != "" || in.APIKey != ""):
		badRequest(rw, "a TMDb list takes no url or api_key")
		return
	case arrList && in.TmdbID != 0:
		badRequest(rw, "a "+in.ListType+" list takes no tmdb_id")
		return
	case arrList && strings.TrimSpace(in.APIKey) == "":
		badRequest(rw, "api_key is required")
		return
	case in.Name == "":
		badRequest(rw, "name is required")
		return
	case !validAvailability(in.MinimumAvailability):
		badRequest(rw, "minimum_availability must be tba, announced, inCinemas or released")
		return
	}
	if err := checkListMonitor(in.ListType, in.Monitor); err != nil {
		badRequest(rw, err.Error())
		return
	}
	if arrList {
		u, err := normalListURL(in.URL)
		if err != nil {
			badRequest(rw, err.Error())
			return
		}
		in.URL, in.APIKey = u, strings.TrimSpace(in.APIKey)
	}
	kind := "movie"
	if in.ListType == "sonarr" {
		kind = "series"
	}
	versions, err := checkListVersions(in.Versions, w.listLibraries("", in.ListType), "", kind)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	ctx := r.Context()
	if exists, err := w.listExists(ctx, in.ListType, in.TmdbID, in.URL); err != nil {
		w.internal(rw, err)
		return
	} else if exists && arrList {
		writeJSON(rw, http.StatusConflict, apiError{Error: "this instance is already a list"})
		return
	} else if exists {
		writeJSON(rw, http.StatusConflict, apiError{Error: "this TMDb list is already added"})
		return
	}
	enabled := in.Enabled == nil || *in.Enabled
	now := store.Millis(w.now())
	res, err := w.st.Writer().ExecContext(ctx, `INSERT INTO import_list (name, list_type, tmdb_id, url, api_key, enabled,
		enable_auto, monitor, search_on_add, versions, minimum_availability, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.ListType, in.TmdbID, in.URL, in.APIKey, enabled, enabled, in.Monitor, in.SearchOnAdd, versions,
		in.MinimumAvailability, now, now)
	if err != nil {
		w.internal(rw, errors.New(arr.Mask(err.Error(), in.APIKey)))
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

// PUT /lists/{id}: change a list's name, enabled, monitor, search on add, minimum availability or libraries, and a
// Radarr or Sonarr list's address and API key. The
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
		if err := checkListMonitor(cur.ListType, *in.Monitor); err != nil {
			badRequest(rw, err.Error())
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
		versions, err := checkListVersions(*in.Versions, cur.Libraries, cur.View, cur.Kind)
		if err != nil {
			badRequest(rw, err.Error())
			return
		}
		set, args = append(set, "versions = ?"), append(args, versions)
	}
	key := ""
	if in.APIKey != nil {
		key = strings.TrimSpace(*in.APIKey)
	}
	if (in.URL != nil || key != "") && cur.ListType == "tmdb" {
		badRequest(rw, "a TMDb list takes no url or api_key")
		return
	}
	if in.URL != nil {
		u, err := normalListURL(*in.URL)
		if err != nil {
			badRequest(rw, err.Error())
			return
		}
		if u != cur.URL {
			// The stored key is never sent to a new address.
			if key == "" {
				badRequest(rw, "enter the API key again for a new address")
				return
			}
			if exists, err := w.listExists(ctx, cur.ListType, 0, u); err != nil {
				w.internal(rw, err)
				return
			} else if exists {
				writeJSON(rw, http.StatusConflict, apiError{Error: "this instance is already a list"})
				return
			}
			set, args = append(set, "url = ?"), append(args, u)
		}
	}
	if key != "" {
		set, args = append(set, "api_key = ?"), append(args, key)
	}
	if len(set) > 0 {
		set, args = append(set, "updated_at = ?"), append(args, store.Millis(w.now()), id)
		if _, err := w.st.Writer().ExecContext(ctx, `UPDATE import_list SET `+strings.Join(set, ", ")+
			` WHERE id = ?`, args...); err != nil {
			w.internal(rw, errors.New(arr.Mask(err.Error(), key)))
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
	res, err := w.st.Writer().ExecContext(r.Context(), `DELETE FROM import_list WHERE id = ?`, id)
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

// POST /lists/check: read an instance's status and titles, by list_type, url and api_key, or a stored list's by id.
// 502 with the client's error (the key masked) when it does not answer as the right app.
func (w *Web) apiCheckList(rw http.ResponseWriter, r *http.Request) {
	var in ListCheck
	if err := decodeJSON(r, &in); err != nil {
		badRequest(rw, "invalid body: "+err.Error())
		return
	}
	li, ok := w.listIntake(rw)
	if !ok {
		return
	}
	ctx := r.Context()
	if in.ID > 0 {
		err := w.st.Reader().QueryRowContext(ctx, `SELECT list_type, url, api_key FROM import_list WHERE id = ?`, in.ID).
			Scan(&in.ListType, &in.URL, &in.APIKey)
		if errors.Is(err, sql.ErrNoRows) {
			notFound(rw)
			return
		}
		if err != nil {
			w.internal(rw, err)
			return
		}
	}
	in.APIKey = strings.TrimSpace(in.APIKey)
	if in.ListType != "radarr" && in.ListType != "sonarr" {
		badRequest(rw, "list_type must be radarr or sonarr")
		return
	}
	u, err := normalListURL(in.URL)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	if in.APIKey == "" {
		badRequest(rw, "api_key is required")
		return
	}
	out, err := li.CheckArrList(ctx, in.ListType, u, in.APIKey)
	if err != nil {
		writeJSON(rw, http.StatusBadGateway, apiError{Error: arr.Mask(err.Error(), in.APIKey)})
		return
	}
	writeJSON(rw, http.StatusOK, out)
}

// POST /lists/{id}/preview: what Sync now would add, without writing anything.
func (w *Web) apiPreviewList(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	li, ok := w.listIntake(rw)
	if !ok {
		return
	}
	out, err := li.PreviewList(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.log.Warn("lists: preview", "list", id, "err", err)
		writeJSON(rw, http.StatusBadGateway, apiError{Error: "The list could not be read: " + err.Error()})
		return
	}
	writeJSON(rw, http.StatusOK, out)
}

// listIntake is the part of the movie intake (catalog.MovieIntake, which every MovieIntake the web layer is given
// embeds) that previews lists and checks Radarr and Sonarr instances.
type listIntake interface {
	PreviewList(ctx context.Context, id int64) (catalog.ListPreview, error)
	CheckArrList(ctx context.Context, listType, url, key string) (catalog.ArrCheck, error)
}

// listIntake is the intake's listIntake; false (503 answered) when TMDb is not set up.
func (w *Web) listIntake(rw http.ResponseWriter) (listIntake, bool) {
	if w.noTMDb(rw) {
		return nil, false
	}
	li, ok := w.add.Collections.(listIntake)
	if !ok {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "lists cannot be read now"})
	}
	return li, ok
}

// ListPreview answers POST /lists/{id}/preview.
type ListPreview = catalog.ListPreview

// ListCheckResult answers POST /lists/check.
type ListCheckResult = catalog.ArrCheck
