package activity

import (
	"context"
	"database/sql"
	"slices"
	"sort"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
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
// lists' Next release and Release date and their sorts, and the title page's dates: per movie and release type (in
// cinemas, digital, physical) the primary release's first date. A title with a successful TMDb refresh takes its
// primary country's rows (source tmdb, a country: the only country stored, tmdb.PrimaryCountry), in cinemas falling
// back to that country's limited release and then to the canonical cinema date (the first anywhere, never a
// re-release); digital and physical never come from another country. A title without a refresh takes the canonical
// dates (the arrs'), else its stored TMDb country rows. Later dates of a type (a re-release, a streaming service
// picking an old film up) are never the type's date. Display only: what may be fetched reads availability.Dates.

// releaseDatesSQL aggregates title_release_date d per title and release type (CalendarTypes; a limited cinema
// release counts as theatrical), the rows narrowed by cond (an SQL condition on d, such as "d.title_id = t.id"):
// canon is the type's first canonical date (country empty, the successful TMDb refresh when there is one, else the
// arrs' dates: availability.Dates' choice) and first the release-date choice. One pass over the rows; YYYY-MM-DD.
func releaseDatesSQL(cond string) string {
	return `SELECT d.title_id, CASE d.type WHEN 'theatrical_limited' THEN 'theatrical' ELSE d.type END AS type,
		` + canonSQL + ` AS canon, CASE WHEN MAX(fm.movie_dates_at) IS NOT NULL THEN ` + primarySQL + `
		ELSE COALESCE(` + canonSQL + `, ` + primarySQL + `) END AS first, ` + firstCinemaSQL + ` AS first_cinema
		FROM title_release_date d LEFT JOIN title_meta fm ON fm.title_id = d.title_id
		WHERE d.type IN ('theatrical', 'theatrical_limited', 'digital', 'physical') AND (` + cond + `)
		GROUP BY 1, 2`
}

// firstCinemaSQL is availability's first cinema date (availability.Dates' FirstCinema) in releaseDatesSQL's cinema
// group: the canonical theatrical_limited row or a cinema date of the country rows.
const firstCinemaSQL = `MIN(CASE WHEN d.type IN ('theatrical', 'theatrical_limited') AND (d.country <> ''
	OR d.type = 'theatrical_limited' AND (d.source = 'tmdb-art' OR fm.movie_dates_at IS NULL)) THEN substr(d.date, 1, 10) END)`

// canonSQL and primarySQL are releaseDatesSQL's aggregates over d, with fm the title's title_meta row: primarySQL is
// the primary country's first date of the type (wide before limited), a cinema date else the canonical one.
const (
	canonSQL = `MIN(CASE WHEN d.country = '' AND d.type <> 'theatrical_limited'
		AND (d.source = 'tmdb-art' OR fm.movie_dates_at IS NULL) THEN substr(d.date, 1, 10) END)`
	primarySQL = `COALESCE(
		MIN(CASE WHEN d.source = 'tmdb' AND d.country <> '' AND d.type <> 'theatrical_limited' THEN substr(d.date, 1, 10) END),
		MIN(CASE WHEN d.source = 'tmdb' AND d.country <> '' AND d.type = 'theatrical_limited' THEN substr(d.date, 1, 10) END),
		CASE WHEN MAX(d.type) LIKE 'theatrical%' THEN ` + canonSQL + ` END)`
)

// FirstReleasesSQL selects title_id, type and date of the release-date choice for the rows cond narrows
// (releaseDatesSQL).
func FirstReleasesSQL(cond string) string {
	return `SELECT title_id, type, first AS date FROM (` + releaseDatesSQL(cond) + `) WHERE first IS NOT NULL`
}

// FirstReleasesBetweenSQL selects title_id, type and date of every chosen release date (FirstReleasesSQL) from the
// first to the second argument (YYYY-MM-DD, both included).
var FirstReleasesBetweenSQL = `SELECT title_id, type, date FROM (` + FirstReleasesSQL("1") + `) WHERE date >= ? AND date <= ?`

// MovieDates are a movie's dates by release type: First is the release-date choice (FirstReleasesSQL), Canonical
// availability.Dates' dates (the canonical dates of the three types and the first cinema date, what HomeRelease reads).
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
			var canon, first, firstCinema sql.NullString
			if err := rows.Scan(&id, &typ, &canon, &first, &firstCinema); err != nil {
				rows.Close()
				return nil, err
			}
			m, ok := out[id]
			if !ok {
				m = MovieDates{First: map[string]string{}, Canonical: map[string]time.Time{}}
				out[id] = m
			}
			if first.Valid {
				m.First[typ] = first.String
			}
			if d, err := time.Parse(time.DateOnly, firstCinema.String); err == nil && firstCinema.Valid {
				m.Canonical[availability.FirstCinema] = d
			}
			if canon.Valid {
				if d, err := time.Parse(time.DateOnly, canon.String); err == nil {
					m.Canonical[typ] = d
				}
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// EpisodeMonitoredSQL is the condition that episode e of title t is monitored, as the calendar, Home and the
// episode grid read it: the title is monitored and a library of it that was not removed monitors both the copy and
// the episode (episode_version_state). Only an episode with no library row at all keeps its cached flag
// (e.monitored, with a monitored library left). The per-library readers (acquisition, Wanted, reports) keep theirs.
const EpisodeMonitoredSQL = `(t.monitored = 1 AND CASE
	WHEN EXISTS (SELECT 1 FROM episode_version_state ms0 WHERE ms0.episode_id = e.id)
	THEN EXISTS (SELECT 1 FROM episode_version_state ms JOIN version mv ON mv.id = ms.version_id
		WHERE ms.episode_id = e.id AND ms.monitored = 1 AND mv.monitored = 1
			AND NOT EXISTS (SELECT 1 FROM arrview_removed_version mr WHERE mr.version_id = mv.id))
	ELSE e.monitored = 1 AND EXISTS (SELECT 1 FROM version mv WHERE mv.title_id = t.id AND mv.monitored = 1
		AND NOT EXISTS (SELECT 1 FROM arrview_removed_version mr WHERE mr.version_id = mv.id)) END)`

// Calendar lists the movie release dates and episode air dates from from to to (dates, both included), oldest
// first. Movie dates are the release-date choice (FirstReleasesSQL). Episodes come from the episode table (TVDB and
// the arrs). Only monitored titles (and monitored episodes) unless unmonitored; never a title with no library copy
// left (every version removed).
func Calendar(ctx context.Context, db *sql.DB, from, to string, unmonitored bool) ([]CalendarItem, error) {
	var out []CalendarItem
	// Titles in the library only, with or without unmonitored: a title every copy of which was removed is gone.
	live := ` AND ` + store.TitleInLibrarySQL
	mon := live + ` AND t.monitored = 1 AND EXISTS (SELECT 1 FROM version v WHERE v.title_id = t.id AND v.monitored = 1
		AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id))`
	if unmonitored {
		mon = live
	}
	rows, err := db.QueryContext(ctx, `WITH best (title_id, type, date) AS (`+FirstReleasesBetweenSQL+`)
	SELECT t.id, t.name, COALESCE(t.year, 0), t.anime,
		t.monitored AND EXISTS (SELECT 1 FROM version v WHERE v.title_id = t.id AND v.monitored = 1
			AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)), best.type, best.date,
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

	// An episode the metadata source removed (removed_at; kept for its files and history) is not on the calendar.
	epLive := live + ` AND e.removed_at IS NULL`
	epMon := epLive + ` AND ` + EpisodeMonitoredSQL
	if unmonitored {
		epMon = epLive
	}
	rows, err = db.QueryContext(ctx, `SELECT t.id, t.name, COALESCE(t.year, 0), t.anime, `+EpisodeMonitoredSQL+`,
		e.id, e.season, e.number, e.name, e.air_date, COALESCE(e.air_date_utc_at, 0), COALESCE(sm.time_zone, ''),
		`+DayFirstAirSQL+`,
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
		if !MadeUpAir(it.Date, zone, firstMs) {
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

// DayFirstAirSQL is the earliest stored air instant of the episode's season and air date (an episode aliased e), 0
// for none: MadeUpAir's firstMs.
const DayFirstAirSQL = `COALESCE((SELECT MIN(x.air_date_utc_at) FROM episode x WHERE x.title_id = e.title_id
	AND x.season = e.season AND x.air_date = e.air_date), 0)`

// SeriesZoneSQL is the series' time zone of an episode aliased e (empty for none): MadeUpAir's zone.
const SeriesZoneSQL = `COALESCE((SELECT sm.time_zone FROM series_meta sm WHERE sm.title_id = e.title_id), '')`

// MadeUpAir reports whether an episode's stored instant was made up by the refresh for a date without a time, so a
// page shows the nominal date rather than a moment. The instant is the episode's own (an import brings real ones
// without a series air time); it is made up when the day's first episode of the season (firstMs) sits at the
// refresh's midnight in the series' zone: the refresh spaces the later ones of that day a runtime apart from it.
// The stored instant still decides whether the episode has aired.
func MadeUpAir(date, zone string, firstMs int64) bool {
	mid, ok := tvrefresh.MidnightAir(date, zone)
	return ok && firstMs == store.Millis(mid)
}
