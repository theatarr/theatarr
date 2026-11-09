package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/metadata/anime/playlists"
	"github.com/theatarr/theatarr/internal/metadata/tvrefresh"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/shadow"
)

// serveLoops is what the serve workers run on: the startup services, which resolve the settings generation in force
// for each operation, and current, which captures that generation and returns its services.
type serveLoops struct {
	router  *router.Router
	sync    *providerSync
	shadow  *shadow.Runner
	current func(context.Context) (context.Context, *serveRuntime)
	log     *slog.Logger
}

// Workers are keyed on the settings each loop schedules with; a save that leaves a key alone leaves the loop running.
func (l serveLoops) workers() []serveWorker {
	return []serveWorker{
		// The periodic VPN egress check (egress.interval), on while an egress section exists: a section added, removed
		// or re-timed after startup starts, stops or restarts it. Each pass checks the gateway in force.
		egressWorker(l.current),
		// The library's import pass, relink sweep, dangling report, naming sync and zurg log watch
		// (library.poll_interval, relink_interval, dangling_every, mount.zurg_log...).
		{name: "library", key: func(rt *serveRuntime) string {
			if rt.Library == nil || rt.Library.pol.Library == nil {
				return ""
			}
			s := rt.Library.set
			return keyOf(s.PollInterval, s.RelinkInterval, s.DanglingEvery, s.ZurgLog, s.ZurgLogURL, s.ZurgLogInterval)
		}, run: func(ctx context.Context, rt *serveRuntime, _ string) error {
			rt.Library.Run(ctx)
			return nil
		}},
		// The provider registry sync schedule (provider_sync.interval and the providers with a key).
		{name: "provider-sync", key: func(rt *serveRuntime) string {
			if rt.Providers == nil || len(rt.Providers.keys) == 0 {
				return ""
			}
			return keyOf(rt.Providers.interval, rt.Providers.keys)
		}, run: func(ctx context.Context, _ *serveRuntime, _ string) error {
			l.sync.run(ctx)
			return nil
		}},
		// The download poll (router.poll_interval); each pass polls through the router in force.
		{name: "router-poll", key: func(rt *serveRuntime) string {
			return rt.Router.Settings().PollInterval.String()
		}, run: func(ctx context.Context, rt *serveRuntime, _ string) error {
			l.router.RunEvery(ctx, rt.Router.Settings().PollInterval)
			return nil
		}},
		// The wanted search loop (router.wanted), on while a version is active.
		{name: "wanted", key: func(rt *serveRuntime) string {
			w := rt.Policy.WantedSettings()
			if !w.Enabled || rt.Acquirer == nil {
				return ""
			}
			return w.Interval.String()
		}, run: func(ctx context.Context, rt *serveRuntime, _ string) error {
			rt.Acquirer.Run(ctx, 5*time.Minute)
			return nil
		}},
		// arr.import and shadow.rss scheduling (shadow.import_interval, import.arr). The first start also buckets the
		// stored grabs again (shadow.Runner.Run).
		{name: "shadow", key: func(rt *serveRuntime) string {
			return keyOf(rt.Policy.ShadowSettings().ImportInterval, rt.Policy.Import != nil && len(rt.Policy.Import.Arr) > 0)
		}, run: func(ctx context.Context, _ *serveRuntime, prev string) error {
			if prev == "" {
				l.shadow.Run(ctx, time.Minute, 3*time.Minute)
			} else {
				l.shadow.Schedule(ctx, time.Minute, 3*time.Minute)
			}
			return nil
		}},
		// Watch-order playlist syncs (anime.playlists.interval), each with the syncer in force.
		{name: "playlists", key: func(rt *serveRuntime) string {
			return rt.Policy.PlaylistSettings().Interval.String()
		}, run: func(ctx context.Context, rt *serveRuntime, _ string) error {
			l.runPlaylists(ctx, rt.Policy.PlaylistSettings().Interval, rt.Playlists)
			return nil
		}},
		// meta.art scheduling (metadata.art, metadata.sources.tmdb, ui.images).
		// It returns at once, logging so, while TMDb is off.
		{name: "art", key: func(rt *serveRuntime) string {
			if rt.Metadata == nil {
				return ""
			}
			return rt.Metadata.artKey
		}, run: func(ctx context.Context, rt *serveRuntime, _ string) error {
			rt.Metadata.art.Run(ctx)
			return nil
		}},
		// The series metadata refresh (metadata.series and its sources).
		{name: "series-metadata", key: func(rt *serveRuntime) string {
			if rt.Metadata == nil {
				return ""
			}
			return rt.Metadata.tvKey
		}, run: func(ctx context.Context, rt *serveRuntime, _ string) error {
			rt.Metadata.tv.Run(ctx)
			return nil
		}},
		// TVDB's hourly /updates read (metadata.sources.tvdb).
		{name: "tvdb-updates", key: func(rt *serveRuntime) string {
			if rt.Metadata == nil || rt.Metadata.tvc == nil {
				return ""
			}
			return rt.Metadata.tvdbKey
		}, run: func(ctx context.Context, rt *serveRuntime, _ string) error {
			runTVDBUpdates(ctx, rt.Metadata.tvc, func(ctx context.Context) *tvrefresh.Service {
				if _, rt := l.current(ctx); rt.Metadata != nil {
					return rt.Metadata.tv
				}
				return rt.Metadata.tv
			}, l.log)
			return nil
		}},
		// Radarr collections and TMDb lists (metadata.lists, metadata.sources.tmdb). It refreshes at once when TMDb
		// was off or its source changed, not when only the interval did.
		{name: "movie-intake", key: func(rt *serveRuntime) string {
			if rt.Metadata == nil || rt.Metadata.intake == nil {
				return ""
			}
			return rt.Metadata.tmdbKey + " every " + rt.Metadata.listInterval.String()
		}, run: func(ctx context.Context, rt *serveRuntime, prev string) error {
			source, _, _ := strings.Cut(prev, " every ")
			runMovieIntake(ctx, rt.Metadata.listInterval, source != rt.Metadata.tmdbKey,
				func(ctx context.Context) *catalog.MovieIntake {
					if _, rt := l.current(ctx); rt.Metadata != nil {
						return rt.Metadata.intake
					}
					return nil
				}, l.log)
			return nil
		}},
		// The now playing tracker (library.plex.now_playing, docs/design/now-playing.md): Plex's sessions and websocket,
		// zurg's /stats, the mount's rclone rc and the next-episode pre-warm. Read only; it needs no library.enabled.
		{name: "nowplaying", key: nowPlayingKey, run: func(ctx context.Context, rt *serveRuntime, _ string) error {
			return l.runNowPlaying(ctx, rt)
		}},
		// The cardigann definitions refresh (cardigann.definitions). A changed repository or branch is read at once.
		{name: "cardigann", key: func(rt *serveRuntime) string {
			return cardigannWorkerKey(rt.Policy)
		}, run: func(ctx context.Context, rt *serveRuntime, prev string) error {
			c := serveCardigann.Load()
			if c == nil {
				return fmt.Errorf("cardigann.definitions is set but the catalog is not ready")
			}
			was, _, _ := strings.Cut(prev, " every ")
			now, _, _ := strings.Cut(cardigannWorkerKey(rt.Policy), " every ")
			if prev != "" && was != now {
				_ = c.store.Refresh(ctx) // logged and kept in the catalog status by the store
			}
			c.Run(ctx)
			return nil
		}},
	}
}

// egressWorker checks every egress path at its start and then every egress.interval, each time through the gateway
// of the generation in force (a publication's own immediate check is serveEgressChecks').
func egressWorker(current func(context.Context) (context.Context, *serveRuntime)) serveWorker {
	return serveWorker{name: "egress", key: func(rt *serveRuntime) string {
		if rt.Gateway == nil || rt.Policy == nil || rt.Policy.Egress == nil {
			return ""
		}
		return egressInterval(rt.Policy.Egress).String()
	}, run: func(ctx context.Context, rt *serveRuntime, _ string) error {
		t := time.NewTicker(egressInterval(rt.Policy.Egress))
		defer t.Stop()
		for {
			checkCtx, now := current(ctx)
			gw := rt.Gateway
			if now != nil && now.Gateway != nil {
				gw = now.Gateway
			}
			gw.CheckEgress(checkCtx)
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
			}
		}
	}}
}

// warnVPNGuardOff logs once at startup that no egress section restricts requests.
func warnVPNGuardOff(pol *policy.Policy, log *slog.Logger) {
	if pol == nil || pol.Egress == nil {
		log.Warn("VPN guard off: no egress section; requests are not restricted by VPN status")
	}
}

func egressInterval(e *policy.Egress) time.Duration {
	if d := e.Interval.D(); d > 0 {
		return d
	}
	return policy.DefaultEgressInterval
}

func (l serveLoops) runPlaylists(ctx context.Context, interval time.Duration, fallback *playlists.Syncer) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			current := fallback
			syncCtx, rt := l.current(ctx)
			if rt.Playlists != nil {
				current = rt.Playlists
			}
			if current == nil {
				continue
			}
			if _, err := current.Sync(syncCtx, "", false); err != nil {
				l.log.Warn("playlists: sync failed", "err", err)
			}
		}
	}
}
