package library

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// The old copy of a file that broke while someone played it, when it comes back (docs/design/library.md "A file that
// broke while playing"). While its incident is open the hourly sweep leaves its dead link alone (playingHeld): it comes
// back only through a read. After each sweep the library's maintenance calls ComeBack, which reads each open row's own
// file at its saved range, the least recently checked first (a read that times out ends the pass): a file that reads on two sweeps at least deadConfirm apart, whose film or episodes no other file
// holds, is revived as the sweep would revive it. A file other files now hold (in part or whole), or that an import
// replaced, is kept aside (comeback_covered.go).

// PlayingPiece is how much a check of a file reads, PlayingPieceLimit its time limit (now playing's check and
// ComeBack).
const (
	PlayingPiece      = 256 << 10
	PlayingPieceLimit = 15 * time.Second
)

// comeBackLimit is a come-back read's time limit (tests shorten it).
var comeBackLimit = PlayingPieceLimit

// playingHeld are the open incidents' links by path, with the old target: a dead link still pointing there is the
// incident's to bring back. An incident's dead link that belongs only to removed versions (removed with its files
// kept) stays held after its row settled: the sweep neither revives, retires nor rewrites it.
func playingHeld(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT pb.path, pb.old_target FROM playing_break pb WHERE pb.settled = ''
		OR EXISTS (SELECT 1 FROM link l WHERE l.path = pb.path AND l.target = pb.old_target AND l.state = 'dead'
			AND EXISTS (SELECT 1 FROM link_version lv WHERE lv.link_id = l.id)
			AND NOT EXISTS (SELECT 1 FROM link_version lv WHERE lv.link_id = l.id AND NOT EXISTS (
				SELECT 1 FROM arrview_removed_version rv WHERE rv.version_id = lv.version_id)))`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, t string
		if err := rows.Scan(&p, &t); err != nil {
			return nil, err
		}
		out[p] = t
	}
	return out, rows.Err()
}

// LastSweep is when a relink sweep of this library last ended without error (zero: none yet).
func (l *Library) LastSweep() time.Time {
	if ms := l.swept.Load(); ms > 0 {
		return time.UnixMilli(ms)
	}
	return time.Time{}
}

// returned is an open row whose old copy is not in place: its link still the dead old copy, or replaced (at the same
// path or another one).
type returned struct {
	id, linkID, titleID int64
	incident            int64
	version, path, old  string
	hash, inner         string
	offset              int64
	backSince           sql.NullInt64
	// dead: the link at the row's path is still the dead old copy.
	dead bool
}

// replacedAside is how long a replaced row is read: older, it is settled aside unread.
const replacedAside = 30 * 24 * time.Hour

// ComeBack reads the old copy of each open incident row whose copy is not in place (its link still dead at the old
// target, or replaced, or other active files hold its film or some of its saved episodes): listed where the sweep's
// own folder choice puts it and read at its saved range. A read starts or continues its streak (back_since), an
// unlisted file or a failed read ends it; a read at least deadConfirm after the streak began decides: reviveReturned
// for a dead link nothing else covers, settleAside (kept aside) for the rest. A replaced row older than 30 days is
// settled aside unread. A mount that does not answer, or a read that times out, ends the pass; rows are read the least
// recently checked first, so a file that always hangs comes last.
func (l *Library) ComeBack(ctx context.Context) error {
	if l.mount == nil {
		return nil
	}
	if err := l.asideOldReplaced(ctx); err != nil {
		return err
	}
	if err := l.settleGone(ctx); err != nil {
		return err
	}
	rows, err := l.st.Reader().QueryContext(ctx, `SELECT pb.id, pb.incident, pb.link_id, pb.title_id, pb.version_key,
		pb.path, pb.old_target, pb.info_hash, pb.inner_file, pb.read_offset, pb.back_since,
		EXISTS (SELECT 1 FROM link l WHERE l.id = pb.link_id AND l.path = pb.path AND l.target = pb.old_target
			AND l.state = 'dead') FROM playing_break pb
		WHERE pb.settled = '' AND NOT EXISTS (SELECT 1 FROM link l WHERE l.path = pb.path AND l.target = pb.old_target
			AND l.state = 'active') AND (pb.fixed_by = 'replaced' OR EXISTS (SELECT 1 FROM link l WHERE l.id = pb.link_id
			AND l.path = pb.path AND l.target = pb.old_target AND l.state = 'dead') OR EXISTS (
			-- what is active now for its film or saved episodes (a partial import may have moved its link aside)
			SELECT 1 FROM link o JOIN link_version ov ON ov.link_id = o.id
			JOIN version v ON v.id = ov.version_id AND v.title_id = pb.title_id AND v.policy_key = pb.version_key
			JOIN title t ON t.id = v.title_id WHERE o.state = 'active' AND (t.kind <> 'series' OR EXISTS (
				SELECT 1 FROM link_episode oe WHERE oe.link_id = o.id
				AND oe.episode_id IN (SELECT value FROM json_each(pb.episodes))))))
		AND NOT (pb.fixed_by = 'replaced' AND pb.broken_at < ?) -- expired: settled unread (asideOldReplaced)
		ORDER BY pb.updated_at, pb.id -- least recently checked first`, store.Millis(l.now().Add(-replacedAside)))
	if err != nil {
		return err
	}
	var todo []returned
	for rows.Next() {
		var r returned
		if err := rows.Scan(&r.id, &r.incident, &r.linkID, &r.titleID, &r.version, &r.path, &r.old, &r.hash, &r.inner,
			&r.offset, &r.backSince, &r.dead); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(todo) == 0 {
		return nil
	}
	// Where each old copy is now is the sweep's own choice (pickFolder: its saved hash, the folder names, a reused bare
	// name, decypharr's current name, zurg's and decypharr's confirmation), made before any read; only that place is
	// read. One listing of the release root and one cache of decypharr's file lists serve the whole pass.
	ix, err := l.newMountIndex(ctx)
	if err != nil {
		return nil // a mount that does not list: no verdict this pass
	}
	db, lists := l.st.Reader(), relinkLists{}
	rootUp := map[string]bool{} // a library root answers and can tell a missing link (rootUnavailable), this pass
	for _, r := range todo {
		if r.dead {
			// Its symlink removed from the library by hand: never read again (a link without a library path does not
			// come back). Only a root that answers can say so; a share that is away leaves the row for the next pass.
			gone, ok := l.symlinkGone(ctx, r, rootUp)
			if !ok {
				continue
			}
			if gone {
				if err := l.settleUnread(ctx, r.id); err != nil {
					return err
				}
				continue
			}
		}
		read, ok := l.mount.FromLink(r.old)
		if !ok {
			continue
		}
		rel, err := filepath.Rel(l.mount.AllPath(), read)
		if err != nil || escapes(rel) {
			continue
		}
		folder, inner, _ := strings.Cut(rel, string(filepath.Separator))
		ml := mountLink{path: r.path, target: r.old, folder: folder, inner: inner}
		listed, err := ix.has(folder, inner)
		if errors.Is(err, errFolderUnreadable) {
			continue
		}
		if err != nil {
			return nil
		}
		// Only the place the selection returns is read: never a fallback to the old path.
		_, newFolder, target, err := l.pickFolder(ctx, db, ix, lists, ml, listed, false, "", r.hash)
		if err != nil {
			return err
		}
		if target == r.old && len(r.hash) == 40 {
			// The old folder may now be another torrent's (the old one left, another took the name): what zurg or
			// decypharr says holds it must be this hash. With no such evidence (a plain mount) it is read as it is.
			if _, ok := l.zurgConfirm(ctx, r.hash, folder, inner); !ok || !l.relinkHolds(ctx, db, lists, ml, r.hash, folder) {
				target = ""
			}
		}
		if target == "" {
			if err := l.setBack(ctx, r.id, false); err != nil { // not where its torrent is: the streak ends
				return err
			}
			continue
		}
		_, err = l.ReadPiece(ctx, target, r.offset, PlayingPiece, comeBackLimit)
		switch {
		case errors.Is(err, ErrPieceNoVerdict):
			continue
		case err != nil:
			if err := l.setBack(ctx, r.id, false); err != nil {
				return err
			}
			if errors.Is(err, errPieceTimedOut) {
				// A read that hangs ends the pass: the next one goes on, this row last (stamped checked).
				l.log.Info("library: a returned copy's read timed out; the rest wait for the next sweep", "link", r.path)
				return nil
			}
			continue
		case !r.backSince.Valid || l.now().Sub(time.UnixMilli(r.backSince.Int64)) < deadConfirm:
			if err := l.setBack(ctx, r.id, true); err != nil {
				return err
			}
			continue
		}
		if !r.dead {
			if err := l.putBackOrAside(ctx, r, target, newFolder); err != nil {
				return err
			}
			continue
		}
		if err := l.reviveReturned(ctx, r, target, newFolder); err != nil {
			return err
		}
	}
	return nil
}

// symlinkGone reports whether a dead row's library symlink is confirmed missing: its library root answers and is
// listable (as the sweep requires before it trusts a root), and the path's bounded lstat says it does not exist. ok
// false: nothing can be said now (the root away, empty or not answering; no root of its version holds the path).
func (l *Library) symlinkGone(ctx context.Context, r returned, rootUp map[string]bool) (gone, ok bool) {
	root := ""
	for _, rt := range l.gate.Roots(r.version) {
		if p := filepath.Clean(rt) + string(filepath.Separator); strings.HasPrefix(r.path, p) {
			root = rt
		}
	}
	if root == "" {
		return false, false
	}
	up, seen := rootUp[root]
	if !seen {
		why, err := rootUnavailable(ctx, root)
		up = err == nil && why == ""
		rootUp[root] = up
	}
	if !up {
		return false, false
	}
	missing, err := missingPaths(ctx, []string{r.path})
	if err != nil {
		return false, false
	}
	return missing[0], true
}

// setBack starts (back) or ends a row's streak of reads, and stamps the row checked now (updated_at: a pass reads the
// least recently checked rows first).
func (l *Library) setBack(ctx context.Context, id int64, back bool) error {
	now := store.Millis(l.now())
	q := `UPDATE playing_break SET back_since = NULL, updated_at = ? WHERE id = ?`
	args := []any{now, id}
	if back {
		q = `UPDATE playing_break SET back_since = COALESCE(back_since, ?), updated_at = ? WHERE id = ?`
		args = []any{now, now, id}
	}
	_, err := l.st.Writer().ExecContext(ctx, q, args...)
	return err
}

// reviveReturned brings back an old copy that read on two sweeps, when nothing else holds any of its film or
// episodes (a file held in part or whole by another is kept aside: settleAside), and only with Pause off and the
// version's library writing; whatever the monitoring is now, as the sweep's own revive (a row whose link was removed
// or whose title is gone was settled unread before: settleGone). In place it is the sweep's revive: active again, its items filled, Plex scanned, the row settled revived,
// and the release's router:playing miss lifted. In a renamed folder it is the sweep's repoint (repointReturned).
func (l *Library) reviveReturned(ctx context.Context, r returned, target, newFolder string) error {
	if !l.writableNow(r.version) {
		return nil
	}
	db := l.st.Reader()
	if held, err := partlyCovered(ctx, db, r.linkID); err != nil {
		return err
	} else if held {
		return l.settleAside(ctx, r, asideWords)
	}
	if newFolder != "" {
		return l.repointReturned(ctx, r, target, newFolder)
	}
	lctx, unlock, err := l.lockLinks(ctx, r.path)
	defer unlock()
	if err != nil {
		l.log.Info("library: returned copy left: another run is changing its link", "link", r.path, "err", err)
		return nil
	}
	now := store.Millis(l.now())
	var revived []stateVersion
	err = l.st.WriteTx(lctx, func(tx *sql.Tx) error {
		revived = nil
		if err := l.linksHeldTx(lctx, tx); err != nil {
			return err
		}
		res, err := tx.ExecContext(lctx, `UPDATE link SET state = 'active', missing_since = NULL, last_checked_at = ?,
			updated_at = ? WHERE id = ? AND path = ? AND target = ? AND state = 'dead'`, now, now, r.linkID, r.path, r.old)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return err
		}
		if revived, err = reviveLink(lctx, tx, r.linkID, now); err != nil {
			return err
		}
		if err := l.record(lctx, tx, action{Kind: "relink", Mode: "applied", Version: r.version, LinkID: r.linkID,
			Path: r.path, Target: r.old, Detail: "revived: the copy that broke while playing reads again"}); err != nil {
			return err
		}
		return settleRevivedTx(lctx, tx, r.id, r.hash, now)
	})
	if errors.Is(err, ErrLinkChanged) {
		return nil
	}
	if err != nil {
		return err
	}
	l.log.Info("library: the copy that broke while playing reads again; revived", "link", r.path, "incident_row", r.id)
	if err := l.EnqueueScan(ctx, r.version, filepath.Dir(r.path)); err != nil {
		l.log.Warn("library: plex scan not queued", "path", r.path, "err", err)
	}
	for _, v := range revived {
		if _, err := l.recomputeVersion(ctx, v); err != nil {
			l.log.Warn("library: state not recomputed after a revive", "version", v.key, "title", v.titleID, "err", err)
		}
	}
	return nil
}

// settleRevivedTx settles a row revived and lifts its release's router:playing miss.
func settleRevivedTx(ctx context.Context, tx *sql.Tx, id int64, hash string, now int64) error {
	return settleReturnTx(ctx, tx, id, hash, "revived", now)
}

// settleReturnTx settles an open row (revived or aside) and lifts its release's router:playing miss, unless another
// file of that release is still dead and unsettled (a pack's other episode that does not read).
func settleReturnTx(ctx context.Context, tx *sql.Tx, id int64, hash, settled string, now int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE playing_break SET settled = ?, updated_at = ? WHERE id = ? AND settled = ''`,
		settled, now, id); err != nil {
		return err
	}
	if hash == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM provider_cache WHERE info_hash = ? AND source = ? AND NOT EXISTS (
		SELECT 1 FROM playing_break pb JOIN link l ON l.id = pb.link_id AND l.path = pb.path AND l.target = pb.old_target
		WHERE pb.info_hash = ? AND pb.settled = '' AND l.state = 'dead')`, hash, router.PlayingMissSource, hash)
	return err
}

// repointReturned brings back an old copy that read on two sweeps in a renamed folder: the sweep's own repoint
// (under the link lease, compare and swap, the symlink rewritten, the dead row active again), and only once its link
// points there and is active, the row settled revived and the release's miss lifted.
func (l *Library) repointReturned(ctx context.Context, r returned, target, newFolder string) error {
	root := ""
	for _, rt := range l.gate.Roots(r.version) {
		if p := filepath.Clean(rt) + string(filepath.Separator); strings.HasPrefix(r.path, p) {
			root = rt
		}
	}
	if root == "" {
		return nil
	}
	read, _ := l.mount.FromLink(r.old)
	rel, _ := filepath.Rel(l.mount.AllPath(), read)
	folder, inner, _ := strings.Cut(rel, string(filepath.Separator))
	sc := &LinkScan{Version: r.version, Root: root, Causes: map[string]int{}}
	var recompute []stateVersion
	if err := l.repoint(ctx, sc, r.version, root, mountLink{path: r.path, target: r.old, folder: folder, inner: inner},
		DanglingLink{Path: r.path, Target: r.old, NewTarget: target, Hash: r.hash}, r.hash, newFolder, &recompute); err != nil {
		return err
	}
	now := store.Millis(l.now())
	settled := false
	if err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		settled = false
		var ok bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM link WHERE id = ? AND path = ? AND target = ?
			AND state = 'active')`, r.linkID, r.path, target).Scan(&ok); err != nil || !ok {
			return err
		}
		settled = true
		return settleRevivedTx(ctx, tx, r.id, r.hash, now)
	}); err != nil {
		return err
	}
	if !settled {
		l.log.Info("library: the copy that broke while playing reads again in a renamed folder, but was not repointed",
			"link", r.path, "folder", newFolder)
		return nil
	}
	l.log.Info("library: the copy that broke while playing reads again in a renamed folder; repointed and revived",
		"link", r.path, "folder", newFolder, "incident_row", r.id)
	for _, v := range recompute {
		if _, err := l.recomputeVersion(ctx, v); err != nil {
			l.log.Warn("library: state not recomputed after a revive", "version", v.key, "title", v.titleID, "err", err)
		}
	}
	return nil
}

// partlyCovered reports whether another active link of any of a link's versions holds its film, or any one of its
// episodes.
func partlyCovered(ctx context.Context, db *sql.DB, linkID int64) (bool, error) {
	var held bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM link_version lv JOIN version v ON v.id = lv.version_id JOIN title t ON t.id = v.title_id
		JOIN link_version ov ON ov.version_id = lv.version_id JOIN link o ON o.id = ov.link_id
		WHERE lv.link_id = ? AND o.id <> ? AND o.state = 'active'
		AND (t.kind <> 'series' OR EXISTS (SELECT 1 FROM link_episode me JOIN link_episode oe
			ON oe.episode_id = me.episode_id WHERE me.link_id = ? AND oe.link_id = o.id)))`, linkID, linkID,
		linkID).Scan(&held)
	return held, err
}

// SetLiveWritable sets how the come-back asks whether it may write now (PlayingWritable of the settings in force):
// a pass keeps the library it started with, and a settings save meanwhile (Pause on) must stop its writes. nil: this
// library's own PlayingWritable.
func (l *Library) SetLiveWritable(f func(version string) bool) { l.liveWritable = f }

// writableNow is whether the come-back may write for version now: the settings in force (SetLiveWritable) and this
// library's own (PlayingWritable). Asked right before each revive or aside.
func (l *Library) writableNow(version string) bool {
	return l.PlayingWritable(version) && (l.liveWritable == nil || l.liveWritable(version))
}
