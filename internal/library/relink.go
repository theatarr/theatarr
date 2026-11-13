package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// LinkScan is what a walk of a version's root found.
type LinkScan struct {
	Version  string         `json:"version"`
	Root     string         `json:"root"`
	Dirs     int            `json:"dirs"`
	Links    int            `json:"links"`      // symlinks
	IntoMnt  int            `json:"into_mount"` // symlinks into the zurg mount
	Files    int            `json:"files"`      // regular files (not links)
	Dangling []DanglingLink `json:"dangling"`   // links into the mount whose target is missing
	Relinked []DanglingLink `json:"relinked"`   // repointed (or, in a dry run, would be)
	Causes   map[string]int `json:"causes"`     // dangling by cause
	DryRun   bool           `json:"dry_run"`    // nothing was written
	Refused  string         `json:"refused"`    // why the gate made it a dry run
	Names    *NameCheck     `json:"names,omitempty"`
	Mount    string         `json:"mount_error,omitempty"`
	// Revived are dead links whose file is on the mount again: active again (or, in a dry run, would be).
	Revived []DanglingLink `json:"revived,omitempty"`
	// Vanished are active links whose symlink is gone (ReconcileLinks); Skipped says why the root was not checked
	// for them.
	Vanished []VanishedLink `json:"vanished,omitempty"`
	Skipped  string         `json:"skipped,omitempty"`
	// ZurgRepair is zurg's repair state and ZurgError why its torrent listing could not be read, with
	// library.mount.zurg_api set (zurgapi.go).
	ZurgRepair *RepairState `json:"zurg_repair,omitempty"`
	ZurgError  string       `json:"zurg_error,omitempty"`
	// Retired are dead links whose file is back while another file holds their title or episodes: moved aside, not
	// revived (or, in a dry run, would be).
	Retired []DanglingLink `json:"retired,omitempty"`
	// Leftovers are symlinks Theatarr retired (a rename's old path, an upgrade's stray) that a crash left behind: moved
	// aside (or, in a dry run, would be; leftover.go).
	Leftovers []DanglingLink `json:"leftovers,omitempty"`
}

// DanglingLink is a link whose target is gone.
type DanglingLink struct {
	Path      string `json:"path"`
	Target    string `json:"target"`
	NewTarget string `json:"new_target,omitempty"`
	Hash      string `json:"hash,omitempty"`
	Cause     string `json:"cause"` // renamed, gone, missing, unknown, refused; no_video, repairing (zurgapi.go)
	// Reason explains a cause zurg's API gave.
	Reason string `json:"reason,omitempty"`
}

type mountLink struct {
	path, target, folder, inner string
}

// scanRoot walks a root (the library side) and lists its links into the mount, every read bounded through the root
// prober (rootread.go). A root, or a filesystem below it, that stops answering part way is ErrRootDown and sc is left
// as it was: a walk cut short says nothing about the links it did not reach.
func (l *Library) scanRoot(ctx context.Context, root string, sc *LinkScan) ([]mountLink, error) {
	var out []mountLink
	var dirs, links, intoMnt, files int
	err := walkRoot(ctx, root, func(e rootEntry) {
		switch {
		case e.typ.IsDir():
			dirs++
		case e.typ&fs.ModeSymlink != 0:
			links++
			if !e.ok {
				return
			}
			read, ok := l.mount.FromLink(e.target)
			if !ok {
				return
			}
			rel, err := filepath.Rel(l.mount.AllPath(), read)
			if err != nil || escapes(rel) {
				return
			}
			intoMnt++
			folder, inner, _ := strings.Cut(rel, string(filepath.Separator))
			out = append(out, mountLink{path: e.path, target: e.target, folder: folder, inner: inner})
		case e.typ.IsRegular():
			files++
		}
	})
	if err != nil {
		return nil, err
	}
	sc.Dirs += dirs
	sc.Links += links
	sc.IntoMnt += intoMnt
	sc.Files += files
	return out, nil
}

// mountIndex answers "does this release folder (and file) exist", one directory listing per folder.
type mountIndex struct {
	m       *Mount
	ctx     context.Context
	folders map[string]bool
	dirs    map[string]map[string]bool
	// videoless caches videoless's answers by folder.
	videoless map[string]bool
	// unreadable holds the directories whose listing failed while the mount answered (errFolderUnreadable).
	unreadable map[string]bool
}

// newMountIndex lists the release root once. A listing that is implausibly small (Mount.checkListing) is
// ErrMountDown: nothing may be read as missing from it.
func (l *Library) newMountIndex(ctx context.Context) (*mountIndex, error) {
	ents, err := l.mount.ReadDir(ctx, l.mount.AllPath())
	if err != nil {
		return nil, err
	}
	linked, err := l.linkedFolders(ctx)
	if err != nil {
		return nil, err
	}
	if err := l.mount.checkListing(len(ents), linked); err != nil {
		return nil, err
	}
	ix := &mountIndex{m: l.mount, ctx: ctx, folders: map[string]bool{}, dirs: map[string]map[string]bool{}}
	for _, e := range ents {
		ix.folders[e.Name()] = true
	}
	return ix, nil
}

// linkedFolders counts the distinct release folders under the release root that active links point into.
func (l *Library) linkedFolders(ctx context.Context) (int, error) {
	rows, err := l.st.Reader().QueryContext(ctx, `SELECT DISTINCT target FROM link WHERE state = 'active'`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	folders := map[string]bool{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return 0, err
		}
		read, ok := l.mount.FromLink(t)
		if !ok {
			continue
		}
		rel, err := filepath.Rel(l.mount.AllPath(), read)
		if err != nil || rel == "." || escapes(rel) {
			continue
		}
		folder, _, _ := strings.Cut(rel, string(filepath.Separator))
		folders[folder] = true
	}
	return len(folders), rows.Err()
}

// linkMoved says how a link changed since the walk read it ("" when it did not): its symlink no longer points at
// the target read, or its row carries another target.
func linkMoved(ctx context.Context, db *sql.DB, ml mountLink) (string, error) {
	if !linkPointsAt(ml.path, ml.target) {
		return "the symlink no longer points at " + ml.target, nil
	}
	var t string
	err := db.QueryRowContext(ctx, `SELECT target FROM link WHERE path = ?`, ml.path).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if t != ml.target {
		return "its row points at " + t, nil
	}
	return "", nil
}

// errFolderUnreadable is a release folder whose listing failed while the mount answered: no verdict on the links
// into it (neither found nor missing), and the sweep goes on with the others.
var errFolderUnreadable = errors.New("library: release folder unreadable")

func (ix *mountIndex) has(folder, inner string) (bool, error) {
	if !ix.folders[folder] {
		return false, nil
	}
	if inner == "" {
		return true, nil
	}
	dir := filepath.Join(ix.m.AllPath(), folder, filepath.Dir(inner))
	if ix.unreadable[dir] {
		return false, errFolderUnreadable
	}
	names, ok := ix.dirs[dir]
	if !ok {
		ents, err := ix.m.ReadDir(ix.ctx, dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			// One folder's listing failed (EIO reads as ErrMountDown): the sweep stops only when the mount itself
			// does not answer.
			if herr := ix.m.Healthy(ix.ctx); herr != nil {
				return false, herr
			}
			if ix.unreadable == nil {
				ix.unreadable = map[string]bool{}
			}
			ix.unreadable[dir] = true
			return false, fmt.Errorf("%w: %s: %v", errFolderUnreadable, dir, err)
		}
		names = map[string]bool{}
		for _, e := range ents {
			names[e.Name()] = true
		}
		ix.dirs[dir] = names
	}
	return names[filepath.Base(inner)], nil
}

// noVideo reports whether a release folder is listed but holds no video file: zurg still shows a torrent whose
// video the debrid service has lost, with only its .nfo/.txt left. A folder that is not listed, or a listing that
// fails, is no verdict (false).
func (ix *mountIndex) noVideo(folder string) bool {
	if ix == nil || !ix.folders[folder] {
		return false
	}
	if v, ok := ix.videoless[folder]; ok {
		return v
	}
	files, err := ix.m.VideoFiles(ix.ctx, filepath.Join(ix.m.AllPath(), folder))
	if err != nil {
		return false
	}
	if ix.videoless == nil {
		ix.videoless = map[string]bool{}
	}
	ix.videoless[folder] = len(files) == 0
	return len(files) == 0
}

var hash6Suffix = regexp.MustCompile(`^(.*) \{([0-9a-f]{6})\}$`)

// linkHash uses the link's recorded identity, never a guess from its current target's name.
func linkHash(ctx context.Context, db *sql.DB, path string) (string, error) {
	var hash string
	err := db.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(c.info_hash, ''), NULLIF(d.info_hash, ''),
		CASE WHEN json_valid(lk.file_metadata) THEN NULLIF(json_extract(lk.file_metadata, '$.InfoHash'), '') END,
		(SELECT MIN(cp_content.info_hash) FROM content_provider cp JOIN content cp_content ON cp_content.id = cp.content_id
		 WHERE cp.provider = d.provider AND cp.torrent_id = d.provider_torrent_id
		 HAVING COUNT(DISTINCT cp_content.info_hash) = 1), '')
		FROM link lk LEFT JOIN content_file cf ON cf.id = lk.content_file_id
		LEFT JOIN content c ON c.id = cf.content_id LEFT JOIN download d ON d.id = lk.download_id
		WHERE lk.path = ?`, path).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return strings.ToLower(hash), err
}

// candidates finds the release's info hash and the folder names it may have now.
func (l *Library) candidates(ctx context.Context, db *sql.DB, ml mountLink) (string, []string, error) {
	hash, err := linkHash(ctx, db, ml.path)
	if err != nil {
		return "", nil, err
	}
	base, prefix := ml.folder, ""
	if m := hash6Suffix.FindStringSubmatch(ml.folder); m != nil {
		base, prefix = m[1], m[2]
	}
	if hash == "" {
		rows, err := db.QueryContext(ctx, `
			SELECT DISTINCT c.info_hash FROM content c LEFT JOIN content_provider cp ON cp.content_id = c.id
			WHERE c.mount_folder = ? OR c.name IN (?, ?, ?) OR cp.folder_name IN (?, ?, ?)`,
			ml.folder, base, base+".mkv", base+".mp4", base, base+".mkv", base+".mp4")
		if err != nil {
			return "", nil, err
		}
		var hashes []string
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return "", nil, err
			}
			if prefix == "" || strings.HasPrefix(strings.ToLower(h), prefix) {
				hashes = append(hashes, strings.ToLower(h))
			}
		}
		rows.Close()
		if len(hashes) == 1 {
			hash = hashes[0]
		}
	}
	names := []string{base}
	if hash != "" {
		rows, err := db.QueryContext(ctx, `SELECT COALESCE(c.mount_folder, ''), c.name, COALESCE(cp.folder_name, '')
			FROM content c LEFT JOIN content_provider cp ON cp.content_id = c.id WHERE c.info_hash = ?`, hash)
		if err != nil {
			return "", nil, err
		}
		for rows.Next() {
			var mf, n, f string
			if err := rows.Scan(&mf, &n, &f); err != nil {
				rows.Close()
				return "", nil, err
			}
			names = append(names, mf, f, n)
		}
		rows.Close()
	}
	var out []string
	for _, n := range ReleaseFolderNames(hash, names...) {
		if n != ml.folder {
			out = append(out, n)
		}
	}
	return strings.ToLower(hash), out, nil
}

// identityQuery counts the registry's claims on folder name n: by recorded mount folder, and by any of mount folder,
// content name or provider folder name. Each arm is an index lookup (migration 00103), so a sweep over every healthy
// link does not scan the registry once per link.
const identityQuery = `SELECT MIN(CASE WHEN c.mount_folder = ? THEN LOWER(c.info_hash) END),
	COUNT(DISTINCT CASE WHEN c.mount_folder = ? THEN LOWER(c.info_hash) END),
	MIN(LOWER(c.info_hash)), COUNT(DISTINCT LOWER(c.info_hash)) FROM content c
	WHERE c.id IN (SELECT id FROM content WHERE mount_folder = ?
		UNION SELECT id FROM content WHERE name IN (?, ?, ?)
		UNION SELECT content_id FROM content_provider WHERE folder_name IN (?, ?, ?))`

// claimedByOther reports whether the registry records folder name n (by mount folder, content name or provider
// folder name, as identityQuery) for a torrent other than hash, whatever its state: a torrent marked absent may be
// back on a provider the last sync did not read.
func claimedByOther(ctx context.Context, db *sql.DB, hash, n string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM content c WHERE LOWER(c.info_hash) <> ?
		AND c.id IN (SELECT id FROM content WHERE mount_folder = ?
			UNION SELECT id FROM content WHERE name IN (?, ?, ?)
			UNION SELECT content_id FROM content_provider WHERE folder_name IN (?, ?, ?))`,
		strings.ToLower(hash), n, n, n+".mkv", n+".mp4", n, n+".mkv", n+".mp4").Scan(&count)
	return count > 0, err
}

// identityFolders prefers hash-suffixed folders over bare names. A replacement bare name needs an unambiguous registry
// match; its existence alone cannot identify a torrent. If a suffixed folder exists but lacks the file, do not
// fall back to the bare name: a different torrent may have taken it.
func identityFolders(ctx context.Context, db *sql.DB, ix *mountIndex, hash string, names []string) ([]string, error) {
	var suffixed []string
	for _, n := range names {
		if m := hash6Suffix.FindStringSubmatch(n); m != nil && strings.HasPrefix(hash, m[2]) && ix.folders[n] {
			suffixed = append(suffixed, n)
		}
	}
	if len(suffixed) > 0 {
		return suffixed, nil
	}
	var folder string
	err := db.QueryRowContext(ctx, `SELECT COALESCE(mount_folder, '') FROM content WHERE info_hash = ?`, hash).Scan(&folder)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var out []string
	// The recorded mount folder is stronger evidence than an old torrent/provider name.
	for _, n := range append([]string{folder}, names...) {
		if !ix.folders[n] || hash6Suffix.MatchString(n) {
			continue
		}
		var mountHash, nameHash sql.NullString
		var mountCount, nameCount int
		err := db.QueryRowContext(ctx, identityQuery, n, n, n, n, n+".mkv", n+".mp4", n, n+".mkv", n+".mp4").Scan(&mountHash, &mountCount, &nameHash, &nameCount)
		if err != nil {
			return nil, err
		}
		// A torrent's original name does not prove it reclaimed a bare name after a collision.
		matched := (mountCount == 1 && mountHash.String == hash) ||
			(mountCount == 0 && nameCount == 1 && nameHash.String == hash && !hash6Suffix.MatchString(names[0]))
		// An existing target with no registry claim against it can stay. This is not evidence for moving
		// to some other bare folder, and a known different mount folder still takes precedence.
		unchanged := n == names[0] && nameCount == 0 && (folder == "" || folder == n)
		if matched && n == folder {
			return []string{folder}, nil
		}
		if matched || unchanged {
			out = append(out, n)
		}
	}
	return out, nil
}

// Relink finds dangling or misdirected links in the versions' roots and repoints each to its release's current
// folder, found by info hash, when the same file is there (zurg renames a folder when two hashes share a name or an account
// re-files a release). version "" means every version under library.versions. Links of a version the gate refuses
// are reported as what would be done. A mount outage stops the sweep: nothing is read as missing then. The sweep
// also reconciles each root's active links whose symlink is gone (ReconcileLinks).
func (l *Library) Relink(ctx context.Context, version string, dryRun bool) ([]*LinkScan, error) {
	return l.relink(ctx, version, dryRun, nil)
}

// RenameWait is how long RelinkRenamed waits for a renamed folder to show in the mount (2 min).
var RenameWait = 2 * time.Minute

// renamePoll is how often RelinkRenamed looks for the new folder.
var renamePoll = 5 * time.Second

// RelinkRenamed repoints links after zurg renamed release folders (old -> new, from its log): it waits for each
// new folder to show in the mount, then sweeps every version's root trying the new name first for links into the
// old one. The link moves only when the same file is in the new folder, like any relink. A folder that does not
// show within RenameWait is left to the hourly sweep.
func (l *Library) RelinkRenamed(ctx context.Context, renames map[string]string) ([]*LinkScan, error) {
	if l.set.MountPath == "" {
		return nil, errors.New("library: library.mount.path is not set")
	}
	hints := map[string]string{}
	deadline := l.now().Add(RenameWait)
	for old, nw := range renames {
		for {
			np := filepath.Join(l.mount.AllPath(), nw)
			_, err := l.mount.withTimeout(ctx, np, func(f mountFS) (any, error) { return f.Stat(np) })
			if err == nil {
				hints[old] = nw
				break
			}
			if errors.Is(err, ErrMountDown) {
				return nil, err
			}
			if !l.now().Before(deadline) {
				l.log.Warn("library: renamed folder not in the mount; left to the sweep", "old", old, "new", nw,
					"waited", RenameWait)
				break
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(renamePoll):
			}
		}
	}
	if len(hints) == 0 {
		return nil, nil
	}
	return l.relink(ctx, "", false, hints)
}

func (l *Library) relink(ctx context.Context, version string, dryRun bool, hints map[string]string) ([]*LinkScan, error) {
	if l.set.MountPath == "" {
		return nil, errors.New("library: library.mount.path is not set")
	}
	out, err := l.sweep(ctx, version, dryRun, hints)
	if errors.Is(err, ErrMountDown) && !dryRun {
		// A sweep refused for the mount forgets the misses noted before: the listing that noted them may have been
		// the start of the outage, and a partial listing after it must not confirm them all at once.
		if ferr := l.forgetMisses(ctx, version); ferr != nil {
			l.log.Warn("library: noted misses not cleared after a refused sweep", "err", ferr)
		}
	}
	return out, err
}

// forgetMisses clears missing_since on the active links of version (every version for "").
func (l *Library) forgetMisses(ctx context.Context, version string) error {
	return l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE link SET missing_since = NULL WHERE state = 'active'
			AND missing_since IS NOT NULL AND (? = '' OR id IN (SELECT lv.link_id FROM link_version lv
			JOIN version v ON v.id = lv.version_id WHERE v.policy_key = ?))`, version, version)
		return err
	})
}

func (l *Library) sweep(ctx context.Context, version string, dryRun bool, hints map[string]string) ([]*LinkScan, error) {
	if err := l.mountHealthy(ctx); err != nil {
		return nil, err
	}
	ix, err := l.newMountIndex(ctx)
	if err != nil {
		return nil, err
	}
	zv := l.zurgView(ctx)
	if zv != nil {
		zv.ix = ix
	}
	var out []*LinkScan
	for _, k := range l.gate.Versions() {
		if version != "" && k != version {
			continue
		}
		for _, root := range l.gate.Roots(k) {
			sc, err := l.relinkRoot(ctx, ix, zv, k, root, dryRun, hints)
			if err != nil {
				return out, err
			}
			// Links whose symlink is gone are not in the walk: checked from our rows (ReconcileLinks). A root that
			// did not answer is skipped whole.
			if sc.Skipped == "" {
				if err := l.reconcileRoot(ctx, sc, nil, dryRun); err != nil {
					return out, err
				}
			}
			out = append(out, sc)
		}
	}
	return out, nil
}

// relinkRoot sweeps one root. zv is zurg's view (nil without its API): a candidate folder zurg says belongs to
// another torrent is skipped, a link whose torrent has no video upstream is named so, and while zurg repairs (within
// the grace) a dangling link is reported as repairing and not marked dead.
func (l *Library) relinkRoot(ctx context.Context, ix *mountIndex, zv *zurgView, version, root string, dryRun bool,
	hints map[string]string) (*LinkScan, error) {
	sc := &LinkScan{Version: version, Root: root, Causes: map[string]int{}, DryRun: dryRun}
	zv.note(sc)
	refused, down := l.scanCheck(ctx, version, root)
	if down != nil {
		l.skipRoot(sc, down)
		return sc, nil
	}
	if refused != nil && !dryRun {
		sc.DryRun, sc.Refused = true, refused.Error()
	}
	links, err := l.scanRoot(ctx, root, sc)
	if Unavailable(err) {
		l.skipRoot(sc, err)
		return sc, nil
	}
	if err != nil {
		return sc, err
	}
	db := l.st.Reader()
	// Symlinks Theatarr retired but a crash left behind (leftover.go) go first, and are not swept as links.
	los, links, err := l.findLeftovers(ctx, db, version, links)
	if err != nil {
		return sc, err
	}
	if err := l.cleanLeftovers(ctx, sc, version, root, los, dryRun); err != nil {
		return sc, err
	}
	marks, err := linkMarks(ctx, db, version)
	if err != nil {
		return sc, err
	}
	var found []mountLink  // links whose file is there: a dead row revives, a noted miss is forgotten
	lists := relinkLists{} // decypharr: the file lists read in this sweep
	var recompute []stateVersion
	for _, ml := range links {
		knownHash, err := linkHash(ctx, db, ml.path)
		if err != nil {
			return sc, err
		}
		ok, err := ix.has(ml.folder, ml.inner)
		if errors.Is(err, errFolderUnreadable) {
			l.log.Warn("library: sweep left a link whose release folder is unreadable", "link", ml.path, "err", err)
			continue
		}
		if err != nil {
			return sc, err
		}
		if m, marked := marks[ml.path]; ok && marked && m.target == ml.target {
			found = append(found, ml)
		}
		hint, hinted := hints[ml.folder]
		if m := hash6Suffix.FindStringSubmatch(hint); hinted && m != nil && knownHash != "" && !strings.HasPrefix(knownHash, m[2]) {
			// This link belongs to the newer torrent already occupying the bare name.
			hinted = false
		}
		if ok && !hinted && knownHash == "" {
			continue
		}
		dl := DanglingLink{Path: ml.path, Target: ml.target}
		hash, names, err := l.candidates(ctx, db, ml)
		if err != nil {
			return sc, err
		}
		dl.Hash = hash
		identity := hash
		if m := hash6Suffix.FindStringSubmatch(ml.folder); identity == "" && m != nil {
			identity = m[2]
		}
		if hinted {
			// An explicit rename is evidence even while the bare name resolves to a newer torrent. Do not
			// substitute some other candidate if the reported destination lacks this file.
			names = []string{hint}
		} else if identity != "" {
			names, err = identityFolders(ctx, db, ix, identity, append([]string{ml.folder}, names...))
			if err != nil {
				return sc, err
			}
			if ok {
				// An existing target is healthy unless a hash-suffixed folder for its identity also holds the
				// same file. Registry names alone cannot prove that a bare folder was reclaimed by this torrent.
				filtered := names[:0]
				for _, n := range names {
					m := hash6Suffix.FindStringSubmatch(n)
					if m != nil && strings.HasPrefix(identity, m[2]) {
						filtered = append(filtered, n)
					}
				}
				names = filtered
			}
		}
		if l.decy != nil && !ok && !hinted && len(hash) == 40 {
			// decypharr's current name for the hash, whatever the registry records.
			if dnames, known := l.decypharrNames(ctx, hash, false); known {
				names = withDecypharrNames(dnames, names)
			}
		}
		newFolder := ""
		for _, n := range names {
			found, err := ix.has(n, ml.inner)
			if errors.Is(err, errFolderUnreadable) {
				continue // no verdict on this candidate
			}
			if err != nil {
				return sc, err
			}
			if found && len(hash) == 40 {
				// zurg knows which torrent holds the file: a candidate holding another torrent's is not this link's.
				if e, ok := l.zurgConfirm(ctx, hash, n, ml.inner); !ok {
					l.log.Info("library: relink candidate holds another torrent", "link", ml.path, "folder", n,
						"want", hash, "zurg_hash", e.Hash)
					continue
				}
				// decypharr: the file there must be one of the download's own, when its list can be read.
				if !l.relinkHolds(ctx, db, lists, ml, hash, n) {
					l.log.Info("library: relink candidate holds another torrent's file", "link", ml.path, "folder", n,
						"want", hash)
					continue
				}
			}
			if found {
				newFolder = n
				dl.NewTarget = l.mount.ToLink(filepath.Join(l.mount.AllPath(), n, ml.inner))
				break
			}
		}
		if dl.NewTarget == ml.target {
			continue
		}
		if dl.NewTarget == "" {
			if ok {
				// A target that still exists must never become dangling because registry identity is ambiguous or
				// stale. It can move only on positive evidence handled above.
				continue
			}
			dl.Cause = "unknown"
			if hash != "" {
				var state string
				_ = db.QueryRowContext(ctx, `SELECT state FROM content WHERE info_hash = ?`, hash).Scan(&state)
				if state == "absent" || state == "gone" {
					dl.Cause = "gone"
				} else {
					dl.Cause = "missing"
				}
			}
			dl.Cause, dl.Reason = zv.classify(dl.Cause, hash, ml.folder)
			sc.Dangling = append(sc.Dangling, dl)
			sc.Causes[dl.Cause]++
			continue
		}
		if err := l.repoint(ctx, sc, version, root, ml, dl, hash, newFolder, &recompute); err != nil {
			return sc, err
		}
	}
	// A link the sweep repointed (or would) is not revived in place: the repoint revives it.
	kept := found[:0]
	for _, ml := range found {
		if !slices.ContainsFunc(sc.Relinked, func(dl DanglingLink) bool { return dl.Path == ml.path }) {
			kept = append(kept, ml)
		}
	}
	found = kept
	// Every link the sweep writes from here (a revive, a forgotten miss, a dead mark) is held under its link lease
	// (linklock.go) through the commit; one another run is changing now is left to the next sweep.
	if !sc.DryRun {
		lctx, unlockAll, _ := l.lockLinks(ctx)
		defer unlockAll()
		ctx = lctx
	}
	locked := func(p string) bool {
		if sc.DryRun {
			return true
		}
		if _, _, err := l.lockLinks(ctx, p); err != nil {
			l.log.Info("library: sweep left a link another run is changing", "link", p, "err", err)
			return false
		}
		return true
	}
	// A dead link whose file is back while another file holds its title or episodes now is moved aside, not revived:
	// it would be a second file next to its replacement.
	revive := found[:0]
	for _, ml := range found {
		if !locked(ml.path) {
			continue
		}
		m := marks[ml.path]
		state := m.state
		if err := db.QueryRowContext(ctx, `SELECT state FROM link WHERE id = ? AND path = ? AND target = ?`,
			m.id, ml.path, ml.target).Scan(&state); errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			return sc, err
		}
		if state == "dead" {
			covered, err := coveredElsewhere(ctx, db, m.id)
			if err != nil {
				return sc, err
			}
			if covered {
				if err := l.retireDead(ctx, sc, version, root, ml, m.id); err != nil {
					return sc, err
				}
				continue
			}
			if m.state == "dead" {
				sc.Revived = append(sc.Revived, DanglingLink{Path: ml.path, Target: ml.target, Cause: "revived"})
			}
		}
		revive = append(revive, ml)
	}
	found = revive
	var marking []DanglingLink
	for _, dl := range sc.Dangling {
		if locked(dl.Path) {
			marking = append(marking, dl)
		}
	}
	// Record the sweep's outcome (not in a pure dry run). A gated version (sc.DryRun: the library is disabled, or the
	// version is not mode: active) records its plan as dry runs only: no link turns dead or active, no title wanted.
	var scanned []string
	if !dryRun && (len(sc.Dangling) > 0 || len(found) > 0 || (sc.DryRun && (len(sc.Relinked) > 0 || len(sc.Retired) > 0))) {
		if l.beforeSweepWrite != nil {
			l.beforeSweepWrite()
		}
		// The mount is checked again before a miss is noted or confirmed: the walk can outlast a zurg restart, whose
		// half-loaded listing reads the folders as missing. A mount that is down now stops the sweep
		// (relink forgets the misses), as at its start.
		if len(marking) > 0 && !sc.DryRun {
			if _, err := l.newMountIndex(ctx); err != nil {
				return sc, err
			}
		}
		now := store.Millis(l.now())
		err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
			scanned = nil
			if sc.DryRun {
				return l.recordGatedSweep(ctx, tx, sc)
			}
			if err := l.linksHeldTx(ctx, tx); err != nil {
				return err
			}
			for _, dl := range marking {
				if err := l.markMissing(ctx, tx, version, dl, now); err != nil {
					return err
				}
			}
			for _, ml := range found {
				m := marks[ml.path]
				// The state now, not as the sweep began: another sweep may have marked the link dead meanwhile.
				var state string
				if err := tx.QueryRowContext(ctx, `SELECT state FROM link WHERE id = ? AND path = ? AND target = ?`,
					m.id, ml.path, ml.target).Scan(&state); errors.Is(err, sql.ErrNoRows) {
					continue
				} else if err != nil {
					return err
				}
				if state != "dead" {
					// Seen missing on an earlier sweep, there now: the miss is forgotten.
					if _, err := tx.ExecContext(ctx, `UPDATE link SET missing_since = NULL WHERE id = ? AND target = ?`,
						m.id, ml.target); err != nil {
						return err
					}
					continue
				}
				res, err := tx.ExecContext(ctx, `UPDATE link SET state = 'active', missing_since = NULL, last_checked_at = ?,
					updated_at = ? WHERE id = ? AND path = ? AND target = ? AND state = 'dead'`, now, now, m.id, ml.path, ml.target)
				if err != nil {
					return err
				}
				if n, err := res.RowsAffected(); err != nil {
					return err
				} else if n == 0 {
					continue
				}
				vs, err := reviveLink(ctx, tx, m.id, now)
				if err != nil {
					return err
				}
				recompute = append(recompute, vs...)
				scanned = append(scanned, filepath.Dir(ml.path))
				if err := l.record(ctx, tx, action{Kind: "relink", Mode: "applied", Version: version, LinkID: m.id,
					Path: ml.path, Target: ml.target, Detail: "revived: its file is on the mount again (was dead)"}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return sc, err
		}
	}
	// Plex sees a revived link's file again, as after a repoint.
	for _, d := range uniqueDirs(scanned...) {
		if err := l.EnqueueScan(ctx, version, d); err != nil {
			l.log.Warn("library: plex scan not queued", "dir", d, "err", err)
		}
	}
	// The revived links' versions: cutoff_met where the file meets the cutoff (reviveLink left them filled).
	for _, v := range recompute {
		if _, err := l.recomputeVersion(ctx, v); err != nil {
			l.log.Warn("library: state not recomputed after a revive", "version", v.key, "title", v.titleID, "err", err)
		}
	}
	sort.Slice(sc.Dangling, func(i, j int) bool { return sc.Dangling[i].Path < sc.Dangling[j].Path })
	return sc, nil
}

// deadConfirm is how long a link's file must stay missing before a sweep marks the link dead: the miss is noted on
// the first sweep (link.missing_since) and confirmed by a later one at least this long after, with no sweep finding
// the file in between. One half-loaded or briefly empty folder listing therefore never kills a link; the hourly
// sweep marks a link dead on its second miss.
var deadConfirm = 30 * time.Minute

// linkMark is a link row of a version a sweep has noted: dead, or seen missing before (missing_since).
type linkMark struct {
	id            int64
	target, state string
}

// linkMarks reads a version's dead links and those seen missing, by path: the few rows a sweep may revive or clear.
func linkMarks(ctx context.Context, db *sql.DB, version string) (map[string]linkMark, error) {
	rows, err := db.QueryContext(ctx, `SELECT l.id, l.path, l.target, l.state FROM link l
		JOIN link_version lv ON lv.link_id = l.id JOIN version v ON v.id = lv.version_id
		WHERE v.policy_key = ? AND (l.state = 'dead' OR (l.state = 'active' AND l.missing_since IS NOT NULL))`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]linkMark{}
	for rows.Next() {
		var m linkMark
		var p string
		if err := rows.Scan(&m.id, &p, &m.target, &m.state); err != nil {
			return nil, err
		}
		out[p] = m
	}
	return out, rows.Err()
}

// markMissing handles one dangling link of a sweep that may write. Only our active row with the target the walk saw
// counts (a link a refresh or relink repointed since is not dead, and an untracked symlink has no row to mark). The
// first miss is noted; a miss deadConfirm after it marks the link dead and its title or episodes wanted
// (reconcileRemovedLink), and queues one search for them (queueDeadLinkSearch). A link zurg is repairing (within the
// grace) is noted, never marked.
func (l *Library) markMissing(ctx context.Context, tx *sql.Tx, version string, dl DanglingLink, now int64) error {
	var id int64
	var since sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT id, missing_since FROM link WHERE path = ? AND target = ? AND state = 'active'`,
		dl.Path, dl.Target).Scan(&id, &since)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !since.Valid {
		_, err := tx.ExecContext(ctx, `UPDATE link SET missing_since = ?, last_checked_at = ? WHERE id = ?`, now, now, id)
		return err
	}
	if dl.Cause == CauseRepairing || now-since.Int64 < deadConfirm.Milliseconds() {
		return nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE link SET state = 'dead', missing_since = NULL, last_checked_at = ?, updated_at = ?
		WHERE id = ? AND path = ? AND target = ? AND state = 'active'`, now, now, id, dl.Path, dl.Target)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err
	}
	if err := reconcileRemovedLink(ctx, tx, id, now); err != nil {
		return err
	}
	if err := l.queueDeadLinkSearch(ctx, tx, id); err != nil {
		return err
	}
	return l.record(ctx, tx, action{Kind: "dead", Mode: "applied", Version: version, LinkID: id, Path: dl.Path,
		Target: dl.Target, Detail: dl.Cause + " " + dl.Hash})
}

// queueDeadLinkSearch queues, in the dead mark's transaction, one search per version of a link marked dead: its
// movie, or its episodes (router.ReasonDeadLink, docs/design/router.md "The search schedule"). Relink repoints a link
// by hash to any provider's folder first, so a dead link's file is on no provider: a new release is wanted. The
// acquirer makes the item due in the search schedule, and its round checks everything else (Pause off, the version
// active and monitored, still wanted, nothing in flight); the key keeps one job per version, title and episodes
// pending. Only
// versions the library gate lets write get one (a link can be shared with a shadow version).
func (l *Library) queueDeadLinkSearch(ctx context.Context, tx *sql.Tx, linkID int64) error {
	if l.queue == nil {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT v.id, v.policy_key, v.title_id, t.kind FROM link_version lv
		JOIN version v ON v.id = lv.version_id JOIN title t ON t.id = v.title_id WHERE lv.link_id = ?`, linkID)
	if err != nil {
		return err
	}
	type version struct {
		id, title int64
		key, kind string
	}
	var vs []version
	for rows.Next() {
		var v version
		if err := rows.Scan(&v.id, &v.key, &v.title, &v.kind); err != nil {
			rows.Close()
			return err
		}
		vs = append(vs, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var eps []int64
	erows, err := tx.QueryContext(ctx, `SELECT episode_id FROM link_episode WHERE link_id = ? ORDER BY episode_id`, linkID)
	if err != nil {
		return err
	}
	for erows.Next() {
		var ep int64
		if err := erows.Scan(&ep); err != nil {
			erows.Close()
			return err
		}
		eps = append(eps, ep)
	}
	erows.Close()
	if err := erows.Err(); err != nil {
		return err
	}
	for _, v := range vs {
		if !l.gate.Active(v.key) { // a shared link's shadow, followed or unlisted version: its own sweep records it
			continue
		}
		p := router.ResearchRequest{TitleID: v.title, VersionID: v.id, Version: v.key, Reason: router.ReasonDeadLink}
		key := fmt.Sprintf("%s/%s/%s/%d/", router.JobResearch, router.ReasonDeadLink, v.key, v.title)
		if v.kind == string(policy.KindSeries) {
			if len(eps) == 0 {
				continue
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
		l.log.Info("library: search queued for a dead link", "link", linkID, "version", v.key, "title", v.title,
			"episodes", len(p.Episodes))
	}
	return nil
}

// recordGatedSweep records what a sweep of a gated version would do, as dry runs: the repoints, the revives, and the
// links it would mark dead (our active rows only; an untracked symlink has none).
func (l *Library) recordGatedSweep(ctx context.Context, tx *sql.Tx, sc *LinkScan) error {
	for _, dl := range sc.Dangling {
		if dl.Cause == CauseRepairing {
			continue
		}
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM link WHERE path = ? AND target = ? AND state = 'active'`,
			dl.Path, dl.Target).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if err := l.record(ctx, tx, action{Kind: "dead", Mode: "dry_run", Version: sc.Version, LinkID: id, Path: dl.Path,
			Target: dl.Target, Detail: dl.Cause + " " + dl.Hash + "; " + sc.Refused}); err != nil {
			return err
		}
	}
	for _, dl := range sc.Revived {
		if err := l.record(ctx, tx, action{Kind: "relink", Mode: "dry_run", Version: sc.Version, Path: dl.Path,
			Target: dl.Target, Detail: "revive: its file is on the mount again; " + sc.Refused}); err != nil {
			return err
		}
	}
	for _, dl := range sc.Retired {
		if err := l.record(ctx, tx, action{Kind: "replace", Mode: "dry_run", Version: sc.Version, Path: dl.Path,
			Target: dl.Target, Detail: "move aside: its file is back, but another file holds its title or episodes; " +
				sc.Refused}); err != nil {
			return err
		}
	}
	for _, dl := range sc.Relinked {
		if err := l.record(ctx, tx, action{Kind: "relink", Mode: "dry_run", Version: sc.Version, Path: dl.Path,
			Target: dl.NewTarget, Detail: sc.Refused}); err != nil {
			return err
		}
	}
	return nil
}

// repoint moves one link a sweep found renamed or misdirected to dl.NewTarget, holding its link lease from the
// compare-and-swap to the commit. A dead link whose title or episodes have another file now is moved aside instead
// (retireDead): repointed, it would be a second file.
func (l *Library) repoint(ctx context.Context, sc *LinkScan, version, root string, ml mountLink, dl DanglingLink,
	hash, newFolder string, recompute *[]stateVersion) error {
	db := l.st.Reader()
	if !sc.DryRun {
		lctx, unlock, err := l.lockLinks(ctx, ml.path)
		defer unlock()
		if err != nil {
			l.log.Info("library: relink left a link another run is changing", "link", ml.path, "err", err)
			sc.Causes["changed"]++
			return nil
		}
		ctx = lctx
	}
	// Compare-and-swap (cas.go): the link must still point where the walk read it, and its row (when it has
	// one) must still carry that target. A refresh or rename that changed it since is left alone.
	if changed, err := linkMoved(ctx, db, ml); err != nil {
		return err
	} else if changed != "" {
		l.log.Info("library: relink left a link that changed during the sweep", "link", ml.path, "detail", changed)
		sc.Causes["changed"]++
		return nil
	}
	if !sc.DryRun {
		var id int64
		var state string
		err := db.QueryRowContext(ctx, `SELECT id, state FROM link WHERE path = ? AND target = ?`, ml.path, ml.target).
			Scan(&id, &state)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if state == "dead" {
			covered, err := coveredElsewhere(ctx, db, id)
			if err != nil {
				return err
			}
			if covered {
				return l.retireDead(ctx, sc, version, root, ml, id)
			}
		}
	}
	// The torrent delete fence is held from before the symlink is replaced through its row (refFence);
	// after a wait for a delete the new target is checked again.
	if !sc.DryRun {
		fctx, waited, release, err := l.refFence(ctx)
		defer release()
		if errors.Is(err, ErrLinkChanged) {
			l.log.Info("library: relink left a link while a torrent delete runs", "link", ml.path, "err", err)
			sc.Causes["changed"]++
			return nil
		} else if err != nil {
			return err
		}
		ctx = fctx
		if waited {
			if gone, err := l.targetGone(ctx, dl.NewTarget); err != nil {
				return err
			} else if gone {
				l.log.Info("library: relink target went with a torrent delete", "link", ml.path, "target", dl.NewTarget)
				sc.Causes["changed"]++
				return nil
			}
		}
	}
	dl.Cause = "renamed"
	sc.Causes["renamed"]++
	if sc.DryRun {
		sc.Relinked = append(sc.Relinked, dl)
		return nil
	}
	// A relink inside a job that lost its lease stops before replacing the link.
	if err := jobs.Fence(ctx); err != nil {
		return err
	}
	if err := l.w.symlink(version, dl.NewTarget, ml.path); err != nil {
		_ = l.record(ctx, nil, action{Kind: "relink", Mode: modeOf(err), Version: version, Path: ml.path, Target: dl.NewTarget, Detail: err.Error()})
		dl.Cause = "refused"
		sc.Dangling = append(sc.Dangling, dl)
		return nil
	}
	sc.Relinked = append(sc.Relinked, dl)
	now := store.Millis(l.now())
	var revivedVersions []stateVersion
	if err := l.refWriteTx(ctx, func(tx *sql.Tx) error {
		revivedVersions = nil
		if err := l.linksHeldTx(ctx, tx); err != nil {
			return err
		}
		// An unlinked row stays unlinked (an unlink or upgrade took it out); a dead one is active again.
		var revived int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM link WHERE path = ? AND target = ? AND state = 'dead'`,
			ml.path, ml.target).Scan(&revived); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE link SET target = ?, state = 'active', missing_since = NULL,
			last_checked_at = ?, updated_at = ? WHERE path = ? AND target = ? AND state IN ('active', 'dead')`,
			dl.NewTarget, now, now, ml.path, ml.target)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			// No row with the old target: an untracked link, or a row that changed after the check above.
			var other string
			if err := tx.QueryRowContext(ctx, `SELECT target FROM link WHERE path = ?`, ml.path).Scan(&other); err == nil {
				return linkChanged(0, ml.path, ml.target, "its row now points at "+other)
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if n, _ := res.RowsAffected(); n > 0 && revived != 0 {
			vs, err := reviveLink(ctx, tx, revived, now)
			if err != nil {
				return err
			}
			revivedVersions = vs
		}
		if hash != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE content SET mount_folder = ? WHERE LOWER(info_hash) = ?`, newFolder, hash); err != nil {
				return err
			}
		}
		return l.record(ctx, tx, action{Kind: "relink", Mode: "applied", Version: version, Path: ml.path,
			Target: dl.NewTarget, Detail: "was " + ml.target + " (" + hash + ")"})
	}); errors.Is(err, ErrLinkChanged) {
		l.log.Warn("library: relink lost a race; the link's row was left", "link", ml.path, "err", err)
		return nil
	} else if err != nil {
		return err
	}
	*recompute = append(*recompute, revivedVersions...)
	_ = l.EnqueueScan(ctx, version, filepath.Dir(ml.path))
	return nil
}

// coveredElsewhere reports whether a dead link's title (a movie) or every one of its episodes (a series) has another
// file in each of the link's versions: an active link or an arr file. Revived, the link would be a second file.
func coveredElsewhere(ctx context.Context, db *sql.DB, linkID int64) (bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT lv.version_id, t.kind FROM link_version lv
		JOIN version v ON v.id = lv.version_id JOIN title t ON t.id = v.title_id WHERE lv.link_id = ?`, linkID)
	if err != nil {
		return false, err
	}
	type version struct {
		id   int64
		kind string
	}
	var versions []version
	for rows.Next() {
		var v version
		if err := rows.Scan(&v.id, &v.kind); err != nil {
			rows.Close()
			return false, err
		}
		versions = append(versions, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(versions) == 0 {
		return false, err
	}
	for _, v := range versions {
		var uncovered int
		var err error
		if v.kind != "series" {
			err = db.QueryRowContext(ctx, `SELECT NOT EXISTS (SELECT 1 FROM link l JOIN link_version lv ON lv.link_id = l.id
				WHERE lv.version_id = ? AND l.id <> ? AND l.state = 'active')
				AND NOT EXISTS (SELECT 1 FROM version_file WHERE version_id = ?)`, v.id, linkID, v.id).Scan(&uncovered)
		} else {
			err = db.QueryRowContext(ctx, `SELECT COUNT(*) + (SELECT COUNT(*) = 0 FROM link_episode WHERE link_id = ?)
				FROM link_episode le WHERE le.link_id = ?
				AND NOT EXISTS (SELECT 1 FROM link l JOIN link_version lv ON lv.link_id = l.id
					JOIN link_episode x ON x.link_id = l.id
					WHERE lv.version_id = ? AND l.id <> ? AND l.state = 'active' AND x.episode_id = le.episode_id)
				AND NOT EXISTS (SELECT 1 FROM version_file vf JOIN version_file_episode fe ON fe.version_file_id = vf.id
					WHERE vf.version_id = ? AND fe.episode_id = le.episode_id)`, linkID, linkID, v.id, linkID, v.id).
				Scan(&uncovered)
		}
		if err != nil {
			return false, err
		}
		if uncovered > 0 {
			return false, nil
		}
	}
	return true, nil
}

// retireDead moves aside a dead link whose file is back but whose title or episodes have another file now
// (coveredElsewhere): the symlink goes to the trash and the row is unlinked, as an import moves an old file aside.
// The caller holds the path's link lease.
func (l *Library) retireDead(ctx context.Context, sc *LinkScan, version, root string, ml mountLink, id int64) error {
	sc.Retired = append(sc.Retired, DanglingLink{Path: ml.path, Target: ml.target, Cause: "replaced"})
	if sc.DryRun {
		return nil
	}
	if err := jobs.Fence(ctx); err != nil {
		return err
	}
	if !linkPointsAt(ml.path, ml.target) {
		return nil // changed since the walk
	}
	a := action{Kind: "replace", Version: version, LinkID: id, Path: ml.path, Target: ml.target}
	tp, _, err := l.w.copyToTrash(version, root, ml.path)
	if err == nil {
		err = l.w.removeLink(version, ml.path)
	}
	if err != nil {
		a.Mode, a.Detail = modeOf(err), "dead link not moved aside: "+err.Error()
		return l.record(ctx, nil, a)
	}
	now := store.Millis(l.now())
	if err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := l.linksHeldTx(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE link SET state = 'unlinked', missing_since = NULL, updated_at = ?
			WHERE id = ? AND path = ? AND target = ? AND state = 'dead'`, now, id, ml.path, ml.target); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO link_trash (link_id, version_key, path, trash_path, target, reason,
			moved_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, version, ml.path, tp, ml.target, "dead, replaced", now); err != nil {
			return err
		}
		a.Mode, a.Detail = "applied", "dead link moved aside to "+tp+": its file is back, but another file holds its title or episodes"
		return l.record(ctx, tx, a)
	}); err != nil {
		return err
	}
	_ = l.EnqueueScan(ctx, version, filepath.Dir(ml.path))
	return nil
}
