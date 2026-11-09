package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/library/naming"
	"github.com/theatarr/theatarr/internal/library/probe"
)

// nameMovie and nameSeries are the single naming path used by imports and rename previews.
func (l *Library) nameMovie(ctx context.Context, v *importVersion, vn *VersionNaming, root, rel string,
	facts decide.FileFacts, p *probe.Result, formats []string) (string, error) {
	q := naming.Quality{Title: vn.QualityTitle(facts.Quality), Version: max(facts.Revision, 1), Real: facts.Real}
	mf := &naming.MovieFile{Quality: q, ReleaseGroup: facts.ReleaseGroup, Edition: facts.Edition,
		SceneName: facts.SceneName, RelativePath: filepath.Base(rel), CustomFormats: formats}
	if p != nil {
		mf.MediaInfo = p.Model().Format(mf.SceneOrFileName(), false)
	}
	m := &naming.Movie{Title: v.Title.Name, Year: v.Title.Year, ImdbID: v.Title.ImdbID, TmdbID: v.Title.TmdbID}
	folder, err := titleFolder(ctx, l.st.Reader(), v.VersionID, v.Title.ID, "movie", root)
	if err != nil {
		return "", err
	}
	if folder == "" {
		if folder, err = naming.MovieFolder(&vn.Config, m); err != nil {
			return "", err
		}
	}
	name, err := naming.MovieFileName(&vn.Config, m, mf)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, folder, name+filepath.Ext(rel)), nil
}

func (l *Library) nameSeries(ctx context.Context, v *importVersion, vn *VersionNaming, root, rel string,
	facts decide.FileFacts, p *probe.Result, formats []string, eps []episodeInfo) (string, error) {
	stype := naming.SeriesType(v.Title.SeriesType)
	if stype == "" {
		stype = naming.SeriesStandard
	}
	s := &naming.Series{Title: v.Title.Name, Year: v.Title.Year, Type: stype, ImdbID: v.Title.ImdbID,
		TvdbID: v.Title.TvdbID, TmdbID: v.Title.TmdbID}
	var neps []naming.Episode
	for _, e := range eps {
		neps = append(neps, naming.Episode{Season: e.Season, Number: e.Number, Absolute: e.Absolute, Title: e.Name, AirDate: e.AirDate})
	}
	q := naming.Quality{Title: vn.QualityTitle(facts.Quality), Version: max(facts.Revision, 1), Real: facts.Real}
	ef := &naming.EpisodeFile{Quality: q, ReleaseGroup: facts.ReleaseGroup, ReleaseHash: facts.ReleaseHash,
		SceneName: facts.SceneName, RelativePath: filepath.Base(rel), CustomFormats: formats}
	if p != nil {
		ef.MediaInfo = p.Model().Format(ef.SceneOrFileName(), true)
	}
	folder, err := titleFolder(ctx, l.st.Reader(), v.VersionID, v.Title.ID, "series", root)
	if err != nil {
		return "", err
	}
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
	name, err := naming.SeriesFileName(&vn.Config, s, neps, ef, filepath.Ext(rel), seasonPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(seasonPath, name), nil
}

func metadataFacts(raw string) (decide.ExistingFile, error) {
	var f decide.ExistingFile
	if raw == "" || !json.Valid([]byte(raw)) {
		return f, fmt.Errorf("invalid file metadata")
	}
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return f, err
	}
	if f.Quality == "" || f.RelativePath == "" {
		return f, fmt.Errorf("incomplete file metadata")
	}
	return f, nil
}

func probeResult(ctx context.Context, db *sql.DB, linkID int64) (*probe.Result, error) {
	var raw sql.NullString
	err := db.QueryRowContext(ctx, `SELECT cf.probe FROM link l LEFT JOIN content_file cf ON cf.id = l.content_file_id WHERE l.id = ?`, linkID).Scan(&raw)
	if err != nil || !raw.Valid || raw.String == "" || raw.String == "{}" {
		return nil, err
	}
	var p probe.Result
	if err := json.Unmarshal([]byte(raw.String), &p); err != nil {
		return nil, err
	}
	return &p, nil
}
