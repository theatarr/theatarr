package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
)

// Follower refresh (docs/parallel.md "Refresh"): adopt never replaces a link it made, so when the arr upgrades a
// file, the parallel version's adopted link keeps pointing at the old file, and dangles once the arr deletes it.
// Refresh moves each adopted link to the arr's current file for the same title (movies) or the same episodes
// (series): the new link is made under the version's naming, the old one is copied to the trash and removed, the
// link row is recorded as adopted again, and Plex gets a partial scan of the folders. Only links adopt made are
// touched (their latest applied link action says "adopted from ..." and they carry no download), never the links of
// Theatarr's own imports, and never in a version that grabs (FollowerGrabs). The arr's files are only read.

// Reasons a refresh leaves an adopted link as it is (besides the adopt reasons for the arr's file).
const (
	// RefreshNoArrFile: the arr has no file for the title (movies) or for any of the link's episodes.
	RefreshNoArrFile = "arr_file_removed"
	// RefreshSplit: the arr's files hold the link's episodes in another split (no one file with the same episodes).
	RefreshSplit = "arr_episodes_split"
	// RefreshTargetMissing: the arr's current file is not on the mount (a mount hiccup, or not there yet).
	RefreshTargetMissing = "target_missing"
	// RefreshChanged: the link changed under the refresh (a rename or relink got there first, cas.go); it is left.
	RefreshChanged = "link_changed"
	// RefreshFailed: a write of the swap was refused or failed; the link is left and the next run tries again.
	RefreshFailed = "swap_failed"
)

// RefreshOptions say which parallel version's adopted links to refresh.
type RefreshOptions struct {
	Version string
	// DryRun plans and reports without writing anything.
	DryRun bool
	// Titles limits the refresh to these title ids (nil: every title). The arr import passes the titles whose arr
	// files changed.
	Titles []int64
}

// RefreshReport is what a refresh did, or would do.
type RefreshReport struct {
	Version string `json:"version"`
	Follows string `json:"follows"`
	DryRun  bool   `json:"dry_run"`
	// Links counts the adopted links looked at; Current those already pointing at the arr's current file.
	Links   int `json:"links"`
	Current int `json:"current"`
	// Swapped counts the links moved to the arr's current file (in a dry run: would move).
	Swapped int `json:"swapped"`
	// Skipped counts the links left as they are, by reason.
	Skipped map[string]int `json:"skipped"`
	// Failed counts the links whose swap failed (RefreshChanged, RefreshFailed): each is in Problems, and the run
	// went on with the other links.
	Failed   int            `json:"failed"`
	Problems []AdoptProblem `json:"problems,omitempty"`
	Swaps    []RefreshSwap  `json:"swaps,omitempty"`
	// made are the link paths this run made or planned: two adopted links never move onto one.
	made map[string]bool
}

// RefreshSwap is one adopted link moved (or to be moved) to the arr's current file.
type RefreshSwap struct {
	From      string `json:"from"` // the arr's current file
	Old       string `json:"old"`
	OldTarget string `json:"old_target"`
	Path      string `json:"path"`
	Target    string `json:"target"`
	TrashPath string `json:"trash_path,omitempty"`
}

// fail records a link whose swap failed; the refresh goes on with the next link.
func (r *RefreshReport) fail(p, reason, detail string) {
	r.Failed++
	if len(r.Problems) < adoptListMax {
		r.Problems = append(r.Problems, AdoptProblem{Path: p, Reason: reason, Detail: detail})
	}
}

// swapFailed is a refresh failure of one link only: the run records it and goes on.
type swapFailed struct {
	reason string
	err    error
}

func (e *swapFailed) Error() string { return e.err.Error() }
func (e *swapFailed) Unwrap() error { return e.err }

func failed(err error) error {
	if errors.Is(err, ErrLinkChanged) {
		return &swapFailed{reason: RefreshChanged, err: err}
	}
	return &swapFailed{reason: RefreshFailed, err: err}
}

func (r *RefreshReport) skip(p, reason, detail string) {
	r.Skipped[reason]++
	if len(r.Problems) < adoptListMax {
		r.Problems = append(r.Problems, AdoptProblem{Path: p, Reason: reason, Detail: detail})
	}
}

// FollowerGrabs reports whether a version grabs on its own: the router's gate (router.Gate) lets it write, which
// takes router.enabled, mode: active and the version's grab switch (policy.Grabs). A parallel version
// is active to have links at all, so it follows its arr while it does not grab; then refresh keeps its adopted links
// on the arr's files. Once it grabs, its links are its own and refresh leaves them.
func FollowerGrabs(pol *policy.Policy, version string) bool {
	return pol.Grabs(version)
}

// ErrGrabbing refuses a refresh of a version that grabs on its own.
var ErrGrabbing = errors.New("the version grabs on its own (router.enabled, mode: active and grab): its links are not refreshed from the arr")

// adoptedLink is one link adopt made for the parallel version: active, or dead (its file left the mount before a
// refresh moved it to the arr's new file).
type adoptedLink struct {
	id, titleID     int64
	path, target    string
	state           string
	episodes        []int64
	versionID       int64
	adoptedFromPath string
}

// Refresh moves the parallel version's adopted links to the arr's current files.
func (l *Library) Refresh(ctx context.Context, o RefreshOptions) (*RefreshReport, error) {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.Refresh(pinned, o)
	}
	ctx = withCutoffTitles(ctx)
	ctx, err := decide.WithStoredSeaDex(ctx, l.st.Reader())
	if err != nil {
		return nil, err
	}
	vp := l.pol.Versions[o.Version]
	if vp == nil {
		return nil, fmt.Errorf("library: refresh: unknown library %q", o.Version)
	}
	if vp.Follows == "" {
		return nil, fmt.Errorf("library: refresh: %s is not a parallel version (versions.%s.follows)", o.Version, o.Version)
	}
	if FollowerGrabs(l.pol, o.Version) {
		return nil, fmt.Errorf("library: refresh %s: %w", o.Version, ErrGrabbing)
	}
	rep := &RefreshReport{Version: o.Version, Follows: vp.Follows, DryRun: o.DryRun, Skipped: map[string]int{},
		made: map[string]bool{}}
	if o.Titles != nil && len(o.Titles) == 0 {
		return rep, nil
	}
	if err := l.followGate(ctx, o.Version, o.DryRun); err != nil {
		return nil, fmt.Errorf("library: refresh %s: %w", o.Version, err)
	}
	if !o.DryRun {
		// One refresh of a version at a time, across processes too: two would each see the old link, and the loser
		// of the row's compare-and-swap would remove the new link the winner recorded.
		ls, err := l.takeLease(ctx, "refresh:"+o.Version)
		if err != nil {
			return nil, fmt.Errorf("library: refresh %s: %w", o.Version, err)
		}
		defer l.dropLease(ctx, ls)
		ctx = withLease(ctx, ls)
	}
	vn, err := NamingFor(ctx, l.st.Reader(), l.pol, o.Version)
	if err != nil {
		return nil, err
	}
	root := filepath.Clean(vp.Library)
	if l.set.MountPath != "" {
		if err := l.mountHealthy(ctx); err != nil {
			return nil, fmt.Errorf("library: refresh: %w", err)
		}
		// An empty or half-loaded listing is a mount that is down too (Mount.checkListing).
		if _, err := l.newMountIndex(ctx); err != nil {
			return nil, fmt.Errorf("library: refresh: %w", err)
		}
	}
	links, err := adoptedLinks(ctx, l.st.Reader(), o.Version, o.Titles)
	if err != nil {
		return nil, err
	}
	if len(links) == 0 {
		return rep, nil
	}
	titles := map[int64]bool{}
	for _, lk := range links {
		titles[lk.titleID] = true
	}
	all, err := l.adoptFiles(ctx, vp.Follows, o.Version, o.Titles)
	if err != nil {
		return nil, err
	}
	byTitle := map[int64][]*adoptFile{}
	for i := range all {
		if f := &all[i]; titles[f.titleID] {
			byTitle[f.titleID] = append(byTitle[f.titleID], f)
		}
	}
	dirs := map[string]bool{}
	for i := range links {
		rep.Links++
		changed, err := l.refreshOne(ctx, o, vp, vn, root, &links[i], byTitle[links[i].titleID], rep)
		var sf *swapFailed
		if errors.As(err, &sf) {
			// One link's swap failed: recorded, and the other links still run.
			rep.fail(links[i].path, sf.reason, sf.Error())
			l.log.Warn("library: follower refresh: a link's swap failed", "version", o.Version, "link", links[i].path,
				"reason", sf.reason, "err", sf.err)
			continue
		}
		if err != nil {
			return rep, err
		}
		for _, d := range changed {
			dirs[d] = true
		}
	}
	if !o.DryRun {
		for _, d := range sortedKeys(dirs) {
			if err := l.EnqueueScan(ctx, o.Version, d); err != nil {
				l.log.Warn("library: plex scan not queued", "dir", d, "err", err)
			}
		}
	}
	if rep.Swapped > 0 || len(rep.Skipped) > 0 || rep.Failed > 0 {
		l.log.Info("library: follower refresh", "version", o.Version, "follows", vp.Follows, "dry_run", o.DryRun,
			"links", rep.Links, "current", rep.Current, "swapped", rep.Swapped, "skipped", rep.Skipped,
			"failed", rep.Failed)
	}
	for _, p := range rep.Problems {
		l.log.Info("library: follower refresh left a link", "version", o.Version, "link", p.Path, "reason", p.Reason,
			"detail", p.Detail)
	}
	return rep, nil
}

// adoptedLinks reads the version's active and dead links that adopt made: link.adopted_from is set ("<instance>
// <arr path>"), and no download (an import of Theatarr's own) has taken the row over since. A dead one is refreshed too:
// the arr upgraded, and the old file left the mount before a refresh moved the link.
func adoptedLinks(ctx context.Context, db *sql.DB, version string, titles []int64) ([]adoptedLink, error) {
	q := `SELECT l.id, l.title_id, l.path, l.target, l.state, lv.version_id, l.adopted_from,
		       COALESCE((SELECT json_group_array(episode_id) FROM (SELECT episode_id FROM link_episode
		           WHERE link_id = l.id ORDER BY episode_id)), '[]')
		FROM link l JOIN link_version lv ON lv.link_id = l.id JOIN version v ON v.id = lv.version_id
		WHERE v.policy_key = ? AND l.state IN ('active', 'dead') AND l.download_id IS NULL AND l.adopted_from IS NOT NULL`
	// The titles are filtered here, not in SQL: a first import can change thousands.
	var only map[int64]bool
	if titles != nil {
		only = make(map[int64]bool, len(titles))
		for _, id := range titles {
			only[id] = true
		}
	}
	rows, err := db.QueryContext(ctx, q+` ORDER BY l.id`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []adoptedLink
	for rows.Next() {
		var a adoptedLink
		var detail, eps string
		if err := rows.Scan(&a.id, &a.titleID, &a.path, &a.target, &a.state, &a.versionID, &detail, &eps); err != nil {
			return nil, err
		}
		if only != nil && !only[a.titleID] {
			continue
		}
		_ = json.Unmarshal([]byte(eps), &a.episodes)
		// "<instance> <arr path>"
		if f := strings.SplitN(detail, " ", 2); len(f) == 2 {
			a.adoptedFromPath = f[1]
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// refreshFile picks the arr's current file for an adopted link: the title's file for a movie, the file with
// exactly the link's episodes for a series. "" and nil when there is one; else the reason it is left.
func refreshFile(ctx context.Context, db *sql.DB, kind policy.Kind, lk *adoptedLink, files []*adoptFile) (*adoptFile, string, error) {
	if kind != policy.KindSeries {
		if len(files) == 0 {
			return nil, RefreshNoArrFile, nil
		}
		// One file per movie; should two rows exist mid-import, the newest wins.
		return files[len(files)-1], "", nil
	}
	want := map[int64]bool{}
	for _, e := range lk.episodes {
		want[e] = true
	}
	touched := false
	for _, f := range files {
		eps, _, err := adoptEpisodes(ctx, db, f.id)
		if err != nil {
			return nil, "", err
		}
		same := len(eps) == len(want) && len(eps) > 0
		for _, e := range eps {
			if want[e] {
				touched = true
			} else {
				same = false
			}
		}
		if same {
			return f, "", nil
		}
	}
	if touched {
		return nil, RefreshSplit, nil
	}
	return nil, RefreshNoArrFile, nil
}

// refreshOne moves one adopted link to the arr's current file, or reports why not. It returns the folders to scan.
func (l *Library) refreshOne(ctx context.Context, o RefreshOptions, vp *policy.VersionPolicy, vn *VersionNaming, root string,
	lk *adoptedLink, files []*adoptFile, rep *RefreshReport) ([]string, error) {
	f, reason, err := refreshFile(ctx, l.st.Reader(), vp.Kind, lk, files)
	if err != nil {
		return nil, err
	}
	if f == nil {
		rep.skip(lk.path, reason, "adopted from "+lk.adoptedFromPath+"; the link stays")
		return nil, nil
	}
	// The arr's current file: its symlink, read as adopt reads it (bounded: readLinkBounded).
	ar, err := readLinkBounded(ctx, f.path)
	if err != nil {
		return nil, fmt.Errorf("library: refresh: %w", err)
	}
	if ar.lerr != nil {
		rep.skip(lk.path, AdoptMissing, f.path)
		return nil, nil
	}
	if ar.fi.Mode()&os.ModeSymlink == 0 {
		rep.skip(lk.path, AdoptNotLink, f.path)
		return nil, nil
	}
	if ar.rerr != nil {
		rep.skip(lk.path, AdoptNotLink, ar.rerr.Error())
		return nil, nil
	}
	target := ar.target
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(f.path), target)
	}
	target = filepath.Clean(target)
	if target == lk.target {
		rep.Current++
		return nil, nil
	}
	read, ok := l.adoptTarget(target)
	if !ok {
		rep.skip(lk.path, AdoptOffMount, target)
		return nil, nil
	}
	st, err := l.mount.Stat(ctx, read)
	if errors.Is(err, ErrMountDown) {
		return nil, fmt.Errorf("library: refresh: %w", err)
	}
	if err != nil || !st.Mode().IsRegular() {
		detail := target
		if err != nil {
			detail = err.Error()
		}
		rep.skip(lk.path, RefreshTargetMissing, detail)
		return nil, nil
	}
	episodes, neps, err := adoptEpisodes(ctx, l.st.Reader(), f.id)
	if err != nil {
		return nil, err
	}
	p, err := l.adoptPath(ctx, vp, vn, root, f, neps)
	if err != nil {
		rep.skip(lk.path, AdoptNaming, err.Error())
		return nil, nil
	}
	if rep.made[p] {
		rep.skip(lk.path, AdoptConflict, p+" (another adopted link moves there)")
		return nil, nil
	}
	if p != lk.path {
		if row, err := rowAtPath(ctx, l.st.Reader(), p, lk.versionID); err != nil {
			return nil, err
		} else if why := row.taken(lk.titleID, target); why != "" {
			rep.skip(lk.path, AdoptConflict, p+": "+why)
			return nil, nil
		}
		if own, err := importedRowAt(ctx, l.st.Reader(), p); err != nil {
			return nil, err
		} else if own != 0 {
			rep.skip(lk.path, AdoptConflict, fmt.Sprintf("%s: the path holds link %d, imported by the version itself", p, own))
			return nil, nil
		}
		pr, err := readLinkBounded(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("library: refresh: %w", err)
		}
		if pr.lerr == nil {
			if pr.fi.Mode()&os.ModeSymlink == 0 || pr.rerr != nil || pr.target != target {
				rep.skip(lk.path, AdoptConflict, p)
				return nil, nil
			}
		} else if !errors.Is(pr.lerr, fs.ErrNotExist) {
			rep.skip(lk.path, AdoptConflict, pr.lerr.Error())
			return nil, nil
		}
	}
	swap := RefreshSwap{From: f.path, Old: lk.path, OldTarget: lk.target, Path: p, Target: target}
	rep.made[p] = true
	if o.DryRun {
		rep.Swapped++
		if len(rep.Swaps) < adoptListMax {
			rep.Swaps = append(rep.Swaps, swap)
		}
		return nil, nil
	}
	if err := jobs.Fence(ctx); err != nil {
		return nil, fmt.Errorf("library: refresh: stopped before a link: %w", err)
	}
	if err := l.holdLease(ctx); err != nil {
		return nil, fmt.Errorf("library: refresh: stopped before a link: %w", err)
	}
	// Both paths are held under their link leases (linklock.go) through the commit: a reconcile, relink, unlink or
	// import of either waits for this swap, or this swap is left to the next run.
	ctx, unlock, err := l.lockLinks(ctx, lk.path, p)
	defer unlock()
	if err != nil {
		return nil, failed(err)
	}
	// The torrent delete fence is held from before the new symlink through its row (refFence); after a
	// wait for a delete the new target is checked again.
	ctx, waited, release, err := l.refFence(ctx)
	defer release()
	if err != nil {
		return nil, failed(err)
	}
	if waited {
		if gone, err := l.targetGone(ctx, target); err != nil {
			return nil, fmt.Errorf("library: refresh: %w", err)
		} else if gone {
			rep.skip(lk.path, RefreshTargetMissing, target+" (gone after a torrent delete)")
			return nil, nil
		}
	}
	// Compare-and-swap (cas.go): the link must still be the one read, in its row and on disk (or gone, or already
	// on the new target: an interrupted run), or a rename or relink got there first and the link is left.
	if err := rowIsIn(ctx, l.st.Reader(), lk.id, lk.path, lk.target, lk.state); err != nil {
		return nil, failed(err)
	}
	if _, err := os.Lstat(lk.path); err == nil && !linkPointsAt(lk.path, lk.target) && !linkPointsAt(lk.path, target) {
		return nil, failed(linkChanged(lk.id, lk.path, lk.target, "the symlink no longer points there"))
	}
	// The old link is copied to the trash first; then the new link is made (over the old one when the name is the
	// same) and the old one removed. A link that is already gone (an interrupted run) needs no trash copy.
	tp := ""
	if _, err := os.Lstat(lk.path); err == nil {
		t, _, err := l.w.copyToTrash(o.Version, root, lk.path)
		if err != nil {
			_ = l.record(ctx, nil, action{Kind: "replace", Mode: modeOf(err), Version: o.Version, LinkID: lk.id,
				Path: lk.path, Detail: "refresh: " + err.Error()})
			return nil, failed(fmt.Errorf("library: refresh %s: %w", lk.path, err))
		}
		tp = t
	}
	// A refresh that stalled past its lease (a newer one took it over and may have refreshed this link) stops before
	// replacing anything, and checks again before each later write and in the commit.
	if err := l.fenceLease(ctx); err != nil {
		return nil, fmt.Errorf("library: refresh: stopped before a link: %w", err)
	}
	prev, _ := os.Readlink(p) // what p held right before the swap, restored when the commit loses
	made := false
	// undo puts p back as it was when this swap's record does not land: the previous symlink (the same path too),
	// or no link at a new path. Only a link that is still the one made here is touched.
	undo := func() {
		if !made || !linkPointsAt(p, target) {
			return
		}
		if prev != "" {
			_ = l.w.symlink(o.Version, prev, p)
		} else if p != lk.path {
			_ = l.w.removeLink(o.Version, p)
		}
	}
	if !linkPointsAt(p, target) {
		if err := l.w.symlink(o.Version, target, p); err != nil {
			_ = l.record(ctx, nil, action{Kind: "link", Mode: modeOf(err), Version: o.Version, Path: p, Target: target,
				Detail: "refresh: " + err.Error()})
			return nil, failed(fmt.Errorf("library: refresh %s: %w", lk.path, err))
		}
		made = true
	}
	if p != lk.path && tp != "" {
		if err := l.fenceLease(ctx); err != nil {
			undo()
			return nil, fmt.Errorf("library: refresh: stopped before removing a link: %w", err)
		}
		if !linkPointsAt(lk.path, lk.target) {
			// Rewritten since the check: it is not ours to remove, and the new link is not needed.
			undo()
			return nil, failed(linkChanged(lk.id, lk.path, lk.target, "the symlink changed during the swap"))
		}
		if err := l.w.removeLink(o.Version, lk.path); err != nil {
			return nil, failed(fmt.Errorf("library: refresh %s: %w", lk.path, err))
		}
	}
	swap.TrashPath = tp
	note := fmt.Sprintf(" (refresh: replaces %s -> %s)", lk.path, lk.target)
	check := func(tx *sql.Tx) error {
		if err := l.leaseHeldTx(ctx, tx); err != nil {
			return err
		}
		return rowIsIn(ctx, tx, lk.id, lk.path, lk.target, lk.state)
	}
	err = l.recordAdopted(ctx, o.Version, vp, f, p, target, episodes, note, check, func(tx *sql.Tx, now int64) error {
		if p != lk.path {
			res, err := tx.ExecContext(ctx, `UPDATE link SET state = 'unlinked', updated_at = ? WHERE id = ? AND path = ?
				AND target = ? AND state = ?`, now, lk.id, lk.path, lk.target, lk.state)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil {
				return err
			} else if n != 1 {
				return linkChanged(lk.id, lk.path, lk.target, "its row changed")
			}
			if err := reconcileRemovedLink(ctx, tx, lk.id, now); err != nil {
				return err
			}
		}
		detail := "refresh: the arr's file is now " + f.path
		if tp != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO link_trash (link_id, version_key, path, trash_path, target,
				reason, moved_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, lk.id, o.Version, lk.path, tp, lk.target,
				"refresh: arr upgrade", now); err != nil {
				return err
			}
			detail += "; moved aside to " + tp
		}
		return l.record(ctx, tx, action{Kind: "replace", Mode: "applied", Version: o.Version, LinkID: lk.id,
			Path: lk.path, Target: lk.target, Detail: detail})
	})
	if errors.Is(err, ErrLinkChanged) || errors.Is(err, ErrBusy) {
		// The row moved on (a rename or relink) after the disk swap, or the lease was lost to a newer refresh: the
		// link made here is not recorded, so p goes back to what it held (the previous symlink, or none at a new
		// path).
		undo()
		if errors.Is(err, ErrBusy) {
			return nil, fmt.Errorf("library: refresh: lost the lease before recording a link: %w", err)
		}
		return nil, failed(err)
	}
	if err != nil {
		return nil, err
	}
	rep.Swapped++
	if len(rep.Swaps) < adoptListMax {
		rep.Swaps = append(rep.Swaps, swap)
	}
	return uniqueDirs(filepath.Dir(lk.path), filepath.Dir(p)), nil
}

func uniqueDirs(dirs ...string) []string {
	sort.Strings(dirs)
	out := dirs[:0]
	for i, d := range dirs {
		if i == 0 || d != dirs[i-1] {
			out = append(out, d)
		}
	}
	return out
}

// RefreshFollowers refreshes adopted links and adopts new files for every parallel version following followed,
// limited to titles whose arr files an import changed. A version that grabs, or whose gate is closed, is skipped; a
// refresh that fails, or leaves a link whose swap failed, is logged and returned as an error after the next version
// still ran. It is what arr.import calls after each instance.
func (l *Library) RefreshFollowers(ctx context.Context, followed string, titles []int64) error {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.RefreshFollowers(pinned, followed, titles)
	}
	if len(titles) == 0 {
		return nil
	}
	return l.refreshFollowers(ctx, followed, titles)
}

// RefreshFollowersAll is RefreshFollowers over every title: the catch-up arr.import runs on a schedule and after a
// failed refresh (docs/parallel.md "Refresh"), so new files and upgrades skipped during a mount outage, closed gate
// or failed link are picked up although the import that saw them has passed. Both operations are idempotent.
func (l *Library) RefreshFollowersAll(ctx context.Context, followed string) error {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.RefreshFollowersAll(pinned, followed)
	}
	return l.refreshFollowers(ctx, followed, nil)
}

func (l *Library) refreshFollowers(ctx context.Context, followed string, titles []int64) error {
	scope := "all"
	if titles != nil {
		scope = fmt.Sprint(len(titles))
	}
	var errs []error
	for _, k := range l.pol.Followers(followed) {
		if FollowerGrabs(l.pol, k) {
			l.log.Info("library: follower refresh skipped: the version grabs", "version", k, "titles", scope)
			continue
		}
		if err := l.gate.CheckVersion(k); err != nil {
			l.log.Info("library: follower refresh skipped: gate closed", "version", k, "titles", scope, "err", err)
			continue
		}
		rep, err := l.Refresh(ctx, RefreshOptions{Version: k, Titles: titles})
		if errors.Is(err, ErrBusy) {
			// Another refresh of the version (another process) is running; the scheduled catch-up covers these titles.
			l.log.Info("library: follower refresh skipped: another refresh holds the version", "version", k, "titles", scope)
			continue
		}
		if err != nil {
			l.log.Warn("library: follower refresh failed", "version", k, "titles", scope, "err", err)
			errs = append(errs, err)
			continue
		}
		l.log.Info("library: follower refresh", "version", k, "titles", scope, "links", rep.Links,
			"current", rep.Current, "swapped", rep.Swapped, "skipped", rep.Skipped, "failed", rep.Failed)
		if rep.Failed > 0 {
			errs = append(errs, fmt.Errorf("library: follower refresh %s: %d link(s) failed", k, rep.Failed))
			continue
		}
		adopted, err := l.Adopt(ctx, AdoptOptions{Version: k, Titles: titles})
		if err != nil {
			l.log.Warn("library: follower adoption failed", "version", k, "titles", scope, "err", err)
			errs = append(errs, err)
			continue
		}
		l.log.Info("library: follower adoption", "version", k, "titles", scope, "files", adopted.Files,
			"linked", adopted.Linked, "existing", adopted.Existing, "has_file", adopted.HasFile, "skipped", adopted.Skipped)
	}
	return errors.Join(errs...)
}
