package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// linkedFile is what a planned link records: its file's inputs (link.file_metadata, with the file's own aggregated
// quality, group and languages) and, for an active version, the state the file gives the version or its episodes
// ("" for any other mode).
type linkedFile struct {
	metadata, state string
	quality         string // the file's own quality (fileFacts), link.quality
	score           int
}

type trashed struct {
	version, path, trashPath, target, reason string
	linkPath                                 string // the new link that replaced it
	linkID                                   int64  // the replaced link's row (0: an arr file, or found by path)
}

// apply writes the plan: links, the trash copies of what they replace, the registry rows, the download's state,
// and the Plex scans. Filesystem steps come first; the database follows in one transaction. A crash between the two
// leaves links the database does not know: the retry plans them as in place (Exists), still moves aside the files
// they replace, and records them.
//
// Every link replaced or moved aside is fenced first (fence): an import that lost its claim, or whose job lost its
// lease, stops before the step and records nothing, so a newer run's outcome is not overwritten.
func (l *Library) apply(ctx context.Context, d *importDownload, vn *VersionNaming, root string, plan *Plan) error {
	v := &d.Version
	now := store.Millis(l.now())
	metadata, title, err := l.linkedMetadata(ctx, d)
	if err != nil {
		return errf(d.ID, "file metadata: %w", err)
	}
	var existing []existingFile
	if v.Title.Kind == "series" {
		existing, err = existingFiles(ctx, l.st.Reader(), v.VersionID, v.Title.ID)
		if err != nil {
			return err
		}
	}
	// Each link keeps its file's inputs (00038) and, for an active version, the state that file gives it: the
	// formats can differ by file name, so the cutoff is judged per file.
	files := map[string]linkedFile{}
	for i := range plan.Links {
		lk := &plan.Links[i]
		file := withFacts(*metadata, lk.facts, lk.File, lk.Target, lk.size)
		if d.Manual[lk.File].Overrides != nil {
			mf, err := l.manualMetadata(ctx, d, lk.facts, lk.File, lk.Target, lk.size)
			if err != nil {
				return err
			}
			file = *mf
		}
		if v.Policy.PreferSeaDex {
			decide.ResolveIncomingSeaDexFile(ctx, title, &file, lk.Episodes)
		}
		b, err := json.Marshal(&file)
		if err != nil {
			return errf(d.ID, "file metadata: %w", err)
		}
		lf := linkedFile{metadata: string(b), quality: file.Quality}
		lf.score = v.Score
		if file.Score != nil {
			lf.score = *file.Score
		}
		if v.Policy.Mode == policy.ModeActive {
			if title == nil {
				return errf(d.ID, "quality cutoff: decision engine is not configured")
			}
			if lf.state, err = l.importedState(v, title, &file); err != nil {
				return errf(d.ID, "quality cutoff: %w", err)
			}
		}
		files[lk.Path] = lf
	}
	// One mutation per link path (linklock.go): every path this import makes or moves aside is held from here to the
	// commit. A path another run is changing now leaves the import to the next pass, before anything changed.
	octx := ctx
	var paths []string
	for _, lk := range plan.Links {
		paths = append(append(paths, lk.Path), lk.Replaces...)
	}
	ctx, unlock, err := l.lockLinks(ctx, paths...)
	defer unlock()
	if err != nil {
		return errf(d.ID, "%w", err)
	}
	// The torrent delete fence is held from before the first link is made through the commit (refFence).
	// An import that waited for a delete plans again: the delete may have taken its files.
	ctx, waited, release, err := l.refFence(ctx)
	defer release()
	if err != nil {
		return errf(d.ID, "%w", err)
	}
	if waited {
		return errf(d.ID, "%w: a torrent delete ran while the import waited; it plans again", ErrLinkChanged)
	}
	if err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := holdsClaim(ctx, tx, d); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE download SET state = 'importing', updated_at = ? WHERE id = ?
			AND state IN ('ready', 'importing')`, now, d.ID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return errf(d.ID, "%w: no longer ready", ErrClaimed)
		}
		return nil
	}); err != nil {
		return err
	}
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
			if !lk.Exists && unsafeSeriesReplacements(append(linked, *lk), existing)[lk.Path] {
				deferred = append(deferred, i)
				continue
			}
			fail := func(msg string) {
				failures = append(failures, msg)
				lk.Error = msg
			}
			// The path must still hold what the plan saw, and no other title's or version's link (linkReady).
			if why, err := l.linkReady(ctx, v, lk); err != nil {
				return err
			} else if why != "" {
				raced++
				fail(why)
				continue
			}
			// A link in place already (this import, interrupted after it, made it) still moves its old files aside.
			if !lk.Exists {
				// Each replacement is fenced: an import that lost its claim (or its job lease) stops before it.
				if err := l.fence(ctx, d); err != nil {
					return l.stopped(ctx, d, root, plan, made, moved, err)
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
					_ = l.record(ctx, nil, action{Kind: "link", Mode: modeOf(err), Version: v.Key, DownloadID: d.ID, Path: lk.Path,
						Target: lk.Target, Detail: err.Error()})
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
	unsafe := unsafeSeriesReplacements(linked, existing)
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
					return err
				}
				if cur != "" && cur != old {
					l.log.Info("library: replaced link was renamed since the plan", "download", d.ID, "link", oldID,
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
			if err := l.fence(ctx, d); err != nil {
				return l.stopped(ctx, d, root, plan, made, moved, err)
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

	pj, _ := json.Marshal(plan)
	var strays []trashed
	err = l.refWriteTx(ctx, func(tx *sql.Tx) error {
		if err := holdsClaim(ctx, tx, d); err != nil {
			return err
		}
		if err := l.linksHeldTx(ctx, tx); err != nil {
			return err
		}
		// A client may have removed the download from the queue while the links were made (router.Dismiss): the
		// removal wins, and nothing of this import is recorded.
		if err := stillImporting(ctx, tx, d.ID); err != nil {
			return err
		}
		contentID := d.ContentID
		if contentID == 0 && d.InfoHash != "" {
			res, err := tx.ExecContext(ctx, `INSERT INTO content (info_hash, name, mount_folder, state, first_seen_at, last_seen_at)
				VALUES (?, ?, ?, 'present', ?, ?) ON CONFLICT (info_hash) DO NOTHING`,
				d.InfoHash, d.Name, filepath.Base(plan.Folder), now, now)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				contentID, _ = res.LastInsertId()
			} else if err := tx.QueryRowContext(ctx, `SELECT id FROM content WHERE info_hash = ?`, d.InfoHash).Scan(&contentID); err != nil {
				return err
			}
		}
		if contentID != 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE content SET mount_folder = ?, last_seen_at = ? WHERE id = ?`,
				filepath.Base(plan.Folder), now, contentID); err != nil {
				return err
			}
		}
		for _, lk := range linked {
			var cfID any
			if contentID != 0 {
				var probeJSON any
				if lk.probe != nil {
					b, _ := json.Marshal(lk.probe)
					probeJSON = string(b)
				}
				var id int64
				err := tx.QueryRowContext(ctx, `INSERT INTO content_file (content_id, path, size_bytes, probe, probed_at)
					VALUES (?, ?, ?, ?, ?) ON CONFLICT (content_id, path) DO UPDATE SET size_bytes = excluded.size_bytes,
					probe = COALESCE(excluded.probe, content_file.probe), probed_at = excluded.probed_at RETURNING id`,
					contentID, lk.File, lk.size, probeJSON, now).Scan(&id)
				if err != nil {
					return err
				}
				cfID = id
			}
			if err := takePathTx(ctx, tx, lk.Path, v.Title.ID, v.VersionID, lk.Target); err != nil {
				return err
			}
			var linkID int64
			lf := files[lk.Path]
			// The row at the path is this title's (takePathTx): its title never changes, and a new file starts with
			// no noted miss.
			err = tx.QueryRowContext(ctx, `INSERT INTO link (path, target, kind, content_file_id, title_id, download_id,
					release_title, quality, score, file_metadata, state, last_checked_at, created_at, updated_at)
				VALUES (?, ?, 'symlink', ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?)
				ON CONFLICT (path) DO UPDATE SET target = excluded.target, content_file_id = excluded.content_file_id,
					download_id = excluded.download_id, release_title = excluded.release_title,
					quality = excluded.quality, score = excluded.score, state = 'active', adopted = 0,
					adopted_from = NULL, file_metadata = excluded.file_metadata, missing_since = NULL,
					last_checked_at = excluded.last_checked_at, updated_at = excluded.updated_at
				WHERE link.title_id = excluded.title_id
				RETURNING id`, lk.Path, lk.Target, cfID, v.Title.ID, d.ID, d.ReleaseTitle, lf.quality, lf.score, lf.metadata, now, now, now).
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
			if err := recordFile(ctx, tx, d.ID, &lk, now); err != nil {
				return err
			}
			mode := "applied"
			detail := ""
			if lk.Exists {
				detail = "already linked"
			}
			if o := d.Manual[lk.File].Overrides; o != nil && o.ImportMode != "" {
				detail += "; client importMode " + o.ImportMode + "; Theatarr linked instead; source left in place"
			}
			if err := l.record(ctx, tx, action{Kind: "link", Mode: mode, Version: v.Key, DownloadID: d.ID, LinkID: linkID,
				Path: lk.Path, Target: lk.Target, Detail: detail}); err != nil {
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
			if m.path != m.linkPath {
				if err := retireArrFile(ctx, tx, v.VersionID, m.path, now); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO link_trash (link_id, version_key, path, trash_path, target, reason, moved_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)`, oldID, m.version, m.path, m.trashPath, m.target, m.reason, now); err != nil {
				return err
			}
			if err := l.record(ctx, tx, action{Kind: "replace", Mode: "applied", Version: m.version, DownloadID: d.ID,
				Path: m.path, Target: m.target, Detail: "moved aside to " + m.trashPath}); err != nil {
				return err
			}
		}
		for _, m := range gone {
			if m.path == m.linkPath {
				continue
			}
			if err := retireArrFile(ctx, tx, v.VersionID, m.path, now); err != nil {
				return err
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
			if err := l.record(ctx, tx, action{Kind: "replace", Mode: "applied", Version: m.version, DownloadID: d.ID,
				Path: m.path, Detail: "already gone from the library (moved aside by an interrupted import)"}); err != nil {
				return err
			}
		}
		// Every link made: imported. Some made, some failed: partial, and the download stays importing so the
		// periodic pass (after a back-off) or a manual import finishes it. None made: failed.
		state, dl := "linked", "imported"
		var lastErr any
		if len(failures) > 0 {
			lastErr = fmt.Sprint(failures)
			state, dl = "partial", "importing"
		}
		if len(linked) == 0 && raced < len(failures) {
			state, dl = "failed", "failed"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO library_import (download_id, state, mount_folder, last_error, plan, attempts, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 1, ?, ?) ON CONFLICT (download_id) DO UPDATE SET state = excluded.state,
			mount_folder = excluded.mount_folder, last_error = excluded.last_error, plan = excluded.plan,
			attempts = library_import.attempts + 1, updated_at = excluded.updated_at`,
			d.ID, state, plan.Folder, lastErr, string(pj), now, now); err != nil {
			return err
		}
		if dl == "imported" {
			_, err := tx.ExecContext(ctx, `UPDATE download SET state = 'imported', imported_at = ?, error = NULL, updated_at = ? WHERE id = ?`, now, now, d.ID)
			return err
		}
		if dl == "importing" {
			_, err := tx.ExecContext(ctx, `UPDATE download SET error = ?, updated_at = ? WHERE id = ?`,
				"library: partial import, retried: "+fmt.Sprint(failures), now, d.ID)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE download SET state = 'failed', error = ?, updated_at = ? WHERE id = ?`,
			"library: "+fmt.Sprint(failures), now, d.ID)
		return err
	})
	if errors.Is(err, ErrRemoved) {
		return l.stopped(ctx, d, root, plan, made, moved, err)
	}
	if err != nil {
		return err
	}
	for _, m := range strays {
		l.moveStray(ctx, d, root, m)
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
	if len(failures) > 0 {
		return errf(d.ID, "%d step(s) failed: %v", len(failures), failures)
	}
	l.log.Info("library: imported", "download", d.ID, "version", v.Key, "links", len(linked), "moved_aside", len(moved))
	if len(linked) > 0 && l.onImported != nil {
		l.onImported(octx, d.ID)
	}
	return nil
}

// retireArrFile drops the arr's file row (version_file) at path once an import moved that file aside, as the arr
// deletes its MovieFile/EpisodeFile on an upgrade (UpgradeMediaFileService): the arr API, decide and the wanted
// state stop seeing a file that is no longer in the library. Its file-id aliases go with it, and episodes it held
// that the new links do not cover are wanted again.
func retireArrFile(ctx context.Context, tx *sql.Tx, versionID int64, path string, now int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT vf.id, fe.episode_id FROM version_file vf
		LEFT JOIN version_file_episode fe ON fe.version_file_id = vf.id
		WHERE vf.version_id = ? AND vf.path = ? ORDER BY vf.id`, versionID, path)
	if err != nil {
		return err
	}
	var ids, episodes []int64
	for rows.Next() {
		var id int64
		var ep sql.NullInt64
		if err := rows.Scan(&id, &ep); err != nil {
			rows.Close()
			return err
		}
		if len(ids) == 0 || ids[len(ids)-1] != id {
			ids = append(ids, id)
		}
		if ep.Valid {
			episodes = append(episodes, ep.Int64)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM version_file WHERE id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM arr_alias WHERE kind IN ('moviefile', 'episodefile')
			AND target_id = ?`, id); err != nil {
			return err
		}
	}
	if len(episodes) == 0 {
		return nil
	}
	return episodesLostFile(ctx, tx, versionID, episodes, now)
}

// recordFile keeps the episode identity a linked file was imported with (download_file, source import), so a retry
// or relink of the download maps it the same way. A client's manual row is left as it is.
func recordFile(ctx context.Context, tx *sql.Tx, downloadID int64, lk *PlannedLink, now int64) error {
	if lk.How == "" || lk.How == HowManual {
		return nil
	}
	eps, _ := json.Marshal(nonNilIDs(lk.Episodes))
	_, err := tx.ExecContext(ctx, `INSERT INTO download_file (download_id, rel, episodes, source, how, updated_at)
		VALUES (?, ?, ?, 'import', ?, ?) ON CONFLICT (download_id, rel) DO UPDATE SET episodes = excluded.episodes,
		how = excluded.how, updated_at = excluded.updated_at WHERE download_file.source = 'import'`,
		downloadID, lk.File, string(eps), lk.How, now)
	return err
}

func nonNilIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

// fence is the cheap check before an irreversible library write: the job lease when the import runs inside a job
// (jobs.Fence), this import's claim, still its own and not expired, and the download, still importing (ErrRemoved
// when a client removed it from the queue meanwhile).
func (l *Library) fence(ctx context.Context, d *importDownload) error {
	if err := jobs.Fence(ctx); err != nil {
		return errf(d.ID, "stopped before a link change: %w", err)
	}
	var n int
	var state string
	if err := l.st.Reader().QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM library_claim WHERE download_id = ?
		AND owner = ? AND until > ?), COALESCE((SELECT state FROM download WHERE id = ?), '')`, d.ID, d.claim,
		store.Millis(l.now()), d.ID).Scan(&n, &state); err != nil {
		return errf(d.ID, "stopped before a link change: %w", err)
	}
	if n == 0 {
		return errf(d.ID, "%w: %w; stopped before a link change", ErrClaimed, ErrClaimLost)
	}
	if state != "importing" {
		return errf(d.ID, "%w (state %s); stopped before a link change", ErrRemoved, state)
	}
	return nil
}

// ErrRemoved: the download stopped importing while its links were being made (a client removed it from the queue,
// router.Dismiss). The import takes back what it changed in the library and records nothing else.
var ErrRemoved = errors.New("library: the download was removed during the import")

// stillImporting checks, inside the transaction that records an import, that the download is still importing.
func stillImporting(ctx context.Context, tx *sql.Tx, id int64) error {
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM download WHERE id = ?`, id).Scan(&state); err != nil {
		return err
	}
	if state != "importing" {
		return errf(id, "%w (state %s)", ErrRemoved, state)
	}
	return nil
}

// stopped ends an import that a fence or the recording stopped. A lost claim or lease leaves the library as it is:
// the run that took over finds those links in place. A removed download takes its links back (undo).
func (l *Library) stopped(ctx context.Context, d *importDownload, root string, plan *Plan, made []PlannedLink,
	moved []trashed, cause error) error {
	if !errors.Is(cause, ErrRemoved) {
		return cause
	}
	if err := l.undo(ctx, d, root, plan, made, moved, cause); err != nil {
		return fmt.Errorf("%w; undoing it: %v", cause, err)
	}
	return cause
}

// undo takes back what this run changed in the library once its download was removed: each link it made goes to
// the trash (a link it replaced in place points at its old target again), and each file it moved aside is linked
// back. Links an earlier, interrupted run left in place stay. Every trash copy is recorded (purged after
// keep_days); the import is recorded failed, and the download keeps the state the removal gave it. Steps that fail
// are recorded and reported; the others still run.
func (l *Library) undo(ctx context.Context, d *importDownload, root string, plan *Plan, made []PlannedLink,
	moved []trashed, cause error) error {
	v := &d.Version
	var failures []string
	var acts []action
	trash := append([]trashed(nil), moved...)
	inPlace := map[string]trashed{}
	for _, m := range moved {
		if m.path == m.linkPath {
			inPlace[m.path] = m
		}
	}
	for _, lk := range made {
		if m, ok := inPlace[lk.Path]; ok {
			if err := l.w.symlink(v.Key, m.target, lk.Path); err != nil {
				failures = append(failures, err.Error())
				acts = append(acts, action{Kind: "replace", Mode: modeOf(err), Version: v.Key, DownloadID: d.ID,
					Path: lk.Path, Target: m.target, Detail: "import undone: old link not restored: " + err.Error()})
				continue
			}
			acts = append(acts, action{Kind: "replace", Mode: "applied", Version: v.Key, DownloadID: d.ID,
				Path: lk.Path, Target: m.target, Detail: "import undone: the link it replaced is back"})
			continue
		}
		tp, target, err := l.w.copyToTrash(v.Key, root, lk.Path)
		if err == nil {
			err = l.w.removeLink(v.Key, lk.Path)
		}
		if err != nil {
			failures = append(failures, err.Error())
			acts = append(acts, action{Kind: "replace", Mode: modeOf(err), Version: v.Key, DownloadID: d.ID,
				Path: lk.Path, Target: lk.Target, Detail: "import undone: link not removed: " + err.Error()})
			continue
		}
		trash = append(trash, trashed{v.Key, lk.Path, tp, target, "import undone", lk.Path, 0})
		acts = append(acts, action{Kind: "replace", Mode: "applied", Version: v.Key, DownloadID: d.ID,
			Path: lk.Path, Target: target, Detail: "import undone: moved aside to " + tp})
	}
	for _, m := range moved {
		if m.path == m.linkPath {
			continue
		}
		if err := l.w.symlink(v.Key, m.target, m.path); err != nil {
			failures = append(failures, err.Error())
			acts = append(acts, action{Kind: "replace", Mode: modeOf(err), Version: v.Key, DownloadID: d.ID,
				Path: m.path, Target: m.target, Detail: "import undone: old link not restored: " + err.Error()})
			continue
		}
		acts = append(acts, action{Kind: "replace", Mode: "applied", Version: v.Key, DownloadID: d.ID,
			Path: m.path, Target: m.target, Detail: "import undone: linked back"})
	}
	now := store.Millis(l.now())
	pj, _ := json.Marshal(plan)
	lastErr := cause.Error()
	if len(failures) > 0 {
		lastErr += "; undo failed: " + fmt.Sprint(failures)
	}
	err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, t := range trash {
			var oldID sql.NullInt64
			_ = tx.QueryRowContext(ctx, `SELECT id FROM link WHERE path = ? AND state = 'active'`, t.path).Scan(&oldID)
			reason := t.reason
			if reason != "import undone" {
				reason += " (import undone, linked back)"
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO link_trash (link_id, version_key, path, trash_path, target,
				reason, moved_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, oldID, t.version, t.path, t.trashPath, t.target,
				reason, now); err != nil {
				return err
			}
		}
		for _, a := range acts {
			if err := l.record(ctx, tx, a); err != nil {
				return err
			}
		}
		if err := holdsClaim(ctx, tx, d); err != nil {
			return nil // a newer run owns the outcome
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO library_import (download_id, state, mount_folder, last_error, plan,
			attempts, created_at, updated_at) VALUES (?, 'failed', ?, ?, ?, 1, ?, ?) ON CONFLICT (download_id) DO UPDATE
			SET state = excluded.state, mount_folder = excluded.mount_folder, last_error = excluded.last_error,
			plan = excluded.plan, attempts = library_import.attempts + 1, updated_at = excluded.updated_at`,
			d.ID, plan.Folder, lastErr, string(pj), now, now)
		return err
	})
	if err != nil {
		return err
	}
	dirs := map[string]bool{}
	for _, lk := range made {
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
	l.log.Info("library: import undone, the download was removed", "download", d.ID, "version", v.Key,
		"links", len(made), "restored", len(moved), "failed", len(failures))
	if len(failures) > 0 {
		return fmt.Errorf("%d step(s) failed: %v", len(failures), failures)
	}
	return nil
}

// moveStray moves aside a replaced link that a rename moved to path after the upgrade had moved its old symlink aside
// (the row, unlinked by id, no longer holds it). Only a symlink still pointing at the replaced target goes; a failure
// is logged and left for a human (the link's row is unlinked already).
func (l *Library) moveStray(ctx context.Context, d *importDownload, root string, m trashed) {
	ctx, unlock, err := l.lockLinks(ctx, m.path)
	defer unlock()
	if err != nil {
		l.log.Warn("library: a link renamed during an upgrade was not moved aside", "download", d.ID, "link", m.linkID,
			"path", m.path, "err", err)
		return
	}
	if !linkPointsAt(m.path, m.target) {
		return
	}
	tp, target, err := l.w.copyToTrash(m.version, root, m.path)
	if err == nil {
		err = l.w.removeLink(m.version, m.path)
	}
	if err == nil {
		err = l.st.WriteTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO link_trash (link_id, version_key, path, trash_path, target, reason,
				moved_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, m.linkID, m.version, m.path, tp, target, m.reason,
				store.Millis(l.now())); err != nil {
				return err
			}
			return l.record(ctx, tx, action{Kind: "replace", Mode: "applied", Version: m.version, DownloadID: d.ID,
				LinkID: m.linkID, Path: m.path, Target: target, Detail: "moved aside to " + tp + " (renamed during the upgrade)"})
		})
	}
	if err != nil {
		l.log.Warn("library: a link renamed during an upgrade was not moved aside", "download", d.ID, "link", m.linkID,
			"path", m.path, "err", err)
	}
}

// pathRow is the link row at a path an import or adopt is about to take.
type pathRow struct {
	id, titleID   int64
	state, target string
	others        bool // the row belongs to a version other than the one taking the path too
}

// rowAtPath reads the link row at path (nil when there is none); others is about versions besides versionID.
func rowAtPath(ctx context.Context, q rowQuerier, path string, versionID int64) (*pathRow, error) {
	r := &pathRow{}
	err := q.QueryRowContext(ctx, `SELECT id, title_id, state, target, EXISTS (SELECT 1 FROM link_version
		WHERE link_id = link.id AND version_id <> ?) FROM link WHERE path = ?`, versionID, path).
		Scan(&r.id, &r.titleID, &r.state, &r.target, &r.others)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// taken says why a link of titleID to target may not take the row's path: it is another title's active link, or
// another version's active link to another file. "" when it may: no row, the version's own link (replaced in
// place), the same file shared, or a dead or unlinked row.
func (r *pathRow) taken(titleID int64, target string) string {
	switch {
	case r == nil || r.state != "active":
		return ""
	case r.titleID != titleID:
		return fmt.Sprintf("the path holds link %d of another title (%d)", r.id, r.titleID)
	case r.others && r.target != target:
		return fmt.Sprintf("the path holds link %d of another version", r.id)
	}
	return ""
}

// takePathTx makes the path free for a link row of titleID and versionID inside the commit: an active row it may
// not take is ErrLinkChanged (a change since the check under the lease), and a dead or unlinked row of another
// title, or shared with other versions, is dropped so the new row neither changes its title nor gives its versions
// this file. The version's own row is taken over by the insert that follows.
func takePathTx(ctx context.Context, tx *sql.Tx, path string, titleID, versionID int64, target string) error {
	r, err := rowAtPath(ctx, tx, path, versionID)
	if err != nil || r == nil {
		return err
	}
	if why := r.taken(titleID, target); why != "" {
		return linkChanged(r.id, path, target, why)
	}
	if r.state != "active" && (r.titleID != titleID || r.others) {
		_, err := tx.ExecContext(ctx, `DELETE FROM link WHERE id = ? AND state <> 'active'`, r.id)
		return err
	}
	return nil
}

// linkReady checks, under the import's link lease and right before the link is made, that its path still holds
// what the plan saw (the symlink it read, or this link's own target) and no other title's or version's link. It
// returns why not ("" when the link may be made): a change since the plan, which a retry plans again.
func (l *Library) linkReady(ctx context.Context, v *importVersion, lk *PlannedLink) (string, error) {
	if cur, _ := os.Readlink(lk.Path); cur != lk.prev && cur != lk.Target {
		return fmt.Sprintf("%s changed since the plan (it now points at %q); retried", lk.Path, cur), nil
	}
	r, err := rowAtPath(ctx, l.st.Reader(), lk.Path, v.VersionID)
	if err != nil {
		return "", err
	}
	if why := r.taken(v.Title.ID, lk.Target); why != "" {
		return lk.Path + ": " + why + "; retried", nil
	}
	return "", nil
}
