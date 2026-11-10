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

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/metadata/art"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/store"
)

// Collections (docs/design/arrview.md, Collections): the movie_collection rows movie intake refreshes (internal/catalog
// MovieIntake), for the native Collections page. A collection is added by searching TMDb (GET /collections/search,
// POST /collections); a monitored one adds its missing movies to its target versions when it is added, on "Add
// missing movies now" (POST /collections/{id}/refresh) and at every intake refresh (metadata.lists.interval).
// Searches on add go through the acquirer and router like every other add, so nothing grabs while router.enabled is
// off. The same rows back the Radarr view's collection endpoints.

// Collection is one TMDb collection as movie intake keeps it.
type Collection struct {
	TmdbID    int64  `json:"tmdb_id"`
	Name      string `json:"name"`
	Monitored bool   `json:"monitored"`
	// Versions are the libraries a monitored collection adds movies to: collection targets only (policy
	// CollectionTarget: active movie versions nothing follows, the ones the library gate lets write). Stored targets
	// that are no longer eligible are left out; movie intake skips them.
	Versions            []string `json:"versions"`
	SearchOnAdd         bool     `json:"search_on_add"`
	MinimumAvailability string   `json:"minimum_availability"`
	// Poster is the first part's TMDb poster on this origin; absent before the first intake refresh.
	Poster string `json:"poster,omitempty"`
	// Held is the collection's TMDb movies the catalog has in a visible version (not removed from every view);
	// Total its movies on TMDb, known after the first intake refresh.
	Held  int `json:"held"`
	Total int `json:"total"`
}

// CollectionsPage is GET /collections: every known collection, and the libraries a collection may target.
type CollectionsPage struct {
	Collections []Collection      `json:"collections"`
	Libraries   map[string]string `json:"libraries"`
	// CanAdd: TMDb is set up, so collections can be searched, added and refreshed now.
	CanAdd bool `json:"can_add"`
}

// CollectionHit is one TMDb collection of GET /collections/search.
type CollectionHit struct {
	TmdbID   int64  `json:"tmdb_id"`
	Name     string `json:"name"`
	Poster   string `json:"poster,omitempty"`
	Overview string `json:"overview,omitempty"`
	// Added: the collection is already on the Collections page.
	Added bool `json:"added"`
}

// CollectionSearch is GET /collections/search.
type CollectionSearch struct {
	Results []CollectionHit `json:"results"`
}

// CollectionAdd is POST /collections.
type CollectionAdd struct {
	TmdbID   int64    `json:"tmdb_id"`
	Versions []string `json:"versions"`
	// Monitored defaults to true.
	Monitored   *bool `json:"monitored"`
	SearchOnAdd bool  `json:"search_on_add"`
	// MinimumAvailability defaults to released.
	MinimumAvailability string `json:"minimum_availability"`
}

// CollectionRefreshed answers POST /collections and POST /collections/{id}/refresh: the collection as stored and
// what the refresh did.
type CollectionRefreshed struct {
	Collection Collection `json:"collection"`
	// Added are the movies added to the library now; Skipped the parts left alone (already in the library, or
	// excluded).
	Added   int `json:"added"`
	Skipped int `json:"skipped"`
	// RefreshError: the collection is stored, but adding its movies failed in part; the scheduled refresh retries.
	RefreshError string `json:"refresh_error,omitempty"`
}

// CollectionUpdate is PUT /collections/{id}; absent fields keep their value.
type CollectionUpdate struct {
	Monitored           *bool     `json:"monitored"`
	Versions            *[]string `json:"versions"`
	SearchOnAdd         *bool     `json:"search_on_add"`
	MinimumAvailability *string   `json:"minimum_availability"`
}

func (w *Web) mountCollectionsAPI(mux *http.ServeMux) {
	b := APIBase + "/collections"
	w.handle(mux, "GET "+b, SignedIn, auth.PermManage, w.apiCollections)
	w.handle(mux, "POST "+b, SignedIn, auth.PermManage, w.apiAddCollection)
	w.handle(mux, "GET "+b+"/search", SignedIn, auth.PermManage, w.apiSearchCollections)
	w.handle(mux, "PUT "+b+"/{id}", SignedIn, auth.PermManage, w.apiSetCollection)
	w.handle(mux, "POST "+b+"/{id}/refresh", SignedIn, auth.PermManage, w.apiRefreshCollection)
	w.mountListsAPI(mux)
}

// collectionTargets are the libraries a collection may target (policy CollectionTargets), sorted.
func (w *Web) collectionTargets() []string {
	if w.pol == nil {
		return nil
	}
	return w.pol.CollectionTargets()
}

// heldMovies are the TMDb ids of the catalog's movies with a visible version: a movie removed from every view is
// not held.
func (w *Web) heldMovies(ctx context.Context) (map[int]bool, error) {
	held := map[int]bool{}
	err := scanAll(ctx, w.st.Reader(), `SELECT DISTINCT t.tmdb_id FROM title t JOIN version v ON v.title_id = t.id
		WHERE t.kind = 'movie' AND t.tmdb_id > 0 AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r
		WHERE r.version_id = v.id)`, nil, func(rows *sql.Rows) error {
		var m int
		err := rows.Scan(&m)
		held[m] = true
		return err
	})
	return held, err
}

func (w *Web) apiCollections(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// The catalog's collection memberships show up as unmonitored collections, as in the Radarr view.
	if err := catalog.DiscoverCollections(ctx, w.st, w.now()); err != nil {
		w.internal(rw, err)
		return
	}
	held, err := w.heldMovies(ctx)
	if err != nil {
		w.internal(rw, err)
		return
	}
	targets := w.collectionTargets()
	out := CollectionsPage{Collections: []Collection{}, Libraries: w.keyLabels(targets), CanAdd: w.add.Collections != nil}
	err = scanAll(ctx, w.st.Reader(), `SELECT tmdb_id, name, monitored, versions, search_on_add, minimum_availability,
		movies FROM movie_collection ORDER BY monitored DESC, name = '', name COLLATE NOCASE, tmdb_id`, nil,
		func(rows *sql.Rows) error {
			c, err := scanCollection(rows, held, targets)
			if err == nil {
				out.Collections = append(out.Collections, c)
			}
			return err
		})
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, out)
}

func scanCollection(row interface{ Scan(...any) error }, held map[int]bool, targets []string) (Collection, error) {
	var c Collection
	var versions, movies string
	if err := row.Scan(&c.TmdbID, &c.Name, &c.Monitored, &versions, &c.SearchOnAdd, &c.MinimumAvailability,
		&movies); err != nil {
		return c, err
	}
	var stored []string
	if err := json.Unmarshal([]byte(versions), &stored); err != nil {
		return c, err
	}
	c.Versions = eligible(stored, targets)
	var parts []tmdb.MovieSummary
	if err := json.Unmarshal([]byte(movies), &parts); err != nil {
		return c, err
	}
	// TMDb lists parts by release; undated ones (announced) go last.
	slices.SortStableFunc(parts, func(a, b tmdb.MovieSummary) int {
		return strings.Compare(cmpDate(a.ReleaseDate), cmpDate(b.ReleaseDate))
	})
	seen := map[int]bool{}
	for _, p := range parts {
		if p.ID <= 0 || seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		if held[p.ID] {
			c.Held++
		}
		if f := strings.TrimPrefix(p.PosterPath, "/"); c.Poster == "" && art.FileRE.MatchString(f) {
			c.Poster = "/img/tmdb/" + tmdbPosterSize + "/" + f
		}
	}
	c.Total = len(seen)
	return c, nil
}

// eligible keeps the keys that are collection targets, sorted and never nil.
func eligible(keys, targets []string) []string {
	out := []string{}
	for _, k := range keys {
		if slices.Contains(targets, k) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func cmpDate(d string) string {
	if d == "" {
		return "9999"
	}
	return d
}

func (w *Web) apiSetCollection(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var in CollectionUpdate
	if err := decodeJSON(r, &in); err != nil {
		badRequest(rw, "invalid body: "+err.Error())
		return
	}
	set, args := []string{}, []any{}
	if in.Monitored != nil {
		set, args = append(set, "monitored = ?"), append(args, *in.Monitored)
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
	targets := w.collectionTargets()
	if in.Versions != nil {
		for _, k := range *in.Versions {
			if !slices.Contains(targets, k) {
				badRequest(rw, strconv.Quote(k)+" is not a library a collection may add to (an active movie library nothing follows)")
				return
			}
		}
		b, _ := json.Marshal(eligible(*in.Versions, targets))
		set, args = append(set, "versions = ?"), append(args, string(b))
	}
	ctx := r.Context()
	// A monitored collection needs a library to add to: the one asked for, else the stored ones still eligible.
	var on bool
	var stored string
	err := w.st.Reader().QueryRowContext(ctx, `SELECT monitored, versions FROM movie_collection WHERE tmdb_id = ?`,
		id).Scan(&on, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	keys := []string{}
	if in.Versions != nil {
		keys = *in.Versions
	} else if err := json.Unmarshal([]byte(stored), &keys); err != nil {
		w.internal(rw, err)
		return
	}
	if in.Monitored != nil {
		on = *in.Monitored
	}
	if on && len(eligible(keys, targets)) == 0 {
		badRequest(rw, "a monitored collection needs at least one library")
		return
	}
	if len(set) > 0 {
		set, args = append(set, "updated_at = ?"), append(args, store.Millis(w.now()), id)
		res, err := w.st.Writer().ExecContext(ctx, `UPDATE movie_collection SET `+strings.Join(set, ", ")+
			` WHERE tmdb_id = ?`, args...)
		if err != nil {
			w.internal(rw, err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			notFound(rw)
			return
		}
	}
	c, err := w.collection(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, c)
}

// collection reads one stored collection; sql.ErrNoRows when it is not known.
func (w *Web) collection(ctx context.Context, id int64) (Collection, error) {
	held, err := w.heldMovies(ctx)
	if err != nil {
		return Collection{}, err
	}
	return scanCollection(w.st.Reader().QueryRowContext(ctx, `SELECT tmdb_id, name, monitored, versions,
		search_on_add, minimum_availability, movies FROM movie_collection WHERE tmdb_id = ?`, id), held,
		w.collectionTargets())
}

func validAvailability(v string) bool {
	return slices.Contains([]string{"tba", "announced", "inCinemas", "released"}, v)
}

// noTMDb answers 503 when TMDb is not set up; the Collections page hides its add and refresh actions then.
func (w *Web) noTMDb(rw http.ResponseWriter) bool {
	if w.add.Collections != nil {
		return false
	}
	writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "TMDb is not set up"})
	return true
}

// GET /collections/search?q=: TMDb collections by name, each marked when it is already on the page.
func (w *Web) apiSearchCollections(rw http.ResponseWriter, r *http.Request) {
	if w.noTMDb(rw) {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		badRequest(rw, "q is required")
		return
	}
	ctx := r.Context()
	res, err := w.add.Collections.SearchCollection(ctx, q)
	if err != nil {
		w.log.Warn("collections: TMDb search", "err", err)
		writeJSON(rw, http.StatusBadGateway, apiError{Error: "TMDb search failed; try again in a moment"})
		return
	}
	known := map[int64]bool{}
	err = scanAll(ctx, w.st.Reader(), `SELECT tmdb_id FROM movie_collection`, nil, func(rows *sql.Rows) error {
		var id int64
		err := rows.Scan(&id)
		known[id] = true
		return err
	})
	if err != nil {
		w.internal(rw, err)
		return
	}
	out := CollectionSearch{Results: []CollectionHit{}}
	for _, x := range res {
		h := CollectionHit{TmdbID: int64(x.ID), Name: x.Name, Overview: shortText(x.Overview, overviewShort), Added: known[int64(x.ID)]}
		if f := strings.TrimPrefix(x.PosterPath, "/"); art.FileRE.MatchString(f) {
			h.Poster = "/img/tmdb/" + tmdbPosterSize + "/" + f
		}
		out.Results = append(out.Results, h)
	}
	writeJSON(rw, http.StatusOK, out)
}

// POST /collections: add a TMDb collection with its name and parts, or update the one already known, and, when it
// is monitored, add its missing movies now.
func (w *Web) apiAddCollection(rw http.ResponseWriter, r *http.Request) {
	if w.noTMDb(rw) {
		return
	}
	var in CollectionAdd
	if err := decodeJSON(r, &in); err != nil {
		badRequest(rw, "invalid body: "+err.Error())
		return
	}
	if in.TmdbID <= 0 {
		badRequest(rw, "tmdb_id is required")
		return
	}
	if in.MinimumAvailability == "" {
		in.MinimumAvailability = "released"
	}
	if !validAvailability(in.MinimumAvailability) {
		badRequest(rw, "minimum_availability must be tba, announced, inCinemas or released")
		return
	}
	targets := w.collectionTargets()
	for _, k := range in.Versions {
		if !slices.Contains(targets, k) {
			badRequest(rw, strconv.Quote(k)+" is not a library a collection may add to (an active movie library nothing follows)")
			return
		}
	}
	versions := eligible(in.Versions, targets)
	monitored := in.Monitored == nil || *in.Monitored
	if monitored && len(versions) == 0 {
		badRequest(rw, "a monitored collection needs at least one library")
		return
	}
	ctx := r.Context()
	c, err := w.add.Collections.Collection(ctx, int(in.TmdbID))
	if errors.Is(err, tmdb.ErrNotFound) {
		writeJSON(rw, http.StatusNotFound, apiError{Error: "TMDb has no collection " + strconv.FormatInt(in.TmdbID, 10)})
		return
	}
	if err != nil {
		w.log.Warn("collections: TMDb collection", "collection", in.TmdbID, "err", err)
		writeJSON(rw, http.StatusBadGateway, apiError{Error: "TMDb did not answer; try again in a moment"})
		return
	}
	vb, _ := json.Marshal(versions)
	parts, err := json.Marshal(c.Parts)
	if err != nil {
		w.internal(rw, err)
		return
	}
	now := store.Millis(w.now())
	// A known collection keeps the view, profile and root an arr client gave it.
	if _, err := w.st.Writer().ExecContext(ctx, `INSERT INTO movie_collection (tmdb_id, name, movies, monitored, versions,
		minimum_availability, search_on_add, refreshed_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tmdb_id) DO UPDATE SET name = excluded.name, movies = excluded.movies, monitored = excluded.monitored,
		versions = excluded.versions, minimum_availability = excluded.minimum_availability,
		search_on_add = excluded.search_on_add, refreshed_at = excluded.refreshed_at, updated_at = excluded.updated_at`,
		in.TmdbID, c.Name, string(parts), monitored, string(vb), in.MinimumAvailability, in.SearchOnAdd, now, now); err != nil {
		w.internal(rw, err)
		return
	}
	w.refreshCollection(rw, r, in.TmdbID, monitored)
}

// POST /collections/{id}/refresh: reread one collection from TMDb and add a monitored one's missing movies now.
func (w *Web) apiRefreshCollection(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	if w.noTMDb(rw) {
		return
	}
	w.refreshCollection(rw, r, id, true)
}

// refreshCollection runs the one-collection refresh (when refresh is set) and answers the stored collection with
// what it did. A failed refresh of a stored collection still answers 200, with refresh_error.
func (w *Web) refreshCollection(rw http.ResponseWriter, r *http.Request, id int64, refresh bool) {
	ctx := r.Context()
	var out CollectionRefreshed
	if refresh {
		res, err := w.add.Collections.RefreshCollection(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			notFound(rw)
			return
		}
		if err != nil {
			w.log.Warn("collections: refresh", "collection", id, "err", err)
			out.RefreshError = "Some movies could not be added now; the next list refresh tries again."
		}
		out.Added, out.Skipped = res.Added, res.Skipped
	}
	var err error
	if out.Collection, err = w.collection(ctx, id); errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	} else if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, out)
}
