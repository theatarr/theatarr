package tmdb

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Discover is a TMDb discover rule (an import list's rule, docs/design/arrview.md, Lists): the titles that match it,
// in TMDb's popularity order, the first Limit of them. A zero field does not narrow the rule.
type Discover struct {
	// YearFrom and YearTo bound the release year (a movie's primary release, a series' first air date).
	YearFrom int `json:"year_from,omitempty"`
	YearTo   int `json:"year_to,omitempty"`
	// MinRating is the lowest TMDb vote average (0 to 10); MinVotes the fewest votes.
	MinRating float64 `json:"min_rating,omitempty"`
	MinVotes  int     `json:"min_votes"`
	// Genres are TMDb genre ids: a title with any of them matches.
	Genres []int `json:"genres,omitempty"`
	// Language is the original language (ISO 639-1, "ja").
	Language string `json:"language,omitempty"`
	// Limit is how many results are taken, at most MaxDiscover.
	Limit int `json:"limit"`
}

// DefaultDiscover and MaxDiscover bound a discover rule's results; DefaultDiscoverVotes is its default minimum votes.
const (
	DefaultDiscover      = 100
	MaxDiscover          = 500
	DefaultDiscoverVotes = 200
)

// discoverPage is TMDb's page size for discover.
const discoverPage = 20

// DiscoverMovies reads the movies of rule d, fresh (not cached), page by page up to its limit. Each result's
// MediaType is movie.
func (c *Client) DiscoverMovies(ctx context.Context, d Discover) ([]MovieSummary, error) {
	return c.discover(ctx, "movie", d)
}

// DiscoverTV reads the series of rule d as DiscoverMovies does. Each result's MediaType is tv, with its Name and
// FirstAirDate.
func (c *Client) DiscoverTV(ctx context.Context, d Discover) ([]MovieSummary, error) {
	return c.discover(ctx, "tv", d)
}

func (c *Client) discover(ctx context.Context, media string, d Discover) ([]MovieSummary, error) {
	limit := d.Limit
	if limit <= 0 {
		limit = DefaultDiscover
	}
	if limit > MaxDiscover {
		return nil, fmt.Errorf("tmdb: discover limit %d is over %d", limit, MaxDiscover)
	}
	q := url.Values{}
	q.Set("sort_by", "popularity.desc")
	q.Set("include_adult", "false")
	date := "primary_release_date"
	if media == "tv" {
		date = "first_air_date"
	}
	if d.YearFrom > 0 {
		q.Set(date+".gte", fmt.Sprintf("%04d-01-01", d.YearFrom))
	}
	if d.YearTo > 0 {
		q.Set(date+".lte", fmt.Sprintf("%04d-12-31", d.YearTo))
	}
	if d.MinRating > 0 {
		q.Set("vote_average.gte", strconv.FormatFloat(d.MinRating, 'f', -1, 64))
	}
	if d.MinVotes > 0 {
		q.Set("vote_count.gte", strconv.Itoa(d.MinVotes))
	}
	if len(d.Genres) > 0 {
		ids := make([]string, len(d.Genres))
		for i, g := range d.Genres {
			ids[i] = strconv.Itoa(g)
		}
		q.Set("with_genres", strings.Join(ids, "|"))
	}
	if d.Language != "" {
		q.Set("with_original_language", d.Language)
	}
	var out []MovieSummary
	for page := 1; len(out) < limit; page++ {
		var r struct {
			Results    []MovieSummary `json:"results"`
			TotalPages int            `json:"total_pages"`
		}
		q.Set("page", strconv.Itoa(page))
		if err := c.fresh(ctx, "/discover/"+media, q, &r); err != nil {
			return nil, err
		}
		for _, m := range r.Results {
			if len(out) == limit {
				break
			}
			m.MediaType = media
			out = append(out, m)
		}
		if len(r.Results) < discoverPage || page >= r.TotalPages {
			break
		}
	}
	return out, nil
}
