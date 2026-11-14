package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/theatarr/theatarr/internal/store"
)

// UnlinkResult is what Unlink did, or would do.
type UnlinkResult struct {
	Version   string `json:"version"`
	Path      string `json:"path"`
	TrashPath string `json:"trash_path,omitempty"`
	Target    string `json:"target,omitempty"`
	DryRun    bool   `json:"dry_run"`
	Refused   string `json:"refused,omitempty"` // why the gate made it a dry run
}

// CheckUnlink checks Unlink's gate, symlink and trash path without changing files or recording an action.
func (l *Library) CheckUnlink(version, path string) error {
	if err := l.gate.CheckPath(version, path); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return &Refused{Reason: "not_symlink", Detail: fmt.Sprintf("%q is not a symlink; only links are moved aside", path)}
	}
	return l.gate.CheckTrash(version, l.w.trashPath(version, l.unlinkRoot(version, path), path))
}

func (l *Library) unlinkRoot(version, path string) string {
	for _, r := range l.gate.Roots(version) {
		if rel, err := filepath.Rel(r, path); err == nil && !escapes(rel) {
			return r
		}
	}
	if vp := l.pol.Versions[version]; vp != nil {
		return vp.Library
	}
	return ""
}

// Unlink takes one library link of a version out of its root (an arr client deleted the movie or episode file):
// the symlink is recreated in the trash, then removed, as an upgrade moves an old file aside. Only the link goes;
// what it points at (debrid content on the read-only mount) is never touched, and a regular file is refused. It
// passes the gate like every write: when the gate refuses, nothing changes on disk and the planned move is recorded
// as a dry run. linkID, when not 0, is our link row, which turns unlinked. A path that is already gone is no error.
func (l *Library) Unlink(ctx context.Context, version, path string, linkID int64, reason string) (*UnlinkResult, error) {
	res := &UnlinkResult{Version: version, Path: path}
	root := l.unlinkRoot(version, path)
	err := l.gate.CheckVersion(version)
	if err == nil {
		err = l.gate.CheckPath(version, path)
	}
	if err != nil {
		res.DryRun, res.Refused = true, err.Error()
		return res, l.record(ctx, nil, action{Kind: "replace", Mode: "dry_run", Version: version, LinkID: linkID,
			Path: path, Detail: "unlink (" + reason + "): " + err.Error()})
	}
	// The path's link lease (linklock.go) is held from the check to the commit: a relink, refresh or import of the
	// same path waits for the next pass, or this unlink is refused while one of them runs.
	ctx, unlock, err := l.lockLinks(ctx, path)
	defer unlock()
	if err != nil {
		return nil, err
	}
	// Compare-and-swap (cas.go): our row must still be at this path, and the symlink must still point at its target.
	// A rename or relink that changed it since wins, and nothing is changed. A row already unlinked here is a link
	// shared by versions, unlinked for another one.
	rowTarget := ""
	if linkID > 0 {
		var p, st string
		err := l.st.Reader().QueryRowContext(ctx, `SELECT path, state, target FROM link WHERE id = ?`, linkID).
			Scan(&p, &st, &rowTarget)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, linkChanged(linkID, path, "", "its row is gone")
		}
		if err != nil {
			return nil, err
		}
		if p != path {
			return nil, linkChanged(linkID, path, "", fmt.Sprintf("its row is now at %s (%s)", p, st))
		}
		if cur, err := os.Readlink(path); err == nil && st != "unlinked" && cur != rowTarget {
			return nil, linkChanged(linkID, path, rowTarget, "the symlink now points at "+cur)
		}
	}
	tp, target, err := l.w.copyToTrash(version, root, path)
	if errors.Is(err, os.ErrNotExist) {
		return res, l.markUnlinked(ctx, version, linkID, path, "", "", reason+": already gone")
	}
	if err != nil {
		_ = l.record(ctx, nil, action{Kind: "replace", Mode: modeOf(err), Version: version, LinkID: linkID, Path: path,
			Detail: "unlink (" + reason + "): " + err.Error()})
		return nil, err
	}
	// Only the symlink copied to the trash goes.
	if !linkPointsAt(path, target) {
		return nil, linkChanged(linkID, path, target, "the symlink changed during the unlink")
	}
	if err := l.w.removeLink(version, path); err != nil {
		return nil, err
	}
	res.TrashPath, res.Target = tp, target
	err = l.markUnlinked(ctx, version, linkID, path, tp, target, reason)
	// The link is gone from disk either way: Plex drops the file at its folder's next partial scan.
	if err := l.EnqueueScan(ctx, version, filepath.Dir(path)); err != nil {
		l.log.Warn("library: plex scan not queued", "dir", filepath.Dir(path), "err", err)
	}
	return res, err
}

// markUnlinked records the unlink. The row update is a compare-and-swap (id, path, not unlinked): a row that a
// rename moved after the check in Unlink is left as it is, the trash copy is still recorded, and the result is
// ErrLinkChanged.
func (l *Library) markUnlinked(ctx context.Context, version string, linkID int64, path, tp, target, reason string) error {
	lost := false
	err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := l.linksHeldTx(ctx, tx); err != nil {
			return err
		}
		var err error
		lost, err = l.markUnlinkedTx(ctx, tx, version, linkID, path, tp, target, reason)
		return err
	})
	if err == nil && lost {
		return linkChanged(linkID, path, target, "its row changed during the unlink")
	}
	return err
}

// markUnlinkedTx is markUnlinked's write in tx (a reassign's replace commits it with the move); lost is a row that
// changed under us.
func (l *Library) markUnlinkedTx(ctx context.Context, tx *sql.Tx, version string, linkID int64, path, tp, target,
	reason string) (lost bool, err error) {
	now := store.Millis(l.now())
	if linkID > 0 {
		// The row with the target moved aside ("" when the symlink was gone already: any target).
		res, err := tx.ExecContext(ctx, `UPDATE link SET state = 'unlinked', updated_at = ? WHERE id = ? AND path = ?
			AND state IN ('active', 'dead') AND (? = '' OR target = ?)`, now, linkID, path, target, target)
		if err != nil {
			return false, err
		}
		if n, err := res.RowsAffected(); err != nil {
			return false, err
		} else if n > 0 {
			if err := reconcileRemovedLink(ctx, tx, linkID, now); err != nil {
				return false, err
			}
		} else {
			// Unlinked here already (a shared link, or a retry) is done; anything else changed under us.
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM link WHERE id = ? AND path = ? AND state = 'unlinked'`,
				linkID, path).Scan(&n); err != nil {
				return false, err
			}
			lost = n == 0
		}
	}
	if lost && tp == "" {
		return lost, nil
	}
	if tp != "" {
		var lid any
		if linkID > 0 {
			lid = linkID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO link_trash (link_id, version_key, path, trash_path, target,
			reason, moved_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, lid, version, path, tp, target, "unlink: "+reason,
			now); err != nil {
			return false, err
		}
	}
	detail := "unlinked (" + reason + "), moved aside to " + tp
	var rel relFacts
	if lost {
		detail += "; its row had changed and was left"
	} else {
		rel = linkFacts(ctx, tx, linkID, path)
	}
	return lost, l.record(ctx, tx, action{Kind: "replace", Mode: "applied", Version: version, LinkID: linkID, Path: path,
		Target: target, Detail: detail, Rel: rel})
}
