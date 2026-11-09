package anime

import (
	"context"
	"encoding/json"
	"fmt"
)

// Sources are what the pipeline reads. Each has two kinds of backend: a cache adapter over an offline response cache
// (package offlinecache, used by the golden fixture tests) and live adapters (package live, through internal/gateway,
// with the responses cached in the database). TVDB has a third: our own imported Sonarr episodes (DBTVDB).
type Sources struct {
	AniList AniList
	Kitsu   Kitsu
	TVDB    TVDB
	XEM     XEM
	// Movies looks TMDb ids up and searches movies by title (the golden outputs used Radarr's lookup, which proxies TMDb).
	Movies Movies
	// Library lists the TVDB episodes the Sonarr has; nil, or a nil map from it, means unknown.
	Library Library
	// Lists are the mapping lists and id joins (ScudLee, Fribb, anibridge, manami).
	Lists *Lists
}

// AniList fetches media by id. Implementations batch the ids sorted, 50 per request, and return
// the media in response order.
type AniList interface {
	Media(ctx context.Context, ids []int) ([]*Media, error)
}

// Kitsu fetches one anime with its media relationships (JSON:API document).
type Kitsu interface {
	Anime(ctx context.Context, id int) (*KitsuDoc, error)
}

// TVDB returns a series with its episodes. An error means the series is unavailable (deleted or merged ids).
type TVDB interface {
	Show(ctx context.Context, tvdbID int) (*Show, error)
}

// XEM returns TheXEM's scene mapping for a TVDB series.
type XEM interface {
	Map(ctx context.Context, tvdbID int) (*XEMMap, error)
}

// Movies looks movies up. LookupTMDb returns nil (and no error) when the id is unknown or the lookup failed.
type Movies interface {
	LookupTMDb(ctx context.Context, tmdbID int) (*MovieInfo, error)
	Search(ctx context.Context, term string) ([]MovieInfo, error)
}

// Library reports which TVDB episodes the anime Sonarr has, per TVDB series id. A nil map means unavailable.
type Library interface {
	SonarrEpisodes(ctx context.Context, tvdbIDs []int) (map[int]map[SE]bool, error)
}

// Show is a TVDB series in SkyHook's shape (what Sonarr gets from its metadata proxy).
type Show struct {
	TVDBID     int             `json:"tvdbId"`
	Title      *string         `json:"title"`
	AniListIDs json.RawMessage `json:"aniListIds,omitempty"`
	Episodes   []ShowEpisode   `json:"episodes"`
	// HasEpisodes is false when the response had no "episodes" key.
	HasEpisodes bool `json:"-"`
}

// ShowEpisode is one TVDB episode.
type ShowEpisode struct {
	SeasonNumber             int     `json:"seasonNumber"`
	EpisodeNumber            int     `json:"episodeNumber"`
	AbsoluteEpisodeNumber    *int    `json:"absoluteEpisodeNumber"`
	AiredAfterSeasonNumber   *int    `json:"airedAfterSeasonNumber"`
	AiredBeforeSeasonNumber  *int    `json:"airedBeforeSeasonNumber"`
	AiredBeforeEpisodeNumber *int    `json:"airedBeforeEpisodeNumber"`
	Title                    *string `json:"title"`
	AirDate                  *string `json:"airDate"`
	Runtime                  *int    `json:"runtime"`
	Overview                 *string `json:"overview"`
}

// UnmarshalJSON records whether "episodes" was present.
func (s *Show) UnmarshalJSON(b []byte) error {
	type plain Show
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		return err
	}
	_, p.HasEpisodes = keys["episodes"]
	*s = Show(p)
	return nil
}

// KitsuDoc is a Kitsu JSON:API document for one anime.
type KitsuDoc struct {
	Data struct {
		Relationships struct {
			MediaRelationships struct {
				Data []KitsuRef `json:"data"`
			} `json:"mediaRelationships"`
		} `json:"relationships"`
	} `json:"data"`
	Included []KitsuIncluded `json:"included"`
}

// KitsuRef is a JSON:API resource identifier.
type KitsuRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// KitsuIncluded is an included resource: a media relationship or an anime.
type KitsuIncluded struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	Attributes struct {
		Role           string           `json:"role"`
		CanonicalTitle *string          `json:"canonicalTitle"`
		Titles         orderedStringMap `json:"titles"`
		Subtype        *string          `json:"subtype"`
		StartDate      *string          `json:"startDate"`
		EndDate        *string          `json:"endDate"`
		EpisodeCount   *int             `json:"episodeCount"`
		EpisodeLength  *int             `json:"episodeLength"`
	} `json:"attributes"`
	Relationships struct {
		Destination struct {
			Data *KitsuRef `json:"data"`
		} `json:"destination"`
	} `json:"relationships"`
}

// orderedStringMap keeps a JSON object's values in document order (Kitsu's titles, listed in that order).
type orderedStringMap struct {
	Keys   []string
	Values []*string
}

func (m *orderedStringMap) UnmarshalJSON(b []byte) error {
	*m = orderedStringMap{}
	if string(b) == "null" {
		return nil
	}
	return decodeOrdered(b, func(k string, v json.RawMessage) error {
		var s *string
		if err := json.Unmarshal(v, &s); err != nil {
			s = nil
		}
		m.Keys = append(m.Keys, k)
		m.Values = append(m.Values, s)
		return nil
	})
}

// XEMMap is TheXEM's map/all answer.
type XEMMap struct {
	Result  string `json:"result"`
	Message string `json:"message"`
	Data    []struct {
		Scene *XEMNumber `json:"scene"`
		TVDB  *XEMNumber `json:"tvdb"`
	} `json:"data"`
}

// XEMNumber is one numbering in a TheXEM row.
type XEMNumber struct {
	Season   int `json:"season"`
	Episode  int `json:"episode"`
	Absolute int `json:"absolute"`
}

// MovieInfo is a movie as the lookup returns it (the Radarr lookup fields the pipeline uses).
type MovieInfo struct {
	TMDbID        int     `json:"tmdbId"`
	Title         *string `json:"title"`
	OriginalTitle *string `json:"originalTitle"`
	Year          *int    `json:"year"`
	InCinemas     *string `json:"inCinemas"`
	Collection    *struct {
		Title *string `json:"title"`
	} `json:"collection"`
}

// TVDBChain asks each backend in turn and returns the first series found (e.g. our imported Sonarr, then a
// SkyHook-shaped cache).
type TVDBChain []TVDB

// Show implements TVDB.
func (c TVDBChain) Show(ctx context.Context, id int) (*Show, error) {
	var first error
	for _, t := range c {
		s, err := t.Show(ctx, id)
		if err == nil && s != nil {
			return s, nil
		}
		if first == nil {
			first = err
		}
	}
	if first == nil {
		first = fmt.Errorf("tvdb %d: no backend", id)
	}
	return nil, first
}
