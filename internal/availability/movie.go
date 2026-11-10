// Port of Radarr src/NzbDrone.Core/Movies/Movie.cs and MetadataSource/SkyHook/SkyHookProxy.cs @ a96bf7c.
//
// Package availability gates movie acquisition on the stored release dates.
package availability

import (
	"context"
	"database/sql"
	"slices"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Available ports Radarr Movie.IsAvailable (Movie.cs @ a96bf7c): delay is the availability delay in days (negative
// is earlier), added to the date compared against; announced and tba are always available and a movie without a
// date never is, whatever the delay.
func Available(minimum string, dates map[string]time.Time, now time.Time, delay int) bool {
	if minimum == "announced" || minimum == "tba" {
		return true
	}
	var date time.Time
	if minimum == "inCinemas" {
		date = dates["theatrical"]
	}
	if date.IsZero() {
		date = HomeRelease(dates)
	}
	return !date.IsZero() && !now.Before(date.AddDate(0, 0, delay))
}

// HomeRelease is the day a movie is out at home, the date Available's "released" minimum compares against: the
// earlier of the digital and physical dates, else the cinema date plus 90 days; zero without any of them.
func HomeRelease(dates map[string]time.Time) time.Time {
	date := dates["digital"]
	if p := dates["physical"]; !p.IsZero() && (date.IsZero() || p.Before(date)) {
		date = p
	}
	if date.IsZero() && !dates["theatrical"].IsZero() {
		date = dates["theatrical"].AddDate(0, 0, 90)
	}
	return date
}

// Status ports SkyHookProxy.MapMovie's date-derived status (Radarr @ a96bf7c).
func Status(dates map[string]time.Time, now time.Time) string {
	status := "announced"
	if c := dates["theatrical"]; !c.IsZero() && now.After(c) {
		status = "inCinemas"
		if dates["digital"].IsZero() && dates["physical"].IsZero() && now.After(c.AddDate(0, 0, 90)) {
			status = "released"
		}
	}
	for _, kind := range []string{"digital", "physical"} {
		if d := dates[kind]; !d.IsZero() && !now.Before(d) {
			status = "released"
		}
	}
	return status
}

// Movie checks one version against its rule (Rules): a matching view's latest explicit choice wins over its imported
// arr setting, that over the library's minimum_availability, and a movie with none of them is gated as released.
func Movie(ctx context.Context, db *sql.DB, pol *policy.Policy, titleID, versionID int64, key string, now time.Time) (bool, error) {
	rules := map[int64]Rule{}
	if err := rulesInto(ctx, db, pol, []copyRef{{titleID, versionID, key}}, rules); err != nil {
		return false, err
	}
	rule := rules[versionID]
	// Parallel versions inherit the owning arr's dates until cutover, including corrections after our TMDb fetch.
	var instance string
	err := db.QueryRowContext(ctx, `SELECT vs.instance FROM version v LEFT JOIN version_follow f ON f.version_id = v.id
		JOIN version_source vs ON vs.version_id = COALESCE(f.follows_id, v.id) WHERE v.id = ?`, versionID).Scan(&instance)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if !pol.ArrFed(instance) {
		instance = ""
	}
	dates, err := datesFrom(ctx, db, titleID, instance)
	if err != nil {
		return false, err
	}
	return Available(rule.Minimum, dates, now, rule.Delay), nil
}

// Dates reads canonical dates (empty country), preferring a successful TMDb refresh over imported dates.
func Dates(ctx context.Context, db *sql.DB, titleID int64) (map[string]time.Time, error) {
	return datesFrom(ctx, db, titleID, "")
}

func datesFrom(ctx context.Context, db *sql.DB, titleID int64, instance string) (map[string]time.Time, error) {
	if instance != "" {
		return readDates(ctx, db, `SELECT type, MIN(date) FROM title_release_date WHERE title_id = ? AND country = '' AND source = ? GROUP BY type`, titleID, instance)
	}
	return readDates(ctx, db, `SELECT type, MIN(date) FROM title_release_date d
		WHERE title_id = ? AND `+canonical+` GROUP BY type`, titleID)
}

// canonical picks the canonical dates of title_release_date d: empty country, the successful TMDb refresh when
// there is one, else the imported dates.
const canonical = `d.country = '' AND (d.source = 'tmdb-art' OR NOT EXISTS
	(SELECT 1 FROM title_meta m WHERE m.title_id = d.title_id AND m.movie_dates_at IS NOT NULL))`

// datesBatch keeps DatesFor's IN lists well under SQLite's bind-parameter limit.
const datesBatch = 500

// DatesFor reads Dates for many titles, by title id, datesBatch ids per query; titles without dates are left out.
func DatesFor(ctx context.Context, db *sql.DB, titleIDs []any) (map[int64]map[string]time.Time, error) {
	out := map[int64]map[string]time.Time{}
	for ids := range slices.Chunk(titleIDs, datesBatch) {
		if err := datesBatchInto(ctx, db, ids, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func datesBatchInto(ctx context.Context, db *sql.DB, ids []any, out map[int64]map[string]time.Time) error {
	rows, err := db.QueryContext(ctx, `SELECT title_id, type, MIN(date) FROM title_release_date d
		WHERE title_id IN (`+store.Placeholders(len(ids))+`) AND `+canonical+` GROUP BY title_id, type`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var kind, date string
		if err := rows.Scan(&id, &kind, &date); err != nil {
			return err
		}
		if d, err := time.Parse(time.DateOnly, date); err == nil {
			if out[id] == nil {
				out[id] = map[string]time.Time{}
			}
			out[id][kind] = d
		}
	}
	return rows.Err()
}

func readDates(ctx context.Context, db *sql.DB, query string, args ...any) (map[string]time.Time, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	dates := map[string]time.Time{}
	for rows.Next() {
		var kind, date string
		if err := rows.Scan(&kind, &date); err != nil {
			return nil, err
		}
		if d, err := time.Parse(time.DateOnly, date); err == nil {
			dates[kind] = d
		}
	}
	return dates, rows.Err()
}
