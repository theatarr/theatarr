// Package arr is a read-only client for the Radarr and Sonarr v3 API and for Prowlarr's v1 indexer list.
//
// The client can only send GET requests, and only to a fixed allowlist of endpoints. This holds by construction:
//
//   - the exported API has no way to name an HTTP method; every request is built in one place with http.MethodGet;
//   - the transport is wrapped in a guard that refuses any request whose method is not GET or whose path is not on
//     the allowlist, before it reaches the network;
//   - redirects are refused, so a server cannot steer the client to another endpoint.
//
// The API key is sent in the X-Api-Key header, never in the URL, and is masked in every error the client returns. It
// goes to LAN addresses only (loopback, RFC 1918, 100.64.0.0/10, ULA): see lan.go.
package arr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Endpoints the client may call, relative to /api/v3/. Nothing here triggers a search, a command or a write: the
// client sends GETs only, and a GET on each of these reads. They are what the one-time import (Admin › Import from
// Sonarr/Radarr) and Radarr/Sonarr lists read.
var allowedEndpoints = map[string]bool{
	"system/status":  true,
	"qualityprofile": true,
	"rootfolder":     true,
	"movie":          true,
	"moviefile":      true,
	"series":         true,
	"episode":        true,
	"episodefile":    true,
}

// prowlarrEndpoints are what a Prowlarr client (Options.Prowlarr) may call, relative to /api/v1/: its indexer list,
// for Link Prowlarr. Nothing else.
var prowlarrEndpoints = map[string]bool{
	"indexer": true,
}

// Allowed reports whether endpoint (relative to /api/v3/) is on the read-only allowlist.
func Allowed(endpoint string) bool { return allowedEndpoints[endpoint] }

// ErrNotAllowed is returned for a request the client refuses to send.
var ErrNotAllowed = errors.New("arr: request not allowed")

// Options tune a Client. Zero values take the defaults.
type Options struct {
	// Timeout per attempt. Default 60s (history pages compute custom formats per record and can be slow).
	Timeout time.Duration
	// MinInterval between the starts of two requests to this instance. Default 400ms (2.5 requests/s).
	MinInterval time.Duration
	// Retries after the first attempt on 5xx, 429, timeouts and network errors. Default 4.
	Retries int
	// Backoff before the first retry; it doubles each time, capped at 30s. Default 2s.
	Backoff time.Duration
	// Transport replaces the LAN-only transport (tests). An IP-literal base URL is still checked in New.
	Transport http.RoundTripper
	// Prowlarr makes a Prowlarr client: requests go under /api/v1/ and only to prowlarrEndpoints.
	Prowlarr bool
}

// Client talks to one arr instance.
type Client struct {
	base    *url.URL
	api     string
	allowed map[string]bool
	key     string
	hc      *http.Client
	opt     Options
	limiter *limiter
}

// New returns a client for the instance at baseURL (scheme, host and optional URL base, without /api/v3).
func New(baseURL, key string, opt Options) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("arr: bad base URL %q", baseURL)
	}
	if u.RawQuery != "" || u.User != nil {
		return nil, fmt.Errorf("arr: base URL must not carry a query or credentials")
	}
	if key == "" {
		return nil, errors.New("arr: empty API key")
	}
	// An IP literal is checked now; a name is checked by the dialer on every connection (lan.go).
	if err := CheckLiteral(u.Hostname()); err != nil {
		return nil, fmt.Errorf("arr: base URL %w", err)
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 60 * time.Second
	}
	if opt.MinInterval <= 0 {
		opt.MinInterval = 400 * time.Millisecond
	}
	if opt.Retries < 0 {
		opt.Retries = 0
	} else if opt.Retries == 0 {
		opt.Retries = 4
	}
	if opt.Backoff <= 0 {
		opt.Backoff = 2 * time.Second
	}
	var rt http.RoundTripper = opt.Transport
	if opt.Transport == nil {
		rt = lanTransport()
	}
	c := &Client{base: u, api: "/api/v3/", allowed: allowedEndpoints, key: key, opt: opt, limiter: &limiter{interval: opt.MinInterval}}
	if opt.Prowlarr {
		c.api, c.allowed = "/api/v1/", prowlarrEndpoints
	}
	c.hc = &http.Client{
		Transport: &getOnly{next: rt, prefix: u.Path + c.api, allowed: c.allowed},
		Timeout:   opt.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirect refused")
		},
	}
	return c, nil
}

// Response is a raw API answer.
type Response struct {
	Status int
	Body   []byte
}

// StatusError is returned by GetJSON for a non-2xx answer.
type StatusError struct {
	Endpoint string
	Status   int
	Body     string // first bytes of the body, key masked
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("arr: GET %s: HTTP %d: %s", e.Endpoint, e.Status, e.Body)
}

// Get sends GET /api/v3/<endpoint>?<query> and returns the status and body. Non-2xx answers that are not retried
// (4xx other than 429) come back as a Response without error, so callers can record them. 5xx, 429 and network
// errors are retried with back-off; when retries run out the last failure is returned as an error.
func (c *Client) Get(ctx context.Context, endpoint string, query url.Values) (*Response, error) {
	if !c.allowed[endpoint] {
		return nil, fmt.Errorf("%w: endpoint %q", ErrNotAllowed, endpoint)
	}
	u := *c.base
	u.Path = c.base.Path + c.api + endpoint
	u.RawQuery = query.Encode()

	var lastErr error
	backoff := c.opt.Backoff
	for attempt := 0; attempt <= c.opt.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, c.mask(ctx.Err())
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
		if err := c.limiter.wait(ctx); err != nil {
			return nil, c.mask(err)
		}
		resp, err := c.do(ctx, u.String())
		if err != nil {
			if ctx.Err() != nil {
				return nil, c.mask(ctx.Err())
			}
			if errors.Is(err, ErrNotAllowed) || !retryable(err) {
				return nil, c.mask(err)
			}
			lastErr = err
			continue
		}
		if resp.Status >= 500 || resp.Status == http.StatusTooManyRequests {
			lastErr = &StatusError{Endpoint: endpoint, Status: resp.Status, Body: snippet(resp.Body)}
			continue
		}
		return resp, nil
	}
	return nil, c.mask(fmt.Errorf("arr: GET %s: giving up after %d attempts: %w", endpoint, c.opt.Retries+1, lastErr))
}

// GetJSON is Get plus a 2xx check and json.Unmarshal into v.
func (c *Client) GetJSON(ctx context.Context, endpoint string, query url.Values, v any) error {
	resp, err := c.Get(ctx, endpoint, query)
	if err != nil {
		return err
	}
	if resp.Status < 200 || resp.Status > 299 {
		return c.mask(&StatusError{Endpoint: endpoint, Status: resp.Status, Body: snippet(resp.Body)})
	}
	if err := json.Unmarshal(resp.Body, v); err != nil {
		return c.mask(fmt.Errorf("arr: GET %s: decode: %w", endpoint, err))
	}
	return nil
}

func (c *Client) do(ctx context.Context, rawURL string) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", c.key)
	req.Header.Set("Accept", "application/json")
	res, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 256<<20))
	if err != nil {
		return nil, err
	}
	return &Response{Status: res.StatusCode, Body: body}, nil
}

// mask replaces the API key (raw and URL-escaped) in an error's text.
func (c *Client) mask(err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	m := Mask(s, c.key)
	if m == s {
		return err
	}
	return &maskedError{msg: m, err: err}
}

// Mask replaces every occurrence of key (raw, query-escaped and path-escaped) in s with "***".
func Mask(s, key string) string {
	if key == "" {
		return s
	}
	for _, k := range []string{key, url.QueryEscape(key), url.PathEscape(key)} {
		s = strings.ReplaceAll(s, k, "***")
	}
	return s
}

type maskedError struct {
	msg string
	err error
}

func (e *maskedError) Error() string { return e.msg }

// Is lets errors.Is see sentinel errors and context errors through the mask, without exposing the unmasked text.
func (e *maskedError) Is(target error) bool { return errors.Is(e.err, target) }

func retryable(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return !errors.Is(err, ErrNotAllowed)
	}
	return errors.Is(err, io.ErrUnexpectedEOF)
}

func snippet(b []byte) string {
	const n = 200
	s := string(b)
	if len(s) > n {
		s = s[:n] + "..."
	}
	return strings.TrimSpace(s)
}

// getOnly is the transport guard: anything but a GET to an allowlisted endpoint under the instance's /api/v3/ is
// refused before it is sent.
type getOnly struct {
	next    http.RoundTripper
	prefix  string
	allowed map[string]bool
}

func (g *getOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return nil, fmt.Errorf("%w: method %s", ErrNotAllowed, req.Method)
	}
	if req.Body != nil && req.Body != http.NoBody {
		return nil, fmt.Errorf("%w: request body", ErrNotAllowed)
	}
	ep, ok := strings.CutPrefix(req.URL.Path, g.prefix)
	if !ok || !g.allowed[ep] {
		return nil, fmt.Errorf("%w: path %q", ErrNotAllowed, req.URL.Path)
	}
	return g.next.RoundTrip(req)
}

// limiter spaces request starts at least interval apart.
type limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func (l *limiter) wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	at := l.next
	if at.Before(now) {
		at = now
	}
	l.next = at.Add(l.interval)
	l.mu.Unlock()
	d := time.Until(at)
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
