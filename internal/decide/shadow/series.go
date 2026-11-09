package shadow

// Series grabs. Sonarr writes one grabbed event per episode of a release, so one grab here is the group of grabbed
// events with the same series, download id, release title and date; its episodes are theirs. The decision runs with
// search criteria for those episodes (the arr searched for them, or found them in RSS when history says so), and a
// season pack is judged against every episode's file as the arr's was.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
)

// SearchFor is the search for these episodes in TVDB numbering, the target's own identity: none for an RSS grab;
// otherwise a season search when it took several episodes or a pack, else a single-episode search (anime and daily
// kinds for those series). A search asks each source its own numbering and judges each answer with that query's
// criteria (router.SearchRequests); ArrSearchFor is the arr's criteria for a replay.
func SearchFor(t *decide.Title, episodes []int64, releaseSource, releaseType string) *decide.SeriesSearch {
	if releaseSource == "Rss" || releaseSource == "ReleasePush" {
		return nil
	}
	sc := &decide.SeriesSearch{Episodes: episodes, Interactive: releaseSource == "InteractiveSearch"}
	if e := firstEpisode(t, episodes); e != nil {
		sc.Season, sc.Episode = e.Season, e.Number
	}
	multi := len(episodes) > 1 || releaseType == "SeasonPack" || releaseType == "seasonPack"
	switch {
	case t.SeriesType == "anime" && multi:
		sc.Kind = profile.SearchAnimeSeason
	case t.SeriesType == "anime":
		sc.Kind = profile.SearchAnimeEpisode
	case t.SeriesType == "daily" && !multi:
		sc.Kind = profile.SearchDaily
	case t.SeriesType == "daily":
		sc.Kind = profile.SearchDailySeason
	case multi:
		sc.Kind = profile.SearchSeason
	default:
		sc.Kind = profile.SearchSingleEpisode
	}
	return sc
}

// ArrSearchFor is the search criteria the arr had for a grab of these episodes: SearchFor in the arr's numbering, the
// first episode's scene season and episode for a scene-numbered series (what SearchSingle, SeasonSearch and
// SearchAnime ask under the series' own title). The arr's own criteria are not in history; these accept what the
// arr's would.
func ArrSearchFor(t *decide.Title, episodes []int64, releaseSource, releaseType string) *decide.SeriesSearch {
	sc := SearchFor(t, episodes, releaseSource, releaseType)
	if sc == nil || !t.UseSceneNumbering {
		return sc
	}
	if e := firstEpisode(t, episodes); e != nil && e.SceneSeason != nil {
		sc.Season = *e.SceneSeason
		if e.SceneNumber != nil {
			sc.Episode = *e.SceneNumber
		}
	}
	return sc
}

// firstEpisode is the title's episode of the first of these ids that it has, in the title's episode order.
func firstEpisode(t *decide.Title, episodes []int64) *decide.Episode {
	for i := range t.Episodes {
		if slices.Contains(episodes, t.Episodes[i].ID) {
			return &t.Episodes[i]
		}
	}
	return nil
}

// groupKey identifies one series grab across its per-episode history rows: the same series, download id and release
// title, written within groupWindow of the group's first row (the arr writes them one by one, so a large pack spans
// a second or two).
type groupKey struct {
	series      int
	download    string
	sourceTitle string
	date        int64
}

const groupWindow = 2 * time.Minute

// grouper finds the group of a grabbed row.
type grouper struct {
	last map[groupKey]groupKey // key without date -> the open group's key
}

func (gr *grouper) key(series int, download, title string, date int64) groupKey {
	if gr.last == nil {
		gr.last = map[groupKey]groupKey{}
	}
	base := groupKey{series, download, title, 0}
	if k, ok := gr.last[base]; ok && date-k.date <= groupWindow.Milliseconds() && date >= k.date {
		return k
	}
	k := groupKey{series, download, title, date}
	gr.last[base] = k
	return k
}

// ---- Corpus ----

type corpusSeries struct {
	ID                int    `json:"id"`
	Title             string `json:"title"`
	Year              int    `json:"year"`
	TvdbID            int    `json:"tvdbId"`
	ImdbID            string `json:"imdbId"`
	SeriesType        string `json:"seriesType"`
	UseSceneNumbering bool   `json:"useSceneNumbering"`
	Runtime           int    `json:"runtime"`
	Monitored         bool   `json:"monitored"`
	QualityProfileID  int    `json:"qualityProfileId"`
	OriginalLanguage  struct {
		Name string `json:"name"`
	} `json:"originalLanguage"`
	AlternateTitles []struct {
		Title             string `json:"title"`
		SeasonNumber      *int   `json:"seasonNumber"`
		SceneSeasonNumber *int   `json:"sceneSeasonNumber"`
		SceneOrigin       string `json:"sceneOrigin"`
		Comment           string `json:"comment"`
	} `json:"alternateTitles"`
}

type corpusEpisode struct {
	ID                         int        `json:"id"`
	SeriesID                   int        `json:"seriesId"`
	SeasonNumber               int        `json:"seasonNumber"`
	EpisodeNumber              int        `json:"episodeNumber"`
	Title                      string     `json:"title"`
	AirDate                    string     `json:"airDate"`
	AirDateUtc                 *time.Time `json:"airDateUtc"`
	Runtime                    int        `json:"runtime"`
	Monitored                  bool       `json:"monitored"`
	AbsoluteEpisodeNumber      *int       `json:"absoluteEpisodeNumber"`
	SceneSeasonNumber          *int       `json:"sceneSeasonNumber"`
	SceneEpisodeNumber         *int       `json:"sceneEpisodeNumber"`
	SceneAbsoluteEpisodeNumber *int       `json:"sceneAbsoluteEpisodeNumber"`
}

func (e *corpusEpisode) episode() decide.Episode {
	x := decide.Episode{ID: int64(e.ID), Season: e.SeasonNumber, Number: e.EpisodeNumber, Title: e.Title,
		AirDate: e.AirDate, Runtime: e.Runtime, Monitored: e.Monitored, SceneSeason: e.SceneSeasonNumber,
		SceneNumber: e.SceneEpisodeNumber, SceneAbsolute: e.SceneAbsoluteEpisodeNumber}
	if e.AbsoluteEpisodeNumber != nil {
		x.Absolute = *e.AbsoluteEpisodeNumber
	}
	if e.AirDateUtc != nil {
		u := e.AirDateUtc.UTC()
		x.AirDateUTC = &u
	}
	return x
}

func (s *corpusSeries) title() decide.Title {
	t := decide.Title{ID: int64(s.ID), Kind: policy.KindSeries, Name: s.Title, Year: s.Year, TVDB: s.TvdbID,
		IMDb: s.ImdbID, SeriesType: s.SeriesType, UseSceneNumbering: s.UseSceneNumbering, Runtime: s.Runtime,
		Monitored: s.Monitored, OriginalLanguage: s.OriginalLanguage.Name, Anime: s.SeriesType == "anime"}
	for _, a := range s.AlternateTitles {
		t.SceneMappings = append(t.SceneMappings, decide.SceneMapping{Title: a.Title, SeasonNumber: a.SeasonNumber,
			SceneSeasonNumber: a.SceneSeasonNumber, SceneOrigin: a.SceneOrigin, Comment: a.Comment})
	}
	return t
}

// corpusSeriesData reads the series and episodes the arr's parse answers carry (parse.jsonl): each series once,
// and per series the union of the episodes any answer mapped.
func corpusSeriesData(dir, instance string) (map[int]*corpusSeries, map[int]map[int]*corpusEpisode, error) {
	series := map[int]*corpusSeries{}
	episodes := map[int]map[int]*corpusEpisode{}
	err := eachLine(filepath.Join(dir, instance, "parse.jsonl"), func(b []byte) error {
		var l struct {
			Body struct {
				Series   *corpusSeries   `json:"series"`
				Episodes []corpusEpisode `json:"episodes"`
			} `json:"body"`
		}
		if err := json.Unmarshal(b, &l); err != nil {
			return err
		}
		if s := l.Body.Series; s != nil && series[s.ID] == nil {
			series[s.ID] = s
		}
		for i := range l.Body.Episodes {
			e := &l.Body.Episodes[i]
			if episodes[e.SeriesID] == nil {
				episodes[e.SeriesID] = map[int]*corpusEpisode{}
			}
			if episodes[e.SeriesID][e.ID] == nil {
				episodes[e.SeriesID][e.ID] = e
			}
		}
		return nil
	})
	return series, episodes, err
}

// SeriesAliases reads alternate titles (scene mappings) from a saved GET series answer (a JSON array, e.g. from
// theatarr import-arr's scratch or the arr's API), by TVDB id. The corpus parse answers carry none.
func SeriesAliases(file string) (map[int][]decide.SceneMapping, error) {
	var list []corpusSeries
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	out := map[int][]decide.SceneMapping{}
	for i := range list {
		out[list[i].TvdbID] = list[i].title().SceneMappings
	}
	return out, nil
}

// LoadCorpusSeries reads one Sonarr instance's grabs from the corpus: grabbed events grouped per release, with the
// series and episodes from the parse answers. aliases (by TVDB id, may be nil) adds scene mappings the corpus lacks.
// The corpus has grabs only, so no episode has a file.
func LoadCorpusSeries(dir, instance string, aliases map[int][]decide.SceneMapping) ([]Grab, error) {
	series, episodes, err := corpusSeriesData(dir, instance)
	if err != nil {
		return nil, err
	}
	// Release titles the arr's parse maps to no series today.
	unmapped := map[string]bool{}
	if err := eachLine(filepath.Join(dir, instance, "parse.jsonl"), func(b []byte) error {
		var l struct {
			Title string `json:"title"`
			Body  struct {
				Series   *struct{}  `json:"series"`
				Episodes []struct{} `json:"episodes"`
			} `json:"body"`
		}
		if err := json.Unmarshal(b, &l); err != nil {
			return err
		}
		if l.Body.Series == nil || len(l.Body.Episodes) == 0 {
			unmapped[l.Title] = true
		}
		return nil
	}); err != nil {
		return nil, err
	}
	titles := map[int]decide.Title{}
	for id, s := range series {
		t := s.title()
		if len(t.SceneMappings) == 0 {
			t.SceneMappings = aliases[s.TvdbID]
		}
		var ids []int
		for eid := range episodes[id] {
			ids = append(ids, eid)
		}
		sort.Ints(ids)
		for _, eid := range ids {
			t.Episodes = append(t.Episodes, episodes[id][eid].episode())
		}
		sort.SliceStable(t.Episodes, func(i, j int) bool {
			a, b := t.Episodes[i], t.Episodes[j]
			if a.Season != b.Season {
				return a.Season < b.Season
			}
			return a.Number < b.Number
		})
		titles[id] = t
	}
	type rec struct {
		corpusHistory
		SeriesID  int    `json:"seriesId"`
		EpisodeID int    `json:"episodeId"`
		Download  string `json:"downloadId"`
	}
	groups := map[groupKey]*Grab{}
	var order []groupKey
	var gr grouper
	type pending struct {
		rec
		at int64
	}
	var recs []pending
	err = eachLine(filepath.Join(dir, instance, "history.jsonl"), func(b []byte) error {
		var h rec
		if err := json.Unmarshal(b, &h); err != nil {
			return err
		}
		if _, ok := titles[h.SeriesID]; !ok || h.EventType != "grabbed" {
			return nil
		}
		recs = append(recs, pending{h, h.Date.UnixMilli()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(recs, func(i, j int) bool {
		return recs[i].at < recs[j].at || recs[i].at == recs[j].at && recs[i].ID < recs[j].ID
	})
	for _, p := range recs {
		h := p.rec
		t := titles[h.SeriesID]
		k := gr.key(h.SeriesID, h.Download, h.SourceTitle, p.at)
		g := groups[k]
		if g == nil {
			g = &Grab{Instance: instance, HistoryID: h.ID, Date: h.Date, Title: t, Quality: h.Quality.Quality.Name,
				NowScore: h.CustomFormatScore, Indexer: h.Data["indexer"], ArrProfileID: series[h.SeriesID].QualityProfileID,
				Release: seriesRelease(h.SourceTitle, h.Data, h.Data["protocol"] == "1"), Kind: policy.KindSeries,
				ReleaseType: h.Data["releaseType"]}
			if unmapped[h.SourceTitle] {
				g.MatchType = "Unknown"
			}
			for _, l := range h.Languages {
				g.Languages = append(g.Languages, l.Name)
			}
			for _, f := range h.CustomFormats {
				g.NowFormats = append(g.NowFormats, f.Name)
			}
			sort.Strings(g.NowFormats)
			groups[k] = g
			order = append(order, k)
		}
		g.HistoryIDs = append(g.HistoryIDs, h.ID)
		g.Episodes = append(g.Episodes, int64(h.EpisodeID))
		if h.ID < g.HistoryID {
			g.HistoryID = h.ID
		}
	}
	out := make([]Grab, 0, len(order))
	for _, k := range order {
		g := groups[k]
		g.Search = ArrSearchFor(&g.Title, g.Episodes, "", g.ReleaseType)
		out = append(out, *g)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}

// seriesRelease is release() for a Sonarr grab: the indexer's TVDB id instead of TMDb, flags in Sonarr's names.
func seriesRelease(title string, d map[string]string, usenet bool) decide.Release {
	r := release(title, d, usenet)
	r.TMDb = 0
	r.IMDb = d["imdbId"]
	r.TVDB, _ = strconv.Atoi(d["tvdbId"])
	fl, _ := format.Sonarr.IndexerFlagsValue(d["indexerFlags"])
	r.IndexerFlags = &fl
	return r
}

// ---- Database ----

// LoadSeriesDB reads one Sonarr instance's grabs from our database, oldest first, each with the files its episodes
// had just before it, rebuilt from the import and delete events. aliases (by TVDB id, may be nil) adds scene
// mappings to series that have none stored.
func LoadSeriesDB(ctx context.Context, db *sql.DB, instance string, aliases map[int][]decide.SceneMapping) ([]Grab, error) {
	return loadSeriesDB(ctx, db, instance, aliases, 0)
}

// LoadSeriesGrab reads the series grab that history row historyID belongs to, with the files its episodes had just
// before it: rebuilt from history, and for episodes history holds no import for, the version's file as the import
// stored it when the arr added it before the grab (Source says which: history, version_file, or none).
func LoadSeriesGrab(ctx context.Context, db *sql.DB, instance string, historyID int) (*Grab, string, error) {
	var arrTitle int
	err := db.QueryRowContext(ctx, `SELECT arr_title_id FROM arr_history WHERE instance = ? AND arr_id = ?
		AND event_type = 'grabbed'`, instance, historyID).Scan(&arrTitle)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", fmt.Errorf("%s: no grab with history id %d", instance, historyID)
	}
	if err != nil {
		return nil, "", err
	}
	grabs, err := loadSeriesDB(ctx, db, instance, nil, arrTitle)
	if err != nil {
		return nil, "", err
	}
	for i := range grabs {
		if slices.Contains(grabs[i].HistoryIDs, historyID) {
			g := &grabs[i]
			source := "none"
			for _, e := range g.Title.Episodes {
				if e.File != nil {
					source = "history"
				}
			}
			// Episodes without a rebuilt file: the version's current file, when it predates the grab.
			var vid int64
			if err := db.QueryRowContext(ctx, `SELECT version_id FROM arr_title WHERE instance = ? AND arr_id = ?`,
				instance, arrTitle).Scan(&vid); err != nil {
				return nil, "", err
			}
			cur, err := decide.LoadEpisodes(ctx, db, g.Title.ID, vid)
			if err != nil {
				return nil, "", err
			}
			byID := map[int64]decide.Episode{}
			for _, e := range cur {
				byID[e.ID] = e
			}
			for j := range g.Title.Episodes {
				e := &g.Title.Episodes[j]
				c, ok := byID[e.ID]
				if e.File != nil || !ok || c.File == nil || c.File.Added.IsZero() || c.File.Added.After(g.Date) {
					continue
				}
				f := *c.File
				e.File, e.FileID = &f, -c.FileID // negative: version_file ids, apart from arr file ids
				if source == "none" {
					source = "version_file"
				}
			}
			return g, source, nil
		}
	}
	return nil, "", fmt.Errorf("%s: grab %d is of a series we do not hold", instance, historyID)
}

func loadSeriesDB(ctx context.Context, db *sql.DB, instance string, aliases map[int][]decide.SceneMapping,
	onlyArr int) ([]Grab, error) {
	series, err := decide.LoadSeries(ctx, db)
	if err != nil {
		return nil, err
	}
	// Each of the instance's series, loaded for its version.
	type arrSeries struct {
		title   decide.Title
		profile int
	}
	byArr := map[int]*arrSeries{}
	rows, err := db.QueryContext(ctx, `SELECT arr_id, title_id, version_id, quality_profile_id FROM arr_title
		WHERE instance = ? AND kind = 'series' AND (? = 0 OR arr_id = ?)`, instance, onlyArr, onlyArr)
	if err != nil {
		return nil, err
	}
	type ref struct {
		arr, profile     int
		title, versionID int64
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.arr, &r.title, &r.versionID, &r.profile); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range refs {
		t, ok := series[r.title]
		if !ok {
			continue
		}
		if err := decide.LoadSeriesVersion(ctx, db, &t, r.versionID); err != nil {
			return nil, err
		}
		if len(t.SceneMappings) == 0 {
			t.SceneMappings = aliases[t.TVDB]
		}
		for i := range t.Episodes { // files come from history, as they were at each grab
			t.Episodes[i].File, t.Episodes[i].FileID = nil, 0
		}
		byArr[r.arr] = &arrSeries{title: t, profile: r.profile}
	}
	episodeIDs, err := arrEpisodes(ctx, db, instance)
	if err != nil {
		return nil, err
	}
	hrows, err := db.QueryContext(ctx, `SELECT arr_id, event_type, date_at, arr_title_id, COALESCE(arr_episode_id, 0),
		COALESCE(download_id, ''), protocol, source_title, quality, custom_format_score, languages, data
		FROM arr_history WHERE instance = ? AND (? = 0 OR arr_title_id = ?) ORDER BY date_at, arr_id`, instance,
		onlyArr, onlyArr)
	if err != nil {
		return nil, err
	}
	defer hrows.Close()
	files := map[int]map[int]*rebuilt{} // arr series id -> arr episode id -> file
	groups := map[groupKey]*Grab{}
	var order []groupKey
	var gr grouper
	for hrows.Next() {
		var (
			id, arrTitle, arrEp             int
			event, dl, proto, source, q, lj string
			data                            string
			date                            int64
			score                           sql.NullInt64
		)
		if err := hrows.Scan(&id, &event, &date, &arrTitle, &arrEp, &dl, &proto, &source, &q, &score, &lj, &data); err != nil {
			return nil, err
		}
		s := byArr[arrTitle]
		if s == nil {
			continue
		}
		d := map[string]string{}
		if err := json.Unmarshal([]byte(data), &d); err != nil {
			return nil, fmt.Errorf("arr_history %s/%d: data: %w", instance, id, err)
		}
		var languages []string
		_ = json.Unmarshal([]byte(lj), &languages)
		if files[arrTitle] == nil {
			files[arrTitle] = map[int]*rebuilt{}
		}
		fs := files[arrTitle]
		switch event {
		case "downloadFolderImported":
			fid, _ := strconv.Atoi(d["fileId"])
			f := fileByID(fs, fid)
			if f == nil {
				fl, _ := format.Sonarr.IndexerFlagsValue(d["indexerFlags"])
				f = &rebuilt{id: fid, f: decide.ExistingFile{Quality: q, Revision: 1, SceneName: source,
					ReleaseGroup: d["releaseGroup"], Languages: languages, Size: atoi64(d["size"]), IndexerFlags: fl,
					ReleaseType: releaseTypeName(d["releaseType"])}}
				if p := d["importedPath"]; p != "" {
					f.f.RelativePath = path.Base(p)
				}
			}
			if old := fs[arrEp]; old != nil && old != f {
				old.f.EpisodeIDs = without(old.f.EpisodeIDs, episodeIDs[arrEp])
			}
			fs[arrEp] = f
			if eid := episodeIDs[arrEp]; eid != 0 && !contains64(f.f.EpisodeIDs, eid) {
				f.f.EpisodeIDs = append(f.f.EpisodeIDs, eid)
			}
		case "episodeFileDeleted":
			if old := fs[arrEp]; old != nil {
				old.f.EpisodeIDs = without(old.f.EpisodeIDs, episodeIDs[arrEp])
			}
			delete(fs, arrEp)
		case "grabbed":
			k := gr.key(arrTitle, dl, source, date)
			g := groups[k]
			if g == nil {
				t := s.title
				t.Episodes = append([]decide.Episode(nil), t.Episodes...)
				for i := range t.Episodes {
					e := &t.Episodes[i]
					for arrID, f := range fs {
						if episodeIDs[arrID] == e.ID {
							c := f.f
							c.EpisodeIDs = append([]int64(nil), f.f.EpisodeIDs...)
							e.File, e.FileID = &c, int64(f.id)
						}
					}
				}
				g = &Grab{Instance: instance, HistoryID: id, Date: time.UnixMilli(date).UTC(), Title: t, Quality: q,
					MatchType: d["seriesMatchType"], ReleaseSource: d["releaseSource"], Indexer: d["indexer"], Known: true,
					Languages: languages, ArrProfileID: s.profile, InfoHash: d["torrentInfoHash"], Kind: policy.KindSeries,
					ReleaseType: d["releaseType"], Release: seriesRelease(source, d, proto == "usenet")}
				if score.Valid {
					v := int(score.Int64)
					g.NowScore = &v
				}
				if v, err := strconv.Atoi(d["customFormatScore"]); err == nil {
					g.GrabScore = &v
				}
				groups[k] = g
				order = append(order, k)
			}
			g.HistoryIDs = append(g.HistoryIDs, id)
			g.ArrEpisodes = append(g.ArrEpisodes, arrEp)
			if eid := episodeIDs[arrEp]; eid != 0 {
				g.Episodes = append(g.Episodes, eid)
			}
		}
	}
	if err := hrows.Err(); err != nil {
		return nil, err
	}
	out := make([]Grab, 0, len(order))
	for _, k := range order {
		g := groups[k]
		g.Search = ArrSearchFor(&g.Title, g.Episodes, g.ReleaseSource, g.ReleaseType)
		out = append(out, *g)
	}
	return out, nil
}

// rebuilt is an episode file rebuilt from history, with the arr's file id (several episodes share one).
type rebuilt struct {
	id int
	f  decide.ExistingFile
}

func fileByID(fs map[int]*rebuilt, fid int) *rebuilt {
	if fid == 0 {
		return nil
	}
	for _, f := range fs {
		if f.id == fid {
			return f
		}
	}
	return nil
}

// arrEpisodes maps the instance's arr episode ids to ours (arr_alias).
func arrEpisodes(ctx context.Context, db *sql.DB, instance string) (map[int]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT arr_id, target_id FROM arr_alias WHERE instance = ? AND kind = 'episode'`, instance)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]int64{}
	for rows.Next() {
		var a int
		var id int64
		if err := rows.Scan(&a, &id); err != nil {
			return nil, err
		}
		out[a] = id
	}
	return out, rows.Err()
}

// releaseTypeName is a history releaseType ("SeasonPack") in the API's spelling ("seasonPack").
func releaseTypeName(s string) string {
	if s == "" {
		return ""
	}
	return string(s[0]|0x20) + s[1:]
}

func without(ids []int64, id int64) []int64 {
	var out []int64
	for _, x := range ids {
		if x != id {
			out = append(out, x)
		}
	}
	return out
}

func contains64(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
