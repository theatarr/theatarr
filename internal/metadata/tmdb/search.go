package tmdb

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SearchResult is one movie of a title search (GET /search/movie).
type SearchResult struct {
	ID               int     `json:"id"`
	Title            string  `json:"title"`
	OriginalTitle    string  `json:"original_title"`
	OriginalLanguage string  `json:"original_language"`
	ReleaseDate      string  `json:"release_date"`
	Popularity       float64 `json:"popularity"`
	Adult            bool    `json:"adult"`
	// Display fields, read by the arr views' movie lookup (internal/arrview).
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	GenreIDs     []int   `json:"genre_ids"`
	VoteAverage  float64 `json:"vote_average"`
	VoteCount    int     `json:"vote_count"`
}

// Year is the release year, 0 when unknown.
func (r *SearchResult) Year() int {
	if len(r.ReleaseDate) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(r.ReleaseDate[:4])
	return y
}

// SearchTTL is how long a title search stays cached: new films appear, so a week.
const SearchTTL = 7 * 24 * time.Hour

// SearchMovie searches movies by title (the first page, TMDb's relevance order), optionally narrowed to a release
// year (0: any). Adult titles are left out. The answer is cached for SearchTTL under the normalised query, so the
// same search is sent at most once a week. An empty query is an error.
func (c *Client) SearchMovie(ctx context.Context, query string, year int) ([]SearchResult, error) {
	query = strings.Join(strings.Fields(query), " ")
	if query == "" {
		return nil, errEmptyQuery
	}
	q := url.Values{}
	q.Set("query", query)
	q.Set("include_adult", "false")
	q.Set("page", "1")
	if year > 0 {
		q.Set("year", strconv.Itoa(year))
	}
	var r struct {
		Results []SearchResult `json:"results"`
	}
	if err := c.get(ctx, "/search/movie", q, &r, func(int, []byte) time.Duration { return SearchTTL }); err != nil {
		return nil, err
	}
	out := r.Results[:0]
	for _, x := range r.Results {
		if !x.Adult {
			out = append(out, x)
		}
	}
	return out, nil
}

var errEmptyQuery = errors.New("tmdb: empty search query")
