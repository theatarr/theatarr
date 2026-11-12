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

// Adopt links the files of a one-time Radarr/Sonarr import (AdoptImported): each file, a symlink whose target is on
// the zurg mount, gets a link of its own under the library's root with the library's naming. Nothing is grabbed,
// read beyond a stat, or renamed; the instance's files and roots are only read.
//
// The link goes through the library's writer (the gate: library.enabled, the version active and listed, the path
// under its roots, never into a mount), never over an existing path, and is recorded as an Theatarr link (link,
// link_version, link_episode, file_metadata from the instance's file record) with a library_action row. The version
// and episode states follow from the links as after an import. A file whose link dangles, whose target is on no
// mount, that is not a symlink, or whose name is taken by something else is skipped and reported. A second run finds
// the links in place and makes nothing new.

// AdoptReport is what an adopt did.
type AdoptReport struct {
	Version  string `json:"version"`
	Naming   string `json:"naming"`
	Files    int    `json:"files"`  // the files looked at
	Linked   int    `json:"linked"` // new links
	Existing int    `json:"existing"`
	// HasFile counts files skipped because the library has its own file for them already.
	HasFile int `json:"has_file"`
	// MissingEpisodes counts uncovered episodes in packs skipped to preserve the library's files.
	MissingEpisodes int `json:"missing_episodes"`
	PartialPacks    int `json:"partial_packs"`
	// Skipped counts the files not linked, by reason.
	Skipped map[string]int `json:"skipped"`
	// Problems are the skipped files, at most 200.
	Problems []AdoptProblem `json:"problems,omitempty"`
	// Links are the links made, at most 200.
	Links []AdoptLink `json:"links,omitempty"`
}

// AdoptProblem is one file not adopted.
type AdoptProblem struct {
	Path            string  `json:"path"`
	Reason          string  `json:"reason"`
	Detail          string  `json:"detail,omitempty"`
	MissingEpisodes []int64 `json:"missing_episodes,omitempty"`
}

// AdoptLink is one link made.
type AdoptLink struct {
	From   string `json:"from"` // the instance's file
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

// adoptFile is one imported file with what naming and the link row need.
type adoptFile struct {
	titleID, versionID                               int64
	path, rel, quality, group, scene, edition, mi    string
	orig, langs, releaseType, titleName, imdb, stype string
	rev, year, tmdb, tvdb, flags                     int
	size                                             int64
	cutoffNotMet                                     bool
	// episodes are the series episodes the file holds (our ids) and eps naming's view of them.
	episodes []int64
	eps      []naming.Episode
	// source is the link's provenance (link.adopted_from).
	source string
}

// adoptSetup prepares an adopt into version once its gate has passed: the cutoff and SeaDex context, and a mount
// that answers.
func (l *Library) adoptSetup(ctx context.Context, version string) (context.Context, error) {
	ctx = withCutoffTitles(ctx)
	ctx, err := decide.WithStoredSeaDex(ctx, l.st.Reader())
	if err != nil {
		return nil, err
	}
	if l.set.MountPath != "" {
		if err := l.mountHealthy(ctx); err != nil {
			return nil, fmt.Errorf("library: adopt: %w", err)
		}
	}
	return ctx, nil
}

// adoptAll adopts files into version, named by its naming, and queues the Plex scans of the folders it linked into.
func (l *Library) adoptAll(ctx context.Context, version string, vp *policy.VersionPolicy, files []adoptFile,
	rep *AdoptReport) error {
	vn, err := NamingFor(ctx, l.st.Reader(), l.pol, version)
	if err != nil {
		return err
	}
	rep.Naming = vn.Source
	root := filepath.Clean(vp.Library)
	dirs := map[string]bool{}
	for i := range files {
		rep.Files++
		dir, err := l.adoptOne(ctx, version, vp, vn, root, &files[i], rep)
		if err != nil {
			return err
		}
		if dir != "" {
			dirs[dir] = true
		}
	}
	for _, d := range sortedKeys(dirs) {
		if err := l.EnqueueScan(ctx, version, d); err != nil {
			l.log.Warn("library: plex scan not queued", "dir", d, "err", err)
		}
	}
	return nil
}

// adoptOne links one file, or reports why not. It returns the folder to scan, "" for none.
func (l *Library) adoptOne(ctx context.Context, version string, vp *policy.VersionPolicy, vn *VersionNaming, root string,
	f *adoptFile, rep *AdoptReport) (string, error) {
	target, reason, detail, err := l.adoptSource(ctx, f.path)
	if err != nil {
		return "", err
	}
	if reason != "" {
		rep.skip(f.path, reason, detail)
		return "", nil
	}

	episodes := f.episodes
	if vp.Kind == policy.KindSeries && len(episodes) == 0 {
		rep.skip(f.path, AdoptNoEpisodes, "")
		return "", nil
	}
	p, err := l.adoptPath(ctx, version, vp, vn, root, f, f.eps)
	if err != nil {
		rep.skip(f.path, AdoptNaming, err.Error())
		return "", nil
	}

	// The library's own files win: one Theatarr already linked for the title (or these episodes) stays.
	existing, err := existingFiles(ctx, l.st.Reader(), f.versionID, f.titleID)
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
	if row, err := rowAtPath(ctx, l.st.Reader(), p, f.versionID); err != nil {
		return "", err
	} else if why := row.taken(f.titleID, target); why != "" {
		rep.skip(f.path, AdoptConflict, p+": "+why)
		return "", nil
	}
	// A row an import made (download_id set) is the version's own file, whatever became of its symlink: adopt never
	// takes it over.
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
	} else {
		if err := jobs.Fence(ctx); err != nil {
			return "", fmt.Errorf("library: adopt: stopped before a link: %w", err)
		}
		if err := l.w.symlink(version, target, p); err != nil {
			_ = l.record(ctx, nil, action{Kind: "link", Mode: modeOf(err), Version: version, Path: p, Target: target,
				Detail: "adopt: " + err.Error()})
			return "", fmt.Errorf("library: adopt %s: %w", f.path, err)
		}
	}
	rep.Linked++
	if len(rep.Links) < adoptListMax {
		rep.Links = append(rep.Links, AdoptLink{From: f.path, Path: p, Target: target})
	}
	if err := l.recordAdopted(ctx, version, vp, f, p, target, episodes); err != nil {
		return "", err
	}
	return filepath.Dir(p), nil
}

// adoptSource reads the file to adopt at p: a symlink (a local directory, read with lstat and readlink only, bounded:
// readLinkBounded) whose target is a file on the mount. It returns the target, or the reason (and detail) the file is
// skipped; an error when a root or the mount does not answer.
func (l *Library) adoptSource(ctx context.Context, p string) (target, reason, detail string, err error) {
	ar, err := readLinkBounded(ctx, p)
	if err != nil {
		return "", "", "", fmt.Errorf("library: adopt: %w", err)
	}
	if errors.Is(ar.lerr, fs.ErrNotExist) {
		return "", AdoptMissing, "", nil
	}
	if ar.lerr != nil {
		return "", AdoptMissing, ar.lerr.Error(), nil
	}
	if ar.fi.Mode()&os.ModeSymlink == 0 {
		return "", AdoptNotLink, "a regular file: Theatarr links mount content only", nil
	}
	if ar.rerr != nil {
		return "", AdoptNotLink, ar.rerr.Error(), nil
	}
	target = ar.target
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(p), target)
	}
	target = filepath.Clean(target)
	read, ok := l.adoptTarget(target)
	if !ok {
		return "", AdoptOffMount, target, nil
	}
	st, err := l.mount.Stat(ctx, read)
	if errors.Is(err, ErrMountDown) {
		return "", "", "", fmt.Errorf("library: adopt: %w", err)
	}
	if err != nil || !st.Mode().IsRegular() {
		detail := target
		if err != nil {
			detail = err.Error()
		}
		return "", AdoptDangling, detail, nil
	}
	return target, "", "", nil
}

// adoptUncovered reports the holes left when any overlap protects the library's own files.
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

// adoptPath names the file under the library's root: the folder the library uses for the title already, else the
// naming's; the file name from the instance's file record (quality, group, edition, its media info, custom formats
// read back from its name or decided from its scene name for the library).
func (l *Library) adoptPath(ctx context.Context, key string, vp *policy.VersionPolicy, vn *VersionNaming, root string, f *adoptFile,
	eps []naming.Episode) (string, error) {
	kind := string(vp.Kind)
	folder, err := titleFolder(ctx, l.st.Reader(), f.versionID, f.titleID, kind, root)
	if err != nil {
		return "", err
	}
	q := naming.Quality{Title: vn.QualityTitle(f.quality), Version: max(f.rev, 1)}
	ext := path.Ext(f.path)
	cfs := l.adoptFormats(ctx, key, f.titleID, path.Base(f.path), q.Title+properSuffix(f.rev, f.stype), f.scene)
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
	name, err := naming.SeriesFileName(&vn.Config, s, eps, &naming.EpisodeFile{Quality: q,
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

// adoptFormats gives an adopted file's {Custom Formats} names: the instance's own, from its file name, else those
// decided from its release title for the library. A rename of the adopted link takes them the same way.
func (l *Library) adoptFormats(ctx context.Context, version string, titleID int64, base, qualityFull, title string) []string {
	cfs := cfFromName(base, qualityFull)
	if cfs == nil && l.formats != nil && title != "" {
		cfs = l.formats(ctx, version, titleID, title)
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

// recordAdopted records an adopted link as an Theatarr link of the library, with the instance's file record as its
// file metadata (what decide compares releases against), and sets the version's or episodes' state.
func (l *Library) recordAdopted(ctx context.Context, version string, vp *policy.VersionPolicy, f *adoptFile, p, target string,
	episodes []int64) error {
	meta := decide.ExistingFile{Quality: f.quality, Revision: max(f.rev, 1), SceneName: f.scene,
		OriginalFilePath: f.orig, RelativePath: f.rel, ReleaseGroup: f.group, Edition: f.edition, Size: f.size,
		IndexerFlags: f.flags, ReleaseType: f.releaseType}
	_ = json.Unmarshal([]byte(f.langs), &meta.Languages)
	// The file is scored by the library's own profile below; the instance's score belongs to the instance's profile
	// and is not kept.
	v := &importVersion{VersionID: f.versionID, Key: version, Policy: vp, Quality: f.quality,
		Title: titleInfo{ID: f.titleID, Kind: string(vp.Kind)}}
	state := "cutoff_met"
	if f.cutoffNotMet {
		state = "filled"
	}
	// The cutoff as an Theatarr import judges it (quality and custom-format score of the file); the arr's own flag only
	// when the engine cannot judge.
	if l.engine != nil {
		t, err := l.cutoffTitle(ctx, f.titleID, f.versionID, vp)
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
			if serr := l.engine.ScoreFile(t, decide.Version{ID: f.versionID, Key: version, Policy: vp}, &meta); serr != nil {
				meta.Score = nil
				l.log.Warn("library: adopt: score unknown", "file", f.path, "err", serr)
			}
			meta.Formats = nil // as adopt names it: a rename takes the formats from adoptFormats
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
	if f.scene != "" {
		release = f.scene
	}
	var score any
	if meta.Score != nil {
		score = *meta.Score
	}
	source := f.source
	now := store.Millis(l.now())
	return l.refWriteTx(ctx, func(tx *sql.Tx) error {
		if err := l.linksHeldTx(ctx, tx); err != nil {
			return err
		}
		// As an import's (apply.go): the row at the path is this title's, and its title never changes.
		if err := takePathTx(ctx, tx, p, f.titleID, f.versionID, target); err != nil {
			return err
		}
		var linkID int64
		// adopted_from is the provenance the router's offer sources read (00077). The offer sweep's stored
		// adopted_source (00114) described the file recorded before: a changed file at the same path clears it, to be
		// read again.
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
			RETURNING id`, p, target, f.titleID, release, f.quality, score, string(metaJSON), source,
			f.mi, now, now, now).Scan(&linkID)
		if errors.Is(err, sql.ErrNoRows) {
			return linkChanged(0, p, target, "the path holds another title's link or one the version imported")
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO link_version (link_id, version_id) VALUES (?, ?)`,
			linkID, f.versionID); err != nil {
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
			Target: target, Detail: "adopted from " + source}); err != nil {
			return err
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

// ImportFile is one file of a one-time Radarr/Sonarr import to link into a library.
type ImportFile struct {
	TitleID int64
	// Source is the provenance on the link (link.adopted_from): "<instance label> <the instance's path>".
	Source string
	// Path is where Theatarr reads the instance's file (after the import's folder mapping).
	Path string
	// The instance's file record. MediaInfo is the instance's mediaInfo object as JSON.
	RelativePath, Quality, ReleaseGroup, SceneName, Edition string
	OriginalFilePath, ReleaseType, MediaInfo                string
	Languages                                               []string
	Revision, IndexerFlags                                  int
	Size                                                    int64
	CutoffNotMet                                            bool
	// Episodes are, for a series, Theatarr's episode ids the file holds.
	Episodes []int64
}

// AdoptImported links one title's files into library version (never over a path that is taken, never over the
// library's own file, mount-backed symlinks only), named by the library's naming.
func (l *Library) AdoptImported(ctx context.Context, version string, files []ImportFile) (*AdoptReport, error) {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.AdoptImported(pinned, version, files)
	}
	vp := l.pol.Versions[version]
	if vp == nil {
		return nil, fmt.Errorf("library: adopt: unknown library %q", version)
	}
	if err := l.gate.CheckVersion(version); err != nil {
		return nil, fmt.Errorf("library: adopt %s: %w", version, err)
	}
	ctx, err := l.adoptSetup(ctx, version)
	if err != nil {
		return nil, err
	}
	rep := &AdoptReport{Version: version, Skipped: map[string]int{}}
	afs, err := l.importFiles(ctx, version, files)
	if err != nil {
		return rep, err
	}
	if err := l.adoptAll(ctx, version, vp, afs, rep); err != nil {
		return rep, err
	}
	l.log.Info("library: adopted imported files", "version", version, "files", rep.Files, "linked", rep.Linked,
		"existing", rep.Existing, "skipped", rep.Skipped)
	return rep, nil
}

// importFiles builds the adoptFiles for imported files: the title row and the library's version id, read once per title, and each file's episodes by id.
func (l *Library) importFiles(ctx context.Context, version string, files []ImportFile) ([]adoptFile, error) {
	db := l.st.Reader()
	titles := map[int64]adoptFile{}
	out := make([]adoptFile, 0, len(files))
	for _, in := range files {
		f, ok := titles[in.TitleID]
		if !ok {
			err := db.QueryRowContext(ctx, `
				SELECT COALESCE((SELECT id FROM version WHERE title_id = t.id AND policy_key = ?), 0), t.name,
				       COALESCE(t.year, 0), COALESCE(t.imdb_id, ''), COALESCE(t.tmdb_id, 0), COALESCE(t.tvdb_id, 0),
				       `+store.SeriesTypeSQL("t")+`
				FROM title t WHERE t.id = ?`, version, in.TitleID).Scan(&f.versionID, &f.titleName, &f.year, &f.imdb,
				&f.tmdb, &f.tvdb, &f.stype)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("library: adopt: no title %d", in.TitleID)
			}
			if err != nil {
				return nil, err
			}
			if f.versionID == 0 {
				return nil, fmt.Errorf("library: adopt: title %d is not in library %s", in.TitleID, version)
			}
			titles[in.TitleID] = f
		}
		langs := []byte("[]")
		if in.Languages != nil {
			langs, _ = json.Marshal(in.Languages)
		}
		f.titleID, f.path, f.rel, f.quality, f.group, f.scene = in.TitleID, in.Path, in.RelativePath, in.Quality,
			in.ReleaseGroup, in.SceneName
		f.edition, f.mi, f.orig, f.langs, f.releaseType = in.Edition, in.MediaInfo, in.OriginalFilePath, string(langs),
			in.ReleaseType
		f.rev, f.flags, f.size, f.cutoffNotMet, f.source = in.Revision, in.IndexerFlags, in.Size, in.CutoffNotMet,
			in.Source
		if len(in.Episodes) > 0 {
			eps, err := loadEpisodes(ctx, db, in.TitleID, in.Episodes)
			if err != nil {
				return nil, err
			}
			for _, e := range eps {
				f.episodes = append(f.episodes, e.ID)
				f.eps = append(f.eps, naming.Episode{Season: e.Season, Number: e.Number, Absolute: e.Absolute,
					Title: e.Name, AirDate: e.AirDate})
			}
			sort.Slice(f.episodes, func(i, j int) bool { return f.episodes[i] < f.episodes[j] })
		}
		out = append(out, f)
	}
	return out, nil
}

// CheckImportFile is adopt's look at one file without writing: "" when it can be linked, else the skip reason.
func (l *Library) CheckImportFile(ctx context.Context, p string) (reason, detail string, err error) {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.CheckImportFile(pinned, p)
	}
	_, reason, detail, err = l.adoptSource(ctx, p)
	return reason, detail, err
}

// DirExists reports whether p is a directory, with the bounded read the root checks use (rootread.go).
func (l *Library) DirExists(ctx context.Context, p string) (bool, error) {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.DirExists(pinned, p)
	}
	var ok bool
	err := rootRead(ctx, p, func() error {
		fi, err := os.Stat(p)
		ok = err == nil && fi.IsDir()
		return nil
	})
	return ok, err
}
