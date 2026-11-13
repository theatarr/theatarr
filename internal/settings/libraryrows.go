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
	// The titles it had, for the monitoring recompute after its versions are gone.
	for _, q := range []string{`CREATE TEMP TABLE IF NOT EXISTS library_rows_title (id INTEGER PRIMARY KEY)`,
		`DELETE FROM library_rows_title`,
		`INSERT INTO library_rows_title (id) SELECT DISTINCT title_id FROM version WHERE policy_key = ?1`} {
		if _, err := tx.ExecContext(ctx, q, key); err != nil {
			return fmt.Errorf("settings: remove rows of library %s: %w", key, err)
		}
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
	// Its upgrade cards (migration 00125's mirror card, FreezeStored's copy card) are resolved: their cause is gone.
	if _, err := tx.ExecContext(ctx, `UPDATE error_event SET status = 'resolved', resolved_at = ?1, updated_at = ?1
		WHERE fingerprint IN ('arr-mirror:' || ?2, 'arr-freeze:' || ?2) AND status IN ('new', 'seen')`, now, key); err != nil {
		return fmt.Errorf("settings: remove rows of library %s: %w", key, err)
	}
	// An episode with states left is monitored when any of them is; one with none left keeps its flag (a remaining
	// library may have no state for it yet, and a series added again keeps its flags). A title is monitored when any
	// version not removed from the views is (catalog.SyncMonitored).
	for _, q := range []string{
		`UPDATE episode SET monitored = m, updated_at = ?1 FROM (SELECT e.id AS eid, (SELECT MAX(s.monitored)
			FROM episode_version_state s WHERE s.episode_id = e.id) AS m FROM episode e
			WHERE e.title_id IN (SELECT id FROM library_rows_title)) x
			WHERE x.eid = episode.id AND x.m IS NOT NULL AND episode.monitored <> x.m`,
		`UPDATE title SET monitored = m, updated_at = ?1 FROM (SELECT t.id AS tid, COALESCE((SELECT MAX(v.monitored)
			FROM version v WHERE v.title_id = t.id AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r
			WHERE r.version_id = v.id)), 0) AS m FROM library_rows_title t) x WHERE x.tid = title.id AND title.monitored <> m`,
		`DROP TABLE library_rows_title`,
	} {
		if _, err := tx.ExecContext(ctx, q, now); err != nil {
			return fmt.Errorf("settings: remove rows of library %s: %w", key, err)
		}
	}
	return nil
}

// libraryRowCounts are what removing a library leaves untracked: its links that no other library holds (the files
// stay on disk), its downloads in progress that were sent (they stay at the provider or client) and its queued
// downloads not sent yet (dropped).
func libraryRowCounts(ctx context.Context, q rootQueryer, key string) (links, downloads, queued int, err error) {
	err = q.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM link l WHERE l.state <> 'unlinked'
			AND EXISTS (SELECT 1 FROM link_version lv JOIN version v ON v.id = lv.version_id
				WHERE lv.link_id = l.id AND v.policy_key = ?1)
			AND NOT EXISTS (SELECT 1 FROM link_version lv JOIN version v ON v.id = lv.version_id
				WHERE lv.link_id = l.id AND v.policy_key <> ?1)),
		(SELECT COUNT(*) FROM download d JOIN version v ON v.id = d.version_id
			WHERE v.policy_key = ?1 AND d.state NOT IN ('queued', 'imported', 'failed')),
		(SELECT COUNT(*) FROM download d JOIN version v ON v.id = d.version_id
			WHERE v.policy_key = ?1 AND d.state = 'queued')`, key).Scan(&links, &downloads, &queued)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("settings: cannot inspect library %s: %w", key, err)
	}
	return links, downloads, queued, nil
}
