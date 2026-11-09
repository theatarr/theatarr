package catalog

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// Remove takes versions of a title out of the library, as the arr views' DELETE movie|series does for a view's
// versions (internal/arrview write.go deleteTitle) and the web UI's Remove copy and Remove title do: each version is
// unmonitored and listed in arrview_removed_version (view names who removed it), so every view and the library hide
// it. Its rows, files, links and history stay, and nothing on disk or at a provider changes; an add or a version
// turned on again restores it (TurnOn). With every version of the title gone, the title's arr view records
// (arrview_title) go too, as an arr's delete removes the movie or series. Inside the caller's transaction. The keys
// actually removed (the ones the title had and that were not removed already) are returned.
func Remove(ctx context.Context, tx *sql.Tx, titleID int64, keys []string, view string, now time.Time) ([]string,
	error) {
	ms := store.Millis(now)
	var removed []string
	for _, k := range keys {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT v.id FROM version v WHERE v.title_id = ? AND v.policy_key = ?
			AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)`, titleID, k).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO arrview_removed_version (version_id, view, removed_at)
			VALUES (?, ?, ?) ON CONFLICT (version_id) DO NOTHING`, id, view, ms); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE version SET monitored = 0, updated_at = ? WHERE id = ?`, ms,
			id); err != nil {
			return nil, err
		}
		removed = append(removed, k)
	}
	var left int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM version v WHERE v.title_id = ? AND NOT EXISTS
		(SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)`, titleID).Scan(&left); err != nil {
		return nil, err
	}
	if left == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM arrview_title WHERE title_id = ?`, titleID); err != nil {
			return nil, err
		}
	}
	return removed, SyncMonitored(ctx, tx, titleID, now)
}
