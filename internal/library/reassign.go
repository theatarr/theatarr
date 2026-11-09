package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// Reassigned is what ReassignEpisodes did.
type Reassigned struct {
	LinkID       int64
	TitleID      int64
	Version      string
	PreviousPath string
	Path         string
	Previous     []string // the episodes the file held, as S01E02
	Episodes     []string // the episodes it holds now
	Replaced     []string // links of other files moved to the trash for them (replace)
}

// reassignConflict is another active link of the version holding one of the chosen episodes.
type reassignConflict struct {
	id           int64
	path, target string
	owners       int
	episodes     []string // the chosen episodes it holds
	downloadID   sql.NullInt64
	rel          string // its file in the download (download_file.rel)
	trash        string // its trash copy, once moved aside
}

// ReassignEpisodes corrects the episodes one of Theatarr's series links holds (an admin's "Change episodes"), for its one
// library: the link is renamed to the version's naming for the chosen episodes through RenameLink's gated
// compare-and-swap move, and in the move's own transaction its episodes, the episode states and the download's
// recorded file identity (download_file, so a retry or re-import of the download maps the file the same way) change
// with it. A failed move changes nothing. A chosen episode that has another active link of the version is refused
// unless replace: that link is then unlinked as Unlink does (moved to the trash; the mount is never touched), in the
// move's transaction, and the chosen episodes leave its file's recorded identity; when the move fails, it is put
// back from the trash.
// An arr file of the version holding a chosen episode, a link shared by several versions and an adopted link
// (no download to record the choice on) are refused.
func (l *Library) ReassignEpisodes(ctx context.Context, linkID int64, episodeIDs []int64, replace bool) (*Reassigned, error) {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.reassignEpisodes(pinned, linkID, episodeIDs, replace)
	}
	return l.reassignEpisodes(ctx, linkID, episodeIDs, replace)
}

func (l *Library) reassignEpisodes(ctx context.Context, linkID int64, episodeIDs []int64, replace bool) (*Reassigned, error) {
	refuse := func(format string, args ...any) error {
		return &Refused{Reason: "reassign", Detail: fmt.Sprintf(format, args...)}
	}
	db := l.st.Reader()
	var kind, state, raw, rel string
	var downloadID sql.NullInt64
	var owners int
	v := importVersion{}
	t := &v.Title
	out := &Reassigned{LinkID: linkID}
	err := db.QueryRowContext(ctx, `SELECT l.path, l.target, l.kind, l.state, l.download_id, COALESCE(cf.path, ''),
		l.file_metadata, (SELECT COUNT(*) FROM link_version x WHERE x.link_id = l.id), t.id, t.kind, t.name,
		COALESCE(t.year, 0), COALESCE(t.imdb_id, ''), COALESCE(t.tmdb_id, 0), COALESCE(t.tvdb_id, 0),
		COALESCE(t.series_type, '') FROM link l JOIN title t ON t.id = l.title_id
		LEFT JOIN content_file cf ON cf.id = l.content_file_id WHERE l.id = ?`, linkID).
		Scan(&out.PreviousPath, new(string), &kind, &state, &downloadID, &rel, &raw, &owners, &t.ID, &t.Kind, &t.Name,
			&t.Year, &t.ImdbID, &t.TmdbID, &t.TvdbID, &t.SeriesType)
	if err != nil {
		return nil, err
	}
	out.TitleID = t.ID
	switch {
	case t.Kind != "series":
		return out, refuse("only a series file holds episodes")
	case kind != "symlink" || state != "active":
		return out, refuse("only an active library symlink can change episodes; this one is a %s, %s", kind, state)
	case owners != 1:
		return out, refuse("the link is shared by %d libraries; changing it would change them all", owners)
	case !downloadID.Valid || rel == "":
		return out, refuse("the link was adopted from an arr and has no download to keep the choice on; use manual import")
	}
	if err := db.QueryRowContext(ctx, `SELECT v.id, v.policy_key FROM link_version lv JOIN version v ON v.id = lv.version_id
		WHERE lv.link_id = ?`, linkID).Scan(&v.VersionID, &v.Key); err != nil {
		return nil, err
	}
	out.Version = v.Key
	if err := l.gate.CheckPath(v.Key, out.PreviousPath); err != nil {
		return out, err
	}

	// The chosen episodes: the title's, each once, in order.
	ids := slices.Compact(slices.Sorted(slices.Values(episodeIDs)))
	if len(ids) == 0 {
		return out, refuse("choose at least one episode")
	}
	eps, err := loadEpisodes(ctx, db, t.ID, ids)
	if err != nil {
		return nil, err
	}
	if len(eps) != len(ids) {
		return out, refuse("an episode chosen is not one of this series'")
	}
	old, err := linkEpisodes(ctx, db, linkID)
	if err != nil {
		return nil, err
	}
	oldIDs := make([]int64, 0, len(old))
	for _, e := range old {
		oldIDs = append(oldIDs, e.ID)
		out.Previous = append(out.Previous, episodeLabel(e))
	}
	slices.Sort(oldIDs)
	for _, e := range eps {
		out.Episodes = append(out.Episodes, episodeLabel(e))
	}
	if slices.Equal(oldIDs, ids) {
		return out, refuse("the file holds %s already", strings.Join(out.Episodes, ", "))
	}

	// Files the version holds for the chosen episodes: an arr file always refuses, another link unless replace.
	label := map[int64]string{}
	for _, e := range eps {
		label[e.ID] = episodeLabel(e)
	}
	args := []any{v.VersionID}
	for _, id := range ids {
		args = append(args, id)
	}
	in := store.Placeholders(len(ids))
	var arrFiles []string
	if err := scanRows(ctx, db, `SELECT DISTINCT vf.path FROM version_file vf JOIN version_file_episode fe
		ON fe.version_file_id = vf.id WHERE vf.version_id = ? AND fe.episode_id IN (`+in+`) ORDER BY vf.path`, args,
		func(r *sql.Rows) error {
			var p string
			err := r.Scan(&p)
			arrFiles = append(arrFiles, p)
			return err
		}); err != nil {
		return nil, err
	}
	if len(arrFiles) > 0 {
		return out, refuse("the arr's file %s holds a chosen episode; delete it from the arr first", strings.Join(arrFiles, ", "))
	}
	var conflicts []*reassignConflict
	byID := map[int64]*reassignConflict{}
	if err := scanRows(ctx, db, `SELECT l.id, l.path, l.target, (SELECT COUNT(*) FROM link_version x WHERE x.link_id = l.id),
		l.download_id, COALESCE(cf.path, ''), le.episode_id FROM link l JOIN link_version lv ON lv.link_id = l.id
		JOIN link_episode le ON le.link_id = l.id LEFT JOIN content_file cf ON cf.id = l.content_file_id
		WHERE lv.version_id = ? AND l.state = 'active' AND l.id <> ? AND le.episode_id IN (`+in+`) ORDER BY l.id, le.episode_id`,
		append([]any{v.VersionID, linkID}, args[1:]...), func(r *sql.Rows) error {
			c := &reassignConflict{}
			var ep int64
			if err := r.Scan(&c.id, &c.path, &c.target, &c.owners, &c.downloadID, &c.rel, &ep); err != nil {
				return err
			}
			if byID[c.id] == nil {
				byID[c.id] = c
				conflicts = append(conflicts, c)
			}
			byID[c.id].episodes = append(byID[c.id].episodes, label[ep])
			return nil
		}); err != nil {
		return nil, err
	}
	for _, c := range conflicts {
		what := fmt.Sprintf("%s already has a file in this library (%s)", strings.Join(c.episodes, ", "), c.path)
		switch {
		case !replace:
			return out, refuse("%s; choose replace to unlink it", what)
		case c.owners != 1:
			return out, refuse("%s, shared by %d libraries; it is not replaced from here", what, c.owners)
		}
		if err := l.CheckUnlink(v.Key, c.path); err != nil {
			return out, err
		}
	}

	// The new name, as a rename names the link (rename_preview.go), for the chosen episodes.
	facts, err := metadataFacts(raw)
	if err != nil {
		return out, refuse("the link's saved file details cannot name it: %v", err)
	}
	p, err := probeResult(ctx, db, linkID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	vn, err := NamingFor(ctx, db, l.pol, v.Key)
	if err != nil {
		return nil, err
	}
	root := ""
	for _, r := range l.gate.Roots(v.Key) {
		if x, e := filepath.Rel(r, out.PreviousPath); e == nil && x != "." && !escapes(x) {
			root = r
			break
		}
	}
	if root == "" {
		return out, refuse("%s is not under the library's roots", out.PreviousPath)
	}
	formats := make([]string, 0, len(facts.Formats))
	for _, f := range facts.Formats {
		formats = append(formats, f.Name)
	}
	newPath, err := l.nameSeries(ctx, &v, vn, root, facts.RelativePath, factsToFileFacts(facts), p, formats, eps)
	if err != nil {
		return out, refuse("naming: %v", err)
	}
	out.Path = filepath.Clean(newPath)
	if err := l.gate.CheckPath(v.Key, out.Path); err != nil {
		return out, err
	}
	// Nothing is unlinked for a move that cannot be made: the destination must be free, or a link being replaced.
	// An unlinked or dead row left at the name makes way in the move's transaction, as for an import (takePathTx).
	replaced := func(id int64, path string) bool {
		return slices.ContainsFunc(conflicts, func(c *reassignConflict) bool { return c.id == id || filepath.Clean(c.path) == path })
	}
	if out.Path != filepath.Clean(out.PreviousPath) {
		if _, err := os.Lstat(out.Path); err == nil && !replaced(0, out.Path) {
			return out, refuse("the new name already exists and is not a link being replaced: %s", out.Path)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return out, err
		}
		r, err := rowAtPath(ctx, db, out.Path, v.VersionID)
		if err != nil {
			return nil, err
		}
		if r != nil && r.state == "active" && !replaced(r.id, "") {
			return out, refuse("the new name is another link's (%d): %s", r.id, out.Path)
		}
	}

	// The file's verdict against the cutoff does not depend on its episodes: the chosen ones take the one its
	// episodes have now.
	fileState := "filled"
	if len(oldIDs) > 0 {
		oargs := []any{v.VersionID}
		for _, id := range oldIDs {
			oargs = append(oargs, id)
		}
		var s sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT MAX(state) FROM episode_version_state WHERE version_id = ?
			AND state IN ('filled', 'cutoff_met') AND episode_id IN (`+store.Placeholders(len(oldIDs))+`)`, oargs...).
			Scan(&s); err != nil {
			return nil, err
		}
		if s.Valid {
			fileState = s.String // filled sorts last: any filled episode keeps the file filled
		}
	}

	// The replaced links leave the library before the move, under leases on every path involved (linklock.go) that
	// the move's own lease check covers; their rows change in the move's transaction. When the move fails, they are
	// put back.
	paths := []string{out.PreviousPath, out.Path}
	for _, c := range conflicts {
		paths = append(paths, c.path)
	}
	ctx, unlock, err := l.lockLinks(ctx, paths...)
	defer unlock()
	if err != nil {
		return out, err
	}
	var moved []*reassignConflict
	for _, c := range conflicts {
		if !linkPointsAt(c.path, c.target) {
			err = linkChanged(c.id, c.path, c.target, "the symlink no longer points there")
			break
		}
		if c.trash, _, err = l.w.copyToTrash(v.Key, l.unlinkRoot(v.Key, c.path), c.path); err != nil {
			break
		}
		if err = l.w.removeLink(v.Key, c.path); err != nil {
			_ = l.w.purgeTrash(v.Key, c.trash)
			break
		}
		moved = append(moved, c)
	}
	if err == nil {
		err = l.reassignMove(ctx, &v, linkID, downloadID.Int64, rel, ids, oldIDs, fileState, conflicts, out)
	}
	if err != nil {
		if len(moved) > 0 {
			l.afterFailedReplace(ctx, v.Key, moved, err)
		}
		return out, err
	}
	for _, c := range conflicts {
		out.Replaced = append(out.Replaced, c.path)
		if err := l.EnqueueScan(ctx, v.Key, filepath.Dir(c.path)); err != nil {
			l.log.Warn("library: plex scan not queued", "dir", filepath.Dir(c.path), "err", err)
		}
	}
	l.log.Info("library: episodes changed by hand", "link", linkID, "version", v.Key, "from", episodeList(out.Previous),
		"to", strings.Join(out.Episodes, ","), "path", out.Path, "replaced", len(out.Replaced))
	return out, nil
}

// reassignMove renames the link to path for the chosen episodes (renameLink), and in the move's transaction marks
// the replaced links (moved aside already) unlinked, drops an unlinked or dead row left at the path, takes the chosen
// episodes out of the replaced files' recorded identities and writes the link's episodes, the episode states and
// its recorded choice.
func (l *Library) reassignMove(ctx context.Context, v *importVersion, linkID, downloadID int64, rel string, ids,
	oldIDs []int64, fileState string, conflicts []*reassignConflict, out *Reassigned) error {
	dropped := slices.DeleteFunc(slices.Clone(oldIDs), func(id int64) bool { return slices.Contains(ids, id) })
	choice, _ := json.Marshal(ids)
	detail := fmt.Sprintf("episodes changed by hand: %s -> %s", episodeList(out.Previous), strings.Join(out.Episodes, ", "))
	return l.renameLink(ctx, linkID, out.Path, func(tx *sql.Tx) error {
		now := store.Millis(l.now())
		for _, c := range conflicts {
			lost, err := l.markUnlinkedTx(ctx, tx, v.Key, c.id, c.path, c.trash, c.target, fmt.Sprintf(
				"replaced: link %d now holds %s", linkID, strings.Join(c.episodes, ", ")))
			if err != nil {
				return err
			}
			if lost {
				return linkChanged(c.id, c.path, c.target, "its row changed during the replace")
			}
			// A by-hand choice wins: the replaced file no longer claims the chosen episodes when its download is
			// imported again (resolve.go). A recorded choice left with none goes; a client's manual row stays, as a
			// chosen file that holds none.
			if !c.downloadID.Valid || c.rel == "" {
				continue
			}
			// A client's manual row with no episodes (its name decided them) first records the ones the file held,
			// as a choice by hand: those it keeps stay its own on a re-import.
			if _, err := tx.ExecContext(ctx, `UPDATE download_file SET episodes = (SELECT json_group_array(episode_id)
				FROM (SELECT episode_id FROM link_episode WHERE link_id = ? ORDER BY episode_id)), how = ?
				WHERE download_id = ? AND rel = ? AND episodes = '[]' AND source = 'manual'`,
				c.id, HowManual, c.downloadID.Int64, c.rel); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE download_file SET episodes = (SELECT json_group_array(value)
				FROM json_each(download_file.episodes) WHERE value NOT IN (SELECT value FROM json_each(?))), updated_at = ?
				WHERE download_id = ? AND rel = ?`, string(choice), now, c.downloadID.Int64, c.rel); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM download_file WHERE download_id = ? AND rel = ? AND episodes = '[]'
				AND source = 'import'`,
				c.downloadID.Int64, c.rel); err != nil {
				return err
			}
			// A client's manual row left with none says so (how replaced): it holds none of the episodes, unlike a
			// chosen file with no episodes, which its name decides.
			if _, err := tx.ExecContext(ctx, `UPDATE download_file SET how = ? WHERE download_id = ? AND rel = ?
				AND episodes = '[]' AND source = 'manual'`, HowReplaced, c.downloadID.Int64, c.rel); err != nil {
				return err
			}
		}
		// An unlinked or dead row at the new path (a replaced link's: files of one pack are named alike; or one an
		// earlier unlink left) makes way for this one, as an import's takes the path (takePathTx).
		if _, err := tx.ExecContext(ctx, `DELETE FROM link WHERE path = ? AND id <> ? AND state <> 'active'`,
			out.Path, linkID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM link_episode WHERE link_id = ?`, linkID); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `INSERT INTO link_episode (link_id, episode_id) VALUES (?, ?)`, linkID, id); err != nil {
				return err
			}
		}
		if err := recordImportedState(ctx, tx, v, ids, fileState, false, now); err != nil {
			return err
		}
		if err := episodesLostFile(ctx, tx, v.VersionID, dropped, now); err != nil {
			return err
		}
		// The choice the resolver honours (resolve.go): a client's manual row keeps its source (only manual files
		// import from such a download); otherwise it is the recorded identity, which does not narrow the files.
		if _, err := tx.ExecContext(ctx, `INSERT INTO download_file (download_id, rel, episodes, source, how, updated_at)
			VALUES (?, ?, ?, 'import', ?, ?) ON CONFLICT (download_id, rel) DO UPDATE SET episodes = excluded.episodes,
			how = CASE download_file.source WHEN 'manual' THEN download_file.how ELSE excluded.how END,
			updated_at = excluded.updated_at`, downloadID, rel, string(choice), HowManual, now); err != nil {
			return err
		}
		return l.record(ctx, tx, action{Kind: "link", Mode: "applied", Version: v.Key, LinkID: linkID, Path: out.Path,
			Detail: detail})
	})
}

// cleanupTimeout bounds what a failed change of episodes does after the request may have gone (a cancelled or
// timed-out request still needs its replaced links put back).
const cleanupTimeout = 30 * time.Second

// afterFailedReplace puts the replaced links of a failed change of episodes back when the move provably did not
// commit. Our transaction is the only one that unlinks (or, at the new path, removes) their rows while we hold the
// leases: one still active is a move that did not commit; one no longer active is a move that committed and failed
// after (the old symlink's removal), which keeps the replace. The check and the put-back do not follow the
// request's cancel. When the check itself fails, nothing is put back over a link that may have moved: the trash
// copies stay, and the failure is logged and recorded.
func (l *Library) afterFailedReplace(ctx context.Context, version string, moved []*reassignConflict, cause error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	active := 0
	err := l.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM link WHERE id = ? AND state = 'active'`,
		moved[0].id).Scan(&active)
	switch {
	case err != nil:
		for _, c := range moved {
			l.log.Error("library: change of episodes failed and its replaced link was not put back: whether the move "+
				"committed is unknown", "path", c.path, "target", c.target, "trash", c.trash, "cause", cause, "err", err)
			if rerr := l.record(ctx, nil, action{Kind: "replace", Mode: "failed", Version: version, LinkID: c.id,
				Path: c.path, Target: c.target, Detail: "change of episodes failed (" + cause.Error() +
					"): not put back, whether the move committed is unknown (" + err.Error() + "); its copy stays at " +
					c.trash}); rerr != nil {
				l.log.Warn("library: put back not recorded", "path", c.path, "err", rerr)
			}
		}
	case active > 0:
		l.putBack(ctx, version, moved, cause)
	}
}

// putBack links the replaced links of a failed change of episodes back from their trash copies (their rows and
// states never changed: the move's transaction did not commit) and removes the copies.
func (l *Library) putBack(ctx context.Context, version string, moved []*reassignConflict, cause error) {
	for _, c := range moved {
		a := action{Kind: "replace", Mode: "applied", Version: version, LinkID: c.id, Path: c.path, Target: c.target,
			Detail: "change of episodes failed (" + cause.Error() + "): put back from " + c.trash}
		if err := l.w.symlink(version, c.target, c.path); err != nil {
			a.Mode, a.Detail = modeOf(err), "change of episodes failed ("+cause.Error()+"): not put back, its copy stays at "+
				c.trash+": "+err.Error()
		} else if err := l.w.purgeTrash(version, c.trash); err != nil {
			l.log.Warn("library: trash copy not removed", "path", c.trash, "err", err)
		}
		if err := l.record(ctx, nil, a); err != nil {
			l.log.Warn("library: put back not recorded", "path", c.path, "err", err)
		}
	}
}

func episodeLabel(e episodeInfo) string { return fmt.Sprintf("S%02dE%02d", e.Season, e.Number) }

func episodeList(labels []string) string {
	if len(labels) == 0 {
		return "none"
	}
	return strings.Join(labels, ", ")
}

// scanRows runs q and calls each for every row.
func scanRows(ctx context.Context, db *sql.DB, q string, args []any, each func(*sql.Rows) error) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
