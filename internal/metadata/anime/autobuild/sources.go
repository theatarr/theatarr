// Package autobuild builds anime franchises without a person running `theatarr anime build`
// (docs/design/anime.md "Automatic builds"): the live sources the service and the CLI share, seed discovery from a
// TVDB series to a franchise definition, the anime_build job, and the discovery and refresh loops of theatarr serve.
//
// Builds go through anime.Build and animestore.Save unchanged: a refresh that drops episodes or moves linked mappings
// is held for review as before. Builds run one at a time (the AniList budget is 30 requests a minute).
package autobuild

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animestore"
	"github.com/theatarr/theatarr/internal/metadata/anime/live"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Required are the metadata.sources keys a live build needs. Every request goes through the gateway, which refuses
// a source the policy does not list, so the in-code default URLs do not make an entry optional.
var Required = []string{live.SourceAniList, live.SourceKitsu, live.SourceXEM, live.SourceScudLee, live.SourceFribb,
	live.SourceAnibridge, live.SourceManami}

// Missing lists the required metadata.sources entries pol lacks.
func Missing(pol *policy.Policy) []string {
	var out []string
	for _, k := range Required {
		if sourceURL(pol, k) == "" {
			out = append(out, k)
		}
	}
	return out
}

func sourceURL(pol *policy.Policy, key string) string {
	if pol != nil && pol.Metadata != nil && pol.Metadata.Sources[key] != nil {
		return pol.Metadata.Sources[key].URL
	}
	return ""
}

// NotConfigured is LiveSources' error when required sources are missing.
type NotConfigured struct{ Missing []string }

func (e *NotConfigured) Error() string {
	return fmt.Sprintf("the policy needs metadata.sources entries for %s (see docs/design/anime.md)", strings.Join(e.Missing, ", "))
}

// SourceOptions are what LiveSources wires besides the policy.
type SourceOptions struct {
	Store *store.Store
	// Doer sends every request: the gateway.
	Doer live.Doer
	// TVDB is the TVDB v4 client, or nil without its key: TVDB episodes then come from the imported Sonarr only.
	TVDB anime.TVDB
	// SonarrInstance limits the imported Sonarr episodes (the "in Sonarr" check) to one instance; empty means any.
	SonarrInstance string
	Logger         *slog.Logger
}

// Live is a live source set: the paced, cached fetcher and the adapters. The mapping lists load on first use and
// are kept, parsed, for a while (Lists).
type Live struct {
	Fetcher *live.Fetcher
	URLs    live.URLs
	base    anime.Sources
}

// LiveSources wires the live adapters: AniList, Kitsu, TheXEM and the lists through the gateway, TVDB from the TVDB
// client with the imported Sonarr episodes behind it, and TMDb when the policy has its source and key. It returns a
// *NotConfigured error when a required source is missing.
func LiveSources(pol *policy.Policy, o SourceOptions) (*Live, error) {
	if m := Missing(pol); len(m) > 0 {
		return nil, &NotConfigured{Missing: m}
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	f := &live.Fetcher{Doer: o.Doer, Store: o.Store}
	urls := live.URLs{AniList: sourceURL(pol, live.SourceAniList), ScudLee: sourceURL(pol, live.SourceScudLee),
		Fribb: sourceURL(pol, live.SourceFribb), Anibridge: sourceURL(pol, live.SourceAnibridge),
		Manami: sourceURL(pol, live.SourceManami)}
	db := animestore.SonarrTVDB{DB: o.Store, Instance: o.SonarrInstance}
	l := &Live{Fetcher: f, URLs: urls, base: anime.Sources{AniList: live.AniList{F: f, URL: urls.AniList},
		Kitsu: live.Kitsu{F: f}, TVDB: db, XEM: live.XEM{F: f}, Library: db}}
	if o.TVDB != nil {
		l.base.TVDB = anime.TVDBChain{o.TVDB, db}
	} else {
		log.Info("anime: no TVDB key; TVDB episodes come from the imported Sonarr")
	}
	if src := pol.Metadata.Sources["tmdb"]; src != nil && src.URL != "" && src.APIKeyEnv != "" {
		if key := pol.Getenv(src.APIKeyEnv); key != "" {
			tc, err := tmdb.New(tmdb.Options{BaseURL: src.URL, APIKey: key,
				Doer:  targetDoer{d: o.Doer, t: gateway.Target{Class: gateway.ClassMetadata, Key: "tmdb"}},
				Cache: &tmdb.SQLCache{Store: o.Store, Source: "tmdb"}})
			if err != nil {
				return nil, err
			}
			l.base.Movies = live.TMDbMovies{C: tc}
		}
	}
	if l.base.Movies == nil {
		log.Info("anime: no TMDb key; TMDb ids are not checked")
	}
	return l, nil
}

// Sources returns the source set with the mapping lists loaded.
func (l *Live) Sources(ctx context.Context) (*anime.Sources, error) {
	lists, err := l.Lists(ctx)
	if err != nil {
		return nil, err
	}
	src := l.base
	src.Lists = lists
	return &src, nil
}

// AniList is the AniList adapter.
func (l *Live) AniList() anime.AniList { return l.base.AniList }

// Lists returns the parsed mapping lists. Parsing them takes a while and much memory, so they are kept for
// listsFresh (the lists' own daily cache) and dropped after listsIdle unused (ReleaseIdle).
func (l *Live) Lists(ctx context.Context) (*anime.Lists, error) {
	return parsed.get(ctx, l.URLs, func(ctx context.Context) (*anime.Lists, error) { return live.Lists(ctx, l.Fetcher, l.URLs) })
}

const (
	listsFresh = 24 * time.Hour
	listsIdle  = 30 * time.Minute
)

// listCache keeps one parsed list set for the whole process, so a settings generation does not parse it again.
type listCache struct {
	mu       sync.Mutex
	urls     live.URLs
	lists    *anime.Lists
	byTVDB   map[int][]*anime.ScudLeeRec
	at, used time.Time
	now      func() time.Time
}

var parsed = &listCache{now: time.Now}

func (c *listCache) get(ctx context.Context, urls live.URLs, load func(context.Context) (*anime.Lists, error)) (*anime.Lists, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.lists != nil && c.urls == urls && now.Sub(c.at) < listsFresh {
		c.used = now
		return c.lists, nil
	}
	l, err := load(ctx)
	if err != nil {
		return nil, err
	}
	c.urls, c.lists, c.byTVDB, c.at, c.used = urls, l, scudLeeByTVDB(l), now, now
	return l, nil
}

// index returns the ScudLee records by TVDB id of the cached lists l (built when they were loaded).
func (c *listCache) index(l *anime.Lists) map[int][]*anime.ScudLeeRec {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lists == l && c.byTVDB != nil {
		return c.byTVDB
	}
	return scudLeeByTVDB(l)
}

// ReleaseIdle drops the parsed lists when nothing used them for a while.
func ReleaseIdle() {
	parsed.mu.Lock()
	defer parsed.mu.Unlock()
	if parsed.lists != nil && parsed.now().Sub(parsed.used) >= listsIdle {
		parsed.lists, parsed.byTVDB = nil, nil
	}
}

// scudLeeByTVDB indexes the ScudLee records by TVDB id, in document order.
func scudLeeByTVDB(l *anime.Lists) map[int][]*anime.ScudLeeRec {
	out := map[int][]*anime.ScudLeeRec{}
	if l == nil {
		return out
	}
	for _, adb := range l.ScudLeeOrder {
		r := l.ScudLee[adb]
		if r != nil && r.TVDBID != nil && *r.TVDBID > 0 {
			out[*r.TVDBID] = append(out[*r.TVDBID], r)
		}
	}
	return out
}

// targetDoer sends TMDb's requests through the gateway for one target (tmdb.Doer).
type targetDoer struct {
	d live.Doer
	t gateway.Target
}

func (d targetDoer) Do(r *http.Request) (*http.Response, error) { return d.d.Do(r.Context(), d.t, r) }
