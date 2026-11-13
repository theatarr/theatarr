package indexer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// DMM's availability (docs/design/router.md "Cache evidence"): POST /api/sponsor/availability on the DMM indexer's host,
// with the DMM indexer's key (x-api-key) and through its target and budget. It answers which of up to 100 hashes DMM's
// users found fully downloaded on their own Real-Debrid or AllDebrid account. A listed hash is evidence like a DMM
// cached feed's (provider_cache cached, for cache_evidence.ttl); a hash it does not list is unknown, and nothing is
// written for it. Releases of any privacy label are asked about, only for rd and ad. No imdbId is sent: unscoped,
// DMM listed about a quarter more hashes in a measurement.
const (
	availabilityBatch = 100 // DMM's limit per call
	// availabilityCalls bounds the calls of one search (rd and ad together); availabilityTimeout bounds the whole
	// pass, budget waits included: DMM shares its budget with the searches, and the pass must not hold one up.
	availabilityCalls   = 4
	availabilityTimeout = 15 * time.Second
	// availabilityAskedTTL is how long an answered hash is not asked again, about as long as a search answer is
	// reused: a title's versions search the same releases one after another.
	availabilityAskedTTL = DefaultCacheTTL
	// cacheSourceAvailability follows the DMM indexer key in the source: feed:<indexer>/availability. It keeps the
	// feed: prefix, so put treats it as feed evidence (it never replaces a live verdict of ours).
	cacheSourceAvailability = "/availability"
)

// availabilityStoreTimeout bounds storing one pass's positives: past it the search goes on with its in-memory marks
// and nothing is stored (a busy writer must not hold the search).
var availabilityStoreTimeout = 5 * time.Second

// availabilityAsker asks DMM's availability for one provider code ("rd" or "ad"): the hashes DMM lists.
type availabilityAsker interface {
	Key() string
	availability(ctx context.Context, service string, hashes []string) ([]string, error)
}

// availability posts one availability request.
func (t *torznab) availability(ctx context.Context, service string, hashes []string) ([]string, error) {
	key, err := t.apiKey()
	if err != nil {
		return nil, err
	}
	u := *t.endpoint
	u.Path = u.Path[:strings.LastIndex(u.Path, "/torznab/")] + "/sponsor/availability"
	u.RawPath, u.RawQuery = "", ""
	b, err := json.Marshal(map[string]any{"service": service, "hashes": hashes})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("indexer %s: bad URL", t.key)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", key)
	resp, err := t.gw.Do(ctx, t.target(), req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil, &HTTPError{Indexer: t.key, Status: resp.StatusCode}
	}
	var ans struct {
		Available []struct {
			Hash string `json:"hash"`
		} `json:"available"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ans); err != nil {
		return nil, fmt.Errorf("indexer %s: availability answer: %w", t.key, err)
	}
	var out []string
	for _, a := range ans.Available {
		if h := NormalizeHash(a.Hash); h != "" && slices.Contains(hashes, h) && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out, nil
}

// availabilitySource is the first DMM indexer, by key, with its key set; nil when there is none (or it is disabled:
// a disabled indexer has no searcher).
func (s *Service) availabilitySource() availabilityAsker {
	for _, k := range s.Keys() {
		t, ok := s.searchers[k].(*torznab)
		if !ok || !t.dmm {
			continue
		}
		if key, err := t.apiKey(); err == nil && key != "" {
			return t
		}
	}
	return nil
}

// CheckAvailability asks DMM's availability, after ApplyCachedOn, about the hashed releases of one search that have no
// rd or ad evidence yet, whatever their privacy label, for the providers (rd, ad) a debrid version in versions may send them to. Listed hashes gain the provider in rs and are stored like a DMM cached feed's;
// nothing else changes. A failed or slow call ends the pass with one log line; each call's answer stands on its own, so
// the hashes of the calls that answered keep their marks (applied and stored) and only the failed call's stay unmarked.
// It reports how many hashes gained a provider.
func (s *Service) CheckAvailability(ctx context.Context, versions []*policy.VersionPolicy, rs []Release) int {
	e := s.evidence()
	if e == nil {
		return 0
	}
	src := s.availabilitySource()
	if src == nil {
		return 0
	}
	return e.availability(ctx, src, versions, rs)
}

// CheckAvailabilityHash asks DMM's availability about one hash for one provider code ("rd" or "ad"), outside a search:
// the router's "Add to other providers" (internal/router copy.go). listed is true when DMM lists it; the answer is then
// stored like a search's (a DMM cached feed's evidence for every provider of the code). Not listed is unknown, never
// uncached, and nothing is written. ok is false when there is no DMM indexer with its key set (or the code is not
// one DMM answers for), and nothing was asked.
func (s *Service) CheckAvailabilityHash(ctx context.Context, code, hash string) (listed, ok bool, err error) {
	h := NormalizeHash(hash)
	if h == "" || (code != "rd" && code != "ad") {
		return false, false, nil
	}
	src := s.availabilitySource()
	if src == nil {
		return false, false, nil
	}
	cctx, cancel := context.WithTimeout(ctx, availabilityTimeout)
	defer cancel()
	got, err := src.availability(cctx, code, []string{h})
	if err != nil {
		return false, true, err
	}
	if !slices.Contains(got, h) {
		return false, true, nil
	}
	if e := s.evidence(); e != nil {
		e.recordAvailability(ctx, map[string][]string{h: {code}}, cacheSourceFeed+src.Key()+cacheSourceAvailability)
	}
	return true, true, nil
}

func (e *Evidence) availability(ctx context.Context, src availabilityAsker, versions []*policy.VersionPolicy,
	rs []Release) int {
	// Candidates per code, in release order: hashed, from an indexer a debrid version being searched sends to that
	// provider kind, with no evidence for it yet. Privacy is a label and plays no part.
	wanted := map[string][]string{}
	picked := map[string]bool{}
	for i := range rs {
		r := &rs[i]
		h := NormalizeHash(r.InfoHash)
		if h == "" {
			continue
		}
		for _, c := range e.availabilityCodes(versions, r.Indexer) {
			if !slices.Contains(r.CachedOn, c) && !picked[c+h] {
				picked[c+h] = true
				wanted[c] = append(wanted[c], h)
			}
		}
	}
	if len(picked) == 0 {
		return 0
	}
	var all []string
	for _, c := range []string{"rd", "ad"} {
		all = append(all, wanted[c]...)
	}
	live, err := e.liveRows(ctx, all)
	if err != nil {
		e.log.Warn("cache evidence not read", "err", err)
		return 0
	}
	// Not asked: a hash with any live row at a provider of that kind (stored evidence, our own verdict, a miss), or
	// one DMM answered for lately.
	now := e.now()
	e.mu.Lock()
	for k, at := range e.asked {
		if now.Sub(at) >= availabilityAskedTTL {
			delete(e.asked, k)
		}
	}
	for c, hs := range wanted {
		wanted[c] = slices.DeleteFunc(hs, func(h string) bool {
			if _, ok := e.asked[c+h]; ok {
				return true
			}
			for pk := range live[h] {
				if pv := e.pol.Providers[pk]; pv != nil && CacheCode(pv.Kind) == c {
					return true
				}
			}
			return false
		})
	}
	e.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, availabilityTimeout)
	defer cancel()
	listed := map[string][]string{} // hash -> codes
	calls, asked := 0, 0
	// rd and ad take turns, a batch each, so the call bound shares them.
	for b := 0; calls < availabilityCalls; b++ {
		more := false
		for _, c := range []string{"rd", "ad"} {
			hs := wanted[c]
			if b*availabilityBatch >= len(hs) || calls >= availabilityCalls {
				continue
			}
			more = true
			batch := hs[b*availabilityBatch : min((b+1)*availabilityBatch, len(hs))]
			calls++
			got, err := src.availability(ctx, c, batch)
			if err != nil {
				e.log.Info("DMM availability not checked; the search goes on without it", "indexer", src.Key(),
					"err", err)
				more = false
				break
			}
			asked += len(batch)
			at := e.now()
			e.mu.Lock()
			if e.asked == nil {
				e.asked = map[string]time.Time{}
			}
			for _, h := range batch {
				e.asked[c+h] = at
			}
			e.mu.Unlock()
			for _, h := range got {
				listed[h] = append(listed[h], c)
			}
		}
		if !more {
			break
		}
	}
	if len(listed) == 0 {
		if asked > 0 {
			e.log.Debug("DMM availability", "hashes", asked, "listed", 0)
		}
		return 0
	}
	source := cacheSourceFeed + src.Key() + cacheSourceAvailability
	e.recordAvailability(ctx, listed, source)
	gained := 0
	for i := range rs {
		r := &rs[i]
		for _, c := range listed[NormalizeHash(r.InfoHash)] {
			if !slices.Contains(r.CachedOn, c) {
				r.addCached(c, source)
				gained++
			}
		}
	}
	e.log.Debug("DMM availability", "hashes", asked, "listed", len(listed))
	return gained
}

// availabilityCodes are the codes DMM's availability answers for (rd, ad) of the providers a debrid version in versions
// that lists indexer ix may send its releases to.
func (e *Evidence) availabilityCodes(versions []*policy.VersionPolicy, ix string) []string {
	var out []string
	for _, v := range versions {
		if v == nil || v.Storage != policy.StorageDebrid || !slices.Contains(v.Indexers, ix) {
			continue
		}
		for _, pk := range e.pol.ReleaseProviders(v, ix) {
			pv := e.pol.Providers[pk]
			if pv == nil {
				continue
			}
			if c := CacheCode(pv.Kind); (c == "rd" || c == "ad") && !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	return out
}

// recordAvailability stores DMM's listed hashes as feed evidence: one cached row per hash and provider of the code.
func (e *Evidence) recordAvailability(ctx context.Context, listed map[string][]string, source string) {
	if e.st == nil {
		return
	}
	// The pass's own deadline may be spent; storing what DMM answered is not a request, but it is bounded.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), availabilityStoreTimeout)
	defer cancel()
	now := e.now()
	for h, codes := range listed {
		for _, c := range codes {
			for _, pk := range e.providersOf(c) {
				if err := put(ctx, e.st.Writer(), h, pk, "cached", source, now.Add(e.set.TTL), now, time.Time{}); err != nil {
					e.log.Warn("cache evidence not stored", "err", err)
					return
				}
			}
		}
	}
}
