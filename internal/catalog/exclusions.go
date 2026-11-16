package catalog

import (
	"context"
	"database/sql"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// ExcludeTitle puts a title on the list exclusions of its kind, with its name and year: a movie by TMDb id
// (movie_exclusion), a series by TVDB id (series_exclusion). Lists, collections and the one-time arr import skip it
// from then on; requests and adds by hand do not read the exclusions. A title without that id is not excluded
// (false); an exclusion already there stays as it is.
func ExcludeTitle(ctx context.Context, tx *sql.Tx, titleID int64, now time.Time) (bool, error) {
	var kind, name string
	var year, tmdbID, tvdbID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT kind, name, year, tmdb_id, tvdb_id FROM title WHERE id = ?`, titleID).
		Scan(&kind, &name, &year, &tmdbID, &tvdbID); err != nil {
		return false, err
	}
	var y any
	if year.Valid && year.Int64 > 0 {
		y = year.Int64
	}
	q, id := `INSERT INTO movie_exclusion(tmdb_id, title, year, created_at) VALUES(?, ?, ?, ?) ON CONFLICT DO NOTHING`, tmdbID
	if kind == "series" {
		q, id = `INSERT INTO series_exclusion(tvdb_id, title, year, created_at) VALUES(?, ?, ?, ?) ON CONFLICT DO NOTHING`, tvdbID
	}
	if !id.Valid || id.Int64 <= 0 {
		return false, nil
	}
	_, err := tx.ExecContext(ctx, q, id.Int64, name, y, store.Millis(now))
	return err == nil, err
}
