package router

import (
	"context"
	"database/sql"
	"math"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

const progressWindow = 5 * time.Minute

func recordProgress(ctx context.Context, tx *sql.Tx, id, now int64, pk, tid string, progress float64,
	downloading bool) error {
	if !downloading || math.IsNaN(progress) || math.IsInf(progress, 0) || progress < 0 || progress >= 100 {
		_, err := tx.ExecContext(ctx, `DELETE FROM download_progress WHERE download_id = ?`, id)
		return err
	}
	// A provider switch, regression or clock reset starts a new measurement window.
	var reset bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM download_progress WHERE download_id = ?
		AND (provider != ? OR torrent_id != ? OR progress > ? OR sampled_at > ?))`,
		id, pk, tid, progress, now).Scan(&reset); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM download_progress WHERE download_id = ? AND
		(? OR sampled_at < ?)`, id, reset, now-progressWindow.Milliseconds()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO download_progress
		(download_id, sampled_at, provider, torrent_id, progress) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (download_id, sampled_at) DO UPDATE SET progress = excluded.progress`,
		id, now, pk, tid, progress); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM download_progress WHERE download_id = ? AND sampled_at NOT IN
		(SELECT sampled_at FROM download_progress WHERE download_id = ? ORDER BY sampled_at DESC LIMIT 6)`, id, id)
	return err
}

// EstimateRemaining uses the router's recent provider progress samples. Missing, stale, flat or reset progress
// has no estimate. The caller handles completed downloads, which need no transfer estimate.
func EstimateRemaining(ctx context.Context, db *sql.DB, id int64, now time.Time) (time.Duration, bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT p.sampled_at, p.progress FROM download_progress p
		JOIN download d ON d.id = p.download_id WHERE d.id = ? AND d.state = 'downloading'
		AND p.provider = d.provider AND p.torrent_id = d.provider_torrent_id AND p.progress <= d.progress
		AND p.sampled_at >= ? ORDER BY p.sampled_at`, id, store.Millis(now.Add(-progressWindow)))
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	type sample struct {
		at       time.Time
		progress float64
	}
	var samples []sample
	for rows.Next() {
		var at int64
		var p float64
		if err := rows.Scan(&at, &p); err != nil {
			return 0, false, err
		}
		samples = append(samples, sample{store.FromMillis(at), p})
	}
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	if len(samples) < 2 {
		return 0, false, nil
	}
	first, last := samples[0], samples[len(samples)-1]
	if last.at.After(now) || last.progress <= samples[len(samples)-2].progress || last.progress <= first.progress {
		return 0, false, nil
	}
	remaining := float64(last.at.Sub(first.at)) * (100 - last.progress) / (last.progress - first.progress)
	if math.IsNaN(remaining) || math.IsInf(remaining, 0) || remaining >= float64(math.MaxInt64) {
		return 0, false, nil
	}
	d := time.Duration(remaining) - now.Sub(last.at)
	return d, d > 0, nil
}
