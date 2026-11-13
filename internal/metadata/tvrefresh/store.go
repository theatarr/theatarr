package tvrefresh

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/store"
)

// row is a stored episode as the refresh reads it.
type row struct {
	id                              int64
	season, number                  int
	extID                           sql.NullInt64 // the source's id column (tvdb_episode_id or tmdb_episode_id)
	removed                         sql.NullInt64
	name                            string
	airDate                         sql.NullString
	airUTC                          sql.NullInt64
	runtime                         sql.NullInt64
	airsBeforeS, airsBeforeE, airsA sql.NullInt64
	keep                            bool // something hangs off it: a file, a link, a download
}

func (r *row) slot() slot { return slot{r.season, r.number} }

// store writes a fetched series in tx: episodes (match, move, insert, delete or mark removed), absolute numbers,
// per-version states of new episodes, scene names, title identity and series_meta.
func (s *Service) store(ctx context.Context, tx *sql.Tx, t *titleRow, sh *show, res *Result) error {
	now := s.now()
	ms := store.Millis(now)
	idCol := "tvdb_episode_id"
	if sh.source == SourceTMDb {
		idCol = "tmdb_episode_id"
	}
	rows, err := s.loadRows(ctx, tx, t.id, idCol)
	if err != nil {
		return err
	}
	hadEpisodes := false
	for _, r := range rows {
		if !r.removed.Valid {
			hadEpisodes = true
			break
		}
	}

	// Sonarr Tv/RefreshEpisodeService.cs:56-79 matches TVDB episodes by slot, including recreated TVDB ids.
	// TMDb retains source-id matching when episodes move.
	bySlot := map[slot]*row{}
	byExt := map[int64]*row{}
	for _, r := range rows {
		bySlot[r.slot()] = r
		if r.extID.Valid {
			byExt[r.extID.Int64] = r
		}
	}
	match := make([]*row, len(sh.episodes)) // per episode
	used := map[int64]bool{}
	for i, e := range sh.episodes {
		if r := bySlot[e.slot()]; r != nil && sh.source == SourceTVDB {
			match[i], used[r.id] = r, true
		}
	}
	for i, e := range sh.episodes {
		if match[i] != nil || sh.source == SourceTVDB {
			continue
		}
		if r := byExt[int64(e.extID)]; r != nil && !used[r.id] {
			match[i], used[r.id] = r, true
		}
	}
	for i, e := range sh.episodes {
		if match[i] != nil {
			continue
		}
		if r := bySlot[e.slot()]; r != nil && !r.extID.Valid && !used[r.id] {
			match[i], used[r.id] = r, true
		}
	}
	// The source ids each row ends with: a matched row takes its episode's id; an unmatched row keeps its id unless
	// a matched row takes it. Ids that move are cleared first (the unique index on title and id).
	want := make([]sql.NullInt64, len(sh.episodes))
	claimed := map[int64]bool{}
	for i := range sh.episodes {
		id := int64(sh.episodes[i].extID)
		want[i], claimed[id] = sql.NullInt64{Int64: id, Valid: true}, true
	}
	for i, r := range match {
		if r == nil || !r.extID.Valid || (want[i].Valid && r.extID.Int64 == want[i].Int64) {
			continue
		}
		if err := s.exec(ctx, tx, `UPDATE episode SET `+idCol+` = NULL WHERE id = ?`, r.id); err != nil {
			return err
		}
		r.extID = sql.NullInt64{}
	}
	for _, r := range rows {
		if !used[r.id] && r.extID.Valid && claimed[r.extID.Int64] {
			if err := s.exec(ctx, tx, `UPDATE episode SET `+idCol+` = NULL WHERE id = ?`, r.id); err != nil {
				return err
			}
			r.extID = sql.NullInt64{}
		}
	}

	// Rows the source no longer lists are deleted, or, with something hanging off them, marked removed and parked
	// off a slot an episode needs.
	need := map[slot]bool{}
	for _, e := range sh.episodes {
		need[e.slot()] = true
	}
	for _, r := range rows {
		if used[r.id] {
			continue
		}
		if !r.keep {
			if err := s.exec(ctx, tx, `DELETE FROM episode WHERE id = ?`, r.id); err != nil {
				return err
			}
			res.Deleted++
			continue
		}
		set, args := []string{}, []any{}
		if !r.removed.Valid {
			set, args = append(set, "removed_at = ?"), append(args, ms)
			res.Removed++
		}
		if need[r.slot()] {
			set, args = append(set, "number = ?"), append(args, -r.id)
		}
		if len(set) > 0 {
			if err := s.exec(ctx, tx, `UPDATE episode SET `+strings.Join(set, ", ")+`, updated_at = ? WHERE id = ?`,
				append(args, ms, r.id)...); err != nil {
				return err
			}
		}
	}

	// Moves: matched rows off their episode's slot go to a free number first, then to their slot.
	var movers []int
	for i, r := range match {
		if r != nil && r.slot() != sh.episodes[i].slot() {
			if err := s.exec(ctx, tx, `UPDATE episode SET number = ? WHERE id = ?`, -r.id, r.id); err != nil {
				return err
			}
			movers = append(movers, i)
		}
	}
	for _, i := range movers {
		e, r := sh.episodes[i], match[i]
		if err := s.exec(ctx, tx, `UPDATE episode SET season = ?, number = ?, updated_at = ? WHERE id = ?`, e.season,
			e.number, ms, r.id); err != nil {
			return err
		}
		r.season, r.number = e.season, e.number
		res.Moved++
	}

	// Updates and inserts.
	var added []int64
	for i := range sh.episodes {
		e := &sh.episodes[i]
		var utc any
		if e.airUTC != nil {
			utc = store.Millis(*e.airUTC)
		}
		r := match[i]
		if r == nil {
			id, err := s.insert(ctx, tx, `INSERT INTO episode (title_id, season, number, name, air_date, air_date_utc_at,
				runtime_minutes, airs_before_season, airs_before_episode, airs_after_season, monitored, `+idCol+`,
				created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`, t.id, e.season, e.number,
				e.name, nullStr(e.airDate), utc, intPtr(e.runtime), intPtr(e.airsBeforeS), intPtr(e.airsBeforeE),
				intPtr(e.airsAfterS), want[i], ms, ms)
			if err != nil {
				return err
			}
			added = append(added, id)
			res.Added++
			if err := s.absolute(ctx, tx, t.id, id, e.absolute, sh.source); err != nil {
				return err
			}
			continue
		}
		set, args := r.changes(e, want[i], utc, idCol)
		if len(set) > 0 {
			if err := s.exec(ctx, tx, `UPDATE episode SET `+strings.Join(set, ", ")+`, updated_at = ? WHERE id = ?`,
				append(args, ms, r.id)...); err != nil {
				return err
			}
			res.Updated++
		}
		if err := s.absolute(ctx, tx, t.id, r.id, e.absolute, sh.source); err != nil {
			return err
		}
	}

	if err := s.states(ctx, tx, t, added, hadEpisodes, sh.status, now, res); err != nil {
		return err
	}
	if err := s.fillTitle(ctx, tx, t, sh, ms); err != nil {
		return err
	}
	if sh.source == SourceTVDB {
		if err := s.sceneMappings(ctx, tx, t.id, sh.aliases); err != nil {
			return err
		}
	}
	var status any = sh.status
	_, err = tx.ExecContext(ctx, `INSERT INTO series_meta (title_id, source, source_id, status, air_time, time_zone,
		episodes, refreshed_at, attempted_at, error, original_language) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?)
		ON CONFLICT (title_id) DO UPDATE SET source = excluded.source, source_id = excluded.source_id,
		status = excluded.status, air_time = excluded.air_time, time_zone = excluded.time_zone,
		episodes = excluded.episodes, refreshed_at = excluded.refreshed_at, attempted_at = excluded.attempted_at,
		error = '', added_at = NULL,
		original_language = COALESCE(NULLIF(excluded.original_language, ''), series_meta.original_language)`,
		t.id, sh.source, sh.id, status, sh.airTime, sh.zone, len(sh.episodes), ms, ms, strings.TrimSpace(sh.language))
	return err
}

// changes are the SET clauses that bring a matched row in line with its episode; extID is the source id it is to
// have. The row takes the source's values and stops being removed.
func (r *row) changes(e *remoteEp, extID sql.NullInt64, utc any, idCol string) (set []string, args []any) {
	add := func(col string, v any) {
		set, args = append(set, col+" = ?"), append(args, v)
	}
	if !r.extID.Valid && extID.Valid {
		add(idCol, extID.Int64)
	}
	if r.removed.Valid {
		add("removed_at", nil)
	}
	if r.name != e.name {
		add("name", e.name)
	}
	if !sameStr(r.airDate, e.airDate) {
		add("air_date", nullStr(e.airDate))
	}
	if !sameInt(r.airUTC, utc) {
		add("air_date_utc_at", utc)
	}
	if !sameInt(r.runtime, intPtr(e.runtime)) {
		add("runtime_minutes", intPtr(e.runtime))
	}
	if !sameInt(r.airsBeforeS, intPtr(e.airsBeforeS)) {
		add("airs_before_season", intPtr(e.airsBeforeS))
	}
	if !sameInt(r.airsBeforeE, intPtr(e.airsBeforeE)) {
		add("airs_before_episode", intPtr(e.airsBeforeE))
	}
	if !sameInt(r.airsA, intPtr(e.airsAfterS)) {
		add("airs_after_season", intPtr(e.airsAfterS))
	}
	return set, args
}

// absoluteSource is the episode_number source the refresh writes absolute numbers under.
const absoluteSource = "tvdb"

// absolute makes the episode's absolute_tvdb number the source's; numbers from other sources are replaced.
func (s *Service) absolute(ctx context.Context, tx *sql.Tx, titleID, episodeID int64, abs *int, source string) error {
	if source != SourceTVDB {
		return nil
	}
	q := `DELETE FROM episode_number WHERE episode_id = ? AND scheme = 'absolute_tvdb'`
	args := []any{episodeID}
	if abs != nil {
		q += ` AND number <> ?`
		args = append(args, *abs)
	}
	if err := s.exec(ctx, tx, q, args...); err != nil {
		return err
	}
	if abs == nil {
		return nil
	}
	return s.exec(ctx, tx, `INSERT OR IGNORE INTO episode_number (episode_id, title_id, scheme, number, source)
		VALUES (?, ?, 'absolute_tvdb', ?, ?)`, episodeID, titleID, *abs, absoluteSource)
}

// loadRows reads the title's episodes with what the refresh needs to match and keep them.
func (s *Service) loadRows(ctx context.Context, tx *sql.Tx, titleID int64, idCol string) ([]*row, error) {
	q := `SELECT e.id, e.season, e.number, e.` + idCol + `, e.removed_at, e.name, e.air_date, e.air_date_utc_at,
		e.runtime_minutes, e.airs_before_season, e.airs_before_episode, e.airs_after_season,
		EXISTS (SELECT 1 FROM version_file_episode f WHERE f.episode_id = e.id)
		OR EXISTS (SELECT 1 FROM link_episode l WHERE l.episode_id = e.id)
		OR EXISTS (SELECT 1 FROM episode_version_state v WHERE v.episode_id = e.id AND v.state <> 'wanted')
		OR EXISTS (SELECT 1 FROM download d, json_each(d.episodes) j WHERE d.title_id = e.title_id AND j.value = e.id)
		OR EXISTS (SELECT 1 FROM download d JOIN download_file f ON f.download_id = d.id, json_each(f.episodes) j
			WHERE d.title_id = e.title_id AND j.value = e.id)
		FROM episode e WHERE e.title_id = ? ORDER BY e.id`
	rs, err := tx.QueryContext(ctx, q, titleID)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []*row
	for rs.Next() {
		r := &row{}
		if err := rs.Scan(&r.id, &r.season, &r.number, &r.extID, &r.removed, &r.name, &r.airDate, &r.airUTC,
			&r.runtime, &r.airsBeforeS, &r.airsBeforeE, &r.airsA, &r.keep); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// fillTitle refreshes metadata identity (Sonarr Tv/RefreshSeriesService.cs:90-114), keeping user settings.
func (s *Service) fillTitle(ctx context.Context, tx *sql.Tx, t *titleRow, sh *show, ms int64) error {
	fill := func(set string, setArgs []any) error {
		return s.exec(ctx, tx, `UPDATE title SET `+set+`, updated_at = ? WHERE id = ?`, append(setArgs, ms, t.id)...)
	}
	if sh.name != "" {
		if err := fill("name = ?, sort_name = ?", []any{sh.name, strings.ToLower(sh.name)}); err != nil {
			return err
		}
	}
	if sh.year > 0 {
		if err := fill("year = ?", []any{sh.year}); err != nil {
			return err
		}
	}
	if sh.runtime > 0 {
		if err := fill("runtime_minutes = ?", []any{sh.runtime}); err != nil {
			return err
		}
	}
	if sh.imdbID != "" {
		if err := fill("imdb_id = ?", []any{sh.imdbID}); err != nil {
			return err
		}
	}
	if sh.tmdbID > 0 {
		return s.exec(ctx, tx, `UPDATE title SET tmdb_id = ? WHERE id = ? AND tmdb_id IS NULL AND NOT EXISTS
			(SELECT 1 FROM title o WHERE o.id <> ? AND o.kind = 'series' AND o.tmdb_id = ?)`, sh.tmdbID, t.id, t.id,
			sh.tmdbID)
	}
	return nil
}

// version is a series version as the state step reads it.
type version struct {
	id      int64
	key     string
	monitor catalog.Monitor
	// have maps the episode ids with a state to their monitored flag, by season.
	have     map[int64]bool
	seasonOn map[int]bool // a season with any state: whether any of them is monitored
}

// unmonitorBefore: a series that had no episodes and was not just added gets its new episodes that aired more than
// a day ago unmonitored, so they are not all searched (Sonarr's UnmonitorReaddedEpisodes).
const unmonitorBefore = 24 * time.Hour

// states creates the per-version states of the episodes a version has none for (new episodes, or a version turned
// on later), from the version's intent, and records seasons the intent did not know yet. New episode rows take monitored when any version monitors them.
func (s *Service) states(ctx context.Context, tx *sql.Tx, t *titleRow, added []int64, hadEpisodes bool, status string,
	now time.Time, res *Result) error {
	ms := store.Millis(now)
	vs, err := s.versions(ctx, tx, t.id)
	if err != nil {
		return err
	}
	// The live episodes after the write, with their season, number and air time.
	type ep struct {
		id             int64
		season, number int
		air            sql.NullInt64
	}
	var eps []ep
	rs, err := tx.QueryContext(ctx, `SELECT id, season, number, air_date_utc_at FROM episode WHERE title_id = ?
		AND removed_at IS NULL ORDER BY season, number`, t.id)
	if err != nil {
		return err
	}
	for rs.Next() {
		var e ep
		if err := rs.Scan(&e.id, &e.season, &e.number, &e.air); err != nil {
			rs.Close()
			return err
		}
		eps = append(eps, e)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return err
	}
	isNew := map[int64]bool{}
	for _, id := range added {
		isNew[id] = true
	}
	fresh := t.addedAt.Valid
	old := store.Millis(now.Add(-unmonitorBefore))
	monitoredNew := map[int64]bool{}
	for _, v := range vs {
		// Seasons the intent does not know: from the version's states when it has some, else a new season
		// (UpdateSeasons: monitored with monitor new items all, never season 0).
		changed := false
		for _, e := range eps {
			if _, ok := v.monitor.Seasons[e.season]; ok {
				continue
			}
			on, has := v.seasonOn[e.season]
			if !has {
				on = e.season != 0 && v.monitor.New == catalog.MonitorAll
			}
			v.monitor.Seasons[e.season], changed = on, true
		}
		wanted := false
		for _, e := range eps {
			if _, ok := v.have[e.id]; ok {
				continue
			}
			// GetMonitoredStatus: episode 0 of a season other than 1 never; else the season's choice.
			mon := v.monitor.Seasons[e.season] && !(e.number == 0 && e.season != 1)
			if mon && isNew[e.id] && !fresh && !hadEpisodes && e.air.Valid && e.air.Int64 < old {
				mon = false
			}
			if mon && isNew[e.id] {
				monitoredNew[e.id] = true
			}
			if err := s.exec(ctx, tx, `INSERT INTO episode_version_state (version_id, episode_id, monitored, state,
				updated_at) VALUES (?, ?, ?, 'wanted', ?) ON CONFLICT (version_id, episode_id) DO NOTHING`, v.id, e.id,
				mon, ms); err != nil {
				return err
			}
			res.States++
			if mon && e.air.Valid && e.air.Int64 <= ms {
				wanted = true
			}
		}
		if changed {
			if err := catalog.SaveMonitor(ctx, tx, v.id, v.monitor, now); err != nil {
				return err
			}
		}
		if wanted {
			if err := s.exec(ctx, tx, `UPDATE version SET state = 'wanted', updated_at = ? WHERE id = ?
				AND state <> 'wanted'`, ms, v.id); err != nil {
				return err
			}
		}
	}
	// Sonarr EpisodeMonitoredService.cs:51-166 applies the add's mode after the first episode refresh.
	for _, v := range vs {
		if err := catalog.ApplyPendingMonitor(ctx, tx, t.id, v.key, status, now); err != nil {
			return err
		}
	}
	for id := range monitoredNew {
		if err := s.exec(ctx, tx, `UPDATE episode SET monitored = 1 WHERE id = ?`, id); err != nil {
			return err
		}
	}
	// Episode monitored: monitored in any version (as the arr import keeps it).
	return s.exec(ctx, tx, `UPDATE episode SET monitored = m, updated_at = ? FROM (SELECT episode_id, MAX(monitored) AS m
		FROM episode_version_state GROUP BY episode_id) st WHERE st.episode_id = episode.id AND episode.title_id = ?
		AND episode.monitored <> st.m`, ms, t.id)
}

// versions are the title's series versions not removed from the views, with their intent and states.
func (s *Service) versions(ctx context.Context, tx *sql.Tx, titleID int64) ([]*version, error) {
	rs, err := tx.QueryContext(ctx, `SELECT v.id, v.policy_key FROM version v WHERE v.title_id = ? AND NOT EXISTS
		(SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id) ORDER BY v.id`, titleID)
	if err != nil {
		return nil, err
	}
	var out []*version
	for rs.Next() {
		v := &version{have: map[int64]bool{}, seasonOn: map[int]bool{}}
		if err := rs.Scan(&v.id, &v.key); err != nil {
			rs.Close()
			return nil, err
		}
		out = append(out, v)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return nil, err
	}
	for _, v := range out {
		if v.monitor, err = catalog.LoadMonitor(ctx, tx, v.id); err != nil {
			return nil, err
		}
		rs, err := tx.QueryContext(ctx, `SELECT s.episode_id, e.season, s.monitored FROM episode_version_state s
			JOIN episode e ON e.id = s.episode_id WHERE s.version_id = ?`, v.id)
		if err != nil {
			return nil, err
		}
		for rs.Next() {
			var id int64
			var season int
			var mon bool
			if err := rs.Scan(&id, &season, &mon); err != nil {
				rs.Close()
				return nil, err
			}
			v.have[id] = mon
			v.seasonOn[season] = v.seasonOn[season] || mon
		}
		rs.Close()
		if err := rs.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Service) exec(ctx context.Context, tx *sql.Tx, q string, args ...any) error {
	_, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("tvrefresh: %w", err)
	}
	return nil
}

func (s *Service) insert(ctx context.Context, tx *sql.Tx, q string, args ...any) (int64, error) {
	r, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("tvrefresh: %w", err)
	}
	return r.LastInsertId()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func intPtr(p *int) any {
	if p == nil {
		return nil
	}
	return int64(*p)
}

func sameStr(a sql.NullString, b string) bool {
	return (!a.Valid && b == "") || (a.Valid && a.String == b)
}

func sameInt(a sql.NullInt64, b any) bool {
	if b == nil {
		return !a.Valid
	}
	return a.Valid && strconv.FormatInt(a.Int64, 10) == fmt.Sprint(b)
}
