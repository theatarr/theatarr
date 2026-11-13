package arrimport

import (
	"encoding/json"
	"time"
)

// Snapshot is what ReadTitles reads from one arr instance, in a flavour-neutral shape.
type Snapshot struct {
	Flavour     string
	AppVersion  string
	RootFolders []string
	Profiles    []QualityProfile
	Titles      []Title
}

// QualityProfile is the part of an arr quality profile kept for reference.
type QualityProfile struct {
	QualityItems      string
	ArrID             int
	Name              string
	Cutoff            string
	UpgradeAllowed    bool
	MinFormatScore    int
	CutoffFormatScore int
}

// Title is a movie or a series as the arr has it.
type Title struct {
	ArrID               int
	Kind                string // movie or series
	Name                string
	SortName            string
	Year                int
	TmdbID              int
	ImdbID              string
	TvdbID              int
	CollectionTmdbID    int
	Monitored           bool
	QualityProfileID    int
	MinimumAvailability string
	Status              string
	Path                string
	RootFolderPath      string
	Tags                []int
	Added               time.Time
	Dates               []ReleaseDate
	// SecondaryYear is the arr's secondary year (0 when none): releases dated that year map to the title too.
	SecondaryYear int
	// Aliases are the arr's alternative titles of the title.
	Aliases []Alias
	// File is the arr's current file, nil when it has none (movies).
	File *File

	// Series only (Sonarr).
	// SeriesType is standard, daily or anime; UseSceneNumbering is the arr's flag (XEM scene numbers exist).
	SeriesType        string
	UseSceneNumbering bool
	// Runtime is the movie or series runtime in minutes (0 unknown); OriginalLanguage the arr's language name.
	Runtime          int
	OriginalLanguage string
	// SceneMappings are the arr's scene mappings of the series (alternateTitles).
	SceneMappings []SceneMapping
	// MonitorNewItems is the arr's choice for seasons that appear later (all or none; "" from an arr without it), and
	// SeasonsMonitored its seasons' monitored flags: the version's monitoring intent (series_monitor).
	MonitorNewItems  string
	SeasonsMonitored map[int]bool
	// Episodes are the series' episodes; Files its episode files, each with the arr ids of its episodes.
	Episodes []Episode
	Files    []*File
}

// SceneMapping is one of Sonarr's scene mappings of a series (AlternateTitleResource). SeasonNumber and
// SceneSeasonNumber are nil when the arr gives none.
type SceneMapping struct {
	Name              string
	SeasonNumber      *int
	SceneSeasonNumber *int
	SceneOrigin       string
	Comment           string
}

// Episode is a Sonarr episode in TVDB numbering, with its absolute and scene numbers when the arr has them.
type Episode struct {
	ArrID         int
	Season        int
	Number        int
	Absolute      *int
	SceneSeason   *int
	SceneNumber   *int
	SceneAbsolute *int
	Name          string
	AirDate       string // YYYY-MM-DD, "" when unknown
	AirDateUTC    *time.Time
	Runtime       int
	Monitored     bool
	// FileArrID is the arr's episode file id, 0 when the episode has no file.
	FileArrID int
}

// ReleaseDate is one dated release of a title. Type is one of premiere, theatrical_limited, theatrical, digital,
// physical, tv. Date is YYYY-MM-DD.
type ReleaseDate struct {
	Country       string
	Type          string
	Date          string
	Certification string
	Note          string
}

// Alias is an alternative title or a translation of a title (table title_alias). Kind is alternative or
// translation; Language (ISO 639-1) and Country (ISO 3166-1) are empty when the source gives none.
type Alias struct {
	Kind     string
	Name     string
	Language string
	Country  string
}

// Alias kinds.
const (
	AliasAlternative = "alternative"
	AliasTranslation = "translation"
)

// File is an arr media file.
type File struct {
	ArrID             int
	Path              string
	RelativePath      string
	Size              int64
	Quality           string
	Revision          int
	ReleaseGroup      string
	SceneName         string
	Edition           string
	Languages         []string
	MediaInfo         json.RawMessage
	CustomFormatScore *int
	IndexerFlags      int
	OriginalFilePath  string
	CutoffNotMet      bool
	Added             time.Time
	// ReleaseType is Sonarr's release type of the file (singleEpisode, multiEpisode, seasonPack), "" for movies.
	ReleaseType string
	// EpisodeArrIDs are the arr ids of the episodes the file holds (series).
	EpisodeArrIDs []int
}
