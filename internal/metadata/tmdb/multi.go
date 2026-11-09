package tmdb

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MultiResult is one movie or series of a multi search (GET /search/multi). People are left out.
type MultiResult struct {
	MediaType        string   `json:"media_type"` // movie or tv
	ID               int      `json:"id"`
	Title            string   `json:"title"` // movies
	Name             string   `json:"name"`  // series
	OriginalTitle    string   `json:"original_title"`
	OriginalName     string   `json:"original_name"`
	OriginalLanguage string   `json:"original_language"`
	OriginCountry    []string `json:"origin_country"`
	Overview         string   `json:"overview"`
	PosterPath       string   `json:"poster_path"`
	BackdropPath     string   `json:"backdrop_path"`
	ReleaseDate      string   `json:"release_date"`   // movies
	FirstAirDate     string   `json:"first_air_date"` // series
	GenreIDs         []int    `json:"genre_ids"`
	VoteAverage      float64  `json:"vote_average"`
	VoteCount        int      `json:"vote_count"`
	Popularity       float64  `json:"popularity"`
	Adult            bool     `json:"adult"`
}

// DisplayName is the title (movie) or name (series).
func (r *MultiResult) DisplayName() string {
	if r.Title != "" {
		return r.Title
	}
	return r.Name
}

// Year is the release (movie) or first air (series) year, 0 when unknown.
func (r *MultiResult) Year() int {
	d := r.ReleaseDate
	if d == "" {
		d = r.FirstAirDate
	}
	if len(d) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(d[:4])
	return y
}

// GenreAnimation is TMDb's Animation genre id (movies and TV).
const GenreAnimation = 16

// LooksAnime is the guess an add starts from: Japanese-language animation. The add sheet lets an admin change it.
func (r *MultiResult) LooksAnime() bool { return LooksAnime(r.OriginalLanguage, r.GenreIDs) }

// LooksAnime is Theatarr's anime guess from TMDb data: Japanese-language animation (original language ja and the
// Animation genre). The search box and the arr views' adds share it.
func LooksAnime(originalLanguage string, genreIDs []int) bool {
	if originalLanguage != "ja" {
		return false
	}
	for _, g := range genreIDs {
		if g == GenreAnimation {
			return true
		}
	}
	return false
}

// LooksAnime applies LooksAnime to a movie's details.
func (m *Movie) LooksAnime() bool {
	ids := make([]int, 0, len(m.Genres))
	for _, g := range m.Genres {
		ids = append(ids, g.ID)
	}
	return LooksAnime(m.OriginalLanguage, ids)
}

// LooksAnime applies LooksAnime to a series' record.
func (s *TVSeries) LooksAnime() bool {
	ids := make([]int, 0, len(s.Genres))
	for _, g := range s.Genres {
		ids = append(ids, g.ID)
	}
	return LooksAnime(s.OriginalLanguage, ids)
}

// MultiTTL is how long a multi search stays cached (docs/design/ui.md: a day).
const MultiTTL = 24 * time.Hour

// SearchMulti searches movies and series by title (the first page, TMDb's relevance order). Adult titles and
// people are left out. The answer is cached for MultiTTL under the normalised query, so typing the same search again
// (in any case) sends nothing. An empty query is an error.
func (c *Client) SearchMulti(ctx context.Context, query string) ([]MultiResult, error) {
	query = strings.ToLower(strings.Join(strings.Fields(query), " "))
	if query == "" {
		return nil, errEmptyQuery
	}
	q := url.Values{}
	q.Set("query", query)
	q.Set("include_adult", "false")
	q.Set("page", "1")
	var r struct {
		Results []MultiResult `json:"results"`
	}
	if err := c.get(ctx, "/search/multi", q, &r, func(int, []byte) time.Duration { return MultiTTL }); err != nil {
		return nil, err
	}
	out := r.Results[:0]
	for _, x := range r.Results {
		if !x.Adult && (x.MediaType == "movie" || x.MediaType == "tv") {
			out = append(out, x)
		}
	}
	return out, nil
}
