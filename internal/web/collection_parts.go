package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/metadata/art"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/store"
)

// CollectionDetail is GET /collections/{id}: one collection with its films as the last intake refresh stored them
// (no TMDb call), each with its place in the library, and what the Edit dialog needs.
type CollectionDetail struct {
	Collection Collection        `json:"collection"`
	Parts      []CollectionPart  `json:"parts"`
	Libraries  map[string]string `json:"libraries"`
	CanAdd     bool              `json:"can_add"`
}

// CollectionPart is one film of a collection, in release order (undated last).
type CollectionPart struct {
	TmdbID int64  `json:"tmdb_id"`
	Title  string `json:"title"`
	// Year is the release year; absent when TMDb has no date.
	Year     int    `json:"year,omitempty"`
	Poster   string `json:"poster,omitempty"`
	Overview string `json:"overview,omitempty"`
	// TitleID is the film's title in the library (a visible version); absent when it is not in the library.
	TitleID int64 `json:"title_id,omitempty"`
	// Availability is the film's status word, as the lists say it; absent when it is not in the library.
	Availability string `json:"availability,omitempty"`
	// Removed: the film was removed from every library (Collection.Removed counts it); an Add brings it back.
	Removed bool `json:"removed,omitempty"`
}

// GET /collections/{id}: one stored collection and its films. Reads the collection's row once and only its own
// films' titles.
func (w *Web) apiCollection(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	ctx := r.Context()
	c := Collection{TmdbID: id}
	var versions, movies string
	err := w.st.Reader().QueryRowContext(ctx, `SELECT name, monitored, versions, search_on_add, minimum_availability,
		movies FROM movie_collection WHERE tmdb_id = ?`, id).Scan(&c.Name, &c.Monitored, &versions, &c.SearchOnAdd,
		&c.MinimumAvailability, &movies)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(rw)
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	var stored []tmdb.MovieSummary
	if err := json.Unmarshal([]byte(movies), &stored); err != nil {
		w.internal(rw, err)
		return
	}
	slices.SortStableFunc(stored, func(a, b tmdb.MovieSummary) int {
		return strings.Compare(cmpDate(a.ReleaseDate), cmpDate(b.ReleaseDate))
	})
	targets := w.collectionTargets()
	out := CollectionDetail{Parts: []CollectionPart{}, Libraries: w.keyLabels(targets), CanAdd: w.add.Collections != nil}
	seen := map[int]bool{}
	tmdbIDs := []any{}
	for _, p := range stored {
		if p.ID <= 0 || seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		tmdbIDs = append(tmdbIDs, p.ID)
		part := CollectionPart{TmdbID: int64(p.ID), Title: p.Title, Overview: shortText(p.Overview, overviewShort)}
		if len(p.ReleaseDate) >= 4 {
			part.Year, _ = strconv.Atoi(p.ReleaseDate[:4])
		}
		if f := strings.TrimPrefix(p.PosterPath, "/"); art.FileRE.MatchString(f) {
			part.Poster = "/img/tmdb/" + tmdbPosterSize + "/" + f
		}
		out.Parts = append(out.Parts, part)
	}
	// How the catalog holds these films (as heldMovies, for this collection's films only), and their titles.
	held, titles := map[int]heldMovie{}, map[int64]int64{}
	if len(tmdbIDs) > 0 {
		err = scanAll(ctx, w.st.Reader(), `SELECT t.tmdb_id, MIN(t.id), MAX(`+liveVersion+`), MAX(`+liveVersion+` AND `+
			hasFileSQL+`) FROM title t JOIN version v ON v.title_id = t.id
			WHERE t.kind = 'movie' AND t.tmdb_id IN (`+store.Placeholders(len(tmdbIDs))+`) GROUP BY t.tmdb_id`, tmdbIDs,
			func(rows *sql.Rows) error {
				var m, tid int64
				var h heldMovie
				err := rows.Scan(&m, &tid, &h.live, &h.file)
				held[int(m)] = h
				if h.live {
					titles[m] = tid
				}
				return err
			})
		if err != nil {
			w.internal(rw, err)
			return
		}
	}
	if out.Collection, err = buildCollection(c, versions, movies, held, targets); err != nil {
		w.internal(rw, err)
		return
	}
	ids := []any{}
	for _, tid := range titles {
		ids = append(ids, tid)
	}
	avail, err := w.titleAvailability(ctx, w.st.Reader(), ids)
	if err != nil {
		w.internal(rw, err)
		return
	}
	for i := range out.Parts {
		p := &out.Parts[i]
		if tid, ok := titles[p.TmdbID]; ok {
			p.TitleID, p.Availability = tid, avail[tid]
		} else if _, ok := held[int(p.TmdbID)]; ok {
			p.Removed = true
		}
	}
	writeJSON(rw, http.StatusOK, out)
}
