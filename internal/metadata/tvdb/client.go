package tvdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/metadata/anime/live"
	"github.com/theatarr/theatarr/internal/store"
)

// Source is the metadata.sources key in the policy, the gateway target and the metadata_cache source.
const Source = "tvdb"

// TTLs (docs/design/tvdb.md): an airing or announced series a day, an ended one a month. Searches and the season types a week
// and a month.
const (
	TTLAiring     = 24 * time.Hour
	TTLEnded      = 30 * 24 * time.Hour
	TTLSearch     = 7 * 24 * time.Hour
	TTLStatic     = 30 * 24 * time.Hour
	TokenLifetime = 25 * 24 * time.Hour // TVDB's tokens last a month; renewed early, and on any 401
	maxPages      = 100
)

var (
	// ErrNotFound is a 404 from TVDB (cached for a day).
	ErrNotFound = live.ErrNotFound
	// ErrSourceDown is returned when the breaker is open and nothing is cached.
	ErrSourceDown = live.ErrSourceDown
	// ErrAuth is a refused login: a wrong API key, or a user-supported key without a valid PIN.
	ErrAuth = errors.New("tvdb: login refused (check the API key, and the subscriber PIN for a user-supported key)")
)

// Options configure a Client.
type Options struct {
	// BaseURL is the API root, https://api4.thetvdb.com/v4 (from the policy; there is no default).
	BaseURL string
	// APIKey is the project API key; PIN is the subscriber PIN, needed only for user-supported keys.
	APIKey, PIN string
	// Doer sends requests: the gateway in the service, so the allowlist, budget and back-offs apply.
	Doer live.Doer
	// Store holds the response cache (metadata_cache) and the /updates watermark (tvdb_sync). Nil: no cache.
	Store *store.Store
	// Language is the translation (ISO 639-2) for series and episode names, default "eng"; "-" keeps TVDB's own.
	Language string
	// MinInterval spaces request starts; default 200ms. The gateway's budget applies on top.
	MinInterval time.Duration
	// NoSpecialsAirs skips fetching each special's extended record when the listing lacks its airs fields.
	NoSpecialsAirs bool
	// Now and Sleep are the clock (tests).
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
}

// Client is a TVDB v4 client: cached, paced, with a circuit breaker, over the gateway.
type Client struct {
	base string
	lang string
	opt  Options
	auth *authDoer
	// F is the fetcher (counters: Requests, Hits, Stale).
	F *live.Fetcher
}

// MetadataDoer is the shared gateway, without the TVDB bearer-token wrapper.
func (c *Client) MetadataDoer() live.Doer { return c.opt.Doer }

// New returns a client. It makes no request until one is needed.
func New(opt Options) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(opt.BaseURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("tvdb: bad base URL %q", opt.BaseURL)
	}
	if opt.APIKey == "" {
		return nil, errors.New("tvdb: empty API key")
	}
	if opt.Doer == nil {
		return nil, errors.New("tvdb: no Doer (requests go through the gateway)")
	}
	if opt.MinInterval <= 0 {
		opt.MinInterval = 200 * time.Millisecond
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	lang := opt.Language
	if lang == "" {
		lang = "eng"
	}
	if lang == "-" {
		lang = ""
	}
	base := u.String()
	a := &authDoer{inner: opt.Doer, loginURL: base + "/login", key: opt.APIKey, pin: opt.PIN, now: opt.Now}
	f := &live.Fetcher{Doer: a, Store: opt.Store, Now: opt.Now, Sleep: opt.Sleep,
		Policies: map[string]live.Policy{Source: {TTL: TTLAiring, MinInterval: opt.MinInterval, MaxBytes: 64 << 20}}}
	return &Client{base: base, lang: lang, opt: opt, auth: a, F: f}, nil
}

// authDoer adds the bearer token to every request, logging in when there is none or it has expired, and once more
// on a 401 (the token was revoked or expired early). The key, PIN and token never reach a log, error or cache key.
type authDoer struct {
	inner    live.Doer
	loginURL string
	key, pin string
	now      func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
	// Logins counts logins (tests).
	Logins int
}

func (a *authDoer) bearer(ctx context.Context, t gateway.Target, renew bool) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !renew && a.token != "" && a.now().Before(a.expires) {
		return a.token, nil
	}
	a.token = ""
	body, _ := json.Marshal(struct {
		APIKey string `json:"apikey"`
		PIN    string `json:"pin,omitempty"`
	}{a.key, a.pin})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.loginURL, bytes.NewReader(body))
	if err != nil {
		return "", errors.New("tvdb: bad login URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	a.Logins++
	resp, err := a.inner.Do(ctx, gateway.Target{Class: t.Class, Key: t.Key, MaxBytes: 1 << 20}, req)
	if err != nil {
		return "", fmt.Errorf("tvdb: login: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", ErrAuth
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("tvdb: login: HTTP %d", resp.StatusCode)
	}
	var env struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &env) != nil || env.Data.Token == "" {
		return "", errors.New("tvdb: login: no token in the answer")
	}
	a.token, a.expires = env.Data.Token, a.now().Add(TokenLifetime)
	return a.token, nil
}

// Do implements live.Doer. Requests are GETs without a body, so a retry can resend them.
func (a *authDoer) Do(ctx context.Context, t gateway.Target, req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		tok, err := a.bearer(ctx, t, attempt > 0)
		if err != nil {
			return nil, err
		}
		r := req.Clone(ctx)
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("Accept", "application/json")
		resp, err := a.inner.Do(ctx, t, r)
		if err != nil || resp.StatusCode != http.StatusUnauthorized || attempt > 0 || req.Body != nil {
			return resp, err
		}
		resp.Body.Close()
	}
}

// Logins reports how often the client logged in.
func (c *Client) Logins() int {
	c.auth.mu.Lock()
	defer c.auth.mu.Unlock()
	return c.auth.Logins
}

func (c *Client) ttlFor(status string) time.Duration {
	if airing(status) {
		return TTLAiring
	}
	return TTLEnded
}

// get fetches path?q through the cache and decodes the envelope's data into out; it returns the paging links.
func (c *Client) get(ctx context.Context, path string, q url.Values, ttl func([]byte) time.Duration, out any) (*Links, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	b, err := c.F.Get(ctx, live.Request{Source: Source, URL: u, TTL: ttl, Check: checkEnvelope})
	if err != nil {
		return nil, fmt.Errorf("tvdb %s: %w", path, err)
	}
	var env envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, fmt.Errorf("tvdb %s: %w", path, err)
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return nil, fmt.Errorf("tvdb %s: %w", path, err)
		}
	}
	return env.Links, nil
}

// checkEnvelope rejects a 200 that is not a v4 envelope or reports a failure (not cached).
func checkEnvelope(b []byte) error {
	var env envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return fmt.Errorf("tvdb: not JSON: %w", err)
	}
	if env.Status != "" && env.Status != "success" {
		return fmt.Errorf("tvdb: status %q", env.Status)
	}
	return nil
}

func fixed(d time.Duration) func([]byte) time.Duration {
	return func([]byte) time.Duration { return d }
}

// Series returns a series' extended record with translations (short: no characters, artworks or trailers).
func (c *Client) Series(ctx context.Context, id int) (*Series, error) {
	var s Series
	ttl := func(b []byte) time.Duration {
		var env struct {
			Data struct {
				Status Status `json:"status"`
			} `json:"data"`
		}
		if json.Unmarshal(b, &env) != nil {
			return 0
		}
		return c.ttlFor(env.Data.Status.Name)
	}
	q := url.Values{"meta": {"translations"}, "short": {"true"}}
	if _, err := c.get(ctx, "/series/"+strconv.Itoa(id)+"/extended", q, ttl, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

var seasonTypeRE = regexp.MustCompile(`^[a-z]+$`)

// Episodes lists a series' episodes in an order (default, official, dvd, absolute, alternate, regional, ...), every
// page, with names and overviews in lang (ISO 639-2) when it is set. The series block tells its status.
func (c *Client) Episodes(ctx context.Context, id int, seasonType, lang string) (*SeriesBase, []Episode, error) {
	if seasonType == "" {
		seasonType = "default"
	}
	if !seasonTypeRE.MatchString(seasonType) || lang != "" && !seasonTypeRE.MatchString(lang) {
		return nil, nil, fmt.Errorf("tvdb: bad season type %q or language %q", seasonType, lang)
	}
	path := "/series/" + strconv.Itoa(id) + "/episodes/" + seasonType
	if lang != "" {
		path += "/" + lang
	}
	ttl := func(b []byte) time.Duration {
		var env struct {
			Data struct {
				Series struct {
					Status Status `json:"status"`
				} `json:"series"`
			} `json:"data"`
		}
		if json.Unmarshal(b, &env) != nil {
			return 0
		}
		return c.ttlFor(env.Data.Series.Status.Name)
	}
	var series *SeriesBase
	var out []Episode
	for page := 0; page < maxPages; page++ {
		var p episodesPage
		links, err := c.get(ctx, path, url.Values{"page": {strconv.Itoa(page)}}, ttl, &p)
		if err != nil {
			return nil, nil, err
		}
		if series == nil {
			s := p.Series
			series = &s
		}
		out = append(out, p.Episodes...)
		if links == nil || links.Next == nil || *links.Next == "" || len(p.Episodes) == 0 {
			break
		}
	}
	return series, out, nil
}

// Episode returns an episode's extended record (airsBeforeSeason/airsBeforeEpisode/airsAfterSeason are reliable
// here). ttl is the series' TTL (0: a day).
func (c *Client) Episode(ctx context.Context, id int, ttl time.Duration) (*Episode, error) {
	if ttl <= 0 {
		ttl = TTLAiring
	}
	var e Episode
	if _, err := c.get(ctx, "/episodes/"+strconv.Itoa(id)+"/extended", nil, fixed(ttl), &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// SeasonExtended is a season with its episodes (GET /seasons/{id}/extended).
type SeasonExtended struct {
	Season
	Episodes []Episode `json:"episodes"`
}

// Season returns a season of any order with its episodes.
func (c *Client) Season(ctx context.Context, id int) (*SeasonExtended, error) {
	var s SeasonExtended
	if _, err := c.get(ctx, "/seasons/"+strconv.Itoa(id)+"/extended", nil, fixed(TTLAiring), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// SeasonTypes lists the episode orders TVDB knows.
func (c *Client) SeasonTypes(ctx context.Context) ([]SeasonType, error) {
	var out []SeasonType
	if _, err := c.get(ctx, "/seasons/types", nil, fixed(TTLStatic), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Search finds records by name; typ narrows it (series, movie, person, company; "" any).
func (c *Client) Search(ctx context.Context, query, typ string) ([]SearchResult, error) {
	q := url.Values{"query": {query}}
	if typ != "" {
		q.Set("type", typ)
	}
	var out []SearchResult
	if _, err := c.get(ctx, "/search", q, fixed(TTLSearch), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SearchRemoteID finds the record an outside id belongs to: an IMDb id (tt…) or a TMDb id.
func (c *Client) SearchRemoteID(ctx context.Context, remoteID string) ([]RemoteIDResult, error) {
	if remoteID == "" || strings.ContainsAny(remoteID, "/?#") {
		return nil, fmt.Errorf("tvdb: bad remote id %q", remoteID)
	}
	var out []RemoteIDResult
	if _, err := c.get(ctx, "/search/remoteid/"+url.PathEscape(remoteID), nil, fixed(TTLSearch), &out); err != nil {
		return nil, err
	}
	return out, nil
}
