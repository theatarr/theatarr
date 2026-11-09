package importarr

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/store"
)

// MovieSource is the TMDb lookup Enrich needs. *tmdb.Client satisfies it.
type MovieSource interface {
	Movie(ctx context.Context, id int) (*tmdb.Movie, error)
}

// EnrichSummary counts what Enrich did.
type EnrichSummary struct {
	// Candidates are movie titles missing an IMDb id, a year, TMDb release dates or TMDb names (alternative titles
	// and translations).
	Candidates int `json:"candidates"`
	Looked     int `json:"looked_up"`
	NotFound   int `json:"not_found"`
	Failed     int `json:"failed"`
	// Filled counts filled fields by name.
	Filled map[string]int `json:"filled"`
	// Disagreements are values TMDb has that differ from the stored ones (kept as stored).
	Disagreements []string `json:"disagreements,omitempty"`
	// Requests and CacheHits are the TMDb client's counters.
	Requests  int     `json:"requests"`
	CacheHits int     `json:"cache_hits"`
	Changes   Changes `json:"changes"`
}

// WriteText prints the summary.
func (e *EnrichSummary) WriteText(w io.Writer) {
	fmt.Fprintf(w, "enrich: %d candidates, %d looked up (%d TMDb requests, %d from cache), %d not found, %d failed\n",
		e.Candidates, e.Looked, e.Requests, e.CacheHits, e.NotFound, e.Failed)
	fmt.Fprintf(w, "  filled: %s\n", mapLine(e.Filled))
	for _, d := range e.Disagreements {
		fmt.Fprintf(w, "  differs: %s\n", d)
	}
	fmt.Fprintf(w, "  changes: %s\n", e.Changes)
}

// Enrich fills missing movie metadata from TMDb: IMDb id, year and name where empty, TMDb release dates per
// country, alternative titles and translated titles (title_alias, source tmdb), the Wikidata id and the collection.
// It never overwrites what the arrs gave; differences are reported. A title already holding TMDb release dates, TMDb
// names, an IMDb id and a year is skipped, so a re-run looks up nothing new (a movie TMDb gives no other names is
// looked up again, from the cache).
func Enrich(ctx context.Context, st *store.Store, md MovieSource, now func() int64) (*EnrichSummary, error) {
	sum := &EnrichSummary{Filled: map[string]int{}, Changes: Changes{}}
	type cand struct {
		id     int64
		tmdbID int
		name   string
		imdb   sql.NullString
		year   sql.NullInt64
	}
	rows, err := st.Reader().QueryContext(ctx, `SELECT t.id, t.tmdb_id, t.name, t.imdb_id, t.year FROM title t
		WHERE t.kind = 'movie' AND t.tmdb_id IS NOT NULL AND (t.imdb_id IS NULL OR t.imdb_id = '' OR t.year IS NULL
		OR NOT EXISTS (SELECT 1 FROM title_release_date d WHERE d.title_id = t.id AND d.source = 'tmdb')
		OR NOT EXISTS (SELECT 1 FROM title_alias a WHERE a.title_id = t.id AND a.source = 'tmdb'))
		ORDER BY t.tmdb_id`)
	if err != nil {
		return nil, err
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.tmdbID, &c.name, &c.imdb, &c.year); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	sum.Candidates = len(cands)

	for _, c := range cands {
		m, err := md.Movie(ctx, c.tmdbID)
		if errors.Is(err, tmdb.ErrNotFound) {
			sum.NotFound++
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return sum, ctx.Err()
			}
			sum.Failed++
			if sum.Failed > 20 {
				return sum, fmt.Errorf("enrich: too many failures, last: %w", err)
			}
			continue
		}
		sum.Looked++
		imdb := m.IMDbID
		if imdb == "" {
			imdb = m.ExternalIDs.IMDbID
		}
		var dates []ReleaseDate
		for _, cr := range m.ReleaseDates.Results {
			for _, r := range cr.Releases {
				typ := tmdb.TypeName(r.Type)
				if typ == "" || r.Date.IsZero() {
					continue
				}
				dates = append(dates, ReleaseDate{Country: cr.Country, Type: typ, Date: r.Date.UTC().Format("2006-01-02"),
					Certification: r.Certification, Note: r.Note})
			}
		}
		dates = dedupeDates(dates)
		names := tmdbAliases(m)
		err = st.WriteTx(ctx, func(tx *sql.Tx) error {
			w := &wtx{tx: tx, changes: Changes{}, now: now()}
			filled := map[string]int{}
			var sets []kv
			if (!c.imdb.Valid || c.imdb.String == "") && imdb != "" {
				sets = append(sets, kv{"imdb_id", imdb})
				filled["imdb_id"]++
			}
			if !c.year.Valid && m.Year() != 0 {
				sets = append(sets, kv{"year", int64(m.Year())})
				filled["year"]++
			}
			if c.name == "" && m.Title != "" {
				sets = append(sets, kv{"name", m.Title})
				filled["name"]++
			}
			if len(sets) > 0 {
				if _, err := w.upsert(ctx, "title", []kv{{"id", c.id}}, sets, nil, []kv{{"updated_at", w.now}}); err != nil {
					return err
				}
			}
			n0 := w.changes.Total()
			if _, err := tx.ExecContext(ctx, "DELETE FROM source_sighting WHERE title_id=? AND source_kind='tmdb'", c.id); err != nil {
				return err
			}
			if err := w.releaseDates(ctx, c.id, "tmdb", dates); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE title_release_date SET fetched_at=? WHERE title_id=? AND source='tmdb'", w.now, c.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE source_ceiling SET next_check_at=0 WHERE title_id=?", c.id); err != nil {
				return err
			}
			if w.changes.Total() > n0 {
				filled["release_dates"]++
			}
			n0 = w.changes.Total()
			if err := w.aliases(ctx, c.id, "tmdb", names); err != nil {
				return err
			}
			if w.changes.Total() > n0 {
				filled["aliases"]++
			}
			ext := []kv{}
			if m.ExternalIDs.WikidataID != "" {
				ext = append(ext, kv{"wikidata", m.ExternalIDs.WikidataID})
			}
			if m.BelongsToCollection != nil && m.BelongsToCollection.ID != 0 {
				ext = append(ext, kv{"tmdb_collection", strconv.Itoa(m.BelongsToCollection.ID)})
			}
			for _, e := range ext {
				n, err := w.exec(ctx, `INSERT OR IGNORE INTO title_external_id (title_id, source, external_id) VALUES (?, ?, ?)`,
					c.id, e.k, e.v)
				if err != nil {
					return err
				}
				if n > 0 {
					filled[e.k]++
				}
				w.changes.add("title_external_id", opInsert, int(n))
			}
			for k, v := range filled {
				sum.Filled[k] += v
			}
			for t, x := range w.changes {
				sum.Changes.add(t, opInsert, x.Inserted)
				sum.Changes.add(t, opUpdate, x.Updated)
				sum.Changes.add(t, opDelete, x.Deleted)
			}
			return nil
		})
		if err != nil {
			return sum, fmt.Errorf("enrich tmdb %d: %w", c.tmdbID, err)
		}
		if c.imdb.String != "" && imdb != "" && c.imdb.String != imdb {
			sum.Disagreements = append(sum.Disagreements, fmt.Sprintf("tmdb %d %q: IMDb id %s stored, TMDb has %s",
				c.tmdbID, c.name, c.imdb.String, imdb))
		}
		if c.year.Valid && m.Year() != 0 && int(c.year.Int64) != m.Year() {
			sum.Disagreements = append(sum.Disagreements, fmt.Sprintf("tmdb %d %q: year %d stored, TMDb has %d",
				c.tmdbID, c.name, c.year.Int64, m.Year()))
		}
	}
	return sum, nil
}

// tmdbAliases are a movie's alternative titles and its translated titles (a translation that keeps the title has an
// empty one and is left out), as Radarr's metadata gives them to its movie lookup.
func tmdbAliases(m *tmdb.Movie) []Alias {
	var out []Alias
	for _, a := range m.AlternativeTitles.Titles {
		if a.Title != "" {
			out = append(out, Alias{Kind: AliasAlternative, Name: a.Title, Country: a.Country})
		}
	}
	for _, t := range m.Translations.Translations {
		if t.Data.Title != "" {
			out = append(out, Alias{Kind: AliasTranslation, Name: t.Data.Title, Language: t.Language, Country: t.Country})
		}
	}
	return out
}

// dedupeDates keeps one entry per (country, type, date), the first, in a stable order.
func dedupeDates(in []ReleaseDate) []ReleaseDate {
	type key struct{ c, t, d string }
	seen := map[key]bool{}
	var out []ReleaseDate
	for _, d := range in {
		k := key{d.Country, d.Type, d.Date}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Country != out[j].Country {
			return out[i].Country < out[j].Country
		}
		return out[i].Date < out[j].Date
	})
	return out
}
