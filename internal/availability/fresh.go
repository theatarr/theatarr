package availability

import (
	"context"
	"database/sql"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// FreshWindow is how long content counts as new after its release (docs/design/router.md "Re-search after a failure"):
// debrid caches pick a new release up within minutes to hours, so an uncached miss on it is soon worth asking again.
const FreshWindow = 7 * 24 * time.Hour

// Fresh reports until when the content of a route or search is new: its newest date within the last FreshWindow plus
// FreshWindow, or zero when it is not new at now. The dates are the content's, never a torrent's: for series the air
// dates (UTC) of episodeIDs, the latest at or before now (none given: not new); for a movie its home release, digital or physical (canonical
// dates, as Dates reads them). A theatrical date never counts: nothing worth caching exists yet.
func Fresh(ctx context.Context, db *sql.DB, kind policy.Kind, titleID int64, episodeIDs []int64, now time.Time) (
	time.Time, error) {
	var dates []time.Time
	if kind == policy.KindSeries {
		if len(episodeIDs) == 0 {
			return time.Time{}, nil
		}
		args := []any{titleID}
		for _, id := range episodeIDs {
			args = append(args, id)
		}
		args = append(args, store.Millis(now))
		var aired sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT MAX(air_date_utc_at) FROM episode WHERE title_id = ? AND id IN (`+
			store.Placeholders(len(episodeIDs))+`) AND air_date_utc_at <= ?`, args...).Scan(&aired); err != nil {
			return time.Time{}, err
		}
		if aired.Valid {
			dates = append(dates, store.FromMillis(aired.Int64))
		}
	} else {
		ds, err := Dates(ctx, db, titleID)
		if err != nil {
			return time.Time{}, err
		}
		dates = append(dates, ds["digital"], ds["physical"])
	}
	var until time.Time
	for _, d := range dates {
		if u := d.Add(FreshWindow); !d.IsZero() && !d.After(now) && now.Before(u) && u.After(until) {
			until = u
		}
	}
	return until, nil
}

// FreshEpisodes lists a series' episodes that are new at now (aired within the last FreshWindow, not removed), with until when the
// newest of them stays new; none and zero when there are none.
func FreshEpisodes(ctx context.Context, db *sql.DB, titleID int64, now time.Time) ([]int64, time.Time, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, air_date_utc_at FROM episode WHERE title_id = ? AND removed_at IS NULL
		AND air_date_utc_at <= ? AND air_date_utc_at > ? ORDER BY id`, titleID, store.Millis(now),
		store.Millis(now.Add(-FreshWindow)))
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()
	var ids []int64
	var until time.Time
	for rows.Next() {
		var id, aired int64
		if err := rows.Scan(&id, &aired); err != nil {
			return nil, time.Time{}, err
		}
		ids = append(ids, id)
		if u := store.FromMillis(aired).Add(FreshWindow); u.After(until) {
			until = u
		}
	}
	return ids, until, rows.Err()
}
