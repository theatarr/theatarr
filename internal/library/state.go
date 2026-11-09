package library

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// importedState is the state an imported file gives its version (or episodes): filled while the file is below the
// profile's cutoff, by quality or by custom-format score (UpgradableSpecification.CutoffNotMet, as decide scores the
// file when it judges an upgrade), else cutoff_met.
func (l *Library) importedState(v *importVersion, t *decide.Title, f *decide.ExistingFile) (string, error) {
	if l.engine == nil {
		return "", fmt.Errorf("decision engine is not configured")
	}
	fl := format.Radarr
	if v.Policy.Kind == policy.KindSeries {
		fl = format.Sonarr
	}
	if _, ok := profile.QualityByName(fl, f.Quality); !ok {
		return "", fmt.Errorf("unknown %s quality %q", fl, f.Quality)
	}
	notMet, err := l.engine.FileCutoffNotMet(t, decide.Version{ID: v.VersionID, Key: v.Key, Policy: v.Policy}, f)
	if err != nil {
		return "", err
	}
	if notMet {
		return "filled", nil
	}
	return "cutoff_met", nil
}

func recordImportedState(ctx context.Context, tx *sql.Tx, v *importVersion, episodes []int64, state string,
	partial bool, now int64) error {
	if v.Title.Kind != string(policy.KindSeries) {
		_, err := tx.ExecContext(ctx, `UPDATE version SET state = CASE WHEN ? AND
			CASE state WHEN 'cutoff_met' THEN 2 WHEN 'filled' THEN 1 ELSE 0 END >
			CASE ? WHEN 'cutoff_met' THEN 2 ELSE 1 END THEN state ELSE ? END, updated_at = ? WHERE id = ?`,
			partial, state, state, now, v.VersionID)
		return err
	}
	for _, episodeID := range episodes {
		_, err := tx.ExecContext(ctx, `INSERT INTO episode_version_state
			(version_id, episode_id, monitored, state, cutoff_met, updated_at)
			VALUES (?, ?, COALESCE((SELECT monitored FROM episode WHERE id = ?), 1), ?, ?, ?)
			ON CONFLICT (version_id, episode_id) DO UPDATE SET
			state = CASE WHEN ? AND
				CASE episode_version_state.state WHEN 'cutoff_met' THEN 2 WHEN 'filled' THEN 1 ELSE 0 END >
				CASE excluded.state WHEN 'cutoff_met' THEN 2 ELSE 1 END
				THEN episode_version_state.state ELSE excluded.state END,
			cutoff_met = CASE WHEN ? AND
				CASE episode_version_state.state WHEN 'cutoff_met' THEN 2 WHEN 'filled' THEN 1 ELSE 0 END >
				CASE excluded.state WHEN 'cutoff_met' THEN 2 ELSE 1 END
				THEN episode_version_state.cutoff_met ELSE excluded.cutoff_met END,
			updated_at = excluded.updated_at`, v.VersionID, episodeID, episodeID, state, state == "cutoff_met", now,
			partial, partial)
		if err != nil {
			return err
		}
	}
	return refreshSeriesState(ctx, tx, v.VersionID, partial, now)
}

func refreshSeriesState(ctx context.Context, tx *sql.Tx, versionID int64, partial bool, now int64) error {
	var state string
	err := tx.QueryRowContext(ctx, `SELECT CASE
		WHEN EXISTS (SELECT 1 FROM episode_version_state s JOIN episode e ON e.id = s.episode_id
			WHERE s.version_id = ? AND s.monitored = 1 AND s.state = 'wanted'
			AND e.air_date_utc_at IS NOT NULL AND e.air_date_utc_at <= ?) THEN 'wanted'
		WHEN EXISTS (SELECT 1 FROM episode_version_state WHERE version_id = ? AND state = 'filled') THEN 'filled'
		WHEN EXISTS (SELECT 1 FROM episode_version_state WHERE version_id = ? AND state = 'cutoff_met') THEN 'cutoff_met'
		ELSE 'wanted' END`, versionID, now, versionID, versionID).Scan(&state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE version SET state = CASE WHEN ? AND
		CASE state WHEN 'cutoff_met' THEN 2 WHEN 'filled' THEN 1 ELSE 0 END >
		CASE ? WHEN 'cutoff_met' THEN 2 WHEN 'filled' THEN 1 ELSE 0 END THEN state ELSE ? END,
		updated_at = ? WHERE id = ?`, partial, state, state, now, versionID)
	return err
}

func reconcileRemovedLink(ctx context.Context, tx *sql.Tx, linkID int64, now int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT lv.version_id, t.kind FROM link_version lv
		JOIN version v ON v.id = lv.version_id JOIN title t ON t.id = v.title_id WHERE lv.link_id = ?`, linkID)
	if err != nil {
		return err
	}
	type version struct {
		id   int64
		kind string
	}
	var versions []version
	for rows.Next() {
		var v version
		if err := rows.Scan(&v.id, &v.kind); err != nil {
			rows.Close()
			return err
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, v := range versions {
		if v.kind != string(policy.KindSeries) {
			if _, err := tx.ExecContext(ctx, `UPDATE version SET state = 'wanted', updated_at = ? WHERE id = ?
				AND NOT EXISTS (SELECT 1 FROM link l JOIN link_version lv ON lv.link_id = l.id
					WHERE lv.version_id = ? AND l.state = 'active')
				AND NOT EXISTS (SELECT 1 FROM version_file WHERE version_id = ?)`, now, v.id, v.id, v.id); err != nil {
				return err
			}
			continue
		}
		episodes, err := tx.QueryContext(ctx, `SELECT episode_id FROM link_episode WHERE link_id = ?`, linkID)
		if err != nil {
			return err
		}
		var ids []int64
		for episodes.Next() {
			var id int64
			if err := episodes.Scan(&id); err != nil {
				episodes.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := episodes.Err(); err != nil {
			episodes.Close()
			return err
		}
		episodes.Close()
		if err := episodesLostFile(ctx, tx, v.id, ids, now); err != nil {
			return err
		}
	}
	return nil
}

// episodesLostFile makes a series version's episodes wanted again once a file of theirs is gone, each only when no
// other file (active link or arr file) of the version holds it, and recomputes the version's state.
func episodesLostFile(ctx context.Context, tx *sql.Tx, versionID int64, episodes []int64, now int64) error {
	for _, id := range episodes {
		if _, err := tx.ExecContext(ctx, `UPDATE episode_version_state SET state = 'wanted', cutoff_met = 0,
			updated_at = ? WHERE version_id = ? AND episode_id = ? AND NOT EXISTS (
				SELECT 1 FROM link l JOIN link_version lv ON lv.link_id = l.id
				JOIN link_episode le ON le.link_id = l.id
				WHERE lv.version_id = ? AND le.episode_id = ? AND l.state = 'active')
			AND NOT EXISTS (SELECT 1 FROM version_file vf JOIN version_file_episode fe ON fe.version_file_id = vf.id
				WHERE vf.version_id = ? AND fe.episode_id = ?)`, now, versionID, id, versionID, id, versionID, id); err != nil {
			return err
		}
	}
	return refreshSeriesState(ctx, tx, versionID, false, now)
}

// RecomputeLinkStates sets the state of every active version's files (Theatarr's links and imported arr files) from
// those files' cutoff under the current profile, as an import records it (importedState: quality and custom-format
// score). The state stored with a file goes stale when the profile changes, and an arr import records the arr's own
// verdict: a cutoff_met file below a raised cutoff would never be searched again. Only filled and
// cutoff_met move, and only between each other; a movie is judged by its current file, an episode by its file (the
// files decide compares a release with). A series version is judged whatever its own state (one missing episode
// makes it wanted while its other episodes hold files). A version an arr still manages (import.arr) keeps the arr's
// verdict on the arr's files; Theatarr's links in it are judged. Idempotent; Run does it once per process and again, for
// the versions whose settings changed, after each settings change (a new policy revision).
func (l *Library) RecomputeLinkStates(ctx context.Context) (int, error) {
	return l.recomputeStates(ctx, nil)
}

// recomputeStates is RecomputeLinkStates for the versions with these policy keys; nil is every version.
func (l *Library) recomputeStates(ctx context.Context, keys []string) (int, error) {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.recomputeStates(pinned, keys)
	}
	if l.engine == nil {
		return 0, nil
	}
	only := ""
	if keys != nil {
		b, _ := json.Marshal(keys)
		only = string(b)
	}
	db := l.st.Reader()
	rows, err := db.QueryContext(ctx, `SELECT v.id, v.title_id, v.policy_key, EXISTS (SELECT 1 FROM version_source vs
			WHERE vs.version_id = v.id AND vs.instance IN (SELECT value FROM json_each(?))) AS managed
		FROM version v WHERE (? = '' OR v.policy_key IN (SELECT value FROM json_each(?)))
		AND (EXISTS (SELECT 1 FROM link_version lv JOIN link l ON l.id = lv.link_id
			WHERE lv.version_id = v.id AND l.state = 'active')
		OR (EXISTS (SELECT 1 FROM version_file vf WHERE vf.version_id = v.id)
			AND NOT EXISTS (SELECT 1 FROM version_source vs WHERE vs.version_id = v.id AND vs.instance IN (
				SELECT value FROM json_each(?))))) ORDER BY v.id`,
		managedJSON(l.pol), only, only, managedJSON(l.pol))
	if err != nil {
		return 0, err
	}
	var versions []stateVersion
	for rows.Next() {
		var v stateVersion
		if err := rows.Scan(&v.id, &v.titleID, &v.key, &v.managed); err != nil {
			rows.Close()
			return 0, err
		}
		versions = append(versions, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	changed := 0
	// One pinned standing for the whole pass, read only if a prefer-on version needs it.
	ctx, err = decide.WithStoredSeaDex(ctx, l.st.Reader())
	if err != nil {
		return 0, err
	}
	for _, v := range versions {
		ok, err := l.recomputeVersion(ctx, v)
		if err != nil {
			return changed, err
		}
		if ok {
			changed++
		}
	}
	return changed, nil
}

// stateVersion is a version whose link states RecomputeLinkStates judges.
type stateVersion struct {
	id, titleID int64
	key         string
	// managed: an arr still manages the version (import.arr); only Theatarr's links are judged.
	managed bool
}

// recomputeVersion is RecomputeLinkStates for one version; true when a state changed. A version that is not mode:
// active, or no decision engine, is left.
func (l *Library) recomputeVersion(ctx context.Context, v stateVersion) (bool, error) {
	vp := l.pol.Versions[v.key]
	if l.engine == nil || vp == nil || vp.Mode != policy.ModeActive {
		return false, nil
	}
	db := l.st.Reader()
	if vp.PreferSeaDex {
		var err error
		ctx, err = decide.WithStoredSeaDex(ctx, db)
		if err != nil {
			return false, err
		}
		ctx = decide.WithSeaDexTitle(decide.WithSeaDexPolicy(ctx, l.pol, vp), v.titleID)
	}
	dv := decide.Version{ID: v.id, Key: v.key, Policy: vp}
	t, err := decide.LoadTitle(ctx, db, v.titleID)
	if err != nil {
		return false, err
	}
	stateOf := func(f *decide.ExistingFile) (string, error) {
		notMet, err := l.engine.FileCutoffNotMet(&t, dv, f)
		if err != nil || notMet {
			return "filled", err
		}
		return "cutoff_met", nil
	}
	now := store.Millis(l.now())
	if dv.Policy.Kind != policy.KindSeries {
		f, fileID, err := decide.LoadExistingFile(ctx, db, v.id)
		if err != nil {
			return false, err
		}
		if f == nil || (v.managed && fileID >= 0) {
			return false, nil // its link went meanwhile, or the managing arr's file
		}
		state, err := stateOf(f)
		if err != nil {
			l.log.Warn("library: state not recomputed", "version", v.key, "title", v.titleID, "err", err)
			return false, nil
		}
		res, err := l.st.Writer().ExecContext(ctx, `UPDATE version SET state = ?, updated_at = ? WHERE id = ?
			AND state IN ('filled', 'cutoff_met') AND state <> ?`, state, now, v.id, state)
		if err != nil {
			return false, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			l.log.Info("library: version state recomputed", "version", v.key, "title", v.titleID, "state", state)
			return true, nil
		}
		return false, nil
	}
	var eps []decide.Episode
	if vp.PreferSeaDex {
		err = decide.LoadSeriesVersion(ctx, db, &t, v.id)
		eps = t.Episodes
	} else {
		eps, err = decide.LoadEpisodes(ctx, db, v.titleID, v.id)
	}
	if err != nil {
		return false, err
	}
	t.Episodes = eps
	states := map[int64]string{} // by file id (a link's negative, an arr file's positive)
	want := map[int64]string{}   // by episode
	for _, ep := range eps {
		if ep.File == nil || (v.managed && ep.FileID >= 0) {
			continue // no file, or the managing arr's
		}
		s, ok := states[ep.FileID]
		if !ok {
			if s, err = stateOf(ep.File); err != nil {
				l.log.Warn("library: state not recomputed", "version", v.key, "file", ep.FileID, "err", err)
				s = ""
			}
			states[ep.FileID] = s
		}
		if s != "" {
			want[ep.ID] = s
		}
	}
	n := 0
	if err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		n = 0
		for epID, s := range want {
			res, err := tx.ExecContext(ctx, `UPDATE episode_version_state SET state = ?, cutoff_met = ?,
				updated_at = ? WHERE version_id = ? AND episode_id = ? AND state IN ('filled', 'cutoff_met')
				AND state <> ?`, s, s == "cutoff_met", now, v.id, epID, s)
			if err != nil {
				return err
			}
			k, _ := res.RowsAffected()
			n += int(k)
		}
		if n == 0 {
			return nil
		}
		return refreshSeriesState(ctx, tx, v.id, false, now)
	}); err != nil {
		return false, err
	}
	if n > 0 {
		l.log.Info("library: episode states recomputed", "version", v.key, "title", v.titleID, "episodes", n)
	}
	return n > 0, nil
}

// reviveLink undoes reconcileRemovedLink for a dead link whose file is back (relink): each version of the link that
// it had left wanted holds the file again. A movie version, and a series version's episodes of the link, go from
// wanted to filled (the least a file gives; recomputeVersion then sets cutoff_met where the file meets the cutoff).
// It returns the link's versions.
func reviveLink(ctx context.Context, tx *sql.Tx, linkID int64, now int64) ([]stateVersion, error) {
	rows, err := tx.QueryContext(ctx, `SELECT v.id, v.title_id, v.policy_key, t.kind FROM link_version lv
		JOIN version v ON v.id = lv.version_id JOIN title t ON t.id = v.title_id WHERE lv.link_id = ?`, linkID)
	if err != nil {
		return nil, err
	}
	var versions []stateVersion
	var kinds []string
	for rows.Next() {
		var v stateVersion
		var kind string
		if err := rows.Scan(&v.id, &v.titleID, &v.key, &kind); err != nil {
			rows.Close()
			return nil, err
		}
		versions = append(versions, v)
		kinds = append(kinds, kind)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i, v := range versions {
		if kinds[i] != string(policy.KindSeries) {
			if _, err := tx.ExecContext(ctx, `UPDATE version SET state = 'filled', updated_at = ? WHERE id = ?
				AND state = 'wanted'`, now, v.id); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE episode_version_state SET state = 'filled', cutoff_met = 0,
			updated_at = ? WHERE version_id = ? AND state = 'wanted'
			AND episode_id IN (SELECT episode_id FROM link_episode WHERE link_id = ?)`, now, v.id, linkID); err != nil {
			return nil, err
		}
		if err := refreshSeriesState(ctx, tx, v.id, false, now); err != nil {
			return nil, err
		}
	}
	return versions, nil
}

// recomputeOnce runs RecomputeLinkStates until it succeeds once in this process, and again once the policy
// revision changes (a settings save), then only for the versions whose judging settings changed (versionPrints).
func (l *Library) recomputeOnce(ctx context.Context) {
	l.recomputeMu.Lock()
	defer l.recomputeMu.Unlock()
	pol := l.pol
	if l.runtime != nil {
		_, target := l.runtime(ctx)
		pol = target.pol
	}
	if l.recomputed && l.recomputedRev == pol.Revision {
		return
	}
	prints := versionPrints(pol)
	var keys []string // nil: every version (the first pass)
	if l.recomputed {
		keys = []string{}
		for k, fp := range prints {
			if l.recomputedPrints[k] != fp {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
	}
	n := 0
	if keys == nil || len(keys) > 0 {
		var err error
		if n, err = l.recomputeStates(ctx, keys); err != nil {
			l.log.Warn("library: link states not recomputed; retried on the next pass", "err", err)
			return
		}
	}
	l.recomputed, l.recomputedRev, l.recomputedPrints = true, pol.Revision, prints
	if n > 0 {
		l.log.Info("library: link states recomputed", "versions", n)
	}
}

// versionPrints fingerprints, per version key, the settings a file's state is judged under: the version, its
// profile, the formats and format sets, and which arrs still manage versions.
func versionPrints(p *policy.Policy) map[string]string {
	out := map[string]string{}
	if p == nil {
		return out
	}
	shared, _ := json.Marshal([]any{p.Formats, p.FormatSets, managedJSON(p)})
	for k, vp := range p.Versions {
		var prof *policy.Profile
		if vp != nil {
			prof = p.Profiles[vp.Profile]
		}
		b, _ := json.Marshal([]any{vp, prof})
		sum := sha256.Sum256(append(b, shared...))
		out[k] = hex.EncodeToString(sum[:])
	}
	return out
}

// managedJSON is the JSON list of arr instances that still manage their versions (import.arr, policy.ManagedBy).
func managedJSON(p *policy.Policy) string {
	out := []string{}
	if p != nil && p.Import != nil {
		for inst := range p.Import.Arr {
			out = append(out, inst)
		}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

type cutoffTitlesKey struct{}
type cutoffTitleKey struct{ title, version int64 }

func withCutoffTitles(ctx context.Context) context.Context {
	return context.WithValue(ctx, cutoffTitlesKey{}, map[cutoffTitleKey]*decide.Title{})
}

func (l *Library) cutoffTitle(ctx context.Context, titleID, versionID int64, vp *policy.VersionPolicy) (*decide.Title, error) {
	cache, _ := ctx.Value(cutoffTitlesKey{}).(map[cutoffTitleKey]*decide.Title)
	key := cutoffTitleKey{titleID, versionID}
	if t := cache[key]; t != nil {
		return t, nil
	}
	t, err := decide.LoadTitle(ctx, l.st.Reader(), titleID)
	if err != nil {
		return nil, err
	}
	if vp.PreferSeaDex && t.Kind == policy.KindSeries {
		err = decide.LoadSeriesVersion(ctx, l.st.Reader(), &t, versionID)
		if err != nil {
			return nil, err
		}
	}
	if cache != nil {
		cache[key] = &t
	}
	return &t, nil
}
