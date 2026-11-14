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
// lists' Next release and its sort, and the title page's dates: per movie and release type (in cinemas, digital,
// physical), the type's first date, from the canonical dates (country '', the successful TMDb refresh when there is
// one, else the arrs' dates), else from TMDb's US dates while the title has no refresh. Later dates of a type (a
// re-release, a streaming service picking an old film up) are never the type's date. Display only: what may be
// fetched reads availability.Dates.

// releaseTypesSQL is CalendarTypes as an SQL list.
const releaseTypesSQL = `('theatrical', 'digital', 'physical')`

// FirstReleaseSQL is the SQL value of the release-date choice for title title and release type typ (SQL
// expressions, such as "d.title_id" and "d.type"): the type's first date, YYYY-MM-DD, or NULL.
func FirstReleaseSQL(title, typ string) string {
	refreshed := `EXISTS (SELECT 1 FROM title_meta fm WHERE fm.title_id = f.title_id AND fm.movie_dates_at IS NOT NULL)`
	return `COALESCE((SELECT MIN(substr(f.date, 1, 10)) FROM title_release_date f WHERE f.title_id = ` + title + `
			AND f.type = ` + typ + ` AND f.country = '' AND (f.source = 'tmdb-art' OR NOT ` + refreshed + `)),
		(SELECT MIN(substr(f.date, 1, 10)) FROM title_release_date f WHERE f.title_id = ` + title + `
			AND f.type = ` + typ + ` AND f.source = 'tmdb' AND f.country = 'US' AND NOT ` + refreshed + `))`
}

// FirstReleaseRowSQL is the condition that release date row d (title_release_date) is its title's chosen date of its
// type (FirstReleaseSQL), one of CalendarTypes.
var FirstReleaseRowSQL = `d.type IN ` + releaseTypesSQL + ` AND substr(d.date, 1, 10) = ` + FirstReleaseSQL("d.title_id", "d.type")

// FirstReleasesBetweenSQL selects title_id, type and date of every chosen release date (FirstReleaseSQL) from the
// first to the second argument (YYYY-MM-DD, both included).
var FirstReleasesBetweenSQL = `SELECT d.title_id, d.type, substr(d.date, 1, 10) FROM title_release_date d
	WHERE substr(d.date, 1, 10) >= ? AND substr(d.date, 1, 10) <= ? AND ` + FirstReleaseRowSQL + `
	GROUP BY d.title_id, d.type`

// FirstReleases reads the release-date choice (FirstReleaseSQL) of titles ids: by title id, release type to date.
func FirstReleases(ctx context.Context, db *sql.DB, ids []any) (map[int64]map[string]string, error) {
	out := map[int64]map[string]string{}
	for chunk := range slices.Chunk(ids, 500) {
		rows, err := db.QueryContext(ctx, `SELECT d.title_id, d.type, substr(d.date, 1, 10) FROM title_release_date d
			WHERE d.title_id IN (`+store.Placeholders(len(chunk))+`) AND `+FirstReleaseRowSQL+` GROUP BY d.title_id, d.type`,
			chunk...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var typ, date string
			if err := rows.Scan(&id, &typ, &date); err != nil {
				rows.Close()
				return nil, err
			}
			if out[id] == nil {
				out[id] = map[string]string{}
			}
			out[id][typ] = date
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Calendar lists the movie release dates and episode air dates from from to to (dates, both included), oldest
// first. Movie dates are the release-date choice (FirstReleaseSQL). Episodes come from the episode table (TVDB and
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
