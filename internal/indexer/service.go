package indexer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/indexer/cardigann"
	"github.com/theatarr/theatarr/internal/policy"
)

// Defaults for Service.
const (
	// DefaultCacheTTL is how long a search result is reused (15 min, a guess).
	DefaultCacheTTL = 15 * time.Minute
	// DefaultSearchTimeout bounds one upstream search, paging and budget waits included. Callers waiting on it
	// keep their own deadlines.
	DefaultSearchTimeout = 5 * time.Minute
)

// Options configure New.
type Options struct {
	Logger *slog.Logger
	// APIKeys supplies resolved settings credentials by indexer key. A missing entry keeps APIKeyEnv fallback behavior.
	APIKeys map[string]string
	// Priority is the settings-store indexer priority by key; lower numbers are searched first.
	Priority map[string]int
	// CacheTTL: 0 means DefaultCacheTTL; negative disables the cache (single-flight still applies).
	// Cardigann runs the cardigann indexers' definitions; nil makes their searches fail with ErrNoCatalog.
	Cardigann     *cardigann.Manager
	CacheTTL      time.Duration
	SearchTimeout time.Duration
	now           func() time.Time // tests
}

// Service holds a Searcher per policy indexer, and runs searches with single-flight and a short result cache.
type Service struct {
	searchers map[string]Searcher
	priority  map[string]int
	log       *slog.Logger
	ttl       time.Duration
	timeout   time.Duration
	now       func() time.Time

	mu       sync.Mutex
	cache    map[string]cacheEntry
	inflight map[string]*flight
	lastGC   time.Time
	ev       *Evidence // cache evidence; nil: in-memory only
}

type cacheEntry struct {
	res *Result
	at  time.Time
}

type flight struct {
	waiters int
	cancel  context.CancelFunc
	done    chan struct{}
	res     *Result
	err     error
}

// New builds a Searcher for every indexer in the policy. Every request they make goes through gw.
func New(p *policy.Policy, gw Gateway, o Options) (*Service, error) {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.CacheTTL == 0 {
		o.CacheTTL = DefaultCacheTTL
	}
	if o.SearchTimeout <= 0 {
		o.SearchTimeout = DefaultSearchTimeout
	}
	if o.now == nil {
		o.now = time.Now
	}
	s := &Service{searchers: map[string]Searcher{}, priority: map[string]int{}, log: o.Logger.With("component", "indexer"), ttl: o.CacheTTL,
		timeout: o.SearchTimeout, now: o.now, cache: map[string]cacheEntry{}, inflight: map[string]*flight{}}
	for k, ix := range p.Indexers {
		key, hasKey := o.APIKey(k)
		sr, err := newSearcher(base{key: k, kind: ix.Kind, ix: ix, gw: gw, apiKeyValue: key, hasAPIKey: hasKey}, o.now, o.Cardigann)
		if err != nil {
			return nil, err
		}
		s.searchers[k] = sr
		s.priority[k] = o.Priority[k]
	}
	return s, nil
}

func (o Options) APIKey(key string) (string, bool) {
	v, ok := o.APIKeys[key]
	return v, ok
}

func newSearcher(b base, now func() time.Time, cm *cardigann.Manager) (Searcher, error) {
	switch b.kind {
	case "torznab":
		return newTorznab(b, false, now)
	case "dmm":
		return newTorznab(b, true, now)
	case "torrentio":
		return newTorrentio(b)
	case "zilean":
		return newZilean(b)
	case "seadex":
		return newSeadex(b)
	case "cardigann":
		return newCardigann(b, cm)
	}
	return nil, fmt.Errorf("indexers[%s].kind: no adapter for %q", b.key, b.kind)
}

// Searcher returns the searcher for a policy key, or nil.
func (s *Service) Searcher(key string) Searcher { return s.searchers[key] }

// RSSChecker is a searcher that can say before a read why it cannot feed the RSS sync (a catalog definition without a
// latest-releases search, or no catalog).
type RSSChecker interface {
	RSSProblem(ctx context.Context) string
}

// RSSProblem says why the indexer cannot feed the RSS sync, or "" when it can.
func (s *Service) RSSProblem(ctx context.Context, key string) string {
	sr := s.searchers[key]
	switch {
	case sr == nil:
		return "unknown indexer"
	case !sr.Supports(Query{Type: QueryRSS}):
		return "the source has no recent-release feed"
	}
	if c, ok := sr.(RSSChecker); ok {
		return c.RSSProblem(ctx)
	}
	return ""
}

// RSSFeeds lists an indexer's recent-release feeds when it has several (a DMM path naming rd and ad: one list per
// provider), each read with Query.RSSFeed; nil when it has one.
func (s *Service) RSSFeeds(key string) []string {
	if f, ok := s.searchers[key].(interface{ RSSFeeds() []string }); ok {
		return f.RSSFeeds()
	}
	return nil
}

// Priority returns the configured settings priority, with zero as the default.
func (s *Service) Priority(key string) int { return s.priority[key] }

// Keys lists the indexer keys, sorted.
func (s *Service) Keys() []string {
	out := make([]string, 0, len(s.searchers))
	for k := range s.searchers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Search runs q on one indexer. Identical concurrent searches (same indexer, same normalised query) make one
// upstream search, and a successful result is reused for the cache TTL; an RSS page never is (e2eperf 1): each sync
// reads the feed fresh, or a release published just after one read waits a whole interval more. The returned Result
// is the caller's own copy.
func (s *Service) Search(ctx context.Context, key string, q Query) (*Result, error) {
	sr := s.searchers[key]
	if sr == nil {
		return nil, fmt.Errorf("indexer: unknown indexer %q", key)
	}
	if err := q.Validate(); err != nil {
		return nil, err
	}
	if !sr.Supports(q) {
		return nil, fmt.Errorf("%w: %s (%s) cannot answer a %s search with the ids given", ErrUnsupported, key, sr.Kind(), q.Type)
	}
	ck := key + "\x00" + q.Key()
	now := s.now()

	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if e, ok := s.cache[ck]; ok && s.ttl > 0 && q.Type != QueryRSS && now.Sub(e.at) < s.ttl {
		s.mu.Unlock()
		r := e.res.clone()
		r.Cached = true
		s.log.Debug("search cached", "indexer", key, "type", q.Type, "results", len(r.Releases))
		return r, nil
	}
	f, shared := s.inflight[ck]
	if !shared {
		f = &flight{done: make(chan struct{})}
		s.inflight[ck] = f
		// The search survives only while it has live waiters.
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.timeout)
		f.cancel = cancel
		go func() {
			defer cancel()
			s.run(fctx, ck, key, sr, q, f)
		}()
	}
	f.waiters++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		f.waiters--
		if f.waiters == 0 {
			f.cancel()
			if s.inflight[ck] == f {
				delete(s.inflight, ck)
			}
		}
	}()

	select {
	case <-f.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if f.err != nil {
		if shared {
			return nil, sharedError(f.err) // the requests were the first caller's
		}
		return nil, f.err
	}
	r := f.res.clone()
	r.Shared = shared
	return r, nil
}

func (s *Service) run(ctx context.Context, ck, key string, sr Searcher, q Query, f *flight) {
	start := time.Now()
	s.mu.Lock()
	ev := s.ev
	s.mu.Unlock()
	var res *Result
	var err error
	uncached := false
	locked := true
	if sr.Kind() == "seadex" {
		before := time.Now()
		var unlock func()
		unlock, err = ev.lockSeaDex(ctx)
		seaDexLockWait.Observe(time.Since(before).Seconds())
		if err == nil {
			defer unlock()
		} else if ctx.Err() == nil && !ev.prefersSeaDex() {
			s.log.Warn("SeaDex standing lock unavailable; answer used without standing", "indexer", key, "err", err)
			locked, uncached, err = false, true, nil
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		res, err = sr.Search(ctx, q)
	}
	took := time.Since(start)
	if err == nil {
		// One identity per release from here on (decisions, journal, shadow, router): a release whose reported hash
		// and magnet have conflicting or unverifiable identities is dropped and logged.
		// An adapter that filters its upstream page itself (Torznab's GUID dedupe) records the raw page; otherwise
		// the page is the releases as returned, before the identity filter.
		if q.Type == QueryRSS && res.RSSPage == nil {
			res.RSSPage = make([]Release, len(res.Releases))
			for i, rel := range res.Releases {
				res.RSSPage[i] = rssCursorFields(rel)
			}
		}
		var conflicting []Release
		res.Releases, conflicting = NormalizeIdentities(res.Releases)
		for _, r := range conflicting {
			s.log.Warn("release dropped: conflicting or unverifiable torrent identity",
				"indexer", key, "title", r.Title, "info_hash", r.InfoHash, "magnet_hash", HashFromMagnet(r.Magnet.URL()))
		}
		if len(conflicting) > 0 {
			res.Notes = append(res.Notes, fmt.Sprintf("dropped %d releases with conflicting or unverifiable torrent identities", len(conflicting)))
		}
	}
	// A SeaDex answer is published (cache and waiters) only after its standing observation is committed, so no caller
	// pairs the fresh answer with an older pinned standing. The write has its own bounded deadline, so an
	// upstream answer that arrives close to the search timeout is still stored. If it cannot be stored and a version
	// prefers SeaDex, the search fails (a prefer-on caller never decides a fresh answer against standing that does not
	// reflect it); when none does, standing decides nothing, so the answer is returned, logged and not cached (the
	// next search stores it). The upstream requests stay
	// counted on failure (RequestsSpent).
	if err == nil && sr.Kind() == "seadex" && locked {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), seadexStandingWriteTimeout)
		serr := ev.observeSeaDex(wctx, res.SeaDex)
		cancel()
		switch {
		case serr != nil && ev.prefersSeaDex():
			res, err = nil, &spentError{requests: res.Requests, err: fmt.Errorf("indexer %s: SeaDex standing not stored: %w", key, serr)}
		case serr != nil:
			s.log.Warn("SeaDex standing not stored; no version prefers SeaDex, so the answer is still used", "indexer", key, "err", serr)
			uncached = true
		}
	}
	s.mu.Lock()
	if s.inflight[ck] == f {
		delete(s.inflight, ck)
	}
	if err == nil && s.ttl > 0 && !uncached && q.Type != QueryRSS && ctx.Err() == nil {
		now := s.now()
		s.cache[ck] = cacheEntry{res: res, at: now}
		if now.Sub(s.lastGC) > s.ttl {
			for k, e := range s.cache {
				if now.Sub(e.at) >= s.ttl {
					delete(s.cache, k)
				}
			}
			s.lastGC = now
		}
	}
	f.res, f.err = res, err
	s.mu.Unlock()
	if err == nil && sr.Kind() == "dmm" {
		// DMM's feeds are cache evidence: stored per hash and provider whoever searched (shadow, acquirer, views).
		// Its RSS answer is the newly cached list, whose listings may reopen an older uncached verdict.
		ev.recordFeed(ctx, res.Releases, q.Type == QueryRSS)
	}
	close(f.done)
	if err != nil {
		s.log.Warn("search failed", "indexer", key, "type", q.Type, "took_ms", took.Milliseconds(), "err", err)
		return
	}
	s.log.Info("search", "indexer", key, "kind", sr.Kind(), "type", q.Type, "results", len(res.Releases),
		"requests", res.Requests, "unrelated", res.unrelated, "truncated", res.Truncated, "took_ms", took.Milliseconds())
}

// seadexStandingWriteTimeout bounds storing one SeaDex answer's standing, apart from the search's own deadline.
const seadexStandingWriteTimeout = 30 * time.Second

// spentError is a failed search that made upstream requests for its caller.
type spentError struct {
	requests int
	err      error
}

func (e *spentError) Error() string { return e.err.Error() }
func (e *spentError) Unwrap() error { return e.err }

// sharedError is err as a caller that shared another's search sees it: without the other's request count.
func sharedError(err error) error {
	var se *spentError
	if errors.As(err, &se) {
		return se.err
	}
	return err
}

// RequestsSpent is how many upstream requests a failed Search made on the caller's behalf (a SeaDex answer whose
// standing could not be stored); 0 for any other error and for a caller that shared another's search. Request
// accounting (the RSS sync's counters) adds it like a successful result's Requests.
func RequestsSpent(err error) int {
	var se *spentError
	if errors.As(err, &se) {
		return se.requests
	}
	return 0
}

// StandingDB is the local evidence database, when configured.
func (s *Service) StandingDB() *sql.DB {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ev == nil || s.ev.st == nil {
		return nil
	}
	return s.ev.st.Reader()
}
