package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// install is one write of planned links through installLinks: the version, its root and plan, each link's file
// inputs (files, by link path), the version's current files (existing, series only), and what the caller does around
// the links. An import (apply) holds its claim, records its content, provider and download rows, and undoes a run
// whose download was removed; every hook may be nil.
type install struct {
	v        *importVersion
	root     string
	plan     *Plan
	files    map[string]linkedFile
	existing []existingFile
	// downloadID is the download the journal's actions name (0: none) and the one errors name.
	downloadID int64
	now        int64 // the time the rows record (store.Millis)
	// begin runs once the link leases and the torrent delete fence are held, before anything changes.
	begin func(ctx context.Context) error
	// fence runs before each irreversible link step; its error goes to stopped.
	fence func(ctx context.Context) error
	// stopped ends a run a fence or the commit stopped: made are the links this run made, moved the files it moved
	// aside. It returns the error installLinks returns (nil stopped: the cause itself).
	stopped func(ctx context.Context, made []PlannedLink, moved []trashed, cause error) error
	// claimTx runs first in the commit; beginTx after the link leases are confirmed there, with the links made or in
	// place.
	claimTx func(ctx context.Context, tx *sql.Tx) error
	beginTx func(ctx context.Context, tx *sql.Tx, linked []PlannedLink) error
	// row gives the values of a planned link's row (it may record the link's content file first).
	row func(ctx context.Context, tx *sql.Tx, lk *PlannedLink) (linkRow, error)
	// linkTx runs after each link's rows and before its journal action; detail is that action's detail.
	linkTx func(ctx context.Context, tx *sql.Tx, lk *PlannedLink) error
	detail func(lk *PlannedLink) string
	// linkedTx runs once every link is written, before the replaced files' rows (linked: the links made or in
	// place).
	linkedTx func(ctx context.Context, tx *sql.Tx, linked []PlannedLink) error
	// endTx runs last in the commit, with the run's outcome so far.
	endTx func(ctx context.Context, tx *sql.Tx, r *installed) error
	// after runs once the commit is done, before the version's states are recomputed.
	after func(ctx context.Context)
}

// linkRow is what a planned link's row records besides its path, target and file inputs.
type linkRow struct {
	contentFileID any // the content_file row (nil: none)
	downloadID    int64
	release       string
}

// installed is what installLinks did: the links made or found in place, the files moved aside, the failures, and
// how many of those are a concurrent change of a path (retried, not failed).
type installed struct {
	linked   []PlannedLink
	moved    []trashed
	failures []string
	raced    int
}

// installLinks writes planned links: under one lease per link path (linklock.go) and the torrent delete fence held
// through the commit (refFence), each link is made (a different symlink at its path copied to the trash first),
// then the files the links replace are moved aside; one transaction records the link, link_version and link_episode
// rows, the trash copies, the replaced links' state and the journal. After the commit the version's states are
// recomputed (storeCutoffs) and Plex is asked to scan each changed folder. A link that fails is a failure in the
// result; the caller decides what that means (an import records partial).
func (l *Library) installLinks(ctx context.Context, in *install) (*installed, error) {
	v, root, plan := in.v, in.root, in.plan
	stopped := func(ctx context.Context, made []PlannedLink, moved []trashed, err error) error {
		if in.stopped == nil {
			return err
		}
		return in.stopped(ctx, made, moved, err)
	}
	fence := func(ctx context.Context) error {
		if in.fence == nil {
			return nil
		}
		return in.fence(ctx)
	}
	// One mutation per link path (linklock.go): every path this write makes or moves aside is held from here to the
	// commit. A path another run is changing now leaves the write to a later try, before anything changed.
	var paths []string
	for _, lk := range plan.Links {
		paths = append(append(paths, lk.Path), lk.Replaces...)
	}
	ctx, unlock, err := l.lockLinks(ctx, paths...)
	defer unlock()
	if err != nil {
		return nil, errf(in.downloadID, "%w", err)
	}
	// The torrent delete fence is held from before the first link is made through the commit (refFence).
	// A write that waited for a delete plans again: the delete may have taken its files.
	ctx, waited, release, err := l.refFence(ctx)
	defer release()
	if err != nil {
		return nil, errf(in.downloadID, "%w", err)
	}
	if waited {
		return nil, errf(in.downloadID, "%w: a torrent delete ran while the import waited; it plans again", ErrLinkChanged)
	}
	if in.begin != nil {
		if err := in.begin(ctx); err != nil {
			return nil, err
		}
	}
	now := in.now
	var moved, gone []trashed
	var failures []string
	raced := 0 // failures from a concurrent change of a path: the import is retried, not failed
	var linked, made []PlannedLink
	var pending []int
	for i := range plan.Links {
		pending = append(pending, i)
	}
	for len(pending) > 0 {
		var deferred []int
		for _, i := range pending {
			lk := &plan.Links[i]
			// An in-place replacement cannot be undone by keeping the old path. Wait until its other episodes
			// have links before overwriting it, just as removals below wait for the successful batch.
			if !lk.Exists && unsafeSeriesReplacements(append(linked, *lk), in.existing)[lk.Path] {
				deferred = append(deferred, i)
				continue
			}
			fail := func(msg string) {
				failures = append(failures, msg)
				lk.Error = msg
			}
			// The path must still hold what the plan saw, and no other title's or version's link (linkReady).
			if why, err := l.linkReady(ctx, v, lk); err != nil {
				return nil, err
			} else if why != "" {
				raced++
				fail(why)
				continue
			}
			// A link in place already (this import, interrupted after it, made it) still moves its old files aside.
			if !lk.Exists {
				// Each replacement is fenced: an import that lost its claim (or its job lease) stops before it.
				if err := fence(ctx); err != nil {
					return nil, stopped(ctx, made, moved, err)
				}
				// A different link already at this path: keep a copy in the trash before it is replaced.
				if fi, err := os.Lstat(lk.Path); err == nil {
					if fi.Mode()&os.ModeSymlink == 0 {
						fail(fmt.Sprintf("%s exists and is not a symlink", lk.Path))
						continue
					}
					tp, target, err := l.w.copyToTrash(v.Key, root, lk.Path)
					if err != nil {
						fail(err.Error())
						continue
					}
					moved = append(moved, trashed{v.Key, lk.Path, tp, target, "replaced in place", lk.Path, 0})
				}
				if err := l.w.symlink(v.Key, lk.Target, lk.Path); err != nil {
					fail(err.Error())
					_ = l.record(ctx, nil, action{Kind: "link", Mode: modeOf(err), Version: v.Key, DownloadID: in.downloadID,
						Path: lk.Path, Target: lk.Target, Detail: err.Error()})
					continue
				}
				made = append(made, *lk)
			}
			lk.Done = true
			linked = append(linked, *lk)
		}
		if len(deferred) == len(pending) {
			for _, i := range deferred {
				lk := &plan.Links[i]
				lk.Error = fmt.Sprintf("old file %s not replaced: batch did not link every episode", lk.Path)
				failures = append(failures, lk.Error)
			}
			break
		}
		pending = deferred
	}
	// Make every new link before removing old ones. A failed link must not leave a shared old file's other
	// episodes uncovered, even though the whole batch passed planning. The retry finishes the replacement.
	unsafe := unsafeSeriesReplacements(linked, in.existing)
	for i := range plan.Links {
		lk := &plan.Links[i]
		if !lk.Done {
			continue
		}
		fail := func(msg string) {
			failures = append(failures, msg)
			lk.Error = msg
		}
		for j, old := range lk.Replaces {
			if unsafe[old] {
				continue
			}
			// The replaced link by its id: a rename since the plan moved it, and that path is the one to move aside.
			oldID := lk.replaceID(j)
			if oldID > 0 {
				var cur string
				err := l.st.Reader().QueryRowContext(ctx, `SELECT path FROM link WHERE id = ? AND state = 'active'`,
					oldID).Scan(&cur)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return nil, err
				}
				if cur != "" && cur != old {
					l.log.Info("library: replaced link was renamed since the plan", "download", in.downloadID, "link", oldID,
						"planned", old, "now", cur)
					old = cur
					if _, _, err := l.lockLinks(ctx, old); err != nil {
						raced++
						fail(err.Error())
						continue
					}
				}
			}
			fi, err := os.Lstat(old)
			if errors.Is(err, os.ErrNotExist) {
				// Gone already (an interrupted run of this import moved it aside): its row follows.
				gone = append(gone, trashed{version: v.Key, path: old, linkPath: lk.Path, linkID: oldID})
				continue
			}
			if err != nil || fi.Mode()&os.ModeSymlink == 0 {
				fail(fmt.Sprintf("old file %s not moved aside: not a symlink", old))
				continue
			}
			if err := fence(ctx); err != nil {
				return nil, stopped(ctx, made, moved, err)
			}
			tp, target, err := l.w.copyToTrash(v.Key, root, old)
			if err == nil {
				err = l.w.removeLink(v.Key, old)
			}
			if err != nil {
				fail(err.Error())
				continue
			}
			moved = append(moved, trashed{v.Key, old, tp, target, "upgrade", lk.Path, oldID})
		}
	}

	var strays []trashed
	err = l.refWriteTx(ctx, func(tx *sql.Tx) error {
		if in.claimTx != nil {
			if err := in.claimTx(ctx, tx); err != nil {
				return err
			}
		}
		if err := l.linksHeldTx(ctx, tx); err != nil {
			return err
		}
		if in.beginTx != nil {
			if err := in.beginTx(ctx, tx, linked); err != nil {
				return err
			}
		}
		// The old files' release facts, before a link at the same path is overwritten (History, 00129).
		oldRel := map[string]relFacts{}
		for _, m := range append(slices.Clone(moved), gone...) {
			oldRel[m.path] = linkFacts(ctx, tx, m.linkID, m.path)
		}
		for _, lk := range linked {
			var row linkRow
			if in.row != nil {
				var err error
				if row, err = in.row(ctx, tx, &lk); err != nil {
					return err
				}
			}
			if err := takePathTx(ctx, tx, lk.Path, v.Title.ID, v.VersionID, lk.Target); err != nil {
				return err
			}
			var linkID int64
			lf := in.files[lk.Path]
			// The row at the path is this title's (takePathTx): its title never changes, and a new file starts with
			// no noted miss, none of an adopted file's facts, and no folder hash (usage finds the new one).
			err := tx.QueryRowContext(ctx, `INSERT INTO link (path, target, kind, content_file_id, title_id, download_id,
					release_title, quality, score, file_metadata, state, last_checked_at, created_at, updated_at)
				VALUES (?, ?, 'symlink', ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?)
				ON CONFLICT (path) DO UPDATE SET target = excluded.target, content_file_id = excluded.content_file_id,
					download_id = excluded.download_id, release_title = excluded.release_title,
					quality = excluded.quality, score = excluded.score, state = 'active', adopted = 0,
					adopted_from = NULL, adopted_source = NULL, adopted_media = NULL, folder_hash = NULL,
					file_metadata = excluded.file_metadata, missing_since = NULL,
					last_checked_at = excluded.last_checked_at, updated_at = excluded.updated_at
				WHERE link.title_id = excluded.title_id
				RETURNING id`, lk.Path, lk.Target, row.contentFileID, v.Title.ID, row.downloadID, row.release, lf.quality,
				lf.score, lf.metadata, now, now, now).
				Scan(&linkID)
			if errors.Is(err, sql.ErrNoRows) {
				return linkChanged(0, lk.Path, lk.Target, "the path holds another title's link")
			}
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO link_version (link_id, version_id) VALUES (?, ?)`, linkID, v.VersionID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM link_episode WHERE link_id = ?`, linkID); err != nil {
				return err
			}
			for _, e := range lk.Episodes {
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO link_episode (link_id, episode_id) VALUES (?, ?)`, linkID, e); err != nil {
					return err
				}
			}
			if lf.state != "" {
				if err := recordImportedState(ctx, tx, v, lk.Episodes, lf.state, len(failures) > 0, now); err != nil {
					return err
				}
			}
			if in.linkTx != nil {
				if err := in.linkTx(ctx, tx, &lk); err != nil {
					return err
				}
			}
			detail := ""
			if in.detail != nil {
				detail = in.detail(&lk)
			}
			if err := l.record(ctx, tx, action{Kind: "link", Mode: "applied", Version: v.Key, DownloadID: in.downloadID,
				LinkID: linkID, Path: lk.Path, Target: lk.Target, Detail: detail, Rel: relFacts{Release: row.release,
					Quality: lf.quality, Score: sql.NullInt64{Int64: int64(lf.score), Valid: true}, TitleID: v.Title.ID,
					Episodes: lk.Episodes}}); err != nil {
				return err
			}
		}
		if in.linkedTx != nil {
			if err := in.linkedTx(ctx, tx, linked); err != nil {
				return err
			}
		}
		strays = nil
		for _, m := range moved {
			var oldID sql.NullInt64
			if m.linkID > 0 && m.path != m.linkPath {
				// By id: a rename that moved the row after the old symlink was moved aside leaves its new symlink,
				// which goes too once this commits (strays).
				oldID = sql.NullInt64{Int64: m.linkID, Valid: true}
				var cur string
				if err := tx.QueryRowContext(ctx, `SELECT path FROM link WHERE id = ? AND state = 'active'`,
					m.linkID).Scan(&cur); err == nil && cur != m.path {
					strays = append(strays, trashed{version: m.version, path: cur, target: m.target, reason: "upgrade",
						linkPath: m.linkPath, linkID: m.linkID})
				} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
			} else {
				_ = tx.QueryRowContext(ctx, `SELECT id FROM link WHERE path = ?`, m.path).Scan(&oldID)
			}
			if oldID.Valid && m.path != m.linkPath {
				if _, err := tx.ExecContext(ctx, `UPDATE link SET state = 'unlinked', updated_at = ? WHERE id = ?`, now, oldID.Int64); err != nil {
					return err
				}
				if err := reconcileRemovedLink(ctx, tx, oldID.Int64, now); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO link_trash (link_id, version_key, path, trash_path, target, reason, moved_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)`, oldID, m.version, m.path, m.trashPath, m.target, m.reason, now); err != nil {
				return err
			}
			if err := l.record(ctx, tx, action{Kind: "replace", Mode: "applied", Version: m.version, DownloadID: in.downloadID,
				Path: m.path, Target: m.target, Detail: "moved aside to " + m.trashPath, Rel: oldRel[m.path]}); err != nil {
				return err
			}
		}
		for _, m := range gone {
			if m.path == m.linkPath {
				continue
			}
			linkID := m.linkID
			if linkID == 0 {
				if err := tx.QueryRowContext(ctx, `SELECT id FROM link WHERE path = ? AND state = 'active'`,
					m.path).Scan(&linkID); errors.Is(err, sql.ErrNoRows) {
					continue
				} else if err != nil {
					return err
				}
			}
			res, err := tx.ExecContext(ctx, `UPDATE link SET state = 'unlinked', updated_at = ? WHERE id = ?
				AND state IN ('active', 'dead')`, now, linkID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			if err := reconcileRemovedLink(ctx, tx, linkID, now); err != nil {
				return err
			}
			if err := l.record(ctx, tx, action{Kind: "replace", Mode: "applied", Version: m.version, DownloadID: in.downloadID,
				Path: m.path, Detail: "already gone from the library (moved aside by an interrupted import)",
				Rel: oldRel[m.path]}); err != nil {
				return err
			}
		}
		if in.endTx != nil {
			return in.endTx(ctx, tx, &installed{linked: linked, moved: moved, failures: failures, raced: raced})
		}
		return nil
	})
	if err != nil {
		return nil, stopped(ctx, made, moved, err)
	}
	if in.after != nil {
		in.after(ctx)
	}
	l.storeCutoffs(ctx, []stateVersion{{id: v.VersionID, titleID: v.Title.ID, key: v.Key}})
	for _, m := range strays {
		l.moveStray(ctx, in.downloadID, root, m)
	}
	dirs := map[string]bool{}
	for _, lk := range linked {
		dirs[filepath.Dir(lk.Path)] = true
	}
	for _, m := range moved {
		dirs[filepath.Dir(m.path)] = true
	}
	for dir := range dirs {
		if err := l.EnqueueScan(ctx, v.Key, dir); err != nil {
			l.log.Warn("library: plex scan not queued", "dir", dir, "err", err)
		}
	}
	return &installed{linked: linked, moved: moved, failures: failures, raced: raced}, nil
}
