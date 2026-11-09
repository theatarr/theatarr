package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
)

// Cache evidence: which debrid providers hold a release cached (docs/design/router.md "Cache evidence").
//
// Sources, all first-party or keyless:
//   - DMM's cached feeds tag each release with cached_on (rd, ad, tb). Within one search, any release with the same
//     info hash inherits it (MarkCachedOn), so a Torrentio or Prowlarr result DMM lists on /ad/cached is "ad".
//   - DMM's availability for the releases of a search with no rd or ad evidence yet (availability.go): its
//     positives are stored like a feed's, source feed:<indexer>/availability.
//   - provider_cache rows in state cached, written from those feeds (source feed:<indexer>) and from TorBox's
//     checkcached (source check:torbox), live for router.cache_evidence.ttl.
//   - TorBox's checkcached at search time, with our own key, for the best few hashes (Evidence.Check).
//
// Real-Debrid and AllDebrid have no cache check: RD's instantAvailability is disabled and AllDebrid removed
// magnet/instant (decypharr and zurg's AllDebrid clients both report it unsupported), so DMM (feeds, availability) is
// their only source.
// No debrid key ever goes to a third party: DMM is searched with DMM's key, Torrentio with the plain config.
//
// Evidence only orders providers. Its absence never means uncached: only a provider's own verdict (a refused add,
// a cache check that says uncached) is stored as uncached. Feed evidence does not replace such a verdict while it
// lives, with one exception: DMM's newly cached RSS list naming a hash on a provider (rd, ad) with a pubDate (when DMM
// listed it as cached) newer than our own uncached or miss verdict at that provider reopens that verdict once (put).
//
// A read-only cache check's uncached answer (TorBox's checkcached, here or in the router) lives CheckUncachedTTL.

// CheckUncachedTTL is how long a cache check's uncached answer lives, at most router.miss_ttl: the check is a cheap
// read, and DMM keeps its own TorBox misses as long.
const CheckUncachedTTL = 30 * time.Minute

// Provider codes, as DMM's feeds and the release views write them, in display order.
var cacheCodes = []string{"rd", "ad", "tb"}

// CacheCode is the short code of a provider kind ("rd", "ad", "tb"), or "".
func CacheCode(kind string) string {
	switch kind {
	case provider.KindRealDebrid:
		return "rd"
	case provider.KindAllDebrid:
		return "ad"
	case provider.KindTorBox:
		return "tb"
	}
	return ""
}

// Sources of cache evidence (provider_cache.source and Release.CachedVia).
const (
	cacheSourceFeed  = "feed:"        // + indexer key: a DMM cached feed listed the hash
	cacheSourceCheck = "check:torbox" // TorBox's checkcached at search time
)

// parseCachedOn reads a cached_on extra ("rd,ad") into codes.
func parseCachedOn(s string) []string {
	var out []string
	for _, x := range strings.Split(strings.ToLower(s), ",") {
		if x = strings.TrimSpace(x); slices.Contains(cacheCodes, x) && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// addCached adds code to r.CachedOn (kept in rd, ad, tb order) with where the evidence came from. It always builds
// new slices and maps: results are shallow copies of the service cache.
func (r *Release) addCached(code, via string) {
	if !slices.Contains(cacheCodes, code) {
		return
	}
	if slices.Contains(r.CachedOn, code) {
		return
	}
	on := append(append([]string(nil), r.CachedOn...), code)
	slices.SortFunc(on, func(a, b string) int { return slices.Index(cacheCodes, a) - slices.Index(cacheCodes, b) })
	r.CachedOn = on
	v := make(map[string]string, len(r.CachedVia)+1)
	for k, x := range r.CachedVia {
		v[k] = x
	}
	v[code] = via
	r.CachedVia = v
}

// MarkCachedOn fills CachedOn from the cached_on extras of rs: each release's own, and every other release of the
// same info hash in rs (cross-feed). It returns how many releases gained a provider from another release.
func MarkCachedOn(rs []Release) int {
	type ev struct{ code, via string }
	byHash := map[string][]ev{}
	for i := range rs {
		r := &rs[i]
		for _, c := range parseCachedOn(r.Extras["cached_on"]) {
			r.addCached(c, r.Indexer)
			if h := NormalizeHash(r.InfoHash); h != "" {
				byHash[h] = append(byHash[h], ev{c, r.Indexer})
			}
		}
	}
	n := 0
	for i := range rs {
		r := &rs[i]
		h := NormalizeHash(r.InfoHash)
		gained := false
		for _, e := range byHash[h] {
			if !slices.Contains(r.CachedOn, e.code) {
				r.addCached(e.code, e.via)
				gained = true
			}
		}
		if gained {
			n++
		}
	}
	return n
}

// Evidence stores and reads cache evidence (provider_cache) and runs the bounded TorBox check. A nil *Evidence
// does the in-memory part only (MarkCachedOn).
type Evidence struct {
	st  *store.Store
	pol *policy.Policy
	set policy.CacheEvidenceSettings
	// checkers are TorBox's read-only clients by provider key (key set).
	checkers map[string]provider.Provider
	log      *slog.Logger
	now      func() time.Time

	mu     sync.Mutex
	checks []time.Time          // checkcached requests in the last hour
	asked  map[string]time.Time // DMM availability: code+hash -> when DMM last answered for it (availability.go)
}

// EvidenceConfig configures NewEvidence. Store may be nil (nothing stored or read); Checkers may be empty (no
// search-time check, e.g. dry runs).
type EvidenceConfig struct {
	Store    *store.Store
	Policy   *policy.Policy
	Checkers map[string]provider.Provider
	Logger   *slog.Logger
	Now      func() time.Time
}

// NewEvidence builds the evidence keeper. Only TorBox providers among Checkers are used.
func NewEvidence(c EvidenceConfig) *Evidence {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	e := &Evidence{st: c.Store, pol: c.Policy, set: c.Policy.CacheEvidenceSettings(), checkers: map[string]provider.Provider{},
		log: c.Logger.With("component", "cache-evidence"), now: c.Now}
	for k, p := range c.Checkers {
		if pv := c.Policy.Providers[k]; p != nil && pv != nil && pv.Kind == provider.KindTorBox && p.Cache().Supported {
			e.checkers[k] = p
		}
	}
	return e
}

// SetEvidence attaches the evidence keeper: DMM results are stored as they arrive, and ApplyCachedOn and
// CheckCached use it. Call it before the first search.
func (s *Service) SetEvidence(e *Evidence) {
	s.mu.Lock()
	s.ev = e
	s.mu.Unlock()
}

func (s *Service) evidence() *Evidence {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ev
}

// ApplyCachedOn fills CachedOn for one search's releases (every indexer's, merged): their own cached_on, the same
// hash in another feed, and stored evidence. Errors reading the store are logged; the result is still usable.
func (s *Service) ApplyCachedOn(ctx context.Context, rs []Release) {
	MarkCachedOn(rs)
	if e := s.evidence(); e != nil {
		e.applyStored(ctx, rs)
	}
}

// CheckCached runs the bounded TorBox check (when configured) for the best releases of a search: up to
// torbox_check.top distinct hashes of priority, in order, that have no live TorBox evidence and whose indexer is listed
// in one of versions (the versions searched) that stores on debrid and may send its releases to a TorBox account. Answers are
// stored (cached for cache_evidence.ttl, uncached for router.miss_ttl) and cached hashes gain "tb" in priority and
// also. It reports how many hashes were checked.
func (s *Service) CheckCached(ctx context.Context, versions []*policy.VersionPolicy, priority, also []*Release) int {
	e := s.evidence()
	if e == nil {
		return 0
	}
	return e.check(ctx, versions, priority, also)
}

// providersOf lists the policy provider keys of a code's kind.
func (e *Evidence) providersOf(code string) []string {
	var out []string
	for _, k := range sortedProviderKeys(e.pol) {
		if CacheCode(e.pol.Providers[k].Kind) == code {
			out = append(out, k)
		}
	}
	return out
}

func sortedProviderKeys(p *policy.Policy) []string {
	var out []string
	for k := range p.Providers {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// recordFeed stores a DMM result's positives: one cached row per hash and provider of each cached_on code
// (docs/design/providers.md), whatever the release's privacy label. rss is DMM's newly cached list (an RSS sync): its pubDate is when DMM listed
// the hash as cached, so an item may reopen an older verdict of ours at the providers it names (put). A
// search result never does: its pubDate is not a cache event.
func (e *Evidence) recordFeed(ctx context.Context, rs []Release, rss bool) {
	if e == nil || e.st == nil {
		return
	}
	now := e.now()
	tx, err := e.st.Writer().BeginTx(ctx, nil)
	if err != nil {
		e.log.Warn("cache evidence not stored", "err", err)
		return
	}
	n := 0
	for i := range rs {
		r := &rs[i]
		h := NormalizeHash(r.InfoHash)
		if h == "" {
			continue
		}
		for _, c := range parseCachedOn(r.Extras["cached_on"]) {
			var listed time.Time
			if rss {
				// A pubDate ahead of our clock reopens nothing: under clock skew it would reopen each retry.
				if !r.PublishDate.After(now) {
					listed = r.PublishDate
				}
			}
			for _, pk := range e.providersOf(c) {
				if err := put(ctx, tx, h, pk, "cached", cacheSourceFeed+r.Indexer, now.Add(e.set.TTL), now, listed); err != nil {
					_ = tx.Rollback()
					e.log.Warn("cache evidence not stored", "indexer", r.Indexer, "err", err)
					return
				}
				n++
			}
		}
	}
	if err := tx.Commit(); err != nil {
		e.log.Warn("cache evidence not stored", "err", err)
		return
	}
	if n > 0 {
		e.log.Debug("cache evidence stored", "rows", n)
	}
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// put upserts a provider_cache row. Feed evidence never replaces a live first-party row (a router verdict, a check);
// nothing replaces a live miss (the router's verdict on every provider, or a 451). The one exception: listed, when not
// zero, is when DMM's newly cached list listed the hash cached; a cached feed row then replaces an older live verdict
// at that provider from our own add or cached-only check (uncached, router:add or router:check) or an all-provider
// miss (router:miss), never a 451. A retry that misses again writes a newer checked_at, so the same listing never
// reopens it twice.
func put(ctx context.Context, db execer, hash, pk, state, source string, expires, now, listed time.Time) error {
	var listedMs int64 // 0: no reopen
	if !listed.IsZero() {
		listedMs = store.Millis(listed)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO provider_cache (info_hash, provider, state, source,
		checked_at, expires_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (info_hash, provider) DO UPDATE SET
		state = excluded.state, source = excluded.source, checked_at = excluded.checked_at,
		expires_at = excluded.expires_at
		WHERE provider_cache.expires_at IS NOT NULL AND provider_cache.expires_at <= ?
		   OR (provider_cache.state <> 'miss'
		       AND (excluded.source NOT LIKE 'feed:%' OR provider_cache.source LIKE 'feed:%'))
		   OR (excluded.state = 'cached' AND excluded.source LIKE 'feed:%' AND ? > provider_cache.checked_at
		       AND (provider_cache.state = 'uncached' AND provider_cache.source IN ('router:add', 'router:check')
		            OR provider_cache.state = 'miss' AND provider_cache.source = 'router:miss'))`,
		hash, pk, state, source, store.Millis(now), store.Millis(expires), store.Millis(now), listedMs)
	if err != nil {
		return fmt.Errorf("provider cache: %w", err)
	}
	return nil
}

// liveRows returns, for each hash, the live provider_cache rows by provider key.
type cacheRow struct{ state, source string }

func (e *Evidence) liveRows(ctx context.Context, hashes []string) (map[string]map[string]cacheRow, error) {
	out := map[string]map[string]cacheRow{}
	if e.st == nil || len(hashes) == 0 {
		return out, nil
	}
	now := store.Millis(e.now())
	for i := 0; i < len(hashes); i += 400 {
		batch := hashes[i:min(i+400, len(hashes))]
		args := make([]any, 0, len(batch)+1)
		for _, h := range batch {
			args = append(args, h)
		}
		args = append(args, now)
		rows, err := e.st.Reader().QueryContext(ctx, `SELECT info_hash, provider, state, source FROM provider_cache
			WHERE info_hash IN (?`+strings.Repeat(",?", len(batch)-1)+`) AND (expires_at IS NULL OR expires_at > ?)`,
			args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h, pk string
			var row cacheRow
			if err := rows.Scan(&h, &pk, &row.state, &row.source); err != nil {
				rows.Close()
				return nil, err
			}
			if out[h] == nil {
				out[h] = map[string]cacheRow{}
			}
			out[h][pk] = row
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func distinctHashes(rs []*Release) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range rs {
		if h := NormalizeHash(r.InfoHash); h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// applyStored adds live cached rows to the releases.
func (e *Evidence) applyStored(ctx context.Context, rs []Release) {
	if e.st == nil {
		return
	}
	ptrs := make([]*Release, len(rs))
	for i := range rs {
		ptrs[i] = &rs[i]
	}
	live, err := e.liveRows(ctx, distinctHashes(ptrs))
	if err != nil {
		e.log.Warn("cache evidence not read", "err", err)
		return
	}
	for _, r := range ptrs {
		for pk, row := range live[NormalizeHash(r.InfoHash)] {
			if row.state != "cached" {
				continue
			}
			if pv := e.pol.Providers[pk]; pv != nil {
				r.addCached(CacheCode(pv.Kind), row.source)
			}
		}
	}
}

// takeCheck spends one checkcached request of the hourly cap.
func (e *Evidence) takeCheck(now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	kept := e.checks[:0]
	for _, t := range e.checks {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
		}
	}
	e.checks = kept
	if len(e.checks) >= e.set.CheckPerHour {
		return false
	}
	e.checks = append(e.checks, now)
	return true
}

func (e *Evidence) check(ctx context.Context, versions []*policy.VersionPolicy, priority, also []*Release) int {
	if !e.set.CheckEnabled || len(e.checkers) == 0 || e.set.CheckTop <= 0 {
		return 0
	}
	// Candidates: hashed, not already known on TorBox (from a feed of this search, or live rows), and from an indexer
	// the user sends to TorBox: listed in a debrid version being searched whose providers, as the indexer allows
	// them (indexers.K.providers), include a TorBox account. That account asks.
	var cands []*Release
	via := map[*Release]string{}
	for _, r := range priority {
		if NormalizeHash(r.InfoHash) == "" || slices.Contains(r.CachedOn, "tb") {
			continue
		}
		if pk := e.checkerFor(versions, r.Indexer); pk != "" {
			cands = append(cands, r)
			via[r] = pk
		}
	}
	hashes := distinctHashes(cands)
	live, err := e.liveRows(ctx, hashes)
	if err != nil {
		e.log.Warn("cache evidence not read", "err", err)
		return 0
	}
	// Hashes to check, grouped by the TorBox account that asks.
	type group struct {
		pk     string
		hashes []string
	}
	var groups []*group
	byPK := map[string]*group{}
	picked := map[string]bool{}
	for _, r := range cands {
		if len(picked) >= e.set.CheckTop {
			break
		}
		h := NormalizeHash(r.InfoHash)
		if picked[h] || e.knownOnTorBox(live[h]) {
			continue
		}
		picked[h] = true
		g := byPK[via[r]]
		if g == nil {
			g = &group{pk: via[r]}
			byPK[via[r]] = g
			groups = append(groups, g)
		}
		g.hashes = append(g.hashes, h)
	}
	if len(picked) == 0 {
		return 0
	}
	cached := map[string]bool{}
	checked := 0
	for _, g := range groups {
		now := e.now()
		if !e.takeCheck(now) {
			e.log.Info("torbox cache check skipped: hourly cap reached", "per_hour", e.set.CheckPerHour)
			break
		}
		got, err := e.checkers[g.pk].CheckCached(ctx, provider.CacheQuery{Hashes: g.hashes})
		if err != nil {
			e.log.Info("torbox cache check failed", "provider", g.pk, "err", err)
			continue
		}
		now = e.now()
		for _, h := range g.hashes {
			v, ok := got[h]
			if !ok {
				continue
			}
			checked++
			state, exp := "uncached", now.Add(min(CheckUncachedTTL, e.set.UncachedTTL))
			if v {
				state, exp = "cached", now.Add(e.set.TTL)
				cached[h] = true
			}
			if e.st == nil {
				continue
			}
			for _, k := range e.providersOf("tb") {
				if err := put(ctx, e.st.Writer(), h, k, state, cacheSourceCheck, exp, now, time.Time{}); err != nil {
					e.log.Warn("cache evidence not stored", "err", err)
				}
			}
		}
	}
	for _, list := range [][]*Release{priority, also} {
		for _, r := range list {
			if cached[NormalizeHash(r.InfoHash)] {
				r.addCached("tb", cacheSourceCheck)
			}
		}
	}
	e.log.Debug("torbox cache check", "hashes", checked, "cached", len(cached))
	return checked
}

// checkerFor is the TorBox account that may ask about a release of indexer ix: the first, in provider order, of a
// debrid version in versions that lists ix, among the providers ix allows. "" when none.
func (e *Evidence) checkerFor(versions []*policy.VersionPolicy, ix string) string {
	for _, v := range versions {
		if v == nil || v.Storage != policy.StorageDebrid || !slices.Contains(v.Indexers, ix) {
			continue
		}
		for _, pk := range e.pol.ReleaseProviders(v, ix) {
			if e.checkers[pk] != nil {
				return pk
			}
		}
	}
	return ""
}

// knownOnTorBox reports whether some TorBox provider has a live first-party row (a check or a router verdict) or
// any live cached row for the hash: no need to ask again.
func (e *Evidence) knownOnTorBox(rows map[string]cacheRow) bool {
	for pk, row := range rows {
		pv := e.pol.Providers[pk]
		if pv == nil || pv.Kind != provider.KindTorBox {
			continue
		}
		if row.state == "cached" || !strings.HasPrefix(row.source, cacheSourceFeed) {
			return true
		}
	}
	return false
}

// CachedCodes is every provider code the release carries evidence for, with where it came from: CachedOn, and its
// own cached_on extra for a release that never went through ApplyCachedOn.
func (r *Release) CachedCodes() map[string]string {
	out := map[string]string{}
	for _, c := range parseCachedOn(r.Extras["cached_on"]) {
		out[c] = r.Indexer
	}
	for _, c := range r.CachedOn {
		if slices.Contains(cacheCodes, c) {
			via := r.CachedVia[c]
			if via == "" {
				via = "search"
			}
			out[c] = via
		}
	}
	return out
}

// CachedList is CachedCodes as a list in rd, ad, tb order; nil when there is no evidence.
func (r *Release) CachedList() []string {
	m := r.CachedCodes()
	var out []string
	for _, c := range cacheCodes {
		if _, ok := m[c]; ok {
			out = append(out, c)
		}
	}
	return out
}
