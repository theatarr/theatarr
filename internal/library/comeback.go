package library

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// The old copy of a file that broke while someone played it, when it comes back (docs/design/library.md "A file that
// broke while playing"). While its incident is open the hourly sweep leaves its dead link alone (playingHeld): it comes
// back only through a read. After each sweep now playing's worker calls ComeBack, which reads each open row's own file
// at its saved range: a file that reads on two sweeps at least deadConfirm apart, whose film or episodes no other file
// holds, is revived as the sweep would revive it. A file another file now holds (in part or whole) is the later
// comparison's (put back or kept aside); it is left as it is.

// PlayingPiece is how much a check of a file reads, PlayingPieceLimit its time limit (now playing's check and
// ComeBack).
const (
	PlayingPiece      = 256 << 10
	PlayingPieceLimit = 15 * time.Second
)

// playingHeld are the open incidents' links by path, with the old target: a dead link still pointing there is the
// incident's to bring back.
func playingHeld(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT path, old_target FROM playing_break WHERE settled = ''`)
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

// returned is an open row whose link is still the dead old copy.
type returned struct {
	id, linkID, titleID int64
	version, path, old  string
	hash, inner         string
	offset              int64
	backSince           sql.NullInt64
}

// ComeBack reads the old copy of each open incident row whose link is still dead at its old target: listed (at its
// old target, or by its saved hash's recorded folder and inner file name when the folder was renamed) and read at its
// saved range. A read starts or continues its streak (back_since), an unlisted file or a failed read ends it; a read
// at least deadConfirm after the streak began decides: reviveReturned. A mount that does not answer ends the pass.
func (l *Library) ComeBack(ctx context.Context) error {
	if l.mount == nil {
		return nil
	}
	rows, err := l.st.Reader().QueryContext(ctx, `SELECT pb.id, pb.link_id, pb.title_id, pb.version_key, pb.path,
		pb.old_target, pb.info_hash, pb.inner_file, pb.read_offset, pb.back_since FROM playing_break pb
		JOIN link l ON l.id = pb.link_id AND l.path = pb.path AND l.target = pb.old_target AND l.state = 'dead'
		WHERE pb.settled = '' ORDER BY pb.id`)
	if err != nil {
		return err
	}
	var todo []returned
	for rows.Next() {
		var r returned
		if err := rows.Scan(&r.id, &r.linkID, &r.titleID, &r.version, &r.path, &r.old, &r.hash, &r.inner, &r.offset,
			&r.backSince); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range todo {
		target, renamed := r.old, false
		_, err := l.MountFileSize(ctx, target)
		if errors.Is(err, fs.ErrNotExist) {
			if alt := l.renamedTarget(ctx, r); alt != "" {
				target, renamed = alt, true
				_, err = l.MountFileSize(ctx, target)
			}
		}
		if Unavailable(err) {
			return nil
		}
		if err != nil {
			if err := l.setBack(ctx, r.id, false); err != nil {
				return err
			}
			continue
		}
		_, err = l.ReadPiece(ctx, target, r.offset, PlayingPiece, PlayingPieceLimit)
		switch {
		case errors.Is(err, ErrPieceNoVerdict):
			continue
		case err != nil:
			if err := l.setBack(ctx, r.id, false); err != nil {
				return err
			}
			continue
		case !r.backSince.Valid:
			if err := l.setBack(ctx, r.id, true); err != nil {
				return err
			}
			continue
		case l.now().Sub(time.UnixMilli(r.backSince.Int64)) < deadConfirm:
			continue
		}
		if err := l.reviveReturned(ctx, r, renamed); err != nil {
			return err
		}
	}
	return nil
}

// setBack starts (back) or ends a row's streak of reads.
func (l *Library) setBack(ctx context.Context, id int64, back bool) error {
	now := store.Millis(l.now())
	q := `UPDATE playing_break SET back_since = NULL, updated_at = ? WHERE id = ? AND back_since IS NOT NULL`
	args := []any{now, id}
	if back {
		q = `UPDATE playing_break SET back_since = ?, updated_at = ? WHERE id = ? AND back_since IS NULL`
		args = []any{now, now, id}
	}
	_, err := l.st.Writer().ExecContext(ctx, q, args...)
	return err
}

// renamedTarget is where a row's file is when its release folder was renamed: the folder the registry records for its
// saved hash, with the saved inner file name; "" when that is not known or not another folder.
func (l *Library) renamedTarget(ctx context.Context, r returned) string {
	if r.hash == "" || r.inner == "" {
		return ""
	}
	var folder string
	if err := l.st.Reader().QueryRowContext(ctx, `SELECT COALESCE(mount_folder, '') FROM content WHERE lower(info_hash) = ?`,
		r.hash).Scan(&folder); err != nil || folder == "" || strings.ContainsAny(folder, "/\x00") {
		return ""
	}
	alt := l.mount.ToLink(filepath.Join(l.mount.AllPath(), folder, r.inner))
	if alt == r.old {
		return ""
	}
	return alt
}

// reviveReturned brings back an old copy that read on two sweeps, when nothing else holds any of its film or
// episodes (a file held in part or whole by another is the later comparison's: left as it is), and only with Pause off,
// the version's library writing, and its title and version still there and monitored (a deliberate removal stays
// removed). In place it is the sweep's revive: active again, its items filled, Plex scanned, the row settled revived,
// and the release's router:playing miss lifted. In a renamed folder the row is settled and the miss lifted, and the
// next sweep repoints the link as it would any renamed one.
func (l *Library) reviveReturned(ctx context.Context, r returned, renamed bool) error {
	if !l.PlayingWritable(r.version) {
		return nil
	}
	db := l.st.Reader()
	var kept bool
	err := db.QueryRowContext(ctx, `SELECT t.monitored = 1 AND v.monitored = 1
		AND NOT EXISTS (SELECT 1 FROM arrview_removed_version rv WHERE rv.version_id = v.id)
		FROM version v JOIN title t ON t.id = v.title_id WHERE v.title_id = ? AND v.policy_key = ?`, r.titleID,
		r.version).Scan(&kept)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !kept {
		return nil
	}
	if err != nil {
		return err
	}
	if held, err := partlyCovered(ctx, db, r.linkID); err != nil || held {
		return err
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
		if !renamed {
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
		}
		if _, err := tx.ExecContext(lctx, `UPDATE playing_break SET settled = 'revived', updated_at = ? WHERE id = ?`,
			now, r.id); err != nil {
			return err
		}
		if r.hash == "" {
			return nil
		}
		_, err := tx.ExecContext(lctx, `DELETE FROM provider_cache WHERE info_hash = ? AND source = ?`, r.hash,
			router.PlayingMissSource)
		return err
	})
	if errors.Is(err, ErrLinkChanged) {
		return nil
	}
	if err != nil {
		return err
	}
	l.log.Info("library: the copy that broke while playing reads again; revived", "link", r.path, "incident_row", r.id,
		"renamed", renamed)
	if !renamed {
		if err := l.EnqueueScan(ctx, r.version, filepath.Dir(r.path)); err != nil {
			l.log.Warn("library: plex scan not queued", "path", r.path, "err", err)
		}
	}
	for _, v := range revived {
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
