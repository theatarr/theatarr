package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/metadata/anime/autobuild"
	"github.com/theatarr/theatarr/internal/metadata/tvdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// newServeAnime builds a generation's anime builder (docs/design/anime.md "Automatic builds"): the live sources
// through the gateway, TVDB from tvc (nil without its key) with the imported Sonarr behind it. Without the anime
// metadata sources it is off, saying why, and its loop does not run.
func newServeAnime(pol *policy.Policy, st *store.Store, q *jobs.Queue, gw *gateway.Gateway, tvc *tvdb.Client,
	log *slog.Logger) *autobuild.Service {
	cfg := autobuild.Config{Policy: pol, Store: st, Queue: q, Logger: log}
	o := autobuild.SourceOptions{Store: st, Doer: gw, Logger: log}
	if tvc != nil {
		o.TVDB = tvc
	}
	l, err := autobuild.LiveSources(pol, o)
	var nc *autobuild.NotConfigured
	switch {
	case errors.As(err, &nc):
		cfg.Off = err.Error()
	case err != nil:
		cfg.Off = err.Error()
		log.Warn("anime: live sources", "err", err)
	default:
		cfg.Live = l
	}
	return autobuild.New(cfg)
}

// animeWorkerKey keys the discovery and refresh loop on its settings and sources; "" while it is off.
func animeWorkerKey(rt *serveRuntime) string {
	if rt.Anime == nil || !rt.Anime.Ready() || !rt.Policy.AnimeBuildSettings().AutoBuild {
		return ""
	}
	sources := map[string]any{}
	for _, k := range autobuild.Required {
		sources[k] = metadataSource(rt.Policy, k)
	}
	key := ""
	if rt.Metadata != nil {
		key = rt.Metadata.tvdbKey + rt.Metadata.tmdbKey
	}
	return keyOf(rt.Policy.AnimeBuildSettings(), sources, key)
}

// animeBuildHealth is a notice while automatic builds are on but the anime sources are missing.
func animeBuildHealth(current func(context.Context) *autobuild.Service) health.Func {
	return func(ctx context.Context) health.Result {
		s := current(ctx)
		st := s.Status()
		switch {
		case st.Ready && st.AutoBuild:
			return health.Result{Type: health.OK, Message: "Anime franchises build automatically"}
		case st.Ready:
			return health.Result{Type: health.OK, Message: "Automatic anime builds are off (metadata.anime.auto_build)"}
		case !st.AutoBuild:
			return health.Result{Type: health.OK, Message: "Automatic anime builds are off"}
		}
		return health.Result{Type: health.Notice, Message: "Anime franchises do not build automatically: " + st.Off}
	}
}
