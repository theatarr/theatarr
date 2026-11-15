package rss

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/theatarr/theatarr/internal/indexer"
)

// withCacheEvidence is a copy of rels with the cache evidence a live search applies before deciding: the same hash in
// another release and live stored provider_cache rows (Service.ApplyCachedOn), so the ranking's cache tie-break sees
// what the route reads. Stored evidence only: it asks no provider or DMM.
func (r *Runner) withCacheEvidence(ctx context.Context, rels []indexer.Release) []indexer.Release {
	out := slices.Clone(rels) // evidence is added with fresh slices and maps: the cached search is not changed
	if r.idx != nil {
		r.idx.ApplyCachedOn(ctx, out)
	}
	return out
}

// budgetBlocked says why an indexer should not be asked now: its budget's hourly or daily cap is spent, or it or
// its budget is backing off. Empty when it may be asked.
func (r *Runner) budgetBlocked(ctx context.Context, key string) string {
	if r.budgets == nil {
		return ""
	}
	ix := r.pol.Indexers[key]
	if ix == nil {
		return ""
	}
	st := r.budgets.StateFor(ctx)
	for _, b := range st.Backoffs {
		if b.Active && (b.Subject == "indexer:"+key || (ix.Budget != "" && b.Subject == "budget:"+ix.Budget)) {
			return fmt.Sprintf("%s backing off until %s (%s)", b.Subject, b.Until.UTC().Format(time.RFC3339), b.Reason)
		}
	}
	for _, b := range st.Budgets {
		if b.Key == ix.Budget && (b.HourCapped || b.DayCapped) {
			return fmt.Sprintf("budget %s is spent for this period", b.Key)
		}
	}
	return ""
}

type metrics struct {
	requests prometheus.Counter
	rssItems prometheus.Counter
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		requests: prometheus.NewCounter(prometheus.CounterOpts{Name: "theatarr_shadow_indexer_requests_total",
			Help: "Upstream indexer requests the shadow runner made (searches and RSS; cached results not counted)."}),
		rssItems: prometheus.NewCounter(prometheus.CounterOpts{Name: "theatarr_shadow_rss_items_total",
			Help: "New RSS items decided."}),
	}
	if reg != nil {
		reg.MustRegister(m.requests, m.rssItems)
	}
	return m
}
