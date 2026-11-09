package tmdb

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TV lookups for Torrentio pack sizes: the series is found by its IMDb id and each season's episode_count taken.
// Cached a day.

const tvTTL = 24 * time.Hour

// FindTVByIMDb returns the TMDb TV id for an IMDb id (tt...). ErrNotFound when TMDb has none.
func (c *Client) FindTVByIMDb(ctx context.Context, imdbID string) (int, error) {
	if !strings.HasPrefix(imdbID, "tt") || len(imdbID) < 3 {
		return 0, fmt.Errorf("tmdb: %q is not an IMDb id", imdbID)
	}
	q := url.Values{}
	q.Set("external_source", "imdb_id")
	var r struct {
		TVResults []struct {
			ID int `json:"id"`
		} `json:"tv_results"`
	}
	if err := c.get(ctx, "/find/"+url.PathEscape(imdbID), q, &r, func(int, []byte) time.Duration { return 7 * 24 * time.Hour }); err != nil {
		return 0, err
	}
	if len(r.TVResults) == 0 {
		return 0, ErrNotFound
	}
	return r.TVResults[0].ID, nil
}

// SeasonEpisodeCounts returns {season number: episode count} for a TMDb TV id, seasons with no episodes left out.
func (c *Client) SeasonEpisodeCounts(ctx context.Context, tvID int) (map[int]int, error) {
	if tvID <= 0 {
		return nil, fmt.Errorf("tmdb: bad TV id %d", tvID)
	}
	var r struct {
		Seasons []struct {
			SeasonNumber int `json:"season_number"`
			EpisodeCount int `json:"episode_count"`
		} `json:"seasons"`
	}
	if err := c.get(ctx, "/tv/"+strconv.Itoa(tvID), nil, &r, func(int, []byte) time.Duration { return tvTTL }); err != nil {
		return nil, err
	}
	out := map[int]int{}
	for _, s := range r.Seasons {
		if s.EpisodeCount > 0 {
			out[s.SeasonNumber] = s.EpisodeCount
		}
	}
	return out, nil
}

// TVSeries is what the series refresh reads of a TMDb series when TVDB lacks it (internal/metadata/tvrefresh):
// GET /tv/{id}, the same cached answer SeasonEpisodeCounts reads.
type TVSeries struct {
	ID               int      `json:"id"`
	Name             string   `json:"name"`
	OriginalLanguage string   `json:"original_language"`
	FirstAirDate     string   `json:"first_air_date"`
	Status           string   `json:"status"` // Returning Series, Ended, Canceled, In Production, Planned, Pilot
	EpisodeRunTime   []int    `json:"episode_run_time"`
	OriginCountry    []string `json:"origin_country"` // ISO 3166-1 alpha-2
	Genres           []Genre  `json:"genres"`
	Seasons          []struct {
		SeasonNumber int `json:"season_number"`
		EpisodeCount int `json:"episode_count"`
	} `json:"seasons"`
}

// TVSeason is one season with its episodes (GET /tv/{id}/season/{n}).
type TVSeason struct {
	ID           int         `json:"id"`
	SeasonNumber int         `json:"season_number"`
	Episodes     []TVEpisode `json:"episodes"`
}

// TVEpisode is an episode of a TVSeason. AirDate is YYYY-MM-DD or empty.
type TVEpisode struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	AirDate       string `json:"air_date"`
	EpisodeNumber int    `json:"episode_number"`
	SeasonNumber  int    `json:"season_number"`
	Runtime       *int   `json:"runtime"`
}

// TVShow returns a series' record (cached a day). ErrNotFound when TMDb has none.
func (c *Client) TVShow(ctx context.Context, tvID int) (*TVSeries, error) {
	if tvID <= 0 {
		return nil, fmt.Errorf("tmdb: bad TV id %d", tvID)
	}
	var s TVSeries
	if err := c.get(ctx, "/tv/"+strconv.Itoa(tvID), nil, &s, func(int, []byte) time.Duration { return tvTTL }); err != nil {
		return nil, err
	}
	return &s, nil
}

// TVSeasonEpisodes returns one season of a series with its episodes (cached a day).
func (c *Client) TVSeasonEpisodes(ctx context.Context, tvID, season int) (*TVSeason, error) {
	if tvID <= 0 || season < 0 {
		return nil, fmt.Errorf("tmdb: bad TV id %d or season %d", tvID, season)
	}
	var s TVSeason
	if err := c.get(ctx, "/tv/"+strconv.Itoa(tvID)+"/season/"+strconv.Itoa(season), nil, &s,
		func(int, []byte) time.Duration { return tvTTL }); err != nil {
		return nil, err
	}
	return &s, nil
}
