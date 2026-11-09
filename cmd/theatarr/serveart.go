package main

import (
	"log/slog"
	"path/filepath"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/metadata/art"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// newServeArt builds title art and metadata (docs/design/artwork.md): the meta.art job when metadata.sources.tmdb and its
// key are set, and the /img cache when metadata.sources.tmdb-images is. Both go through the gateway only. The cache
// lives in ui.images.dir, by default next to the database (/data/images in the container).
func newServeArt(pol *policy.Policy, st *store.Store, q *jobs.Queue, gw *gateway.Gateway, dbPath string,
	log *slog.Logger) (*art.Service, error) {
	cfg := art.Config{Store: st, Queue: q, Settings: pol.ArtSettings(), Logger: log}
	if src := metadataSource(pol, "tmdb"); src != nil && src.URL != "" && src.APIKeyEnv != "" && pol.Getenv(src.APIKeyEnv) != "" {
		tc, err := tmdb.New(tmdb.Options{BaseURL: src.URL, APIKey: pol.Getenv(src.APIKeyEnv),
			Doer: gatewayDoer{gw: gw, target: gateway.Target{Class: gateway.ClassMetadata, Key: "tmdb"}},
			// The find lookups use the response cache; the details calls skip it (title_meta is their cache).
			Cache: &tmdb.SQLCache{Store: st, Source: "tmdb"},
			// Well under the tmdb budget, which the indexer lookups and imports share.
			MinInterval: 250 * time.Millisecond})
		if err != nil {
			return nil, err
		}
		cfg.TMDb = tc
	}
	if src := metadataSource(pol, policy.TMDbImagesSource); src != nil && src.URL != "" {
		set := pol.ImageSettings(filepath.Dir(dbPath))
		im, err := art.NewImages(art.ImagesConfig{Settings: set, BaseURL: src.URL, Logger: log,
			Doer: gatewayDoer{gw: gw, target: gateway.Target{Class: gateway.ClassMetadata, Key: policy.TMDbImagesSource,
				MaxBytes: art.MaxImageBytes}}})
		if err != nil {
			return nil, err
		}
		cfg.Images = im
		log.Info("image cache", "dir", set.Dir, "max_bytes", set.MaxBytes, "original", set.Original)
	} else {
		log.Info("image cache off: no metadata.sources." + policy.TMDbImagesSource + "; generated art only")
	}
	return art.New(cfg)
}
