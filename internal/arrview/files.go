package arrview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/store"
)

// LinkFileBase starts the file ids of our own library links: a movie or episode file id is a version_file id (a
// file the replaced arr held, as imported) below it, or LinkFileBase + link id.
const LinkFileBase = 1_000_000_000

// Library is the part of internal/library the views write through: unlinking a file (DELETE moviefile and
// episodefile) and importing (manualimport, ManualImport). Both pass the library's gate.
type Library interface {
	CheckUnlink(version, path string) error
	Unlink(ctx context.Context, version, path string, linkID int64, reason string) (*library.UnlinkResult, error)
	Import(ctx context.Context, id int64, dryRun bool) (*library.Plan, error)
	// PlanNew is a dry run of an import that writes no download row.
	PlanNew(ctx context.Context, n library.NewImport) (*library.Plan, error)
	Gate() *library.Gate
	// Locate places a path a client named on the debrid mount (library.ErrNotOnMount when it is not there).
	Locate(ctx context.Context, path string) (library.Located, error)
	// ReadDir and Stat read a path a client named under the mount guard (timeout, circuit breaker): a preview of a
	// folder on, or symlinked into, a dead mount fails instead of hanging.
	ReadDir(ctx context.Context, dir string) ([]os.DirEntry, error)
	Stat(ctx context.Context, path string) (os.FileInfo, error)
}

// fileRow is one file of a view's version: an imported arr file or one of our links.
type fileRow struct {
	id        int64 // the view's file id
	titleID   int64
	versionID int64
	version   string
	linkID    int64 // 0 for an arr file
	adopted   bool
	f         *vfile
	episodes  []int64
}

// viewFiles reads the files of the view's versions (all, or of the titles listed): the imported arr files
// (version_file) and our active links.
func (s *Service) viewFiles(ctx context.Context, v *View, titleIDs []int64) ([]*fileRow, error) {
	where, args := scope(v, titleIDs)
	db := s.st.Reader()
	var out []*fileRow
	rows, err := db.QueryContext(ctx, `SELECT vf.id, vf.version_id, v.policy_key, vf.title_id, vf.instance, vf.path,
		vf.relative_path, vf.size_bytes, vf.quality, vf.quality_revision, vf.release_group, vf.scene_name, vf.edition,
		vf.languages, vf.media_info, vf.custom_format_score, vf.indexer_flags, vf.original_file_path,
		vf.cutoff_not_met, vf.added_at FROM version_file vf JOIN version v ON v.id = vf.version_id
		JOIN title t ON t.id = v.title_id WHERE `+where+` ORDER BY vf.id`, args...)
	if err != nil {
		return nil, err
	}
	byVF := map[int64]*fileRow{}
	for rows.Next() {
		f := &vfile{}
		x := &fileRow{f: f}
		var langs, mi string
		if err := rows.Scan(&f.id, &x.versionID, &x.version, &x.titleID, &f.instance, &f.path, &f.relPath, &f.size,
			&f.quality, &f.revision, &f.group, &f.scene, &f.edition, &langs, &mi, &f.cfScore, &f.flags, &f.origPath,
			&f.cutoffNotMet, &f.added); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal([]byte(langs), &f.languages)
		if mi != "" && mi != "{}" && json.Valid([]byte(mi)) {
			f.mediaInfo = json.RawMessage(mi)
		}
		x.id = f.id
		byVF[f.id] = x
		out = append(out, x)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	if v.Flavour == Sonarr && len(byVF) > 0 {
		rows, err = db.QueryContext(ctx, `SELECT fe.version_file_id, fe.episode_id FROM version_file_episode fe
			JOIN version_file vf ON vf.id = fe.version_file_id JOIN version v ON v.id = vf.version_id
			JOIN title t ON t.id = v.title_id WHERE `+where+` ORDER BY fe.episode_id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var fid, eid int64
			if err := rows.Scan(&fid, &eid); err != nil {
				rows.Close()
				return nil, err
			}
			if x := byVF[fid]; x != nil {
				x.episodes = append(x.episodes, eid)
			}
		}
		if err := closeRows(rows); err != nil {
			return nil, err
		}
	}
	links, err := s.linkFiles(ctx, v, titleIDs)
	if err != nil {
		return nil, err
	}
	return append(out, links...), nil
}

// linkFiles reads the active links of the view's versions as files.
func (s *Service) linkFiles(ctx context.Context, v *View, titleIDs []int64) ([]*fileRow, error) {
	where, args := scope(v, titleIDs)
	db := s.st.Reader()
	rows, err := db.QueryContext(ctx, `SELECT l.id, lv.version_id, v.policy_key, l.title_id, l.path,
		COALESCE(cf.size_bytes, 0), COALESCE(l.quality, ''), COALESCE(l.release_title, ''), l.score, l.adopted,
		l.created_at, l.file_metadata FROM link l JOIN link_version lv ON lv.link_id = l.id JOIN version v ON v.id = lv.version_id
		JOIN title t ON t.id = v.title_id LEFT JOIN content_file cf ON cf.id = l.content_file_id
		WHERE l.state = 'active' AND `+where+` ORDER BY l.id`, args...)
	if err != nil {
		return nil, err
	}
	var out []*fileRow
	byLink := map[int64][]*fileRow{}
	for rows.Next() {
		f := &vfile{instance: "theatarr", languages: []string{}}
		x := &fileRow{f: f}
		var created int64
		var raw string
		if err := rows.Scan(&x.linkID, &x.versionID, &x.version, &x.titleID, &f.path, &f.size, &f.quality, &f.scene,
			&f.cfScore, &x.adopted, &created, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		f.relPath = path.Base(f.path)
		var metadata decide.ExistingFile
		if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
			rows.Close()
			return nil, err
		}
		if metadata.Revision != 0 {
			f.revision, f.real, f.isRepack = metadata.Revision, metadata.Real, metadata.IsRepack
			f.group, f.languages, f.flags = metadata.ReleaseGroup, metadata.Languages, metadata.IndexerFlags
			f.scene, f.edition, f.origPath = metadata.SceneName, metadata.Edition, metadata.OriginalFilePath
			f.size = metadata.Size
		}
		f.added = sql.NullInt64{Int64: created, Valid: true}
		x.id = LinkFileBase + x.linkID
		f.id = x.id
		byLink[x.linkID] = append(byLink[x.linkID], x)
		out = append(out, x)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	if v.Flavour == Sonarr && len(out) > 0 {
		rows, err = db.QueryContext(ctx, `SELECT le.link_id, le.episode_id FROM link_episode le
			JOIN link l ON l.id = le.link_id WHERE l.state = 'active' AND l.title_id IN (
			SELECT v.title_id FROM version v JOIN title t ON t.id = v.title_id WHERE `+where+`) ORDER BY le.episode_id`,
			args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var lid, eid int64
			if err := rows.Scan(&lid, &eid); err != nil {
				rows.Close()
				return nil, err
			}
			for _, x := range byLink[lid] {
				x.episodes = append(x.episodes, eid)
			}
		}
		if err := closeRows(rows); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// episodeFileResource is Sonarr's EpisodeFileResource (src/Sonarr.Api.V3/EpisodeFiles/EpisodeFileResource.cs
// @ cab419a). decypharr reads id, seriesId, seasonNumber, path, size; upgrade scripts quality, sceneName, relativePath.
type episodeFileResource struct {
	ID                  int64             `json:"id,omitempty"`
	SeriesID            int64             `json:"seriesId"`
	SeasonNumber        int               `json:"seasonNumber"`
	RelativePath        string            `json:"relativePath,omitempty"`
	Path                string            `json:"path,omitempty"`
	Size                int64             `json:"size"`
	DateAdded           arrTime           `json:"dateAdded"`
	SceneName           string            `json:"sceneName,omitempty"`
	ReleaseGroup        string            `json:"releaseGroup,omitempty"`
	Languages           []language        `json:"languages"`
	Quality             qualityModel      `json:"quality"`
	CustomFormats       []any             `json:"customFormats"`
	CustomFormatScore   int               `json:"customFormatScore"`
	IndexerFlags        *int              `json:"indexerFlags,omitempty"`
	ReleaseType         string            `json:"releaseType,omitempty"`
	MediaInfo           mediaInfoResource `json:"mediaInfo,omitempty"`
	QualityCutoffNotMet bool              `json:"qualityCutoffNotMet"`
}

func (s *Service) episodeFileFrom(v *View, e *entry, x *fileRow, seasons map[int64]int) episodeFileResource {
	f := x.f
	r := episodeFileResource{ID: x.id, SeriesID: viewID(x.titleID), RelativePath: f.relPath, Path: f.path,
		Size: f.size, DateAdded: at(minTime), SceneName: f.scene, ReleaseGroup: f.group,
		Languages: languagesFrom(f.languages), Quality: qualityModelFor(Sonarr, f.quality, f.revision),
		CustomFormats: []any{}, MediaInfo: f.mediaInfo, QualityCutoffNotMet: f.cutoffNotMet}
	r.Quality.Revision.Real, r.Quality.Revision.IsRepack = f.real, f.isRepack
	if f.added.Valid {
		r.DateAdded = at(millis(f.added.Int64))
	}
	if f.cfScore.Valid {
		r.CustomFormatScore = int(f.cfScore.Int64)
	}
	flags := f.flags
	r.IndexerFlags = &flags
	if len(x.episodes) > 0 {
		r.SeasonNumber = seasons[x.episodes[0]]
		switch {
		case len(x.episodes) > 1:
			r.ReleaseType = "multiEpisode"
		default:
			r.ReleaseType = "singleEpisode"
		}
	}
	if x.linkID > 0 && e != nil {
		if _, sp := s.rootAndPath(v, e); sp != "" {
			if rel, err := filepath.Rel(sp, f.path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				r.RelativePath = rel
			}
		}
	}
	return r
}

// episodeSeasons maps episode ids to their seasons.
func (s *Service) episodeSeasons(ctx context.Context, titleIDs []int64) (map[int64]int, error) {
	out := map[int64]int{}
	if len(titleIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(titleIDs))
	for i, id := range titleIDs {
		args[i] = id
	}
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT id, season FROM episode WHERE title_id IN (`+
		store.Placeholders(len(titleIDs))+`)`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var season int
		if err := rows.Scan(&id, &season); err != nil {
			rows.Close()
			return nil, err
		}
		out[id] = season
	}
	return out, closeRows(rows)
}

// filesFor answers the file lists: Radarr moviefile?movieId= (repeatable) / movieFileIds=, Sonarr
// episodefile?seriesId= / episodeFileIds=, and {id}. ids filters by file id; titles by title.
func (s *Service) filesFor(ctx context.Context, v *View, titles []int64, ids []int64) ([]*fileRow, map[int64]*entry,
	error) {
	var tids []int64
	if titles != nil {
		tids = titles
	}
	fs, err := s.viewFiles(ctx, v, tids)
	if err != nil {
		return nil, nil, err
	}
	if ids != nil {
		keep := map[int64]bool{}
		for _, id := range ids {
			keep[id] = true
		}
		var out []*fileRow
		for _, x := range fs {
			if keep[x.id] {
				out = append(out, x)
			}
		}
		fs = out
	}
	// A file shared by two versions of the view is one file (the arrs have one file per movie or episode).
	seen := map[int64]bool{}
	var out []*fileRow
	need := map[int64]bool{}
	for _, x := range fs {
		if !seen[x.id] {
			seen[x.id] = true
			out = append(out, x)
			need[x.titleID] = true
		}
	}
	var load []int64
	for id := range need {
		load = append(load, id)
	}
	sort.Slice(load, func(i, j int) bool { return load[i] < load[j] })
	entries := map[int64]*entry{}
	if len(load) > 0 {
		es, err := s.load(ctx, v, load)
		if err != nil {
			return nil, nil, err
		}
		for _, e := range es {
			entries[e.id] = e
		}
	}
	return out, entries, nil
}

// GET moviefile?movieId=&movieFileIds= (MovieFileController.GetMovieFiles) and GET moviefile/{id}.
func (s *Service) movieFiles(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	if id, ok := r.pathID(1); ok {
		fs, es, err := s.filesFor(ctx, r.v, nil, []int64{id})
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if len(fs) == 0 {
			notFound(w, "MovieFile not found")
			return
		}
		e := es[fs[0].titleID]
		_, p := s.rootAndPath(r.v, e)
		writeJSON(w, http.StatusOK, movieFileFrom(e, fs[0].f, p))
		return
	}
	movies := r.queryInts("movieId")
	fileIDs := r.queryInts("movieFileIds")
	if len(movies) == 0 && len(fileIDs) == 0 {
		badRequest(w, "", "movieId or movieFileIds must be provided", nil)
		return
	}
	var titles, ids []int64
	if len(movies) > 0 {
		titles = []int64{}
		for _, m := range movies {
			tid, err := s.resolve(ctx, r.v, m)
			if err != nil {
				s.internal(w, r, err)
				return
			}
			if tid > 0 {
				titles = append(titles, tid)
			}
		}
	} else {
		ids = fileIDs
	}
	fs, es, err := s.filesFor(ctx, r.v, titles, ids)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []*movieFileResource{}
	for _, x := range fs {
		e := es[x.titleID]
		if e == nil {
			continue
		}
		_, p := s.rootAndPath(r.v, e)
		out = append(out, movieFileFrom(e, x.f, p))
	}
	writeJSON(w, http.StatusOK, out)
}

// GET episodefile?seriesId=&episodeFileIds= (EpisodeFileController.GetEpisodeFiles) and GET episodefile/{id}.
func (s *Service) episodeFiles(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	var titles, ids []int64
	one := false
	if id, ok := r.pathID(1); ok {
		ids, one = []int64{id}, true
	} else if sid, ok := r.query("seriesId"); ok {
		n, _ := strconv.ParseInt(sid, 10, 64)
		tid, err := s.resolve(ctx, r.v, n)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		titles = []int64{tid}
	} else if ids = r.queryInts("episodeFileIds"); len(ids) == 0 {
		badRequest(w, "", "seriesId or episodeFileIds must be provided", nil)
		return
	}
	fs, es, err := s.filesFor(ctx, r.v, titles, ids)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var tids []int64
	for id := range es {
		tids = append(tids, id)
	}
	seasons, err := s.episodeSeasons(ctx, tids)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []episodeFileResource{}
	for _, x := range fs {
		out = append(out, s.episodeFileFrom(r.v, es[x.titleID], x, seasons))
	}
	if one {
		if len(out) == 0 {
			notFound(w, "EpisodeFile not found")
			return
		}
		writeJSON(w, http.StatusOK, out[0])
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// DELETE moviefile/{id}, episodefile/{id} and their bulk forms (body movieFileIds / episodeFileIds): the file leaves
// the view's version, as unlinking only (docs/design/arrview.md): the library link is moved to the trash through the library's
// gate, and what it points at is never touched. While the gate refuses (library disabled, version in shadow), nothing
// changes and the answer is 409 with the reason, so a client does not go on as if the file were gone. Applied, our
// records follow: the link row is unlinked, an arr file row is dropped, and the version or its episodes are wanted
// again only when no other file remains. 200 with no body ({} for bulk).
func (s *Service) deleteFiles(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	bulk := len(r.seg) == 2 && r.seg[1] == "bulk"
	var ids []int64
	if bulk {
		var body map[string][]int64
		if err := decodeBody(r, &body); err != nil {
			badRequest(w, "", err.Error(), nil)
			return
		}
		for k, v := range body {
			if strings.EqualFold(k, "movieFileIds") || strings.EqualFold(k, "episodeFileIds") {
				ids = append(ids, v...)
			}
		}
		if len(ids) == 0 {
			badRequest(w, "", "fileIds must be provided", nil)
			return
		}
	} else if id, ok := r.pathID(1); ok {
		ids = []int64{id}
	} else {
		// An id that does not parse (0, -1, undefined) names no file; never let it reach filesFor as nil (= all).
		notFound(w, "File not found")
		return
	}
	fs, _, err := s.filesFor(ctx, r.v, nil, ids)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(fs) == 0 {
		notFound(w, "File not found")
		return
	}
	if s.library == nil {
		writeJSON(w, http.StatusConflict, errorBody{Message: "the library is not available: nothing unlinked"})
		return
	}
	// Every version of the view that holds the file loses it: all rows with that id, one per version (a link shared
	// by versions is one file id with a row for each), read for the files' titles only.
	var tids []int64
	for _, x := range fs {
		if !slices.Contains(tids, x.titleID) {
			tids = append(tids, x.titleID)
		}
	}
	all, err := s.viewFiles(ctx, r.v, tids)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var rows []*fileRow
	for _, x := range fs {
		for _, y := range all {
			if y.id == x.id {
				rows = append(rows, y)
			}
		}
	}
	// Every row is checked before the first unlink, as a title delete does: a refusal changes nothing.
	for _, y := range rows {
		if err := s.library.CheckUnlink(y.version, y.f.path); err != nil {
			if errors.Is(err, library.ErrRefused) {
				writeJSON(w, http.StatusConflict, errorBody{Message: "dry run: nothing unlinked", Description: err.Error()})
			} else {
				s.internal(w, r, err)
			}
			return
		}
	}
	reason := "deleted through " + r.v.Key + " by " + clientName(r)
	for _, y := range rows {
		// A link shared by versions is unlinked once; the next version's unlink finds it gone, which is no error.
		res, err := s.library.Unlink(ctx, y.version, y.f.path, y.linkID, reason)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if res.DryRun {
			writeJSON(w, http.StatusConflict, errorBody{Message: "dry run: nothing unlinked",
				Description: res.Refused})
			return
		}
		if err := s.fileGone(ctx, y); err != nil {
			s.internal(w, r, err)
			return
		}
		s.emitFileDelete(ctx, r.v, y)
	}
	if bulk {
		writeJSON(w, http.StatusOK, struct{}{})
		return
	}
	w.WriteHeader(http.StatusOK)
}

// fileGone updates our records once a file was unlinked: an arr file row is dropped; the movie version, or the
// episodes of a series version the file held, are wanted again only when no other file remains.
func (s *Service) fileGone(ctx context.Context, x *fileRow) error {
	now := store.Millis(s.now())
	return s.st.WriteTx(ctx, func(tx *sqlTx) error {
		if x.linkID == 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM version_file WHERE id = ?`, x.f.id); err != nil {
				return err
			}
		}
		if len(x.episodes) == 0 {
			_, err := tx.ExecContext(ctx, `UPDATE version SET state = 'wanted', updated_at = ? WHERE id = ?
				AND NOT EXISTS (SELECT 1 FROM link l JOIN link_version lv ON lv.link_id = l.id
					WHERE lv.version_id = ? AND l.state = 'active')
				AND NOT EXISTS (SELECT 1 FROM version_file WHERE version_id = ?)`, now, x.versionID, x.versionID,
				x.versionID)
			return err
		}
		for _, e := range x.episodes {
			if _, err := tx.ExecContext(ctx, `UPDATE episode_version_state SET state = 'wanted', cutoff_met = 0,
				updated_at = ? WHERE version_id = ? AND episode_id = ?
				AND NOT EXISTS (SELECT 1 FROM link l JOIN link_version lv ON lv.link_id = l.id
					JOIN link_episode le ON le.link_id = l.id
					WHERE lv.version_id = ? AND le.episode_id = ? AND l.state = 'active')
				AND NOT EXISTS (SELECT 1 FROM version_file vf JOIN version_file_episode fe ON fe.version_file_id = vf.id
					WHERE vf.version_id = ? AND fe.episode_id = ?)`, now, x.versionID, e, x.versionID, e,
				x.versionID, e); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE version SET state = CASE
			WHEN EXISTS (SELECT 1 FROM episode_version_state s JOIN episode e ON e.id = s.episode_id
				WHERE s.version_id = ? AND s.monitored = 1 AND s.state = 'wanted'
				AND e.air_date_utc_at IS NOT NULL AND e.air_date_utc_at <= ?) THEN 'wanted'
			WHEN EXISTS (SELECT 1 FROM episode_version_state WHERE version_id = ? AND state = 'filled') THEN 'filled'
			WHEN EXISTS (SELECT 1 FROM episode_version_state WHERE version_id = ? AND state = 'cutoff_met') THEN 'cutoff_met'
			ELSE 'wanted' END, updated_at = ? WHERE id = ?`, x.versionID, now, x.versionID, x.versionID, now, x.versionID)
		return err
	})
}
