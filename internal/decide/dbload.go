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
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Loading decision inputs from our database (internal/store schema): titles with their cached TMDb details,
// versions, and current files (active links). Reads only.

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
	ts, err := LoadTitles(ctx, db, []int64{id})
	if err != nil {
		return Title{}, err
	}
	t, ok := ts[id]
	if !ok {
		return Title{}, fmt.Errorf("no title %d", id)
	}
	return t, nil
}

// LoadTitles is LoadTitle for many titles at once, by id; an id without a title is left out.
func LoadTitles(ctx context.Context, db *sql.DB, ids []int64) (map[int64]Title, error) {
	list, _ := json.Marshal(ids)
	rows, err := db.QueryContext(ctx, titleSelect+` WHERE t.id IN (SELECT value FROM json_each(?))`, string(list))
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
	aliases, err := loadAliases(ctx, db, `SELECT title_id, name FROM title_alias
		WHERE title_id IN (SELECT value FROM json_each(?)) ORDER BY title_id, kind, source, name`, string(list))
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
	return loadSeriesVersions(ctx, db, []*Title{t}, []int64{versionID}, true)
}

// loadSeriesVersions is LoadSeriesVersion for titles[i] and versionIDs[i] at once; without history the episodes'
// LastGrab is not read.
func loadSeriesVersions(ctx context.Context, db *sql.DB, titles []*Title, versionIDs []int64, history bool) error {
	var series []titleVersion
	var ts []*Title
	var ids []int64
	var tvdbs []int
	for i, t := range titles {
		t.cutoff = &cutoffCache{}
		if t.Kind != policy.KindSeries {
			continue
		}
		t.SceneMappings = nil
		series = append(series, titleVersion{t.ID, versionIDs[i]})
		ts = append(ts, t)
		ids = append(ids, t.ID)
		tvdbs = append(tvdbs, t.TVDB)
	}
	if len(ts) == 0 {
		return nil
	}
	list, _ := json.Marshal(ids)
	rows, err := db.QueryContext(ctx, `SELECT title_id, name, season_number, scene_season_number, scene_origin, comment
		FROM title_scene_mapping WHERE title_id IN (SELECT value FROM json_each(?)) ORDER BY title_id, source, rowid`,
		string(list))
	if err != nil {
		return err
	}
	mappings := map[int64][]SceneMapping{}
	seen := map[int64]map[string]bool{}
	for rows.Next() {
		var id int64
		var m SceneMapping
		var season, scene sql.NullInt64
		if err := rows.Scan(&id, &m.Title, &season, &scene, &m.SceneOrigin, &m.Comment); err != nil {
			rows.Close()
			return err
		}
		m.SeasonNumber, m.SceneSeasonNumber = nullIntPtr(season), nullIntPtr(scene)
		k := fmt.Sprintf("%s|%d|%d", m.Title, orMinus1(m.SeasonNumber), orMinus1(m.SceneSeasonNumber))
		if seen[id] == nil {
			seen[id] = map[string]bool{}
		}
		if !seen[id][k] {
			seen[id][k] = true
			mappings[id] = append(mappings[id], m)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	eps, err := loadEpisodes(ctx, db, series, history)
	if err != nil {
		return err
	}
	// franchise mappings into the series, if any (anime.go)
	maps, err := animemap.LoadMany(ctx, db, tvdbs)
	if err != nil {
		return err
	}
	for i, t := range ts {
		t.SceneMappings = append(t.SceneMappings, mappings[t.ID]...)
		t.Episodes = eps[i]
		t.AnimeMap = nil
		if t.TVDB > 0 {
			t.AnimeMap = maps[t.TVDB]
		}
	}
	return nil
}

// LoadEpisodes reads a series' episodes in season and number order, with their numbers (episode_number, any
// source) and, for versionID, the version's file of each (its newest active link). Removed rows are excluded.
func LoadEpisodes(ctx context.Context, db *sql.DB, titleID, versionID int64) ([]Episode, error) {
	eps, err := loadEpisodes(ctx, db, []titleVersion{{titleID, versionID}}, true)
	if err != nil {
		return nil, err
	}
	return eps[0], nil
}

// titleVersion is a title and one of its versions (0: none) for a batched load.
type titleVersion struct{ title, version int64 }

// loadEpisodes is LoadEpisodes for each of tvs, in their order; without history the episodes' LastGrab is not read.
func loadEpisodes(ctx context.Context, db *sql.DB, tvs []titleVersion, history bool) ([][]Episode, error) {
	pairs := make([][2]int64, len(tvs))
	byTitle := map[int64][]int{} // the indexes of tvs with the title
	var titles, versions []int64
	for i, tv := range tvs {
		pairs[i] = [2]int64{tv.title, tv.version}
		if byTitle[tv.title] == nil {
			titles = append(titles, tv.title)
		}
		byTitle[tv.title] = append(byTitle[tv.title], i)
		if tv.version != 0 {
			versions = append(versions, tv.version)
		}
	}
	list, _ := json.Marshal(pairs)
	rows, err := db.QueryContext(ctx, `SELECT p.key, e.id, e.season, e.number, e.name, COALESCE(e.air_date, ''),
		e.air_date_utc_at, COALESCE(e.runtime_minutes, 0), COALESCE(s.monitored, e.monitored)
		FROM json_each(?) p JOIN episode e ON e.title_id = json_extract(p.value, '$[0]')
		LEFT JOIN episode_version_state s ON s.episode_id = e.id AND s.version_id = json_extract(p.value, '$[1]')
		WHERE e.removed_at IS NULL ORDER BY p.key, e.season, e.number`, string(list))
	if err != nil {
		return nil, err
	}
	out := make([][]Episode, len(tvs))
	idx := make([]map[int64]int, len(tvs))
	for i := range idx {
		idx[i] = map[int64]int{}
	}
	for rows.Next() {
		var i int
		var e Episode
		var at sql.NullInt64
		if err := rows.Scan(&i, &e.ID, &e.Season, &e.Number, &e.Title, &e.AirDate, &at, &e.Runtime, &e.Monitored); err != nil {
			rows.Close()
			return nil, err
		}
		if at.Valid {
			u := time.UnixMilli(at.Int64).UTC()
			e.AirDateUTC = &u
		}
		idx[i][e.ID] = len(out[i])
		out[i] = append(out[i], e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	tl, _ := json.Marshal(titles)
	nrows, err := db.QueryContext(ctx, `SELECT n.title_id, n.episode_id, n.scheme, n.season, n.number FROM episode_number n
		WHERE n.title_id IN (SELECT value FROM json_each(?)) AND n.scheme IN ('absolute_tvdb', 'scene', 'scene_absolute')
		ORDER BY n.title_id, n.source, n.id`, string(tl))
	if err != nil {
		return nil, err
	}
	for nrows.Next() {
		var title, id int64
		var scheme string
		var season sql.NullInt64
		var number int
		if err := nrows.Scan(&title, &id, &scheme, &season, &number); err != nil {
			nrows.Close()
			return nil, err
		}
		for _, ti := range byTitle[title] {
			i, ok := idx[ti][id]
			if !ok {
				continue
			}
			e := &out[ti][i]
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
	}
	nrows.Close()
	if err := nrows.Err(); err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return out, nil
	}
	links, err := loadLinksOf(ctx, db, versions)
	if err != nil {
		return nil, err
	}
	for ti, tv := range tvs {
		if tv.version == 0 {
			continue
		}
		eps := out[ti]
		l := links[tv.version]
		if l != nil {
			for _, id := range l.order {
				f := l.files[id]
				for _, epID := range f.EpisodeIDs {
					if i, ok := idx[ti][epID]; ok {
						eps[i].File, eps[i].FileID = f, id
					}
				}
			}
		}
		var grabs map[int64]*profile.ImportedGrab
		if history {
			if grabs, err = loadGrabHistory(ctx, db, tv.version, policy.KindSeries); err != nil {
				return nil, err
			}
		}
		var held []*ExistingFile
		numbers := make(map[int64][2]int, len(eps))
		for i := range eps {
			numbers[eps[i].ID] = [2]int{eps[i].Season, eps[i].Number}
		}
		seen := map[*ExistingFile]bool{}
		for i := range eps {
			eps[i].LastGrab = grabs[eps[i].ID]
			f := eps[i].File
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
				f.seaDexEpisodes = [][2]int{numbers[eps[i].ID]}
			}
		}
		if err := ResolveSeaDexFiles(WithSeaDexTitle(ctx, tv.title), db, held); err != nil {
			return nil, err
		}
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
// else English. Port of Radarr src/NzbDrone.Core/MetadataSource/SkyHook/SkyHookProxy.cs @ a96bf7c (line 244). Ours:
// movieLanguageAliases first, the codes TMDb sends that Radarr's table knows under another code.
func tmdbOriginalLanguage(code string) string {
	code = strings.ToLower(code)
	if l, ok := movieLanguageAliases[code]; ok {
		return l.Name
	}
	if iso := radarr.FindIsoLanguage(code); iso != nil {
		return iso.Language.Name
	}
	return radarr.LanguageEnglish.Name
}

// movieLanguageAliases are metadataLanguageAliases for Radarr's table, which already reads cn as Chinese: yue
// (Cantonese), Norwegian Nynorsk, and the old ISO codes for Hebrew and Indonesian.
var movieLanguageAliases = map[string]radarr.Language{
	"yue": radarr.LanguageChinese, "nn": radarr.LanguageNorwegian, "iw": radarr.LanguageHebrew,
	"in": radarr.LanguageIndonesian,
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
	return v, err
}

// LoadExisting reads a version's current movie file: the newest active link. Nil when the version has no file.
func LoadExisting(ctx context.Context, db *sql.DB, versionID int64) (*ExistingFile, error) {
	f, _, err := LoadExistingFile(ctx, db, versionID)
	return f, err
}

// LoadExistingFile is LoadExisting with the file's id: minus the link id (as Episode.FileID).
func LoadExistingFile(ctx context.Context, db *sql.DB, versionID int64) (*ExistingFile, int64, error) {
	if versionID == 0 {
		return nil, 0, nil
	}
	links, order, err := loadLinks(ctx, db, versionID)
	if err != nil {
		return nil, 0, err
	}
	if len(order) == 0 {
		return nil, 0, nil
	}
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

// CutoffInput is what judging a version's held files against the cutoff reads (LoadCutoffInputs).
type CutoffInput struct {
	Title *Title        // nil when the title is gone
	File  *ExistingFile // a movie's current file (LoadExisting); nil without one
}

// LoadCutoffInputs reads, for many versions of one library at once (version id -> title id), what judging their held
// files against the cutoff reads, by version id: the title (LoadTitle), for a series filled for the version
// (LoadSeriesVersion), for a movie with the version's current file (LoadExisting). One batch of reads instead of a
// set per title. Grab history (LastGrab) is not read: no cutoff judgement looks at it. The files take standing as
// LoadExisting and LoadEpisodes give it (ctx as configured for the version, WithSeaDexPolicy).
func LoadCutoffInputs(ctx context.Context, db *sql.DB, kind policy.Kind,
	versions map[int64]int64) (map[int64]*CutoffInput, error) {
	ids := slices.Sorted(maps.Keys(versions))
	titleIDs := make([]int64, len(ids))
	for i, v := range ids {
		titleIDs[i] = versions[v]
	}
	titles, err := LoadTitles(ctx, db, titleIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*CutoffInput, len(ids))
	for _, v := range ids {
		in := &CutoffInput{}
		if t, ok := titles[versions[v]]; ok {
			in.Title = &t
		}
		out[v] = in
	}
	if kind == policy.KindSeries {
		var ts []*Title
		var vs []int64
		for _, v := range ids {
			if t := out[v].Title; t != nil {
				ts, vs = append(ts, t), append(vs, v)
			}
		}
		if err := loadSeriesVersions(ctx, db, ts, vs, false); err != nil {
			return nil, err
		}
		return out, nil
	}
	links, err := loadLinksOf(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	for _, v := range ids {
		if l := links[v]; l != nil && len(l.order) > 0 {
			f := l.files[l.order[len(l.order)-1]]
			if err := ResolveSeaDexFiles(WithSeaDexTitle(ctx, versions[v]), db, []*ExistingFile{f}); err != nil {
				return nil, err
			}
			out[v].File = f
		}
	}
	return out, nil
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
