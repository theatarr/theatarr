package main

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"os"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/indexer/lookup"
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/live"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/metadata/tvdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
)

// wireIndexerLookups gives the indexer service the lookups it needs:
//   - AniList ids for SeaDex (the anibridge mapping): our franchise mappings and recorded ids first, then
//     the anibridge list (metadata.sources.anibridge, fetched at most daily through the gateway and the cache);
//   - for SeaDex's recent feed and titles, AniList ids back to our series seasons and movies (the same two sources)
//     and our titles' names;
//   - episode counts for Torrentio's season packs: our episodes, then TVDB, then TMDb.
//
// Every source is optional; a missing one is skipped and logged once.
func wireIndexerLookups(pol *policy.Policy, st *store.Store, gw *gateway.Gateway, svc *indexer.Service,
	tvc *tvdb.Client, log *slog.Logger) {
	db := lookup.DB{DB: st.Reader()}
	al := []indexer.AniListResolver{db}
	targets := []indexer.AniListMapper{db}
	var have []string
	if src := metadataSource(pol, live.SourceAnibridge); src != nil && src.URL != "" {
		f := &live.Fetcher{Doer: gw, Store: st}
		daily := &lookup.Daily{Load: func(ctx context.Context) (*lookup.Anibridge, error) {
			b, err := f.Get(ctx, live.Request{Source: live.SourceAnibridge, URL: src.URL})
			if err != nil {
				return nil, err
			}
			l := &anime.Lists{}
			if err := l.ParseAnibridge(bytes.NewReader(b)); err != nil {
				return nil, err
			}
			return lookup.NewAnibridge(l), nil
		}}
		al, targets = append(al, daily), append(targets, daily)
		have = append(have, "anibridge")
	}
	counts := []indexer.EpisodeCounter{db}
	if tvc != nil {
		counts = append(counts, lookup.TVDB{C: tvc})
		have = append(have, "tvdb")
	}
	if src := metadataSource(pol, "tmdb"); src != nil && src.URL != "" && src.APIKeyEnv != "" && pol.Getenv(src.APIKeyEnv) != "" {
		tc, err := tmdb.New(tmdb.Options{BaseURL: src.URL, APIKey: pol.Getenv(src.APIKeyEnv),
			Doer:  gatewayDoer{gw: gw, target: gateway.Target{Class: gateway.ClassMetadata, Key: "tmdb"}},
			Cache: &tmdb.SQLCache{Store: st, Source: "tmdb"}})
		if err != nil {
			log.Warn("indexer lookups: tmdb", "err", err)
		} else {
			counts = append(counts, lookup.TMDb{C: tc})
			have = append(have, "tmdb")
		}
	}
	svc.SetMetadata(indexer.Metadata{AniList: indexer.FirstAniList(al...), Episodes: indexer.FirstEpisodeCounts(counts...),
		Targets: indexer.FirstAniListTargets(targets...), Names: db, Torrents: db})
	log.Info("indexer lookups", "database", true, "sources", have)
}

func metadataSource(pol *policy.Policy, key string) *policy.MetadataSource {
	if pol == nil || pol.Metadata == nil {
		return nil
	}
	return pol.Metadata.Sources[key]
}

// wireCacheEvidence gives the indexer service its cache evidence (docs/design/router.md "Cache evidence"): DMM results
// stored per hash and provider, stored evidence read back into every search, and the bounded TorBox check with the
// TorBox clients in provs (read-only, our own key, through the gateway). st may be nil (nothing stored or read).
func wireCacheEvidence(pol *policy.Policy, st *store.Store, svc *indexer.Service, provs map[string]provider.Provider,
	log *slog.Logger) {
	svc.SetEvidence(indexer.NewEvidence(indexer.EvidenceConfig{Store: st, Policy: pol, Checkers: provs, Logger: log}))
	s := pol.CacheEvidenceSettings()
	var tb []string
	for k, p := range provs {
		if p != nil && p.Kind() == provider.KindTorBox {
			tb = append(tb, k)
		}
	}
	log.Info("cache evidence", "ttl", s.TTL, "torbox_check", s.CheckEnabled && len(tb) > 0, "torbox", tb,
		"top", s.CheckTop, "per_hour", s.CheckPerHour)
}

// torboxCheckers builds the read-only TorBox clients whose key is set (theatarr search's cache check).
func torboxCheckers(pol *policy.Policy, d provider.Doer) map[string]provider.Provider {
	out := map[string]provider.Provider{}
	for k, pv := range pol.Providers {
		if pv.Kind != provider.KindTorBox {
			continue
		}
		key := os.Getenv(pv.APIKeyEnv)
		if key == "" {
			continue
		}
		if c, err := provider.New(k, pv, key, d); err == nil {
			out[k] = c
		}
	}
	return out
}

// storedIndexerLookups are the lookups a CLI search (decide -search, router dry-run) gives the indexers: AniList ids,
// targets, names, torrents and episode counts read from the database only, nothing fetched, so the native SeaDex
// indexer can resolve a title's AniList entry as the server's searches do.
func storedIndexerLookups(db *sql.DB) indexer.Metadata {
	l := lookup.DB{DB: db}
	return indexer.Metadata{AniList: l, Episodes: l, Targets: l, Names: l, Torrents: l}
}
