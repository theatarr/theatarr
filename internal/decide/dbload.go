package decide

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/library/naming"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Loading decision inputs from our database (internal/store schema): titles with their cached TMDb details,
// versions, and current files (active links and version_file, from theatarr import-arr). Reads only.

// tmdbDetails is what a title takes from the TMDb movie response the importer cached.
type tmdbDetails struct {
	OriginalTitle    string `json:"original_title"`
	OriginalLanguage string `json:"original_language"`
	Runtime          int    `json:"runtime"`
}

// titleSelect reads normalized metadata, with historical TMDb responses as a fallback for imported movies. A series'
// metadata language is title_meta's (meta.art, TMDb), else the code the series refresh read (series_meta).
var titleSelect = `SELECT t.id, t.kind, t.name, COALESCE(t.year, 0), COALESCE(t.secondary_year, 0),
	COALESCE(t.imdb_id, ''), COALESCE(t.tmdb_id, 0), t.anime, COALESCE(t.tvdb_id, 0), ` + store.SeriesTypeSQL("t") + `,
	t.use_scene_numbering, COALESCE(t.runtime_minutes, 0), COALESCE(t.original_language, ''), t.monitored,
	COALESCE(tm.runtime_minutes, 0), COALESCE(NULLIF(tm.original_language, ''), sm.original_language, ''),
	COALESCE((SELECT a.name FROM title_alias a WHERE a.title_id = t.id AND a.source = 'tmdb-original' LIMIT 1), ''),
	(SELECT m.body FROM metadata_cache m WHERE m.source = 'tmdb' AND m.key IN (
		'/movie/' || t.tmdb_id || '?append_to_response=external_ids%2Crelease_dates%2Calternative_titles%2Ctranslations',
		'/movie/' || t.tmdb_id || '?append_to_response=external_ids%2Crelease_dates')
		ORDER BY length(m.key) DESC LIMIT 1)
	FROM title t LEFT JOIN title_meta tm ON tm.title_id = t.id LEFT JOIN series_meta sm ON sm.title_id = t.id`

// LoadMovies reads every movie, by title id. Runtime and original language prefer title_meta; original title and
// aliases come from title_alias. Historical TMDb cache rows remain a fallback for imported titles.
func LoadMovies(ctx context.Context, db *sql.DB) (map[int64]Title, error) {
	rows, err := db.QueryContext(ctx, titleSelect+` WHERE t.kind = 'movie'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Title{}
	for rows.Next() {
		t, err := scanTitle(rows)
		if err != nil {
			return nil, err
		}
		out[t.ID] = t
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	aliases, err := loadAliases(ctx, db, `SELECT a.title_id, a.name FROM title_alias a JOIN title t ON t.id = a.title_id
		WHERE t.kind = 'movie' ORDER BY a.title_id, a.kind, a.source, a.name`)
	if err != nil {
		return nil, err
	}
	for id, a := range aliases {
		if t, ok := out[id]; ok {
			t.Aliases = a
			out[id] = t
		}
	}
	return out, nil
}

// loadAliases reads (title id, name) rows into each title's distinct names, in row order.
func loadAliases(ctx context.Context, db *sql.DB, q string, args ...any) (map[int64][]string, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]string{}
	seen := map[int64]map[string]bool{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		if seen[id] == nil {
			seen[id] = map[string]bool{}
		}
		if !seen[id][name] {
			seen[id][name] = true
			out[id] = append(out[id], name)
		}
	}
	return out, rows.Err()
}

// LoadTitle reads one title by id.
func LoadTitle(ctx context.Context, db *sql.DB, id int64) (Title, error) {
	rows, err := db.QueryContext(ctx, titleSelect+` WHERE t.id = ?`, id)
	if err != nil {
		return Title{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Title{}, err
		}
		return Title{}, fmt.Errorf("no title %d", id)
	}
	t, err := scanTitle(rows)
	if err != nil {
		return t, err
	}
	rows.Close()
	aliases, err := loadAliases(ctx, db, `SELECT title_id, name FROM title_alias WHERE title_id = ?
		ORDER BY kind, source, name`, id)
	if err != nil {
		return t, err
	}
	t.Aliases = aliases[id]
	return t, nil
}

// LoadSeries reads every series, by title id, without episodes (LoadSeriesVersion adds them for one version).
func LoadSeries(ctx context.Context, db *sql.DB) (map[int64]Title, error) {
	rows, err := db.QueryContext(ctx, titleSelect+` WHERE t.kind = 'series'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Title{}
	for rows.Next() {
		t, err := scanTitle(rows)
		if err != nil {
			return nil, err
		}
		out[t.ID] = t
	}
	return out, rows.Err()
}

// LoadSeriesVersion fills a series title for one of its versions: its scene mappings (every source), its episodes
// with their absolute and scene numbers and the version's file of each (versionID 0: no files). The series type is the
// title's (store.SeriesTypeSQL, read with the title), the same for every version.
func LoadSeriesVersion(ctx context.Context, db *sql.DB, t *Title, versionID int64) error {
	t.cutoff = &cutoffCache{}
	if t.Kind != policy.KindSeries {
		return nil
	}
	t.SceneMappings = nil
	rows, err := db.QueryContext(ctx, `SELECT name, season_number, scene_season_number, scene_origin, comment
		FROM title_scene_mapping WHERE title_id = ? ORDER BY source, rowid`, t.ID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var m SceneMapping
		var season, scene sql.NullInt64
		if err := rows.Scan(&m.Title, &season, &scene, &m.SceneOrigin, &m.Comment); err != nil {
			rows.Close()
			return err
		}
		m.SeasonNumber, m.SceneSeasonNumber = nullIntPtr(season), nullIntPtr(scene)
		k := fmt.Sprintf("%s|%d|%d", m.Title, orMinus1(m.SeasonNumber), orMinus1(m.SceneSeasonNumber))
		if !seen[k] {
			seen[k] = true
			t.SceneMappings = append(t.SceneMappings, m)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if t.OriginalTitle == "" && t.TVDB > 0 {
		if t.OriginalTitle, err = seriesOriginalTitle(ctx, db, t.TVDB); err != nil {
			return err
		}
	}
	eps, err := LoadEpisodes(ctx, db, t.ID, versionID)
	if err != nil {
		return err
	}
	t.Episodes = eps
	// franchise mappings into the series, if any (anime.go)
	if t.AnimeMap, err = animemap.Load(ctx, db, t.TVDB); err != nil {
		return err
	}
	return nil
}

// LoadEpisodes reads a series' episodes in season and number order, with their numbers (episode_number, any
// source) and, for versionID, the version's file of each (active links win over arr files). Removed rows are excluded.
func LoadEpisodes(ctx context.Context, db *sql.DB, titleID, versionID int64) ([]Episode, error) {
	ctx = WithSeaDexTitle(ctx, titleID)
	rows, err := db.QueryContext(ctx, `SELECT e.id, e.season, e.number, e.name, COALESCE(e.air_date, ''),
		e.air_date_utc_at, COALESCE(e.runtime_minutes, 0), COALESCE(s.monitored, e.monitored)
		FROM episode e LEFT JOIN episode_version_state s ON s.episode_id = e.id AND s.version_id = ?
		WHERE e.title_id = ? AND e.removed_at IS NULL ORDER BY e.season, e.number`, versionID, titleID)
	if err != nil {
		return nil, err
	}
	var out []Episode
	idx := map[int64]int{}
	for rows.Next() {
		var e Episode
		var at sql.NullInt64
		if err := rows.Scan(&e.ID, &e.Season, &e.Number, &e.Title, &e.AirDate, &at, &e.Runtime, &e.Monitored); err != nil {
			rows.Close()
			return nil, err
		}
		if at.Valid {
			u := time.UnixMilli(at.Int64).UTC()
			e.AirDateUTC = &u
		}
		idx[e.ID] = len(out)
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	nrows, err := db.QueryContext(ctx, `SELECT n.episode_id, n.scheme, n.season, n.number FROM episode_number n
		WHERE n.title_id = ? AND n.scheme IN ('absolute_tvdb', 'scene', 'scene_absolute') ORDER BY n.source, n.id`, titleID)
	if err != nil {
		return nil, err
	}
	for nrows.Next() {
		var id int64
		var scheme string
		var season sql.NullInt64
		var number int
		if err := nrows.Scan(&id, &scheme, &season, &number); err != nil {
			nrows.Close()
			return nil, err
		}
		i, ok := idx[id]
		if !ok {
			continue
		}
		e := &out[i]
		n := number
		switch scheme {
		case "absolute_tvdb":
			if e.Absolute == 0 {
				e.Absolute = n
			}
		case "scene":
			if e.SceneSeason == nil && season.Valid {
				ss := int(season.Int64)
				e.SceneSeason, e.SceneNumber = &ss, &n
			}
		case "scene_absolute":
			if e.SceneAbsolute == nil {
				e.SceneAbsolute = &n
			}
		}
	}
	nrows.Close()
	if err := nrows.Err(); err != nil {
		return nil, err
	}
	if versionID == 0 {
		return out, nil
	}
	frows, err := db.QueryContext(ctx, `SELECT fe.episode_id, vf.id, vf.quality, vf.quality_revision, vf.scene_name,
		vf.original_file_path, vf.relative_path, vf.release_group, vf.edition, vf.languages, vf.size_bytes,
		vf.indexer_flags, COALESCE(c.info_hash, ''), vf.release_type, COALESCE(vf.added_at, 0)
		FROM version_file vf JOIN version_file_episode fe ON fe.version_file_id = vf.id
		LEFT JOIN version_source vs ON vs.version_id = vf.version_id AND vs.instance = vf.instance
		LEFT JOIN content c ON c.id = vf.content_id
		WHERE vf.version_id = ? ORDER BY vs.version_id IS NULL DESC, COALESCE(vf.added_at, 0), vf.id`, versionID)
	if err != nil {
		return nil, err
	}
	files := map[int64]*ExistingFile{}
	members := map[int64][]int64{}
	for frows.Next() {
		var epID, fid int64
		var f ExistingFile
		var langs string
		var added int64
		if err := frows.Scan(&epID, &fid, &f.Quality, &f.Revision, &f.SceneName, &f.OriginalFilePath, &f.RelativePath,
			&f.ReleaseGroup, &f.Edition, &langs, &f.Size, &f.IndexerFlags, &f.InfoHash, &f.ReleaseType, &added); err != nil {
			frows.Close()
			return nil, err
		}
		if added > 0 {
			f.Added = time.UnixMilli(added).UTC()
		}
		_ = json.Unmarshal([]byte(langs), &f.Languages)
		if files[fid] == nil {
			files[fid] = &f
		}
		members[fid] = append(members[fid], epID)
		// Later rows win: the owner's files come last, newest last.
		if i, ok := idx[epID]; ok {
			out[i].File, out[i].FileID = files[fid], fid
		}
	}
	frows.Close()
	if err := frows.Err(); err != nil {
		return nil, err
	}
	for fid, f := range files {
		f.EpisodeIDs = members[fid]
	}
	links, order, err := loadLinks(ctx, db, versionID)
	if err != nil {
		return nil, err
	}
	for _, id := range order {
		f := links[id]
		for _, epID := range f.EpisodeIDs {
			if i, ok := idx[epID]; ok {
				out[i].File, out[i].FileID = f, id
			}
		}
	}
	history, err := loadGrabHistory(ctx, db, versionID, policy.KindSeries)
	if err != nil {
		return nil, err
	}
	var held []*ExistingFile
	numbers := make(map[int64][2]int, len(out))
	for i := range out {
		numbers[out[i].ID] = [2]int{out[i].Season, out[i].Number}
	}
	seen := map[*ExistingFile]bool{}
	for i := range out {
		out[i].LastGrab = history[out[i].ID]
		f := out[i].File
		if f == nil || seen[f] {
			continue
		}
		seen[f] = true
		held = append(held, f)
		// The file's own episodes (season, number) scope its curator alternatives.
		f.seaDexEpisodes = nil
		for _, id := range f.EpisodeIDs {
			if n, ok := numbers[id]; ok {
				f.seaDexEpisodes = append(f.seaDexEpisodes, n)
			}
		}
		if len(f.seaDexEpisodes) == 0 {
			f.seaDexEpisodes = [][2]int{numbers[out[i].ID]}
		}
	}
	if err := ResolveSeaDexFiles(ctx, db, held); err != nil {
		return nil, err
	}
	return out, nil
}

func nullIntPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

// FindTitle returns the id of the title with a TMDb id (movies) or an IMDb id.
func FindTitle(ctx context.Context, db *sql.DB, tmdb int, imdb string) (int64, error) {
	var id int64
	var err error
	switch {
	case tmdb > 0:
		err = db.QueryRowContext(ctx, `SELECT id FROM title WHERE kind = 'movie' AND tmdb_id = ?`, tmdb).Scan(&id)
	case imdb != "":
		err = db.QueryRowContext(ctx, `SELECT id FROM title WHERE imdb_id = ? ORDER BY id LIMIT 1`, imdb).Scan(&id)
	default:
		return 0, errors.New("no id to look the title up by")
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("no title with tmdb %d / imdb %q", tmdb, imdb)
	}
	return id, err
}

func scanTitle(rows *sql.Rows) (Title, error) {
	var t Title
	var kind string
	var anime int
	var body []byte
	var runtime int
	var lang, metaLang, original string
	var metaRuntime int
	if err := rows.Scan(&t.ID, &kind, &t.Name, &t.Year, &t.SecondaryYear, &t.IMDb, &t.TMDb, &anime, &t.TVDB,
		&t.SeriesType, &t.UseSceneNumbering, &runtime, &lang, &t.Monitored, &metaRuntime, &metaLang, &original, &body); err != nil {
		return t, err
	}
	t.Kind, t.Anime = policy.Kind(kind), anime == 1
	if t.Kind == policy.KindSeries {
		t.Runtime, t.OriginalLanguage = runtime, lang
		if lang == "" {
			t.OriginalLanguage, t.OriginalLanguageResolved = seriesOriginalLanguage(metaLang), true
		}
		return t, nil
	}
	t.Runtime, t.OriginalLanguage = runtime, lang
	if d := gunzipDetails(body); d != nil {
		t.OriginalTitle, t.OriginalLanguage = d.OriginalTitle, tmdbOriginalLanguage(d.OriginalLanguage)
		if d.Runtime > 0 {
			t.Runtime = d.Runtime
		}
	}
	if metaRuntime > 0 {
		t.Runtime = metaRuntime
	}
	if metaLang != "" {
		t.OriginalLanguage = tmdbOriginalLanguage(metaLang)
	}
	if original != "" {
		t.OriginalTitle = original
	}
	return t, nil
}

// seriesOriginalLanguage is the series original language for a title the arr import did not set (a native, Seerr
// or Sonarr API add), from the metadata's code (TMDb's original_language): the code through Sonarr's
// IsoLanguages.Find, else its base language before a hyphen ("zh-tw", "fr-ca"), with metadataLanguageAliases for
// codes Sonarr's table lacks; else English. The ported table itself stays as Sonarr has it (release parsing uses it).
func seriesOriginalLanguage(code string) string {
	find := func(code string) string {
		if l, ok := metadataLanguageAliases[code]; ok {
			return l.Name
		}
		if iso := sonarr.FindIsoLanguage(code); iso != nil {
			return iso.Language.Name
		}
		return ""
	}
	code = strings.ToLower(strings.TrimSpace(code))
	name := find(code)
	if base, _, ok := strings.Cut(code, "-"); name == "" && ok {
		name = find(base)
	}
	if name == "" {
		return sonarr.LanguageEnglish.Name
	}
	return name
}

// metadataLanguageAliases are codes the metadata sources use that Sonarr's IsoLanguages lacks or reads otherwise:
// TMDb's cn (Cantonese) and yue, Norwegian Nynorsk, the old ISO codes for Hebrew and Indonesian, and Latin American
// Spanish (es-419), which Sonarr's table only knows as es-mx.
var metadataLanguageAliases = map[string]sonarr.Language{
	"cn": sonarr.LanguageChinese, "yue": sonarr.LanguageChinese, "nn": sonarr.LanguageNorwegian,
	"nno": sonarr.LanguageNorwegian, "iw": sonarr.LanguageHebrew, "in": sonarr.LanguageIndonesian,
	"es-419": sonarr.LanguageSpanishLatino,
}

// tmdbOriginalLanguage is SkyHookProxy.MapMovie's original language: IsoLanguages.Find(original_language.ToLower()),
// else English. Port of Radarr src/NzbDrone.Core/MetadataSource/SkyHook/SkyHookProxy.cs @ a96bf7c (line 244).
func tmdbOriginalLanguage(code string) string {
	if iso := radarr.FindIsoLanguage(strings.ToLower(code)); iso != nil {
		return iso.Language.Name
	}
	return radarr.LanguageEnglish.Name
}

func gunzipDetails(body []byte) *tmdbDetails {
	if len(body) == 0 {
		return nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil
	}
	var d tmdbDetails
	if json.Unmarshal(raw, &d) != nil {
		return nil
	}
	return &d
}

// LoadVersion reads a title's version by policy key; the id is 0 when the title has no such version yet.
func LoadVersion(ctx context.Context, db *sql.DB, pol *policy.Policy, titleID int64, key string) (Version, error) {
	v := Version{Key: key, Policy: pol.Versions[key]}
	if v.Policy == nil {
		return v, fmt.Errorf("the policy has no version %q", key)
	}
	err := db.QueryRowContext(ctx, `SELECT id FROM version WHERE title_id = ? AND policy_key = ?`, titleID, key).Scan(&v.ID)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err == nil && v.Policy.Kind == policy.KindSeries {
		n := v.Policy.Naming
		if n.FromArr != "" {
			var body string
			err = db.QueryRowContext(ctx, `SELECT naming FROM library_naming WHERE instance = ?`, n.FromArr).Scan(&body)
			if errors.Is(err, sql.ErrNoRows) {
				return v, nil
			}
			if err == nil {
				cfg, parseErr := naming.ParseArrConfig([]byte(body))
				err = parseErr
				v.Naming = &cfg
			}
		}
	}
	return v, err
}

// LoadExisting reads a version's current movie file: the newest active link, else the owning instance's
// version_file row (version_source), else the most recently added one. Nil when the version has no file.
func LoadExisting(ctx context.Context, db *sql.DB, versionID int64) (*ExistingFile, error) {
	f, _, err := LoadExistingFile(ctx, db, versionID)
	return f, err
}

// LoadExistingFile is LoadExisting with the file's id: minus the link id for a link (as Episode.FileID), else the
// version_file id.
func LoadExistingFile(ctx context.Context, db *sql.DB, versionID int64) (*ExistingFile, int64, error) {
	if versionID == 0 {
		return nil, 0, nil
	}
	links, order, err := loadLinks(ctx, db, versionID)
	if err != nil {
		return nil, 0, err
	}
	if len(order) > 0 {
		id := order[len(order)-1]
		f := links[id]
		history, err := loadGrabHistory(ctx, db, versionID, policy.KindMovie)
		if err != nil {
			return nil, 0, err
		}
		f.LastGrab = history[0]
		if err := ResolveSeaDexFiles(ctx, db, []*ExistingFile{f}); err != nil {
			return nil, 0, err
		}
		return f, id, nil
	}
	var f ExistingFile
	var id int64
	var langs string
	var added int64
	err = db.QueryRowContext(ctx, `SELECT vf.id, vf.quality, vf.quality_revision, vf.scene_name, vf.original_file_path,
		vf.relative_path, vf.release_group, vf.edition, vf.languages, vf.size_bytes, vf.indexer_flags,
		COALESCE(c.info_hash, ''), COALESCE(vf.added_at, 0)
		FROM version_file vf LEFT JOIN version_source vs ON vs.version_id = vf.version_id AND vs.instance = vf.instance
		LEFT JOIN content c ON c.id = vf.content_id
		WHERE vf.version_id = ?
		ORDER BY vs.version_id IS NULL, COALESCE(vf.added_at, 0) DESC, vf.id DESC LIMIT 1`, versionID).Scan(
		&id, &f.Quality, &f.Revision, &f.SceneName, &f.OriginalFilePath, &f.RelativePath, &f.ReleaseGroup, &f.Edition,
		&langs, &f.Size, &f.IndexerFlags, &f.InfoHash, &added)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	_ = json.Unmarshal([]byte(langs), &f.Languages)
	if added > 0 {
		f.Added = time.UnixMilli(added).UTC()
	}
	history, err := loadGrabHistory(ctx, db, versionID, policy.KindMovie)
	if err != nil {
		return nil, 0, err
	}
	f.LastGrab = history[0]
	if err := ResolveSeaDexFiles(ctx, db, []*ExistingFile{&f}); err != nil {
		return nil, 0, err
	}
	return &f, id, nil
}

// titleLibrary is a Library over a set of titles.
type titleLibrary struct {
	byIMDb map[string]*Title
	byTMDb map[int]*Title
}

// NewLibrary makes a Library of the given titles (LoadMovies).
func NewLibrary(titles map[int64]Title) Library {
	l := &titleLibrary{byIMDb: map[string]*Title{}, byTMDb: map[int]*Title{}}
	for _, t := range titles {
		t := t
		if t.IMDb != "" {
			l.byIMDb[t.IMDb] = &t
		}
		if t.TMDb > 0 {
			l.byTMDb[t.TMDb] = &t
		}
	}
	return l
}

func (l *titleLibrary) ByIMDb(id string) *Title { return l.byIMDb[id] }
func (l *titleLibrary) ByTMDb(id int) *Title    { return l.byTMDb[id] }

// tvdbSeriesTitles is what seriesOriginalTitle reads from a cached TVDB extended series record (tvdb.Series).
type tvdbSeriesTitles struct {
	Data struct {
		OriginalLanguage string `json:"originalLanguage"`
		Translations     *struct {
			NameTranslations []struct {
				Language string `json:"language"`
				Name     string `json:"name"`
				IsAlias  bool   `json:"isAlias"`
			} `json:"nameTranslations"`
		} `json:"translations"`
	} `json:"data"`
}

// seriesOriginalTitle is a series' original title as Sonarr v5's SkyHookProxy.MapSeries takes it (the translation
// in the series' original language), from the TVDB extended record our TVDB client cached (tvdb.Client.Series: GET
// <base>/series/<id>/extended?meta=translations&short=true; stale entries too). "" when nothing is cached or no
// name translation is in the original language. Only a series version with sonarr: v5 matches it.
func seriesOriginalTitle(ctx context.Context, db *sql.DB, tvdbID int) (string, error) {
	suffix := fmt.Sprintf("/series/%d/extended?meta=translations&short=true", tvdbID)
	var body []byte
	err := db.QueryRowContext(ctx, `SELECT body FROM metadata_cache WHERE source = 'tvdb' AND substr(key, 1, 4) = 'GET '
		AND substr(key, -length(?)) = ? ORDER BY fetched_at DESC LIMIT 1`, suffix, suffix).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return "", nil // a broken entry is a miss
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return "", nil
	}
	var s tvdbSeriesTitles
	if json.Unmarshal(raw, &s) != nil || s.Data.Translations == nil || s.Data.OriginalLanguage == "" {
		return "", nil
	}
	for _, tr := range s.Data.Translations.NameTranslations {
		if tr.Language == s.Data.OriginalLanguage && !tr.IsAlias && strings.TrimSpace(tr.Name) != "" {
			return tr.Name, nil
		}
	}
	return "", nil
}
