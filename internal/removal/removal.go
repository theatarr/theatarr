// Package removal is the one way copies of a title leave the library (docs/design/arrview.md "Remove"): the web UI's
// Remove title / Remove from this library (internal/web apiRemove) and an arr client's DELETE movie|series
// (internal/arrview deleteTitle) both run Run. In order:
//
//  1. the clean-up candidates are collected while the copies still relate to them: the torrent hashes of their links
//     (active or dead) and their downloads not failed;
//  2. one write transaction marks the copies removed (catalog.Remove, plus the caller's own writes), ends their
//     downloads in hand (router.EndRemoved), clears their search requests and records one Removed action per library
//     (journal);
//  3. with the files to be deleted: each of their links moves to the trash (Unlinker.UnlinkRemoved, through the
//     library's gate; a dead symlink too; the folders it leaves empty go, strictly below the library root), except a
//     link another current library of the title still uses, which stays;
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
	// Unlink deletes the files: the links move to the trash and the torrents are cleaned up, unless KeepTorrents
	// (the dialog's "Delete it from the debrid providers" cleared): the torrents then stay for Torrents › Not used by Theatarr.
	Unlink, KeepTorrents bool
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

// Cleanup is the debrid clean-up of one removal: its title, its candidate hashes each with the removed libraries
// (policy keys) it belongs to, and the Removed action of each library, whose detail gets that library's own outcome
// (RecordCleanup).
type Cleanup struct {
	TitleID int64
	Hashes  map[string][]string
	Actions map[string]int64
	// Kept says, per library, which of its torrents the removal kept because their files stay; it is added to that
	// library's outcome (JoinWords).
	Kept map[string]string
	By   string
}

// JoinWords joins outcome parts with "; ", leaving out empty ones.
func JoinWords(parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(p string) bool { return p == "" }), "; ")
}

// HashesOf are the candidate hashes of library key.
func (c Cleanup) HashesOf(key string) []string {
	var out []string
	for h, ks := range c.Hashes {
		if slices.Contains(ks, key) {
			out = append(out, h)
		}
	}
	slices.Sort(out)
	return out
}

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
		// The files' fate: kept now, or with Unlink unknown until the links are done (below). A library that had no
		// file records no file outcome.
		for _, k := range out.Removed {
			rm := journal.Removal{}
			if !s.Unlink {
				var files bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM link l JOIN link_version lv
					ON lv.link_id = l.id JOIN version v ON v.id = lv.version_id WHERE v.title_id = ?1 AND v.policy_key = ?2
					AND l.state IN ('active', 'dead'))`, s.TitleID, k).Scan(&files); err != nil {
					return err
				}
				if files {
					rm.Files = journal.RemovalFilesKept
				}
			}
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
			// A quick re-add must not inherit an old Search now.
			if _, err := tx.ExecContext(ctx, `DELETE FROM search_request WHERE title_id = ? AND version_key IN (`+
				store.Placeholders(len(s.Keys))+`)`, append([]any{s.TitleID}, anyOf(s.Keys)...)...); err != nil {
				return err
			}
		}
		return nil
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
		switch {
		case err == nil:
			actIDs[k] = id
		case errors.Is(err, sql.ErrNoRows):
			// Removed before removals were recorded (an older arr-compatible delete): this one is recorded now.
			err = d.Store.WriteTx(ctx, func(tx *sql.Tx) error {
				id, err = journal.RecordAction(ctx, tx, journal.Action{Action: s.Action, TitleID: s.TitleID, Version: k,
					Target: s.Name, Detail: journal.RemovalDetail(journal.Removal{}), By: s.By, At: d.Now()})
				return err
			})
			if err == nil {
				actIDs[k] = id
			} else {
				d.Log.Warn("removal: recording the removal", "title", s.TitleID, "version", k, "err", err)
			}
		default:
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
			left := len(f.left)
			// A library that had no file says nothing about files ("" reads as no outcome).
			rm := journal.Removal{}
			switch {
			case left > 0 && f.unlinked == 0:
				rm.Files, rm.Left, rm.Reason = journal.RemovalFilesKept, left, f.reason
			case left > 0:
				rm.Files, rm.Left, rm.Reason = journal.RemovalFilesPartial, left, f.reason
			case f.unlinked > 0:
				rm.Files = journal.RemovalFilesDeleted
			}
			if err := journal.SetActionDetail(ctx, tx, id, journal.RemovalDetail(rm)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		d.Log.Warn("removal: recording the files' outcome", "title", s.TitleID, "err", err)
	}
	// The clean-up, library by library: each Removed action gets its own torrents' outcome.
	clean := &Cleanup{TitleID: s.TitleID, Hashes: map[string][]string{}, Actions: map[string]int64{},
		Kept: map[string]string{}, By: s.By}
	for _, h := range cands.hashes() {
		if dropped[h] == "" {
			clean.Hashes[h] = cands.keysOf(h)
		}
	}
	for k, id := range actIDs {
		var mine, kept []string
		for _, h := range cands.hashes() {
			if slices.Contains(cands.keysOf(h), k) {
				mine = append(mine, h)
				if dropped[h] != "" {
					kept = append(kept, dropped[h])
				}
			}
		}
		// What its kept files keep, said with the clean-up's outcome.
		keptWords := ""
		if len(kept) > 0 {
			keptWords = "torrents of the files that stay kept"
		}
		switch {
		case len(mine) == 0:
			// No torrent on record: nothing to clean up, nothing to say.
		case len(kept) == len(mine):
			RecordCleanup(ctx, d.Store, d.Log, id, keptWords)
		case s.KeepTorrents:
			RecordCleanup(ctx, d.Store, d.Log, id, "debrid torrents kept, as asked (Torrents › Not used by Theatarr lists them)")
		case d.StartCleanup == nil:
			RecordCleanup(ctx, d.Store, d.Log, id, JoinWords("torrents not cleaned up: the torrent manager is not running",
				keptWords))
		default:
			clean.Actions[k] = id
			if keptWords != "" {
				clean.Kept[k] = keptWords
			}
		}
	}
	if len(clean.Actions) > 0 {
		out.Cleanup = clean
		d.StartCleanup(*clean)
	}
	return out, nil
}

// droppedLink is why a hash is no clean-up candidate: a link of the removed copies that stays names it.
const droppedLink = "link"

// sharedSQL is the condition that link l is still used by a current library (a version not removed) through its
// link_version rows.
const sharedSQL = `EXISTS (SELECT 1 FROM link_version lv2 WHERE lv2.link_id = l.id
		AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = lv2.version_id))`

// fate is what became of one removed library's files: how many links were unlinked, and the distinct paths left in
// place with the first reason.
type fate struct {
	unlinked int
	left     map[string]bool
	reason   string
}

func (f fate) leave(path, why string) fate {
	if f.left == nil {
		f.left = map[string]bool{}
	}
	f.left[path] = true
	if f.reason == "" {
		f.reason = why
	}
	return f
}

type link struct {
	id                       int64
	path, kind, hash, target string
	keys                     []string
	shared                   bool
}

// unlink takes the removed copies' links out (step 3): it fills out, and returns each library's fate and the hashes
// a link that stays still names (no clean-up candidates).
func unlink(ctx context.Context, d Deps, s Spec, keys []string, c *cands, out *Result) (map[string]fate,
	map[string]string, error) {
	fates := map[string]fate{}
	dropped := map[string]string{}
	var links []link
	err := scan(ctx, d.Store.Reader(), `SELECT l.id, l.path, l.kind, COALESCE(`+router.LinkStoredHashSQL+`, ''), l.target,
		group_concat(DISTINCT v.policy_key),
		`+sharedSQL+`
		FROM link l JOIN link_version lv ON lv.link_id = l.id JOIN version v ON v.id = lv.version_id
		WHERE v.title_id = ? AND v.policy_key IN (`+store.Placeholders(len(keys))+`) AND l.state IN ('active', 'dead')
		GROUP BY l.id ORDER BY l.path`, append([]any{s.TitleID}, anyOf(keys)...), func(r *sql.Rows) error {
		var x link
		var ks string
		if err := r.Scan(&x.id, &x.path, &x.kind, &x.hash, &x.target, &ks, &x.shared); err != nil {
			return err
		}
		x.keys = strings.Split(ks, ",")
		// Hashes are compared normalised (hex, as the candidates are); one that cannot be read counts as none: the
		// link is untied.
		x.hash = indexer.NormalizeHash(x.hash)
		links = append(links, x)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("removal: reading the links: %w", err)
	}
	// A link that stays and cannot be tied to its torrent by a hash keeps every candidate of its libraries; one that
	// stays keeps the hashes of every link sharing its target.
	var untied, stayTargets []string
	count := func(x link, ok bool, why string) {
		for _, k := range x.keys {
			if ok {
				f := fates[k]
				f.unlinked++
				fates[k] = f
			} else {
				fates[k] = fates[k].leave(x.path, why)
			}
		}
		if !ok && x.hash != "" {
			dropped[x.hash] = droppedLink
		}
		if !ok && x.hash == "" {
			untied = append(untied, x.keys...)
		}
		if !ok && x.target != "" {
			stayTargets = append(stayTargets, x.target)
		}
	}
	for _, x := range links {
		// The shared check again, right before the unlink: a library restored (an Add) since the links were read
		// uses the link now.
		if !x.shared && x.kind == "symlink" {
			err := d.Store.Reader().QueryRowContext(ctx, `SELECT `+sharedSQL+` FROM link l WHERE l.id = ?`,
				x.id).Scan(&x.shared)
			if err != nil {
				out.Failed = append(out.Failed, Left{Path: x.path, Reason: err.Error()})
				count(x, false, err.Error())
				continue
			}
		}
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
	// A kept link's hash stays wanted.
	for _, x := range links {
		if x.hash != "" && slices.Contains(stayTargets, x.target) {
			dropped[x.hash] = droppedLink
		}
	}
	for _, h := range c.hashes() {
		if slices.ContainsFunc(c.keysOf(h), func(k string) bool { return slices.Contains(untied, k) }) {
			dropped[h] = droppedLink
		}
	}
	for h := range dropped {
		if !c.has(h) {
			delete(dropped, h)
		}
	}
	return fates, dropped, nil
}

// cands are a removal's clean-up candidates: hashes in the order found, each with the removed libraries (policy keys)
// it belongs to.
type cands struct {
	list []string
	keys map[string][]string
}

func (c *cands) add(h, key string) {
	if h = indexer.NormalizeHash(h); h == "" {
		return
	}
	if c.keys == nil {
		c.keys = map[string][]string{}
	}
	if !slices.Contains(c.list, h) {
		c.list = append(c.list, h)
	}
	if !slices.Contains(c.keys[h], key) {
		c.keys[h] = append(c.keys[h], key)
	}
}

func (c *cands) has(h string) bool { return slices.Contains(c.list, h) }

func (c *cands) hashes() []string { return c.list }

func (c *cands) keysOf(h string) []string { return c.keys[h] }

// candidates are the hashes of keys' copies of the title (step 1): their links not unlinked and their downloads that did not fail (a failed grab attempt may name a torrent that was never ours to delete).
func candidates(ctx context.Context, db *sql.DB, titleID int64, keys []string) (*cands, error) {
	c := &cands{}
	if len(keys) == 0 {
		return c, nil
	}
	args := append([]any{titleID}, anyOf(keys)...)
	in := `v.title_id = ? AND v.policy_key IN (` + store.Placeholders(len(keys)) + `)`
	for _, q := range []string{
		`SELECT ` + router.LinkStoredHashSQL + `, v.policy_key FROM link l JOIN link_version lv ON lv.link_id = l.id
			JOIN version v ON v.id = lv.version_id WHERE ` + in + ` AND l.state IN ('active', 'dead')`,
		`SELECT d.info_hash, v.policy_key FROM download d JOIN version v ON v.id = d.version_id WHERE ` + in +
			` AND d.state <> 'failed'`,
	} {
		err := scan(ctx, db, q, args, func(r *sql.Rows) error {
			var h sql.NullString
			var k string
			if err := r.Scan(&h, &k); err != nil {
				return err
			}
			c.add(h.String, k)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("removal: clean-up candidates: %w", err)
		}
	}
	return c, nil
}

// RecordCleanup writes a library's clean-up outcome into its Removed action's detail (journal.Removal.Cleanup),
// keeping the files' outcome there. A failure is logged: the removal itself is done.
func RecordCleanup(ctx context.Context, st *store.Store, log *slog.Logger, action int64, words string) {
	err := st.WriteTx(ctx, func(tx *sql.Tx) error {
		var detail string
		if err := tx.QueryRowContext(ctx, `SELECT detail FROM journal_action WHERE id = ?`, action).Scan(
			&detail); err != nil {
			return err
		}
		rm := journal.ParseRemovalDetail(detail)
		rm.Cleanup = words
		return journal.SetActionDetail(ctx, tx, action, journal.RemovalDetail(rm))
	})
	if err != nil && log != nil {
		log.Warn("removal: recording the clean-up", "action", action, "err", err)
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
