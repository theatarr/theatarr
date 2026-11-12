package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library/naming"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Adopt by copy (docs/parallel.md): a parallel version (versions[k].follows) starts from Theatarr's own links to the files
// its followed version's arr has now. Each arr file (version_file of the owning instance) is a symlink in the arr's
// root; its target, on the zurg mount, gets a second link under the parallel
// version's root with the parallel version's naming. Nothing is grabbed, read beyond a stat, or renamed; the arr's
// files and roots are only read.
//
// The link goes through the library's writer (the gate: library.enabled, the version active and listed, the path
// under its roots, never into a mount), never over an existing path, and is recorded as an Theatarr link (link,
// link_version, link_episode, file_metadata from the arr's file record) with a library_action row. The version and
// episode states follow from the links as after an import. A file whose link dangles, whose target is on no mount,
// that is not a symlink, or whose name is taken by something else is skipped and reported. A second run finds the
// links in place and makes nothing new.

// AdoptJobKind runs Adopt for one parallel version inside theatarr serve (payload AdoptJob).
const AdoptJobKind = "library.adopt"

// AdoptJob is a library.adopt job's payload.
type AdoptJob struct {
	Version string `json:"version"`
	Limit   int    `json:"limit,omitempty"`
	// Refresh runs Refresh (the adopted links follow the arr's upgrades) instead of Adopt.
	Refresh bool `json:"refresh,omitempty"`
}

// AdoptOptions say which parallel version to start and how.
type AdoptOptions struct {
	Version string
	// Titles limits adoption to changed arr titles. Nil means every title; an empty slice means none.
	Titles []int64
	// DryRun plans and reports without writing anything, to disk or database, whatever the gate says.
	DryRun bool
	// Limit stops after this many new links (0: no limit).
	Limit int
}

// AdoptReport is what an adopt did, or would do.
type AdoptReport struct {
	Version  string `json:"version"`
	Follows  string `json:"follows"`
	DryRun   bool   `json:"dry_run"`
	Naming   string `json:"naming"`
	Files    int    `json:"files"`  // the followed version's arr files looked at
	Linked   int    `json:"linked"` // new links (in a dry run: links it would make)
	Existing int    `json:"existing"`
	// HasFile counts files skipped because the parallel version has its own file for them already.
	HasFile int `json:"has_file"`
	// MissingEpisodes counts uncovered episodes in packs skipped to preserve the parallel version's files.
	MissingEpisodes int `json:"missing_episodes"`
	PartialPacks    int `json:"partial_packs"`
	// Skipped counts the files not linked, by reason.
	Skipped map[string]int `json:"skipped"`
	// Problems are the skipped files, at most 200.
	Problems []AdoptProblem `json:"problems,omitempty"`
	// Links are the links made (or planned), at most 200.
	Links   []AdoptLink `json:"links,omitempty"`
	Limited bool        `json:"limited,omitempty"` // stopped at the limit
}

// AdoptProblem is one arr file not adopted.
type AdoptProblem struct {
	Path            string  `json:"path"`
	Reason          string  `json:"reason"`
	Detail          string  `json:"detail,omitempty"`
	MissingEpisodes []int64 `json:"missing_episodes,omitempty"`
}

// AdoptLink is one link made (or planned).
type AdoptLink struct {
	From   string `json:"from"` // the arr's file
	Path   string `json:"path"`
	Target string `json:"target"`
}

// Reasons a file is skipped.
const (
	AdoptMissing    = "arr_file_missing"
	AdoptNotLink    = "not_a_symlink"
	AdoptOffMount   = "target_off_mount"
	AdoptDangling   = "dangling"
	AdoptConflict   = "path_taken"
	AdoptNoVersion  = "no_parallel_version"
	AdoptNoEpisodes = "no_episodes"
	AdoptNaming     = "naming"
	AdoptHasFile    = "has_file"
)

const adoptListMax = 200

func (r *AdoptReport) skip(p, reason, detail string) {
	r.Skipped[reason]++
	if len(r.Problems) < adoptListMax {
		r.Problems = append(r.Problems, AdoptProblem{Path: p, Reason: reason, Detail: detail})
	}
}

// adoptFile is one arr file of the followed version with what naming and the link row need.
type adoptFile struct {
	id, titleID, followerID                                  int64
	instance, path, rel, quality, group, scene, edition, mi  string
	orig, release, langs, releaseType, hash, titleName, imdb string
	stype, arrFolder                                         string
	rev, year, tmdb, tvdb, flags                             int
	size                                                     int64
	score                                                    sql.NullInt64
	cutoffNotMet                                             bool
}

// Adopt starts a parallel version from links of its own to its followed version's arr files.
func (l *Library) Adopt(ctx context.Context, o AdoptOptions) (*AdoptReport, error) {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.Adopt(pinned, o)
	}
	ctx = withCutoffTitles(ctx)
	ctx, err := decide.WithStoredSeaDex(ctx, l.st.Reader())
	if err != nil {
		return nil, err
	}
	vp := l.pol.Versions[o.Version]
	if vp == nil {
		return nil, fmt.Errorf("library: adopt: unknown library %q", o.Version)
	}
	if vp.Follows == "" {
		return nil, fmt.Errorf("library: adopt: %s is not a parallel version (versions.%s.follows)", o.Version, o.Version)
	}
	rep := &AdoptReport{Version: o.Version, Follows: vp.Follows, DryRun: o.DryRun, Skipped: map[string]int{}}
	if err := l.followGate(ctx, o.Version, o.DryRun); err != nil {
		return nil, fmt.Errorf("library: adopt %s: %w", o.Version, err)
	}
	vn, err := NamingFor(ctx, l.st.Reader(), l.pol, o.Version)
	if err != nil {
		return nil, err
	}
	rep.Naming = vn.Source
	root := filepath.Clean(vp.Library)
	if l.set.MountPath != "" {
		if err := l.mountHealthy(ctx); err != nil {
			return nil, fmt.Errorf("library: adopt: %w", err)
		}
	}
	files, err := l.adoptFiles(ctx, vp.Follows, o.Version, o.Titles)
	if err != nil {
		return nil, err
	}
	dirs := map[string]bool{}
	for i := range files {
		if o.Limit > 0 && rep.Linked >= o.Limit {
			rep.Limited = true
			break
		}
		f := &files[i]
		rep.Files++
		dir, err := l.adoptOne(ctx, o, vp, vn, root, f, rep)
		if err != nil {
			return rep, err
		}
		if dir != "" {
			dirs[dir] = true
		}
	}
	if !o.DryRun {
		for _, d := range sortedKeys(dirs) {
			if err := l.EnqueueScan(ctx, o.Version, d); err != nil {
				l.log.Warn("library: plex scan not queued", "dir", d, "err", err)
			}
		}
		l.log.Info("library: adopted", "version", o.Version, "follows", vp.Follows, "files", rep.Files,
			"linked", rep.Linked, "existing", rep.Existing, "skipped", rep.Skipped)
	}
	return rep, nil
}

// adoptFiles reads the followed version's arr files (the owning instance's), with the parallel version's id.
func (l *Library) adoptFiles(ctx context.Context, followed, follower string, titles []int64) ([]adoptFile, error) {
	if titles != nil && len(titles) == 0 {
		return nil, nil
	}
	query := `
		SELECT vf.id, vf.title_id, COALESCE((SELECT id FROM version WHERE title_id = vf.title_id AND policy_key = ?), 0),
		       vf.instance, vf.path, vf.relative_path, vf.quality, vf.quality_revision, vf.release_group, vf.scene_name,
		       vf.edition, vf.media_info, vf.original_file_path, COALESCE(vf.release_title, ''), vf.languages,
		       COALESCE(vf.release_type, ''), COALESCE(c.info_hash, ''), vf.size_bytes, vf.indexer_flags,
		       vf.custom_format_score, vf.cutoff_not_met, t.name, COALESCE(t.year, 0), COALESCE(t.imdb_id, ''),
		       COALESCE(t.tmdb_id, 0), COALESCE(t.tvdb_id, 0),
		       ` + store.SeriesTypeSQL("t") + `, COALESCE(a.path, '')
		FROM version_file vf JOIN version v ON v.id = vf.version_id
		JOIN version_source vs ON vs.version_id = vf.version_id AND vs.instance = vf.instance
		JOIN title t ON t.id = vf.title_id
		LEFT JOIN arr_title a ON a.instance = vf.instance AND a.version_id = vf.version_id
		LEFT JOIN content c ON c.id = vf.content_id
		WHERE v.policy_key = ?`
	args := []any{follower, followed}
	if titles != nil {
		query += ` AND vf.title_id IN (` + store.Placeholders(len(titles)) + `)`
		for _, id := range titles {
			args = append(args, id)
		}
	}
	rows, err := l.st.Reader().QueryContext(ctx, query+` ORDER BY vf.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []adoptFile
	for rows.Next() {
		var f adoptFile
		if err := rows.Scan(&f.id, &f.titleID, &f.followerID, &f.instance, &f.path, &f.rel, &f.quality, &f.rev,
			&f.group, &f.scene, &f.edition, &f.mi, &f.orig, &f.release, &f.langs, &f.releaseType, &f.hash, &f.size,
			&f.flags, &f.score, &f.cutoffNotMet, &f.titleName, &f.year, &f.imdb, &f.tmdb, &f.tvdb, &f.stype,
			&f.arrFolder); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// adoptOne links one arr file, or reports why not. It returns the folder to scan, "" for none.
func (l *Library) adoptOne(ctx context.Context, o AdoptOptions, vp *policy.VersionPolicy, vn *VersionNaming, root string,
	f *adoptFile, rep *AdoptReport) (string, error) {
	if f.followerID == 0 {
		rep.skip(f.path, AdoptNoVersion, "the arr import has not given this title the parallel version yet")
		return "", nil
	}
	// The arr's file: a symlink in the arr's own root (a local directory, read with lstat and readlink only,
	// bounded: readLinkBounded).
	ar, err := readLinkBounded(ctx, f.path)
	if err != nil {
		return "", fmt.Errorf("library: adopt: %w", err)
	}
	if errors.Is(ar.lerr, fs.ErrNotExist) {
		rep.skip(f.path, AdoptMissing, "")
		return "", nil
	}
	if ar.lerr != nil {
		rep.skip(f.path, AdoptMissing, ar.lerr.Error())
		return "", nil
	}
	if ar.fi.Mode()&os.ModeSymlink == 0 {
		rep.skip(f.path, AdoptNotLink, "a regular file: Theatarr links mount content only")
		return "", nil
	}
	if ar.rerr != nil {
		rep.skip(f.path, AdoptNotLink, ar.rerr.Error())
		return "", nil
	}
	target := ar.target
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(f.path), target)
	}
	target = filepath.Clean(target)
	read, ok := l.adoptTarget(target)
	if !ok {
		rep.skip(f.path, AdoptOffMount, target)
		return "", nil
	}
	st, err := l.mount.Stat(ctx, read)
	if errors.Is(err, ErrMountDown) {
		return "", fmt.Errorf("library: adopt: %w", err)
	}
	if err != nil || !st.Mode().IsRegular() {
		detail := target
		if err != nil {
			detail = err.Error()
		}
		rep.skip(f.path, AdoptDangling, detail)
		return "", nil
	}

	episodes, neps, err := adoptEpisodes(ctx, l.st.Reader(), f.id)
	if err != nil {
		return "", err
	}
	if vp.Kind == policy.KindSeries && len(episodes) == 0 {
		rep.skip(f.path, AdoptNoEpisodes, "")
		return "", nil
	}
	p, err := l.adoptPath(ctx, vp, vn, root, f, neps)
	if err != nil {
		rep.skip(f.path, AdoptNaming, err.Error())
		return "", nil
	}

	// The parallel version's own files win: one Theatarr already linked for the title (or these episodes) stays.
	existing, err := existingFiles(ctx, l.st.Reader(), f.followerID, f.titleID)
	if err != nil {
		return "", err
	}
	recorded := false
	hasFile := false
	for _, e := range existing {
		if e.Path == p {
			recorded = true
			continue
		}
		if vp.Kind != policy.KindSeries || overlaps(e.Episodes, episodes) {
			hasFile = true
		}
	}
	if hasFile {
		rep.HasFile++
		if vp.Kind == policy.KindSeries {
			if err := l.adoptUncovered(ctx, f.path, existing, episodes, rep); err != nil {
				return "", err
			}
		}
		return "", nil
	}
	// The new link's path is held under its link lease (linklock.go) through the commit, and the torrent delete
	// fence from before the symlink is made through its row (refFence). A delete it waited for may
	// have taken the target: it is checked again.
	if !o.DryRun {
		lctx, unlock, err := l.lockLinks(ctx, p)
		defer unlock()
		if err != nil {
			rep.skip(f.path, AdoptConflict, err.Error())
			return "", nil
		}
		fctx, waited, release, err := l.refFence(lctx)
		defer release()
		if err != nil {
			rep.skip(f.path, AdoptConflict, err.Error())
			return "", nil
		}
		ctx = fctx
		if waited {
			if gone, err := l.targetGone(ctx, target); err != nil {
				return "", fmt.Errorf("library: adopt: %w", err)
			} else if gone {
				rep.skip(f.path, AdoptDangling, target+" (gone after a torrent delete)")
				return "", nil
			}
		}
	}
	if row, err := rowAtPath(ctx, l.st.Reader(), p, f.followerID); err != nil {
		return "", err
	} else if why := row.taken(f.titleID, target); why != "" {
		rep.skip(f.path, AdoptConflict, p+": "+why)
		return "", nil
	}
	// A row an import made (download_id set) is the version's own file, whatever became of its symlink: adopt never
	// takes it over, which would make it an adopted link the follower refresh moves.
	if own, err := importedRowAt(ctx, l.st.Reader(), p); err != nil {
		return "", err
	} else if own != 0 {
		rep.skip(f.path, AdoptConflict, fmt.Sprintf("%s: the path holds link %d, imported by the version itself", p, own))
		return "", nil
	}
	pr, err := readLinkBounded(ctx, p)
	if err != nil {
		return "", fmt.Errorf("library: adopt: %w", err)
	}
	if pr.lerr == nil {
		if pr.fi.Mode()&os.ModeSymlink == 0 || pr.rerr != nil || pr.target != target {
			rep.skip(f.path, AdoptConflict, p)
			return "", nil
		}
		if recorded {
			rep.Existing++
			return "", nil
		}
		// In place but not recorded (a run stopped between the link and its row): record it now.
	} else if !errors.Is(pr.lerr, fs.ErrNotExist) {
		rep.skip(f.path, AdoptConflict, pr.lerr.Error())
		return "", nil
	} else if !o.DryRun {
		if err := jobs.Fence(ctx); err != nil {
			return "", fmt.Errorf("library: adopt: stopped before a link: %w", err)
		}
		if err := l.w.symlink(o.Version, target, p); err != nil {
			_ = l.record(ctx, nil, action{Kind: "link", Mode: modeOf(err), Version: o.Version, Path: p, Target: target,
				Detail: "adopt: " + err.Error()})
			return "", fmt.Errorf("library: adopt %s: %w", f.path, err)
		}
	}
	rep.Linked++
	if len(rep.Links) < adoptListMax {
		rep.Links = append(rep.Links, AdoptLink{From: f.path, Path: p, Target: target})
	}
	if o.DryRun {
		return "", nil
	}
	if err := l.recordAdopted(ctx, o.Version, vp, f, p, target, episodes, "", nil, nil); err != nil {
		return "", err
	}
	return filepath.Dir(p), nil
}

// adoptUncovered reports the holes left when any overlap protects a parallel version's own files.
func (l *Library) adoptUncovered(ctx context.Context, path string, existing []existingFile, episodes []int64, rep *AdoptReport) error {
	covered := map[int64]bool{}
	for _, e := range existing {
		read, ok := l.adoptTarget(e.Target)
		if e.LinkID == 0 || !ok {
			continue
		}
		st, err := l.mount.Stat(ctx, read)
		if errors.Is(err, ErrMountDown) {
			return err
		}
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		for _, ep := range e.Episodes {
			covered[ep] = true
		}
	}
	var missing []int64
	for _, ep := range episodes {
		if !covered[ep] {
			missing = append(missing, ep)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	rep.MissingEpisodes += len(missing)
	if len(missing) < len(episodes) {
		rep.PartialPacks++
	}
	sample := missing[:min(len(missing), adoptListMax)]
	rep.Skipped[AdoptHasFile]++
	if len(rep.Problems) < adoptListMax {
		rep.Problems = append(rep.Problems, AdoptProblem{Path: path, Reason: AdoptHasFile,
			Detail:          fmt.Sprintf("kept own files; %d uncovered episodes (ids, at most %d): %v", len(missing), adoptListMax, sample),
			MissingEpisodes: sample})
	}
	return nil
}

// adoptTarget maps a link target to the path read here: only targets on the zurg mount (as links carry it) are
// adopted; anything else is off the mount.
func (l *Library) adoptTarget(target string) (string, bool) {
	if l.set.MountPath == "" {
		return "", false
	}
	return l.mount.FromLink(target)
}

// adoptEpisodes reads the episodes an arr file holds: our ids and naming's view of them.
func adoptEpisodes(ctx context.Context, db *sql.DB, fileID int64) ([]int64, []naming.Episode, error) {
	rows, err := db.QueryContext(ctx, `SELECT episode_id FROM version_file_episode WHERE version_file_id = ?
		ORDER BY episode_id`, fileID)
	if err != nil {
		return nil, nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return nil, nil, err
	}
	eps, err := loadFileEpisodes(ctx, db, fileID)
	return ids, eps, err
}

// adoptPath names the file under the parallel version's root: the folder the version uses for the title already,
// else the arr's folder name, else the naming's; the file name from the arr's file record (quality, group, edition,
// the arr's media info, custom formats read back from the arr's name or decided from its release title).
func (l *Library) adoptPath(ctx context.Context, vp *policy.VersionPolicy, vn *VersionNaming, root string, f *adoptFile,
	eps []naming.Episode) (string, error) {
	kind := string(vp.Kind)
	folder, err := titleFolder(ctx, l.st.Reader(), f.followerID, f.titleID, kind, root)
	if err != nil {
		return "", err
	}
	if folder == "" && f.arrFolder != "" {
		folder = filepath.Base(filepath.Clean(f.arrFolder))
	}
	q := naming.Quality{Title: vn.QualityTitle(f.quality), Version: max(f.rev, 1)}
	ext := path.Ext(f.path)
	title := f.scene
	if title == "" {
		title = f.release
	}
	cfs := l.adoptFormats(ctx, vp.Follows, f.titleID, path.Base(f.path), q.Title+properSuffix(f.rev, f.stype), title)
	media := mediaInfoFromAPI(f.mi, f.scene, f.orig, f.rel)
	if vp.Kind == policy.KindMovie {
		m := &naming.Movie{Title: f.titleName, Year: f.year, ImdbID: f.imdb, TmdbID: f.tmdb}
		if folder == "" {
			if folder, err = naming.MovieFolder(&vn.Config, m); err != nil {
				return "", err
			}
		}
		name, err := naming.MovieFileName(&vn.Config, m, &naming.MovieFile{Quality: q, ReleaseGroup: f.group,
			Edition: f.edition, SceneName: f.scene, RelativePath: f.rel, Path: f.path, MediaInfo: media, CustomFormats: cfs})
		if err != nil {
			return "", err
		}
		return filepath.Join(root, folder, name+ext), nil
	}
	st := naming.SeriesType(f.stype)
	if st == "" {
		st = naming.SeriesStandard
	}
	s := &naming.Series{Title: f.titleName, Year: f.year, Type: st, ImdbID: f.imdb, TvdbID: f.tvdb, TmdbID: f.tmdb}
	if folder == "" {
		if folder, err = naming.SeriesFolder(&vn.Config, s); err != nil {
			return "", err
		}
	}
	season, err := naming.SeasonFolder(&vn.Config, s, eps[0].Season)
	if err != nil {
		return "", err
	}
	seasonPath := filepath.Join(root, folder, season)
	name, err := naming.SeriesFileName(&vn.Config, s, eps, &naming.EpisodeFile{ID: f.id, Quality: q,
		ReleaseGroup: f.group, SceneName: f.scene, RelativePath: f.rel, Path: f.path, MediaInfo: media,
		CustomFormats: cfs}, ext, seasonPath)
	if err != nil {
		return "", err
	}
	if strings.ContainsRune(name, filepath.Separator) {
		return "", fmt.Errorf("file name %q holds a path separator", name)
	}
	return filepath.Join(seasonPath, name), nil
}

// adoptFormats gives an adopted file's {Custom Formats} names: the arr's own, from its file name, else those decided
// from its release title for the followed version. A rename of the adopted link takes them the same way.
func (l *Library) adoptFormats(ctx context.Context, follows string, titleID int64, base, qualityFull, title string) []string {
	cfs := cfFromName(base, qualityFull)
	if cfs == nil && l.formats != nil && title != "" {
		cfs = l.formats(ctx, follows, titleID, title)
	}
	return cfs
}

// properSuffix is what the arr appends to {Quality Full} for a revision: " Proper", or " v<n>" for anime.
func properSuffix(rev int, stype string) string {
	if rev <= 1 {
		return ""
	}
	if stype == "anime" {
		return " v" + strconv.Itoa(rev)
	}
	return " Proper"
}

// importedRowAt is the id of the link row at path an import made (download_id set), 0 for none.
func importedRowAt(ctx context.Context, db *sql.DB, path string) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, `SELECT id FROM link WHERE path = ? AND download_id IS NOT NULL`, path).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// recordAdopted records an adopted link as an Theatarr link of the parallel version, with the arr file's record as its
// file metadata (what decide compares releases against), and sets the version's or episodes' state. note is added
// to the action's detail; then, in the same transaction, runs (refresh retires the link it replaces), when set.
func (l *Library) recordAdopted(ctx context.Context, version string, vp *policy.VersionPolicy, f *adoptFile, p, target string,
	episodes []int64, note string, check func(tx *sql.Tx) error, then func(tx *sql.Tx, now int64) error) error {
	meta := decide.ExistingFile{Quality: f.quality, Revision: max(f.rev, 1), SceneName: f.scene,
		OriginalFilePath: f.orig, RelativePath: f.rel, ReleaseGroup: f.group, Edition: f.edition, Size: f.size,
		IndexerFlags: f.flags, InfoHash: strings.ToLower(f.hash), ReleaseType: f.releaseType}
	if meta.SceneName == "" {
		meta.SceneName = f.release
	}
	_ = json.Unmarshal([]byte(f.langs), &meta.Languages)
	if f.score.Valid {
		s := int(f.score.Int64)
		meta.Score = &s
	}
	v := &importVersion{VersionID: f.followerID, Key: version, Policy: vp, Quality: f.quality,
		Title: titleInfo{ID: f.titleID, Kind: string(vp.Kind)}}
	state := "cutoff_met"
	if f.cutoffNotMet {
		state = "filled"
	}
	// The cutoff as an Theatarr import judges it (quality and custom-format score of the file); the arr's own flag only
	// when the engine cannot judge.
	if l.engine != nil {
		t, err := l.cutoffTitle(ctx, f.titleID, f.followerID, vp)
		if err == nil && vp.PreferSeaDex {
			ctx, err = decide.WithStoredSeaDex(ctx, l.st.Reader())
			if err != nil {
				return err
			}
			ctx = decide.WithSeaDexTitle(decide.WithSeaDexPolicy(ctx, l.pol, vp), f.titleID)
			err = decide.SeaDexError(ctx)
			if err == nil {
				decide.ResolveSeaDexFileEpisodes(ctx, t, &meta, episodes)
			}
		}
		if err == nil {
			state, err = l.importedState(v, t, &meta)
		}
		if err != nil {
			state = map[bool]string{true: "filled", false: "cutoff_met"}[f.cutoffNotMet]
			l.log.Warn("library: adopt: cutoff from the arr's flag", "file", f.path, "err", err)
		}
	}
	metaJSON, err := json.Marshal(&meta)
	if err != nil {
		return err
	}
	var release any
	if f.release != "" {
		release = f.release
	} else if f.scene != "" {
		release = f.scene
	}
	var score any
	if f.score.Valid {
		score = f.score.Int64
	}
	now := store.Millis(l.now())
	return l.refWriteTx(ctx, func(tx *sql.Tx) error {
		if err := l.linksHeldTx(ctx, tx); err != nil {
			return err
		}
		if check != nil {
			if err := check(tx); err != nil {
				return err
			}
		}
		// As an import's (apply.go): the row at the path is this title's, and its title never changes.
		if err := takePathTx(ctx, tx, p, f.titleID, f.followerID, target); err != nil {
			return err
		}
		var linkID int64
		// adopted_from is the provenance the follower refresh and the router's offer sources read (00077). The
		// offer sweep's stored adopted_source (00114) described the file recorded before: a changed file at the same
		// path (a refresh can keep the arr path while its target or hash changes) clears it, to be read again.
		// adopted_media (00117) is the arr's media info of the file, shown on the title page.
		err := tx.QueryRowContext(ctx, `INSERT INTO link (path, target, kind, title_id, release_title, quality, score,
				file_metadata, adopted_from, adopted_media, state, last_checked_at, created_at, updated_at)
			VALUES (?, ?, 'symlink', ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?)
			ON CONFLICT (path) DO UPDATE SET target = excluded.target, content_file_id = NULL,
				download_id = NULL, release_title = excluded.release_title, quality = excluded.quality,
				score = excluded.score, state = 'active', adopted = 0, file_metadata = excluded.file_metadata,
				adopted_from = excluded.adopted_from, adopted_source = NULL, adopted_media = excluded.adopted_media,
				missing_since = NULL, last_checked_at = excluded.last_checked_at, updated_at = excluded.updated_at
			WHERE link.title_id = excluded.title_id AND link.download_id IS NULL
			RETURNING id`, p, target, f.titleID, release, f.quality, score, string(metaJSON), f.instance+" "+f.path,
			f.mi, now, now, now).Scan(&linkID)
		if errors.Is(err, sql.ErrNoRows) {
			return linkChanged(0, p, target, "the path holds another title's link or one the version imported")
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO link_version (link_id, version_id) VALUES (?, ?)`,
			linkID, f.followerID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM link_episode WHERE link_id = ?`, linkID); err != nil {
			return err
		}
		for _, e := range episodes {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO link_episode (link_id, episode_id) VALUES (?, ?)`,
				linkID, e); err != nil {
				return err
			}
		}
		if err := recordImportedState(ctx, tx, v, episodes, state, false, now); err != nil {
			return err
		}
		if err := l.record(ctx, tx, action{Kind: "link", Mode: "applied", Version: version, LinkID: linkID, Path: p,
			Target: target, Detail: "adopted from " + f.instance + " " + f.path + note}); err != nil {
			return err
		}
		if then != nil {
			return then(tx, now)
		}
		return nil
	})
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RegisterAdoptJob registers the library.adopt handler (theatarr library adopt [-refresh] -queue, or the native job API).
func (l *Library) RegisterAdoptJob(w *jobs.Worker) {
	w.Handle(AdoptJobKind, l.HandleAdopt)
}

// HandleAdopt runs an adopt or refresh with the library captured when the job started.
func (l *Library) HandleAdopt(ctx context.Context, j *jobs.Job) error {
	var p AdoptJob
	if err := j.Decode(&p); err != nil {
		return jobs.Permanent(err)
	}
	var err error
	if p.Refresh {
		_, err = l.Refresh(ctx, RefreshOptions{Version: p.Version})
	} else {
		_, err = l.Adopt(ctx, AdoptOptions{Version: p.Version, Limit: p.Limit})
	}
	if Unavailable(err) {
		return err // a root or the mount that does not answer: retried
	}
	if errors.Is(err, ErrRefused) || errors.Is(err, ErrGrabbing) {
		return jobs.Permanent(err)
	}
	return err
}

// followGate checks an adopt or refresh of the parallel version before it reads anything: a write needs the gate
// (whose root check covers every version's roots); a dry run, which the gate does not check, needs the roots it
// reads to answer: the version's own and the followed version's (the arr's links). A root that does not answer is
// ErrRootDown either way: nothing under it is read.
func (l *Library) followGate(ctx context.Context, version string, dryRun bool) error {
	if !dryRun {
		if err := l.gate.CheckVersion(version); err != nil {
			if Unavailable(err) {
				return err
			}
			return fmt.Errorf("%w (use -dry-run to plan)", err)
		}
		return nil
	}
	var roots []string
	for _, k := range []string{version, l.pol.Versions[version].Follows} {
		if vp := l.pol.Versions[k]; vp != nil && filepath.IsAbs(vp.Library) {
			roots = append(roots, filepath.Clean(vp.Library))
		}
		roots = append(roots, l.gate.Roots(k)...)
	}
	seen := map[string]bool{}
	for _, r := range roots {
		if seen[r] {
			continue
		}
		seen[r] = true
		if err := l.gate.Reachable(ctx, r); err != nil {
			return err
		}
	}
	return nil
}
