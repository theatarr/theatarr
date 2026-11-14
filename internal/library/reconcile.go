package library

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/theatarr/theatarr/internal/store"
)

// vanishFloor is how many links may vanish in one pass of a root whatever their share (the failsafe's floor: a
// small root losing a few links is not a missing share).
const vanishFloor = 5

// VanishFailsafeDetail starts the detail of the alert action a reconcile failsafe writes (health reads it).
const VanishFailsafeDetail = "vanish failsafe: "

// VanishedLink is an active link whose symlink is no longer in the library.
type VanishedLink struct {
	LinkID int64  `json:"link_id"`
	Path   string `json:"path"`
	Target string `json:"target"`
}

// ReconcileLinks checks the active links of the versions' roots (version "" means every version under
// library.versions; titleIDs nil means every title) against the disk: a link whose symlink is gone (someone deleted
// it, or its folder) is no longer active, and its version or episodes are wanted again when no other file holds
// them, as the arrs' rescan drops a file missing from disk (DiskScanService.Scan, MediaFileTableCleanupService).
// The relink sweep and Rescan run it.
//
// Only a path that is confirmed missing counts (lstat says it does not exist); any other error leaves the link as
// it is. A root that is missing, unreadable or empty is skipped whole (the arrs' "root folder doesn't exist / is
// empty" failsafe): an unmounted library must not turn every link missing. Links of a download being imported now
// are left to that import. A version the gate refuses (shadow, library off) only reports what it found: nothing is
// written.
func (l *Library) ReconcileLinks(ctx context.Context, version string, titleIDs []int64) ([]*LinkScan, error) {
	var out []*LinkScan
	for _, k := range l.gate.Versions() {
		if version != "" && k != version {
			continue
		}
		for _, root := range l.gate.Roots(k) {
			sc := &LinkScan{Version: k, Root: root, Causes: map[string]int{}}
			refused, down := l.scanCheck(ctx, k, root)
			if down != nil {
				l.skipRoot(sc, down)
				out = append(out, sc)
				continue
			}
			if refused != nil {
				sc.DryRun, sc.Refused = true, refused.Error()
			}
			if err := l.reconcileRoot(ctx, sc, titleIDs, false); err != nil {
				return out, err
			}
			out = append(out, sc)
		}
	}
	return out, nil
}

// reconcileRoot finds sc's root's vanished links (sc.Vanished) and, unless sc is a dry run, records them. Every
// read of the root is bounded through the root prober (rootread.go): a root that stops answering part way is skipped
// whole (sc.Skipped), with nothing recorded.
func (l *Library) reconcileRoot(ctx context.Context, sc *LinkScan, titleIDs []int64, dryRun bool) error {
	why, err := rootUnavailable(ctx, sc.Root)
	if Unavailable(err) {
		l.skipRoot(sc, err)
		return nil
	}
	if err != nil {
		return err
	}
	if why != "" {
		sc.Skipped = why
		l.log.Warn("library: root not reconciled", "version", sc.Version, "root", sc.Root, "reason", why)
		return nil
	}
	if titleIDs != nil && len(titleIDs) == 0 {
		return nil
	}
	now := store.Millis(l.now())
	// The links under the root: path in [root/, root0), '0' being the byte after '/', is exactly the paths that
	// start with root/ (binary collation).
	root := filepath.Clean(sc.Root)
	q := `SELECT l.id, l.path, l.target FROM link l
		JOIN link_version lv ON lv.link_id = l.id JOIN version v ON v.id = lv.version_id
		WHERE v.policy_key = ? AND l.state = 'active' AND l.path >= ? AND l.path < ? AND NOT EXISTS (SELECT 1
			FROM library_claim c JOIN download d ON d.id = c.download_id WHERE d.version_id = v.id AND c.until > ?)`
	args := []any{sc.Version, root + string(filepath.Separator), root + "0", now}
	// total is the root's links, the failsafe's measure, also for a scan of some titles (a season
	// deleted from one series is not a quarter of the root).
	total := -1
	if titleIDs != nil {
		if err := l.st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM (`+q+`)`, args...).Scan(&total); err != nil {
			return err
		}
		ids, err := json.Marshal(titleIDs)
		if err != nil {
			return err
		}
		q += ` AND l.title_id IN (SELECT value FROM json_each(?))`
		args = append(args, string(ids))
	}
	rows, err := l.st.Reader().QueryContext(ctx, q+` ORDER BY l.path`, args...)
	if err != nil {
		return err
	}
	var links []VanishedLink
	for rows.Next() {
		var lk VanishedLink
		if err := rows.Scan(&lk.LinkID, &lk.Path, &lk.Target); err != nil {
			rows.Close()
			return err
		}
		links = append(links, lk)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if total < len(links) {
		total = len(links)
	}
	// By path, so the links of one directory share a bounded read; reported by link id, as before.
	paths := make([]string, len(links))
	for i, lk := range links {
		paths[i] = lk.Path
	}
	missing, err := missingPaths(ctx, paths)
	if Unavailable(err) {
		l.skipRoot(sc, err)
		return nil
	}
	if err != nil {
		return err
	}
	for i, lk := range links {
		if missing[i] {
			sc.Vanished = append(sc.Vanished, lk)
		}
	}
	slices.SortFunc(sc.Vanished, func(a, b VanishedLink) int { return cmp.Compare(a.LinkID, b.LinkID) })
	if len(sc.Vanished) == 0 || sc.DryRun || dryRun {
		return nil
	}
	// Failsafe: a share mounted below the root that is not mounted now (an empty mountpoint) makes
	// every link in it look deleted, and each would be wanted again (a re-grab). More than a quarter of the root's
	// links vanishing in one pass is taken for that: nothing is recorded, and an alert action is written, which the
	// health check (VanishFailsafeCheck) and its webhook report. A person who did delete them runs a reconcile with
	// the share in place, or unlinks them. A scan of some titles is measured against the whole root too.
	if n := len(sc.Vanished); n > vanishFloor && n*4 > total {
		sc.Skipped = fmt.Sprintf("failsafe: %d of the root's %d links are missing at once (more than a quarter); "+
			"none is marked missing: is a share below the root not mounted?", n, total)
		sc.Vanished = nil
		l.log.Warn("library: reconcile failsafe: too many links missing at once; left as they are", "version",
			sc.Version, "root", sc.Root, "missing", n, "links", total)
		return l.record(ctx, nil, action{Kind: "alert", Mode: "refused", Version: sc.Version, Path: sc.Root,
			Detail: VanishFailsafeDetail + sc.Skipped})
	}
	// Each vanished link is checked again under its link lease (linklock.go), held through the commit: a refresh,
	// rename or import changing that path right now (its old symlink removed before its commit) is left to it. The
	// check is bounded too: a root that stops answering now is skipped with the leases released, nothing recorded.
	ctx, unlockAll, _ := l.lockLinks(ctx)
	defer unlockAll()
	vanished := sc.Vanished[:0]
	for _, lk := range sc.Vanished {
		if _, _, err := l.lockLinks(ctx, lk.Path); err != nil {
			l.log.Info("library: reconcile left a link another run is changing", "link", lk.Path, "err", err)
			continue
		}
		gone, err := missingPaths(ctx, []string{lk.Path})
		if Unavailable(err) {
			sc.Vanished = nil
			l.skipRoot(sc, err)
			return nil
		}
		if err != nil {
			return err
		}
		if !gone[0] {
			continue
		}
		vanished = append(vanished, lk)
	}
	sc.Vanished = vanished
	if len(vanished) == 0 {
		return nil
	}
	return l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := l.linksHeldTx(ctx, tx); err != nil {
			return err
		}
		for _, lk := range sc.Vanished {
			// Only the row the check saw: a rename that moved it meanwhile is not a vanished link.
			res, err := tx.ExecContext(ctx, `UPDATE link SET state = 'unlinked', last_checked_at = ?, updated_at = ?
				WHERE id = ? AND path = ? AND target = ? AND state = 'active'`, now, now, lk.LinkID, lk.Path, lk.Target)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			if err := reconcileRemovedLink(ctx, tx, lk.LinkID, now); err != nil {
				return err
			}
			if err := l.record(ctx, tx, action{Kind: "dead", Mode: "applied", Version: sc.Version, LinkID: lk.LinkID,
				Path: lk.Path, Target: lk.Target, Detail: "vanished: the symlink is no longer in the library",
				Rel: linkFacts(ctx, tx, lk.LinkID, lk.Path)}); err != nil {
				return err
			}
		}
		return nil
	})
}

// scanCheck is the gate's word on a scan of version's root: refused is why the scan may only report (a dry run),
// down why the root is skipped whole (ErrRootDown): it, or a filesystem mounted below it, does not answer, and a
// walk would block on it with nothing to bound it (Gate.Reachable: also for a version the gate refuses before its
// roots, shadow or library off).
func (l *Library) scanCheck(ctx context.Context, version, root string) (refused, down error) {
	err := l.gate.CheckVersion(version)
	if Unavailable(err) {
		return nil, err
	}
	if derr := l.gate.Reachable(ctx, root); derr != nil {
		return err, derr
	}
	return err, nil
}

// skipRoot notes a root left out of a scan because it does not answer.
func (l *Library) skipRoot(sc *LinkScan, down error) {
	sc.Skipped = "unreachable: " + down.Error()
	l.log.Warn("library: root not scanned: it does not answer", "version", sc.Version, "root", sc.Root, "err", down)
}
