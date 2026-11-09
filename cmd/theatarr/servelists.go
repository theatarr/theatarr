package main

import (
	"time"

	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

func newServeMovieIntake(pol *policy.Policy, st *store.Store, gw *gateway.Gateway) (*catalog.MovieIntake, error) {
	src := metadataSource(pol, "tmdb")
	if src == nil || src.URL == "" || src.APIKeyEnv == "" || pol.Getenv(src.APIKeyEnv) == "" {
		return nil, nil
	}
	c, err := tmdb.New(tmdb.Options{BaseURL: src.URL, APIKey: pol.Getenv(src.APIKeyEnv),
		Doer:  gatewayDoer{gw: gw, target: gateway.Target{Class: gateway.ClassMetadata, Key: "tmdb"}},
		Cache: &tmdb.SQLCache{Store: st, Source: "tmdb"}, MinInterval: 250 * time.Millisecond})
	if err != nil {
		return nil, err
	}
	return &catalog.MovieIntake{Store: st, Policy: pol, TMDb: c}, nil
}

// newServeViewTMDb is the arr views' TMDb client (movie lookup by name, the anime guess of new titles), through the
// gateway and the tmdb budget with the SQL response cache. nil without metadata.sources.tmdb or its key: lookups
// answer from the catalog and adds decide anime from the mapping data only.
func newServeViewTMDb(pol *policy.Policy, st *store.Store, gw *gateway.Gateway) (arrview.TMDb, error) {
	src := metadataSource(pol, "tmdb")
	if src == nil || src.URL == "" || src.APIKeyEnv == "" || pol.Getenv(src.APIKeyEnv) == "" {
		return nil, nil
	}
	c, err := tmdb.New(tmdb.Options{BaseURL: src.URL, APIKey: pol.Getenv(src.APIKeyEnv),
		Doer:  gatewayDoer{gw: gw, target: gateway.Target{Class: gateway.ClassMetadata, Key: "tmdb"}},
		Cache: &tmdb.SQLCache{Store: st, Source: "tmdb"}, MinInterval: 100 * time.Millisecond})
	if err != nil {
		return nil, err
	}
	return c, nil
}
