package activity

import (
	"context"
	"database/sql"
	"slices"
	"sort"
	"time"

	"github.com/theatarr/theatarr/internal/metadata/tvrefresh"
	"github.com/theatarr/theatarr/internal/store"
)

// CalendarItem is one movie release date or one episode air date.
type CalendarItem struct {
	// Date is the day, YYYY-MM-DD: the release date, or the episode's air date as the network gives it.
	Date string
	// At is the episode's air time in UTC, when it has a real one. An instant that is midnight of the air date in
	// the series' zone is what the refresh writes when the air time is unknown (tvrefresh.MidnightAir), a made-up
	// time: zero here, and Date alone is the day.
	At time.Time
	// Kind is movie or episode; Type is theatrical, digital or physical for a movie, episode for an episode.
	Kind, Type string
	TitleID    int64
	TitleName  string
	TitleYear  int
	Series     bool
	Anime      bool
	EpisodeID  int64
	Season     int
	Number     int
	Episode    string // the episode's name
	Monitored  bool
	// Available is true when some version holds the movie or the episode (state filled or cutoff_met); a version
	// removed from its library (arrview_removed_version) does not count.
	Available bool
}

// CalendarTypes are the movie release types the calendar shows, in the arrs' order.
var CalendarTypes = []string{"theatrical", "digital", "physical"}

// The release-date choice (docs/design/ui.md "Release dates"), one rule for Home's Upcoming row, the calendar, the
// lists' Next release and Release date and their sorts, and the title page's dates: per movie and release type (in cinemas, digital,
// physical), the type's first date, from the canonical dates (country '', the successful TMDb refresh when there is
// one, else the arrs' dates), else from TMDb's US dates while the title has no refresh. Later dates of a type (a
// re-release, a streaming service picking an old film up) are never the type's date. Display only: what may be
// fetched reads availability.Dates.

// releaseDatesSQL aggregates title_release_date d per title and release type (CalendarTypes), the rows narrowed by
// cond (an SQL condition on d, such as "d.title_id = t.id"): canon is the type's first canonical date (country empty,
// the successful TMDb refresh when there is one, else the arrs' dates: availability.Dates' choice), us its first TMDb
// US date while the title has no refresh. One pass over the rows; YYYY-MM-DD.
func releaseDatesSQL(cond string) string {
	return `SELECT d.title_id, d.type, ` + canonSQL + ` AS canon, ` + usSQL + ` AS us
		FROM title_release_date d LEFT JOIN title_meta fm ON fm.title_id = d.title_id
		WHERE d.type IN ('theatrical', 'digital', 'physical') AND d.country IN ('', 'US') AND (` + cond + `)
		GROUP BY d.title_id, d.type`
}

// canonSQL and usSQL are releaseDatesSQL's two aggregates over d, with fm the title's title_meta row.
const (
	canonSQL = `MIN(CASE WHEN d.country = '' AND (d.source = 'tmdb-art' OR fm.movie_dates_at IS NULL) THEN substr(d.date, 1, 10) END)`
	usSQL    = `MIN(CASE WHEN d.source = 'tmdb' AND d.country = 'US' AND fm.movie_dates_at IS NULL THEN substr(d.date, 1, 10) END)`
)

// FirstReleaseOfSQL is the release-date choice of one type for title title (an SQL expression such as "t.id") as one
// scalar value: a flat aggregate, NULL without a date.
func FirstReleaseOfSQL(title, typ string) string {
	return `(SELECT COALESCE(` + canonSQL + `, ` + usSQL + `) FROM title_release_date d
		LEFT JOIN title_meta fm ON fm.title_id = d.title_id
		WHERE d.title_id = ` + title + ` AND d.type = '` + typ + `' AND d.country IN ('', 'US'))`
}

// FirstReleasesSQL selects title_id, type and date of the release-date choice for the rows cond narrows
// (releaseDatesSQL): per type the canonical first date, else the US one.
func FirstReleasesSQL(cond string) string {
	return `SELECT title_id, type, COALESCE(canon, us) AS date FROM (` + releaseDatesSQL(cond) + `)
		WHERE COALESCE(canon, us) IS NOT NULL`
}

// FirstReleasesBetweenSQL selects title_id, type and date of every chosen release date (FirstReleasesSQL) from the
// first to the second argument (YYYY-MM-DD, both included).
var FirstReleasesBetweenSQL = `SELECT title_id, type, date FROM (` + FirstReleasesSQL("1") + `) WHERE date >= ? AND date <= ?`

// MovieDates are a movie's dates by release type: First is the release-date choice (FirstReleasesSQL), Canonical
// availability.Dates' canonical dates of the three types (what HomeRelease reads).
type MovieDates struct {
	First     map[string]string
	Canonical map[string]time.Time
}

// ReadMovieDates reads MovieDates for titles ids in one pass over their release dates; titles without dates are left
// out.
func ReadMovieDates(ctx context.Context, db *sql.DB, ids []any) (map[int64]MovieDates, error) {
	out := map[int64]MovieDates{}
	for chunk := range slices.Chunk(ids, 500) {
		rows, err := db.QueryContext(ctx, releaseDatesSQL(`d.title_id IN (`+store.Placeholders(len(chunk))+`)`), chunk...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var typ string
			var canon, us sql.NullString
			if err := rows.Scan(&id, &typ, &canon, &us); err != nil {
				rows.Close()
				return nil, err
			}
			m, ok := out[id]
			if !ok {
				m = MovieDates{First: map[string]string{}, Canonical: map[string]time.Time{}}
				out[id] = m
			}
			if canon.Valid {
				m.First[typ] = canon.String
				if d, err := time.Parse(time.DateOnly, canon.String); err == nil {
					m.Canonical[typ] = d
				}
			} else if us.Valid {
				m.First[typ] = us.String
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Calendar lists the movie release dates and episode air dates from from to to (dates, both included), oldest
// first. Movie dates are the release-date choice (FirstReleasesSQL). Episodes come from the episode table (TVDB and
// the arrs). Only monitored titles (and monitored episodes) unless unmonitored.
func Calendar(ctx context.Context, db *sql.DB, from, to string, unmonitored bool) ([]CalendarItem, error) {
	var out []CalendarItem
	mon := ` AND t.monitored = 1 AND EXISTS (SELECT 1 FROM version v WHERE v.title_id = t.id AND v.monitored = 1)`
	if unmonitored {
		mon = ``
	}
	rows, err := db.QueryContext(ctx, `WITH best (title_id, type, date) AS (`+FirstReleasesBetweenSQL+`)
	SELECT t.id, t.name, COALESCE(t.year, 0), t.anime, t.monitored, best.type, best.date,
		EXISTS (SELECT 1 FROM version v WHERE v.title_id = t.id AND v.state IN ('filled', 'cutoff_met')
			AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id))
	FROM best JOIN title t ON t.id = best.title_id
	WHERE t.kind = 'movie'`+mon, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		it := CalendarItem{Kind: "movie"}
		if err := rows.Scan(&it.TitleID, &it.TitleName, &it.TitleYear, &it.Anime, &it.Monitored, &it.Type, &it.Date,
			&it.Available); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, it)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	epMon := mon + ` AND e.monitored = 1`
	if unmonitored {
		epMon = ``
	}
	// An episode is monitored when a library that was not removed monitors it (its version and its episode row).
	rows, err = db.QueryContext(ctx, `SELECT t.id, t.name, COALESCE(t.year, 0), t.anime, t.monitored AND e.monitored
		AND EXISTS (SELECT 1 FROM episode_version_state s JOIN version v ON v.id = s.version_id WHERE s.episode_id = e.id
			AND s.monitored = 1 AND v.monitored = 1
			AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)),
		e.id, e.season, e.number, e.name, e.air_date, COALESCE(e.air_date_utc_at, 0), COALESCE(sm.time_zone, ''),
		COALESCE((SELECT MIN(x.air_date_utc_at) FROM episode x WHERE x.title_id = e.title_id AND x.season = e.season
			AND x.air_date = e.air_date), 0),
		EXISTS (SELECT 1 FROM episode_version_state s WHERE s.episode_id = e.id AND s.state IN ('filled', 'cutoff_met')
			AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = s.version_id))
	FROM episode e JOIN title t ON t.id = e.title_id LEFT JOIN series_meta sm ON sm.title_id = t.id
	WHERE t.kind = 'series' AND e.air_date IS NOT NULL AND e.air_date >= ? AND e.air_date <= ?`+epMon, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		it := CalendarItem{Kind: "episode", Type: "episode", Series: true}
		var airMs, firstMs int64
		var zone string
		if err := rows.Scan(&it.TitleID, &it.TitleName, &it.TitleYear, &it.Anime, &it.Monitored, &it.EpisodeID,
			&it.Season, &it.Number, &it.Episode, &it.Date, &airMs, &zone, &firstMs, &it.Available); err != nil {
			rows.Close()
			return nil, err
		}
		// The instant is the episode's own (an import brings real ones without a series air time). It is made up
		// when the day's first episode of the season sits at the refresh's midnight: the refresh spaces the later
		// ones of that day a runtime apart from it.
		if mid, ok := tvrefresh.MidnightAir(it.Date, zone); !ok || firstMs != store.Millis(mid) {
			it.At = at(airMs)
		}
		out = append(out, it)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		if a.TitleName != b.TitleName {
			return a.TitleName < b.TitleName
		}
		if a.Season != b.Season {
			return a.Season < b.Season
		}
		if a.Number != b.Number {
			return a.Number < b.Number
		}
		return typeRank(a.Type) < typeRank(b.Type)
	})
	return out, nil
}

func typeRank(t string) int {
	for i, x := range CalendarTypes {
		if x == t {
			return i
		}
	}
	return len(CalendarTypes)
}
