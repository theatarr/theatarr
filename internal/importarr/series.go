package importarr

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/store"
)

// Series writing (Sonarr): scene mappings, episodes with their numbers, the per-episode state of the version, and
// the episode files with the episodes each holds.

// seriesState is a series version's state: wanted while a monitored episode that has aired has no file (or the
// series has no file at all), filled while a file is below the cutoff, cutoff_met otherwise.
func seriesState(t Title, now time.Time) string {
	files := map[int]*File{}
	for _, f := range t.Files {
		files[f.ArrID] = f
	}
	filled := false
	for _, e := range t.Episodes {
		f := files[e.FileArrID]
		if f == nil {
			if e.Monitored && e.AirDateUTC != nil && !e.AirDateUTC.After(now) {
				return "wanted"
			}
			continue
		}
		if f.CutoffNotMet {
			filled = true
		}
	}
	switch {
	case len(t.Files) == 0:
		return "wanted"
	case filled:
		return "filled"
	}
	return "cutoff_met"
}

// episodeState is one episode's state in the version: no file is wanted, a file below the cutoff filled.
func episodeState(f *File) string {
	switch {
	case f == nil:
		return "wanted"
	case f.CutoffNotMet:
		return "filled"
	}
	return "cutoff_met"
}

// sceneMappings makes the title's scene mappings from this source match want.
func (w *wtx) sceneMappings(ctx context.Context, titleID int64, source string, want []SceneMapping) error {
	type key struct {
		name                string
		season, sceneSeason int64 // -2 for none
	}
	opt := func(p *int) int64 {
		if p == nil {
			return -2
		}
		return int64(*p)
	}
	wanted := map[key]SceneMapping{}
	for _, m := range want {
		wanted[key{m.Name, opt(m.SeasonNumber), opt(m.SceneSeasonNumber)}] = m
	}
	rows, err := w.tx.QueryContext(ctx, `SELECT name, COALESCE(season_number, -2), COALESCE(scene_season_number, -2),
		scene_origin, comment FROM title_scene_mapping WHERE title_id = ? AND source = ?`, titleID, source)
	if err != nil {
		return err
	}
	type cur struct{ origin, comment string }
	have := map[key]cur{}
	for rows.Next() {
		var k key
		var c cur
		if err := rows.Scan(&k.name, &k.season, &k.sceneSeason, &c.origin, &c.comment); err != nil {
			rows.Close()
			return err
		}
		have[k] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	where := `title_id = ? AND source = ? AND name = ? AND COALESCE(season_number, -2) = ?
		AND COALESCE(scene_season_number, -2) = ?`
	for k, c := range have {
		m, ok := wanted[k]
		switch {
		case !ok:
			n, err := w.exec(ctx, `DELETE FROM title_scene_mapping WHERE `+where, titleID, source, k.name, k.season,
				k.sceneSeason)
			if err != nil {
				return err
			}
			w.changes.add("title_scene_mapping", opDelete, int(n))
		case m.SceneOrigin != c.origin || m.Comment != c.comment:
			if _, err := w.exec(ctx, `UPDATE title_scene_mapping SET scene_origin = ?, comment = ? WHERE `+where,
				m.SceneOrigin, m.Comment, titleID, source, k.name, k.season, k.sceneSeason); err != nil {
				return err
			}
			w.changes.add("title_scene_mapping", opUpdate, 1)
		}
	}
	for k, m := range wanted {
		if _, ok := have[k]; ok {
			continue
		}
		if _, err := w.exec(ctx, `INSERT INTO title_scene_mapping (title_id, source, name, season_number,
			scene_season_number, scene_origin, comment) VALUES (?, ?, ?, ?, ?, ?, ?)`, titleID, source, m.Name,
			dbValue(m.SeasonNumber), dbValue(m.SceneSeasonNumber), m.SceneOrigin, m.Comment); err != nil {
			return err
		}
		w.changes.add("title_scene_mapping", opInsert, 1)
	}
	return nil
}

// episodes stores a series' episodes (our Episode by title, season and number), their absolute and scene numbers
// (episode_number, source = the instance), the Seerr alias of each arr episode, and, for the owning instance, the
// version's per-episode state. It returns our episode id per arr episode id.
func (w *wtx) episodes(ctx context.Context, instance string, ref titleRef, t Title, sum *Summary) (map[int]int64, error) {
	files := map[int]*File{}
	for _, f := range t.Files {
		files[f.ArrID] = f
	}
	ids := map[int]int64{}
	keepAlias := map[int]bool{}
	for _, e := range t.Episodes {
		var airUTC any
		if e.AirDateUTC != nil {
			airUTC = store.Millis(*e.AirDateUTC)
		}
		vals := []kv{{"name", e.Name}, {"air_date", nullStr(e.AirDate)}, {"air_date_utc_at", airUTC},
			{"runtime_minutes", nullInt(e.Runtime)}}
		if _, err := w.upsert(ctx, "episode", []kv{{"title_id", ref.titleID}, {"season", e.Season}, {"number", e.Number}},
			vals, []kv{{"monitored", e.Monitored}, {"created_at", w.now}, {"updated_at", w.now}}, []kv{{"updated_at", w.now}}); err != nil {
			return nil, err
		}
		var id int64
		if err := w.tx.QueryRowContext(ctx, `SELECT id FROM episode WHERE title_id = ? AND season = ? AND number = ?`,
			ref.titleID, e.Season, e.Number).Scan(&id); err != nil {
			return nil, err
		}
		ids[e.ArrID] = id
		keepAlias[e.ArrID] = true
		if err := w.episodeNumbers(ctx, id, ref.titleID, instance, e); err != nil {
			return nil, err
		}
		if _, err := w.upsert(ctx, "arr_alias", []kv{{"instance", instance}, {"kind", "episode"}, {"arr_id", e.ArrID}},
			[]kv{{"target_id", id}}, []kv{{"created_at", w.now}}, nil); err != nil {
			return nil, err
		}
		f := files[e.FileArrID]
		sum.Episodes.Total++
		if e.Monitored {
			sum.Episodes.Monitored++
		}
		if f != nil {
			sum.Episodes.WithFile++
		}
		if !ref.owner {
			continue
		}
		var score any
		if f != nil && f.CustomFormatScore != nil {
			score = int64(*f.CustomFormatScore)
		}
		state := episodeState(f)
		if _, err := w.upsert(ctx, "episode_version_state", []kv{{"version_id", ref.versionID}, {"episode_id", id}},
			[]kv{{"monitored", e.Monitored}, {"state", state}, {"current_score", score},
				{"cutoff_met", state == "cutoff_met"}}, []kv{{"updated_at", w.now}}, []kv{{"updated_at", w.now}}); err != nil {
			return nil, err
		}
	}
	if ref.owner {
		// Episodes the arr no longer lists leave the version.
		live := make([]int64, 0, len(ids))
		for _, id := range ids {
			live = append(live, id)
		}
		n, err := w.deleteNotIn(ctx, "episode_version_state", "version_id = ?", []any{ref.versionID}, "episode_id", live)
		if err != nil {
			return nil, err
		}
		w.changes.add("episode_version_state", opDelete, int(n))
		if err := w.seriesMonitor(ctx, ref.versionID, t); err != nil {
			return nil, err
		}
	}
	if err := w.dropEpisodeAliases(ctx, instance, ref.titleID, keepAlias); err != nil {
		return nil, err
	}
	// Episode monitored: monitored in any version.
	n, err := w.exec(ctx, `UPDATE episode SET monitored = m, updated_at = ? FROM (SELECT episode_id, MAX(monitored) AS m
		FROM episode_version_state GROUP BY episode_id) s WHERE s.episode_id = episode.id AND episode.title_id = ?
		AND episode.monitored <> s.m`, w.now, ref.titleID)
	if err != nil {
		return nil, err
	}
	w.changes.add("episode", opUpdate, int(n))
	return ids, nil
}

// seriesMonitor keeps the arr's monitoring intent as the version's (series_monitor): monitorNewItems, so a season
// that appears after the import is monitored only when the arr would have monitored it, and the seasons' flags.
func (w *wtx) seriesMonitor(ctx context.Context, versionID int64, t Title) error {
	mn := catalog.MonitorAll
	if strings.EqualFold(t.MonitorNewItems, catalog.MonitorNone) {
		mn = catalog.MonitorNone
	}
	seasons := t.SeasonsMonitored
	if seasons == nil {
		seasons = map[int]bool{}
	}
	b, err := json.Marshal(seasons)
	if err != nil {
		return err
	}
	_, err = w.upsert(ctx, "series_monitor", []kv{{"version_id", versionID}},
		[]kv{{"monitor_new", mn}, {"seasons", string(b)}}, []kv{{"updated_at", w.now}}, []kv{{"updated_at", w.now}})
	return err
}

// episodeNumbers makes the episode's absolute and scene numbers from this source match the arr's. TVDB season and
// number are the episode row itself.
func (w *wtx) episodeNumbers(ctx context.Context, episodeID, titleID int64, source string, e Episode) error {
	type num struct {
		scheme string
		season int64 // -1 for none
		number int64
	}
	var want []num
	if e.Absolute != nil && *e.Absolute > 0 {
		want = append(want, num{"absolute_tvdb", -1, int64(*e.Absolute)})
	}
	if e.SceneSeason != nil && e.SceneNumber != nil {
		want = append(want, num{"scene", int64(*e.SceneSeason), int64(*e.SceneNumber)})
	}
	if e.SceneAbsolute != nil && *e.SceneAbsolute > 0 {
		want = append(want, num{"scene_absolute", -1, int64(*e.SceneAbsolute)})
	}
	rows, err := w.tx.QueryContext(ctx, `SELECT scheme, COALESCE(season, -1), number FROM episode_number
		WHERE episode_id = ? AND source = ?`, episodeID, source)
	if err != nil {
		return err
	}
	have := map[num]bool{}
	for rows.Next() {
		var n num
		if err := rows.Scan(&n.scheme, &n.season, &n.number); err != nil {
			rows.Close()
			return err
		}
		have[n] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	wanted := map[num]bool{}
	for _, n := range want {
		wanted[n] = true
	}
	for n := range have {
		if wanted[n] {
			continue
		}
		c, err := w.exec(ctx, `DELETE FROM episode_number WHERE episode_id = ? AND source = ? AND scheme = ?
			AND COALESCE(season, -1) = ? AND number = ?`, episodeID, source, n.scheme, n.season, n.number)
		if err != nil {
			return err
		}
		w.changes.add("episode_number", opDelete, int(c))
	}
	for _, n := range want {
		if have[n] {
			continue
		}
		var season any
		if n.season >= 0 {
			season = n.season
		}
		// Another source may hold the same number already (the unique rule is per episode, scheme and number).
		c, err := w.exec(ctx, `INSERT OR IGNORE INTO episode_number (episode_id, title_id, scheme, season, number, source)
			VALUES (?, ?, ?, ?, ?, ?)`, episodeID, titleID, n.scheme, season, n.number, source)
		if err != nil {
			return err
		}
		w.changes.add("episode_number", opInsert, int(c))
	}
	return nil
}

// dropEpisodeAliases removes this instance's episode aliases of the title that the arr no longer lists, then the
// title's episodes nothing refers to any more (no alias from any instance, no link). An episode the series metadata
// refresh stored (one with a TVDB or TMDb episode id) is the refresh's to keep or drop (internal/metadata/tvrefresh).
func (w *wtx) dropEpisodeAliases(ctx context.Context, instance string, titleID int64, keep map[int]bool) error {
	rows, err := w.tx.QueryContext(ctx, `SELECT a.arr_id FROM arr_alias a JOIN episode e ON e.id = a.target_id
		WHERE a.instance = ? AND a.kind = 'episode' AND e.title_id = ?`, instance, titleID)
	if err != nil {
		return err
	}
	var gone []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !keep[id] {
			gone = append(gone, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range gone {
		n, err := w.exec(ctx, `DELETE FROM arr_alias WHERE instance = ? AND kind = 'episode' AND arr_id = ?`, instance, id)
		if err != nil {
			return err
		}
		w.changes.add("arr_alias", opDelete, int(n))
	}
	n, err := w.exec(ctx, `DELETE FROM episode WHERE title_id = ? AND tvdb_episode_id IS NULL AND tmdb_episode_id IS NULL
		AND id NOT IN (SELECT target_id FROM arr_alias WHERE kind = 'episode')
		AND id NOT IN (SELECT episode_id FROM link_episode)`, titleID)
	if err != nil {
		return err
	}
	w.changes.add("episode", opDelete, int(n))
	return nil
}

// deleteNotIn deletes the rows of table matching where whose col is not in keep.
func (w *wtx) deleteNotIn(ctx context.Context, table, where string, args []any, col string, keep []int64) (int64, error) {
	rows, err := w.tx.QueryContext(ctx, "SELECT "+col+" FROM "+table+" WHERE "+where, args...)
	if err != nil {
		return 0, err
	}
	in := map[int64]bool{}
	for _, k := range keep {
		in[k] = true
	}
	var gone []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		if !in[id] {
			gone = append(gone, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var total int64
	for _, id := range gone {
		n, err := w.exec(ctx, "DELETE FROM "+table+" WHERE "+where+" AND "+col+" = ?", append(append([]any{}, args...), id)...)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// seriesFiles stores a series' episode files for the version (one version_file per arr file, with the episodes it
// holds), tied to their downloads and contents like movie files, and drops the instance's files of the version the
// arr no longer has.
func (w *wtx) seriesFiles(ctx context.Context, instance string, t Title, ref titleRef, episodes map[int]int64,
	imports map[int]importEvent, grabs map[string]grabEvent, contents map[string]int64, sum *Summary) error {
	var live []int64
	for _, f := range t.Files {
		vfID, err := w.file(ctx, instance, t.Kind, ref, f, imports, grabs, contents, sum)
		if err != nil {
			return err
		}
		live = append(live, vfID)
		var eps []int64
		for _, a := range f.EpisodeArrIDs {
			if id, ok := episodes[a]; ok {
				eps = append(eps, id)
			}
		}
		sort.Slice(eps, func(i, j int) bool { return eps[i] < eps[j] })
		for _, id := range eps {
			n, err := w.exec(ctx, `INSERT OR IGNORE INTO version_file_episode (version_file_id, episode_id) VALUES (?, ?)`,
				vfID, id)
			if err != nil {
				return err
			}
			w.changes.add("version_file_episode", opInsert, int(n))
		}
		n, err := w.deleteNotIn(ctx, "version_file_episode", "version_file_id = ?", []any{vfID}, "episode_id", eps)
		if err != nil {
			return err
		}
		w.changes.add("version_file_episode", opDelete, int(n))
	}
	n, err := w.deleteNotIn(ctx, "version_file", "instance = ? AND version_id = ?", []any{instance, ref.versionID}, "id", live)
	if err != nil {
		return err
	}
	w.changes.add("version_file", opDelete, int(n))
	return nil
}

// file upserts one arr file as a version_file row and its Seerr alias, and returns the row id.
func (w *wtx) file(ctx context.Context, instance, kind string, ref titleRef, f *File, imports map[int]importEvent,
	grabs map[string]grabEvent, contents map[string]int64, sum *Summary) (int64, error) {
	sum.Files.Total++
	var downloadID, releaseTitle, contentID any
	if imp, ok := imports[f.ArrID]; ok {
		sum.Files.WithImport++
		downloadID = nullStr(imp.downloadID)
		if imp.downloadID == "" {
			sum.Files.ManualImport++
		}
		if g, ok := grabs[imp.downloadID]; ok && imp.downloadID != "" && g.title != "" {
			releaseTitle = g.title
			sum.Files.WithReleaseTitle++
		} else if imp.title != "" {
			// No grab (a manual import): the import's source title is the release or folder name.
			releaseTitle = imp.title
			sum.Files.ReleaseTitleFromImport++
		}
		if imp.infoHash != "" {
			if id, ok := contents[imp.infoHash]; ok {
				contentID = id
				sum.Files.WithHash++
			}
		} else if g, ok := grabs[imp.downloadID]; ok && g.protocol == "usenet" {
			sum.Files.Usenet++
		}
	}
	langs, _ := json.Marshal(nonNil(f.Languages))
	mi := "{}"
	if len(f.MediaInfo) > 0 {
		mi = compactJSON(f.MediaInfo)
	}
	var added any
	if !f.Added.IsZero() && f.Added.Year() > 1900 {
		added = store.Millis(f.Added)
	}
	if _, err := w.upsert(ctx, "version_file", []kv{{"instance", instance}, {"arr_file_id", f.ArrID}},
		[]kv{{"version_id", ref.versionID}, {"title_id", ref.titleID}, {"path", f.Path},
			{"relative_path", f.RelativePath}, {"size_bytes", f.Size}, {"quality", f.Quality},
			{"quality_revision", f.Revision}, {"release_group", f.ReleaseGroup}, {"scene_name", f.SceneName},
			{"edition", f.Edition}, {"languages", string(langs)}, {"media_info", mi},
			{"custom_format_score", f.CustomFormatScore}, {"indexer_flags", f.IndexerFlags},
			{"original_file_path", f.OriginalFilePath}, {"cutoff_not_met", f.CutoffNotMet}, {"added_at", added},
			{"download_id", downloadID}, {"release_title", releaseTitle}, {"content_id", contentID},
			{"release_type", f.ReleaseType}},
		nil, nil); err != nil {
		return 0, err
	}
	var vfID int64
	if err := w.tx.QueryRowContext(ctx, `SELECT id FROM version_file WHERE instance = ? AND arr_file_id = ?`,
		instance, f.ArrID).Scan(&vfID); err != nil {
		return 0, err
	}
	fileKind := "moviefile"
	if kind == "series" {
		fileKind = "episodefile"
	}
	if _, err := w.upsert(ctx, "arr_alias", []kv{{"instance", instance}, {"kind", fileKind}, {"arr_id", f.ArrID}},
		[]kv{{"target_id", vfID}}, []kv{{"created_at", w.now}}, nil); err != nil {
		return 0, err
	}
	return vfID, nil
}
