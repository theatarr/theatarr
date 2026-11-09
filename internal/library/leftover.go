package library

import (
	"context"
	"database/sql"
	"errors"
	"os"

	"github.com/theatarr/theatarr/internal/jobs"
)

// Leftover links (a version Theatarr may write to only). A rename commits the link's new path before it removes the old
// symlink, and an import commits an upgrade before it moves a link renamed meanwhile aside (moveStray). A crash
// between leaves the old symlink in the version's root with nothing to retry it: the rename's retry sees its row
// already at the new path, and the stray list lived in memory. The relink sweep removes such a symlink, through the
// gate and recorded as an action, when the database proves Theatarr itself retired it:
//
//   - no link row at the path, and an active row of the same version with the same target at another path (the
//     rename's old path); or
//   - the row at the path is unlinked, of the same version and with the same target (the upgrade's stray);
//
// and in both cases that row changed after the symlink was made (its updated_at is later than the symlink's mtime):
// a rename still in flight in another process has made its new symlink after the row's last change, so it is left.
// Only symlinks into the mount are seen (scanRoot); the symlink is copied to the trash first (the orphan purge
// removes the copy after keep_days) and then removed; its target, a regular file and the mount are never touched.

// leftover is a symlink the sweep found retired by Theatarr.
type leftover struct {
	ml     mountLink
	linkID int64
	why    string
}

// activeRow is an active link row of a version, by target (findLeftovers).
type activeRow struct {
	id, updated int64
	path        string
}

// findLeftovers splits a root's links into the leftovers of version and the rest. Only a version Theatarr may write to is
// checked: a gated or followed one (an arr's root) keeps every symlink as it is.
func (l *Library) findLeftovers(ctx context.Context, db *sql.DB, version string, links []mountLink) ([]leftover, []mountLink, error) {
	if l.gate.CheckVersion(version) != nil || len(links) == 0 {
		return nil, links, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT l.id, l.path, l.target, l.updated_at FROM link l
		JOIN link_version lv ON lv.link_id = l.id JOIN version v ON v.id = lv.version_id
		WHERE v.policy_key = ? AND l.state = 'active'`, version)
	if err != nil {
		return nil, nil, err
	}
	active := map[string][]activeRow{}
	for rows.Next() {
		var r activeRow
		var target string
		if err := rows.Scan(&r.id, &r.path, &target, &r.updated); err != nil {
			rows.Close()
			return nil, nil, err
		}
		active[target] = append(active[target], r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var out []leftover
	kept := links[:0:0]
	for _, ml := range links {
		lo, ok, err := l.leftoverOf(ctx, db, version, ml, active[ml.target])
		if err != nil {
			return nil, nil, err
		}
		if ok {
			out = append(out, lo)
		} else {
			kept = append(kept, ml)
		}
	}
	return out, kept, nil
}

func (l *Library) leftoverOf(ctx context.Context, db *sql.DB, version string, ml mountLink, same []activeRow) (leftover, bool, error) {
	lo := leftover{ml: ml}
	var id, updated int64
	var state, target string
	err := db.QueryRowContext(ctx, `SELECT id, state, target, updated_at FROM link WHERE path = ?`, ml.path).
		Scan(&id, &state, &target, &updated)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return lo, false, err
	}
	if err == nil && (state != "unlinked" || target != ml.target) {
		return lo, false, nil
	}
	if err != nil && len(same) == 0 {
		return lo, false, nil
	}
	fi, lerr := os.Lstat(ml.path)
	if lerr != nil || fi.Mode()&os.ModeSymlink == 0 {
		return lo, false, nil
	}
	made := fi.ModTime().UnixMilli()
	if err == nil {
		if updated <= made {
			return lo, false, nil
		}
		// A symlink put back from the trash by hand (its trash copy, not purged, is gone) keeps the copy's older
		// mtime: it was restored on purpose, not left behind.
		var tp string
		if err := db.QueryRowContext(ctx, `SELECT trash_path FROM link_trash WHERE path = ? AND target = ?
			AND purged_at IS NULL ORDER BY id DESC LIMIT 1`, ml.path, ml.target).Scan(&tp); err == nil {
			if _, err := os.Lstat(tp); errors.Is(err, os.ErrNotExist) {
				return lo, false, nil
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return lo, false, err
		}
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM link_version lv JOIN version v ON v.id = lv.version_id
			WHERE lv.link_id = ? AND v.policy_key = ?`, id, version).Scan(&n); err != nil || n == 0 {
			return lo, false, err
		}
		lo.linkID, lo.why = id, "its link was unlinked (replaced by an upgrade) and the symlink left behind"
		return lo, true, nil
	}
	for _, r := range same {
		if r.path != ml.path && r.updated > made {
			lo.linkID, lo.why = r.id, "its link was renamed to "+r.path+" and the old symlink left behind"
			return lo, true, nil
		}
	}
	return lo, false, nil
}

// cleanLeftovers removes the leftovers a sweep found (a dry run only reports them).
func (l *Library) cleanLeftovers(ctx context.Context, sc *LinkScan, version, root string, los []leftover, dryRun bool) error {
	for _, lo := range los {
		dl := DanglingLink{Path: lo.ml.path, Target: lo.ml.target, Cause: "leftover"}
		sc.Leftovers = append(sc.Leftovers, dl)
		if dryRun {
			continue
		}
		if err := l.cleanLeftover(ctx, version, root, lo); err != nil {
			return err
		}
	}
	return nil
}

// cleanLeftover moves one leftover aside under its link lease (linklock.go); one another run is changing now is left.
func (l *Library) cleanLeftover(ctx context.Context, version, root string, lo leftover) error {
	ctx, unlock, err := l.lockLinks(ctx, lo.ml.path)
	defer unlock()
	if err != nil {
		l.log.Info("library: leftover left: another run is changing it", "link", lo.ml.path, "err", err)
		return nil
	}
	a := action{Kind: "replace", Version: version, LinkID: lo.linkID, Path: lo.ml.path, Target: lo.ml.target}
	if err := jobs.Fence(ctx); err != nil {
		return err
	}
	if !linkPointsAt(lo.ml.path, lo.ml.target) {
		return nil // changed since the walk
	}
	tp, _, err := l.w.copyToTrash(version, root, lo.ml.path)
	if err == nil {
		err = l.w.removeLink(version, lo.ml.path)
	}
	if err != nil {
		a.Mode, a.Detail = modeOf(err), "leftover symlink: "+err.Error()
		_ = l.record(ctx, nil, a)
		return nil
	}
	a.Mode, a.Detail = "applied", "leftover symlink moved aside to "+tp+": "+lo.why
	return l.record(ctx, nil, a)
}
