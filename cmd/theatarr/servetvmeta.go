package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/metadata/art"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/metadata/tvdb"
	"github.com/theatarr/theatarr/internal/metadata/tvrefresh"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// newServeSeriesMeta builds the series metadata refresh (docs/design/tv-metadata.md): episodes from TVDB (tvc, nil without
// its key), TMDb for series TVDB lacks when metadata.sources.tmdb and its key are set. Both go through the gateway.
func newServeSeriesMeta(pol *policy.Policy, st *store.Store, q *jobs.Queue, gw *gateway.Gateway, tvc *tvdb.Client,
	log *slog.Logger) (*tvrefresh.Service, error) {
	cfg := tvrefresh.Config{Store: st, Policy: pol, Queue: q, Logger: log}
	if tvc != nil {
		cfg.TVDB = tvc
	}
	if src := metadataSource(pol, "tmdb"); src != nil && src.URL != "" && src.APIKeyEnv != "" && pol.Getenv(src.APIKeyEnv) != "" {
		tc, err := tmdb.New(tmdb.Options{BaseURL: src.URL, APIKey: pol.Getenv(src.APIKeyEnv),
			Doer:  gatewayDoer{gw: gw, target: gateway.Target{Class: gateway.ClassMetadata, Key: "tmdb"}},
			Cache: &tmdb.SQLCache{Store: st, Source: "tmdb"}, MinInterval: 250 * time.Millisecond})
		if err != nil {
			return nil, err
		}
		cfg.TMDb = tc
	}
	return tvrefresh.New(cfg)
}

// titleRefresher is the title refresh behind POST titles/{id}/refresh and the arr Refresh commands: TMDb art and
// details (meta.art) for every title, and for a series also its episodes (meta.series), whose job it answers with.
type titleRefresher struct {
	*art.Service
	tv *tvrefresh.Service
	st *store.Store
}

func (r *titleRefresher) Refresh(ctx context.Context, titleID int64) (int64, error) {
	if r.tv == nil || !r.tv.Enabled() {
		return r.Service.Refresh(ctx, titleID)
	}
	var kind string
	err := r.st.Reader().QueryRowContext(ctx, `SELECT kind FROM title WHERE id = ?`, titleID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, art.ErrNoTitle
	}
	if err != nil {
		return 0, err
	}
	if kind != string(policy.KindSeries) {
		return r.Service.Refresh(ctx, titleID)
	}
	if _, err := r.Service.Refresh(ctx, titleID); err != nil && !errors.Is(err, art.ErrNoTMDb) {
		return 0, err
	}
	return r.tv.Refresh(ctx, titleID)
}
