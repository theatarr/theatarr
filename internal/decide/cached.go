package decide

import (
	"slices"

	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// SetUsableProviders tells the engine which policy providers routes can add to (enabled, with a key), for the cache
// tie-break of the ranking. nil, the default, counts every provider of the version.
func (e *Engine) SetUsableProviders(usable func(key string) bool) { e.usable = usable }

// cacheStanding is where the release's cache evidence (its CachedOn and cached_on extra, which the search fills from
// the feeds, stored provider_cache rows and the TorBox check: what the route orders its providers by) puts it among
// the providers v may send it to: the version's providers as the release's indexer allows them, usable ones only.
func (e *Engine) cacheStanding(v *policy.VersionPolicy, rel *indexer.Release) profile.CacheStanding {
	var out profile.CacheStanding
	codes := rel.CachedCodes()
	if len(codes) == 0 {
		return out
	}
	for _, pk := range e.Policy.ReleaseProviders(v, rel.Indexer) {
		pv := e.Policy.Providers[pk]
		if pv == nil || e.usable != nil && !e.usable(pk) {
			continue
		}
		if _, ok := codes[indexer.CacheCode(pv.Kind)]; !ok {
			continue
		}
		if out.Count == 0 && v.ProviderPreferenceRule() == policy.ProviderPreferenceOrdered {
			out.First = slices.Index(v.Providers, pk) + 1
		}
		out.Count++
	}
	return out
}

// Recache sets d's cache standing again from rel, the release it was decided for, after its cache evidence grew
// (a check run on the ranked releases); Rank then orders by it. v is the version d was decided for.
func (e *Engine) Recache(d *Decision, v Version, rel indexer.Release) {
	if d == nil || d.subject == nil {
		return
	}
	if v.Policy == nil {
		v.Policy = e.Policy.Versions[v.Key]
	}
	if v.Policy == nil {
		return
	}
	d.subject.Cached = e.cacheStanding(v.Policy, &rel)
}
