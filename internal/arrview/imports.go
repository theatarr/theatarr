package arrview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/arrview/nativeapi"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// The import API and the decision API as the native API serves them (POST /api/theatarr/v1/import and
// /decide), and the arr views' manualimport and ManualImport command, which use them.

// The request and result types live in nativeapi, shared with internal/web.
type (
	DecideRequest = nativeapi.DecideRequest
	ImportRequest = nativeapi.ImportRequest
	ImportFile    = nativeapi.ImportFile
	ImportResult  = nativeapi.ImportResult
)

// ErrBadRequest marks an error that is the caller's.
var ErrBadRequest = nativeapi.ErrBadRequest

func badf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrBadRequest}, args...)...)
}

// Decide runs the decision engine for one release and version, recording nothing.
func (s *Service) Decide(ctx context.Context, req DecideRequest) (*decide.Decision, error) {
	if s.decider == nil {
		return nil, errors.New("arrview: the decision engine is not running")
	}
	if req.TitleID <= 0 || req.Version == "" || strings.TrimSpace(req.Release) == "" {
		return nil, badf("title_id, version and release are required")
	}
	vp := s.pol.Versions[req.Version]
	if vp == nil {
		return nil, badf("unknown version %q", req.Version)
	}
	rel := indexer.Release{Title: strings.TrimSpace(req.Release), Indexer: req.Indexer, Size: req.Size, Seeders: -1,
		Peers: -1, InfoHash: indexer.NormalizeHash(req.InfoHash), Languages: req.Languages, Flags: req.Flags}
	if ix := s.pol.Indexers[req.Indexer]; ix != nil {
		rel.Kind, rel.Privacy = ix.Kind, ix.Privacy
	}
	if req.Seeders != nil {
		rel.Seeders, rel.Peers = *req.Seeders, *req.Seeders
	}
	var n int
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM title WHERE id = ? AND kind = ?`, req.TitleID,
		string(vp.Kind)).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, badf("no %s with id %d", vp.Kind, req.TitleID)
	}
	return s.decider.Decide(ctx, decide.Release{Release: rel}, req.TitleID, req.Version)
}

// Import runs an import. Through the library's gate only (library.enabled, the version active, its roots): when
// the gate refuses, or a dry run is asked, the plan is made without any download row (library.PlanNew), so nothing
// a worker could act on exists even while it plans.
func (s *Service) Import(ctx context.Context, req ImportRequest) (*ImportResult, error) {
	if s.library == nil {
		return nil, errors.New("arrview: the library is not running")
	}
	vp := s.pol.Versions[req.Version]
	if vp == nil {
		return nil, badf("unknown version %q", req.Version)
	}
	hash := indexer.NormalizeHash(req.InfoHash)
	if req.InfoHash != "" && hash == "" {
		return nil, badf("info_hash is not a v1 info hash")
	}
	if req.Path == "" && len(req.Files) > 0 {
		req.Path = req.Files[0].Path
	}
	var at library.Located
	name := ""
	if req.Path != "" {
		var err error
		if at, err = s.library.Locate(ctx, req.Path); err != nil {
			return nil, locateErr("path", err)
		}
		name = at.Folder
	}
	if hash == "" && name == "" {
		return nil, badf("info_hash or path is required")
	}
	rel := strings.TrimSpace(req.Release)
	if rel == "" && req.Path != "" {
		rel = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(filepath.Clean(req.Path)), ".mkv"), ".mp4")
	}
	if rel == "" {
		return nil, badf("release is required with info_hash only")
	}
	var versionID int64
	err := s.st.Reader().QueryRowContext(ctx, `SELECT v.id FROM version v JOIN title t ON t.id = v.title_id
		WHERE v.title_id = ? AND v.policy_key = ? AND t.kind = ?`, req.TitleID, req.Version, string(vp.Kind)).
		Scan(&versionID)
	if err != nil {
		return nil, badf("title %d has no version %s", req.TitleID, req.Version)
	}
	// The client's files: those named, else the one file Path names when episodes come with it.
	var files []manualFile
	for _, f := range req.Files {
		loc, err := s.library.Locate(ctx, f.Path)
		if err != nil {
			return nil, locateErr("files", err)
		}
		if loc.Folder != at.Folder || loc.Rel == "" {
			return nil, badf("files: %s is not a file of the release folder %s", f.Path, at.Folder)
		}
		files = append(files, manualFile{rel: loc.Rel, episodes: f.Episodes})
	}
	if len(req.Files) == 0 && at.Rel != "" && len(req.Episodes) > 0 {
		files = append(files, manualFile{rel: at.Rel, episodes: req.Episodes})
	}
	for _, f := range append(files, manualFile{episodes: req.Episodes}) {
		if err := s.checkEpisodes(ctx, req.TitleID, f.episodes); err != nil {
			return nil, err
		}
	}
	gateErr := s.library.Gate().CheckVersion(req.Version)
	dry := req.DryRun || gateErr != nil
	row := s.prepareImport(ctx, importRow{titleID: req.TitleID, versionID: versionID, version: req.Version,
		kind: vp.Kind, hash: hash, release: rel, provider: req.Provider, name: name, sourcePath: req.Path,
		episodes: req.Episodes, files: files})
	res := &ImportResult{}
	if gateErr != nil {
		res.Refused = gateErr.Error()
	}
	var plan *library.Plan
	if dry {
		// A dry run writes no download: a ready row, even for the moment the plan takes, is work the import pass
		// may pick up, and one left by a cancelled request or a crash would be imported for real.
		plan, err = s.library.PlanNew(ctx, row)
	} else {
		if res.DownloadID, err = s.insertImport(ctx, row, "api:import"); err != nil {
			return nil, err
		}
		plan, err = s.library.Import(ctx, res.DownloadID, false)
	}
	if err != nil {
		return nil, err
	}
	if dry && plan != nil {
		plan.DryRun = true
		if plan.Refused == "" {
			plan.Refused = res.Refused
		}
	}
	res.Plan = plan
	res.Applied = !dry && plan != nil && !plan.DryRun && !plan.Waiting && len(plan.Links) > 0
	s.log.Info("import api", "title", req.TitleID, "version", req.Version, "release", rel, "dry_run", dry,
		"applied", res.Applied)
	return res, nil
}

// manualFile is a file a client chose: its path in the release folder and its episodes.
type manualFile struct {
	rel       string
	episodes  []int64
	overrides *decide.ManualOverrides
}

// importRow is a download an import makes for the library: ready, from the given folder or hash.
type importRow struct {
	titleID, versionID                        int64
	version                                   string
	kind                                      policy.Kind
	hash, release, provider, name, sourcePath string
	episodes                                  []int64
	files                                     []manualFile
}

// prepareImport is the download an import makes (quality, formats and score from the decision on the release
// title) with its client-chosen files.
func (s *Service) prepareImport(ctx context.Context, r importRow) library.NewImport {
	quality, score, formats := "", 0, "[]"
	epMap := ""
	if s.decider != nil {
		if d, err := s.decider.Decide(ctx, decide.Release{Release: indexer.Release{Title: r.release, Seeders: -1, Peers: -1,
			InfoHash: r.hash}}, r.titleID, r.version); err == nil {
			quality, score = d.Quality, d.Score
			b, _ := json.Marshal(d.Formats)
			formats = string(b)
			if len(r.episodes) == 0 && len(r.files) == 0 && r.kind == policy.KindSeries {
				r.episodes = d.EpisodeIDs
			}
			if m := d.EpisodeMapping(); m != nil {
				b, _ := json.Marshal(m)
				epMap = string(b)
			}
		}
	}
	if quality == "" {
		if r.kind == policy.KindSeries {
			if p := sonarr.ParseTitle(r.release); p != nil {
				quality = p.Quality.Quality.Name
			}
		} else if p := radarr.ParseMovieTitle(r.release, false); p != nil {
			quality = p.Quality.Quality.Name
		}
	}
	n := library.NewImport{TitleID: r.titleID, VersionID: r.versionID, Version: r.version, InfoHash: r.hash,
		ReleaseTitle: r.release, Provider: r.provider, Name: r.name, SourcePath: r.sourcePath, Quality: quality,
		Score: score, Episodes: nonNilIDs(r.episodes), Formats: formats, EpisodeMap: epMap}
	for _, f := range r.files {
		if n.Files == nil {
			n.Files = map[string][]int64{}
		}
		n.Files[f.rel] = nonNilIDs(f.episodes)
		if n.Overrides == nil {
			n.Overrides = map[string]*decide.ManualOverrides{}
		}
		n.Overrides[f.rel] = f.overrides
	}
	return n
}

// insertImport writes a prepared import as a ready download, with its client-chosen files (download_file, source
// manual).
func (s *Service) insertImport(ctx context.Context, n library.NewImport, source string) (int64, error) {
	eps, _ := json.Marshal(nonNilIDs(n.Episodes))
	var epMap any
	if n.EpisodeMap != "" {
		epMap = n.EpisodeMap
	}
	var files []manualFile
	for rel, e := range n.Files {
		files = append(files, manualFile{rel: rel, episodes: e, overrides: n.Overrides[rel]})
	}
	now := store.Millis(s.now())
	var id int64
	err := s.st.WriteTx(ctx, func(tx *sqlTx) error {
		x, err := tx.ExecContext(ctx, `INSERT INTO download (title_id, version_id, version_key, info_hash,
			release_title, indexer, quality, score, episodes, formats, episode_map, provider, name, source_path, progress,
			state, source, created_at, updated_at, added_at, ready_at) VALUES (?, ?, ?, ?, ?, 'import', ?, ?, ?, ?, ?,
			NULLIF(?, ''), ?, NULLIF(?, ''), 100, 'ready', ?, ?, ?, ?, ?)`, n.TitleID, n.VersionID, n.Version, n.InfoHash,
			n.ReleaseTitle, n.Quality, n.Score, string(eps), n.Formats, epMap, n.Provider, n.Name, n.SourcePath, source,
			now, now, now, now)
		if err != nil {
			return err
		}
		if id, err = x.LastInsertId(); err != nil {
			return err
		}
		return writeManualFiles(ctx, tx, id, files, now)
	})
	return id, err
}

// writeManualFiles replaces a download's client-chosen files.
func writeManualFiles(ctx context.Context, tx *sqlTx, id int64, files []manualFile, now int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM download_file WHERE download_id = ? AND source = 'manual'`, id); err != nil {
		return err
	}
	for _, f := range files {
		eps, _ := json.Marshal(nonNilIDs(f.episodes))
		overrides, _ := json.Marshal(f.overrides)
		if _, err := tx.ExecContext(ctx, `INSERT INTO download_file (download_id, rel, episodes, source, how, updated_at, overrides)
			VALUES (?, ?, ?, 'manual', 'manual', ?, ?) ON CONFLICT (download_id, rel) DO UPDATE SET episodes = excluded.episodes,
			source = 'manual', how = 'manual', updated_at = excluded.updated_at, overrides = excluded.overrides`, id, f.rel, string(eps), now, string(overrides)); err != nil {
			return err
		}
	}
	return nil
}

// checkEpisodes refuses episode ids that are not the title's.
func (s *Service) checkEpisodes(ctx context.Context, titleID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	var n int
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT COUNT(DISTINCT id) FROM episode WHERE title_id = ? AND id IN (`+
		store.Placeholders(len(ids))+`)`, append([]any{titleID}, toAny(ids)...)...).Scan(&n); err != nil {
		return err
	}
	if n != len(slices.Compact(slices.Sorted(slices.Values(ids)))) {
		return badf("episodes %v are not all episodes of title %d", ids, titleID)
	}
	return nil
}

// manualImportResource is ManualImportResource (src/Radarr.Api.V3/ManualImport/ManualImportResource.cs @ a96bf7c;
// Sonarr's has series, seasonNumber, episodes, episodeFileId, releaseType). Ruddarr requires id, size and
// rejections; decypharr reads path, quality, movie.id / series.id, episodes, rejections {reason, type}.
type manualImportResource struct {
	ID                int64             `json:"id"`
	Path              string            `json:"path"`
	RelativePath      string            `json:"relativePath,omitempty"`
	FolderName        string            `json:"folderName,omitempty"`
	Name              string            `json:"name,omitempty"`
	Size              int64             `json:"size"`
	Movie             *movieResource    `json:"movie,omitempty"`
	MovieFileID       *int64            `json:"movieFileId,omitempty"`
	Series            *seriesResource   `json:"series,omitempty"`
	SeasonNumber      *int              `json:"seasonNumber,omitempty"`
	Episodes          []episodeResource `json:"episodes,omitempty"`
	EpisodeFileID     *int64            `json:"episodeFileId,omitempty"`
	ReleaseGroup      string            `json:"releaseGroup,omitempty"`
	Quality           qualityModel      `json:"quality"`
	Languages         []language        `json:"languages"`
	QualityWeight     int               `json:"qualityWeight"`
	DownloadID        string            `json:"downloadId,omitempty"`
	CustomFormats     []customFormatID  `json:"customFormats"`
	CustomFormatScore int               `json:"customFormatScore"`
	IndexerFlags      int               `json:"indexerFlags"`
	ReleaseType       string            `json:"releaseType,omitempty"`
	Rejections        []importRejection `json:"rejections"`
}

type importRejection struct {
	Reason string `json:"reason"`
	Type   string `json:"type"`
}

func pathID(p string) int64 { return int64(fnv32(p)&0x7fffffff) | 1 }

var videoExts = []string{".mkv", ".mp4", ".avi", ".m4v", ".ts", ".wmv", ".mov", ".webm", ".m2ts"}

// GET manualimport (ManualImportController.GetMediaItems): candidates with our decision per file.
//   - downloadId: one of our downloads (its info hash): the library's dry-run plan of its release on the mount;
//   - folder: the video files under a folder (read only), each decided for the title its name maps to;
//   - movieId / seriesId (no downloadId): the title's files in the view.
func (s *Service) manualImportList(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	if d, ok := r.query("downloadId"); ok && d != "" && !(r.v.Flavour == Sonarr && r.queryInt("seriesId", 0) > 0) {
		out, err := s.manualFromDownload(ctx, r, strings.ToLower(d))
		if err != nil {
			s.internal(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if folder, ok := r.query("folder"); ok && folder != "" {
		out, err := s.manualFromFolder(ctx, r, folder)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	writeJSON(w, http.StatusOK, []manualImportResource{})
}

func fnv32(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// manualFromDownload plans one of our downloads (by info hash) with the library, without writing.
func (s *Service) manualFromDownload(ctx context.Context, r *request, hash string) ([]manualImportResource, error) {
	out := []manualImportResource{}
	var id, tid int64
	var version, rel, quality string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT id, title_id, version_key, release_title, quality FROM download
		WHERE info_hash = ? AND version_key IN (`+store.Placeholders(len(r.v.Versions))+`) ORDER BY id DESC LIMIT 1`,
		append([]any{hash}, toAny(r.v.Versions)...)...).Scan(&id, &tid, &version, &rel, &quality)
	if err != nil || s.library == nil {
		return out, nil
	}
	plan, err := s.library.Import(ctx, id, true)
	if err != nil {
		s.log.Info("manualimport: no plan", "download", id, "err", err)
		return out, nil
	}
	e, err := s.loadOne(ctx, r.v, tid)
	if err != nil || e == nil {
		return out, err
	}
	rejected := map[string]string{}
	for _, f := range plan.Rejected {
		rejected[f.Path] = f.Reason
	}
	linked := map[string][]int64{}
	for _, l := range plan.Links {
		linked[l.File] = append(linked[l.File], l.Episodes...)
	}
	choices := map[string]*decide.ManualOverrides{}
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT rel, episodes, overrides FROM download_file
		WHERE download_id = ? AND source = 'manual'`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var rel, episodes, raw string
		if err := rows.Scan(&rel, &episodes, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var o *decide.ManualOverrides
		if err := json.Unmarshal([]byte(raw), &o); err != nil {
			rows.Close()
			return nil, err
		}
		if o == nil {
			o = &decide.ManualOverrides{}
		}
		_ = json.Unmarshal([]byte(episodes), &o.Episodes)
		choices[rel] = o
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	files := slices.Clone(plan.Files)
	for _, f := range plan.Rejected {
		if !slices.ContainsFunc(files, func(x library.PlannedFile) bool { return x.Path == f.Path }) {
			files = append(files, f)
		}
	}
	for _, f := range files {
		episodes := linked[f.Rel]
		if o := choices[f.Rel]; o != nil {
			episodes = o.Episodes
		}
		m := s.manualResource(r.v, e, f.Path, filepath.Base(plan.Folder), f.Size, quality, rel, strings.ToUpper(hash))
		if f.Facts != nil {
			facts := f.Facts
			o := &decide.ManualOverrides{Quality: &decide.ManualQuality{Name: facts.Quality,
				Revision: profile.Revision{Version: facts.Revision, Real: facts.Real, IsRepack: facts.IsRepack}},
				Languages: facts.Languages, ReleaseGroup: &facts.ReleaseGroup, Episodes: episodes}
			if chosen := choices[f.Rel]; chosen != nil {
				o.IndexerFlags = chosen.IndexerFlags
			}
			m, err = s.decideFile(ctx, r.v, f.Path, filepath.Base(plan.Folder),
				strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path)), f.Size, tid, episodes, strings.ToUpper(hash), o)
			if err != nil {
				return nil, err
			}
			m.Quality = qualityModelFor(r.v.Flavour, f.Facts.Quality, f.Facts.Revision)
			m.Quality.Revision.Real = f.Facts.Real
			m.Quality.Revision.IsRepack = f.Facts.IsRepack
			m.Languages, m.ReleaseGroup = languagesFrom(f.Facts.Languages), f.Facts.ReleaseGroup
			m.QualityWeight = qualityWeight(r.v.Flavour, f.Facts.Quality, profile.Revision{Version: f.Facts.Revision, Real: f.Facts.Real})
		}
		m.RelativePath = f.Rel
		if why, ok := rejected[f.Path]; ok {
			m.Rejections = append(m.Rejections, importRejection{Reason: why, Type: "permanent"})
		} else if _, ok := linked[f.Rel]; !ok {
			m.Rejections = append(m.Rejections, importRejection{Reason: "Not matched to an episode of the download",
				Type: "permanent"})
		}
		if plan.Refused != "" {
			m.Rejections = append(m.Rejections, importRejection{Reason: plan.Refused, Type: "temporary"})
		}
		if r.v.Flavour == Sonarr {
			if err := s.fillEpisodes(ctx, r.v, e, &m, episodes); err != nil {
				return nil, err
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func toAny[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// manualResource is the common part of a candidate.
func (s *Service) manualResource(v *View, e *entry, p, folder string, size int64, quality, rel, dl string) manualImportResource {
	m := manualImportResource{ID: pathID(p), Path: p, FolderName: folder, Name: strings.TrimSuffix(filepath.Base(p),
		filepath.Ext(p)), Size: size, Quality: qualityModelFor(v.Flavour, quality, 1), Languages: []language{},
		QualityWeight: qualityWeight(v.Flavour, quality, profile.Revision{Version: 1}), DownloadID: dl,
		CustomFormats: []customFormatID{}, Rejections: []importRejection{}}
	if g := radarr.ParseReleaseGroup(rel); g != nil {
		m.ReleaseGroup = *g
	}
	if e != nil {
		if v.Flavour == Radarr {
			mr := s.movieResource(v, e)
			m.Movie = &mr
		}
	}
	return m
}

func (s *Service) fillEpisodes(ctx context.Context, v *View, e *entry, m *manualImportResource, ids []int64) error {
	srs, err := s.seriesResources(ctx, v, []*entry{e})
	if err != nil {
		return err
	}
	if len(srs) > 0 {
		m.Series = &srs[0]
	}
	eps, err := s.loadEpisodesWith(ctx, v, []*entry{e}, true)
	if err != nil {
		return err
	}
	m.Episodes = []episodeResource{}
	for _, x := range eps[e.id] {
		if slices.Contains(ids, x.id) {
			m.Episodes = append(m.Episodes, episodeFrom(e, x))
			sn := x.season
			m.SeasonNumber = &sn
		}
	}
	switch {
	case len(m.Episodes) > 1:
		m.ReleaseType = "multiEpisode"
	case len(m.Episodes) == 1:
		m.ReleaseType = "singleEpisode"
	default:
		m.ReleaseType = "unknown"
	}
	return nil
}

// manualFromFolder lists the video files under a folder (two levels, read only) and decides each for the title
// its name (or the folder's) maps to, against the title's first version in the view. Every read goes through the
// mount guard: the folder may be on the mount, or hold symlinks into it. A mount that does not answer is an error,
// not an empty folder.
func (s *Service) manualFromFolder(ctx context.Context, r *request, folder string) ([]manualImportResource, error) {
	out := []manualImportResource{}
	if s.library == nil {
		return out, nil
	}
	folder = filepath.Clean(folder)
	type file struct {
		path string
		size int64
	}
	var files []file
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		ents, err := s.library.ReadDir(ctx, dir)
		if err != nil {
			return mountErr(err)
		}
		for _, de := range ents {
			p := filepath.Join(dir, de.Name())
			if de.IsDir() {
				if depth < 2 {
					if err := walk(p, depth+1); err != nil {
						return err
					}
				}
				continue
			}
			if !slices.Contains(videoExts, strings.ToLower(filepath.Ext(de.Name()))) {
				continue
			}
			fi, err := s.library.Stat(ctx, p)
			if err != nil {
				if err := mountErr(err); err != nil {
					return err
				}
				continue
			}
			files = append(files, file{p, fi.Size()})
		}
		return nil
	}
	fi, err := s.library.Stat(ctx, folder)
	if err := mountErr(err); err != nil {
		return nil, err
	}
	if err == nil && !fi.IsDir() {
		files = append(files, file{folder, fi.Size()})
		folder = filepath.Dir(folder)
	} else if err := walk(folder, 0); err != nil {
		return nil, err
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f.path), filepath.Ext(f.path))
		m, err := s.decideFile(ctx, r.v, f.path, filepath.Base(folder), name, f.size, 0, nil, "")
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// locateErr is a client's path Locate could not place: not on the mount is the request's (ErrBadRequest); a mount that
// does not answer, or another failure reading the path, is not, and is returned as it is.
func locateErr(what string, err error) error {
	if errors.Is(err, library.ErrNotOnMount) || !library.Unavailable(err) && !errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) {
		return badf("%s %v", what, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// mountErr keeps the errors of a guarded read that are the mount's (it did not answer, or the request ended);
// any other (a missing or unreadable path) is the path's, and the preview skips it.
func mountErr(err error) error {
	if errors.Is(err, library.ErrMountDown) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

// decideFile maps a file (by its name, then its folder's) to a title of the view, unless titleID is given, and
// decides it for the title's first version: quality, formats, score and rejections.
func (s *Service) decideFile(ctx context.Context, v *View, p, folder, name string, size, titleID int64,
	episodes []int64, dl string, overrides ...*decide.ManualOverrides) (manualImportResource, error) {
	rel := name
	if titleID == 0 {
		for _, cand := range []string{name, folder} {
			r := indexer.Release{Title: cand}
			tid, err := s.mapRelease(ctx, v, &r)
			if err != nil {
				return manualImportResource{}, err
			}
			if tid > 0 {
				titleID, rel = tid, cand
				break
			}
		}
	}
	e, err := s.loadOne(ctx, v, titleID)
	if err != nil {
		return manualImportResource{}, err
	}
	m := s.manualResource(v, e, p, folder, size, "Unknown", rel, dl)
	if e == nil || len(e.versions) == 0 {
		what := "Movie"
		if v.Flavour == Sonarr {
			what = "Series"
		}
		m.Rejections = append(m.Rejections, importRejection{Reason: "Unknown " + what, Type: "permanent"})
		return m, nil
	}
	if s.decider != nil {
		var manual *decide.ManualOverrides
		if len(overrides) > 0 {
			manual = overrides[0]
		}
		d, err := s.decider.Decide(ctx, decide.Release{Release: indexer.Release{Title: rel, Size: size, Seeders: -1,
			Peers: -1, Indexer: "import", Privacy: "public"}, Manual: manual}, e.id, e.versions[0].key)
		if err != nil {
			return m, err
		}
		m.Quality = qualityModelFor(v.Flavour, d.Quality, d.Revision.Version)
		m.Quality.Revision = revision{Version: d.Revision.Version, Real: d.Revision.Real, IsRepack: d.Revision.IsRepack}
		if manual != nil && manual.IndexerFlags != nil {
			m.IndexerFlags = *manual.IndexerFlags
		}
		m.QualityWeight = qualityWeight(v.Flavour, d.Quality, d.Revision)
		m.CustomFormats, m.CustomFormatScore = formatIDs(d.Formats), d.Score
		m.Languages = languagesFrom(d.Languages)
		if d.ReleaseGroup != "" {
			m.ReleaseGroup = d.ReleaseGroup
		}
		for _, rj := range d.Rejections {
			if rj.Ours {
				continue // a file on disk has no indexer: the version's indexer list does not apply
			}
			t := "permanent"
			if rj.Temporary {
				t = "temporary"
			}
			msg := rj.Message
			if msg == "" {
				msg = rj.Reason
			}
			m.Rejections = append(m.Rejections, importRejection{Reason: msg, Type: t})
		}
		if len(episodes) == 0 {
			episodes = d.EpisodeIDs
		}
	}
	if v.Flavour == Sonarr {
		if err := s.fillEpisodes(ctx, v, e, &m, episodes); err != nil {
			return m, err
		}
	}
	return m, nil
}

// POST manualimport (ManualImportController.ReprocessItems): decide each item again for the title the client chose
// (movieId / seriesId, episodeIds); the list comes back with movie or series, episodes, rejections and formats.
func (s *Service) manualImportReprocess(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	var items []map[string]any
	if err := decodeBody(r, &items); err != nil {
		badRequest(w, "", err.Error(), nil)
		return
	}
	if len(items) == 0 {
		badRequest(w, "", "items must be provided", nil)
		return
	}
	out := make([]manualImportResource, 0, len(items))
	for _, it := range items {
		p, _ := it["path"].(string)
		key := "movieId"
		if r.v.Flavour == Sonarr {
			key = "seriesId"
		}
		var tid int64
		if f, ok := it[key].(float64); ok && f > 0 {
			t, err := s.resolve(ctx, r.v, int64(f))
			if err != nil {
				s.internal(w, r, err)
				return
			}
			tid = t
		}
		var eps []int64
		if arr, ok := it["episodeIds"].([]any); ok {
			for _, x := range arr {
				if f, ok := x.(float64); ok {
					eps = append(eps, int64(f))
				}
			}
		}
		dl, _ := it["downloadId"].(string)
		var size int64
		if s.library != nil {
			fi, err := s.library.Stat(ctx, p)
			if err := mountErr(err); err != nil {
				s.internal(w, r, err)
				return
			}
			if err == nil {
				size = fi.Size()
			}
		}
		name := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		overrides, err := manualOverrides(r.v, it, eps)
		if err != nil {
			badRequest(w, "", err.Error(), nil)
			return
		}
		if err := s.checkEpisodes(ctx, tid, eps); err != nil {
			badRequest(w, "", err.Error(), nil)
			return
		}
		m, err := s.decideFile(ctx, r.v, p, filepath.Base(filepath.Dir(p)), name, size, tid, eps, dl, overrides)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if f, ok := it["id"].(float64); ok {
			m.ID = int64(f)
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, out)
}

// manualImportCommand runs the ManualImport command (ManualImportService.Execute): each file is imported as the
// client chose it, at its path, for its movie or series and episodes. Files are grouped by release folder: a file of
// one of our downloads still in hand (downloadId, ready or importing) imports into that download, any other file
// into a new download of the title's version in the view. The library links only the chosen files, each with the
// chosen episodes, and still applies its sample guard and gate (a dry run while the gate refuses).
//
// Theatarr links content on the debrid mount and never copies or moves files: every importMode links and leaves the
// source in place. A path not on the mount (nor a symlink into it) is refused. It returns the
// command's message and whether it failed.
func (s *Service) manualImportCommand(ctx context.Context, v *View, body map[string]any) (string, bool) {
	if s.library == nil {
		return "Nothing imported: the library is not running", true
	}
	mode, _ := body["importMode"].(string)
	switch strings.ToLower(mode) {
	case "", "auto", "copy", "move", "hardlink", "link":
	default:
		return fmt.Sprintf("Nothing imported: unknown importMode %q", mode), true
	}
	files, _ := body["files"].([]any)
	if len(files) == 0 {
		return "No files to import", false
	}
	type group struct {
		titleID, versionID, downloadID int64
		version, hash, path, folder    string
		kind                           policy.Kind
		files                          []manualFile
	}
	var groups []*group
	byKey := map[string]*group{}
	var why []string
	refused := 0
	for _, f := range files {
		m, _ := f.(map[string]any)
		p, _ := m["path"].(string)
		fail := func(msg string) {
			refused++
			why = append(why, p+": "+msg)
		}
		loc, err := s.library.Locate(ctx, p)
		if err != nil {
			switch {
			case errors.Is(err, library.ErrNotOnMount):
				fail("not on the debrid mount (Theatarr links mount content only)")
			case library.Unavailable(err):
				fail("the debrid mount does not answer; try again once it does")
			default:
				fail(err.Error())
			}
			continue
		}
		if loc.Rel == "" {
			fail("a release folder, not a file")
			continue
		}
		key := "movieId"
		if v.Flavour == Sonarr {
			key = "seriesId"
		}
		var titleID int64
		if x, ok := m[key].(float64); ok && x > 0 {
			if titleID, err = s.resolve(ctx, v, int64(x)); err != nil {
				fail(err.Error())
				continue
			}
		}
		g := &group{folder: loc.Folder, path: p}
		if dl, _ := m["downloadId"].(string); dl != "" {
			g.hash = indexer.NormalizeHash(dl)
			var tid int64
			var state string
			err := s.st.Reader().QueryRowContext(ctx, `SELECT id, title_id, version_id, version_key, state FROM download
				WHERE info_hash = ? AND version_key IN (`+store.Placeholders(len(v.Versions))+`) ORDER BY id DESC LIMIT 1`,
				append([]any{strings.ToLower(dl)}, toAny(v.Versions)...)...).Scan(&g.downloadID, &tid, &g.versionID, &g.version, &state)
			switch {
			case err != nil:
				g.downloadID = 0
			case titleID == 0:
				titleID = tid
			}
			if g.downloadID != 0 && (tid != titleID || (state != "ready" && state != "importing")) {
				g.downloadID, g.versionID, g.version = 0, 0, "" // another title, or no longer in hand: a new download
			}
		}
		if titleID == 0 {
			fail("no " + strings.TrimSuffix(key, "Id") + " chosen")
			continue
		}
		if g.version == "" {
			e, err := s.loadOne(ctx, v, titleID)
			if err != nil || e == nil || len(e.versions) == 0 {
				fail("the title has no version in this view")
				continue
			}
			g.version, g.versionID = e.versions[0].key, e.versions[0].id
		}
		g.titleID, g.kind = titleID, s.pol.Versions[g.version].Kind
		var eps []int64
		if arr, ok := m["episodeIds"].([]any); ok {
			for _, x := range arr {
				if n, ok := x.(float64); ok {
					eps = append(eps, int64(n))
				}
			}
		}
		if g.kind == policy.KindSeries && len(eps) == 0 {
			fail("no episodes chosen")
			continue
		}
		if err := s.checkEpisodes(ctx, titleID, eps); err != nil {
			fail(err.Error())
			continue
		}
		overrides, err := manualOverrides(v, m, eps)
		if err != nil {
			fail(err.Error())
			continue
		}
		overrides.ImportMode = strings.ToLower(mode)
		if overrides.ImportMode == "" {
			overrides.ImportMode = "auto"
		}
		k := fmt.Sprintf("%d/%d/%d/%s", g.titleID, g.versionID, g.downloadID, g.folder)
		if byKey[k] == nil {
			byKey[k] = g
			groups = append(groups, g)
		}
		byKey[k].files = append(byKey[k].files, manualFile{rel: loc.Rel, episodes: eps, overrides: overrides})
	}
	var imported, dry int
	for _, g := range groups {
		id := g.downloadID
		now := store.Millis(s.now())
		var err error
		if id == 0 {
			id, err = s.insertImport(ctx, s.prepareImport(ctx, importRow{titleID: g.titleID, versionID: g.versionID,
				version: g.version, kind: g.kind, hash: g.hash, release: g.folder, name: g.folder, sourcePath: g.path,
				files: g.files}), "api:manualimport")
		} else {
			err = s.st.WriteTx(ctx, func(tx *sqlTx) error {
				if _, err := tx.ExecContext(ctx, `UPDATE download SET source_path = ?, updated_at = ? WHERE id = ?`,
					g.path, now, id); err != nil {
					return err
				}
				return writeManualFiles(ctx, tx, id, g.files, now)
			})
		}
		if err != nil {
			why = append(why, g.folder+": "+err.Error())
			continue
		}
		plan, err := s.library.Import(ctx, id, false)
		switch {
		case err != nil:
			why = append(why, err.Error())
		case plan.DryRun:
			dry += len(plan.Links)
			if plan.Refused != "" && !slices.Contains(why, plan.Refused) {
				why = append(why, plan.Refused)
			}
		default:
			imported += len(plan.Links)
			for _, r := range plan.Rejected {
				why = append(why, r.Rel+": "+r.Reason)
			}
			for _, u := range plan.Unmatched {
				why = append(why, u+": not in the release on the mount")
			}
		}
	}
	msg := fmt.Sprintf("Imported %d file(s)", imported)
	if dry > 0 {
		msg += fmt.Sprintf("; %d planned as a dry run", dry)
	}
	if refused > 0 {
		msg += fmt.Sprintf("; %d file(s) refused", refused)
	}
	if len(why) > 0 {
		msg += " (" + strings.Join(why, "; ") + ")"
	}
	return msg, imported == 0 && dry == 0
}

// manualOverrides resolves arr IDs to canonical names. Like ManualImportService, Unknown and blank choices
// fall back to parsing; concrete choices replace the aggregated values, including the quality's revision.
func manualOverrides(v *View, item map[string]any, episodes []int64) (*decide.ManualOverrides, error) {
	o := &decide.ManualOverrides{Episodes: episodes}
	fl := format.Radarr
	if v.Flavour == Sonarr {
		fl = format.Sonarr
	}
	if raw, ok := item["quality"]; ok && raw != nil {
		b, _ := json.Marshal(raw)
		var q qualityModel
		if err := json.Unmarshal(b, &q); err != nil {
			return nil, badf("invalid quality: %v", err)
		}
		if q.Quality.ID != 0 {
			canonical, ok := profile.QualityByID(fl, q.Quality.ID)
			if !ok {
				return nil, badf("unknown quality id %d", q.Quality.ID)
			}
			o.Quality = &decide.ManualQuality{Name: canonical.Name, Revision: profile.Revision{
				Version: max(q.Revision.Version, 1), Real: q.Revision.Real, IsRepack: q.Revision.IsRepack}}
		}
	}
	if raw, ok := item["languages"]; ok && raw != nil {
		b, _ := json.Marshal(raw)
		var langs []language
		if err := json.Unmarshal(b, &langs); err != nil {
			return nil, badf("invalid languages: %v", err)
		}
		for _, l := range langs {
			if l.ID == 0 {
				continue
			}
			resolved, ok := manualLanguage(v.Flavour, l.ID)
			if !ok {
				return nil, badf("unknown language id %d", l.ID)
			}
			o.Languages = append(o.Languages, resolved.Name)
		}
	}
	if g, ok := item["releaseGroup"].(string); ok && strings.TrimSpace(g) != "" {
		o.ReleaseGroup = &g
	}
	if n, ok := item["indexerFlags"].(float64); ok {
		flags := int(n)
		o.IndexerFlags = &flags
	}
	return o, nil
}

func manualLanguage(flavour string, id int) (language, bool) {
	if flavour == Sonarr {
		l, ok := sonarr.FindLanguageByID(id)
		return language{ID: l.ID, Name: l.Name}, ok
	}
	l, ok := radarr.LanguageByID(id)
	return language{ID: l.ID, Name: l.Name}, ok
}
