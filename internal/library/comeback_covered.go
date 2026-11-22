package library

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"

	"github.com/theatarr/theatarr/internal/store"
)

// The covered case of the come-back (docs/design/library.md "A file that broke while playing"): an old copy that read
// on two sweeps while other files hold its film or some of its episodes, or that an import replaced. It is kept aside:
// nothing in the library is swapped, its release's router:playing miss is lifted (ordinary searches may take it
// again), and a dead symlink of it still in the library goes to the trash so no sweep revives it beside the files
// holding its place. Putting it back automatically is not built yet. Nothing is deleted on any account.

// Words of the aside's action.
const (
	asideWords     = "the earlier copy works again; kept aside"
	asideOldWords  = "not checked again after 30 days; kept aside"
	asideTrashNote = "; its dead link moved to the trash at "
)

// asideOldReplaced settles aside, unread, the open rows an import replaced more than 30 days ago.
func (l *Library) asideOldReplaced(ctx context.Context) error {
	rows, err := l.st.Reader().QueryContext(ctx, `SELECT id, link_id, version_key, path, old_target, info_hash
		FROM playing_break WHERE settled = '' AND fixed_by = 'replaced' AND broken_at < ? ORDER BY id`,
		store.Millis(l.now().Add(-replacedAside)))
	if err != nil {
		return err
	}
	var old []returned
	for rows.Next() {
		var r returned
		if err := rows.Scan(&r.id, &r.linkID, &r.version, &r.path, &r.old, &r.hash); err != nil {
			rows.Close()
			return err
		}
		old = append(old, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range old {
		if err := l.settleAside(ctx, r, asideOldWords); err != nil {
			return err
		}
	}
	return nil
}

// settleAside settles a row aside with one action, a relink in the words given, and lifts the release's miss. A dead
// symlink of the old copy still in the library goes to the trash in the same step, as the sweep moves a covered dead
// link aside (moveDeadAside, under the path's link lease). With Pause on or the version's library not writing nothing
// is settled: a later sweep.
func (l *Library) settleAside(ctx context.Context, r returned, words string) error {
	if !l.PlayingWritable(r.version) { // Pause on or the library not writing: a later sweep
		return nil
	}
	now := store.Millis(l.now())
	relink := action{Kind: "relink", Version: r.version, LinkID: r.linkID, Path: r.path, Target: r.old, Detail: words}
	settle := func(tx *sql.Tx) error { return settleReturnTx(ctx, tx, r.id, r.hash, "aside", now) }
	var dead bool
	if err := l.st.Reader().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM link WHERE id = ? AND path = ?
		AND target = ? AND state = 'dead')`, r.linkID, r.path, r.old).Scan(&dead); err != nil {
		return err
	}
	if !dead {
		relink.Mode = "applied"
		if err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
			if err := l.record(ctx, tx, relink); err != nil {
				return err
			}
			return settle(tx)
		}); err != nil {
			return err
		}
		l.log.Info("library: the copy that broke while playing is kept aside", "link", r.path, "incident_row", r.id)
		return nil
	}
	root := ""
	for _, rt := range l.gate.Roots(r.version) {
		if p := filepath.Clean(rt) + string(filepath.Separator); strings.HasPrefix(r.path, p) {
			root = rt
		}
	}
	if root == "" {
		return nil
	}
	lctx, unlock, err := l.lockLinks(ctx, r.path)
	defer unlock()
	if err != nil {
		l.log.Info("library: aside left: another run is changing its link", "link", r.path, "err", err)
		return nil
	}
	err = l.moveDeadAside(lctx, r.version, root, mountLink{path: r.path, target: r.old}, r.linkID,
		func(tp string) action {
			a := relink
			a.Detail += asideTrashNote + tp
			return a
		}, settle)
	if errors.Is(err, ErrLinkChanged) {
		return nil
	}
	if err == nil {
		l.log.Info("library: the copy that broke while playing is kept aside; its dead link moved to the trash",
			"link", r.path, "incident_row", r.id)
	}
	return err
}
