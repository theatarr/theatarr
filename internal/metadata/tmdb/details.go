package tmdb

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Details calls for the meta.art job (docs/design/ui.md): a movie or a series with what the title page shows
// (overview, tagline, art paths, rating, certification, status, network or studio) in one request each. They skip
// the response cache: the job's title_meta table is their cache, and a refresh must see TMDb's current answer.

// ImageLanguages is the include_image_language of the details calls: English art first, then art without text.
const ImageLanguages = "en,null"

// Company is a production company (movies) or a network (TV).
type Company struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Image is one image of a title: a TMDb path ('/abc.png') with its language (empty for no text).
type Image struct {
	FilePath    string  `json:"file_path"`
	Language    string  `json:"iso_639_1"`
	AspectRatio float64 `json:"aspect_ratio"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	VoteAverage float64 `json:"vote_average"`
	VoteCount   int     `json:"vote_count"`
}

// Images are a title's posters, backdrops and logos (append_to_response=images).
type Images struct {
	Posters   []Image `json:"posters"`
	Backdrops []Image `json:"backdrops"`
	Logos     []Image `json:"logos"`
}

// Genre is a TMDb genre.
type Genre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// TV is a series' details with external ids, content ratings and images (one request, append_to_response).
type TV struct {
	ID               int       `json:"id"`
	Name             string    `json:"name"`
	OriginalName     string    `json:"original_name"`
	OriginalLanguage string    `json:"original_language"`
	Overview         string    `json:"overview"`
	Tagline          string    `json:"tagline"`
	PosterPath       string    `json:"poster_path"`
	BackdropPath     string    `json:"backdrop_path"`
	FirstAirDate     string    `json:"first_air_date"`
	LastAirDate      string    `json:"last_air_date"`
	Status           string    `json:"status"`
	InProduction     bool      `json:"in_production"`
	EpisodeRunTime   []int     `json:"episode_run_time"`
	VoteAverage      float64   `json:"vote_average"`
	VoteCount        int       `json:"vote_count"`
	Genres           []Genre   `json:"genres"`
	Networks         []Company `json:"networks"`
	NextEpisodeToAir *struct {
		AirDate string `json:"air_date"`
	} `json:"next_episode_to_air"`
	LastEpisodeToAir *struct {
		AirDate string `json:"air_date"`
		Runtime int    `json:"runtime"`
	} `json:"last_episode_to_air"`
	ExternalIDs struct {
		IMDbID string `json:"imdb_id"`
		TVDbID int    `json:"tvdb_id"`
	} `json:"external_ids"`
	ContentRatings struct {
		Results []struct {
			Country string `json:"iso_3166_1"`
			Rating  string `json:"rating"`
		} `json:"results"`
	} `json:"content_ratings"`
	Images Images `json:"images"`
}

// Certification is the series' content rating in country (ISO 3166-1), or "".
func (t *TV) Certification(country string) string {
	for _, r := range t.ContentRatings.Results {
		if r.Country == country && r.Rating != "" {
			return r.Rating
		}
	}
	return ""
}

// Certification is the movie's certification in country: the theatrical release's first, else any release's.
func (m *Movie) Certification(country string) string {
	var other string
	for _, c := range m.ReleaseDates.Results {
		if c.Country != country {
			continue
		}
		for _, r := range c.Releases {
			if r.Certification == "" {
				continue
			}
			if r.Type == 3 {
				return r.Certification
			}
			if other == "" {
				other = r.Certification
			}
		}
	}
	return other
}

// MovieAppendDetails is the append_to_response of MovieDetails.
const MovieAppendDetails = "external_ids,release_dates,alternative_titles,translations,images"

// TVAppendDetails is the append_to_response of TVDetails.
const TVAppendDetails = "external_ids,content_ratings,images"

// MovieDetails fetches a movie's details with external ids, release dates, names and images, bypassing the cache.
func (c *Client) MovieDetails(ctx context.Context, id int) (*Movie, error) {
	if id <= 0 {
		return nil, fmt.Errorf("tmdb: bad movie id %d", id)
	}
	q := url.Values{}
	q.Set("append_to_response", MovieAppendDetails)
	q.Set("include_image_language", ImageLanguages)
	var m Movie
	if err := c.fresh(ctx, "/movie/"+strconv.Itoa(id), q, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// TVDetails fetches a series' details with external ids, content ratings and images, bypassing the cache.
func (c *Client) TVDetails(ctx context.Context, id int) (*TV, error) {
	if id <= 0 {
		return nil, fmt.Errorf("tmdb: bad TV id %d", id)
	}
	q := url.Values{}
	q.Set("append_to_response", TVAppendDetails)
	q.Set("include_image_language", ImageLanguages)
	var t TV
	if err := c.fresh(ctx, "/tv/"+strconv.Itoa(id), q, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// FindTVByTVDB returns the TMDb TV id for a TVDB series id. ErrNotFound when TMDb has none.
func (c *Client) FindTVByTVDB(ctx context.Context, tvdbID int) (int, error) {
	if tvdbID <= 0 {
		return 0, fmt.Errorf("tmdb: bad TVDB id %d", tvdbID)
	}
	q := url.Values{}
	q.Set("external_source", "tvdb_id")
	var r struct {
		TVResults []struct {
			ID int `json:"id"`
		} `json:"tv_results"`
	}
	if err := c.get(ctx, "/find/"+strconv.Itoa(tvdbID), q, &r, findTTL); err != nil {
		return 0, err
	}
	if len(r.TVResults) == 0 {
		return 0, ErrNotFound
	}
	return r.TVResults[0].ID, nil
}

func findTTL(int, []byte) time.Duration { return 7 * 24 * time.Hour }

// fresh sends GET base+path?query&api_key without the cache and decodes the answer.
func (c *Client) fresh(ctx context.Context, p string, q url.Values, v any) error {
	status, body, err := c.fetch(ctx, p, q)
	if err != nil {
		return err
	}
	key := p
	if enc := q.Encode(); enc != "" {
		key += "?" + enc
	}
	return decode(key, status, body, v)
}
