package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// A file that broke while someone was playing it (docs/design/now-playing.md "A file that stops working while it
// plays"). Now playing's check proves by its own reads through the mount that the file does not read; the library then
// keeps the incident (table playing_break: the old copy as it was, what the repair came to) and, when the same release
// cannot be had from another account, marks the file dead at once and queues one search for it (reason
// router.ReasonPlaying): the hourly sweep's dead mark without its two misses 30 minutes apart and without zurg's
// repair grace. Nothing is deleted from any account.

// PlayingBound is how long one fast repair per title and library version holds: a second incident in that time
// takes the ordinary hourly path.
const PlayingBound = 24 * time.Hour

// ErrPlayingStopped: the library may not act on a broken file now (Pause on, or the version's library writes
// nothing). Nothing was marked.
var ErrPlayingStopped = errors.New("library: not acting on a broken file now")

// PlayingIncident is the watched file's incident as now playing found it.
type PlayingIncident struct {
	TitleID int64
	Version string
	// Viewer is the Plex user name and Player the device of the playback that hit the break.
	Viewer, Player string
	// OutAccount is the provider found out (another release on it fails too, or zurg says so); "" when only this
	// release is broken.
	OutAccount string
}

// PlayingFile is a file of an incident: an active link, the target its check read (the copy judged), and where it
// read (the come-back reads the same range).
type PlayingFile struct {
	LinkID int64
	Target string
	Offset int64
}

// PlayingWritable reports whether the library may act on a broken file of version now: Pause off and the version's
// library writing.
func (l *Library) PlayingWritable(version string) bool {
	return l.pol.RouterSettings().Enabled && l.gate.Active(version)
}

// PlayingBoundReached reports whether an incident of the title's version began within PlayingBound.
func (l *Library) PlayingBoundReached(ctx context.Context, titleID int64, version string) (bool, error) {
	var n int
	err := l.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM playing_break WHERE title_id = ? AND version_key = ?
		AND broken_at > ?`, titleID, version, store.Millis(l.now().Add(-PlayingBound))).Scan(&n)
	return n > 0, err
}

// OpenPlayingIncident records the watched file's row (the incident's id is its id): from here History and Now
// playing have it. With f.Target set, only while the link still points at that copy (ErrLinkChanged otherwise). The row stays open (not settled) until the repair says otherwise.
func (l *Library) OpenPlayingIncident(ctx context.Context, inc PlayingIncident, f PlayingFile) (int64, error) {
	var id int64
	err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = l.insertBreak(ctx, tx, 0, inc, f, false)
		return err
	})
	return id, err
}

// insertBreak writes one row of incident (0: a new incident, whose id is the row's) from the link as it is now.
func (l *Library) insertBreak(ctx context.Context, tx *sql.Tx, incident int64, inc PlayingIncident, f PlayingFile,
	searched bool) (int64, error) {
	var path, target string
	err := tx.QueryRowContext(ctx, `SELECT path, target FROM link WHERE id = ?`, f.LinkID).Scan(&path, &target)
	if err != nil {
		return 0, fmt.Errorf("library: playing break: link %d: %w", f.LinkID, err)
	}
	if f.Target != "" && target != f.Target {
		return 0, fmt.Errorf("library: playing break: link %d: %w", f.LinkID,
			linkChanged(f.LinkID, path, f.Target, "it points at "+target+" now"))
	}
	hash, err := l.playingHash(ctx, tx, f.LinkID, target)
	if err != nil {
		return 0, err
	}
	inner := ""
	if read, ok := l.mount.FromLink(target); ok {
		if rel, err := filepath.Rel(l.mount.AllPath(), read); err == nil && !escapes(rel) {
			_, inner, _ = strings.Cut(rel, string(filepath.Separator))
		}
	}
	eps := "[]"
	if rf := linkFacts(ctx, tx, f.LinkID, ""); len(rf.Episodes) > 0 {
		b, _ := json.Marshal(rf.Episodes)
		eps = string(b)
	}
	now := store.Millis(l.now())
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO playing_break (incident, watched, link_id, title_id, version_key, path,
		old_target, info_hash, inner_file, read_offset, content_file_id, download_id, release_title, quality, score,
		file_metadata, adopted, adopted_from, adopted_source, adopted_media, folder_hash, episodes, broken_at,
		out_account, searched, viewer, player, updated_at)
		SELECT ?, ?, l.id, ?, ?, l.path, l.target, ?, ?, ?, l.content_file_id, l.download_id, l.release_title, l.quality,
			l.score, l.file_metadata, l.adopted, l.adopted_from, l.adopted_source, l.adopted_media, l.folder_hash, ?, ?,
			?, ?, ?, ?, ? FROM link l WHERE l.id = ? RETURNING id`, incident, incident == 0, inc.TitleID, inc.Version,
		hash, inner, f.Offset, eps, now, inc.OutAccount, searched, inc.Viewer, inc.Player, now, f.LinkID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("library: playing break: %w", err)
	}
	if incident == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE playing_break SET incident = id WHERE id = ?`, id); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// playingHash is the hash a link has stored (router.LinkStoredHashSQL, an adopted link's source included), else the
// one release the registry records in the link's mount folder (as relink and now playing find an adopted link's).
// A hash only guessed from a folder's name does not count: "".
func (l *Library) playingHash(ctx context.Context, tx dbQuerier, linkID int64, target string) (string, error) {
	var hash string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(`+router.LinkStoredHashSQL+`, '') FROM link l WHERE l.id = ?`,
		linkID).Scan(&hash); err != nil {
		return "", err
	}
	if hash != "" {
		return strings.ToLower(hash), nil
	}
	read, ok := l.mount.FromLink(target)
	if !ok {
		return "", nil
	}
	rel, err := filepath.Rel(l.mount.AllPath(), read)
	if err != nil || escapes(rel) {
		return "", nil
	}
	folder, _, _ := strings.Cut(rel, string(filepath.Separator))
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT lower(info_hash) FROM content WHERE mount_folder = ?`, folder)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return "", err
		}
		found = append(found, h)
	}
	if len(found) != 1 {
		return "", rows.Err()
	}
	return found[0], rows.Err()
}

// dbQuerier is a *sql.DB or *sql.Tx.
type dbQuerier interface {
	rowQuerier
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// PlayingHash is playingHash for a link, outside a write: the hash now playing copies and finds siblings by.
func (l *Library) PlayingHash(ctx context.Context, linkID int64) (string, error) {
	db := l.st.Reader()
	var target string
	if err := db.QueryRowContext(ctx, `SELECT target FROM link WHERE id = ?`, linkID).Scan(&target); err != nil {
		return "", err
	}
	return l.playingHash(ctx, db, linkID, target)
}

// SettlePlayingIncident settles every open row of an incident whose link never went dead: settled copy (step 1 made
// it readable again; fixed_by copy, and account the one zurg logged serving it since, "" when unknown) or none
// (nothing was done). A row an
// import fixed meanwhile (fixed_by replaced) is left as it is.
func (l *Library) SettlePlayingIncident(ctx context.Context, incident int64, settled, account string) error {
	fixed := ""
	if settled == "copy" {
		fixed = "copy"
	}
	_, err := l.st.Writer().ExecContext(ctx, `UPDATE playing_break SET settled = ?, fixed_by = ?, serving = ?,
		updated_at = ? WHERE incident = ? AND settled = '' AND fixed_by <> 'replaced'`, settled, fixed, account,
		store.Millis(l.now()), incident)
	return err
}

// DropPlayingIncident removes the rows of an incident cut short before anything was done (still open, not fixed, no
// search queued), so the day's bound and Now playing's list hold only real incidents. A row an import fixed, or one
// marked and searched, stays.
func (l *Library) DropPlayingIncident(ctx context.Context, incident int64) error {
	_, err := l.st.Writer().ExecContext(ctx, `DELETE FROM playing_break WHERE incident = ? AND settled = ''
		AND fixed_by = '' AND searched = 0`, incident)
	return err
}

// PlayingMiss writes the miss of the incident's release on each of keys (a provider key, or the client's
// "client:"+key), source router:playing, for router.PlayingMissTTL: no search takes that release there again
// meanwhile (it may elsewhere). The come-back lifts it.
func (l *Library) PlayingMiss(ctx context.Context, incident int64, keys []string) error {
	return l.st.WriteTx(ctx, func(tx *sql.Tx) error { return l.playingMissTx(ctx, tx, incident, keys) })
}

func (l *Library) playingMissTx(ctx context.Context, tx *sql.Tx, incident int64, keys []string) error {
	var hash string
	if err := tx.QueryRowContext(ctx, `SELECT info_hash FROM playing_break WHERE id = ?`, incident).
		Scan(&hash); err != nil {
		return err
	}
	if len(hash) != 40 {
		return nil
	}
	for _, k := range keys {
		if err := router.RecordPlayingMiss(ctx, tx, hash, k, l.now()); err != nil {
			return err
		}
	}
	return nil
}

// MarkBrokenPlaying marks one file of an incident dead at once and wants its film or episodes again, as the sweep's
// dead mark does but with no 30-minute rule and no repair grace (markDead): under its link lease, and only while the
// link still points at f.Target, the copy the check read (a same-path import or a sweep may have changed it during the
// check). In that one transaction: the file's row of the incident (the watched one exists already, OpenPlayingIncident;
// another file gets its own), the release's miss on missKeys (PlayingMiss), the dead mark, the history line and, with
// search, one search per version (the incident's version: reason playing; another version sharing the link:
// dead_link); without search, QueuePlayingSearch queues it later (a release's other files, once the watched file's
// search has ended). It reports whether it marked the file; a copy that changed is left, with nothing written. Pause
// on or a version that does not write: ErrPlayingStopped, nothing marked.
func (l *Library) MarkBrokenPlaying(ctx context.Context, incident int64, f PlayingFile, search bool,
	missKeys []string) (bool, error) {
	var inc PlayingIncident
	err := l.st.Reader().QueryRowContext(ctx, `SELECT title_id, version_key, viewer, player, out_account
		FROM playing_break WHERE id = ?`, incident).Scan(&inc.TitleID, &inc.Version, &inc.Viewer, &inc.Player,
		&inc.OutAccount)
	if err != nil {
		return false, fmt.Errorf("library: playing incident %d: %w", incident, err)
	}
	if !l.PlayingWritable(inc.Version) {
		return false, ErrPlayingStopped
	}
	var path, target, state string
	fi := inc // a file of another title or version (a pack shared across them) has its own row there
	if err := l.st.Reader().QueryRowContext(ctx, `SELECT l.path, l.target, l.state, l.title_id,
		COALESCE((SELECT v.policy_key FROM link_version lv JOIN version v ON v.id = lv.version_id
			WHERE lv.link_id = l.id ORDER BY v.policy_key = ? DESC, v.id LIMIT 1), '')
		FROM link l WHERE l.id = ?`, inc.Version, f.LinkID).Scan(&path, &target, &state, &fi.TitleID,
		&fi.Version); err != nil {
		return false, nil
	}
	if state != "active" || target != f.Target || !l.PlayingWritable(fi.Version) {
		return false, nil
	}
	lctx, unlock, err := l.lockLinks(ctx, path)
	defer unlock()
	if err != nil {
		l.log.Info("library: broken file left: another run is changing it", "link", path, "err", err)
		return false, nil
	}
	now := store.Millis(l.now())
	marked := false
	err = l.st.WriteTx(lctx, func(tx *sql.Tx) error {
		marked = false
		if err := l.linksHeldTx(lctx, tx); err != nil {
			return err
		}
		var cur string
		if err := tx.QueryRowContext(lctx, `SELECT target FROM link WHERE id = ? AND path = ? AND state = 'active'`,
			f.LinkID, path).Scan(&cur); errors.Is(err, sql.ErrNoRows) || err == nil && cur != f.Target {
			return nil // the copy the check read is no longer the link's
		} else if err != nil {
			return err
		}
		var row int64
		err := tx.QueryRowContext(lctx, `SELECT id FROM playing_break WHERE incident = ? AND link_id = ?
			AND old_target = ? AND settled = ''`, incident, f.LinkID, f.Target).Scan(&row)
		if errors.Is(err, sql.ErrNoRows) {
			if row, err = l.insertBreak(lctx, tx, incident, fi, f, search); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if err := l.playingMissTx(lctx, tx, incident, missKeys); err != nil {
			return err
		}
		dead, err := l.markDead(lctx, tx, f.LinkID, path, f.Target, now)
		if err != nil || !dead {
			return err
		}
		if _, err := tx.ExecContext(lctx, `UPDATE playing_break SET searched = ?, updated_at = ? WHERE id = ?`,
			search, now, row); err != nil {
			return err
		}
		if search {
			if err := l.queueLinkSearch(lctx, tx, f.LinkID, fi.Version); err != nil {
				return err
			}
		} else if err := l.queueOtherVersions(lctx, tx, f.LinkID, fi.Version); err != nil {
			return err
		}
		marked = true
		return l.record(lctx, tx, action{Kind: "dead", Mode: "applied", Version: fi.Version, LinkID: f.LinkID,
			Path: path, Target: f.Target, Detail: "stopped working while playing: a read through the mount failed",
			Rel: linkFacts(lctx, tx, f.LinkID, path)})
	})
	if errors.Is(err, ErrLinkChanged) {
		l.log.Info("library: broken file left: its link changed", "link", path, "err", err)
		return false, nil
	}
	if err != nil || !marked {
		return false, err
	}
	l.log.Info("library: file marked dead while playing", "link", f.LinkID, "version", fi.Version, "title", fi.TitleID,
		"incident", incident)
	return true, nil
}

// markDead turns an active link dead by compare and swap on its path and target and wants its film or episodes
// again (reconcileRemovedLink). It reports whether it did.
func (l *Library) markDead(ctx context.Context, tx *sql.Tx, id int64, path, target string, now int64) (bool, error) {
	res, err := tx.ExecContext(ctx, `UPDATE link SET state = 'dead', missing_since = NULL, last_checked_at = ?,
		updated_at = ? WHERE id = ? AND path = ? AND target = ? AND state = 'active'`, now, now, id, path, target)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	return true, reconcileRemovedLink(ctx, tx, id, now)
}

// queueLinkSearch queues the searches for a link marked dead while playing: reason playing for the incident's
// version, dead_link for any other version sharing the link (its own sweep would have).
func (l *Library) queueLinkSearch(ctx context.Context, tx *sql.Tx, linkID int64, version string) error {
	return l.queueSearches(ctx, tx, linkID, func(key string) string {
		if key == version {
			return router.ReasonPlaying
		}
		return router.ReasonDeadLink
	})
}

// queueOtherVersions queues only the dead_link searches of the other versions sharing a link (QueuePlayingSearch
// queues the incident version's later).
func (l *Library) queueOtherVersions(ctx context.Context, tx *sql.Tx, linkID int64, version string) error {
	return l.queueSearches(ctx, tx, linkID, func(key string) string {
		if key == version {
			return ""
		}
		return router.ReasonDeadLink
	})
}

// QueuePlayingSearch queues, for each title and version of an incident, one playing search for the union of the
// episodes of its marked files whose search was not queued yet (a release's other files), and notes them searched.
// It returns how many searches it queued.
func (l *Library) QueuePlayingSearch(ctx context.Context, incident int64) (int, error) {
	n := 0
	err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		n = 0
		// Every row waiting is noted searched; only those whose link is still the dead one get a search.
		if _, err := tx.ExecContext(ctx, `UPDATE playing_break SET searched = 1, updated_at = ? WHERE incident = ?
			AND settled = '' AND searched = 0 AND NOT EXISTS (SELECT 1 FROM link l WHERE l.id = playing_break.link_id
				AND l.path = playing_break.path AND l.target = playing_break.old_target AND l.state = 'dead')`,
			store.Millis(l.now()), incident); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT pb.id, pb.title_id, pb.version_key, pb.episodes FROM playing_break pb
			JOIN link l ON l.id = pb.link_id AND l.path = pb.path AND l.target = pb.old_target AND l.state = 'dead'
			WHERE pb.incident = ? AND pb.settled = '' AND pb.searched = 0 ORDER BY pb.id`, incident)
		if err != nil {
			return err
		}
		type group struct {
			title   int64
			version string
			eps     []int64
			ids     []int64
		}
		var groups []*group
		for rows.Next() {
			var id, title int64
			var version, js string
			if err := rows.Scan(&id, &title, &version, &js); err != nil {
				rows.Close()
				return err
			}
			var g *group
			for _, x := range groups {
				if x.title == title && x.version == version {
					g = x
				}
			}
			if g == nil {
				g = &group{title: title, version: version}
				groups = append(groups, g)
			}
			var eps []int64
			_ = json.Unmarshal([]byte(js), &eps)
			g.eps = append(g.eps, eps...)
			g.ids = append(g.ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, g := range groups {
			for _, id := range g.ids {
				if _, err := tx.ExecContext(ctx, `UPDATE playing_break SET searched = 1, updated_at = ? WHERE id = ?`,
					store.Millis(l.now()), id); err != nil {
					return err
				}
			}
			if !l.gate.Active(g.version) {
				continue
			}
			var versionID int64
			var kind string
			if err := tx.QueryRowContext(ctx, `SELECT v.id, t.kind FROM version v JOIN title t ON t.id = v.title_id
				WHERE v.title_id = ? AND v.policy_key = ?`, g.title, g.version).Scan(&versionID, &kind); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				return err
			}
			if err := l.enqueueResearch(ctx, tx, g.title, versionID, g.version, kind, g.eps,
				router.ReasonPlaying); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// enqueueResearch queues one acquire.research job of reason for a title's version (its episodes for a series; a
// series with none gets none), keyed so one job per version, title and episodes is pending.
func (l *Library) enqueueResearch(ctx context.Context, tx *sql.Tx, titleID, versionID int64, version, kind string,
	eps []int64, reason string) error {
	if l.queue == nil {
		return nil
	}
	p := router.ResearchRequest{TitleID: titleID, VersionID: versionID, Version: version, Reason: reason}
	key := fmt.Sprintf("%s/%s/%s/%d/", router.JobResearch, reason, version, titleID)
	if kind == string(policy.KindSeries) {
		if len(eps) == 0 {
			return nil
		}
		p.Episodes = eps
		for i, ep := range eps {
			if i > 0 {
				key += ","
			}
			key += strconv.FormatInt(ep, 10)
		}
	}
	if _, _, err := l.queue.EnqueueTx(ctx, tx, router.JobResearch, p, jobs.EnqueueOptions{DedupeKey: key,
		MaxAttempts: 1}); err != nil {
		return err
	}
	l.log.Info("library: search queued for a "+map[string]string{router.ReasonPlaying: "file that broke while playing",
		router.ReasonDeadLink: "dead link"}[reason], "version", version, "title", titleID, "episodes", len(p.Episodes))
	return nil
}

// ForgetRelease clears rclone's cached listing of a link target's release folder (rclone rc vfs/forget), when the
// mount has that configured (library.mount.rclone_rc.forget_release_root): after another account took the release,
// zurg may serve it under the same folder. Nothing without it.
func (l *Library) ForgetRelease(ctx context.Context, target string) error {
	if l.forget == nil {
		return nil
	}
	read, ok := l.mount.FromLink(target)
	if !ok {
		return nil
	}
	rel, err := filepath.Rel(l.mount.AllPath(), read)
	if err != nil || escapes(rel) {
		return nil
	}
	folder, _, _ := strings.Cut(rel, string(filepath.Separator))
	return l.forget.call(ctx, filepath.Join(l.set.AllDir, folder))
}

// PlayingRoute is a version's route as the repair of a broken file needs it: the download client's provider_cache key
// ("client:"+key) for a version that adds through a client, else "" and its providers in fallback order.
func (l *Library) PlayingRoute(version string) (client string, providers []string) {
	vp := l.pol.Versions[version]
	if vp == nil {
		return "", nil
	}
	if vp.ViaClient() {
		return router.ClientCacheKey(vp.Client), nil
	}
	return "", slices.Clone(vp.Providers)
}

// PlayingMessage keeps on an incident what the viewer was told, or why Plex refused (Now playing shows it).
func (l *Library) PlayingMessage(ctx context.Context, incident int64, note string) error {
	_, err := l.st.Writer().ExecContext(ctx, `UPDATE playing_break SET message = ?, updated_at = ? WHERE id = ?`, note,
		store.Millis(l.now()), incident)
	return err
}

// PlayingNotStopped keeps on an incident why its player was not stopped (note), only while nothing is kept there: the
// first reason stays, and a stop with a message or Plex's refusal is never replaced.
func (l *Library) PlayingNotStopped(ctx context.Context, incident int64, note string) error {
	_, err := l.st.Writer().ExecContext(ctx, `UPDATE playing_break SET message = ?, updated_at = ?
		WHERE id = ? AND message = ''`, note, store.Millis(l.now()), incident)
	return err
}

// PlayingSearchable reports whether the search a broken file would get could take anything: at least one of its
// items is one the round searches in version (the version's library in the settings and writing; its title and
// version monitored and not removed from the arr view; for a series file, one of its episodes monitored in that
// version, aired (the round searches aired episodes only) and not dropped). Now playing asks it before an incident
// opens and for each other file of the release; a file it refuses is left to the hourly sweep (no mark, no message,
// nothing counted).
func (l *Library) PlayingSearchable(ctx context.Context, linkID int64, version string) (bool, error) {
	if l.pol.Versions[version] == nil || !l.gate.Active(version) {
		return false, nil
	}
	var ok bool
	err := l.st.Reader().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM link k
		JOIN version v ON v.title_id = k.title_id AND v.policy_key = ? JOIN title t ON t.id = v.title_id
		WHERE k.id = ? AND t.monitored = 1 AND v.monitored = 1
		AND NOT EXISTS (SELECT 1 FROM arrview_removed_version rv WHERE rv.version_id = v.id)
		AND (t.kind <> 'series' OR EXISTS (SELECT 1 FROM link_episode le JOIN episode e ON e.id = le.episode_id
			LEFT JOIN episode_version_state s ON s.episode_id = e.id AND s.version_id = v.id
			WHERE le.link_id = k.id AND COALESCE(s.monitored, e.monitored) = 1 AND e.removed_at IS NULL
			AND e.air_date_utc_at IS NOT NULL AND e.air_date_utc_at <= ?)))`,
		version, linkID, store.Millis(l.now())).Scan(&ok)
	return ok, err
}

// DropUnfinishedPlaying removes, when now playing starts, the incidents a stop cut off before anything was done:
// open rows never marked, searched or fixed whose link is still the active copy the check read. Their check starts
// again at the next sign, and the day's bound does not count them. It returns how many rows it removed.
func (l *Library) DropUnfinishedPlaying(ctx context.Context) (int, error) {
	res, err := l.st.Writer().ExecContext(ctx, `DELETE FROM playing_break WHERE settled = '' AND fixed_by = ''
		AND searched = 0 AND EXISTS (SELECT 1 FROM link l WHERE l.id = playing_break.link_id
			AND l.path = playing_break.path AND l.target = playing_break.old_target AND l.state = 'active')`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
