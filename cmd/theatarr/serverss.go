package main

import (
	"context"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/theatarr/theatarr/internal/acquire/rss"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

// newRSSRunner builds the RSS sync theatarr serve runs (job shadow.rss, docs/design/rss.md): the Live libraries' feeds,
// their accepted items to the router rt. It is the decision engine's film index.
func newRSSRunner(pol *policy.Policy, st *store.Store, q *jobs.Queue, gw *gateway.Gateway, reg prometheus.Registerer,
	log *slog.Logger, rt rss.Router, prepared *serveRuntime, runtime rss.RuntimeSource) *rss.Runner {
	r := rss.New(rss.Config{Store: st, Policy: pol, Engine: prepared.Engine, Queue: q, Indexers: prepared.Indexers,
		Budgets: gw, Router: rt, Logger: log, Registerer: reg, Runtime: runtime})
	log.Info("shadow runner", "rss_to_router", r.ActiveVersions())
	return r
}

// rssRuntimeSource resolves the RSS sync's runtime from the settings generation in force.
type rssRuntimeSource struct{ manager *settings.RuntimeCoordinator }

func (s rssRuntimeSource) Resolve(ctx context.Context, _ int64) (rss.Runtime, bool) {
	current := settings.ContextRuntime(ctx)
	if current == nil {
		current = s.manager.Current()
	}
	components, ok := current.Components.(*serveRuntime)
	if !ok || components == nil {
		return rss.Runtime{}, false
	}
	return rss.Runtime{Generation: current.Generation, Settings: current, Policy: current.Policy, Engine: components.Engine,
		Indexers: components.Indexers, Budgets: components.Gateway, Router: components.Router}, true
}
