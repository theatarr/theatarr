// Package tmdb is a small TMDb v3 client for movies: details with external ids, release dates, alternative titles
// and translations by TMDb id, and find by IMDb id. MovieDetails and TVDetails add the art and display
// fields for the meta.art job (details.go).
//
// HTTP goes through an injected [Doer]: in the service, the gateway (host allowlist, budget, back-off, metrics).
// The v3 api_key travels as a query parameter, as TMDb's v3 auth wants; it is never part of a cache key and is
// masked in every error. Responses are cached through [Cache] (the database in production, see [SQLCache]) with a
// time to live per answer, always under TMDb's six-month limit. Requests are paced (default 10 per second; TMDb
// allows about 40).
package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Doer sends an HTTP request. *http.Client satisfies it.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Cache stores raw responses by key. Get reports ok=false on a miss or an expired entry.
type Cache interface {
	Get(ctx context.Context, key string) (status int, body []byte, ok bool, err error)
	Put(ctx context.Context, key string, status int, body []byte, ttl time.Duration) error
}

// ErrNotFound is returned when TMDb has no such movie (HTTP 404, or an empty find).
var ErrNotFound = errors.New("tmdb: not found")

// MaxTTL keeps every cached response under TMDb's six-month caching limit.
const MaxTTL = 150 * 24 * time.Hour

// Options configure a Client.
type Options struct {
	// BaseURL is the API root, e.g. https://api.themoviedb.org/3 (from the policy; there is no default).
	BaseURL string
	// APIKey is the v3 api_key.
	APIKey string
	// Doer sends requests. Required: in the service it is the gateway (internal/gateway) with the metadata target
	// for tmdb, so the host allowlist, budget and metrics apply.
	Doer Doer
	// Cache, if set, stores responses.
	Cache Cache
	// MinInterval spaces request starts. Default 100ms (10 requests/s).
	MinInterval time.Duration
	// Now is the clock for TTL decisions. Default time.Now.
	Now func() time.Time
	// Backoff is the first wait before a retry; it grows linearly. Default 1s.
	Backoff time.Duration
}

// Client is a TMDb v3 client.
type Client struct {
	base *url.URL
	key  string
	opt  Options

	mu   sync.Mutex
	next time.Time

	// Requests counts requests sent to TMDb (cache misses), for summaries.
	Requests int
	// Hits counts answers served from the cache.
	Hits int
}

// New returns a client.
func New(opt Options) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(opt.BaseURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("tmdb: bad base URL %q", opt.BaseURL)
	}
	if opt.APIKey == "" {
		return nil, errors.New("tmdb: empty API key")
	}
	if opt.Doer == nil {
		return nil, errors.New("tmdb: no Doer (requests go through the gateway)")
	}
	if opt.MinInterval <= 0 {
		opt.MinInterval = 100 * time.Millisecond
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Backoff <= 0 {
		opt.Backoff = time.Second
	}
	return &Client{base: u, key: opt.APIKey, opt: opt}, nil
}

// Movie is a movie's details with external ids and release dates (one request, append_to_response).
type Movie struct {
	ID               int     `json:"id"`
	IMDbID           string  `json:"imdb_id"`
	Title            string  `json:"title"`
	OriginalTitle    string  `json:"original_title"`
	OriginalLanguage string  `json:"original_language"`
	ReleaseDate      string  `json:"release_date"`
	Runtime          int     `json:"runtime"`
	Status           string  `json:"status"`
	Adult            bool    `json:"adult"`
	Popularity       float64 `json:"popularity"`
	Genres           []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"genres"`
	BelongsToCollection *struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"belongs_to_collection"`
	ExternalIDs       ExternalIDs       `json:"external_ids"`
	ReleaseDates      ReleaseDates      `json:"release_dates"`
	AlternativeTitles AlternativeTitles `json:"alternative_titles"`
	Translations      Translations      `json:"translations"`

	// Art and display fields (docs/design/ui.md), read by the meta.art job through MovieDetails.
	Overview            string    `json:"overview"`
	Tagline             string    `json:"tagline"`
	PosterPath          string    `json:"poster_path"`
	BackdropPath        string    `json:"backdrop_path"`
	VoteAverage         float64   `json:"vote_average"`
	VoteCount           int       `json:"vote_count"`
	ProductionCompanies []Company `json:"production_companies"`
	// Images is filled only by MovieDetails (append_to_response=images).
	Images Images `json:"images"`
}

// MovieSummary is a movie returned as a collection part or list item.
type MovieSummary struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	ReleaseDate string `json:"release_date"`
	Overview    string `json:"overview"`
	MediaType   string `json:"media_type,omitempty"`
	// PosterPath is TMDb's poster file ("/abc.jpg"); the Collections page shows the first part's.
	PosterPath string `json:"poster_path,omitempty"`
}

// Collection is the movie membership returned by TMDb's collection endpoint.
type Collection struct {
	ID    int            `json:"id"`
	Name  string         `json:"name"`
	Parts []MovieSummary `json:"parts"`
}

// List is a TMDb user's or public list. TMDb returns the list's movies in items.
type List struct {
	ID          int            `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Items       []MovieSummary `json:"items"`
}

// AlternativeTitles are a movie's other titles (GET /movie/{id}/alternative_titles).
type AlternativeTitles struct {
	Titles []AlternativeTitle `json:"titles"`
}

// AlternativeTitle is one other title, with its country (ISO 3166-1) and TMDb's free-text type.
type AlternativeTitle struct {
	Country string `json:"iso_3166_1"`
	Title   string `json:"title"`
	Type    string `json:"type"`
}

// Translations are a movie's translated details (GET /movie/{id}/translations).
type Translations struct {
	Translations []Translation `json:"translations"`
}

// Translation is one language's translated details. Data.Title is empty when the translation keeps the title.
type Translation struct {
	Country     string `json:"iso_3166_1"`
	Language    string `json:"iso_639_1"`
	Name        string `json:"name"`
	EnglishName string `json:"english_name"`
	Data        struct {
		Title string `json:"title"`
	} `json:"data"`
}

// Year is the release year, 0 when unknown.
func (m *Movie) Year() int {
	if len(m.ReleaseDate) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(m.ReleaseDate[:4])
	return y
}

// ExternalIDs are a movie's ids elsewhere.
type ExternalIDs struct {
	IMDbID      string `json:"imdb_id"`
	WikidataID  string `json:"wikidata_id"`
	FacebookID  string `json:"facebook_id"`
	InstagramID string `json:"instagram_id"`
	TwitterID   string `json:"twitter_id"`
}

// ReleaseDates are a movie's release dates per country.
type ReleaseDates struct {
	Results []CountryReleases `json:"results"`
}

// CountryReleases are the releases in one country (ISO 3166-1).
type CountryReleases struct {
	Country  string    `json:"iso_3166_1"`
	Releases []Release `json:"release_dates"`
}

// Release is one dated release. Type: 1 premiere, 2 theatrical (limited), 3 theatrical, 4 digital, 5 physical, 6 TV.
type Release struct {
	Certification string    `json:"certification"`
	Language      string    `json:"iso_639_1"`
	Note          string    `json:"note"`
	Date          time.Time `json:"release_date"`
	Type          int       `json:"type"`
}

// TypeName is our name for a TMDb release type.
func TypeName(t int) string {
	switch t {
	case 1:
		return "premiere"
	case 2:
		return "theatrical_limited"
	case 3:
		return "theatrical"
	case 4:
		return "digital"
	case 5:
		return "physical"
	case 6:
		return "tv"
	}
	return ""
}

// MovieAppend is the append_to_response of every movie request, so one cached answer serves all the movie
// lookups.
const MovieAppend = "external_ids,release_dates,alternative_titles,translations"

// Movie fetches a movie by TMDb id, with external ids, release dates, alternative titles and translations.
func (c *Client) Movie(ctx context.Context, id int) (*Movie, error) {
	q := url.Values{}
	q.Set("append_to_response", MovieAppend)
	var m Movie
	if err := c.get(ctx, "/movie/"+strconv.Itoa(id), q, &m, c.movieTTL); err != nil {
		return nil, err
	}
	return &m, nil
}

// Collection fetches a TMDb collection and its movie parts.
func (c *Client) Collection(ctx context.Context, id int) (*Collection, error) {
	if id <= 0 {
		return nil, fmt.Errorf("tmdb: bad collection id %d", id)
	}
	var out Collection
	if err := c.fresh(ctx, "/collection/"+strconv.Itoa(id), url.Values{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// List fetches a TMDb list by id.
func (c *Client) List(ctx context.Context, id int) (*List, error) {
	if id <= 0 {
		return nil, fmt.Errorf("tmdb: bad list id %d", id)
	}
	var out List
	if err := c.fresh(ctx, "/list/"+strconv.Itoa(id), url.Values{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ExternalIDs fetches a movie's external ids (served from the same cached request as Movie).
func (c *Client) ExternalIDs(ctx context.Context, id int) (*ExternalIDs, error) {
	m, err := c.Movie(ctx, id)
	if err != nil {
		return nil, err
	}
	return &m.ExternalIDs, nil
}

// ReleaseDates fetches a movie's release dates (served from the same cached request as Movie).
func (c *Client) ReleaseDates(ctx context.Context, id int) ([]CountryReleases, error) {
	m, err := c.Movie(ctx, id)
	if err != nil {
		return nil, err
	}
	return m.ReleaseDates.Results, nil
}

// FindResult is a find-by-external-id answer, movies only.
type FindResult struct {
	MovieResults []struct {
		ID          int    `json:"id"`
		Title       string `json:"title"`
		ReleaseDate string `json:"release_date"`
	} `json:"movie_results"`
}

// FindByIMDb returns the TMDb movie id for an IMDb id (tt...). ErrNotFound when TMDb has none.
func (c *Client) FindByIMDb(ctx context.Context, imdbID string) (int, error) {
	if !strings.HasPrefix(imdbID, "tt") || len(imdbID) < 3 {
		return 0, fmt.Errorf("tmdb: %q is not an IMDb id", imdbID)
	}
	q := url.Values{}
	q.Set("external_source", "imdb_id")
	var r FindResult
	err := c.get(ctx, "/find/"+url.PathEscape(imdbID), q, &r, func(int, []byte) time.Duration { return 7 * 24 * time.Hour })
	if err != nil {
		return 0, err
	}
	if len(r.MovieResults) == 0 {
		return 0, ErrNotFound
	}
	return r.MovieResults[0].ID, nil
}

// movieTTL: a movie released more than half a year ago changes rarely (30 days); anything newer is refreshed daily.
func (c *Client) movieTTL(status int, body []byte) time.Duration {
	if status != http.StatusOK {
		return 24 * time.Hour
	}
	var m struct {
		Status      string `json:"status"`
		ReleaseDate string `json:"release_date"`
	}
	if json.Unmarshal(body, &m) != nil {
		return 24 * time.Hour
	}
	if m.Status == "Released" {
		if d, err := time.Parse("2006-01-02", m.ReleaseDate); err == nil && c.opt.Now().Sub(d) > 182*24*time.Hour {
			return 30 * 24 * time.Hour
		}
	}
	return 24 * time.Hour
}

// get answers from the cache, else sends GET base+path?query&api_key and caches 200 and 404 answers.
func (c *Client) get(ctx context.Context, p string, q url.Values, v any, ttl func(int, []byte) time.Duration) error {
	key := p
	if enc := q.Encode(); enc != "" {
		key += "?" + enc
	}
	if c.opt.Cache != nil {
		status, body, ok, err := c.opt.Cache.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("tmdb: cache: %w", err)
		}
		if ok {
			c.count(&c.Hits)
			return decode(key, status, body, v)
		}
	}
	status, body, err := c.fetch(ctx, p, q)
	if err != nil {
		return err
	}
	if c.opt.Cache != nil && (status == http.StatusOK || status == http.StatusNotFound) {
		t := min(ttl(status, body), MaxTTL)
		if err := c.opt.Cache.Put(ctx, key, status, body, t); err != nil {
			return fmt.Errorf("tmdb: cache: %w", err)
		}
	}
	return decode(key, status, body, v)
}

func decode(key string, status int, body []byte, v any) error {
	switch {
	case status == http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, key)
	case status < 200 || status > 299:
		return fmt.Errorf("tmdb: GET %s: HTTP %d: %s", key, status, snippet(body))
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("tmdb: GET %s: decode: %w", key, err)
	}
	return nil
}

// fetch sends the request, retrying 429 (after Retry-After, at most 10s) and 5xx answers and network errors.
func (c *Client) fetch(ctx context.Context, p string, q url.Values) (int, []byte, error) {
	u := *c.base
	u.Path = c.base.Path + p
	qq := url.Values{}
	for k, vs := range q {
		qq[k] = vs
	}
	qq.Set("api_key", c.key)
	u.RawQuery = qq.Encode()
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if err := c.wait(ctx); err != nil {
			return 0, nil, err
		}
		c.count(&c.Requests)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return 0, nil, c.mask(err)
		}
		req.Header.Set("Accept", "application/json")
		res, err := c.opt.Doer.Do(req)
		if err != nil {
			last = err
			if ctx.Err() != nil {
				return 0, nil, ctx.Err()
			}
			if !sleep(ctx, time.Duration(attempt+1)*c.opt.Backoff) {
				return 0, nil, ctx.Err()
			}
			continue
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
		res.Body.Close()
		if err != nil {
			last = err
			continue
		}
		if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
			last = fmt.Errorf("HTTP %d", res.StatusCode)
			wait := time.Duration(attempt+1) * c.opt.Backoff
			if s, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && s > 0 {
				wait = min(time.Duration(s)*time.Second, 10*time.Second)
			}
			if !sleep(ctx, wait) {
				return 0, nil, ctx.Err()
			}
			continue
		}
		return res.StatusCode, body, nil
	}
	return 0, nil, c.mask(fmt.Errorf("tmdb: GET %s: giving up: %w", p, last))
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	now := time.Now()
	at := c.next
	if at.Before(now) {
		at = now
	}
	c.next = at.Add(c.opt.MinInterval)
	c.mu.Unlock()
	if d := time.Until(at); d > 0 && !sleep(ctx, d) {
		return ctx.Err()
	}
	return nil
}

// mask replaces the key in an error's text (a *url.Error carries the whole URL).
func (c *Client) mask(err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	for _, k := range []string{c.key, url.QueryEscape(c.key)} {
		s = strings.ReplaceAll(s, k, "***")
	}
	if s == err.Error() {
		return err
	}
	return errors.New(s)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// count bumps a counter under the client's lock: the client is shared by concurrent jobs.
func (c *Client) count(n *int) {
	c.mu.Lock()
	*n++
	c.mu.Unlock()
}
