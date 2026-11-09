package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/plex"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/secrets"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

// serveLibrary runs the library in theatarr serve: imports of ready downloads every poll interval and as soon as one
// turns ready, the relink sweep,
// and a daily naming sync and trash purge (docs/design/library.md).
type serveLibrary struct {
	lib    *library.Library
	pol    *policy.Policy
	st     *store.Store
	log    *slog.Logger
	set    policy.LibrarySettings
	scan   library.Scanner
	gw     library.ZurgLogDoer
	engine *decide.Engine
	// breakerSeen is when the mount breaker last opened, as logged (backOff).
	breakerSeen time.Time
	manager     *settings.RuntimeCoordinator
	// readyWake, when set, wakes the import pass: the router signals it each time it records a download as ready.
	// It belongs to serve, not to a settings generation, so a router or library rebuilt by a save keeps it.
	readyWake <-chan struct{}
}

// readyRecheck is how soon the next import pass runs while a download that turned ready moments ago waits for its
// release to show on the mount (library.ReadyRecheckWindow).
var readyRecheck = 2 * time.Second

// newServeLibrary builds one generation's library service. Its mount metrics are not registered here: serve registers
// them once, against the active generation's library (serveLibrary.current).
func newServeLibrary(pol *policy.Policy, st *store.Store, q *jobs.Queue, gw *gateway.Gateway, log *slog.Logger,
	engine *decide.Engine, helper *library.MountHelper,
	onImported func(ctx context.Context, downloadID int64)) (*serveLibrary, error) {
	s := &serveLibrary{pol: pol, st: st, log: log.With("component", "library"), set: pol.LibrarySettings(),
		gw: gw, engine: engine}
	if p := s.set.Plex; p != nil {
		if tok := pol.Getenv(p.TokenEnv); tok != "" {
			c, err := plex.New(gw, p.URL, tok)
			if err != nil {
				return nil, err
			}
			s.scan = c
		} else {
			s.log.Warn("library.plex is set but its token is not; no scans", "env", p.TokenEnv)
		}
	}
	s.lib = library.New(library.Options{Store: st, Policy: pol, Engine: engine, Queue: q, Logger: s.log, Formats: s.formats,
		Matched: matchedRenameFormats(pol, s.decider), OnImported: onImported,
		Zurg: newZurgAPI(s.set, gw, s.log), MountHelper: helper})
	return s, nil
}

// newZurgAPI is zurg's read-only API client (library.mount.zurg_api) through the gateway; nil when it is not set.
func newZurgAPI(set policy.LibrarySettings, gw *gateway.Gateway, log *slog.Logger) *library.ZurgAPI {
	if set.ZurgAPI == "" || gw == nil {
		return nil
	}
	log.Info("library: reading zurg's API", "url", (secrets.URLMasker{}).Mask(set.ZurgAPI), "interval", set.ZurgAPIInterval,
		"repair_grace", set.ZurgAPIRepairGrace)
	return &library.ZurgAPI{URL: set.ZurgAPI, GW: gw, AllDir: set.AllDir, Interval: set.ZurgAPIInterval,
		RepairGrace: set.ZurgAPIRepairGrace, Logger: log}
}

// decider returns the engine shared with the acquirer (from_arr profiles were read GET-only at startup).
func (s *serveLibrary) decider() *decide.Engine {
	return s.engine
}

// formats decides the release title again: for downloads routed without their matched formats.
func (s *serveLibrary) formats(ctx context.Context, version string, titleID int64, release string) []string {
	return decideRenameFormats(s.st, s.pol, s.decider())(ctx, version, titleID, release)
}

// LogState logs the gate loudly at start.
func (s *serveLibrary) LogState() {
	g := s.lib.Gate()
	switch {
	case !g.Enabled():
		s.log.Warn("LIBRARY DISABLED: " + g.Summary())
	default:
		s.log.Warn("LIBRARY ENABLED: " + g.Summary())
	}
}

func (s *serveLibrary) RegisterJobs(w *jobs.Worker) {
	if s.manager != nil {
		w.Handle(library.ScanJobKind, func(ctx context.Context, j *jobs.Job) error {
			ctx, current := s.forOperation(ctx)
			return current.lib.HandleScan(ctx, j, current.scan)
		})
		w.Handle(library.AdoptJobKind, func(ctx context.Context, j *jobs.Job) error {
			ctx, current := s.forOperation(ctx)
			return current.lib.HandleAdopt(ctx, j)
		})
		return
	}
	s.lib.RegisterJobs(w, s.scan)
	s.lib.RegisterAdoptJob(w)
}

func (s *serveLibrary) forOperation(ctx context.Context) (context.Context, *serveLibrary) {
	if s.manager != nil {
		ctx = s.manager.Capture(ctx)
		if runtime, err := runtimeForSearch(ctx, s.manager); err == nil && runtime.Library != nil {
			return ctx, runtime.Library
		}
	}
	return ctx, s
}

// current is the active generation's library (the startup one without settings in the database).
func (s *serveLibrary) current() *library.Library {
	_, current := s.forOperation(context.Background())
	return current.lib
}

// Run loops until ctx ends.
func (s *serveLibrary) Run(ctx context.Context) {
	if s.pol.Library == nil {
		return
	}
	startCtx, initial := s.forOperation(ctx)
	initial.syncNaming(startCtx)
	// zurg's renames, as soon as it logs them; the hourly sweep stays the safety net.
	// rotated asks the loop below for a sweep now: renames logged just before an HTTP-followed log rotated are lost.
	rotated := make(chan struct{}, 1)
	// The log followers end with Run, so a restart on new settings never has two following the same log.
	var followers sync.WaitGroup
	defer followers.Wait()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if s.set.ZurgLog != "" {
		onRenames := func(ctx context.Context, m map[string]string) {
			ctx, current := s.forOperation(ctx)
			scans, err := current.lib.RelinkRenamed(ctx, m)
			n := 0
			for _, sc := range scans {
				n += len(sc.Relinked)
			}
			s.log.Info("library: zurg renamed folders", "renames", len(m), "relinked", n, "err", err)
		}
		if s.set.ZurgLogURL {
			f := &library.ZurgLogHTTPFollower{URL: s.set.ZurgLog, GW: s.gw, Interval: s.set.ZurgLogInterval,
				Logger: s.log, OnRenames: onRenames, OnLine: nowPlayingLine, OnRotate: func(context.Context) {
					s.log.Info("library: zurg's log rotated; sweeping for renames logged just before")
					select {
					case rotated <- struct{}{}:
					default:
					}
				}}
			followers.Go(func() { f.Run(ctx) })
			s.log.Info("library: following zurg's log for renames", "url", s.set.ZurgLog,
				"interval", s.set.ZurgLogInterval)
		} else {
			f := &library.ZurgLogFollower{Path: s.set.ZurgLog, OnRenames: onRenames, OnLine: nowPlayingLine}
			followers.Go(func() { f.Run(ctx) })
			s.log.Info("library: following zurg's log for renames", "path", s.set.ZurgLog)
		}
	}
	initial.danglingReport(startCtx)
	importTick := time.NewTicker(s.set.PollInterval)
	relinkTick := time.NewTicker(s.set.RelinkInterval)
	dailyTick := time.NewTicker(24 * time.Hour)
	danglingTick := time.NewTicker(s.set.DanglingEvery)
	defer importTick.Stop()
	defer relinkTick.Stop()
	defer dailyTick.Stop()
	defer danglingTick.Stop()
	// recheck runs the next import pass soon after one that left a just-ready download waiting for the mount.
	recheck := time.NewTimer(readyRecheck)
	recheck.Stop()
	defer recheck.Stop()
	importPass := func(ctx context.Context) {
		ctx, current := s.forOperation(ctx)
		if current.backOff("import pass") {
			return
		}
		if _, err := current.lib.Run(ctx); err != nil && !errors.Is(err, library.ErrMountDown) {
			s.log.Error("library: import pass", "err", err)
		}
		recheck.Stop()
		if current.lib.FreshWaiting(ctx) {
			recheck.Reset(readyRecheck)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-danglingTick.C:
			ctx, current := s.forOperation(ctx)
			if current.backOff("dangling report") {
				continue
			}
			current.danglingReport(ctx)
		case <-importTick.C:
			importPass(ctx)
		case <-s.readyWake:
			// Several downloads turning ready at once share one wake-up, and so one pass.
			importPass(ctx)
		case <-recheck.C:
			importPass(ctx)
		case <-rotated:
			ctx, current := s.forOperation(ctx)
			if current.backOff("relink sweep") {
				continue
			}
			if _, err := current.lib.Relink(ctx, "", false); err != nil {
				s.log.Warn("library: relink sweep", "err", err)
			}
		case <-relinkTick.C:
			ctx, current := s.forOperation(ctx)
			if current.backOff("relink sweep") {
				continue
			}
			if _, err := current.lib.Relink(ctx, "", false); err != nil {
				s.log.Warn("library: relink sweep", "err", err)
			}
		case <-dailyTick.C:
			ctx, current := s.forOperation(ctx)
			current.syncNaming(ctx)
			if n, err := current.lib.PurgeTrash(ctx); err != nil || n > 0 {
				s.log.Info("library: trash purge", "removed", n, "err", err)
			}
		}
	}
}

// backOff reports whether a pass is skipped because the mount's circuit breaker is open (the mount stopped answering;
// docs/design/library.md "Mount guard"). Once its cooldown is over the next pass runs, and its first mount call is the
// one the breaker lets through.
func (s *serveLibrary) backOff(pass string) bool {
	b := s.lib.MountBreaker()
	if b.State != library.BreakerOpen {
		return false
	}
	if !b.OpenedAt.Equal(s.breakerSeen) {
		s.breakerSeen = b.OpenedAt
		s.log.Warn("library: mount not answering; import, relink and dangling passes back off", "retry_at", b.RetryAt,
			"timeouts_in_a_row", b.Consecutive, "outstanding", b.Outstanding, "last_error", b.LastError)
	}
	s.log.Debug("library: "+pass+" skipped; mount circuit open", "retry_at", b.RetryAt)
	return true
}

// danglingReport records the dangling links (theatarr library dangling prints them).
func (s *serveLibrary) danglingReport(ctx context.Context) {
	if s.set.MountPath == "" {
		return
	}
	sum, err := s.lib.DanglingReport(ctx)
	switch {
	case err != nil:
		s.log.Warn("library: dangling report", "err", err)
	case sum.Dangling > 0 || len(sum.Errors) > 0:
		attrs := []any{"dangling", sum.Dangling, "links", sum.Links, "by_root", sum.ByRoot, "errors", sum.Errors,
			"took_ms", sum.Took.Milliseconds()}
		if z := sum.Zurg; z != nil {
			attrs = append(attrs, "no_video", z.NoVideo, "zurg_no_video_torrents", z.NoVideoTorrents,
				"zurg_repairing", z.Repairing, "zurg_error", z.Error)
		}
		s.log.Warn("library: dangling links", attrs...)
	default:
		s.log.Info("library: no dangling links", "links", sum.Links, "took_ms", sum.Took.Milliseconds())
	}
}

func (s *serveLibrary) syncNaming(ctx context.Context) {
	getters := map[string]library.ArrGetter{}
	if s.pol.Import != nil {
		for k, a := range s.pol.Import.Arr {
			if !s.pol.ArrFed(k) {
				continue
			}
			if key := s.pol.Getenv(a.APIKeyEnv); key != "" {
				if cl, err := arr.New(a.URL, key, arr.Options{}); err == nil {
					getters[k] = cl
				}
			}
		}
	}
	if done, err := library.SyncNaming(ctx, s.st, s.pol, getters, time.Now()); err != nil {
		s.log.Warn("library: naming sync", "err", err, "done", done)
	}
}
