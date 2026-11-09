package art

import (
	"context"
	"database/sql"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/store"
)

func movieDates(m *tmdb.Movie) map[string]time.Time { return m.ReleaseSchedule() }

func writeMovieDates(ctx context.Context, tx *sql.Tx, titleID int64, m *tmdb.Movie, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM title_release_date WHERE title_id = ? AND source IN ('tmdb', 'tmdb-art')`, titleID); err != nil {
		return err
	}
	dates := movieDates(m)
	for kind, d := range dates {
		if d.IsZero() {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO title_release_date (title_id, source, country, type, date, fetched_at)
			VALUES (?, 'tmdb-art', '', ?, ?, ?)`, titleID, kind, d.Format(time.DateOnly), store.Millis(now)); err != nil {
			return err
		}
	}
	for _, c := range m.ReleaseDates.Results {
		if c.Country == "" {
			continue
		}
		for _, r := range c.Releases {
			kind := tmdb.TypeName(r.Type)
			if kind == "" || r.Date.IsZero() {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO title_release_date
				(title_id, source, country, type, date, certification, note, fetched_at) VALUES (?, 'tmdb', ?, ?, ?, ?, ?, ?)`,
				titleID, c.Country, kind, r.Date.UTC().Format(time.DateOnly), r.Certification, r.Note, store.Millis(now)); err != nil {
				return err
			}
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE title_meta SET movie_dates_at = ?, status = ? WHERE title_id = ?`, store.Millis(now), availability.Status(dates, now), titleID)
	return err
}
