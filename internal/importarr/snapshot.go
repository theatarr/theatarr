package importarr

import (
	"encoding/json"
	"time"
)

// Snapshot is what one arr instance holds, in a flavour-neutral shape. A fetcher per flavour (Radarr now, Sonarr
// later) fills it from the API; the writer only reads a Snapshot, so both flavours share it.
type Snapshot struct {
	Instance    string
	Flavour     string
	AppVersion  string
	RootFolders []string
	Tags        map[int]string
	Profiles    []QualityProfile
	// PolicySnapshot keeps the complete decision inputs for offline policy exports.
	PolicySnapshot map[string]json.RawMessage
	Titles         []Title
	// History holds the events fetched in this run (new ones, plus a page of overlap).
	History []HistoryEvent
	// HistoryTotal is the arr's total history record count.
	HistoryTotal int
	// Blocklist is the instance's complete blocklist, including entries without a torrent hash.
	Blocklist []BlockedRelease
	// Selection lists the arr's delay and release profiles that Theatarr does not apply (selection.go), one line each.
	Selection []string
	// Collections are a Radarr's collections (collections.go); nil when the instance has none or could not say.
	Collections []Collection
	// Warnings are fetch problems that did not stop the import.
	Warnings []string
}

// Collection is a Radarr collection's intake settings.
type Collection struct {
	TmdbID              int
	Name                string
	Monitored           bool
	SearchOnAdd         bool
	MinimumAvailability string
}

// BlockedRelease is a release the arr has blocklisted, regardless of its recorded failure reason.
type BlockedRelease struct {
	ArrTitleID  int
	InfoHash    string
	SourceTitle string
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

// HistoryEvent is one arr history record after sanitising.
type HistoryEvent struct {
	ArrID        int
	EventType    string // the arr's camelCase name: grabbed, downloadFolderImported, downloadFailed, ...
	Date         time.Time
	ArrTitleID   int
	ArrEpisodeID int
	DownloadID   string
	// InfoHash is the torrent info hash in lower-case hex, when the download is a torrent.
	InfoHash          string
	Protocol          string // torrent, usenet or ""
	SourceTitle       string
	Quality           string
	CustomFormatScore *int
	Languages         []string
	// Data is the record's data map, cut to an allowlist of keys (see sanitizeData).
	Data map[string]string
}

// History event types the registry uses.
const (
	EventGrabbed        = "grabbed"
	EventImported       = "downloadFolderImported"
	EventFailed         = "downloadFailed"
	EventFileDeleted    = "movieFileDeleted"
	EventEpisodeDeleted = "episodeFileDeleted"
)
