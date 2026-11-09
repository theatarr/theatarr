package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/theatarr/theatarr/internal/store"
)

// RenameLink moves one of Theatarr's active links within its owning version's library roots, through the gate. The move
// is a compare-and-swap on the link: its symlink must still point at the target read first, and its row must still
// have that path and target, or nothing is moved (ErrLinkChanged). After the move Plex gets a debounced partial scan
// of the old and the new folder.
func (l *Library) RenameLink(ctx context.Context, linkID int64, newPath string) error {
	return l.renameLink(ctx, linkID, newPath, nil)
}

// renameLink is RenameLink with also, when set, run in the move's write transaction (a reassign's episode change,
// reassign.go): it commits with the new path or not at all, and when it fails the new link goes and the old one
// stays. With also set, a move to the same path still runs it, under the path's lease and the same checks.
func (l *Library) renameLink(ctx context.Context, linkID int64, newPath string, also func(*sql.Tx) error) error {
	var version, oldPath, target string
	var owners int
	err := l.st.Reader().QueryRowContext(ctx, `SELECT v.policy_key, path, target,
		(SELECT COUNT(*) FROM link_version x WHERE x.link_id = l.id) FROM link l
		JOIN link_version lv ON lv.link_id = l.id JOIN version v ON v.id = lv.version_id
		WHERE l.id = ? AND l.state = 'active' ORDER BY lv.version_id LIMIT 1`, linkID).Scan(&version, &oldPath, &target, &owners)
	if err != nil {
		return err
	}
	oldPath, newPath = filepath.Clean(oldPath), filepath.Clean(newPath)
	if oldPath == newPath && also == nil {
		return nil
	}
	if owners != 1 {
		return &Refused{Reason: "outside", Detail: "link is shared by multiple versions"}
	}
	if !filepath.IsAbs(newPath) || l.gate.CheckPath(version, newPath) != nil {
		return &Refused{Reason: "outside", Detail: fmt.Sprintf("%q is outside version %q library roots", newPath, version)}
	}
	if err := l.gate.CheckPath(version, oldPath); err != nil {
		return err
	}
	// Both paths are held under their link leases (linklock.go) through the commit and the old link's removal.
	ctx, unlock, err := l.lockLinks(ctx, oldPath, newPath)
	defer unlock()
	if err != nil {
		return err
	}
	// Compare-and-swap (cas.go): the link must still be the one read above, on disk and in its row, or the rename
	// leaves it alone. A relink or refresh that got there first wins.
	if !linkPointsAt(oldPath, target) {
		return linkChanged(linkID, oldPath, target, "the symlink no longer points there")
	}
	if oldPath == newPath {
		err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
			if err := l.linksHeldTx(ctx, tx); err != nil {
				return err
			}
			if err := rowIs(ctx, tx, linkID, oldPath, target); err != nil {
				return err
			}
			return also(tx)
		})
		if err == nil {
			l.scanAfterRename(ctx, version, oldPath, newPath)
		}
		return err
	}
	// A destination that already points at the target, with no row of its own, is this rename's own link from a
	// run that stopped before its commit: the retry takes it.
	made := false
	if _, err := os.Lstat(newPath); err == nil {
		var rows int
		if err := l.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM link WHERE path = ?`, newPath).Scan(&rows); err != nil {
			return err
		}
		if rows > 0 || !linkPointsAt(newPath, target) {
			return fmt.Errorf("library: rename destination already exists: %s", newPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		if err := l.w.symlink(version, target, newPath); err != nil {
			return err
		}
		made = true
	}
	// The old symlink is read again before the commit, outside the write transaction: a readlink on a share that
	// stops answering must not hold the database's writer. The link lease keeps every other run of ours
	// off both paths until the old link is removed.
	err = nil
	if !linkPointsAt(oldPath, target) {
		err = linkChanged(linkID, oldPath, target, "the symlink no longer points there")
	}
	if err == nil {
		err = l.st.WriteTx(ctx, func(tx *sql.Tx) error {
			if err := l.linksHeldTx(ctx, tx); err != nil {
				return err
			}
			if err := rowIs(ctx, tx, linkID, oldPath, target); err != nil {
				return err
			}
			if also != nil {
				if err := also(tx); err != nil {
					return err
				}
			}
			res, err := tx.ExecContext(ctx, `UPDATE link SET path = ?, updated_at = ? WHERE id = ? AND path = ?
				AND target = ? AND state = 'active'`, newPath, store.Millis(l.now()), linkID, oldPath, target)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return linkChanged(linkID, oldPath, target, "its row changed")
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO notify_rename (title_id, version_key, link_id, previous_path,
				path, quality, quality_version, created_at) SELECT title_id, ?, id, ?, ?, COALESCE(quality, ''),
				MAX(1, COALESCE(json_extract(file_metadata, '$.Revision'), 1)), ? FROM link WHERE id = ?`,
				version, oldPath, newPath, store.Millis(l.now()), linkID)
			return err
		})
	}
	if err != nil {
		// Only our own new link goes; the original, whoever changed it, stays.
		if made && linkPointsAt(newPath, target) {
			_ = l.w.removeLink(version, newPath)
		}
		return err
	}
	l.scanAfterRename(ctx, version, oldPath, newPath)
	// The old link goes only while it still points at the target moved; one a relink rewrote meanwhile stays.
	if !linkPointsAt(oldPath, target) {
		return linkChanged(linkID, oldPath, target, "the old symlink changed after the move; it was left in place")
	}
	if err := l.w.removeLink(version, oldPath); err != nil {
		return fmt.Errorf("library: link moved to %s but old link %s remains: %w", newPath, oldPath, err)
	}
	for _, root := range l.gate.Roots(version) {
		for dir := filepath.Dir(oldPath); dir != root && dir != "."; dir = filepath.Dir(dir) {
			rel, err := filepath.Rel(root, dir)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				break
			}
			if err := l.w.removeEmptyDir(version, dir); err != nil {
				break
			}
		}
	}
	return nil
}

// scanAfterRename queues Plex partial scans (debounced) of a moved link's old and new folders.
func (l *Library) scanAfterRename(ctx context.Context, version, oldPath, newPath string) {
	for _, d := range uniqueDirs(filepath.Dir(oldPath), filepath.Dir(newPath)) {
		if err := l.EnqueueScan(ctx, version, d); err != nil {
			l.log.Warn("library: plex scan not queued", "dir", d, "err", err)
		}
	}
}
