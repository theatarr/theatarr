package web

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/theatarr/theatarr/internal/metadata/art"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/store"
)

// GET /search: the search overlay and page. The library part is local and instant; the TMDb part goes through the
// gateway and the tmdb budget (tmdb.Client.SearchMulti, cached a day), with posters through /img/tmdb so the browser
// never talks to TMDb.

// SearchHit is one TMDb result.
type SearchHit struct {
	MediaType string `json:"media_type"` // movie or tv
	Kind      string `json:"kind"`       // movie or series
	TMDbID    int64  `json:"tmdb_id"`
	Name      string `json:"name"`
	Original  string `json:"original_name,omitempty"`
	Year      int    `json:"year,omitempty"`
	Overview  string `json:"overview,omitempty"`
	// Poster is a URL on this origin (/img/tmdb/w342/<file>); absent when TMDb has none.
	Poster     string   `json:"poster,omitempty"`
	PosterPath string   `json:"poster_path,omitempty"`
	Rating     *float64 `json:"rating,omitempty"`
	// Anime is the starting guess for an add (Japanese-language animation).
	Anime bool `json:"anime"`
	// TitleID is the library title with this TMDb id; InLibrary marks it.
	InLibrary bool           `json:"in_library"`
	TitleID   *int64         `json:"title_id,omitempty"`
	Versions  []VersionBrief `json:"versions,omitempty"`
	// Requested: an open request (pending, approved or processing) asks for this title; RequestStatus is its state.
	Requested     bool   `json:"requested"`
	RequestStatus string `json:"request_status,omitempty"`
}

// SearchAnswer is GET /search.
type SearchAnswer struct {
	Query        string      `json:"query"`
	Library      []TitleRow  `json:"library"`
	LibraryTotal int         `json:"library_total"`
	TMDb         []SearchHit `json:"tmdb"`
	// TMDbState is ok, off (no key), short (query under two characters), skipped (scope=library, or a later page
	// of the library: offset > 0) or error.
	TMDbState string `json:"tmdb_state"`
	TMDbError string `json:"tmdb_error,omitempty"`
}

// tmdbPosterSize is the poster size of search results.
const tmdbPosterSize = "w342"

// GET /search?q=&kind=movie|series&scope=all|library|tmdb&limit=&offset=: titles in the library whose name matches
// (kind filtered before the limit; offset pages them, library_total counts them all), and TMDb movies and series,
// each marked when the library has it.
func (w *Web) apiSearch(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	qv := r.URL.Query()
	q := strings.Join(strings.Fields(qv.Get("q")), " ")
	kind := qv.Get("kind")
	if kind == "tv" {
		kind = "series"
	}
	if !titleKinds[kind] {
		badRequest(rw, "kind must be movie or series")
		return
	}
	scope := qv.Get("scope")
	if scope == "" {
		scope = "all"
	}
	if scope != "all" && scope != "library" && scope != "tmdb" {
		badRequest(rw, "scope must be all, library or tmdb")
		return
	}
	limit, err := intParam(r, "limit", 12)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	limit = min(max(limit, 1), 50)
	offset, err := intParam(r, "offset", 0)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	if utf8.RuneCountInString(q) > 200 {
		badRequest(rw, "q is too long")
		return
	}
	out := SearchAnswer{Query: q, Library: []TitleRow{}, TMDb: []SearchHit{}, TMDbState: "skipped"}
	if q == "" {
		writeJSON(rw, http.StatusOK, out)
		return
	}
	if scope != "tmdb" {
		page, err := w.listTitles(ctx, TitleFilter{Kind: kind, Q: q, Limit: limit, Offset: offset})
		if err != nil {
			w.internal(rw, err)
			return
		}
		for i := range page.Items {
			redactRowFor(w.permsOf(r), &page.Items[i])
		}
		out.Library, out.LibraryTotal = page.Items, page.Total
	}
	if scope != "library" && offset == 0 {
		switch {
		case utf8.RuneCountInString(q) < 2:
			out.TMDbState = "short"
		case w.add.TMDb == nil:
			out.TMDbState = "off"
		default:
			hits, err := w.tmdbSearch(ctx, q, kind, limit)
			if err != nil {
				w.log.Warn("search: TMDb", "err", err)
				out.TMDbState, out.TMDbError = "error", "TMDb search failed; try again in a moment"
			} else {
				out.TMDb, out.TMDbState = hits, "ok"
				if !w.permsOf(r).SeeFiles {
					for i := range hits {
						for j := range hits[i].Versions {
							redactVersionBrief(&hits[i].Versions[j])
						}
					}
				}
			}
		}
	}
	writeJSON(rw, http.StatusOK, out)
}

func (w *Web) tmdbSearch(ctx context.Context, q, kind string, limit int) ([]SearchHit, error) {
	res, err := w.add.TMDb.SearchMulti(ctx, q)
	if err != nil {
		return nil, err
	}
	hits := []SearchHit{}
	for i := range res {
		x := &res[i]
		h := SearchHit{MediaType: x.MediaType, Kind: "movie", TMDbID: int64(x.ID), Name: x.DisplayName(),
			Year: x.Year(), Overview: shortText(x.Overview, overviewShort), Anime: x.LooksAnime()}
		if x.MediaType == "tv" {
			h.Kind = "series"
		}
		if kind != "" && h.Kind != kind {
			continue
		}
		if o := x.OriginalTitle + x.OriginalName; o != h.Name {
			h.Original = o
		}
		if f := strings.TrimPrefix(x.PosterPath, "/"); art.FileRE.MatchString(f) {
			h.Poster, h.PosterPath = "/img/tmdb/"+tmdbPosterSize+"/"+f, x.PosterPath
		}
		if x.VoteCount > 0 {
			v := x.VoteAverage
			h.Rating = &v
		}
		hits = append(hits, h)
		if len(hits) == limit {
			break
		}
	}
	if err := w.markInLibrary(ctx, hits); err != nil {
		return nil, err
	}
	return hits, w.markRequested(ctx, hits)
}

// markRequested marks the hits an open request asks for.
func (w *Web) markRequested(ctx context.Context, hits []SearchHit) error {
	if len(hits) == 0 {
		return nil
	}
	args := make([]any, 0, len(hits))
	for _, h := range hits {
		args = append(args, h.TMDbID)
	}
	return scanAll(ctx, w.st.Reader(), `SELECT kind, tmdb_id, status FROM request WHERE status IN ('pending', 'approved',
		'processing') AND tmdb_id IN (`+store.Placeholders(len(args))+`) ORDER BY id`, args, func(r *sql.Rows) error {
		var kind, status string
		var id int64
		if err := r.Scan(&kind, &id, &status); err != nil {
			return err
		}
		for i := range hits {
			if hits[i].Kind == kind && hits[i].TMDbID == id && !hits[i].Requested {
				hits[i].Requested, hits[i].RequestStatus = true, status
			}
		}
		return nil
	})
}

// markInLibrary marks the hits the library has: by the title's TMDb id, or the TMDb id the art job found for it.
func (w *Web) markInLibrary(ctx context.Context, hits []SearchHit) error {
	if len(hits) == 0 {
		return nil
	}
	type key struct {
		kind string
		id   int64
	}
	idx := map[key][]int{}
	var args []any
	for i, h := range hits {
		k := key{h.Kind, h.TMDbID}
		if _, ok := idx[k]; !ok {
			args = append(args, h.TMDbID)
		}
		idx[k] = append(idx[k], i)
	}
	ph := store.Placeholders(len(args))
	byTitle := map[int64][]int{}
	var ids []any
	err := scanAll(ctx, w.st.Reader(), `SELECT t.id, t.kind, COALESCE(t.tmdb_id, m.tmdb_id) FROM title t
		LEFT JOIN title_meta m ON m.title_id = t.id WHERE t.tmdb_id IN (`+ph+`) OR m.tmdb_id IN (`+ph+`)`,
		append(append([]any{}, args...), args...), func(r *sql.Rows) error {
			var id, tm int64
			var k string
			if err := r.Scan(&id, &k, &tm); err != nil {
				return err
			}
			for _, i := range idx[key{k, tm}] {
				if hits[i].TitleID == nil {
					tid := id
					hits[i].TitleID, hits[i].InLibrary = &tid, true
					if _, ok := byTitle[id]; !ok {
						ids = append(ids, id)
					}
					byTitle[id] = append(byTitle[id], i)
				}
			}
			return nil
		})
	if err != nil || len(ids) == 0 {
		return err
	}
	return scanAll(ctx, w.st.Reader(), `SELECT v.title_id, v.id, v.policy_key, v.state, v.monitored, `+hasFileSQL+`
		FROM version v WHERE v.title_id IN (`+store.Placeholders(len(ids))+`) AND NOT EXISTS (SELECT 1 FROM
		arrview_removed_version r WHERE r.version_id = v.id) ORDER BY v.policy_key`, ids, func(r *sql.Rows) error {
		var tid int64
		var v VersionBrief
		if err := r.Scan(&tid, &v.ID, &v.Key, &v.State, &v.Monitored, &v.HasFile); err != nil {
			return err
		}
		for _, i := range byTitle[tid] {
			hits[i].Versions = append(hits[i].Versions, v)
		}
		return nil
	})
}

// compile-time check: the TMDb client satisfies TMDbClient.
var _ TMDbClient = (*tmdb.Client)(nil)
