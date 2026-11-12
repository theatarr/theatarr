package importarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// Radarr v3 resources, the fields the importer reads (Radarr.Api.V3 @ v6.4.4).

type radarrStatus struct {
	Version string `json:"version"`
	AppName string `json:"appName"`
}

type radarrRootFolder struct {
	ID   int    `json:"id"`
	Path string `json:"path"`
}

type radarrTag struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

type radarrProfile struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	UpgradeAllowed    bool   `json:"upgradeAllowed"`
	Cutoff            int    `json:"cutoff"`
	MinFormatScore    int    `json:"minFormatScore"`
	CutoffFormatScore int    `json:"cutoffFormatScore"`
	Items             []struct {
		Allowed bool   `json:"allowed"`
		ID      int    `json:"id"`
		Name    string `json:"name"`
		Quality *struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"quality"`
		Items []struct {
			Allowed bool `json:"allowed"`
			Quality *struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
			} `json:"quality"`
		} `json:"items"`
	} `json:"items"`
}

// cutoffName finds the name of the profile's cutoff item: a quality id, or a group id.
func (p radarrProfile) cutoffName() string {
	for _, it := range p.Items {
		if it.Quality != nil && it.Quality.ID == p.Cutoff {
			return it.Quality.Name
		}
		if it.Quality == nil && it.ID == p.Cutoff {
			return it.Name
		}
	}
	return ""
}

type radarrMovie struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	SortTitle     string `json:"sortTitle"`
	Year          int    `json:"year"`
	Runtime       int    `json:"runtime"`
	SecondaryYear *int   `json:"secondaryYear"`
	// OriginalLanguage is Radarr's MovieMetadata.OriginalLanguage (TMDb's original_language, else English).
	OriginalLanguage *idName `json:"originalLanguage"`
	AlternateTitles  []struct {
		SourceType string `json:"sourceType"`
		Title      string `json:"title"`
	} `json:"alternateTitles"`
	TmdbID              int              `json:"tmdbId"`
	ImdbID              string           `json:"imdbId"`
	Monitored           bool             `json:"monitored"`
	HasFile             bool             `json:"hasFile"`
	MovieFileID         int              `json:"movieFileId"`
	QualityProfileID    int              `json:"qualityProfileId"`
	MinimumAvailability string           `json:"minimumAvailability"`
	Status              string           `json:"status"`
	Path                string           `json:"path"`
	RootFolderPath      string           `json:"rootFolderPath"`
	Tags                []int            `json:"tags"`
	Added               time.Time        `json:"added"`
	InCinemas           *time.Time       `json:"inCinemas"`
	DigitalRelease      *time.Time       `json:"digitalRelease"`
	PhysicalRelease     *time.Time       `json:"physicalRelease"`
	MovieFile           *radarrMovieFile `json:"movieFile"`
	Collection          *struct {
		TmdbID int `json:"tmdbId"`
	} `json:"collection"`
}

type radarrMovieFile struct {
	ID                  int             `json:"id"`
	MovieID             int             `json:"movieId"`
	RelativePath        string          `json:"relativePath"`
	Path                string          `json:"path"`
	Size                int64           `json:"size"`
	DateAdded           time.Time       `json:"dateAdded"`
	SceneName           string          `json:"sceneName"`
	ReleaseGroup        string          `json:"releaseGroup"`
	Edition             string          `json:"edition"`
	Languages           []idName        `json:"languages"`
	Quality             qualityModel    `json:"quality"`
	CustomFormatScore   *int            `json:"customFormatScore"`
	IndexerFlags        int             `json:"indexerFlags"`
	MediaInfo           json.RawMessage `json:"mediaInfo"`
	OriginalFilePath    string          `json:"originalFilePath"`
	QualityCutoffNotMet bool            `json:"qualityCutoffNotMet"`
}

// movieFileBatch is how many movie ids go in one GET moviefile?movieId=...
const movieFileBatch = 50

// FetchRadarr reads one Radarr instance. known reports whether a history id is already stored, so history paging
// stops at the stored part.
func FetchRadarr(ctx context.Context, g Getter, instance string, known func(int) bool) (*Snapshot, error) {
	s := &Snapshot{Instance: instance, Flavour: "radarr", Tags: map[int]string{}}

	if err := readStatus(ctx, g, s, "Radarr"); err != nil {
		return nil, err
	}
	if err := fetchReference(ctx, g, s); err != nil {
		return nil, err
	}
	if err := readRadarrMovies(ctx, g, s); err != nil {
		return nil, err
	}

	if err := fetchCollections(ctx, g, s); err != nil {
		return nil, err
	}

	recs, total, err := fetchHistory(ctx, g, known)
	if err != nil {
		return nil, err
	}
	s.HistoryTotal = total
	for _, r := range recs {
		s.History = append(s.History, toEvent(r))
	}
	if err := fetchBlocklist(ctx, g, s); err != nil {
		return nil, err
	}
	return s, nil
}

// readStatus reads system/status into s.AppVersion and checks that the instance is app (Radarr or Sonarr).
func readStatus(ctx context.Context, g Getter, s *Snapshot, app string) error {
	var st radarrStatus
	if err := g.GetJSON(ctx, "system/status", nil, &st); err != nil {
		return err
	}
	if st.AppName != "" && st.AppName != app {
		name := s.Instance
		if name == "" {
			name = "the instance"
		}
		return fmt.Errorf("%s: system/status says %q, not %s", name, st.AppName, app)
	}
	s.AppVersion = st.Version
	return nil
}

// readRadarrMovies reads the movies (GET movie) and their files (GET moviefile in batches) into s.Titles.
func readRadarrMovies(ctx context.Context, g Getter, s *Snapshot) error {
	var movies []radarrMovie
	if err := g.GetJSON(ctx, "movie", nil, &movies); err != nil {
		return err
	}
	sort.Slice(movies, func(i, j int) bool { return movies[i].ID < movies[j].ID })

	// GET moviefile adds what the movie list leaves out (custom format score, scene name on some builds).
	files := map[int]*radarrMovieFile{}
	var withFile []int
	for _, m := range movies {
		if m.HasFile || m.MovieFile != nil {
			withFile = append(withFile, m.ID)
		}
	}
	for i := 0; i < len(withFile); i += movieFileBatch {
		q := url.Values{}
		for _, id := range withFile[i:min(i+movieFileBatch, len(withFile))] {
			q.Add("movieId", strconv.Itoa(id))
		}
		var batch []radarrMovieFile
		if err := g.GetJSON(ctx, "moviefile", q, &batch); err != nil {
			return err
		}
		for k := range batch {
			files[batch[k].MovieID] = &batch[k]
		}
	}

	for _, m := range movies {
		t := Title{
			ArrID: m.ID, Kind: "movie", Name: m.Title, SortName: m.SortTitle, Year: m.Year, TmdbID: m.TmdbID,
			ImdbID: m.ImdbID, Monitored: m.Monitored, QualityProfileID: m.QualityProfileID,
			MinimumAvailability: m.MinimumAvailability, Status: m.Status, Path: m.Path, RootFolderPath: m.RootFolderPath,
			Tags: m.Tags, Added: m.Added.UTC(), Runtime: m.Runtime,
		}
		if m.SecondaryYear != nil {
			t.SecondaryYear = *m.SecondaryYear
		}
		if m.OriginalLanguage != nil {
			t.OriginalLanguage = m.OriginalLanguage.Name
		}
		for _, a := range m.AlternateTitles {
			if a.Title != "" {
				t.Aliases = append(t.Aliases, Alias{Kind: AliasAlternative, Name: a.Title})
			}
		}
		if m.Collection != nil {
			t.CollectionTmdbID = m.Collection.TmdbID
		}
		for _, d := range []struct {
			typ string
			at  *time.Time
		}{{"theatrical", m.InCinemas}, {"digital", m.DigitalRelease}, {"physical", m.PhysicalRelease}} {
			if d.at != nil && !d.at.IsZero() {
				t.Dates = append(t.Dates, ReleaseDate{Type: d.typ, Date: d.at.UTC().Format("2006-01-02")})
			}
		}
		mf := files[m.ID]
		if mf == nil {
			mf = m.MovieFile
		} else if m.MovieFile != nil {
			// The movie list fills path; some builds leave it out of GET moviefile.
			if mf.Path == "" {
				mf.Path = m.MovieFile.Path
			}
			if mf.SceneName == "" {
				mf.SceneName = m.MovieFile.SceneName
			}
		}
		if mf != nil && mf.ID != 0 {
			t.File = radarrFile(mf, m.Path)
		}
		s.Titles = append(s.Titles, t)
	}
	return nil
}

func radarrFile(mf *radarrMovieFile, moviePath string) *File {
	f := &File{
		ArrID: mf.ID, Path: mf.Path, RelativePath: mf.RelativePath, Size: mf.Size,
		Quality: mf.Quality.Quality.Name, Revision: mf.Quality.Revision.Version, ReleaseGroup: mf.ReleaseGroup,
		SceneName: mf.SceneName, Edition: mf.Edition, Languages: languageNames(mf.Languages),
		CustomFormatScore: mf.CustomFormatScore, IndexerFlags: mf.IndexerFlags, OriginalFilePath: mf.OriginalFilePath,
		CutoffNotMet: mf.QualityCutoffNotMet, Added: mf.DateAdded.UTC(),
	}
	if f.Path == "" && moviePath != "" && mf.RelativePath != "" {
		f.Path = moviePath + "/" + mf.RelativePath
	}
	if len(mf.MediaInfo) > 0 && string(mf.MediaInfo) != "null" {
		f.MediaInfo = mf.MediaInfo
	}
	return f
}
