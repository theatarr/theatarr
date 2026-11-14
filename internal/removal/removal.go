// Package removal is the one way copies of a title leave the library (docs/design/arrview.md "Remove"): the web UI's
// Remove title / Remove from this library (internal/web apiRemove) and an arr client's DELETE movie|series
// (internal/arrview deleteTitle) both run Run. In order:
//
//  1. the clean-up candidates are collected while the copies still relate to them: the torrent hashes of their links
//     (active or dead), their arr files and their downloads not failed;
//  2. one write transaction marks the copies removed (catalog.Remove, plus the caller's own writes), ends their
//     downloads in hand (router.EndRemoved), clears their search requests and pending offers, stops the queued copies
//     to other providers that nothing else wants (router.StopRemovedCopies) and records one Removed action per
//     library (journal);
//  3. with the files to be deleted: each of their links moves to the trash (Unlinker.UnlinkRemoved, through the
//     library's gate; a dead symlink too; the folders it leaves empty go, strictly below the library root), except a
//     link another current library of the title still uses, which stays; the arr file rows at an unlinked path go
//     with it, so the removed title is not counted as its torrent's user;
//  4. each Removed action gets what became of its files, and the hashes no kept or failed link names are handed to
//     the debrid clean-up (Cleanup, run after the answer by the caller's starter: torrents.DeleteForRemoval).
//
// Without the files, links and torrents stay; the actions say so.
package removal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// Unlinker moves one library link of a removed copy to the trash (arrview.Service.UnlinkRemoved): an active or dead
// symlink, through the library's gate, removing the folders it leaves empty below the library root. A refusal (the
// gate, not a symlink) is an error with Refused() true: the link stays, and nothing changed.
type Unlinker interface {
	UnlinkRemoved(ctx context.Context, linkID int64, reason string) (trash string, err error)
}

// Deps are what Run works with.
type Deps struct {
	Store    *store.Store
	Unlinker Unlinker
	// AfterRemove runs right after the transaction when copies were removed (the title's requests follow:
	// web.TitleRemoved); nil does nothing.
	AfterRemove func(ctx context.Context, titleID int64)
	// StartCleanup hands the debrid clean-up to the service, which runs it after the answer on a context of its own
	// (web.StartCleanup); nil records that it did not run.
	StartCleanup func(Cleanup)
	Now          func() time.Time
	Log          *slog.Logger
}

// Spec is one removal.
type Spec struct {
	TitleID int64
	// Name is the title's name (the actions' target).
	Name string
	// Keys are the copies (policy keys) to remove; Leftover are copies removed before whose library links are taken
	// out too (with Unlink: an earlier removal that kept its files, or whose moves failed).
	Keys, Leftover []string
	// View is who removes, for arrview_removed_version ("ui", or the arr view's key).
	View string
	// Unlink deletes the files: the links move to the trash and the torrents are cleaned up.
	Unlink bool
	// Action is journal.ActRemoveTitle or ActRemoveVersion; By who removed; Reason the words on ended downloads and
	// moved links.
	Action, By, Reason string
	// Extra runs in the transaction after catalog.Remove (the arr view's own rows); nil for none. OwnViewRecords:
	// Extra handles the title's arr view records (catalog.Hide instead of catalog.Remove).
	Extra          func(ctx context.Context, tx *sql.Tx) error
	OwnViewRecords bool
}

// Left is a link a removal left in place, and why.
type Left struct {
	Path   string
	Reason string
}

// Unlinked is a link a removal moved to the trash.
type Unlinked struct {
	Path, Trash string
	Versions    []string
}

// Result is what Run did.
type Result struct {
	Removed        []string
	DownloadsEnded int
	Unlinked       []Unlinked
	// Kept are links the gate refused (they stay), Shared links another current library of the title still uses
	// (they stay for it), Failed links whose move failed otherwise (they stay; removing again with the files retries).
	Kept, Shared, Failed []Left
	// Cleanup is the debrid clean-up handed to StartCleanup; nil when none runs.
	Cleanup *Cleanup
}

// Cleanup is the debrid clean-up of one removal: its candidate hashes, and the Removed actions whose detail gets the
// outcome (RecordCleanup).
type Cleanup struct {
	TitleID int64
	Hashes  []string
	Actions []int64
	By      string
}

// SkippedRemoved is the message of a queued copy to another provider that a removal stopped.
const SkippedRemoved = "Skipped: its title was removed from the library"

// Run removes spec's copies (see the package comment for the order). An error before the transaction commits
// changes nothing; after it, a link that fails is reported in the result, not as an error.
func Run(ctx context.Context, d Deps, s Spec) (*Result, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	all := append(slices.Clone(s.Keys), s.Leftover...)
	cands, err := candidates(ctx, d.Store.Reader(), s.TitleID, all)
	if err != nil {
		return nil, err
	}
	out := &Result{Removed: []string{}}
	now := d.Now()
	ms := store.Millis(now)
	actIDs := map[string]int64{}
	err = d.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		if len(s.Keys) > 0 {
			remove := catalog.Remove
			if s.OwnViewRecords {
				remove = catalog.Hide
			}
			removed, err := remove(ctx, tx, s.TitleID, s.Keys, s.View, now)
			if err != nil {
				return err
			}
			if removed != nil {
				out.Removed = removed
			}
		}
		if s.Extra != nil {
			if err := s.Extra(ctx, tx); err != nil {
				return err
			}
		}
		// The files' fate: kept now, or with Unlink unknown until the links are done (below).
		rm := journal.Removal{Files: journal.RemovalFilesKept}
		if s.Unlink {
			rm.Files = ""
		}
		for _, k := range out.Removed {
			id, err := journal.RecordAction(ctx, tx, journal.Action{Action: s.Action, TitleID: s.TitleID, Version: k,
				Target: s.Name, Detail: journal.RemovalDetail(rm), By: s.By, At: now})
			if err != nil {
				return err
			}
			actIDs[k] = id
		}
		// Downloads in hand end with the hide: an import running now cannot commit a link after it (it takes its
		// links back), so the links read below are every link the copies have.
		if out.DownloadsEnded, err = router.EndRemoved(ctx, tx, s.TitleID, s.Keys, s.Reason, ms); err != nil {
			return err
		}
		if len(s.Keys) > 0 {
			// A quick re-add must not inherit an old Search now or a pending offer.
			if _, err := tx.ExecContext(ctx, `DELETE FROM search_request WHERE title_id = ? AND version_key IN (`+
				store.Placeholders(len(s.Keys))+`)`, append([]any{s.TitleID}, anyOf(s.Keys)...)...); err != nil {
				return err
			}
			if _, err := router.EndRemovedOffers(ctx, tx, s.TitleID, s.Keys, ms); err != nil {
				return err
			}
		}
		_, err = router.StopRemovedCopies(ctx, tx, cands.hashes(), SkippedRemoved, ms)
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(out.Removed) > 0 && d.AfterRemove != nil {
		d.AfterRemove(ctx, s.TitleID)
	}
	if !s.Unlink {
		return out, nil
	}
	// Leftover copies' latest Removed actions get the new outcome too.
	for _, k := range s.Leftover {
		if _, ok := actIDs[k]; ok {
			continue
		}
		var id int64
		err := d.Store.Reader().QueryRowContext(ctx, `SELECT id FROM journal_action WHERE title_id = ? AND version_key = ?
			AND action IN (?, ?) ORDER BY id DESC LIMIT 1`, s.TitleID, k, journal.ActRemoveTitle,
			journal.ActRemoveVersion).Scan(&id)
		if err == nil {
			actIDs[k] = id
		} else if !errors.Is(err, sql.ErrNoRows) {
			d.Log.Warn("removal: reading an earlier removal", "title", s.TitleID, "version", k, "err", err)
		}
	}
	fates, dropped, err := unlink(ctx, d, s, all, cands, out)
	if err != nil {
		return out, err
	}
	if err := d.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		for k, id := range actIDs {
			f := fates[k]
			rm := journal.Removal{Files: journal.RemovalFilesDeleted}
			if f.left > 0 {
				rm.Files, rm.Left, rm.Reason = journal.RemovalFilesPartial, f.left, f.reason
				if f.unlinked == 0 {
					rm.Files = journal.RemovalFilesKept
				}
			}
			if err := journal.SetActionDetail(ctx, tx, id, journal.RemovalDetail(rm)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		d.Log.Warn("removal: recording the files' outcome", "title", s.TitleID, "err", err)
	}
	ids := make([]int64, 0, len(actIDs))
	for _, id := range actIDs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var hashes []string
	for _, h := range cands.hashes() {
		if !dropped[h] {
			hashes = append(hashes, h)
		}
	}
	switch {
	case len(hashes) == 0 && len(dropped) == 0:
		// No torrent on record: nothing to clean up, nothing to say.
	case len(hashes) == 0:
		RecordCleanup(ctx, d.Store, d.Log, ids, "torrents kept: their files stay in the library")
	case d.StartCleanup == nil:
		RecordCleanup(ctx, d.Store, d.Log, ids, "torrents not cleaned up: the torrent manager is not running")
	default:
		out.Cleanup = &Cleanup{TitleID: s.TitleID, Hashes: hashes, Actions: ids, By: s.By}
		d.StartCleanup(*out.Cleanup)
	}
	return out, nil
}

// fate is what became of one removed library's links.
type fate struct {
	unlinked, left int
	reason         string
}

type link struct {
	id               int64
	path, kind, hash string
	keys             []string
	shared           bool
}

// unlink takes the removed copies' links out (step 3): it fills out, and returns each library's fate and the hashes
// a link that stays still names (no clean-up candidates).
func unlink(ctx context.Context, d Deps, s Spec, keys []string, c *cands, out *Result) (map[string]fate, map[string]bool,
	error) {
	fates := map[string]fate{}
	dropped := map[string]bool{}
	var links []link
	err := scan(ctx, d.Store.Reader(), `SELECT l.id, l.path, l.kind, COALESCE(`+router.LinkStoredHashSQL+`, ''),
		group_concat(DISTINCT v.policy_key),
		EXISTS (SELECT 1 FROM link_version lv2 WHERE lv2.link_id = l.id
			AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = lv2.version_id))
		FROM link l JOIN link_version lv ON lv.link_id = l.id JOIN version v ON v.id = lv.version_id
		WHERE v.title_id = ? AND v.policy_key IN (`+store.Placeholders(len(keys))+`) AND l.state IN ('active', 'dead')
		GROUP BY l.id ORDER BY l.path`, append([]any{s.TitleID}, anyOf(keys)...), func(r *sql.Rows) error {
		var x link
		var ks string
		if err := r.Scan(&x.id, &x.path, &x.kind, &x.hash, &ks, &x.shared); err != nil {
			return err
		}
		x.keys = strings.Split(ks, ",")
		links = append(links, x)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("removal: reading the links: %w", err)
	}
	count := func(x link, ok bool, why string) {
		for _, k := range x.keys {
			f := fates[k]
			if ok {
				f.unlinked++
			} else if f.left++; f.reason == "" {
				f.reason = why
			}
			fates[k] = f
		}
		if !ok && x.hash != "" {
			dropped[x.hash] = true
		}
	}
	for _, x := range links {
		if x.shared {
			why := "another library still uses it"
			out.Shared = append(out.Shared, Left{Path: x.path, Reason: why})
			count(x, false, why)
			continue
		}
		if x.kind != "symlink" {
			why := "not one of Theatarr's symlinks"
			out.Kept = append(out.Kept, Left{Path: x.path, Reason: why})
			count(x, false, why)
			continue
		}
		trash, err := d.Unlinker.UnlinkRemoved(ctx, x.id, s.Reason)
		var ref interface{ Refused() bool }
		switch {
		case errors.As(err, &ref) && ref.Refused():
			out.Kept = append(out.Kept, Left{Path: x.path, Reason: err.Error()})
			count(x, false, err.Error())
			continue
		case err != nil:
			d.Log.Warn("removal: moving a library link to the trash failed", "title", s.TitleID, "link", x.id,
				"path", x.path, "err", err)
			out.Failed = append(out.Failed, Left{Path: x.path, Reason: err.Error()})
			count(x, false, err.Error())
			continue
		}
		count(x, true, "")
		out.Unlinked = append(out.Unlinked, Unlinked{Path: x.path, Trash: trash, Versions: x.keys})
		err = d.Store.WriteTx(ctx, func(tx *sql.Tx) error {
			// The removed copies' arr files at that path go with the link: their torrent is not theirs to keep.
			if _, err := tx.ExecContext(ctx, `DELETE FROM version_file WHERE path = ? AND version_id IN
				(SELECT v.id FROM version v JOIN arrview_removed_version r ON r.version_id = v.id WHERE v.title_id = ?
				AND v.policy_key IN (`+store.Placeholders(len(x.keys))+`))`,
				append([]any{x.path, s.TitleID}, anyOf(x.keys)...)...); err != nil {
				return err
			}
			trashed := trash
			if trashed == "" {
				trashed = "—"
			}
			_, err := journal.RecordAction(ctx, tx, journal.Action{Action: journal.ActUnlinkFile, TitleID: s.TitleID,
				Version: strings.Join(x.keys, ","), Target: x.path, By: s.By, At: d.Now(),
				Detail: "moved to " + trashed + " when its library was removed"})
			return err
		})
		if err != nil {
			d.Log.Warn("removal: recording an unlinked link", "title", s.TitleID, "path", x.path, "err", err)
		}
	}
	// A kept link's hash stays wanted; so does a hash an arr file row of a removed copy still holds (its path was
	// not one of the links unlinked).
	err = scan(ctx, d.Store.Reader(), `SELECT DISTINCT `+fileHashSQL+` FROM version_file vf
		JOIN version v ON v.id = vf.version_id WHERE v.title_id = ? AND v.policy_key IN (`+
		store.Placeholders(len(keys))+`)`, append([]any{s.TitleID}, anyOf(keys)...), func(r *sql.Rows) error {
		var h string
		if err := r.Scan(&h); err != nil {
			return err
		}
		if h = indexer.NormalizeHash(h); h != "" {
			dropped[h] = true
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("removal: reading the arr files: %w", err)
	}
	for h := range dropped {
		if !c.has(h) {
			delete(dropped, h)
		}
	}
	return fates, dropped, nil
}

// fileHashSQL is an arr file vf's torrent hash: its content's, else its download id when that is a v1 hash.
const fileHashSQL = `COALESCE((SELECT c.info_hash FROM content c WHERE c.id = vf.content_id),
	CASE WHEN length(vf.download_id) = 40 THEN vf.download_id END, '')`

// cands are a removal's clean-up candidates: hashes in the order found.
type cands struct{ list []string }

func (c *cands) add(h string) {
	if h = indexer.NormalizeHash(h); h != "" && !slices.Contains(c.list, h) {
		c.list = append(c.list, h)
	}
}

func (c *cands) has(h string) bool { return slices.Contains(c.list, h) }

func (c *cands) hashes() []string { return c.list }

// candidates are the hashes of keys' copies of the title (step 1): their links not unlinked, their arr files, and
// their downloads that did not fail (a failed grab attempt may name a torrent that was never ours to delete).
func candidates(ctx context.Context, db *sql.DB, titleID int64, keys []string) (*cands, error) {
	c := &cands{}
	if len(keys) == 0 {
		return c, nil
	}
	args := append([]any{titleID}, anyOf(keys)...)
	in := `v.title_id = ? AND v.policy_key IN (` + store.Placeholders(len(keys)) + `)`
	for _, q := range []string{
		`SELECT ` + router.LinkStoredHashSQL + ` FROM link l JOIN link_version lv ON lv.link_id = l.id
			JOIN version v ON v.id = lv.version_id WHERE ` + in + ` AND l.state IN ('active', 'dead')`,
		`SELECT ` + fileHashSQL + ` FROM version_file vf JOIN version v ON v.id = vf.version_id WHERE ` + in,
		`SELECT d.info_hash FROM download d JOIN version v ON v.id = d.version_id WHERE ` + in +
			` AND d.state <> 'failed'`,
	} {
		err := scan(ctx, db, q, args, func(r *sql.Rows) error {
			var h sql.NullString
			if err := r.Scan(&h); err != nil {
				return err
			}
			c.add(h.String)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("removal: clean-up candidates: %w", err)
		}
	}
	return c, nil
}

// RecordCleanup writes a clean-up's outcome into each Removed action's detail (journal.Removal.Cleanup), keeping the
// files' outcome there. A failure is logged: the removal itself is done.
func RecordCleanup(ctx context.Context, st *store.Store, log *slog.Logger, actions []int64, words string) {
	err := st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, id := range actions {
			var detail string
			if err := tx.QueryRowContext(ctx, `SELECT detail FROM journal_action WHERE id = ?`, id).Scan(
				&detail); err != nil {
				return err
			}
			rm := journal.ParseRemovalDetail(detail)
			rm.Cleanup = words
			if err := journal.SetActionDetail(ctx, tx, id, journal.RemovalDetail(rm)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil && log != nil {
		log.Warn("removal: recording the clean-up", "actions", actions, "err", err)
	}
}

func scan(ctx context.Context, db *sql.DB, q string, args []any, row func(*sql.Rows) error) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := row(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func anyOf(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
