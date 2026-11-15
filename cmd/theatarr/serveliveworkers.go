package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/acquire/rss"
	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/metadata/anime/playlists"
	"github.com/theatarr/theatarr/internal/metadata/tvrefresh"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/tasks"
)

// serveLoops is what the serve workers run on: the startup services, which resolve the settings generation in force
// for each operation, and current, which captures that generation and returns its services.
type serveLoops struct {
	router *router.Router
	sync   *providerSync
	rss    *rss.Runner
	tasks  *tasks.Table
	// acquirer runs the search round with the acquirer in force.
	acquirer *serveAcquirer
	current  func(context.Context) (context.Context, *serveRuntime)
	log      *slog.Logger
}

// Workers are keyed on the settings each loop schedules with; a save that leaves a key alone leaves the loop running.
func (l serveLoops) workers() []serveWorker {
	return []serveWorker{
		// The periodic VPN egress check (egress.interval), on while an egress section exists: a section added, removed
		// or re-timed after startup starts, stops or restarts it. Each pass checks the gateway in force.
		egressWorker(l.tasks, l.current),
		// The library's import pass, relink sweep, dangling report, naming sync and zurg log watch
		// (library.poll_interval, relink_interval, dangling_every, mount.zurg_log...).
		{name: "library", key: func(rt *serveRuntime) string {
			if rt.Library == nil || rt.Library.pol.Library == nil {
				return ""
			}
			s := rt.Library.set
			return keyOf(s.PollInterval, s.RelinkInterval, s.DanglingEvery, s.ZurgLog, s.ZurgLogURL, s.ZurgLogInterval)
		}, run: func(ctx context.Context, rt *serveRuntime, _ string) error {
			rt.Library.Run(ctx, l.tasks)
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
		// The search schedule's rounds (internal/acquire Round), while a library is Live and Pause is off; the first
		// round 5 minutes after start, or at once when Pause is turned off (the due rows waited for it).
		{name: "wanted", key: wantedWorkerKey, run: func(ctx context.Context, rt *serveRuntime, prev string) error {
			l.runWanted(ctx, rt, prev)
			return nil
		}},
		// shadow.rss scheduling: always on; the schedule follows each generation's shadow.rss, rss_interval and
		// libraries by itself.
		{name: "rss", key: func(*serveRuntime) string { return "on" }, run: func(ctx context.Context, _ *serveRuntime, _ string) error {
			l.rss.Schedule(ctx, 3*time.Minute)
			return nil
		}},
		// Watch-order playlist syncs (anime.playlists.interval), each with the syncer in force.
		{name: "playlists", key: func(rt *serveRuntime) string {
			return rt.Policy.PlaylistSettings().Interval.String()
		}, run: func(ctx context.Context, rt *serveRuntime, _ string) error {
			l.runPlaylists(ctx, rt.Policy.PlaylistSettings().Interval, rt.Playlists)
			return nil
		}},
		// Anime franchise discovery and refresh (metadata.anime and the anime metadata sources); off while auto_build is
		// off or a source is missing.
		{name: "anime-build", key: animeWorkerKey, run: func(ctx context.Context, rt *serveRuntime, _ string) error {
			rt.Anime.Run(ctx)
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
			rt.Metadata.art.Run(ctx, l.tasks, func(ctx context.Context) error {
				// Each run on the service in force; dropped while TMDb is off.
				ctx, now := l.current(ctx)
				a := rt.Metadata.art
				if now != nil && now.Metadata != nil {
					a = now.Metadata.art
				}
				if !a.Enabled() {
					return nil
				}
				_, err := a.RunBatch(ctx)
				return err
			})
			return nil
		}},
		// The series metadata refresh (metadata.series and its sources).
		{name: "series-metadata", key: func(rt *serveRuntime) string {
			if rt.Metadata == nil {
				return ""
			}
			return rt.Metadata.tvKey
		}, run: func(ctx context.Context, rt *serveRuntime, _ string) error {
			// Each run on the service in force; dropped while the refresh is off.
			current := func(ctx context.Context) (context.Context, *tvrefresh.Service) {
				ctx, now := l.current(ctx)
				if now != nil && now.Metadata != nil {
					return ctx, now.Metadata.tv
				}
				return ctx, rt.Metadata.tv
			}
			rt.Metadata.tv.Run(ctx, l.tasks, func(ctx context.Context) error {
				ctx, s := current(ctx)
				if !s.Enabled() {
					return nil
				}
				_, err := s.Sweep(ctx)
				return err
			}, func(ctx context.Context) error {
				ctx, s := current(ctx)
				if !s.Enabled() {
					return nil
				}
				return s.RefreshScene(ctx)
			})
			return nil
		}},
		// TVDB's hourly /updates read (metadata.sources.tvdb).
		{name: "tvdb-updates", key: func(rt *serveRuntime) string {
			if rt.Metadata == nil || rt.Metadata.tvc == nil {
				return ""
			}
			return rt.Metadata.tvdbKey
		}, run: func(ctx context.Context, rt *serveRuntime, _ string) error {
			runTVDBUpdates(ctx, l.tasks, rt.Metadata.tvc, func(ctx context.Context) *tvrefresh.Service {
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
			runMovieIntake(ctx, l.tasks, rt.Metadata.listInterval, source != rt.Metadata.tmdbKey,
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

// egressWorker checks every egress path at its start and then every egress.interval (task egress.check), each time
// through the gateway of the generation in force (a publication's own immediate check is serveEgressChecks').
func egressWorker(tb *tasks.Table, current func(context.Context) (context.Context, *serveRuntime)) serveWorker {
	return serveWorker{name: "egress", key: func(rt *serveRuntime) string {
		if rt.Gateway == nil || rt.Policy == nil || rt.Policy.Egress == nil {
			return ""
		}
		return egressInterval(rt.Policy.Egress).String()
	}, run: func(ctx context.Context, rt *serveRuntime, _ string) error {
		tb.Loop(ctx, tasks.Task{Key: "egress.check", Name: "Check the VPN egress", Every: egressInterval(rt.Policy.Egress),
			Run: func(ctx context.Context) error {
				checkCtx, now := current(ctx)
				gw := rt.Gateway
				if now != nil && now.Gateway != nil {
					gw = now.Gateway
				}
				gw.CheckEgress(checkCtx)
				return nil
			}}, 0)
		return nil
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

// runPlaylists syncs the watch-order playlists every interval (task anime.playlists, listed while playlists are set
// up), each time with the syncer in force.
func (l serveLoops) runPlaylists(ctx context.Context, interval time.Duration, fallback *playlists.Syncer) {
	tb := l.tasks
	if fallback == nil {
		tb = nil
	}
	tb.Loop(ctx, tasks.Task{Key: "anime.playlists", Name: "Sync watch-order playlists", Every: interval,
		Run: func(ctx context.Context) error {
			current := fallback
			syncCtx, rt := l.current(ctx)
			if rt.Playlists != nil {
				current = rt.Playlists
			}
			if current == nil {
				return nil
			}
			_, err := current.Sync(syncCtx, "", false)
			return err
		}}, interval)
}

// runWanted runs the search round's task every acquire.RoundEvery until ctx ends, the first after 5 minutes, or at
// once when Pause was just turned off (the due rows waited for it). While Pause is on the task is listed as paused.
func (l serveLoops) runWanted(ctx context.Context, rt *serveRuntime, prev string) {
	task := tasks.Task{Key: acquire.TaskWanted, Name: "Search everything wanted", Every: acquire.RoundEvery, Inbox: true}
	if wantedWorkerKey(rt) == "paused" {
		task.Paused = true
		defer l.tasks.Register(ctx, task)()
		<-ctx.Done()
		return
	}
	rt.Acquirer.LogSchedule()
	delay := 5 * time.Minute
	if prev == "paused" {
		delay = 0
	}
	task.Next, task.Run = time.Now().Add(delay), l.acquirer.Round
	defer l.tasks.Register(ctx, task)()
	t := time.NewTimer(delay)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			start := time.Now()
			_, _ = l.tasks.Run(ctx, task)
			t.Reset(time.Until(start.Add(acquire.RoundEvery)))
		}
	}
}

// wantedWorkerKey is the search round worker's key: "" with no acquirer or no Live library, "paused" while Pause is
// on (the worker waits, so turning Pause off is a change from "paused"), "on" otherwise.
func wantedWorkerKey(rt *serveRuntime) string {
	switch {
	case rt.Acquirer == nil || rt.Policy == nil || len(rt.Policy.ActiveVersions()) == 0:
		return ""
	case !rt.Policy.RouterSettings().Enabled:
		return "paused"
	}
	return "on"
}
