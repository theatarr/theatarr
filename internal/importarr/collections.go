package importarr

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/theatarr/theatarr/internal/arr"
)

// Radarr collections (docs/design/arrview.md, Collections). The import carries every collection the arr monitors over to
// movie intake (movie_collection, which internal/catalog's MovieIntake refreshes): monitored, with the arr's search on
// add and minimum availability, targeting the instance's version and its followers that are collection targets
// (policy CollectionTargets: active movie versions nothing follows, the ones the library gate lets write). Targets are
// always explicit. It only ever turns collections on: a collection the arr does not monitor is left as it is, so
// nothing Theatarr monitors is unmonitored or deleted here. A collection Theatarr already monitors keeps its settings;
// the import drops stored targets that are no longer eligible (an empty list among them), adds the missing ones and
// turns search on add on when the arr has it.

// CollectionCounts counts what the import did for the arr's collections.
type CollectionCounts struct {
	// Monitored is the arr's monitored collections.
	Monitored int `json:"monitored"`
	// Added is the collections this run created in movie intake; Updated those it turned on or gave new targets.
	Added   int `json:"added"`
	Updated int `json:"updated"`
}

type radarrCollection struct {
	TmdbID              int    `json:"tmdbId"`
	Title               string `json:"title"`
	Monitored           bool   `json:"monitored"`
	SearchOnAdd         bool   `json:"searchOnAdd"`
	MinimumAvailability string `json:"minimumAvailability"`
}

// fetchCollections reads GET collection. An older Radarr without the endpoint has none; any other failure is a
// warning, not a failed import, and leaves movie intake as it is.
func fetchCollections(ctx context.Context, g Getter, s *Snapshot) error {
	var cols []radarrCollection
	if err := g.GetJSON(ctx, "collection", nil, &cols); err != nil {
		if ctx.Err() != nil {
			return err
		}
		var se *arr.StatusError
		if !errors.As(err, &se) || se.Status != http.StatusNotFound {
			s.Warnings = append(s.Warnings, fmt.Sprintf("collections not read: %v", err))
		}
		return nil
	}
	for _, c := range cols {
		s.Collections = append(s.Collections, Collection{TmdbID: c.TmdbID, Name: c.Title, Monitored: c.Monitored,
			SearchOnAdd: c.SearchOnAdd, MinimumAvailability: c.MinimumAvailability})
	}
	return nil
}

// collectionTargets are the versions a carried-over collection adds movies to: the instance's version and its
// followers, as far as they are collection targets (a shadow version grabs nothing and the arr itself still adds
// there; a followed version's roots are its arr's).
func collectionTargets(src Source) []string {
	var out []string
	for _, k := range append([]string{src.Version}, src.Followers...) {
		if slices.Contains(src.CollectionTargets, k) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func minimumAvailability(s string) string {
	switch s {
	case "tba", "announced", "inCinemas", "released":
		return s
	}
	return "released"
}

func (w *wtx) collections(ctx context.Context, src Source, snap *Snapshot, sum *Summary) error {
	var monitored []Collection
	for _, c := range snap.Collections {
		if c.Monitored && c.TmdbID > 0 {
			monitored = append(monitored, c)
		}
	}
	sum.Collections.Monitored = len(monitored)
	if len(monitored) == 0 {
		return nil
	}
	targets := collectionTargets(src)
	if len(targets) == 0 {
		sum.Warnings = append(sum.Warnings, fmt.Sprintf("%d monitored collections not carried over: neither version %s "+
			"nor its followers is an active movie version nothing follows", len(monitored), src.Version))
		return nil
	}
	for _, c := range monitored {
		var on, search bool
		var versions, name string
		err := w.tx.QueryRowContext(ctx, `SELECT monitored, versions, search_on_add, name FROM movie_collection
			WHERE tmdb_id = ?`, c.TmdbID).Scan(&on, &versions, &search, &name)
		if errors.Is(err, sql.ErrNoRows) {
			vb, _ := json.Marshal(targets)
			if _, err := w.exec(ctx, `INSERT INTO movie_collection (tmdb_id, name, monitored, versions,
				minimum_availability, search_on_add, updated_at) VALUES (?, ?, 1, ?, ?, ?, ?)`,
				c.TmdbID, c.Name, string(vb), minimumAvailability(c.MinimumAvailability), dbValue(c.SearchOnAdd), w.now); err != nil {
				return err
			}
			w.changes.add("movie_collection", opInsert, 1)
			sum.Collections.Added++
			continue
		}
		if err != nil {
			return err
		}
		if name == "" {
			name = c.Name
		}
		if !on {
			// Discovered (or turned off here earlier): the arr's settings, as for a new one.
			vb, _ := json.Marshal(targets)
			if _, err := w.exec(ctx, `UPDATE movie_collection SET name = ?, monitored = 1, versions = ?,
				minimum_availability = ?, search_on_add = ?, updated_at = ? WHERE tmdb_id = ?`, name, string(vb),
				minimumAvailability(c.MinimumAvailability), dbValue(c.SearchOnAdd), w.now, c.TmdbID); err != nil {
				return err
			}
			w.changes.add("movie_collection", opUpdate, 1)
			sum.Collections.Updated++
			continue
		}
		var have []string
		if err := json.Unmarshal([]byte(versions), &have); err != nil {
			return fmt.Errorf("collection %d: versions: %w", c.TmdbID, err)
		}
		// Stored targets that are no longer eligible go (intake skips them anyway), and so does an empty list, which
		// targets nothing explicit: the arr's targets are added.
		var merged []string
		for _, k := range have {
			if slices.Contains(src.CollectionTargets, k) {
				merged = append(merged, k)
			}
		}
		merged = append(merged, targets...)
		slices.Sort(merged)
		merged = slices.Compact(merged)
		if slices.Equal(merged, have) && (search || !c.SearchOnAdd) {
			continue
		}
		vb, _ := json.Marshal(merged)
		if _, err := w.exec(ctx, `UPDATE movie_collection SET versions = ?, search_on_add = ?, updated_at = ?
			WHERE tmdb_id = ?`, string(vb), dbValue(search || c.SearchOnAdd), w.now, c.TmdbID); err != nil {
			return err
		}
		w.changes.add("movie_collection", opUpdate, 1)
		sum.Collections.Updated++
	}
	return nil
}
