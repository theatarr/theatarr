package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// One-time import from Radarr/Sonarr (internal/arrimport): one title at a time, added to one library by the
// Radarr/Sonarr list rules (exclusions, a library row in any state is a choice already made, fit, never a search).
// A copy the import adds starts unmonitored, so no Wanted or RSS run picks it up before the import has applied its
// episode monitoring and linked its files; the import then gives it the instance's monitored flag
// (SetImportedMonitored).

// ArrImportTitle is one title of a one-time Radarr or Sonarr import, with the instance's monitoring.
type ArrImportTitle struct {
	Kind           policy.Kind
	TmdbID, TvdbID int64
	ImdbID, Name   string
	Year           int
	SeriesType     string // Sonarr
	Monitored      bool
	// MinimumAvailability is Radarr's: announced, inCinemas or released; anything else is released.
	MinimumAvailability string
	// MonitorNew is Sonarr's monitorNewItems: all or none; anything else is "" (Theatarr's default, all).
	MonitorNew string
	// Seasons are Sonarr's seasons' monitored flags.
	Seasons []Season
}

// EpisodeMonitor is one Sonarr episode's monitored flag, by TVDB season and number.
type EpisodeMonitor struct {
	Season, Number int
	Monitored      bool
}

// importConfig is the list configuration an import of t into library key runs with: lists' rules (Fits only, one
// library), no search, and the copy created unmonitored.
func importConfig(key string, t ArrImportTitle) (movieIntakeConfig, error) {
	versions, err := json.Marshal([]string{key})
	if err != nil {
		return movieIntakeConfig{}, err
	}
	cfg := movieIntakeConfig{versions: string(versions), monitor: "movieOnly", minimum: "released", listType: "radarr"}
	if t.Kind == policy.KindSeries {
		cfg.listType = "sonarr"
	}
	switch t.MinimumAvailability {
	case "announced", "inCinemas", "released":
		cfg.minimum = t.MinimumAvailability
	}
	return cfg, nil
}

// ImportArrTitle classifies (dryRun) or adds one title to library key by the Radarr/Sonarr list rules, with no
// search. change is add_title, add_library, present, excluded or skipped (does not fit the library); titleID is the
// title's id in Theatarr, 0 when it has none. A copy it adds (add_title, add_library) is unmonitored: the caller
// applies the series' episode monitoring (ImportEpisodeMonitoring), links its files, then calls
// SetImportedMonitored with t.Monitored. A movie copy takes t's minimum availability.
func (s *MovieIntake) ImportArrTitle(ctx context.Context, key string, t ArrImportTitle, dryRun bool) (change string,
	titleID int64, err error) {
	if err := s.init(); err != nil {
		return "", 0, err
	}
	if t.Kind != policy.KindMovie && t.Kind != policy.KindSeries {
		return "", 0, fmt.Errorf("catalog: bad kind %q", t.Kind)
	}
	cfg, err := importConfig(key, t)
	if err != nil {
		return "", 0, err
	}
	keys := []string{key}
	series := arrSeries{TvdbID: t.TvdbID, TmdbID: t.TmdbID, ImdbID: t.ImdbID, Title: t.Name, Year: t.Year,
		SeriesType: t.SeriesType}
	summary := tmdb.MovieSummary{ID: int(t.TmdbID), Title: t.Name}
	var m *tmdb.Movie // a new movie's TMDb details, fetched once for the anime fit and the add
	if t.Kind == policy.KindSeries {
		change, titleID, err = s.previewSeries(ctx, cfg, keys, series)
	} else if change, titleID, err = s.previewMovie(ctx, cfg, keys, t.TmdbID); err == nil && titleID == 0 {
		// A new movie for a library with an anime setting fits by TMDb's details, as addMovie decides it.
		vp := s.Policy.Versions[key]
		animeRule := vp != nil && vp.Anime != nil
		if animeRule && (change == "add_title" || change == "skipped") || !dryRun && change == "add_title" {
			if m, err = s.TMDb.MovieDetails(ctx, summary.ID); err == nil && animeRule {
				change, err = s.previewChange(ctx, cfg, keys, 0, false, tmdbAnime(m), policy.KindMovie)
			}
		}
	}
	if err != nil {
		return "", 0, err
	}
	if dryRun || change != "add_title" && change != "add_library" {
		return change, titleID, nil
	}
	var id int64
	var added bool
	switch {
	case t.Kind == policy.KindSeries:
		id, added, err = s.addSeries(ctx, series, cfg)
	case change == "add_title":
		if m.ID != summary.ID {
			return "", 0, fmt.Errorf("TMDb movie id %d does not match %d", m.ID, summary.ID)
		}
		id, added, err = s.addNewMovie(ctx, summary, m, cfg, 0)
	default:
		added, err = s.addMovie(ctx, summary, cfg, 0)
	}
	if err != nil {
		return "", 0, err
	}
	if titleID == 0 {
		titleID = id
	}
	if !added {
		return "skipped", titleID, nil
	}
	return change, titleID, nil
}

// ImportNeedsEpisodes reports whether the import should refresh a series' episode list before bringing its
// monitoring and files over: it has no episode rows, or its list is not from TVDB while TVDB is configured.
func (s *MovieIntake) ImportNeedsEpisodes(ctx context.Context, titleID int64, tvdbConfigured bool) (bool, error) {
	var episodes int
	var source string
	if err := s.Store.Reader().QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM episode WHERE title_id = ? AND
		removed_at IS NULL), COALESCE((SELECT source FROM series_meta WHERE title_id = ?), '')`, titleID, titleID).
		Scan(&episodes, &source); err != nil {
		return false, err
	}
	return episodes == 0 || tvdbConfigured && source != "tvdb", nil
}

// ImportEpisodeMonitoring brings Sonarr's monitoring over to library key's copy of the series, in one transaction,
// only when the series' episode list comes from TVDB (Sonarr's numbering); applied is false, and nothing is written,
// otherwise. It sets monitor new seasons (t.MonitorNew, when all or none), each of t.Seasons, then each episode
// Theatarr has by season and number, on the copy's episode states and on the episodes' own monitored flag, and
// recomputes the copy's state.
func (s *MovieIntake) ImportEpisodeMonitoring(ctx context.Context, titleID int64, key string, t ArrImportTitle,
	eps []EpisodeMonitor) (applied bool, err error) {
	if err := s.init(); err != nil {
		return false, err
	}
	now := s.Now()
	ms := store.Millis(now)
	// The flags as [season, flag] and [season, number, flag], the last one given winning.
	seasonOn := map[int]bool{}
	for _, se := range t.Seasons {
		seasonOn[se.Number] = se.Monitored
	}
	seasons := make([][2]any, 0, len(seasonOn))
	for n, on := range seasonOn {
		seasons = append(seasons, [2]any{n, on})
	}
	episodeOn := map[[2]int]bool{}
	for _, e := range eps {
		episodeOn[[2]int{e.Season, e.Number}] = e.Monitored
	}
	episodes := make([][3]any, 0, len(episodeOn))
	for se, on := range episodeOn {
		episodes = append(episodes, [3]any{se[0], se[1], on})
	}
	seasonsJSON, err := json.Marshal(seasons)
	if err != nil {
		return false, err
	}
	episodesJSON, err := json.Marshal(episodes)
	if err != nil {
		return false, err
	}
	err = s.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		applied = false
		var source string
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(source, '') FROM series_meta WHERE title_id = ?`, titleID).
			Scan(&source)
		if errors.Is(err, sql.ErrNoRows) || err == nil && source != "tvdb" {
			return nil
		}
		if err != nil {
			return err
		}
		var vid int64
		err = tx.QueryRowContext(ctx, `SELECT v.id FROM version v WHERE v.title_id = ? AND v.policy_key = ? AND NOT
			EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)`, titleID, key).Scan(&vid)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			setNew := t.MonitorNew == MonitorAll || t.MonitorNew == MonitorNone
			if setNew || len(seasonOn) > 0 {
				m, err := LoadMonitor(ctx, tx, vid)
				if err != nil {
					return err
				}
				if setNew {
					m.New = t.MonitorNew
				}
				for n, on := range seasonOn {
					m.Seasons[n] = on
				}
				if err := SaveMonitor(ctx, tx, vid, m, now); err != nil {
					return err
				}
			}
			// The seasons' episodes, then each episode Theatarr has by season and number, on the copy's states.
			for _, q := range []struct{ flag, match, flags string }{
				{`'$[1]'`, `e.season = json_extract(j.value, '$[0]')`, string(seasonsJSON)},
				{`'$[2]'`, `e.season = json_extract(j.value, '$[0]') AND e.number = json_extract(j.value, '$[1]')
					AND e.removed_at IS NULL`, string(episodesJSON)},
			} {
				if _, err := tx.ExecContext(ctx, `INSERT INTO episode_version_state (version_id, episode_id, monitored,
					state, updated_at) SELECT ?, e.id, json_extract(j.value, `+q.flag+`), 'wanted', ? FROM json_each(?) j, episode e
					WHERE e.title_id = ? AND `+q.match+` ON CONFLICT (version_id, episode_id) DO UPDATE SET
					monitored = excluded.monitored, updated_at = excluded.updated_at
					WHERE monitored <> excluded.monitored`, vid, ms, q.flags, titleID); err != nil {
					return err
				}
			}
		}
		// An episode is monitored when any version monitors it (as manage and the arr import keep it).
		if _, err := tx.ExecContext(ctx, `UPDATE episode SET monitored = m, updated_at = ? FROM (SELECT episode_id,
			MAX(monitored) AS m FROM episode_version_state WHERE episode_id IN (SELECT id FROM episode
			WHERE title_id = ?) GROUP BY episode_id) st WHERE st.episode_id = episode.id
			AND episode.title_id = ? AND episode.monitored <> st.m`, ms, titleID, titleID); err != nil {
			return err
		}
		// The copy's summary from its episode states, as the library keeps it after an import.
		if _, err := tx.ExecContext(ctx, `UPDATE version SET state = st.state, updated_at = ? FROM (SELECT CASE
			WHEN EXISTS (SELECT 1 FROM episode_version_state es JOIN episode e ON e.id = es.episode_id
				WHERE es.version_id = v.id AND es.monitored = 1 AND es.state = 'wanted'
				AND e.air_date_utc_at IS NOT NULL AND e.air_date_utc_at <= ?) THEN 'wanted'
			WHEN EXISTS (SELECT 1 FROM episode_version_state WHERE version_id = v.id AND state = 'filled') THEN 'filled'
			WHEN EXISTS (SELECT 1 FROM episode_version_state WHERE version_id = v.id AND state = 'cutoff_met')
				THEN 'cutoff_met'
			ELSE 'wanted' END AS state, v.id AS id FROM version v WHERE v.title_id = ? AND v.policy_key = ?) st
			WHERE version.id = st.id AND version.state <> st.state`, ms, ms, titleID, key); err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, err
}

// SetImportedMonitored gives library key's copy of the title the instance's monitored flag once the import has
// applied its monitoring and linked its files (the last step for a copy ImportArrTitle added), and keeps the title's
// own flag in line.
func (s *MovieIntake) SetImportedMonitored(ctx context.Context, titleID int64, key string, monitored bool) error {
	if err := s.init(); err != nil {
		return err
	}
	now := s.Now()
	return s.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE version SET monitored = ?, updated_at = ? WHERE title_id = ? AND
			policy_key = ? AND monitored <> ?`, monitored, store.Millis(now), titleID, key, monitored); err != nil {
			return err
		}
		return SyncMonitored(ctx, tx, titleID, now)
	})
}
