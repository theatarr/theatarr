package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Edit file facts (docs/design/library.md "Correcting an installed file"): a manager corrects the quality, revision,
// languages and release group an installed file was imported with. They are the link's stored file inputs
// (link.quality and file_metadata), which nothing parses again after the import (a re-probe writes only the
// content file's probe), so the correction stays until the file is replaced. The formats and score are calculated
// again from them, and each library the file serves judges its cutoff again at once.

// FileFactsEdit is an installed file's corrected facts: its quality name (of the title kind's quality list), its
// revision (1, or 2 and more for a Proper or Repack, as manual import sets it), languages (names) and release group.
type FileFactsEdit struct {
	Quality      string
	Revision     int
	Languages    []string
	ReleaseGroup string
}

// FileFactsEdited is what EditFileFacts did: the facts before and after, and each library's own format score of the
// file (their profiles differ), in the link's library order.
type FileFactsEdited struct {
	LinkID   int64
	TitleID  int64
	Path     string
	Versions []string
	Before   decide.ExistingFile
	After    decide.ExistingFile
	Scores   []int
}

// EditFileFacts stores the corrected facts of one of the title's active links and recalculates what follows from
// them: the formats and score (link.score and the stored formats are those of the link's first library), each
// library's state against its cutoff and its below-the-quality-cutoff flags. Nothing is renamed or moved.
func (l *Library) EditFileFacts(ctx context.Context, linkID int64, e FileFactsEdit) (*FileFactsEdited, error) {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.editFileFacts(pinned, linkID, e)
	}
	return l.editFileFacts(ctx, linkID, e)
}

func (l *Library) editFileFacts(ctx context.Context, linkID int64, e FileFactsEdit) (*FileFactsEdited, error) {
	refuse := func(format string, args ...any) error {
		return &Refused{Reason: "facts", Detail: fmt.Sprintf(format, args...)}
	}
	db := l.st.Reader()
	var raw, kind, state, release, quality, path, target, hash string
	var size int64
	out := &FileFactsEdited{LinkID: linkID}
	err := db.QueryRowContext(ctx, `SELECT l.title_id, t.kind, l.state, COALESCE(l.file_metadata, '{}'),
		COALESCE(l.release_title, ''), COALESCE(l.quality, ''), l.path, l.target, COALESCE(cf.size_bytes, 0),
		COALESCE(c.info_hash, '')
		FROM link l JOIN title t ON t.id = l.title_id LEFT JOIN content_file cf ON cf.id = l.content_file_id
		LEFT JOIN content c ON c.id = cf.content_id
		WHERE l.id = ?`, linkID).Scan(&out.TitleID, &kind, &state, &raw, &release, &quality, &path, &target, &size, &hash)
	if err != nil {
		return nil, err
	}
	out.Path = path
	if state != "active" {
		return out, refuse("the file is no longer in the library")
	}
	var versions []stateVersion
	if err := scanRows(ctx, db, `SELECT v.id, v.title_id, v.policy_key FROM link_version lv
		JOIN version v ON v.id = lv.version_id WHERE lv.link_id = ? ORDER BY v.id`, []any{linkID}, func(r *sql.Rows) error {
		var v stateVersion
		err := r.Scan(&v.id, &v.titleID, &v.key)
		versions = append(versions, v)
		return err
	}); err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return out, refuse("the file is in no library")
	}
	for _, v := range versions {
		if l.pol.Versions[v.key] == nil {
			return out, refuse("the library %s is no longer configured", v.key)
		}
		out.Versions = append(out.Versions, v.key)
	}
	fl := format.Radarr
	if policy.Kind(kind) == policy.KindSeries {
		fl = format.Sonarr
	}
	q, ok := profile.QualityByName(fl, e.Quality)
	if !ok {
		return out, refuse("%q is not a quality", e.Quality)
	}
	f, err := decide.LinkFileInputs([]byte(raw), kind, release, quality)
	if err != nil {
		return nil, fmt.Errorf("library: link %d metadata: %w", linkID, err)
	}
	if f.Size == 0 {
		f.Size = size
	}
	out.Before = f
	f.Quality = q.Name
	f.Revision = max(e.Revision, 1)
	// A Proper or Repack stays one while the revision says so; Real is the release's own word and is kept.
	f.IsRepack = f.IsRepack && f.Revision > 1
	f.Languages = slices.Clone(e.Languages)
	f.ReleaseGroup = e.ReleaseGroup

	// Each library's own score; the first library's formats and score are stored with the facts.
	t, err := decide.LoadTitle(ctx, db, out.TitleID)
	if err != nil {
		return nil, err
	}
	var episodes []int64
	if policy.Kind(kind) == policy.KindSeries {
		if err := scanRows(ctx, db, `SELECT episode_id FROM link_episode WHERE link_id = ? ORDER BY episode_id`,
			[]any{linkID}, func(r *sql.Rows) error {
				var id int64
				err := r.Scan(&id)
				episodes = append(episodes, id)
				return err
			}); err != nil {
			return nil, err
		}
	}
	stored := f
	for i, v := range versions {
		x := f
		if x.RelativePath == "" {
			x.RelativePath = path
		}
		if x.OriginalFilePath == "" {
			x.OriginalFilePath = target
		}
		if l.engine == nil {
			break
		}
		vp := l.pol.Versions[v.key]
		if vp.PreferSeaDex {
			if x.InfoHash == "" { // as the link loaders read it (decide.loadLinksOf)
				x.InfoHash = hash
			}
			// As adoption scores it: the file's SeaDex standing for its episodes (a film's for the title), then the score.
			if err := l.resolveFactsStanding(ctx, v, vp, episodes, &x); err != nil {
				return nil, err
			}
		}
		if err := l.engine.ScoreFile(&t, decide.Version{ID: v.id, Key: v.key, Policy: vp}, &x); err != nil {
			return nil, err
		}
		if i == 0 {
			stored.Formats, stored.Score = x.Formats, x.Score
		}
		out.Scores = append(out.Scores, *x.Score)
	}
	out.After = stored
	b, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}
	formats := "[]" // an empty list, not left out: the file was scored and matched none
	if len(stored.Formats) > 0 {
		fb, err := json.Marshal(stored.Formats)
		if err != nil {
			return nil, err
		}
		formats = string(fb)
	}
	var score any
	if stored.Score != nil {
		score = *stored.Score
	}
	res, err := l.st.Writer().ExecContext(ctx, `UPDATE link SET quality = ?, score = COALESCE(?, score),
		file_metadata = json_set(?, '$.Formats', json(?)), updated_at = ? WHERE id = ? AND state = 'active'`,
		q.Name, score, string(b), formats, store.Millis(l.now()), linkID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return out, refuse("the file is no longer in the library")
	}
	for _, v := range versions {
		if _, err := l.recomputeVersion(ctx, v); err != nil {
			return nil, err
		}
	}
	l.log.Info("library: file facts corrected", "link", linkID, "quality", q.Name, "revision", f.Revision)
	return out, nil
}

// resolveFactsStanding gives x its SeaDex standing in the prefer-SeaDex library v, as adoption does before scoring
// (recordAdopted): the operation's stored standing, then the file's for the link's episodes, or a film's.
func (l *Library) resolveFactsStanding(ctx context.Context, v stateVersion, vp *policy.VersionPolicy, episodes []int64,
	x *decide.ExistingFile) error {
	ctx, err := decide.WithStoredSeaDex(ctx, l.st.Reader())
	if err != nil {
		return err
	}
	ctx = decide.WithSeaDexTitle(decide.WithSeaDexPolicy(ctx, l.pol, vp), v.titleID)
	if err := decide.SeaDexError(ctx); err != nil {
		return err
	}
	if vp.Kind != policy.KindSeries {
		return decide.ResolveSeaDexFiles(ctx, l.st.Reader(), []*decide.ExistingFile{x})
	}
	t, err := l.cutoffTitle(ctx, v.titleID, v.id, vp)
	if err != nil {
		return err
	}
	decide.ResolveSeaDexFileEpisodes(ctx, t, x, episodes)
	return nil
}
