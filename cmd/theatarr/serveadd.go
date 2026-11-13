package main

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/web"
)

// newServeAdd builds what search and add use (docs/design/ui.md): the acquirer, which searches only active
// versions, and a TMDb client for the search box and the add's names and ids, through the gateway and the tmdb
// budget, with the SQL response cache. Without metadata.sources.tmdb or its key, search answers from the library only.
func newServeAdd(pol *policy.Policy, st *store.Store, gw *gateway.Gateway, ac web.Acquirer,
	log *slog.Logger) (web.AddConfig, error) {
	cfg := web.AddConfig{Acquirer: ac}
	src := metadataSource(pol, "tmdb")
	if src == nil || src.URL == "" || src.APIKeyEnv == "" || pol.Getenv(src.APIKeyEnv) == "" {
		log.Info("search: TMDb off (no metadata.sources.tmdb key); library results only")
		return cfg, nil
	}
	tc, err := tmdb.New(tmdb.Options{BaseURL: src.URL, APIKey: pol.Getenv(src.APIKeyEnv),
		Doer:  gatewayDoer{gw: gw, target: gateway.Target{Class: gateway.ClassMetadata, Key: "tmdb"}},
		Cache: &tmdb.SQLCache{Store: st, Source: "tmdb"},
		// A typed search sends one request per settled query (the UI debounces; answers are cached a day).
		MinInterval: 100 * time.Millisecond})
	if err != nil {
		return cfg, err
	}
	cfg.TMDb = tc
	return cfg, nil
}

// gatewayDoer sends a request through the gateway for one target (tmdb.Doer).
type gatewayDoer struct {
	gw     *gateway.Gateway
	target gateway.Target
}

func (d gatewayDoer) Do(r *http.Request) (*http.Response, error) {
	return d.gw.Do(r.Context(), d.target, r)
}
