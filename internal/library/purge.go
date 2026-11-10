package library

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// PurgeTrash removes trashed links older than library.trash.keep_days. Only the trash copies (symlinks) go; their
// targets are never touched. It does nothing while the gate refuses the link's version. Copies with no link_trash
// row (a run interrupted between the copy and its commit) go too, by the date folder they were filed under. While
// zurg is still loading its library (/ready 503) it waits for the next run, like the other sweeps.
func (l *Library) PurgeTrash(ctx context.Context) (int, error) {
	if err := l.zurg.Ready(ctx); err != nil {
		return 0, fmt.Errorf("library: trash purge: %w: %v", ErrMountDown, err)
	}
	if err := l.decy.Ready(ctx); err != nil {
		return 0, fmt.Errorf("library: trash purge: %w: %v", ErrMountDown, err)
	}
	cutoff := l.now().Add(-time.Duration(l.set.TrashKeepDays) * 24 * time.Hour)
	rows, err := l.st.Reader().QueryContext(ctx, `SELECT id, version_key, trash_path FROM link_trash
		WHERE purged_at IS NULL AND moved_at < ? ORDER BY id`, store.Millis(cutoff))
	if err != nil {
		return 0, err
	}
	type item struct {
		id      int64
		version string
		path    string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.version, &it.path); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	n := 0
	for _, it := range items {
		if err := l.w.purgeTrash(it.version, it.path); err != nil {
			continue
		}
		now := store.Millis(l.now())
		if err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE link_trash SET purged_at = ? WHERE id = ?`, now, it.id); err != nil {
				return err
			}
			return l.record(ctx, tx, action{Kind: "purge", Mode: "applied", Version: it.version, Path: it.path})
		}); err != nil {
			return n, err
		}
		n++
	}
	orphans, err := l.purgeOrphans(ctx, cutoff)
	return n + orphans, err
}

// purgeOrphans removes the trash copies no link_trash row knows, filed under a date (trashPath:
// <trash>/<version>/<date>/...) wholly before cutoff. Symlinks only, through the gate like every trash purge; regular
// files and directories stay.
func (l *Library) purgeOrphans(ctx context.Context, cutoff time.Time) (int, error) {
	if l.gate.trash == "" {
		return 0, nil
	}
	n := 0
	for _, version := range l.gate.Versions() {
		if l.gate.CheckVersion(version) != nil {
			continue
		}
		// The trash's reads are bounded like a root scan's (rootread.go): a trash on a share that
		// stops answering after the gate's check stops the purge (ErrRootDown, retried) instead of holding it.
		base := filepath.Join(l.gate.trash, version)
		days, rerr, err := listDir(ctx, base)
		if err != nil {
			return n, err
		}
		if rerr != nil {
			continue // no trash for the version yet
		}
		known, err := l.trashPaths(ctx, version)
		if err != nil {
			return n, err
		}
		for _, day := range days {
			d, err := time.Parse("2006-01-02", day.Name())
			if err != nil || !day.IsDir() || d.Add(24*time.Hour).After(cutoff) {
				continue
			}
			var orphans []string
			if err := walkRoot(ctx, filepath.Join(base, day.Name()), func(e rootEntry) {
				if e.typ&fs.ModeSymlink != 0 && !known[e.path] {
					orphans = append(orphans, e.path)
				}
			}); Unavailable(err) {
				return n, err
			}
			for _, p := range orphans {
				if err := l.w.purgeTrash(version, p); err != nil {
					l.log.Warn("library: trash copy not purged", "path", p, "err", err)
					continue
				}
				if err := l.record(ctx, nil, action{Kind: "purge", Mode: "applied", Version: version, Path: p,
					Detail: "no trash row (left by an interrupted run)"}); err != nil {
					return n, err
				}
				n++
			}
		}
	}
	return n, nil
}

// trashPaths are the trash copies link_trash records for a version.
func (l *Library) trashPaths(ctx context.Context, version string) (map[string]bool, error) {
	rows, err := l.st.Reader().QueryContext(ctx, `SELECT trash_path FROM link_trash WHERE version_key = ?`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}
