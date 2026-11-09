package catalog

// ApplyMonitor follows Sonarr src/NzbDrone.Core/Tv/EpisodeMonitoredService.cs @ cab419a (SetEpisodeMonitoredStatus).

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// Sonarr's MonitorTypes (addOptions.monitor), as the API names them.
const (
	MonitorTypeAll               = "all"
	MonitorTypeFuture            = "future"
	MonitorTypeMissing           = "missing"
	MonitorTypeExisting          = "existing"
	MonitorTypeFirstSeason       = "firstSeason"
	MonitorTypeLastSeason        = "lastSeason"
	MonitorTypeLatestSeason      = "latestSeason" // obsolete in Sonarr: lastSeason
	MonitorTypePilot             = "pilot"
	MonitorTypeRecent            = "recent"
	MonitorTypeMonitorSpecials   = "monitorSpecials"
	MonitorTypeUnmonitorSpecials = "unmonitorSpecials"
	MonitorTypeNone              = "none"
	MonitorTypeSkip              = "skip"
	MonitorTypeUnknown           = "unknown"
)

// MonitorTypes lists Sonarr's MonitorTypes in enum order (an integer in a request is an index into it).
var MonitorTypes = []string{MonitorTypeUnknown, MonitorTypeAll, MonitorTypeFuture, MonitorTypeMissing,
	MonitorTypeExisting, MonitorTypeFirstSeason, MonitorTypeLastSeason, MonitorTypeLatestSeason, MonitorTypePilot,
	MonitorTypeRecent, MonitorTypeMonitorSpecials, MonitorTypeUnmonitorSpecials, MonitorTypeNone, MonitorTypeSkip}

// recentDays is Sonarr's "recent": aired in the last 90 days, or not yet aired.
const recentDays = 90

// ApplyMonitor applies Sonarr's add-time monitoring option (addOptions.monitor) to the title's series versions with
// these keys, as Sonarr's EpisodeMonitoredService does after the add's first refresh: each episode's per-version
// monitored flag from the option, then each season's choice (series_monitor) from Sonarr's season rule. seasons are
// the series' seasons the caller knows (the request's); the episodes' seasons are added. status is the series status
// (continuing, ended, upcoming; "" counts as continuing).
//
// "", unknown and skip change nothing: Sonarr keeps the request's seasons for them (its legacy v2 path for unknown,
// ignoreEpisodesWithFiles and the like, is not ported). Without episodes the mode is persisted until the first
// successful refresh, when the episode predicates and season rules can be applied together.
func ApplyMonitor(ctx context.Context, tx *sql.Tx, titleID int64, keys []string, monitor string, seasons []int,
	status string, now time.Time) error {
	if monitor == MonitorTypeLatestSeason {
		monitor = MonitorTypeLastSeason
	}
	switch monitor {
	case "", MonitorTypeUnknown, MonitorTypeSkip:
		return nil
	case MonitorTypeAll, MonitorTypeFuture, MonitorTypeMissing, MonitorTypeExisting, MonitorTypeFirstSeason,
		MonitorTypeLastSeason, MonitorTypePilot, MonitorTypeRecent, MonitorTypeMonitorSpecials,
		MonitorTypeUnmonitorSpecials, MonitorTypeNone:
	default:
		return fmt.Errorf("catalog: monitor %q: not one of Sonarr's monitor types", monitor)
	}
	vids, err := versionIDs(ctx, tx, titleID, keys)
	if err != nil || len(vids) == 0 {
		return err
	}
	type episode struct {
		id             int64
		season, number int
		air            sql.NullInt64
	}
	var eps []episode
	rows, err := tx.QueryContext(ctx, `SELECT id, season, number, air_date_utc_at FROM episode WHERE title_id = ?
		AND removed_at IS NULL ORDER BY season, number`, titleID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var e episode
		if err := rows.Scan(&e.id, &e.season, &e.number, &e.air); err != nil {
			rows.Close()
			return err
		}
		eps = append(eps, e)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(eps) == 0 {
		for _, vid := range vids {
			if _, err := tx.ExecContext(ctx, `UPDATE series_monitor SET pending_monitor = ?, updated_at = ? WHERE version_id = ?`,
				monitor, store.Millis(now), vid); err != nil {
				return err
			}
		}
		return nil
	}
	for _, vid := range vids {
		if _, err := tx.ExecContext(ctx, `UPDATE series_monitor SET pending_monitor = '' WHERE version_id = ?`, vid); err != nil {
			return err
		}
	}
	if monitor == MonitorTypeMonitorSpecials || monitor == MonitorTypeUnmonitorSpecials {
		// Only specials change; the other episodes and seasons keep their choices.
		return SetSeasonMonitored(ctx, tx, titleID, keys, 0, monitor == MonitorTypeMonitorSpecials, now)
	}
	known := map[int]bool{}
	for _, n := range seasons {
		known[n] = true
	}
	for _, e := range eps {
		known[e.season] = true
	}
	first, last := 0, 0
	for n := range known {
		if n > 0 && (first == 0 || n < first) {
			first = n
		}
		if n > last {
			last = n
		}
	}
	airing := !strings.EqualFold(status, "ended")
	ms := store.Millis(now)
	for _, vid := range vids {
		m, err := LoadMonitor(ctx, tx, vid)
		if err != nil {
			return err
		}
		monSeason := map[int]bool{}
		files, err := episodesWithFile(ctx, tx, vid)
		if err != nil {
			return err
		}
		for _, e := range eps {
			on := e.season > 0
			aired := e.air.Valid && e.air.Int64 < ms
			switch monitor {
			case MonitorTypeFuture:
				on = on && !aired
			case MonitorTypeMissing:
				on = on && !files[e.id]
			case MonitorTypeExisting:
				on = on && files[e.id]
			case MonitorTypePilot:
				on = on && e.season == first && e.number == 1
			case MonitorTypeFirstSeason:
				on = on && e.season == first
			case MonitorTypeLastSeason:
				on = on && e.season == last
			case MonitorTypeRecent:
				on = on && (!e.air.Valid || !aired || e.air.Int64 >= store.Millis(now.AddDate(0, 0, -recentDays)))
			case MonitorTypeNone:
				on = false
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO episode_version_state (version_id, episode_id, monitored,
					state, updated_at) VALUES (?, ?, ?, 'wanted', ?) ON CONFLICT (version_id, episode_id) DO UPDATE
					SET monitored = excluded.monitored, updated_at = excluded.updated_at
					WHERE monitored <> excluded.monitored`, vid, e.id, on, ms); err != nil {
				return err
			}
			if on {
				monSeason[e.season] = true
			}
		}
		// Sonarr's season rule: the last season is monitored for all, and for future while the series airs;
		// the first season never for pilot; any other season when one of its episodes is.
		for n := range known {
			switch {
			case n > 0 && n == last && (monitor == MonitorTypeAll || monitor == MonitorTypeFuture && airing):
				monSeason[n] = true
			case n == first && monitor == MonitorTypePilot:
				monSeason[n] = false
			case !monSeason[n]:
				monSeason[n] = false
			}
		}
		for n, on := range monSeason {
			m.Seasons[n] = on
		}
		if err := SaveMonitor(ctx, tx, vid, m, now); err != nil {
			return err
		}
	}
	return nil
}

// episodesWithFile are the episodes a version holds: its state is filled or cutoff_met, an arr file of the
// version has it, or an active link of the version does.
func episodesWithFile(ctx context.Context, tx *sql.Tx, versionID int64) (map[int64]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT episode_id FROM episode_version_state WHERE version_id = ?
		AND state IN ('filled', 'cutoff_met')
		UNION SELECT fe.episode_id FROM version_file_episode fe JOIN version_file vf ON vf.id = fe.version_file_id
		WHERE vf.version_id = ?
		UNION SELECT le.episode_id FROM link_episode le JOIN link l ON l.id = le.link_id
		JOIN link_version lv ON lv.link_id = l.id WHERE lv.version_id = ? AND l.state = 'active'`,
		versionID, versionID, versionID)
	if err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		out[id] = true
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return out, rows.Err()
}

// SetSeriesType keeps the arr API's user choice on the catalog row used by decide.
func SetSeriesType(ctx context.Context, tx *sql.Tx, titleID int64, seriesType string) error {
	if seriesType == "" {
		return nil
	}
	switch seriesType {
	case "standard", "daily", "anime":
	default:
		return fmt.Errorf("catalog: invalid series type %q", seriesType)
	}
	_, err := tx.ExecContext(ctx, `UPDATE title SET series_type = ? WHERE id = ? AND kind = 'series'`, seriesType, titleID)
	return err
}

// ApplyPendingMonitor consumes an add's choice once episodes exist. The caller skips versions still owned by an arr.
func ApplyPendingMonitor(ctx context.Context, tx *sql.Tx, titleID int64, key, status string, now time.Time) error {
	var mode string
	var vid int64
	err := tx.QueryRowContext(ctx, `SELECT v.id, m.pending_monitor FROM series_monitor m JOIN version v ON v.id = m.version_id
		WHERE v.title_id = ? AND v.policy_key = ?`, titleID, key).Scan(&vid, &mode)
	if err == sql.ErrNoRows || err == nil && mode == "" {
		return nil
	}
	if err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM episode WHERE title_id = ? AND removed_at IS NULL`, titleID).Scan(&count); err != nil || count == 0 {
		return err
	}
	m, err := LoadMonitor(ctx, tx, vid)
	if err != nil {
		return err
	}
	seasons := make([]int, 0, len(m.Seasons))
	for season := range m.Seasons {
		seasons = append(seasons, season)
	}
	return ApplyMonitor(ctx, tx, titleID, []string{key}, mode, seasons, status, now)
}
