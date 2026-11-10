package main

import (
	"time"

	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/web"
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

// serveMovieIntake is what the Collections page uses of a generation's movie intake (web.MovieIntake): the intake's
// one-collection refresh, and its TMDb client for the collection search and lookup.
type serveMovieIntake struct {
	*catalog.MovieIntake
	*tmdb.Client
}

// webMovieIntake is mi for the web layer; nil (no interface value) when TMDb is not set up.
func webMovieIntake(mi *catalog.MovieIntake) web.MovieIntake {
	if mi == nil {
		return nil
	}
	c, ok := mi.TMDb.(*tmdb.Client)
	if !ok {
		return nil
	}
	return serveMovieIntake{MovieIntake: mi, Client: c}
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
