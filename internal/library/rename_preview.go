package library

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/store"
)

// RenameFile is one active link whose name differs from the version's current naming.
type RenameFile struct {
	LinkID       int64
	Version      string
	ExistingPath string
	NewPath      string
	SeasonNumber int
	EpisodeNums  []int
}

// RenamePreview is a read-only rename plan. Skipped counts links whose saved inputs cannot be rebuilt safely.
type RenamePreview struct {
	Files   []RenameFile
	Skipped int
}

// PreviewRename rebuilds names from the link's frozen import inputs and the current version naming.
func (l *Library) PreviewRename(ctx context.Context, titleID int64, season *int) (*RenamePreview, error) {
	return l.previewRename(ctx, titleID, season, nil)
}

// PreviewRenameVersions limits a preview to the supplied version keys, as an arr view does.
func (l *Library) PreviewRenameVersions(ctx context.Context, titleID int64, season *int, versions []string) (*RenamePreview, error) {
	allowed := make(map[string]bool, len(versions))
	for _, key := range versions {
		allowed[key] = true
	}
	return l.previewRename(ctx, titleID, season, allowed)
}

func (l *Library) previewRename(ctx context.Context, titleID int64, season *int, allowed map[string]bool) (*RenamePreview, error) {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.previewRename(pinned, titleID, season, allowed)
	}
	out := &RenamePreview{Files: []RenameFile{}}
	rows, err := l.st.Reader().QueryContext(ctx, `
		SELECT l.id, l.path, v.id, v.policy_key, t.kind, t.name, COALESCE(t.year, 0), COALESCE(t.imdb_id, ''),
			COALESCE(t.tmdb_id, 0), COALESCE(t.tvdb_id, 0), `+store.SeriesTypeSQL("t")+`, l.file_metadata
		FROM link l JOIN link_version lv ON lv.link_id = l.id JOIN version v ON v.id = lv.version_id
		JOIN title t ON t.id = l.title_id WHERE l.title_id = ? AND l.state = 'active' ORDER BY l.id`, titleID)
	if err != nil {
		return nil, err
	}
	// Read every link first and close the rows: the per-link lookups below take their own reader connection,
	// and holding one while waiting for another deadlocks the pool once enough previews run at once.
	type previewLink struct {
		linkID       int64
		oldPath, raw string
		v            importVersion
	}
	var links []previewLink
	for rows.Next() {
		pl := previewLink{v: importVersion{Title: titleInfo{ID: titleID}}}
		t := &pl.v.Title
		if err := rows.Scan(&pl.linkID, &pl.oldPath, &pl.v.VersionID, &pl.v.Key, &t.Kind, &t.Name, &t.Year, &t.ImdbID,
			&t.TmdbID, &t.TvdbID, &t.SeriesType, &pl.raw); err != nil {
			rows.Close()
			return nil, err
		}
		if allowed != nil && !allowed[pl.v.Key] {
			continue
		}
		links = append(links, pl)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, pl := range links {
		linkID, oldPath, key, kind, v := pl.linkID, pl.oldPath, pl.v.Key, pl.v.Title.Kind, pl.v
		facts, err := metadataFacts(pl.raw)
		if err != nil {
			out.Skipped++
			continue
		}
		var p *probe.Result
		p, err = probeResult(ctx, l.st.Reader(), linkID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			out.Skipped++
			continue
		}
		vn, err := NamingFor(ctx, l.st.Reader(), l.pol, key)
		if err != nil {
			return nil, err
		}
		root := ""
		for _, r := range l.gate.Roots(key) {
			if rel, e := filepath.Rel(r, oldPath); e == nil && rel != "." && !escapes(rel) {
				root = r
				break
			}
		}
		if root == "" {
			out.Skipped++
			continue
		}
		var episodes []episodeInfo
		if kind == "series" {
			episodes, err = linkEpisodes(ctx, l.st.Reader(), linkID)
			if err != nil {
				return nil, err
			}
			if len(episodes) == 0 {
				out.Skipped++
				continue
			}
			if season != nil && !hasSeason(episodes, *season) {
				continue
			}
		}
		var path string
		formats := l.storedRenameFormats(ctx, key, facts)
		if kind == "movie" {
			path, err = l.nameMovie(ctx, &v, vn, root, facts.RelativePath, factsToFileFacts(facts), p, formats)
		} else {
			path, err = l.nameSeries(ctx, &v, vn, root, facts.RelativePath, factsToFileFacts(facts), p, formats, episodes)
		}
		if err != nil {
			out.Skipped++
			continue
		}
		if filepath.Clean(path) != filepath.Clean(oldPath) {
			rf := RenameFile{LinkID: linkID, Version: key, ExistingPath: oldPath, NewPath: path}
			if len(episodes) > 0 {
				rf.SeasonNumber = episodes[0].Season
				for _, e := range episodes {
					rf.EpisodeNums = append(rf.EpisodeNums, e.Number)
				}
			}
			out.Files = append(out.Files, rf)
		}
	}
	return out, nil
}

// ApplyRename applies a title's preview through the same gated link writer.
func (l *Library) ApplyRename(ctx context.Context, titleID int64, season *int) (*RenamePreview, error) {
	return l.applyRename(ctx, titleID, season, nil)
}

// ApplyRenameVersions applies only links belonging to the supplied version keys.
func (l *Library) ApplyRenameVersions(ctx context.Context, titleID int64, season *int, versions []string) (*RenamePreview, error) {
	allowed := make(map[string]bool, len(versions))
	for _, key := range versions {
		allowed[key] = true
	}
	return l.applyRename(ctx, titleID, season, allowed)
}

func (l *Library) applyRename(ctx context.Context, titleID int64, season *int, allowed map[string]bool) (*RenamePreview, error) {
	if l.runtime != nil {
		pinned, target := l.runtime(ctx)
		return target.applyRename(pinned, titleID, season, allowed)
	}
	p, err := l.previewRename(ctx, titleID, season, allowed)
	if err != nil {
		return nil, err
	}
	for _, f := range p.Files {
		if err := l.gate.CheckPath(f.Version, f.NewPath); err != nil {
			return p, err
		}
	}
	for _, f := range p.Files {
		if err := l.RenameLink(ctx, f.LinkID, f.NewPath); err != nil {
			return p, err
		}
	}
	return p, nil
}

func factsToFileFacts(f decide.ExistingFile) decide.FileFacts {
	return decide.FileFacts{Quality: f.Quality, Revision: f.Revision, Real: f.Real, ReleaseGroup: f.ReleaseGroup,
		ReleaseHash: f.ReleaseHash, Edition: f.Edition, SceneName: f.SceneName, Languages: f.Languages}
}

func linkEpisodes(ctx context.Context, db *sql.DB, linkID int64) ([]episodeInfo, error) {
	rows, err := db.QueryContext(ctx, `SELECT e.id, e.season, e.number, e.name, COALESCE(e.air_date, ''),
		COALESCE(e.runtime_minutes, 0), (SELECT n.number FROM episode_number n WHERE n.episode_id = e.id AND n.scheme = 'absolute_tvdb' ORDER BY n.id LIMIT 1)
		FROM link_episode le JOIN episode e ON e.id = le.episode_id WHERE le.link_id = ? ORDER BY e.season, e.number`, linkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []episodeInfo
	for rows.Next() {
		var e episodeInfo
		var abs sql.NullInt64
		if err := rows.Scan(&e.ID, &e.Season, &e.Number, &e.Name, &e.AirDate, &e.Runtime, &abs); err != nil {
			return nil, err
		}
		if abs.Valid {
			x := int(abs.Int64)
			e.Absolute = &x
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func hasSeason(eps []episodeInfo, season int) bool {
	for _, e := range eps {
		if e.Season == season {
			return true
		}
	}
	return false
}

// storedRenameFormats gives the {Custom Formats} names for a link's stored formats as import names them: through the
// same include-when-renaming filter (l.matched), so a rename does not add scoring-only formats to the name.
func (l *Library) storedRenameFormats(ctx context.Context, version string, facts decide.ExistingFile) []string {
	names := make([]string, 0, len(facts.Formats))
	for _, f := range facts.Formats {
		names = append(names, f.Name)
	}
	if l.matched != nil {
		return l.matched(ctx, version, names)
	}
	return names
}
