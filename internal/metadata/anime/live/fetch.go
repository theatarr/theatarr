// Package live fetches the anime pipeline's sources over the network, through internal/gateway (host allowlist,
// budgets, back-offs), with every response cached in the database (metadata_cache: gzip body, keyed by request,
// with ETag/Last-Modified for conditional requests and a time to live per source) and a circuit breaker per source.
//
// Pacing (docs/design/anime.md "Sources and caching"): AniList 30 requests/min (one every 2.1 s), Kitsu 1/s, the mapping lists daily,
// the AniDB titles dump at most daily, TheXEM weekly, SeaDex daily per AniList id. A source whose breaker is open,
// or that fails, is served from stale cache when there is any.
package live

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/store"
)

// Doer sends a request for a gateway target. *gateway.Gateway satisfies it.
type Doer interface {
	Do(ctx context.Context, t gateway.Target, req *http.Request) (*http.Response, error)
}

// Source keys: the metadata.sources keys in the policy (and the gateway targets).
const (
	SourceAniList   = "anilist"
	SourceKitsu     = "kitsu"
	SourceScudLee   = "anime-lists"
	SourceFribb     = "fribb"
	SourceAnibridge = "anibridge"
	SourceManami    = "manami"
	SourceXEM       = "thexem"
	SourceAniDB     = "anidb"
	SourceSeaDex    = "seadex"
)

// Policy for one source: how long a response is fresh, the minimum gap between requests, and the largest body.
type Policy struct {
	TTL         time.Duration
	MinInterval time.Duration
	MaxBytes    int64
}

// Defaults per source. The TTLs for AniList depend on the answer (see anilist.go); this is the finished-title TTL.
var Defaults = map[string]Policy{
	SourceAniList:   {TTL: 30 * 24 * time.Hour, MinInterval: 2100 * time.Millisecond},
	SourceKitsu:     {TTL: 7 * 24 * time.Hour, MinInterval: time.Second},
	SourceScudLee:   {TTL: 24 * time.Hour, MaxBytes: 64 << 20},
	SourceFribb:     {TTL: 24 * time.Hour, MaxBytes: 64 << 20},
	SourceAnibridge: {TTL: 24 * time.Hour, MaxBytes: 128 << 20},
	SourceManami:    {TTL: 24 * time.Hour, MaxBytes: 256 << 20},
	SourceXEM:       {TTL: 7 * 24 * time.Hour, MinInterval: time.Second},
	SourceAniDB:     {TTL: 24 * time.Hour, MaxBytes: 64 << 20},
	SourceSeaDex:    {TTL: 24 * time.Hour, MinInterval: 2 * time.Second},
}

// Breaker settings: after BreakerFailures consecutive failures a source is skipped for BreakerOpen, doubling up to
// BreakerMax while it keeps failing.
const (
	BreakerFailures = 3
	BreakerOpen     = 5 * time.Minute
	BreakerMax      = time.Hour
	notFoundTTL     = 24 * time.Hour
)

// ErrSourceDown is returned when a source's breaker is open and nothing is cached.
var ErrSourceDown = errors.New("live: source unavailable (circuit open)")

// ErrNotFound is a 404 (cached for a day).
var ErrNotFound = errors.New("live: not found")

// Fetcher does cached, paced, breaker-guarded requests.
type Fetcher struct {
	Doer  Doer
	Store *store.Store
	// Policies override Defaults per source.
	Policies map[string]Policy
	Now      func() time.Time
	Sleep    func(context.Context, time.Duration) error

	mu       sync.Mutex
	last     map[string]time.Time
	breakers map[string]*breaker

	// Requests and Hits count network requests and fresh cache answers.
	Requests, Hits, Stale int
}

type breaker struct {
	fails int
	until time.Time
	open  time.Duration
}

func (f *Fetcher) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Fetcher) policy(source string) Policy {
	if p, ok := f.Policies[source]; ok {
		return p
	}
	return Defaults[source]
}

func (f *Fetcher) sleep(ctx context.Context, d time.Duration) error {
	if f.Sleep != nil {
		return f.Sleep(ctx, d)
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

// Request is one fetch.
type Request struct {
	Source string
	Method string // default GET
	URL    string
	Body   []byte
	Header http.Header
	// TTL overrides the source's TTL for this answer, given the body (nil keeps the default).
	TTL func(body []byte) time.Duration
	// Check rejects a 200 answer that is an error in disguise (AniList's GraphQL errors); it is not cached.
	Check func(body []byte) error
}

// Key is the cache key: method and URL, plus a hash of a POST body.
func (r Request) Key() string {
	m := r.Method
	if m == "" {
		m = http.MethodGet
	}
	k := m + " " + r.URL
	if len(r.Body) > 0 {
		h := sha1.Sum(r.Body)
		k += " " + hex.EncodeToString(h[:])
	}
	return k
}

type cached struct {
	status       int
	body         []byte
	expires      time.Time
	etag, lastMd string
}

func (f *Fetcher) load(ctx context.Context, source, key string) (*cached, error) {
	if f.Store == nil {
		return nil, nil
	}
	var c cached
	var body []byte
	var exp int64
	err := f.Store.Reader().QueryRowContext(ctx, `SELECT status, body, expires_at, etag, last_modified FROM metadata_cache
		WHERE source = ? AND key = ?`, source, key).Scan(&c.status, &body, &exp, &c.etag, &c.lastMd)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, nil // a broken entry is a miss
	}
	if c.body, err = io.ReadAll(zr); err != nil {
		return nil, nil
	}
	c.expires = store.FromMillis(exp)
	return &c, nil
}

func (f *Fetcher) save(ctx context.Context, source, key string, c *cached) error {
	if f.Store == nil {
		return nil
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(c.body)
	if err := zw.Close(); err != nil {
		return err
	}
	now := f.now()
	_, err := f.Store.Writer().ExecContext(ctx, `INSERT INTO metadata_cache (source, key, status, body, fetched_at, expires_at,
		etag, last_modified) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (source, key) DO UPDATE SET status = excluded.status,
		body = excluded.body, fetched_at = excluded.fetched_at, expires_at = excluded.expires_at, etag = excluded.etag,
		last_modified = excluded.last_modified`, source, key, c.status, buf.Bytes(), store.Millis(now),
		store.Millis(c.expires), c.etag, c.lastMd)
	return err
}

func (f *Fetcher) touch(ctx context.Context, source, key string, expires time.Time) {
	if f.Store == nil {
		return
	}
	_, _ = f.Store.Writer().ExecContext(ctx, `UPDATE metadata_cache SET expires_at = ?, fetched_at = ? WHERE source = ? AND key = ?`,
		store.Millis(expires), store.Millis(f.now()), source, key)
}

func (f *Fetcher) open(source string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.breakers[source]
	return b != nil && f.now().Before(b.until)
}

func (f *Fetcher) result(source string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.breakers == nil {
		f.breakers = map[string]*breaker{}
	}
	b := f.breakers[source]
	if b == nil {
		b = &breaker{}
		f.breakers[source] = b
	}
	if ok {
		*b = breaker{}
		return
	}
	b.fails++
	if b.fails >= BreakerFailures {
		if b.open == 0 {
			b.open = BreakerOpen
		} else if b.open *= 2; b.open > BreakerMax {
			b.open = BreakerMax
		}
		b.until = f.now().Add(b.open)
	}
}

// BreakerOpen reports whether a source is being skipped.
func (f *Fetcher) BreakerOpen(source string) bool { return f.open(source) }

func (f *Fetcher) pace(ctx context.Context, source string) error {
	gap := f.policy(source).MinInterval
	if gap <= 0 {
		return nil
	}
	f.mu.Lock()
	if f.last == nil {
		f.last = map[string]time.Time{}
	}
	wait := f.last[source].Add(gap).Sub(f.now())
	next := f.now()
	if wait > 0 {
		next = next.Add(wait)
	}
	f.last[source] = next
	f.mu.Unlock()
	if wait > 0 {
		return f.sleep(ctx, wait)
	}
	return nil
}

// Get returns the response body for r: fresh from cache, else from the network (conditionally when a stale copy
// has validators), else stale from cache when the source fails or its breaker is open.
func (f *Fetcher) Get(ctx context.Context, r Request) ([]byte, error) {
	key := r.Key()
	c, err := f.load(ctx, r.Source, key)
	if err != nil {
		return nil, err
	}
	now := f.now()
	if c != nil && now.Before(c.expires) {
		f.Hits++
		if c.status == http.StatusNotFound {
			return nil, ErrNotFound
		}
		return c.body, nil
	}
	stale := func(cause error) ([]byte, error) {
		if c != nil && c.status == http.StatusOK {
			f.Stale++
			return c.body, nil
		}
		return nil, cause
	}
	if f.open(r.Source) {
		return stale(fmt.Errorf("%w: %s", ErrSourceDown, r.Source))
	}
	if f.Doer == nil {
		return stale(fmt.Errorf("live: no network for %s", r.Source))
	}
	if err := f.pace(ctx, r.Source); err != nil {
		return nil, err
	}
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if r.Body != nil {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.URL, body)
	if err != nil {
		return nil, err
	}
	for k, v := range r.Header {
		req.Header[k] = v
	}
	if c != nil && c.status == http.StatusOK {
		if c.etag != "" {
			req.Header.Set("If-None-Match", c.etag)
		}
		if c.lastMd != "" {
			req.Header.Set("If-Modified-Since", c.lastMd)
		}
	}
	pol := f.policy(r.Source)
	f.Requests++
	resp, err := f.Doer.Do(ctx, gateway.Target{Class: gateway.ClassMetadata, Key: r.Source, MaxBytes: pol.MaxBytes}, req)
	if err != nil {
		var ref *gateway.Refused
		if !errors.As(err, &ref) || ref.Reason == gateway.ReasonBackoff {
			f.result(r.Source, false)
		}
		return stale(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		f.result(r.Source, false)
		return stale(err)
	}
	ttl := pol.TTL
	switch {
	case resp.StatusCode == http.StatusNotModified && c != nil:
		f.result(r.Source, true)
		if r.TTL != nil {
			if t := r.TTL(c.body); t > 0 {
				ttl = t
			}
		}
		f.touch(ctx, r.Source, key, now.Add(ttl))
		return c.body, nil
	case resp.StatusCode == http.StatusOK:
		if r.Check != nil {
			if err := r.Check(b); err != nil {
				f.result(r.Source, false)
				return stale(err)
			}
		}
		f.result(r.Source, true)
		if r.TTL != nil {
			if t := r.TTL(b); t > 0 {
				ttl = t
			}
		}
		nc := &cached{status: http.StatusOK, body: b, expires: now.Add(ttl), etag: resp.Header.Get("ETag"),
			lastMd: resp.Header.Get("Last-Modified")}
		if err := f.save(ctx, r.Source, key, nc); err != nil {
			return nil, err
		}
		return b, nil
	case resp.StatusCode == http.StatusNotFound:
		f.result(r.Source, true)
		_ = f.save(ctx, r.Source, key, &cached{status: http.StatusNotFound, body: []byte{}, expires: now.Add(notFoundTTL)})
		return nil, ErrNotFound
	}
	f.result(r.Source, false)
	return stale(fmt.Errorf("live: %s: HTTP %d", r.Source, resp.StatusCode))
}
