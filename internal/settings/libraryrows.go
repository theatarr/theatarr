package settings

import (
	"context"
	"database/sql"
	"fmt"
)

// removeLibraryRows deletes a removed library's rows, in the save that removes it (docs/design/library.md "Removing a
// library"): its versions with everything that cascades from them (downloads, wanted state, decisions, episode
// states, file records), the links it alone holds, its text-keyed rows and its own view rows. Links of its downloads
// that another library still holds stay, without the download. The episode and title monitoring of the titles it
// had is recomputed (monitored in any remaining version) and movie collections drop the key. Files on disk and at
// the providers are not touched. Migration 00125 does the same for libraries already gone from the settings.
func removeLibraryRows(ctx context.Context, tx *sql.Tx, key string, now int64) error {
	var titles []int64
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT title_id FROM version WHERE policy_key = ?`, key)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		titles = append(titles, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, q := range []string{
		`UPDATE link SET download_id = NULL WHERE download_id IN (SELECT d.id FROM download d
			JOIN version v ON v.id = d.version_id WHERE v.policy_key = ?1)`,
		`DELETE FROM link WHERE id IN (SELECT lv.link_id FROM link_version lv JOIN version v ON v.id = lv.version_id
			WHERE v.policy_key = ?1) AND NOT EXISTS (SELECT 1 FROM link_version lv JOIN version v ON v.id = lv.version_id
			WHERE lv.link_id = link.id AND v.policy_key <> ?1)`,
		`DELETE FROM version WHERE policy_key = ?1`,
		`DELETE FROM router_offer WHERE version_key = ?1 OR from_version = ?1`,
		`DELETE FROM search_request WHERE version_key = ?1`,
		`DELETE FROM arrview_title_tag WHERE version = ?1`,
		`DELETE FROM arrview_title WHERE view = 'v/' || ?1`,
		`UPDATE movie_collection SET versions = (SELECT json_group_array(j.value) FROM json_each(movie_collection.versions) j
			WHERE j.value <> ?1) WHERE EXISTS (SELECT 1 FROM json_each(movie_collection.versions) j WHERE j.value = ?1)`,
	} {
		if _, err := tx.ExecContext(ctx, q, key); err != nil {
			return fmt.Errorf("settings: remove rows of library %s: %w", key, err)
		}
	}
	for _, id := range titles {
		// An episode is monitored when any version monitors it (internal/web/manage.go, tvrefresh); a title when any
		// version not removed from the views is (catalog.SyncMonitored).
		if _, err := tx.ExecContext(ctx, `UPDATE episode SET monitored = s.m, updated_at = ?1 FROM (SELECT episode_id,
			MAX(monitored) AS m FROM episode_version_state GROUP BY episode_id) s WHERE s.episode_id = episode.id
			AND episode.title_id = ?2 AND episode.monitored <> s.m`, now, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE title SET monitored = m, updated_at = ?1 FROM (SELECT
			COALESCE(MAX(v.monitored), 0) AS m FROM version v WHERE v.title_id = ?2 AND NOT EXISTS (SELECT 1
			FROM arrview_removed_version r WHERE r.version_id = v.id)) WHERE id = ?2 AND monitored <> m`, now, id); err != nil {
			return err
		}
	}
	return nil
}

// libraryRowCounts are what removing a library leaves untracked: its links that no other library holds (the files
// stay on disk) and its downloads still in progress (they stay at the provider).
func libraryRowCounts(ctx context.Context, q rootQueryer, key string) (links, downloads int, err error) {
	err = q.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM link l WHERE l.state <> 'unlinked'
			AND EXISTS (SELECT 1 FROM link_version lv JOIN version v ON v.id = lv.version_id
				WHERE lv.link_id = l.id AND v.policy_key = ?1)
			AND NOT EXISTS (SELECT 1 FROM link_version lv JOIN version v ON v.id = lv.version_id
				WHERE lv.link_id = l.id AND v.policy_key <> ?1)),
		(SELECT COUNT(*) FROM download d JOIN version v ON v.id = d.version_id
			WHERE v.policy_key = ?1 AND d.state NOT IN ('imported', 'failed'))`, key).Scan(&links, &downloads)
	if err != nil {
		return 0, 0, fmt.Errorf("settings: cannot inspect library %s: %w", key, err)
	}
	return links, downloads, nil
}
