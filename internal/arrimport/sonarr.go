package arrimport

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Sonarr v3 resources, the fields ReadTitles reads (Sonarr.Api.V3 @ v4.0.20).

type sonarrSeries struct {
	ID              int    `json:"id"`
	Title           string `json:"title"`
	SortTitle       string `json:"sortTitle"`
	Year            int    `json:"year"`
	TvdbID          int    `json:"tvdbId"`
	TmdbID          int    `json:"tmdbId"`
	ImdbID          string `json:"imdbId"`
	AlternateTitles []struct {
		Title             string `json:"title"`
		SeasonNumber      *int   `json:"seasonNumber"`
		SceneSeasonNumber *int   `json:"sceneSeasonNumber"`
		SceneOrigin       string `json:"sceneOrigin"`
		Comment           string `json:"comment"`
	} `json:"alternateTitles"`
	Monitored       bool   `json:"monitored"`
	MonitorNewItems string `json:"monitorNewItems"`
	Seasons         []struct {
		SeasonNumber int  `json:"seasonNumber"`
		Monitored    bool `json:"monitored"`
	} `json:"seasons"`
	QualityProfileID  int        `json:"qualityProfileId"`
	Status            string     `json:"status"`
	Path              string     `json:"path"`
	RootFolderPath    string     `json:"rootFolderPath"`
	Tags              []int      `json:"tags"`
	Added             time.Time  `json:"added"`
	FirstAired        *time.Time `json:"firstAired"`
	SeriesType        string     `json:"seriesType"`
	UseSceneNumbering bool       `json:"useSceneNumbering"`
	Runtime           int        `json:"runtime"`
	OriginalLanguage  *idName    `json:"originalLanguage"`
}

type sonarrEpisode struct {
	ID                         int        `json:"id"`
	SeriesID                   int        `json:"seriesId"`
	EpisodeFileID              int        `json:"episodeFileId"`
	SeasonNumber               int        `json:"seasonNumber"`
	EpisodeNumber              int        `json:"episodeNumber"`
	Title                      string     `json:"title"`
	AirDate                    string     `json:"airDate"`
	AirDateUtc                 *time.Time `json:"airDateUtc"`
	Runtime                    int        `json:"runtime"`
	HasFile                    bool       `json:"hasFile"`
	Monitored                  bool       `json:"monitored"`
	AbsoluteEpisodeNumber      *int       `json:"absoluteEpisodeNumber"`
	SceneAbsoluteEpisodeNumber *int       `json:"sceneAbsoluteEpisodeNumber"`
	SceneEpisodeNumber         *int       `json:"sceneEpisodeNumber"`
	SceneSeasonNumber          *int       `json:"sceneSeasonNumber"`
}

type sonarrEpisodeFile struct {
	ID                  int             `json:"id"`
	SeriesID            int             `json:"seriesId"`
	SeasonNumber        int             `json:"seasonNumber"`
	RelativePath        string          `json:"relativePath"`
	Path                string          `json:"path"`
	Size                int64           `json:"size"`
	DateAdded           time.Time       `json:"dateAdded"`
	SceneName           string          `json:"sceneName"`
	ReleaseGroup        string          `json:"releaseGroup"`
	Languages           []idName        `json:"languages"`
	Quality             qualityModel    `json:"quality"`
	CustomFormatScore   *int            `json:"customFormatScore"`
	IndexerFlags        *int            `json:"indexerFlags"`
	ReleaseType         string          `json:"releaseType"`
	MediaInfo           json.RawMessage `json:"mediaInfo"`
	QualityCutoffNotMet bool            `json:"qualityCutoffNotMet"`
}

// readSonarrSeries reads the series (GET series) and, per series with a TVDB id, its episodes and episode files into
// s.Titles. progress, when not nil, is called after each series.
func readSonarrSeries(ctx context.Context, g Getter, s *Snapshot, progress func(done, total int)) error {
	var series []sonarrSeries
	if err := g.GetJSON(ctx, "series", nil, &series); err != nil {
		return err
	}
	sort.Slice(series, func(i, j int) bool { return series[i].ID < series[j].ID })
	for i, sr := range series {
		t := Title{
			ArrID: sr.ID, Kind: "series", Name: sr.Title, SortName: sr.SortTitle, Year: sr.Year, TvdbID: sr.TvdbID,
			TmdbID: sr.TmdbID, ImdbID: sr.ImdbID, Monitored: sr.Monitored, QualityProfileID: sr.QualityProfileID,
			Status: sr.Status, Path: sr.Path, RootFolderPath: sr.RootFolderPath, Tags: sr.Tags, Added: sr.Added.UTC(),
			SeriesType: sr.SeriesType, UseSceneNumbering: sr.UseSceneNumbering, Runtime: sr.Runtime,
			MonitorNewItems: sr.MonitorNewItems,
		}
		for _, se := range sr.Seasons {
			if t.SeasonsMonitored == nil {
				t.SeasonsMonitored = map[int]bool{}
			}
			t.SeasonsMonitored[se.SeasonNumber] = se.Monitored
		}
		if sr.OriginalLanguage != nil {
			t.OriginalLanguage = sr.OriginalLanguage.Name
		}
		if sr.FirstAired != nil && !sr.FirstAired.IsZero() {
			t.Dates = append(t.Dates, ReleaseDate{Type: "premiere", Date: sr.FirstAired.UTC().Format("2006-01-02")})
		}
		for _, a := range sr.AlternateTitles {
			if strings.TrimSpace(a.Title) == "" {
				continue
			}
			t.SceneMappings = append(t.SceneMappings, SceneMapping{Name: a.Title, SeasonNumber: a.SeasonNumber,
				SceneSeasonNumber: a.SceneSeasonNumber, SceneOrigin: a.SceneOrigin, Comment: a.Comment})
		}
		if t.TvdbID != 0 {
			if err := fetchEpisodes(ctx, g, &t); err != nil {
				return err
			}
		}
		s.Titles = append(s.Titles, t)
		if progress != nil {
			progress(i+1, len(series))
		}
	}
	return nil
}

// readRootFolders reads the instance's root folders into s.RootFolders, sorted.
func readRootFolders(ctx context.Context, g Getter, s *Snapshot) error {
	var roots []radarrRootFolder
	if err := g.GetJSON(ctx, "rootfolder", nil, &roots); err != nil {
		return err
	}
	for _, r := range roots {
		s.RootFolders = append(s.RootFolders, r.Path)
	}
	sort.Strings(s.RootFolders)
	return nil
}

// readProfiles reads the quality profiles into s.Profiles.
func readProfiles(ctx context.Context, g Getter, s *Snapshot) error {
	var body json.RawMessage
	if err := g.GetJSON(ctx, "qualityprofile", nil, &body); err != nil {
		return err
	}
	var profiles []radarrProfile
	if err := json.Unmarshal(body, &profiles); err != nil {
		return err
	}
	for _, p := range profiles {
		items, _ := json.Marshal(p.Items)
		s.Profiles = append(s.Profiles, QualityProfile{
			QualityItems: string(items), ArrID: p.ID, Name: p.Name, Cutoff: p.cutoffName(), UpgradeAllowed: p.UpgradeAllowed,
			MinFormatScore: p.MinFormatScore, CutoffFormatScore: p.CutoffFormatScore,
		})
	}
	return nil
}

// fetchEpisodes reads one series' episodes and episode files.
func fetchEpisodes(ctx context.Context, g Getter, t *Title) error {
	q := url.Values{}
	q.Set("seriesId", strconv.Itoa(t.ArrID))
	var eps []sonarrEpisode
	if err := g.GetJSON(ctx, "episode", q, &eps); err != nil {
		return err
	}
	var files []sonarrEpisodeFile
	if err := g.GetJSON(ctx, "episodefile", q, &files); err != nil {
		return err
	}
	sort.Slice(eps, func(i, j int) bool {
		if eps[i].SeasonNumber != eps[j].SeasonNumber {
			return eps[i].SeasonNumber < eps[j].SeasonNumber
		}
		if eps[i].EpisodeNumber != eps[j].EpisodeNumber {
			return eps[i].EpisodeNumber < eps[j].EpisodeNumber
		}
		return eps[i].ID < eps[j].ID
	})
	byFile := map[int][]int{}
	for _, e := range eps {
		ep := Episode{ArrID: e.ID, Season: e.SeasonNumber, Number: e.EpisodeNumber, Absolute: e.AbsoluteEpisodeNumber,
			SceneSeason: e.SceneSeasonNumber, SceneNumber: e.SceneEpisodeNumber, SceneAbsolute: e.SceneAbsoluteEpisodeNumber,
			Name: e.Title, AirDate: e.AirDate, Runtime: e.Runtime, Monitored: e.Monitored, FileArrID: e.EpisodeFileID}
		if e.AirDateUtc != nil && !e.AirDateUtc.IsZero() {
			u := e.AirDateUtc.UTC()
			ep.AirDateUTC = &u
		}
		if !e.HasFile && e.EpisodeFileID == 0 {
			ep.FileArrID = 0
		}
		if ep.FileArrID != 0 {
			byFile[ep.FileArrID] = append(byFile[ep.FileArrID], e.ID)
		}
		t.Episodes = append(t.Episodes, ep)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ID < files[j].ID })
	for i := range files {
		ef := &files[i]
		f := &File{
			ArrID: ef.ID, Path: ef.Path, RelativePath: ef.RelativePath, Size: ef.Size,
			Quality: ef.Quality.Quality.Name, Revision: ef.Quality.Revision.Version, ReleaseGroup: ef.ReleaseGroup,
			SceneName: ef.SceneName, Languages: languageNames(ef.Languages), CustomFormatScore: ef.CustomFormatScore,
			CutoffNotMet: ef.QualityCutoffNotMet, Added: ef.DateAdded.UTC(), ReleaseType: ef.ReleaseType,
			EpisodeArrIDs: byFile[ef.ID],
		}
		if ef.IndexerFlags != nil {
			f.IndexerFlags = *ef.IndexerFlags
		}
		if f.Path == "" && t.Path != "" && ef.RelativePath != "" {
			f.Path = t.Path + "/" + ef.RelativePath
		}
		if len(ef.MediaInfo) > 0 && string(ef.MediaInfo) != "null" {
			f.MediaInfo = ef.MediaInfo
		}
		t.Files = append(t.Files, f)
	}
	return nil
}
