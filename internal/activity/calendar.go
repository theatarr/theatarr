package activity

import (
	"context"
	"database/sql"
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

// Calendar lists the movie release dates and episode air dates from from to to (dates, both included), oldest
// first. Movie dates prefer a successful TMDb date refresh, else the arrs' dates or TMDb's US dates. Episodes come
// from the episode table (TVDB and the arrs). Only monitored titles (and monitored episodes) unless unmonitored.
func Calendar(ctx context.Context, db *sql.DB, from, to string, unmonitored bool) ([]CalendarItem, error) {
	var out []CalendarItem
	mon := ` AND t.monitored = 1 AND EXISTS (SELECT 1 FROM version v WHERE v.title_id = t.id AND v.monitored = 1)`
	if unmonitored {
		mon = ``
	}
	// Per title and type: canonical refreshed dates, else the arr's dates, else TMDb's earliest US date.
	rows, err := db.QueryContext(ctx, `WITH d AS (
		SELECT title_id, type, MIN(date) AS date, 0 AS pref FROM title_release_date d WHERE country = ''
			AND (source = 'tmdb-art' OR NOT EXISTS (SELECT 1 FROM title_meta m
				WHERE m.title_id = d.title_id AND m.movie_dates_at IS NOT NULL))
			AND type IN ('theatrical', 'digital', 'physical') GROUP BY title_id, type
		UNION ALL
		SELECT title_id, type, MIN(date), 1 FROM title_release_date d WHERE source = 'tmdb' AND country = 'US'
			AND NOT EXISTS (SELECT 1 FROM title_meta m WHERE m.title_id = d.title_id AND m.movie_dates_at IS NOT NULL)
			AND type IN ('theatrical', 'digital', 'physical') GROUP BY title_id, type),
	best AS (SELECT title_id, type, date FROM d d1 WHERE pref = (SELECT MIN(pref) FROM d d2
		WHERE d2.title_id = d1.title_id AND d2.type = d1.type))
	SELECT t.id, t.name, COALESCE(t.year, 0), t.anime, t.monitored, best.type, best.date,
		EXISTS (SELECT 1 FROM version v WHERE v.title_id = t.id AND v.state IN ('filled', 'cutoff_met')
			AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id))
	FROM best JOIN title t ON t.id = best.title_id
	WHERE t.kind = 'movie' AND best.date >= ? AND best.date <= ?`+mon, from, to)
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
	rows, err = db.QueryContext(ctx, `SELECT t.id, t.name, COALESCE(t.year, 0), t.anime, t.monitored AND e.monitored,
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
