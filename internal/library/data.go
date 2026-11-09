package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// ReadyState is the download state the import picks up (migration 00014: the router sets it once the provider
// holds every selected file). The library then sets importing, and imported or failed.
const ReadyState = "ready"

type titleInfo struct {
	ID         int64
	Kind       string
	Name       string
	Year       int
	ImdbID     string
	TmdbID     int
	TvdbID     int
	SeriesType string
	Runtime    int // minutes, 0 when unknown
}

type episodeInfo struct {
	ID       int64
	Season   int
	Number   int
	Absolute *int
	Name     string
	AirDate  string
	Runtime  int
}

// fileIdentity is one download_file row: a file's episodes and how they were found.
type fileIdentity struct {
	Episodes  []int64
	How       string
	Overrides *decide.ManualOverrides
}

type importVersion struct {
	VersionID int64
	Key       string
	Policy    *policy.VersionPolicy
	Title     titleInfo
	Episodes  []episodeInfo // the download's episodes (series)
	Quality   string        // the decision's quality name
	Score     int
}

type importDownload struct {
	ID           int64
	State        string
	Provider     string
	Client       string // the download client of a client-routed download (download.client), "" otherwise
	ReleaseTitle string
	InfoHash     string
	Indexer      string
	Name         string // the provider's torrent name: the mount folder
	// OriginalName is the name the torrent declared (download.original_name), "" when no read gave one: zurg names
	// the folder after it when the provider's name differs.
	OriginalName string
	ContentID    int64
	ContentName  string
	FolderNames  []string // provider torrent names from the registry, the download's provider first
	MountFolder  string   // content.mount_folder when known
	// Formats are the custom formats the router's decision matched (download.formats); HasFormats is false for a
	// download routed without them, which names files by deciding the release title again.
	Formats    []string
	HasFormats bool
	// EpisodeMap is the decision's own mapping of a series release (download.episode_map), nil for Sonarr's.
	EpisodeMap *decide.EpisodeMap
	// rescoped are the download's episodes Sonarr's numbering gives the release once its EpisodeMap is abandoned
	// (rescope); a real import writes them to download.episodes. Nil when the scope was not changed.
	rescoped []int64
	// SourcePath is the path a path or manual import named on the mount (download.source_path), "" when the release
	// is found by its hash and names.
	SourcePath string
	// ReadyAt is when the router recorded the download as ready (download.ready_at); zero when unknown.
	ReadyAt time.Time
	// Manual are a client's explicit files (download_file, source manual) by their path in the release folder: only
	// these import, with these episodes. Recorded are the identities an earlier import of the download linked.
	Manual, Recorded map[string]fileIdentity
	Version          importVersion
	// claim is the library_claim owner this import holds ("" for a dry run, which writes nothing).
	claim string
	// claimed is when the claim was last taken or renewed (renewClaim).
	claimed time.Time
	// linked memoizes linkedMetadata: the upgrade check at planning and apply read the same inputs.
	linked *linkedInputs
}

// loadDownload reads a download (00014: one version per download) with its title and episodes.
func loadDownload(ctx context.Context, db *sql.DB, pol *policy.Policy, id int64) (*importDownload, error) {
	d := &importDownload{ID: id}
	v := &d.Version
	var provider, client sql.NullString
	var eps string
	var formats, epMap, srcPath, origName sql.NullString
	var readyAt sql.NullInt64
	err := db.QueryRowContext(ctx, `
		SELECT d.state, d.provider, d.client, d.release_title, d.info_hash, d.indexer, d.name, d.quality, d.score, d.episodes,
		       d.formats, d.episode_map, d.source_path, d.ready_at, d.version_id, d.version_key, d.title_id, d.original_name
		FROM download d WHERE d.id = ?`, id).
		Scan(&d.State, &provider, &client, &d.ReleaseTitle, &d.InfoHash, &d.Indexer, &d.Name, &v.Quality, &v.Score, &eps,
			&formats, &epMap, &srcPath, &readyAt, &v.VersionID, &v.Key, &v.Title.ID, &origName)
	if err != nil {
		return nil, fmt.Errorf("library: download %d: %w", id, err)
	}
	d.Provider, d.Client, d.InfoHash = provider.String, client.String, strings.ToLower(d.InfoHash)
	d.SourcePath, d.ReadyAt, d.OriginalName = srcPath.String, store.NullMillis(readyAt), origName.String
	if d.Manual, d.Recorded, err = loadFiles(ctx, db, id); err != nil {
		return nil, err
	}
	var ids []int64
	_ = json.Unmarshal([]byte(eps), &ids)
	if err := completeDownload(ctx, db, pol, d, ids, formats, epMap); err != nil {
		return nil, fmt.Errorf("library: download %d: %w", id, err)
	}
	return d, nil
}

// NewImport is a download an import would make (the import API's row), for a dry run that plans it without
// writing any download row: a ready row, even for the moment a plan takes, is work the import pass may pick up, and
// one left by a cancelled request or a crash would be imported for real.
type NewImport struct {
	TitleID, VersionID int64
	Version            string
	InfoHash           string
	ReleaseTitle       string
	Provider, Name     string
	SourcePath         string
	Quality            string
	Score              int
	Episodes           []int64
	// Formats and EpisodeMap are the download.formats and download.episode_map JSON ("" for none).
	Formats, EpisodeMap string
	// Files are the client's files (download_file, source manual) by their path in the release folder.
	Files     map[string][]int64
	Overrides map[string]*decide.ManualOverrides
}

func (n *NewImport) download(ctx context.Context, db *sql.DB, pol *policy.Policy) (*importDownload, error) {
	d := &importDownload{Provider: n.Provider, ReleaseTitle: n.ReleaseTitle, InfoHash: strings.ToLower(n.InfoHash),
		Indexer: "import", Name: n.Name, SourcePath: n.SourcePath}
	v := &d.Version
	v.VersionID, v.Key, v.Quality, v.Score, v.Title.ID = n.VersionID, n.Version, n.Quality, n.Score, n.TitleID
	for rel, eps := range n.Files {
		if d.Manual == nil {
			d.Manual = map[string]fileIdentity{}
		}
		d.Manual[rel] = fileIdentity{Episodes: eps, How: HowManual, Overrides: n.Overrides[rel]}
	}
	null := func(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
	if err := completeDownload(ctx, db, pol, d, n.Episodes, null(n.Formats), null(n.EpisodeMap)); err != nil {
		return nil, fmt.Errorf("library: new import: %w", err)
	}
	return d, nil
}

// completeDownload reads what a download needs besides its own row: the title, the registry's names for the hash
// and the episodes (series).
func completeDownload(ctx context.Context, db *sql.DB, pol *policy.Policy, d *importDownload, eps []int64,
	formats, epMap sql.NullString) error {
	v := &d.Version
	err := db.QueryRowContext(ctx, `SELECT kind, name, COALESCE(year, 0), COALESCE(imdb_id, ''), COALESCE(tmdb_id, 0),
		COALESCE(tvdb_id, 0), COALESCE(series_type, ''), COALESCE(runtime_minutes, 0) FROM title WHERE id = ?`,
		v.Title.ID).Scan(&v.Title.Kind, &v.Title.Name, &v.Title.Year, &v.Title.ImdbID, &v.Title.TmdbID,
		&v.Title.TvdbID, &v.Title.SeriesType, &v.Title.Runtime)
	if err != nil {
		return err
	}
	if v.Title.Kind == "series" {
		s, err := store.LoadSourceSettings(ctx, db, v.VersionID)
		if err != nil {
			return err
		}
		if s.SeriesType != "" {
			v.Title.SeriesType = s.SeriesType
		}
	}
	if formats.Valid {
		var fs []struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(formats.String), &fs) == nil {
			d.HasFormats = true
			for _, f := range fs {
				d.Formats = append(d.Formats, f.Name)
			}
		}
	}
	if epMap.Valid {
		var m decide.EpisodeMap
		if json.Unmarshal([]byte(epMap.String), &m) == nil && m.How != "" {
			d.EpisodeMap = &m
		}
	}
	v.Policy = pol.Versions[v.Key]
	if d.InfoHash != "" {
		var name, folder sql.NullString
		err := db.QueryRowContext(ctx, `SELECT id, name, mount_folder FROM content WHERE info_hash = ?`, d.InfoHash).
			Scan(&d.ContentID, &name, &folder)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		d.ContentName, d.MountFolder = name.String, folder.String
		if d.ContentID != 0 {
			rows, err := db.QueryContext(ctx, `SELECT COALESCE(folder_name, '') FROM content_provider
				WHERE content_id = ? ORDER BY CASE WHEN provider = ? THEN 0 ELSE 1 END, provider`, d.ContentID, d.Provider)
			if err != nil {
				return err
			}
			for rows.Next() {
				var f string
				if err := rows.Scan(&f); err != nil {
					rows.Close()
					return err
				}
				if f != "" {
					d.FolderNames = append(d.FolderNames, f)
				}
			}
			rows.Close()
		}
	}
	if v.Title.Kind == "series" {
		var err error
		if v.Episodes, err = loadEpisodes(ctx, db, v.Title.ID, eps); err != nil {
			return err
		}
	}
	return nil
}

// loadFiles reads a download's file identities (download_file), manual and recorded.
func loadFiles(ctx context.Context, db *sql.DB, downloadID int64) (manual, recorded map[string]fileIdentity, err error) {
	rows, err := db.QueryContext(ctx, `SELECT rel, episodes, source, how, overrides FROM download_file WHERE download_id = ?`, downloadID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rel, eps, source, overrides string
		var f fileIdentity
		if err := rows.Scan(&rel, &eps, &source, &f.How, &overrides); err != nil {
			return nil, nil, err
		}
		_ = json.Unmarshal([]byte(eps), &f.Episodes)
		if err := json.Unmarshal([]byte(overrides), &f.Overrides); err != nil {
			return nil, nil, err
		}
		m := &recorded
		if source == "manual" {
			m = &manual
		}
		if *m == nil {
			*m = map[string]fileIdentity{}
		}
		(*m)[rel] = f
	}
	return manual, recorded, rows.Err()
}

// loadEpisodes loads a title's episodes by id; with no ids, every episode of the title (a pack the router did not
// narrow down), excluding removed rows. Explicit ids keep access to retained episodes for manual imports.
func loadEpisodes(ctx context.Context, db *sql.DB, titleID int64, ids []int64) ([]episodeInfo, error) {
	q := `SELECT e.id, e.season, e.number, e.name, COALESCE(e.air_date, ''), COALESCE(e.runtime_minutes, 0),
	             (SELECT n.number FROM episode_number n WHERE n.episode_id = e.id AND n.scheme = 'absolute_tvdb'
	              ORDER BY n.id LIMIT 1)
	      FROM episode e WHERE e.title_id = ?`
	args := []any{titleID}
	if len(ids) > 0 {
		q += ` AND e.id IN (` + store.Placeholders(len(ids)) + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	} else {
		q += ` AND e.removed_at IS NULL`
	}
	rows, err := db.QueryContext(ctx, q+` ORDER BY e.season, e.number`, args...)
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
			v := int(abs.Int64)
			e.Absolute = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// existingFile is a file the version has now: our link or the arr's file.
type existingFile struct {
	LinkID   int64 // 0 for an arr file we have no link row for
	Path     string
	Target   string // a link's target ("" for an arr file)
	Episodes []int64
}

// existingFiles lists the version's current files for a title: active links, and the arr's files (version_file)
// whose paths have no link row.
func existingFiles(ctx context.Context, db *sql.DB, versionID, titleID int64) ([]existingFile, error) {
	var out []existingFile
	seen := map[string]bool{}
	rows, err := db.QueryContext(ctx, `
		SELECT l.id, l.path, l.target, COALESCE((SELECT json_group_array(le.episode_id) FROM link_episode le WHERE le.link_id = l.id), '[]')
		FROM link l JOIN link_version lv ON lv.link_id = l.id
		WHERE lv.version_id = ? AND l.title_id = ? AND l.state = 'active' ORDER BY l.id`, versionID, titleID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var f existingFile
		var eps string
		if err := rows.Scan(&f.LinkID, &f.Path, &f.Target, &eps); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal([]byte(eps), &f.Episodes)
		seen[f.Path] = true
		out = append(out, f)
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, `
		SELECT vf.path, COALESCE((SELECT json_group_array(e.episode_id) FROM version_file_episode e WHERE e.version_file_id = vf.id), '[]')
		FROM version_file vf WHERE vf.version_id = ? AND vf.title_id = ? ORDER BY vf.id`, versionID, titleID)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return out, nil
		}
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f existingFile
		var eps string
		if err := rows.Scan(&f.Path, &eps); err != nil {
			return nil, err
		}
		if seen[f.Path] {
			continue
		}
		_ = json.Unmarshal([]byte(eps), &f.Episodes)
		seen[f.Path] = true
		out = append(out, f)
	}
	return out, rows.Err()
}

// deadFiles lists the version's dead links for a title: links whose file left the mount (relink's dead mark).
func deadFiles(ctx context.Context, db *sql.DB, versionID, titleID int64) ([]existingFile, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT l.id, l.path, l.target, COALESCE((SELECT json_group_array(le.episode_id) FROM link_episode le WHERE le.link_id = l.id), '[]')
		FROM link l JOIN link_version lv ON lv.link_id = l.id
		WHERE lv.version_id = ? AND l.title_id = ? AND l.state = 'dead' ORDER BY l.id`, versionID, titleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []existingFile
	for rows.Next() {
		var f existingFile
		var eps string
		if err := rows.Scan(&f.LinkID, &f.Path, &f.Target, &eps); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(eps), &f.Episodes)
		out = append(out, f)
	}
	return out, rows.Err()
}

// titleFolder returns the folder this version already uses for the title under root: an active link's, or the
// arr's (arr_title.path). The arrs created many folders by other means (copies, imports from older names), and a
// new link must land in the folder Plex already has, so a known folder wins over the computed one.
func titleFolder(ctx context.Context, db *sql.DB, versionID, titleID int64, kind, root string) (string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT l.path FROM link l JOIN link_version lv ON lv.link_id = l.id
		WHERE lv.version_id = ? AND l.title_id = ? AND l.state = 'active' ORDER BY l.id DESC`, versionID, titleID)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return "", err
		}
		dir := filepath.Dir(p)
		if kind == "series" {
			dir = filepath.Dir(dir)
		}
		if filepath.Dir(dir) == root {
			rows.Close()
			return filepath.Base(dir), nil
		}
	}
	rows.Close()
	var p string
	err = db.QueryRowContext(ctx, `SELECT path FROM arr_title WHERE version_id = ? AND title_id = ? AND path != ''
		ORDER BY instance LIMIT 1`, versionID, titleID).Scan(&p)
	if err == nil && filepath.Dir(filepath.Clean(p)) == root {
		return filepath.Base(p), nil
	}
	if err != nil && err != sql.ErrNoRows && !strings.Contains(err.Error(), "no such table") {
		return "", err
	}
	return "", nil
}
