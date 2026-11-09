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
// MovieIntake), for the native Collections page. A monitored collection adds its missing movies to its target
// versions at the next intake refresh (metadata.lists.interval); searches on add go through the acquirer and router
// like every other add, so nothing grabs while router.enabled is off. The same rows back the Radarr view's
// collection endpoints.

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
	w.handle(mux, "PUT "+b+"/{id}", SignedIn, auth.PermManage, w.apiSetCollection)
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
	out := CollectionsPage{Collections: []Collection{}, Libraries: w.keyLabels(targets)}
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
		switch *in.MinimumAvailability {
		case "tba", "announced", "inCinemas", "released":
		default:
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
	held, err := w.heldMovies(ctx)
	if err != nil {
		w.internal(rw, err)
		return
	}
	c, err := scanCollection(w.st.Reader().QueryRowContext(ctx, `SELECT tmdb_id, name, monitored, versions,
		search_on_add, minimum_availability, movies FROM movie_collection WHERE tmdb_id = ?`, id), held, targets)
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
