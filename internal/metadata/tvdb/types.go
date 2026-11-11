package tvdb

import "encoding/json"

// The records below follow TVDB's v4 OpenAPI (https://thetvdb.github.io/v4-api/), written from knowledge of it and
// kept to the fields we read. Unknown fields are ignored, so a newer API decodes as long as these keep their names.

// envelope is every v4 answer: {"status": "success", "data": ..., "links": ...}.
type envelope struct {
	Status  string          `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	Links   *Links          `json:"links"`
}

// Links is the paging block of a paged answer. Next is null on the last page.
type Links struct {
	Prev       *string `json:"prev"`
	Self       *string `json:"self"`
	Next       *string `json:"next"`
	TotalItems int     `json:"total_items"`
	PageSize   int     `json:"page_size"`
}

// Status is a series status. Name: Continuing, Ended, Upcoming.
type Status struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	RecordType  string `json:"recordType"`
	KeepUpdated bool   `json:"keepUpdated"`
}

// Alias is another name of a series, with its language (ISO 639-2, e.g. "eng", "jpn").
type Alias struct {
	Language string `json:"language"`
	Name     string `json:"name"`
}

// RemoteID is an id elsewhere. SourceName: IMDB, TheMovieDB.com, TV.com, ...; Type is TVDB's numeric source type.
type RemoteID struct {
	ID         string `json:"id"`
	Type       int    `json:"type"`
	SourceName string `json:"sourceName"`
}

// SeasonType is an episode order: Type is the API name (official, dvd, absolute, alternate, regional, altdvd,
// alttwo); "default" in the episodes path means the series' default order.
type SeasonType struct {
	ID            int     `json:"id"`
	Name          string  `json:"name"`
	Type          string  `json:"type"`
	AlternateName *string `json:"alternateName"`
}

// Season is a season of one order (SeasonBaseRecord).
type Season struct {
	ID       int        `json:"id"`
	SeriesID int        `json:"seriesId"`
	Number   int        `json:"number"`
	Name     *string    `json:"name"`
	Type     SeasonType `json:"type"`
	Year     string     `json:"year"`
}

// Translation is a name or overview in one language.
type Translation struct {
	Language  string   `json:"language"`
	Name      string   `json:"name"`
	Overview  string   `json:"overview"`
	Aliases   []string `json:"aliases"`
	IsAlias   bool     `json:"isAlias"`
	IsPrimary bool     `json:"isPrimary"`
}

// Translations are a record's translations (meta=translations).
type Translations struct {
	NameTranslations     []Translation `json:"nameTranslations"`
	OverviewTranslations []Translation `json:"overviewTranslations"`
}

// Series is a series' extended record (GET /series/{id}/extended, short, meta=translations).
type Series struct {
	ID                int           `json:"id"`
	Name              string        `json:"name"`
	Slug              string        `json:"slug"`
	Aliases           []Alias       `json:"aliases"`
	FirstAired        string        `json:"firstAired"`
	LastAired         string        `json:"lastAired"`
	NextAired         string        `json:"nextAired"`
	Status            Status        `json:"status"`
	OriginalCountry   string        `json:"originalCountry"`
	OriginalLanguage  string        `json:"originalLanguage"`
	DefaultSeasonType int           `json:"defaultSeasonType"`
	IsOrderRandomized bool          `json:"isOrderRandomized"`
	LastUpdated       string        `json:"lastUpdated"`
	AverageRuntime    *int          `json:"averageRuntime"`
	Overview          *string       `json:"overview"`
	Year              string        `json:"year"`
	RemoteIDs         []RemoteID    `json:"remoteIds"`
	Seasons           []Season      `json:"seasons"`
	SeasonTypes       []SeasonType  `json:"seasonTypes"`
	Translations      *Translations `json:"translations"`
	NameTranslations  []string      `json:"nameTranslations"`
	// AirsTime is the local time episodes air ("21:00", "" unknown), in the network's country.
	AirsTime        string   `json:"airsTime"`
	OriginalNetwork *Network `json:"originalNetwork"`
	LatestNetwork   *Network `json:"latestNetwork"`
}

// Network is a series' network (a CompanyRecord). Country is ISO 3166-1 alpha-3, lower case ("usa", "jpn").
type Network struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Country string `json:"country"`
}

// Airing reports whether the series is still airing or announced (a day's TTL) rather than ended (a month).
func (s *Series) Airing() bool { return airing(s.Status.Name) }

func airing(status string) bool { return status != "Ended" }

// TranslatedName is the series' name in lang (ISO 639-2), else its name.
func (s *Series) TranslatedName(lang string) string {
	if s.Translations != nil {
		for _, t := range s.Translations.NameTranslations {
			if t.Language == lang && t.Name != "" && !t.IsAlias {
				return t.Name
			}
		}
	}
	return s.Name
}

// HasSeasonType reports whether the series has seasons in the order typ (e.g. "absolute").
func (s *Series) HasSeasonType(typ string) bool {
	for _, x := range s.Seasons {
		if x.Type.Type == typ {
			return true
		}
	}
	for _, x := range s.SeasonTypes {
		if x.Type == typ {
			return true
		}
	}
	return false
}

// Episode is an episode (EpisodeBaseRecord; the extended record adds the airs fields reliably). In a season-type
// listing, SeasonNumber and Number are that order's numbers (in the absolute order: season 1, number = absolute).
type Episode struct {
	ID                int     `json:"id"`
	SeriesID          int     `json:"seriesId"`
	Name              *string `json:"name"`
	Aired             *string `json:"aired"`
	Runtime           *int    `json:"runtime"`
	Overview          *string `json:"overview"`
	SeasonNumber      int     `json:"seasonNumber"`
	Number            int     `json:"number"`
	AbsoluteNumber    *int    `json:"absoluteNumber"`
	AirsAfterSeason   *int    `json:"airsAfterSeason"`
	AirsBeforeSeason  *int    `json:"airsBeforeSeason"`
	AirsBeforeEpisode *int    `json:"airsBeforeEpisode"`
	FinaleType        *string `json:"finaleType"`
	IsMovie           int     `json:"isMovie"`
	LastUpdated       string  `json:"lastUpdated"`
	SeasonName        *string `json:"seasonName"`
}

// hasAirs reports whether any airs field is set.
func (e *Episode) hasAirs() bool {
	return e.AirsAfterSeason != nil || e.AirsBeforeSeason != nil || e.AirsBeforeEpisode != nil
}

// SeriesBase is the series block of an episodes listing (SeriesBaseRecord).
type SeriesBase struct {
	ID     int     `json:"id"`
	Name   string  `json:"name"`
	Slug   string  `json:"slug"`
	Status Status  `json:"status"`
	Year   string  `json:"year"`
	Image  *string `json:"image"`
}

// episodesPage is data of GET /series/{id}/episodes/{season-type}[/{lang}].
type episodesPage struct {
	Series   SeriesBase `json:"series"`
	Episodes []Episode  `json:"episodes"`
}

// SearchResult is one hit of GET /search.
type SearchResult struct {
	ObjectID        string            `json:"objectID"`
	ID              string            `json:"id"` // "series-81189"
	TVDBID          string            `json:"tvdb_id"`
	Type            string            `json:"type"`
	Name            string            `json:"name"`
	Slug            string            `json:"slug"`
	Year            string            `json:"year"`
	FirstAirTime    string            `json:"first_air_time"`
	Status          string            `json:"status"`
	Network         string            `json:"network"`
	Country         string            `json:"country"`
	PrimaryLanguage string            `json:"primary_language"`
	Overview        string            `json:"overview"`  // in the original language
	Overviews       map[string]string `json:"overviews"` // by language ("eng")
	ImageURL        string            `json:"image_url"`
	Aliases         []string          `json:"aliases"`
	Translations    map[string]string `json:"translations"`
	RemoteIDs       []RemoteID        `json:"remote_ids"`
}

// RemoteIDResult is one hit of GET /search/remoteid/{id}: the record the id belongs to, of whichever kind.
type RemoteIDResult struct {
	Series  *SeriesBase `json:"series"`
	Episode *Episode    `json:"episode"`
	Movie   *struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"movie"`
}

// Update is one record of GET /updates. EntityType: series, episodes, seasons, translatedseries,
// translatedepisodes, artwork, ...; RecordID is the changed record, SeriesID its series where TVDB gives it.
type Update struct {
	EntityType        string `json:"entityType"`
	Method            string `json:"method"`
	RecordID          int    `json:"recordId"`
	SeriesID          int    `json:"seriesId"`
	MergeToID         int    `json:"mergeToId"`
	MergeToEntityType string `json:"mergeToEntityType"`
	TimeStamp         int64  `json:"timeStamp"`
}
