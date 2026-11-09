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
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/library/naming"
	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// Plan is what an import does, or would do.
type Plan struct {
	DownloadID int64         `json:"download_id"`
	Release    string        `json:"release"`
	Version    string        `json:"version"`
	Folder     string        `json:"folder,omitempty"` // the release folder on the mount, as read here
	Waiting    bool          `json:"waiting,omitempty"`
	Files      []PlannedFile `json:"files,omitempty"`
	Links      []PlannedLink `json:"links,omitempty"`
	Rejected   []PlannedFile `json:"rejected,omitempty"`
	Unmatched  []string      `json:"unmatched,omitempty"` // files no episode of the download matched
	// Unresolved says, for an unmatched file, why the resolver's own answer could not be used (resolver.episodes):
	// ambiguous, out of scope or a conflict. Such a file is not read by its canonical numbers.
	Unresolved map[string]string `json:"unresolved,omitempty"`
	Held       []string          `json:"held,omitempty"` // episodes held: their franchise mapping is not reviewed (anime.go)
	DryRun     bool              `json:"dry_run"`
	Refused    string            `json:"refused,omitempty"` // why the gate made it a dry run
	Naming     string            `json:"naming,omitempty"`
	Probes     map[string]any    `json:"-"`
}

// PlannedFile is a video file of the release.
type PlannedFile struct {
	Path   string        `json:"path"`
	Rel    string        `json:"rel"`
	Size   int64         `json:"size"`
	Probe  *probe.Result `json:"probe,omitempty"`
	Reason string        `json:"reason,omitempty"` // why the guard rejected it, or why the probe failed
	// Note is what the mislabel guard saw without refusing the file (mislabel.go), e.g. "remux: x264 encoder SEI, ...".
	Note  string            `json:"note,omitempty"`
	Facts *decide.FileFacts `json:"-"`
	// NotUpgrade marks a file skipped because it does not upgrade a current file it would replace (upgradeCheck);
	// Reason says which and why.
	NotUpgrade bool `json:"not_upgrade,omitempty"`
	// Zurg is zurg's entry for the file, with library.mount.zurg_api set and an answer (zurgapi.go).
	Zurg *ZurgFileCheck `json:"zurg,omitempty"`
	// Collision marks a file whose path holds another title's or version's link (pathRow.taken); Reason says whose.
	Collision bool `json:"collision,omitempty"`
	// Episodes are the episodes (S01E02) of a series file the guard refused: in a pack whose other files link, they
	// stay wanted and the journal names them.
	Episodes []string `json:"episodes,omitempty"`
}

// ZurgFileCheck is what zurg's API said about a release file. Confirmed: the download's hash, and the size the
// mount shows. A file that is not confirmed is not linked, and an import left with only such files is held for
// review, never failed or blocklisted: the mount and zurg disagree, which is no verdict on the release.
type ZurgFileCheck struct {
	Hash      string `json:"hash"`
	Size      int64  `json:"size"`
	Confirmed bool   `json:"confirmed"`
}

// PlannedLink is one symlink.
type PlannedLink struct {
	Path     string  `json:"path"`
	Target   string  `json:"target"`
	File     string  `json:"file"` // the release file (Rel)
	Episodes []int64 `json:"episodes,omitempty"`
	// How says where a series file's episodes came from: HowManual (a client's choice), the parse and mapping the
	// shared resolver used (decide.ResolveFile: file, release, folder, special, +franchise:<entry>, +alt_cut:<name>),
	// or HowNumber / HowAbsolute (the canonical numbers only, when the resolver found nothing).
	How      string   `json:"how,omitempty"`
	Replaces []string `json:"replaces,omitempty"` // current files of the version moved aside
	// ReplaceIDs are the link ids of Replaces, by index (0 for an arr file): apply finds a link a rename moved since
	// the plan by its id.
	ReplaceIDs []int64 `json:"replace_ids,omitempty"`
	// Quality is the file's own quality (fileFacts), which names it and is stored on its link.
	Quality string `json:"quality,omitempty"`
	Exists  bool   `json:"exists,omitempty"` // already linked to this target
	// Done and Error are the link's outcome in an applied import: made (or found in place), or why it failed. A
	// partial import keeps both in library_import.plan; its retry finds the links made in place (Exists).
	Done  bool   `json:"done,omitempty"`
	Error string `json:"error,omitempty"`
	// prev is what the path held when the import planned it (its symlink's target, "" for none): apply replaces
	// only that (linkReady).
	prev  string
	probe *probe.Result
	size  int64
	facts decide.FileFacts
}

// replaceID is the link id of Replaces[i], 0 when unknown (an arr file, or a plan recorded before ReplaceIDs).
func (lk *PlannedLink) replaceID(i int) int64 {
	if i < len(lk.ReplaceIDs) {
		return lk.ReplaceIDs[i]
	}
	return 0
}

// Where a linked series file's episodes came from (PlannedLink.How), besides the resolver's own labels.
const (
	HowManual   = "manual"
	HowNumber   = "number"
	HowAbsolute = "absolute"
	// HowReplaced marks a client's file (download_file, source manual) that Change episodes with replace left with
	// no episodes (reassign.go): it holds none, where an ordinary chosen file with no episodes is decided by its name.
	HowReplaced = "replaced"
)

// Run imports every ready download (and those still waiting for the mount). It stops at a mount outage. The first
// pass of a process recomputes the states Theatarr's links give their versions first (RecomputeLinkStates).
func (l *Library) Run(ctx context.Context) (int, error) {
	l.recomputeOnce(ctx)
	rows, err := l.st.Reader().QueryContext(ctx, `SELECT id FROM download WHERE state IN ('ready', 'importing') ORDER BY id`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	done := 0
	for _, id := range ids {
		var st, lastErr string
		var attempts int
		var updated int64
		_ = l.st.Reader().QueryRowContext(ctx, `SELECT state, attempts, updated_at, COALESCE(last_error, '')
			FROM library_import WHERE download_id = ?`, id).Scan(&st, &attempts, &updated, &lastErr)
		if st == "rejected" {
			continue // held for manual review; a manual import calls Import directly
		}
		if st == "dry_run" && !l.gate.Active(l.versionOf(ctx, id)) {
			continue // planned already; nothing changed that would allow it
		}
		if st == "partial" && l.now().Before(store.FromMillis(updated).Add(partialBackoff(attempts))) {
			continue // some links failed; retried after a back-off (a manual import retries at once)
		}
		if st == "waiting_mount" && strings.HasPrefix(lastErr, UnreadablePrefix) &&
			l.now().Before(store.FromMillis(updated).Add(unreadableBackoff)) {
			continue // its files were unreadable while the mount answered; retried after a back-off
		}
		p, err := l.Import(ctx, id, false)
		if errors.Is(err, ErrUnreadable) {
			l.log.Warn("library: a download's files are unreadable on the mount; retried later, the other imports go on",
				"download", id, "retry_in", unreadableBackoff, "err", err)
			continue
		}
		if errors.Is(err, ErrRootDown) {
			l.log.Warn("library: the version's library root does not answer; the import waits for it, the others go on",
				"download", id, "err", err)
			continue
		}
		if errors.Is(err, ErrMountDown) {
			l.log.Warn("library: mount unhealthy, imports paused", "err", err)
			return done, err
		}
		if errors.Is(err, ErrClaimLost) {
			l.log.Warn("library: import stopped, its claim expired or was taken over", "download", id, "err", err)
			continue
		}
		if errors.Is(err, ErrClaimed) {
			l.log.Debug("library: import claimed by another run", "download", id)
			continue
		}
		if errors.Is(err, ErrLinkChanged) {
			l.log.Info("library: a link of the import is being changed by another run; retried", "download", id,
				"err", err)
			continue
		}
		if errors.Is(err, ErrRemoved) {
			l.log.Info("library: download removed during its import", "download", id, "err", err)
			continue
		}
		if err != nil {
			l.log.Error("library: import failed", "download", id, "err", err)
			continue
		}
		if p != nil && !p.Waiting && !p.DryRun {
			done++
		}
	}
	return done, nil
}

func (l *Library) versionOf(ctx context.Context, id int64) string {
	var k string
	_ = l.st.Reader().QueryRowContext(ctx, `SELECT version_key FROM download WHERE id = ?`, id).Scan(&k)
	return k
}

// Import imports one download. dryRun plans without writing anything at all (not even the database): the
// `theatarr library dry-run` command. Otherwise a version the gate refuses is planned and recorded as a dry run.
func (l *Library) Import(ctx context.Context, id int64, dryRun bool) (*Plan, error) {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.Import(pinned, id, dryRun)
	}
	d, err := loadDownload(ctx, l.st.Reader(), l.pol, id)
	if err != nil {
		return nil, err
	}
	return l.importDownload(ctx, d, dryRun)
}

// PlanNew plans a download an import would make, as a dry run that writes nothing and needs no download row
// (the import API's dry run, and its plan while the gate refuses). The plan has no download id.
func (l *Library) PlanNew(ctx context.Context, n NewImport) (*Plan, error) {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.PlanNew(pinned, n)
	}
	d, err := n.download(ctx, l.st.Reader(), l.pol)
	if err != nil {
		return nil, err
	}
	return l.importDownload(ctx, d, true)
}

func (l *Library) importDownload(ctx context.Context, d *importDownload, dryRun bool) (_ *Plan, rerr error) {
	id := d.ID
	var err error
	v := &d.Version
	plan := &Plan{DownloadID: id, Release: d.ReleaseTitle, Version: v.Key, DryRun: dryRun}
	if v.Policy == nil {
		return nil, errf(id, "version %q is not in the policy", v.Key)
	}
	if v.Policy.PreferSeaDex {
		ctx, err = decide.WithStoredSeaDex(ctx, l.st.Reader())
		if err != nil {
			return nil, err
		}
		ctx = decide.WithSeaDexTitle(decide.WithSeaDexPolicy(ctx, l.pol, v.Policy), v.Title.ID)
	}
	if !dryRun && d.State != ReadyState && d.State != "importing" {
		return nil, errf(id, "state %s, not ready", d.State)
	}
	if !dryRun {
		// One import per download at a time: claimed before planning or any write, released at the end.
		if err := l.claim(ctx, d); err != nil {
			return nil, err
		}
		defer l.unclaim(ctx, d)
		// Runs before the unclaim: the back-off is written under this run's claim.
		defer func() {
			if errors.Is(rerr, ErrMountDown) {
				rerr = l.unreadable(ctx, d, rerr)
			}
		}()
	}
	// A client's explicit episode choice (manual import) is the review: nothing is held.
	if len(d.Manual) == 0 {
		held, allHeld, err := l.animeHold(ctx, d)
		if err != nil {
			return nil, err
		}
		plan.Held = held
		if allHeld {
			return l.holdAnimeReview(ctx, d, plan, dryRun)
		}
	}
	if err := l.gate.CheckVersion(v.Key); err != nil {
		// A root that does not answer is a pause, like the mount (ErrRootDown): planning reads under the root
		// (the links already there), unbounded, so nothing is planned or recorded; the import is retried.
		if Unavailable(err) {
			return nil, errf(id, "%w", err)
		}
		plan.DryRun, plan.Refused = true, err.Error()
	}
	// The plan reads under the root (shadow and library-off versions too, which the gate refuses before their roots),
	// and a filesystem mounted below it may hold the planned paths: each must answer (Gate.Reachable).
	if root := v.Policy.Library; filepath.IsAbs(root) {
		if err := l.gate.Reachable(ctx, filepath.Clean(root)); err != nil {
			return nil, errf(id, "%w", err)
		}
	}
	if l.set.MountPath == "" {
		return nil, errf(id, "library.mount.path is not set")
	}
	if err := l.mountHealthy(ctx); err != nil {
		return nil, err
	}
	if len(d.Manual) == 0 && d.Version.Title.Kind == "series" && l.engine != nil {
		t := decide.Title{ID: d.Version.Title.ID, Kind: policy.KindSeries, Name: d.Version.Title.Name, Year: d.Version.Title.Year,
			SeriesType: d.Version.Title.SeriesType, Runtime: d.Version.Title.Runtime}
		eps := make([]decide.Episode, len(d.Version.Episodes))
		for i, ep := range d.Version.Episodes {
			eps[i] = decide.Episode{ID: ep.ID, Season: ep.Season, Number: ep.Number, Title: ep.Name, AirDate: ep.AirDate}
			if ep.AirDate != "" {
				if tm, err := time.Parse("2006-01-02", ep.AirDate); err == nil {
					eps[i].AirDateUTC = &tm
				}
			}
		}
		if reason, msg := l.engine.EpisodeTitleHold(t, vToDecideVersion(v), eps); reason != "" {
			plan.Waiting = true
			if !dryRun {
				if err := l.markTitleWaiting(ctx, d, msg); err != nil {
					return nil, err
				}
			}
			return plan, nil
		}
	}

	// 1. The release on the mount: the folder a path import named, else found by the hash and names.
	known := append(append([]string{d.MountFolder, d.Name}, d.FolderNames...), d.ContentName, d.ReleaseTitle)
	names := ReleaseFolderNames(d.InfoHash, known...)
	// mountNames are the candidates without zurg's name, to tell when only zurg's name found the folder.
	mountNames := names
	var at Located
	sourced := d.SourcePath != ""
	if sourced {
		var mapTarget func(string) string
		if d.Client != "" {
			mapTarget = l.pol.Clients[d.Client].LocalContentPath // nil-safe: an unknown client maps nothing
		}
		at, err = l.mount.LocateMapped(ctx, d.SourcePath, mapTarget)
		switch {
		case err != nil && d.Client != "" && errors.Is(err, fs.ErrNotExist):
			// A completed client download with no folder of its own (decypharr's multi-season fan-out makes one
			// per season and none for the torrent): found on the mount by hash and names, as import_from: mount.
			sourced = false
		case err != nil:
			return nil, errf(id, "%w", err)
		default:
			names = []string{at.Folder}
		}
	}
	zurgCheck := l.zurg != nil && d.InfoHash != "" && !sourced && len(d.Manual) == 0
	// zurgListed: zurg's listing holds the hash, so its folder is in zurg's release root.
	zurgListed := false
	if zurgCheck {
		// zurg names the folder after its own name for the torrent, which the providers may report otherwise
		// (another case, a video file's name, a cut or different title). Its current name goes first: zurg may
		// rename. Without a listing, or with the hash not listed, the provider's names alone are tried.
		list, err := l.zurg.Torrents(ctx)
		zname, ok := list.Name(d.InfoHash)
		if err == nil && !ok && l.justReady(d) {
			// A torrent zurg listed after the cached listing was read is not in it: read a newer one.
			if fresh, err := l.zurg.FreshTorrents(ctx); err == nil {
				zname, ok = fresh.Name(d.InfoHash)
			}
		}
		if ok {
			names = ReleaseFolderNames(d.InfoHash, append([]string{zname}, known...)...)
		}
		zurgListed = ok
	}
	if !sourced && len(d.Manual) == 0 {
		// A matching hash suffix identifies our torrent even when the bare name holds another one.
		hash := strings.ToLower(d.InfoHash)
		priority := func(name string) int {
			if m := hash6Suffix.FindStringSubmatch(name); m != nil {
				if strings.HasPrefix(hash, m[2]) {
					return 0
				}
				return 2
			}
			return 1
		}
		sort.SliceStable(names, func(i, j int) bool {
			return priority(names[i]) < priority(names[j])
		})
	}
	folder, err := l.mount.FindRelease(ctx, names)
	if err != nil {
		return nil, err
	}
	// zurg confirms the folder is this download's torrent: a bare name can hold another torrent of the same name
	// (this one then shows as "name {hash6}"). Without an answer the mount's stands. A client's explicit path or files
	// are its choice and are not checked.
	for zurgCheck && folder != "" {
		e, ok := l.zurgConfirm(ctx, d.InfoHash, filepath.Base(folder), "")
		if ok {
			break
		}
		l.log.Info("library: release folder holds another torrent; trying its other names", "download", id,
			"folder", folder, "want", strings.ToLower(d.InfoHash), "zurg_hash", e.Hash)
		names = slices.DeleteFunc(names, func(n string) bool { return n == filepath.Base(folder) })
		if folder, err = l.mount.FindRelease(ctx, names); err != nil {
			return nil, err
		}
	}
	if folder != "" && zurgCheck && !slices.Contains(mountNames, filepath.Base(folder)) {
		l.log.Info("library: release folder found by zurg's name for the hash", "download", id,
			"folder", filepath.Base(folder), "name", d.Name)
	}
	if folder == "" {
		plan.Waiting = true
		if !dryRun {
			if zurgListed && !plan.DryRun {
				// zurg has the folder; the mount's cached listing of the root does not show it yet.
				l.forgetReleaseRoot(ctx, id)
			}
			if err := l.markWaiting(ctx, d); err != nil {
				return nil, err
			}
		}
		return plan, nil
	}
	plan.Folder = folder
	files, err := l.mount.VideoFiles(ctx, folder)
	if err != nil {
		return nil, err
	}
	files = selectFiles(d, at, files, plan)
	if len(files) == 0 {
		// Sonarr v5 (DownloadedEpisodesImportService.CheckEmptyResultForIssue): with nothing to import, a
		// potentially dangerous or executable file is the reason, and the download fails (FailDownloads default).
		all, err := l.mount.AllFiles(ctx, folder)
		if err != nil {
			return nil, err
		}
		rels := make([]string, len(all))
		for i, f := range all {
			rels[i] = f.Rel
		}
		if _, msg := router.ImportFileRejection(rels); msg != "" {
			return l.reject(ctx, d, plan, dryRun, msg)
		}
		if len(d.Manual) > 0 || at.Rel != "" {
			return l.holdForReview(ctx, d, plan, dryRun, "none of the selected files is a video file in "+folder)
		}
		return l.holdForReview(ctx, d, plan, dryRun, "no video files in "+folder)
	}

	// 2. Probe and guard.
	resolution := releaseResolution(d.ReleaseTitle, v.Title.Kind, v.Policy.Sonarr)
	labels := claimOf(d.ReleaseTitle, v.Title.Kind, v.Policy.Sonarr)
	for _, f := range files {
		// Probing a large pack can outlast the claim: it is renewed as the files are read.
		if !dryRun {
			if err := l.renewClaim(ctx, d); err != nil {
				return nil, err
			}
		}
		pf := PlannedFile{Path: f.Path, Rel: f.Rel, Size: f.Size}
		if zurgCheck {
			if e, ok := l.zurgConfirm(ctx, d.InfoHash, filepath.Base(folder), f.Rel); e != nil {
				pf.Zurg = &ZurgFileCheck{Hash: e.Hash, Size: e.Size, Confirmed: ok && e.Size == f.Size}
				switch {
				case !ok:
					pf.Reason = "zurg: the file is in torrent " + e.Hash + ", not the download's"
				case e.Size != f.Size:
					pf.Reason = fmt.Sprintf("zurg: the file is %d bytes, the mount shows %d", e.Size, f.Size)
				}
				if !pf.Zurg.Confirmed {
					plan.Rejected = append(plan.Rejected, pf)
					continue
				}
			}
		}
		res, perr := l.mount.ProbeFile(ctx, f.Path, l.set.ProbeMaxBytes, l.probe)
		if errors.Is(perr, ErrMountDown) || (perr != nil && ctx.Err() != nil) {
			return nil, perr // the mount did not answer: no verdict on the file
		}
		if perr != nil {
			pf.Reason = "probe: " + perr.Error()
		} else {
			pf.Probe = res
		}
		plan.Files = append(plan.Files, pf)
	}

	// 3. Which file goes where.
	vn, err := NamingFor(ctx, l.st.Reader(), l.pol, v.Key)
	if err != nil {
		return nil, err
	}
	plan.Naming = vn.Source
	root := filepath.Clean(v.Policy.Library)
	if v.Title.Kind == "movie" {
		err = l.planMovie(ctx, d, vn, root, resolution, labels, plan)
	} else {
		err = l.planSeries(ctx, d, vn, root, resolution, labels, plan)
	}
	if err != nil {
		return nil, err
	}
	if len(plan.Links) == 0 {
		reason := "no file passed the sample guard"
		switch {
		case len(plan.Rejected) == 0:
			reason = "no file matched the download's episodes"
		case allNotUpgrades(plan.Rejected):
			// Sonarr's and Radarr's "Not an upgrade" import: nothing is linked and the release is not taken again.
			reason = "not an upgrade for the existing files"
		}
		if why := firstCollision(plan.Rejected); why != "" {
			// Two titles (or versions) name a file the same: the naming must tell them apart. Nothing is replaced.
			return l.holdForReview(ctx, d, plan, dryRun, "naming collision: "+why)
		}
		if anyZurgUnconfirmed(plan.Rejected) {
			// The mount and zurg disagree about the files: no verdict on the release.
			return l.holdForReview(ctx, d, plan, dryRun, "zurg does not confirm the files are this download's ("+
				firstZurgReason(plan.Rejected)+")")
		}
		if reason == "no file passed the sample guard" {
			for _, f := range plan.Rejected {
				if isMislabel(f.Reason) {
					reason = f.Reason // the blocklist and the Why? say what the release lied about
					break
				}
			}
			return l.reject(ctx, d, plan, dryRun, reason)
		}
		return l.holdForReview(ctx, d, plan, dryRun, reason)
	}
	if dryRun {
		return plan, nil
	}
	if plan.DryRun {
		return plan, l.recordDryRun(ctx, d, plan)
	}
	return plan, l.apply(ctx, d, vn, root, plan)
}

func vToDecideVersion(v *importVersion) decide.Version {
	return decide.Version{ID: v.VersionID, Key: v.Key, Policy: v.Policy}
}

// selectFiles keeps the files a client chose: a manual import's files (each must be in the release), else those
// under the path a path import named. Every file otherwise.
func selectFiles(d *importDownload, at Located, files []MountFile, plan *Plan) []MountFile {
	if len(d.Manual) == 0 && at.Rel == "" {
		return files
	}
	var out []MountFile
	found := map[string]bool{}
	for _, f := range files {
		if len(d.Manual) > 0 {
			if _, ok := d.Manual[f.Rel]; !ok {
				continue
			}
			found[f.Rel] = true
		} else if !at.Holds(f.Rel) {
			continue
		}
		out = append(out, f)
	}
	for rel := range d.Manual {
		if !found[rel] {
			plan.Unmatched = append(plan.Unmatched, rel)
		}
	}
	sort.Strings(plan.Unmatched)
	return out
}

// ErrClaimed: another import holds the download (the periodic pass and a manual import of the same download).
var ErrClaimed = errors.New("library: the download is being imported by another run")

// ErrClaimLost: this import's claim expired and another run took it over; the import stopped (an ErrClaimed too).
var ErrClaimLost = errors.New("the claim expired or was taken over")

// claimFor is how long an import's claim lasts; a claim left by a crash is taken over after it.
const claimFor = 30 * time.Minute

var claimSeq atomic.Int64

// claim takes the download for this import: it must still be ready or importing, and no other live claim may hold
// it. Without the claim the import stops (ErrClaimed) before planning or writing anything.
func (l *Library) claim(ctx context.Context, d *importDownload) error {
	now := l.now()
	owner := fmt.Sprintf("%d/%d", now.UnixNano(), claimSeq.Add(1))
	err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM download WHERE id = ?`, d.ID).Scan(&state); err != nil {
			return err
		}
		if state != ReadyState && state != "importing" {
			return fmt.Errorf("%w: state %s", ErrClaimed, state)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO library_claim (download_id, owner, until) VALUES (?, ?, ?)
			ON CONFLICT (download_id) DO UPDATE SET owner = excluded.owner, until = excluded.until
			WHERE library_claim.until <= ?`, d.ID, owner, store.Millis(now.Add(claimFor)), store.Millis(now))
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrClaimed
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrClaimed) {
			return errf(d.ID, "%w", err)
		}
		return err
	}
	d.claim, d.claimed = owner, now
	return nil
}

// renewClaim extends this import's claim once a third of it has passed since it was taken or last renewed, or
// fails (ErrClaimLost) when another run took it over.
func (l *Library) renewClaim(ctx context.Context, d *importDownload) error {
	now := l.now()
	if now.Sub(d.claimed) < claimFor/3 {
		return nil
	}
	err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE library_claim SET until = ? WHERE download_id = ? AND owner = ?`,
			store.Millis(now.Add(claimFor)), d.ID, d.claim)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return errf(d.ID, "%w: %w", ErrClaimed, ErrClaimLost)
		}
		return nil
	})
	if err == nil {
		d.claimed = now
	}
	return err
}

// unclaim releases the claim, even when ctx is cancelled.
func (l *Library) unclaim(ctx context.Context, d *importDownload) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM library_claim WHERE download_id = ? AND owner = ?`, d.ID, d.claim)
		return err
	}); err != nil {
		l.log.Warn("library: claim not released (it expires)", "download", d.ID, "err", err)
	}
}

// holdsClaim checks, inside a write transaction, that this import still holds its claim. Every outcome an import
// records (imported, partial, failed, rejected, waiting, dry run, held) checks it in the same transaction: a stale
// run, whose claim expired and was taken over, records nothing over the newer run's outcome.
func holdsClaim(ctx context.Context, tx *sql.Tx, d *importDownload) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM library_claim WHERE download_id = ? AND owner = ?`,
		d.ID, d.claim).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return errf(d.ID, "%w: %w", ErrClaimed, ErrClaimLost)
	}
	return nil
}

// partialBackoff is how long the periodic pass waits before retrying a partial import after its attempts.
func partialBackoff(attempts int) time.Duration {
	d := 10 * time.Minute
	for i := 1; i < attempts && d < 6*time.Hour; i++ {
		d *= 2
	}
	return min(d, 6*time.Hour)
}

// PartialRetryAt is when the periodic pass next retries a partial import last updated at updated after attempts.
func PartialRetryAt(updated time.Time, attempts int) time.Time {
	return updated.Add(partialBackoff(attempts))
}

// UnreadablePrefix starts the import error of a download whose files the mount would not read while the mount
// itself answered (an EIO from one debrid link zurg cannot unrestrict, or one stuck read): the periodic pass backs
// it off for unreadableBackoff and carries on with the others. The ready deadline still applies.
const UnreadablePrefix = "file unreadable on the mount: "

// ErrUnreadable is an import stopped by one download's unreadable files on a mount that answers (UnreadablePrefix).
// It wraps the ErrMountDown it came from; Run checks it first.
var ErrUnreadable = errors.New("library: the download's files are unreadable on the mount")

// unreadableBackoff is how long the periodic pass leaves a download whose files were unreadable.
var unreadableBackoff = 10 * time.Minute

// unreadable turns a mount error of one download's import into that download's back-off when the mount itself is
// healthy (its release root answers under the guard). A mount that does not answer stays ErrMountDown: the whole
// pass stops, as before.
func (l *Library) unreadable(ctx context.Context, d *importDownload, err error) error {
	if herr := l.mountHealthy(ctx); herr != nil {
		return err
	}
	if werr := l.markWaitingWithError(ctx, d, UnreadablePrefix+err.Error()); werr != nil {
		return errors.Join(err, werr)
	}
	// Still ErrMountDown for a caller that only knows that (a pause, not a verdict on the file).
	return fmt.Errorf("%w: %w", ErrUnreadable, err)
}

// notOnMount is the import error of a download whose release folder is not on the mount yet.
const notOnMount = "release not on the mount yet"

func (l *Library) markWaiting(ctx context.Context, d *importDownload) error {
	return l.markWaitingWithError(ctx, d, notOnMount)
}

// forgetFloor is the least time between two clears of the release root's cached listing.
const forgetFloor = 3 * time.Second

// rootForget clears the mount's cached listing of the release root (rclone rc vfs/forget), at most once per
// forgetFloor across all imports.
type rootForget struct {
	call func(ctx context.Context, dir string) error
	mu   sync.Mutex
	at   time.Time
}

// forgetReleaseRoot clears rclone's cached listing of the release root when zurg lists a download's torrent but the
// mount's stat did not find its folder: rclone keeps a directory's listing for its dir-cache-time, so a folder zurg
// added since shows only once that expires. Only the listing is dropped (no refresh): the import's next stat reads
// the root again, once. Nothing changes on zurg or the debrid service.
func (l *Library) forgetReleaseRoot(ctx context.Context, id int64) {
	f := l.forget
	if f == nil {
		return
	}
	now := l.now()
	f.mu.Lock()
	if !f.at.IsZero() && now.Sub(f.at) < forgetFloor {
		f.mu.Unlock()
		return
	}
	f.at = now
	f.mu.Unlock()
	if err := f.call(ctx, l.set.AllDir); err != nil {
		l.log.Warn("library: clearing the mount's cached listing of the release root failed", "download", id,
			"dir", l.set.AllDir, "err", err)
		return
	}
	l.log.Debug("library: cleared the mount's cached listing of the release root", "download", id, "dir", l.set.AllDir)
}

// ReadyRecheckWindow is how long after a download turned ready its import is retried quickly while its release is not
// on the mount yet (theatarr serve's re-check, FreshWaiting) and reads zurg's listing afresh (FreshTorrents): a new
// torrent shows on the mount within seconds to about half a minute, so waiting for the next poll interval only
// delays it. Past the window the poll interval and the cached listing apply.
const ReadyRecheckWindow = 2 * time.Minute

// justReady reports whether d turned ready within ReadyRecheckWindow. A ready time in the future (a clock that
// was ahead) is not fresh: it would keep the quick reads going for as long as it stays ahead.
func (l *Library) justReady(d *importDownload) bool {
	if d.ReadyAt.IsZero() {
		return false
	}
	age := l.now().Sub(d.ReadyAt)
	return age >= 0 && age < ReadyRecheckWindow
}

// FreshWaiting reports whether a ready download that turned ready within ReadyRecheckWindow (not in the future)
// waits because its release is not on the mount yet, for a version the gate lets write: theatarr serve then runs the
// next import pass after a short delay instead of at the next poll interval. A gated version's import is only
// planned, so it gains nothing from a quick pass.
func (l *Library) FreshWaiting(ctx context.Context) bool {
	now := l.now()
	rows, err := l.st.Reader().QueryContext(ctx, `SELECT DISTINCT d.version_key FROM library_import li
		JOIN download d ON d.id = li.download_id
		WHERE li.state = 'waiting_mount' AND li.last_error = ? AND d.state = 'ready' AND d.ready_at > ? AND d.ready_at <= ?`,
		notOnMount, store.Millis(now.Add(-ReadyRecheckWindow)), store.Millis(now))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil && l.gate.Active(k) {
			return true
		}
	}
	return false
}

// TitleHoldPrefix starts the import error of a download held for its episode title; the ready timeout skips them.
const TitleHoldPrefix = "episode title hold: "

func (l *Library) markTitleWaiting(ctx context.Context, d *importDownload, reason string) error {
	return l.markWaitingWithError(ctx, d, TitleHoldPrefix+reason)
}

func (l *Library) markWaitingWithError(ctx context.Context, d *importDownload, reason string) error {
	now := l.now()
	return l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := holdsClaim(ctx, tx, d); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO library_import (download_id, state, waiting_since, last_error, attempts, created_at, updated_at)
			VALUES (?, 'waiting_mount', ?, ?, 1, ?, ?)
			ON CONFLICT (download_id) DO UPDATE SET state = 'waiting_mount', last_error = excluded.last_error, attempts = attempts + 1,
			waiting_since = COALESCE(library_import.waiting_since, excluded.waiting_since), updated_at = excluded.updated_at`,
			d.ID, store.Millis(now), reason, store.Millis(now), store.Millis(now))
		if err != nil {
			return err
		}
		var since int64
		var alerted sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT waiting_since, alerted_at FROM library_import WHERE download_id = ?`, d.ID).
			Scan(&since, &alerted); err != nil {
			return err
		}
		if !alerted.Valid && now.Sub(store.FromMillis(since)) >= l.set.WaitTimeout {
			l.log.Warn("library: release not on the mount yet; still waiting (no re-grab)", "download", d.ID,
				"release", d.ReleaseTitle, "waited", now.Sub(store.FromMillis(since)).Round(1e9).String())
			if _, err := tx.ExecContext(ctx, `UPDATE library_import SET alerted_at = ? WHERE download_id = ?`, store.Millis(now), d.ID); err != nil {
				return err
			}
			return l.record(ctx, tx, action{Kind: "alert", Mode: "applied", Version: d.Version.Key, DownloadID: d.ID,
				Detail: reason + " after " + l.set.WaitTimeout.String()})
		}
		return nil
	})
}

// reject fails and blocklists an import rejected by a file guard; nothing is linked or deleted on the provider.
func (l *Library) reject(ctx context.Context, d *importDownload, plan *Plan, dryRun bool, reason string) (*Plan, error) {
	if dryRun {
		return plan, nil
	}
	if plan.DryRun {
		return plan, l.recordDryRun(ctx, d, plan, reason)
	}
	pj, _ := json.Marshal(plan)
	now := store.Millis(l.now())
	l.log.Warn("library: import rejected", "download", d.ID, "release", d.ReleaseTitle, "reason", reason)
	return plan, l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := holdsClaim(ctx, tx, d); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO library_import (download_id, state, mount_folder, last_error, plan, created_at, updated_at)
			VALUES (?, 'rejected', ?, ?, ?, ?, ?) ON CONFLICT (download_id) DO UPDATE SET state = 'rejected',
			mount_folder = excluded.mount_folder, last_error = excluded.last_error, plan = excluded.plan, updated_at = excluded.updated_at`,
			d.ID, plan.Folder, reason, string(pj), now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE download SET state = 'failed', error = ?, updated_at = ? WHERE id = ?`,
			"library: "+reason, now, d.ID); err != nil {
			return err
		}
		why := reason
		if len(plan.Rejected) > 0 && plan.Rejected[0].Reason != reason {
			why += " (" + plan.Rejected[0].Reason + ")"
		}
		if err := router.Blocklist(ctx, tx, d.Version.Key, d.InfoHash, d.Version.Title.ID, d.ID, d.ReleaseTitle,
			why, router.BlocklistLibrary, now); err != nil {
			return err
		}
		return l.record(ctx, tx, action{Kind: "reject", Mode: "applied", Version: d.Version.Key, DownloadID: d.ID,
			Path: plan.Folder, Detail: reason})
	})
}

// holdForReview leaves a non-dangerous import in the queue for manual resolution. Its ready state keeps it in flight
// for wanted decisions, while the library row carries the reason shown by the arr queue and native activity views.
func (l *Library) holdForReview(ctx context.Context, d *importDownload, plan *Plan, dryRun bool, reason string) (*Plan, error) {
	if dryRun {
		return plan, nil
	}
	if plan.DryRun {
		return plan, l.recordDryRun(ctx, d, plan, reason)
	}
	pj, _ := json.Marshal(plan)
	now := store.Millis(l.now())
	l.log.Warn("library: import held for review", "download", d.ID, "release", d.ReleaseTitle, "reason", reason)
	return plan, l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := holdsClaim(ctx, tx, d); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO library_import (download_id, state, mount_folder, last_error, plan, created_at, updated_at)
			VALUES (?, 'rejected', ?, ?, ?, ?, ?) ON CONFLICT (download_id) DO UPDATE SET state = 'rejected',
			mount_folder = excluded.mount_folder, last_error = excluded.last_error, plan = excluded.plan, updated_at = excluded.updated_at`,
			d.ID, plan.Folder, reason, string(pj), now, now); err != nil {
			return err
		}
		return l.record(ctx, tx, action{Kind: "reject", Mode: "applied", Version: d.Version.Key, DownloadID: d.ID,
			Path: plan.Folder, Detail: reason})
	})
}

func (l *Library) recordDryRun(ctx context.Context, d *importDownload, plan *Plan, reason ...string) error {
	if plan.Refused != "" {
		reason = append(reason, plan.Refused)
	}
	pj, _ := json.Marshal(plan)
	now := store.Millis(l.now())
	l.log.Info("library: dry run (gate)", "download", d.ID, "version", d.Version.Key, "links", len(plan.Links),
		"refused", plan.Refused)
	return l.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if err := holdsClaim(ctx, tx, d); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO library_import (download_id, state, mount_folder, last_error, plan, created_at, updated_at)
			VALUES (?, 'dry_run', ?, ?, ?, ?, ?) ON CONFLICT (download_id) DO UPDATE SET state = 'dry_run',
			mount_folder = excluded.mount_folder, last_error = excluded.last_error, plan = excluded.plan, updated_at = excluded.updated_at`,
			d.ID, plan.Folder, strings.Join(reason, "; "), string(pj), now, now); err != nil {
			return err
		}
		for _, lk := range plan.Links {
			if err := l.record(ctx, tx, action{Kind: "link", Mode: "dry_run", Version: d.Version.Key, DownloadID: d.ID,
				Path: lk.Path, Target: lk.Target, Detail: plan.Refused}); err != nil {
				return err
			}
			for _, r := range lk.Replaces {
				if err := l.record(ctx, tx, action{Kind: "replace", Mode: "dry_run", Version: d.Version.Key,
					DownloadID: d.ID, Path: r, Detail: plan.Refused}); err != nil {
					return err
				}
			}
		}
		for _, r := range plan.Rejected {
			if err := l.record(ctx, tx, action{Kind: "reject", Mode: "dry_run", Version: d.Version.Key,
				DownloadID: d.ID, Path: r.Path, Detail: r.Reason}); err != nil {
				return err
			}
		}
		return nil
	})
}

// renameFormats gives the {Custom Formats} names: from the formats the router's decision matched when the download
// stores them, else by deciding the release title again.
func (l *Library) renameFormats(ctx context.Context, d *importDownload) []string {
	version, titleID, release := d.Version.Key, d.Version.Title.ID, d.ReleaseTitle
	if d.HasFormats && l.matched != nil {
		return l.matched(ctx, version, d.Formats)
	}
	if l.formats == nil {
		return nil
	}
	return l.formats(ctx, version, titleID, release)
}

func (l *Library) planMovie(ctx context.Context, d *importDownload, vn *VersionNaming, root string, resolution int, labels labelClaim, plan *Plan) error {
	v := &d.Version
	var pick *PlannedFile
	for i := range plan.Files {
		f := &plan.Files[i]
		reason := sampleCheck(l.set.MinRuntimeRatio, l.set.Resolution, "movie", v.Title.Runtime, false, f, resolution)
		if reason == "" {
			reason = l.mislabel(d, labels, f)
		}
		if reason != "" {
			f.Reason = reason
			plan.Rejected = append(plan.Rejected, *f)
			continue
		}
		if pick == nil {
			pick = f // largest first
		}
	}
	if pick == nil {
		return nil
	}
	facts, err := l.fileFacts(d, plan, pick, nil)
	if err != nil {
		return err
	}
	pick.Facts = &facts
	formats, err := l.fileFormats(ctx, d, pick, facts)
	if err != nil {
		return err
	}
	path, err := l.nameMovie(ctx, v, vn, root, pick.Path, facts, pick.Probe, formats)
	if err != nil {
		return err
	}
	lk := PlannedLink{Path: path, Target: l.mount.ToLink(pick.Path), File: pick.Rel, Quality: facts.Quality,
		probe: pick.Probe, size: pick.Size, facts: facts}
	existing, err := existingFiles(ctx, l.st.Reader(), v.VersionID, v.Title.ID)
	if err != nil {
		return err
	}
	up, err := l.newUpgradeCheck(ctx, d, existing)
	if err != nil {
		return err
	}
	if up != nil {
		why, err := up.check(nil, lk.Target, lk.File, lk.size, facts)
		if err != nil {
			return err
		}
		if why != "" {
			skipNotUpgrade(plan, pick, why)
			return nil
		}
	}
	row, err := rowAtPath(ctx, l.st.Reader(), path, v.VersionID)
	if err != nil {
		return err
	}
	if why := row.taken(v.Title.ID, lk.Target); why != "" {
		l.skipCollision(d, plan, pick, path, why)
		return nil
	}
	for _, e := range existing {
		if e.Path != path {
			lk.Replaces = append(lk.Replaces, e.Path)
			lk.ReplaceIDs = append(lk.ReplaceIDs, e.LinkID)
		}
	}
	// A dead link of the version (its file left the mount) is replaced too: revived later, it would be a second file.
	dead, err := deadFiles(ctx, l.st.Reader(), v.VersionID, v.Title.ID)
	if err != nil {
		return err
	}
	for _, e := range dead {
		if e.Path != path {
			lk.Replaces = append(lk.Replaces, e.Path)
			lk.ReplaceIDs = append(lk.ReplaceIDs, e.LinkID)
		}
	}
	lk.Exists = linkPointsAt(path, lk.Target)
	lk.prev, _ = os.Readlink(path)
	plan.Links = append(plan.Links, lk)
	return nil
}

// anyZurgUnconfirmed: a rejected file is one zurg did not confirm (ZurgFileCheck).
func anyZurgUnconfirmed(files []PlannedFile) bool {
	return firstZurgReason(files) != ""
}

func firstZurgReason(files []PlannedFile) string {
	for _, f := range files {
		if f.Zurg != nil && !f.Zurg.Confirmed {
			return f.Reason
		}
	}
	return ""
}

// firstCollision is the reason of the first rejected file whose path another link holds, or "".
func firstCollision(files []PlannedFile) string {
	for _, f := range files {
		if f.Collision {
			return f.Reason
		}
	}
	return ""
}

// skipCollision records a file whose path holds another title's or version's link, like a guard rejection.
func (l *Library) skipCollision(d *importDownload, plan *Plan, f *PlannedFile, path, why string) {
	f.Reason, f.Collision = path+": "+why, true
	plan.Rejected = append(plan.Rejected, *f)
	l.log.Warn("library: naming collision, the file is not linked", "download", d.ID, "version", d.Version.Key,
		"path", path, "reason", why)
}

// allNotUpgrades: every rejected file was skipped by the upgrade check.
func allNotUpgrades(files []PlannedFile) bool {
	for _, f := range files {
		if !f.NotUpgrade {
			return false
		}
	}
	return len(files) > 0
}

// skipNotUpgrade records a file the upgrade check refused, like a guard rejection.
func skipNotUpgrade(plan *Plan, f *PlannedFile, why string) {
	f.Reason, f.NotUpgrade = why, true
	plan.Rejected = append(plan.Rejected, *f)
}

// readLink is linkPointsAt's readlink (tests watch when it runs).
var readLink = os.Readlink

func linkPointsAt(path, target string) bool {
	t, err := readLink(path)
	return err == nil && t == target
}

func (l *Library) planSeries(ctx context.Context, d *importDownload, vn *VersionNaming, root string, resolution int, labels labelClaim, plan *Plan) error {
	v := &d.Version
	stype := naming.SeriesType(v.Title.SeriesType)
	if stype == "" {
		stype = naming.SeriesStandard
	}
	byNumber := map[[2]int]episodeInfo{}
	byAbs := map[int]episodeInfo{}
	for _, e := range v.Episodes {
		byNumber[[2]int{e.Season, e.Number}] = e
		if e.Absolute != nil {
			byAbs[*e.Absolute] = e
		}
	}
	ver, err := sonarr.ParseVersion(v.Policy.Sonarr)
	if err != nil {
		return errf(d.ID, "%w", err)
	}
	existing, err := existingFiles(ctx, l.st.Reader(), v.VersionID, v.Title.ID)
	if err != nil {
		return err
	}
	up, err := l.newUpgradeCheck(ctx, d, existing)
	if err != nil {
		return err
	}
	res, err := l.newResolver(ctx, d, len(plan.Files) > 1)
	if err != nil {
		return err
	}
	taken := map[int64]bool{}
	for _, i := range res.order(plan.Files) {
		f := &plan.Files[i]
		eps, how, unresolved, err := res.episodes(f.Rel, taken)
		if err != nil {
			return err
		}
		if unresolved != "" {
			plan.Unmatched = append(plan.Unmatched, f.Rel)
			if plan.Unresolved == nil {
				plan.Unresolved = map[string]string{}
			}
			plan.Unresolved[f.Rel] = unresolved
			continue
		}
		if len(eps) == 0 && how != HowManual && unresolved == "" {
			info := ver.ParsePath(f.Rel)
			if info == nil {
				info = ver.ParseTitle(filepath.Base(f.Rel))
			}
			if info != nil {
				how = HowNumber
				for _, n := range info.EpisodeNumbers {
					if e, ok := byNumber[[2]int{info.SeasonNumber, n}]; ok && !taken[e.ID] {
						eps = append(eps, e)
					}
				}
				if len(eps) == 0 && stype == naming.SeriesAnime {
					how = HowAbsolute
					for _, n := range info.AbsoluteEpisodeNumbers {
						if e, ok := byAbs[n]; ok && !taken[e.ID] {
							eps = append(eps, e)
						}
					}
				}
			}
		}
		if len(eps) == 0 {
			plan.Unmatched = append(plan.Unmatched, f.Rel)
			continue
		}
		runtime, special := 0, false
		for _, e := range eps {
			r := e.Runtime
			if r == 0 {
				r = v.Title.Runtime
			}
			runtime += r
			special = special || e.Season == 0
		}
		reason := sampleCheck(l.set.MinRuntimeRatio, l.set.Resolution, "series", runtime, special, f, resolution)
		if reason == "" && !special { // a special in a pack is often a disc extra, in SD
			reason = l.mislabel(d, labels, f)
		}
		if reason != "" {
			f.Reason = reason
			for _, e := range eps {
				f.Episodes = append(f.Episodes, episodeLabel(e))
			}
			plan.Rejected = append(plan.Rejected, *f)
			continue
		}
		target := l.mount.ToLink(f.Path)
		var titles []string
		for _, e := range eps {
			titles = append(titles, e.Name)
		}
		facts, err := l.fileFacts(d, plan, f, titles)
		if err != nil {
			return err
		}
		f.Facts = &facts
		if up != nil {
			var ids []int64
			for _, e := range eps {
				ids = append(ids, e.ID)
			}
			why, err := up.check(ids, target, f.Rel, f.Size, facts)
			if err != nil {
				return err
			}
			if why != "" {
				// Not linked, and its episodes are not taken: another file of the release may still fill them.
				skipNotUpgrade(plan, f, why)
				continue
			}
		}
		var ids []int64
		for _, e := range eps {
			ids = append(ids, e.ID)
			taken[e.ID] = true
		}
		formats, err := l.fileFormats(ctx, d, f, facts)
		if err != nil {
			return err
		}
		path, err := l.nameSeries(ctx, v, vn, root, f.Path, facts, f.Probe, formats, eps)
		if err != nil {
			return err
		}
		row, err := rowAtPath(ctx, l.st.Reader(), path, v.VersionID)
		if err != nil {
			return err
		}
		if why := row.taken(v.Title.ID, target); why != "" {
			l.skipCollision(d, plan, f, path, why)
			continue
		}
		lk := PlannedLink{Path: path, Target: target, File: f.Rel, Episodes: ids, How: how, Quality: facts.Quality,
			probe: f.Probe, size: f.Size, facts: facts}
		lk.Exists = linkPointsAt(path, lk.Target)
		lk.prev, _ = os.Readlink(path)
		plan.Links = append(plan.Links, lk)
	}
	planSeriesReplacements(plan, existing)
	dead, err := deadFiles(ctx, l.st.Reader(), v.VersionID, v.Title.ID)
	if err != nil {
		return err
	}
	planDeadReplacements(plan, dead)
	return nil
}

func overlaps(a, b []int64) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// releaseResolution is the release title's parsed resolution (0 when unknown).
func releaseResolution(title, kind, sonarrVersion string) int {
	if kind == "movie" {
		if p := radarr.ParseMovieTitle(title, false); p != nil {
			return p.Quality.Quality.Resolution
		}
		return 0
	}
	ver, _ := sonarr.ParseVersion(sonarrVersion)
	if p := ver.ParseTitle(title); p != nil {
		return p.Quality.Quality.Resolution
	}
	return 0
}

// sampleCheck is the sample and fake guard (docs/design/library.md): the arrs' DetectSample runtime rule, plus a file
// shorter than minRatio of the known runtime, plus a file whose resolution is two or more steps under the
// release's. It returns why the file is rejected, or "".
func sampleCheck(minRatio float64, checkRes bool, kind string, expectedMin int, special bool, f *PlannedFile, releaseRes int) string {
	ext := strings.ToLower(filepath.Ext(f.Path))
	if ext == ".flv" || ext == ".strm" || special || (kind == "movie" && (ext == ".iso" || ext == ".img" || ext == ".m2ts")) {
		return ""
	}
	if f.Probe == nil {
		return "" // indeterminate, as the arr treats a file ffprobe can't read
	}
	rt := f.Probe.RunTimeSeconds
	if rt <= 0 {
		return "runtime of 0: not a valid video file"
	}
	expected := expectedMin
	if kind == "series" && expected == 0 {
		expected = 45
	}
	if minimum := arrMinimumRuntime(expected); rt < float64(minimum) {
		return fmt.Sprintf("sample: runtime %.0fs is under %ds", rt, minimum)
	}
	if expectedMin > 0 && minRatio > 0 && rt < minRatio*float64(expectedMin)*60 {
		return fmt.Sprintf("runtime %.0f min is under %.0f%% of the expected %d min", rt/60, minRatio*100, expectedMin)
	}
	if checkRes && releaseRes > 0 && f.Probe.Video != nil {
		if got := resolutionStep(f.Probe.Video.Width, f.Probe.Video.Height); got > 0 && got <= stepOf(releaseRes)-2 {
			return fmt.Sprintf("resolution %dx%d contradicts the release's %dp", f.Probe.Video.Width, f.Probe.Video.Height, releaseRes)
		}
	}
	return ""
}

// mislabel runs the mislabel guard (mislabel.go) on a file sampleCheck passed and returns its refusal, recording a
// note on the file. A client's explicit files (manual import) are only noted: the choice is the review.
func (l *Library) mislabel(d *importDownload, release labelClaim, f *PlannedFile) string {
	if !l.set.Mislabel {
		return ""
	}
	refuse, note := mislabelCheck(fileClaim(release, f, d.Version.Title.Kind, d.Version.Policy.Sonarr), f)
	if refuse != "" && len(d.Manual) > 0 {
		refuse, note = "", "not refused (manual import): "+refuse
	}
	f.Note = note
	return refuse
}

// arrMinimumRuntime is DetectSample.GetMinimumAllowedRuntime, in seconds.
func arrMinimumRuntime(runtimeMin int) int {
	switch {
	case runtimeMin <= 3:
		return 15
	case runtimeMin <= 10:
		return 90
	case runtimeMin <= 30:
		return 300
	}
	return 600
}

func stepOf(res int) int {
	switch {
	case res >= 2160:
		return 4
	case res >= 1080:
		return 3
	case res >= 720:
		return 2
	case res > 0:
		return 1
	}
	return 0
}

// resolutionStep classifies a probed frame size the way release names count it (by width first, for scope films).
func resolutionStep(w, h int) int {
	switch {
	case w >= 3200 || h >= 1800:
		return 4
	case w >= 1800 || h >= 1000:
		return 3
	case w >= 1200 || h >= 700:
		return 2
	case w > 0 || h > 0:
		return 1
	}
	return 0
}
