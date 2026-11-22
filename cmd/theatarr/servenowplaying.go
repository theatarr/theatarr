package main

import (
	"context"
	"net/http"
	"sync/atomic"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/nowplaying"
	"github.com/theatarr/theatarr/internal/plex"
	"github.com/theatarr/theatarr/internal/secrets"
	"github.com/theatarr/theatarr/internal/torrents"
)

// serveNowPlaying is the now playing tracker in force (docs/design/now-playing.md), nil while it is off. The web API and
// the library's zurg log followers reach it here.
var serveNowPlaying atomic.Pointer[nowplaying.Tracker]

// nowPlayingSnapshot is the web API's source, with the incidents of the last 24 hours: nil while the tracker is off.
func nowPlayingSnapshot() *nowplaying.Snapshot {
	t := serveNowPlaying.Load()
	if t == nil {
		return nil
	}
	s := t.Snapshot()
	s.Incidents = t.Incidents()
	return &s
}

// nowPlayingLine forwards a zurg log line to the tracker.
func nowPlayingLine(line string) {
	if t := serveNowPlaying.Load(); t != nil {
		t.ZurgLine(line)
	}
}

// nowPlayingKey is the worker's key: library.plex with now_playing on and a token, and what the tracker reads with.
// Secrets only enter it hashed (keyOf).
func nowPlayingKey(rt *serveRuntime) string {
	if rt.Library == nil || rt.Library.pol.Library == nil {
		return ""
	}
	s := rt.Library.set
	if !s.NowPlaying || s.Plex == nil {
		return ""
	}
	tok := rt.Policy.Getenv(s.Plex.TokenEnv)
	if tok == "" {
		return ""
	}
	var rc []string
	if r := s.RcloneRC; r != nil {
		rc = []string{r.URL, rt.Policy.Getenv(r.UsernameEnv), rt.Policy.Getenv(r.PasswordEnv)}
	}
	return keyOf(s.Plex.URL, tok, s.ZurgAPI, s.MountPath, s.LinkPath, s.AllDir, s.Prewarm, s.PrewarmRate,
		s.PrewarmMaxSize, rc)
}

// currentGateway sends each request through the gateway of the generation in force, so a budget change applies
// without restarting the tracker.
type currentGateway struct {
	current func(context.Context) (context.Context, *serveRuntime)
}

func (g currentGateway) Do(ctx context.Context, t gateway.Target, req *http.Request) (*http.Response, error) {
	_, rt := g.current(ctx)
	return rt.Gateway.Do(ctx, t, req)
}

// runNowPlaying runs a tracker on rt's settings until ctx ends.
func (l serveLoops) runNowPlaying(ctx context.Context, rt *serveRuntime) error {
	o, err := l.nowPlayingOptions(ctx, rt)
	if err != nil {
		return err
	}
	t := nowplaying.New(o)
	serveNowPlaying.Store(t)
	defer serveNowPlaying.CompareAndSwap(t, nil)
	s := rt.Library.set
	o.Logger.Info("now playing: following Plex's sessions", "url", (secrets.URLMasker{}).Mask(s.Plex.URL), "zurg_api", s.ZurgAPI != "",
		"rclone_rc", o.RC != nil, "prewarm", s.Prewarm,
		"prewarm_rate", s.PrewarmRate)
	t.Run(ctx)
	return nil
}

// nowPlayingOptions are the tracker's options on rt's settings; the ones a save may change without a restart
// (StopWithMessage, Roots, the gateway) read the generation in force each time.
func (l serveLoops) nowPlayingOptions(ctx context.Context, rt *serveRuntime) (nowplaying.Options, error) {
	s, pol, lb := rt.Library.set, rt.Policy, rt.Library
	gw := currentGateway{current: l.current}
	pc, err := plex.New(gw, s.Plex.URL, pol.Getenv(s.Plex.TokenEnv))
	if err != nil {
		return nowplaying.Options{}, err
	}
	log := l.log.With("component", "nowplaying")
	o := nowplaying.Options{Plex: pc, Store: lb.st, LinkPath: s.LinkPath, MountPath: s.MountPath, AllDir: s.AllDir,
		Prewarm: s.Prewarm, PrewarmRate: s.PrewarmRate, PrewarmMaxSize: s.PrewarmMaxSize, Logger: log}
	if z := newZurgAPI(s, rt.Gateway, log); z != nil {
		z.GW = gw
		o.Zurg = z
	}
	if r := s.RcloneRC; r != nil {
		o.RC = &nowplaying.RC{URL: r.URL, User: pol.Getenv(r.UsernameEnv), Password: pol.Getenv(r.PasswordEnv), GW: gw}
	}
	// A version's roots under the settings in force: versions[k].library and library.versions[k].roots.
	o.Roots = func(k string) []string {
		_, cur := l.current(ctx)
		if cur == nil || cur.Library == nil || cur.Library.pol == nil {
			return nil
		}
		p := cur.Library.pol
		var roots []string
		if v := p.Versions[k]; v != nil && v.Library != "" {
			roots = append(roots, v.Library)
		}
		if p.Library != nil && p.Library.Versions[k] != nil {
			roots = append(roots, p.Library.Versions[k].Roots...)
		}
		return roots
	}
	if l.torrents != nil {
		// Step 1 of the repair of a file that broke while playing: the release on one other account, as the automatic
		// "Add to other providers" adds it; it counts when the account now holds it complete.
		o.Copy = func(ctx context.Context, hash, pk string) (bool, error) {
			m, err := l.torrents(ctx)
			if err != nil {
				return false, err
			}
			out, err := m.Copy(ctx, torrents.CopyPayload{Hash: hash, By: "playing", Auto: true, Providers: []string{pk}})
			if err != nil {
				return false, err
			}
			for _, je := range out {
				if je.Provider == pk && (je.Outcome == torrents.CopyAdded || je.Outcome == torrents.CopyPresent && je.Detail == "") {
					return true, nil
				}
			}
			return false, nil
		}
	}
	// library.plex.stop_with_message of the settings in force: a save turns it on or off without restarting the tracker.
	o.StopWithMessage = func() bool {
		if _, cur := l.current(ctx); cur != nil && cur.Library != nil {
			return cur.Library.set.StopWithMessage
		}
		return s.StopWithMessage
	}
	if s.MountPath != "" {
		o.Mount = func() nowplaying.Mount {
			if lib := lb.current(); lib != nil {
				return lib
			}
			return nil
		}
	}
	return o, nil
}
