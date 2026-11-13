package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/buildinfo"
	"github.com/theatarr/theatarr/internal/ceiling"
	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/logring"
	"github.com/theatarr/theatarr/internal/metadata/anime/autobuild"
	"github.com/theatarr/theatarr/internal/metadata/art"
	"github.com/theatarr/theatarr/internal/metadata/tvrefresh"
	"github.com/theatarr/theatarr/internal/migration"
	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/pipeline"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/retention"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/server"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/torrents"
	"github.com/theatarr/theatarr/internal/web"
)

func init() {
	register(command{
		name:    "serve",
		summary: "run the service: migrate the database, load the stored settings, serve HTTP and run jobs",
		run:     runServe,
	})
}

func init() {
	register(command{
		name:    "healthcheck",
		summary: "exit 0 if the local service answers /readyz (for the container healthcheck)",
		run:     runHealthcheck,
	})
}

// runHealthcheck asks the local service's /readyz. The image has no curl, so the binary checks itself.
func runHealthcheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	listen := fs.String("listen", envOr("THEATARR_LISTEN", server.DefaultListen), "the service's listen address (env THEATARR_LISTEN)")
	path := fs.String("path", "/readyz", "path to check")
	if err := fs.Parse(args); err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + *path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", *path, resp.Status)
	}
	return nil
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// serveSettingsFlags are serve's flags that say where its settings resolve: the policy directory relative data paths
// (overrides) resolve under, and the deprecated settings source.
type serveSettingsFlags struct {
	policyDir, source *string
}

func addServeSettingsFlags(fs *flag.FlagSet) serveSettingsFlags {
	return serveSettingsFlags{
		policyDir: fs.String("policy", policyDirDefault(), "policy directory: data files the settings point at (overrides) and what backups archive (env THEATARR_POLICY)"),
		source:    fs.String("settings-source", settingsSourceDefault(), "deprecated: only db is supported (env THEATARR_SETTINGS_SOURCE)"),
	}
}

// base is the policy the stored settings load over: the -policy directory, whose root YAML is not read.
func (f serveSettingsFlags) base() (*policy.Policy, error) {
	return databasePolicyBase(*f.policyDir)
}

// serveSettings is what serve runs with from its settings flags and the database: the base the stored settings load
// over, the settings store and its head, and the policy that head makes.
type serveSettings struct {
	base     *policy.Policy
	store    *settings.Store
	snapshot *settings.Snapshot
	policy   *policy.Policy
}

// load is serve's settings setup before it listens, on the opened and migrated database st: the stored head loaded
// over the -policy directory (base), whose root YAML is not read.
func (f serveSettingsFlags) load(ctx context.Context, st *store.Store) (*serveSettings, error) {
	// Relative data paths in the stored settings resolve under the policy directory.
	base, err := f.base()
	if err != nil {
		return nil, err
	}
	settingsStore, snap, pol, err := loadServePolicy(ctx, *f.source, *f.policyDir, base, st)
	if err != nil {
		return nil, err
	}
	settingsStore.SetCardigannSecrets(cardigannSecretSettings)
	return &serveSettings{base: base, store: settingsStore, snapshot: snap, policy: pol}, nil
}

// prepareStartup prepares the runtime of the stored head initial, or of the last published generation when it does
// not prepare (prepareStartupRuntime), loaded over the same base.
func (s *serveSettings) prepareStartup(ctx context.Context, initial *settings.Snapshot, log *slog.Logger,
	prepare func(context.Context, *settings.Snapshot) (any, error)) (*settings.Snapshot, any, bool, error) {
	return prepareStartupRuntime(ctx, s.store, s.base, initial, log, prepare)
}

// coordinator is the runtime coordinator serve's later settings loads, saves and imports go through, over the same
// base: relative data paths keep resolving under the -policy directory.
func (s *serveSettings) coordinator(initial *settings.Snapshot, components any, prepare settings.Prepare) (*settings.RuntimeCoordinator, error) {
	return settings.NewRuntimeCoordinator(s.store, s.base, initial, components, prepare)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	dbPath := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	settingsFlags := addServeSettingsFlags(fs)
	policyDir, settingsSource := settingsFlags.policyDir, settingsFlags.source
	listen := fs.String("listen", envOr("THEATARR_LISTEN", server.DefaultListen), "listen address (env THEATARR_LISTEN)")
	level := fs.String("log-level", envOr("THEATARR_LOG_LEVEL", "info"), "log level: debug, info, warn, error (env THEATARR_LOG_LEVEL)")
	logDir := fs.String("log-dir", envOr("THEATARR_LOG_DIR", ""), "rotated JSON log directory (env THEATARR_LOG_DIR; default logs/ next to the database)")
	logMaxSize := fs.Int("log-max-size", envInt("THEATARR_LOG_MAX_SIZE", 10*1024*1024), "maximum size of one log file in bytes (env THEATARR_LOG_MAX_SIZE)")
	logMaxFiles := fs.Int("log-max-files", envInt("THEATARR_LOG_MAX_FILES", 10), "number of log files kept including the current file (env THEATARR_LOG_MAX_FILES)")
	backupDir := fs.String("backup-dir", envOr("THEATARR_BACKUP_DIR", ""), "directory for the nightly backup sets (database, policy, manifest; docs/restore.md); default backups/ next to the database, off disables them (env THEATARR_BACKUP_DIR)")
	backupKeep := fs.Int("backup-keep", envInt("THEATARR_BACKUP_KEEP", 14), "nightly backup sets to keep (env THEATARR_BACKUP_KEEP)")
	workers := fs.Int("workers", envInt("THEATARR_WORKERS", 2), "job worker slots for every kind (env THEATARR_WORKERS)")
	reserved := fs.Int("workers-reserved", envInt("THEATARR_WORKERS_RESERVED", 1), "extra job worker slots only acquisition and import jobs may use (env THEATARR_WORKERS_RESERVED)")
	backupHour := fs.Int("backup-hour", 3, "local hour (0-23) the nightly job runs")
	retentionDays := fs.Int("retention-days", envInt("THEATARR_RETENTION_DAYS", 180), "days of history kept: failed downloads, acquire runs, router plans, library actions, settings audit and RSS runs; 0 keeps them for good (docs/design/jobs.md, Retention; env THEATARR_RETENTION_DAYS)")
	retentionRunDays := fs.Int("retention-run-days", envInt("THEATARR_RETENTION_RUN_DAYS", 30), "days of provider sync runs and finished notification deliveries kept; 0 keeps them for good (env THEATARR_RETENTION_RUN_DAYS)")
	metricsPolicy := fs.String("metrics", envOr("THEATARR_METRICS", string(web.MetricsOpen)), "who may read /metrics: open, lan (private addresses) or key (env THEATARR_METRICS)")
	mountHelper := fs.Bool("mount-helper", envOr("THEATARR_MOUNT_HELPER", "true") != "false", "run the calls on the debrid mount in helper processes that are killed when a call hangs; false runs them in Theatarr under the in-process guard, for debugging (env THEATARR_MOUNT_HELPER)")
	cookieSecure := fs.String("cookie-secure", envOr("THEATARR_COOKIE_SECURE", string(auth.SecureAuto)), "Secure flag on cookies: auto (TLS or X-Forwarded-Proto https), always, never (env THEATARR_COOKIE_SECURE)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	mpol, err := web.ParseMetricsPolicy(*metricsPolicy)
	if err != nil {
		return err
	}
	csec, err := auth.ParseCookieSecure(*cookieSecure)
	if err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	helper, err := mountHelperConfig(*mountHelper)
	if err != nil {
		return err
	}
	if err := checkSettingsSource(*settingsSource); err != nil {
		return err
	}
	*settingsSource = "db"
	if *backupHour < 0 || *backupHour > 23 {
		return errors.New("--backup-hour must be 0-23")
	}
	switch *backupDir {
	case "":
		*backupDir = filepath.Join(filepath.Dir(*dbPath), "backups")
	case "off":
		*backupDir = ""
	}
	if *logDir == "" {
		*logDir = filepath.Join(filepath.Dir(*dbPath), "logs")
	}
	// Before the log writer, the first thing to write there: a data directory the service cannot write is reported
	// as one line with its fix (docs/troubleshooting.md).
	if err := checkDataDir(filepath.Dir(*dbPath)); err != nil {
		return err
	}
	rotated, err := server.NewRotatingWriter(*logDir, "theatarr.log", int64(*logMaxSize), *logMaxFiles)
	if err != nil {
		return err
	}
	defer rotated.Close()
	log, logLevel, err := server.NewRuntimeLogger(io.MultiWriter(os.Stderr, rotated), *level)
	if err != nil {
		return err
	}
	// Recent lines are also kept in memory for Admin › Advanced › Logs (GET /api/theatarr/v1/logs).
	logs := logring.New(2000)
	log = slog.New(logs.Handler(log.Handler()))
	slog.SetDefault(log)
	version := buildinfo.String()
	log.Info("starting", "version", version, "db", *dbPath, "policy", *policyDir, "listen", *listen)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	restoreResult, err := applyRestoreRequest(ctx, log, io.MultiWriter(os.Stderr, rotated), *backupDir, *dbPath, *policyDir, time.Now())
	if err != nil {
		return err
	}

	st, err := store.Open(ctx, *dbPath, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	applied, err := st.Migrate(ctx)
	for _, r := range applied {
		log.Info("migration applied", "version", r.Source.Version, "file", r.Source.Path, "took_ms", r.Duration.Milliseconds())
	}
	if err != nil {
		return err
	}
	// A stored profile or naming taken from an arr becomes a native copy before the settings load (migration 00125
	// kept the arr snapshots it needs in arr_frozen_input). A no-op once nothing stored is taken from an arr.
	if err := migration.FreezeStored(ctx, st, time.Now()); err != nil {
		return fmt.Errorf("copy profiles taken from Sonarr/Radarr: %w", err)
	}
	// The error inbox (docs/design/errors.md): error-level log records, recovered panics, 5xx answers, jobs that failed
	// for good and health checks turning error, deduplicated in error_event. Its writer outlives ctx, so what goes
	// wrong during shutdown is still written before the database closes.
	inbox := errinbox.New(st, errinbox.Options{Version: version, Logger: log})
	logs.OnError(inbox.LogTee)
	if restoreResult != nil && !restoreResult.OK {
		log.Error("backup restore failed", "set", restoreResult.Set, "err", restoreResult.Error)
	}
	errinbox.Install(inbox)
	inboxCtx, stopInbox := context.WithCancel(context.WithoutCancel(ctx))
	inboxDone := make(chan struct{})
	go func() { inbox.Run(inboxCtx); close(inboxDone) }()
	defer func() {
		stopInbox()
		<-inboxDone
		logs.OnError(nil)
		errinbox.Install(nil)
	}()
	loaded, err := settingsFlags.load(ctx, st)
	if err != nil {
		return err
	}
	settingsStore, settingsSnapshot, pol := loaded.store, loaded.snapshot, loaded.policy
	logPolicy(log, pol, settingsSnapshot)
	log.Info("settings source", "source", *settingsSource, "generation", settingsSnapshot.Generation)
	cur, _, err := st.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	log.Info("database ready", "path", st.Path(), "schema", cur)

	reg := server.NewRegistry(version)
	q := jobs.NewQueue(st)
	server.RegisterJobGauges(reg, q)
	// Acquisition and import jobs go first and have reserved slots, so metadata, art and sync backlogs never hold
	// every worker while a grab waits (urgentJobKinds).
	w := jobs.NewWorker(q, jobs.WorkerOptions{Logger: log, Registerer: reg, Concurrency: *workers,
		Reserved: *reserved, Urgent: urgentJobKinds})
	// Backup sets name the secrets of the settings head at backup time, not of the start (backupPolicyNow).
	backupPolicy := backupPolicyNow(*settingsSource, *policyDir, *dbPath, pol, log)
	n := &nightly{st: st, q: q, pol: pol, policyNow: backupPolicy, log: log, dir: *backupDir, keep: *backupKeep,
		hour: *backupHour}
	w.Handle(nightlyKind, n.run)
	keep := retention.Config{History: time.Duration(*retentionDays) * 24 * time.Hour, Runs: time.Duration(*retentionRunDays) * 24 * time.Hour}
	w.Handle(retention.JobKind, func(ctx context.Context, _ *jobs.Job) error {
		sum, err := retention.Prune(ctx, st, time.Now(), keep)
		if len(sum) > 0 {
			log.Info("retention pruned", "rows", sum)
		}
		return err
	})
	if err := n.schedule(ctx, time.Now()); err != nil {
		return err
	}
	if *backupDir == "" {
		log.Warn("NIGHTLY BACKUP OFF: --backup-dir off; nothing protects the database")
	} else {
		log.Info("nightly backup", "dir", *backupDir, "keep", *backupKeep, "hour", *backupHour)
	}

	egressChecks := newServeEgressChecks(ctx)
	defer egressChecks.Close()
	var serveSnapshot *serveRuntime
	var runtimeManager *settings.RuntimeCoordinator
	var gw *gateway.Gateway
	// The cardigann definition catalog (off unless cardigann.definitions is set) fetches through the active
	// runtime's gateway; its engine is shared by every runtime generation.
	cardigannCurrent := func(ctx context.Context) (*gateway.Gateway, *policy.Policy, error) {
		if runtimeManager != nil {
			rt, err := runtimeForSearch(runtimeManager.Capture(ctx), runtimeManager)
			if err != nil {
				return nil, nil, err
			}
			return rt.Gateway, rt.Policy, nil
		}
		if gw == nil {
			return nil, nil, errors.New("the gateway is not ready")
		}
		return gw, pol, nil
	}
	// A settings generation that sets cardigann.definitions later makes the catalog then (cardigannEnsure).
	defer serveCardigann.Store(nil)
	if err := cardigannEnsure(pol, filepath.Dir(*dbPath), st, cardigannCurrent, log); err != nil {
		return err
	}
	// The metadata services of each settings generation (servemetadata.go); auth and the acquirer join below.
	metaDeps := &serveMetadataDeps{st: st, q: q, dbPath: *dbPath, log: log}
	// The long-running loops, restarted when a published generation changes their settings (serveliveworkers.go).
	var liveWorkers *serveWorkers
	var wireRuntimeExtras func(ctx context.Context, next, previous *serveRuntime) error
	var runtimeMountCheck func(context.Context) error
	// readyWake wakes the library's import pass when the router records a download as ready, so it need not wait
	// for the next poll interval. It lives here, not in a settings generation: every generation's router signals it
	// and every generation's library loop reads it. Downloads turning ready while a wake-up is pending share it.
	readyWake := make(chan struct{}, 1)
	notifyReady := func() {
		select {
		case readyWake <- struct{}{}:
		default:
		}
	}
	settingsSnapshot, prepared, usedFallback, prepareErr := loaded.prepareStartup(ctx, settingsSnapshot, log,
		func(ctx context.Context, snap *settings.Snapshot) (any, error) {
			return prepareServeRuntime(ctx, snap, nil, st, q, reg, log)
		})
	if prepareErr != nil {
		return prepareErr
	}
	serveSnapshot, _ = prepared.(*serveRuntime)
	serveSnapshot.EgressChecks = egressChecks
	pol = settingsSnapshot.Policy
	runtimeManager, err = loaded.coordinator(settingsSnapshot, serveSnapshot,
		func(ctx context.Context, snap *settings.Snapshot, previous *settings.Runtime) (any, error) {
			var old *serveRuntime
			if previous != nil {
				old, _ = previous.Components.(*serveRuntime)
			}
			if err := cardigannEnsure(snap.Policy, filepath.Dir(*dbPath), st, cardigannCurrent, log); err != nil {
				return nil, err
			}
			next, err := prepareServeRuntime(ctx, snap, old, st, q, nil, log)
			if err == nil {
				next.Workers = liveWorkers
				next.Router.SetCurrentRuntime(func() *router.Router {
					return currentServeRouter(runtimeManager)
				})
				next.EgressChecks = egressChecks
				if runtimeMountCheck != nil {
					next.Router.SetMountCheck(runtimeMountCheck)
				}
				next.Router.SetReadyNotify(notifyReady)
				if wireRuntimeExtras != nil {
					if err := wireRuntimeExtras(ctx, next, old); err != nil {
						return nil, err
					}
				}
			}
			return next, err
		})
	if err != nil {
		return err
	}
	if !usedFallback {
		if err := runtimeManager.Reload(ctx, settingsSnapshot.Generation); err != nil {
			return err
		}
	}
	serveSnapshot = runtimeManager.Current().Components.(*serveRuntime)
	captureRuntime := func(ctx context.Context) context.Context {
		return runtimeManager.Capture(ctx)
	}
	// Every outbound call goes through the gateway, including any configured VPN requirements.
	gw = serveSnapshot.Gateway
	ps := serveSnapshot.Providers
	// The inbox masks the known credentials of the active settings generation (the gateway follows publications).
	inbox.SetKnownSecrets(gw.Redact)
	// The provider registry sync runs as a job on the policy's interval, for providers whose key is set.
	ps.syncer.Queue = q
	ps.runtimeSyncer = func() *providerSync {
		active, ok := runtimeManager.Current().Components.(*serveRuntime)
		if !ok || active == nil {
			return ps
		}
		return active.Providers
	}
	w.Handle(provider.JobKind, func(ctx context.Context, j *jobs.Job) error {
		var payload provider.SyncPayload
		if len(j.Payload) > 0 {
			if err := j.Decode(&payload); err != nil {
				return jobs.Permanent(err)
			}
		}
		ctx = runtimeManager.Capture(ctx)
		runtime := settings.ContextRuntime(ctx)
		if runtime == nil {
			runtime = runtimeManager.Current()
		}
		active, ok := runtime.Components.(*serveRuntime)
		if !ok || active == nil || active.Providers == nil {
			return errors.New("provider sync: active settings runtime is unavailable")
		}
		return active.Providers.syncer.Handle(ctx, j)
	})

	// The download router: provider writes only for active versions while router.enabled is true (docs/design/router.md).
	rt := serveSnapshot.Router
	rt.SetReadyNotify(notifyReady)
	routeService := &serveRouter{manager: runtimeManager, fallback: rt}
	rt.SetCurrentRuntime(func() *router.Router {
		return currentServeRouter(runtimeManager)
	})
	router.RegisterRuntimeJobs(w, q, routeService.forOperation)
	rt.SetRuntimeResolver(routeService.forOperation)
	rt.LogState()

	// The RSS sync (docs/design/rss.md): the Live libraries' feeds, their accepted items to the router. It is the
	// decision engine's film index.
	eng, idx := serveSnapshot.Engine, serveSnapshot.Indexers
	rssRunner := newRSSRunner(pol, st, q, gw, reg, log, routeService, serveSnapshot, rssRuntimeSource{manager: runtimeManager})
	rssRunner.RegisterJobs(w)

	ceiling.RegisterJobs(w, st, pol)
	// The torrent manager (docs/design/torrents.md): backups and restores run as jobs; deletes only on request.
	torrentSvc := &serveTorrents{manager: runtimeManager, st: st, q: q, dir: torrentBackupDir(*dbPath), log: log,
		pol: pol, gw: gw, ps: ps, addLock: rt.AddLock()}
	torrentManager := func(ctx context.Context) (*torrents.Manager, error) {
		return torrentSvc.Manager(captureRuntime(ctx))
	}
	torrents.RegisterJobs(w, torrentManager)

	// The library: imports of ready downloads, relinks and Plex scans, written only through its gate
	// (docs/design/library.md).
	// Offers: an imported movie is offered to the title's other movie versions; their own
	// rules decide, and an accepted offer reuses the provider's torrent.
	off := newServeOfferer(st, pol, eng, routeService, q, log, runtimeManager)
	off.RegisterJobs(w)
	lb, err := newServeLibrary(pol, st, q, gw, log, eng, helper, ps.syncer.Providers, func(ctx context.Context, id int64) {
		ctx = captureRuntime(ctx)
		if err := router.EnqueueOffer(ctx, q, id, time.Now()); err != nil {
			log.Warn("offer not queued", "download", id, "err", err)
		}
	})
	if err != nil {
		return err
	}
	lb.manager, lb.readyWake = runtimeManager, readyWake
	torrentSvc.lib = lb
	// The mount metrics read the active generation's library: a save that moves the mount starts a fresh guard there.
	library.RegisterMountMetrics(reg, lb.current)
	serveSnapshot.Library = lb
	lb.RegisterJobs(w)
	// Imports and renames pin the library of the runtime they start on (its roots, naming and engine). The
	// fallback copy has no resolver, so an operation without a runtime library cannot resolve back to lb.lib.
	fallback := lb.lib.WithPolicy(pol, eng, lb.formats, matchedRenameFormats(pol, lb.decider))
	lb.lib.SetRuntimeResolver(func(ctx context.Context) (context.Context, *library.Library) {
		ctx, current := lb.forOperation(ctx)
		if current == lb {
			return ctx, fallback
		}
		return ctx, current.lib
	})
	lb.LogState()
	rt.SetMountCheck(lb.lib.MountListed)
	runtimeMountCheck = lb.lib.MountListed

	// Watch-order playlists in Plex, behind the library and playlists gates; closed, a sync only records dry runs
	// (docs/design/anime.md).
	pls, err := newPlaylistSyncer(ctx, pol, st, gw, log)
	if err != nil {
		return err
	}
	serveSnapshot.Playlists = pls

	// TVDB v4 (docs/design/tvdb.md): nil without the tvdb source or its key. An hourly /updates read marks changed
	// series stale in the metadata cache.
	tvc, err := newTVDBClient(pol, st, gw)
	if err != nil {
		return err
	}
	wireRuntimeExtras = func(ctx context.Context, runtime, previous *serveRuntime) error {
		var prevMeta *serveMetadata
		if previous != nil {
			prevMeta = previous.Metadata
		}
		var err error
		if runtime.Metadata, err = prepareServeMetadata(prevMeta, runtime.Policy, runtime.Gateway, metaDeps); err != nil {
			return err
		}
		wireIndexerLookups(runtime.Policy, st, runtime.Gateway, runtime.Indexers, runtime.Metadata.tvc, log)
		wireCacheEvidence(runtime.Policy, st, runtime.Indexers, runtime.Providers.syncer.Providers, log)
		runtime.Library, err = newServeLibrary(runtime.Policy, st, q, runtime.Gateway, log, runtime.Engine, helper,
			runtime.Providers.syncer.Providers, func(ctx context.Context, id int64) { _ = router.EnqueueOffer(ctx, q, id, time.Now()) })
		if err != nil {
			return err
		}
		// Its loops (serveliveworkers.go) resolve each pass's library like the startup one.
		runtime.Library.manager, runtime.Library.readyWake = runtimeManager, readyWake
		// The mount guard, helpers and listing floor carry over from the generation being replaced (the startup
		// library before the first save), so a save neither resets an open breaker nor keeps old mount settings.
		prevLib := lb.lib
		if previous != nil && previous.Library != nil && previous.Library.lib != nil {
			prevLib = previous.Library.lib
		}
		runtime.Library.lib.InheritMount(prevLib)
		runtime.Router.SetMountCheck(runtime.Library.lib.MountListed)
		runtime.Playlists, err = newPlaylistSyncer(ctx, runtime.Policy, st, runtime.Gateway, log)
		if err != nil {
			return err
		}
		runtime.Anime = newServeAnime(runtime.Policy, st, q, runtime.Gateway, runtime.Metadata.tvc, log)
		return nil
	}
	// Series metadata (docs/design/tv-metadata.md): Theatarr's own episode list from TVDB (TMDb for series TVDB lacks), refreshed on
	// add, on demand and on Sonarr's cadence; a new series' searches wait for its first refresh.
	tv, err := newServeSeriesMeta(pol, st, q, gw, tvc, log)
	if err != nil {
		return err
	}
	// currentMetadata resolves the services of the captured generation, falling back during construction.
	var startMeta *serveMetadata
	currentMetadata := func(ctx context.Context) (context.Context, *serveMetadata) {
		ctx = runtimeManager.Capture(ctx)
		if runtime, err := runtimeForSearch(ctx, runtimeManager); err == nil && runtime.Metadata != nil {
			return ctx, runtime.Metadata
		}
		return ctx, startMeta
	}
	tvrefresh.RegisterRuntimeJobs(w, func(ctx context.Context) (context.Context, *tvrefresh.Service) {
		ctx, m := currentMetadata(ctx)
		return ctx, m.tv
	})
	// The acquirer: the arr views' search commands and the wanted loop, for active versions only, routed
	// (docs/design/router.md "Triggers").
	acConfig := acquire.Config{Store: st, Policy: pol, Queue: q, Router: routeService, Engine: eng, Indexers: idx, Logger: log}
	acConfig.CaptureRuntime = runtimeManager.Capture
	acConfig.Search, acConfig.SearchBatch, acConfig.RuntimeEngine = runtimeSearchers(runtimeManager)
	ac, err := pipeline.NewAcquirer(acConfig, tv)
	if err != nil {
		return err
	}
	acService := &serveAcquirer{manager: runtimeManager, fallback: ac}
	metaDeps.acquirer = acService
	serveSnapshot.Acquirer = ac
	// The runtime was published before the acquirer existed: with router.wanted.retry_new off, what its chains left
	// queued is cancelled here, before any worker runs a job.
	cancelRetryNew(ac)
	acService.RegisterJobs(w)
	if tv.Enabled() {
		tv.SetSearches(acService)
	}
	// The lookups the indexer helpers did themselves: AniList ids for SeaDex, episode counts for Torrentio.
	wireIndexerLookups(pol, st, gw, idx, tvc, log)
	// Cache evidence: DMM feeds stored per hash and provider, and the bounded TorBox check (docs/design/router.md).
	wireCacheEvidence(pol, st, idx, ps.syncer.Providers, log)

	// Auth is always on: the first start creates the signing secret and no account; the first visit to the web UI
	// creates the admin account. An older version's unused initial-admin-password file is removed at the first login.
	au, _, err := auth.New(ctx, st, auth.Options{Logger: log, CookieSecure: csec, DataDir: filepath.Dir(*dbPath)})
	if err != nil {
		return err
	}
	metaDeps.auth = au
	// The arr views (/radarr, /sonarr, /v/<version>/...): arr-shaped APIs over our data. They write to our database,
	// and through the router (grabs, active versions only) and the library (imports and unlinks, behind its gate).
	avConfig := arrview.Config{ReleaseCache: arrview.NewReleaseCache(nil), Store: st, Auth: au, Policy: pol, Engine: eng, Logger: log, Searcher: acService, Started: time.Now(),
		RSSSync: rssRunner.QueueRSS,
		Decider: acService, Cutoff: acService, Router: routeService, Library: lb.lib, SeriesLookup: tv, CaptureRuntime: captureRuntime}
	if avConfig.TMDb, err = newServeViewTMDb(pol, st, gw); err != nil {
		return err
	}
	av, err := arrview.New(ctx, avConfig)
	if err != nil {
		return err
	}
	// Outbound webhooks: arr-shaped per view and native, through the gateway; off unless notify.enabled.
	// notify.enabled and the privacy of DB-backed indexers follow settings reloads (PolicyFor).
	// Payloads and the Health webhook resolve the arr views of the current settings generation (runtimeArrView).
	notifyViews := runtimeArrView{manager: runtimeManager, fallback: av}
	nt, err := notify.New(notify.Config{Store: st, Policy: pol, Doer: gw, Payloads: notifyViews, Health: notifyViews.HealthItems,
		Logger: log, PolicyFor: runtimePolicy(runtimeManager), Jobs: q})
	if err != nil {
		return err
	}
	nt.RegisterJobs(w)
	av.SetNotifier(nt)
	nt.LogState()
	// Title art and metadata: the meta.art job and the /img cache, through the gateway (docs/design/artwork.md).
	ar, err := newServeArt(pol, st, q, gw, *dbPath, log)
	if err != nil {
		return err
	}
	mi, err := newServeMovieIntake(pol, st, gw)
	if err != nil {
		return err
	}
	if mi != nil {
		mi.OnAdd, mi.Now, mi.Log = movieIntakeSearch(acService), time.Now, log
	}
	// Share metadata refresh with the native API and queue the arr maintenance commands; a series refresh also
	// queues its episodes' refresh.
	refresher := &titleRefresher{Service: ar, tv: tv, st: st}
	av.SetMaintenance(q, refresher)
	av.RegisterRuntimeJobs(w, func(ctx context.Context) (context.Context, *arrview.Service) {
		ctx = runtimeManager.Capture(ctx)
		if runtime, err := runtimeForSearch(ctx, runtimeManager); err == nil && runtime.HTTP != nil && runtime.HTTP.av != nil {
			return ctx, runtime.HTTP.av
		}
		return ctx, av
	})
	art.RegisterRuntimeJobs(w, func(ctx context.Context) (context.Context, *art.Service) {
		ctx, m := currentMetadata(ctx)
		return ctx, m.art
	})
	// The grab journal: readers and verdicts; a "wrong" verdict on an active version queues acquire.research.
	// It follows the current settings generation, so a version activated later is journaled and judged as active.
	jr := journal.New(journal.Config{Store: st, Policy: pol, Queue: q, Logger: log, PolicyFor: runtimePolicy(runtimeManager)})
	// Search and add (docs/design/ui.md): TMDb search through the gateway; searches for active versions only.
	add, err := newServeAdd(pol, st, gw, acService, log)
	if err != nil {
		return err
	}
	add.Collections = webMovieIntake(mi)
	// Plex sign-in (docs/design/users.md): plex.tv through the gateway (metadata.sources.plex-tv); off without it.
	pl, err := newPlexLogin(pol, au, gw, log)
	if err != nil {
		return err
	}
	startMeta = startupServeMetadata(pol, *dbPath, serveMetadata{tvc: tvc, tv: tv, art: ar, refresher: refresher, intake: mi,
		add: add, plex: pl, viewTMDb: avConfig.TMDb})
	serveSnapshot.Metadata = startMeta
	// Anime franchises (docs/design/anime.md "Automatic builds"): anime_build jobs on the builder of the captured
	// generation; the discovery and refresh loop is a live worker (serveliveworkers.go).
	startAnime := newServeAnime(pol, st, q, gw, tvc, log)
	serveSnapshot.Anime = startAnime
	currentAnime := func(ctx context.Context) (context.Context, *autobuild.Service) {
		ctx = runtimeManager.Capture(ctx)
		if runtime, err := runtimeForSearch(ctx, runtimeManager); err == nil && runtime.Anime != nil {
			return ctx, runtime.Anime
		}
		return ctx, startAnime
	}
	autobuild.RegisterRuntimeJobs(w, currentAnime)
	// Health checks (internal/health): the native API, the System screen, every view's GET health and the Health
	// webhook read the same cached results.
	hc := newServeHealth(serveHealthConfig{pol: pol, st: st, q: q, gw: gw, lib: lb.lib, dbPath: *dbPath, backupDir: *backupDir,
		manager: runtimeManager, providers: ps.keys, providerInterval: ps.interval, accounts: ps.syncer.Providers, tmdb: ar.Enabled(), tvdb: tvc != nil, log: log})
	av.SetHealth(hc)
	hc.Add("AnimeBuildCheck", "Anime builds", animeBuildHealth(func(ctx context.Context) *autobuild.Service {
		_, s := currentAnime(ctx)
		return s
	}))
	qualitySync := newQualitySync(settingsStore, st, runtimeManager, os.Getenv("THEATARR_TRASH"))
	hc.Add("QualitySyncReviewCheck", "Quality sync", func(ctx context.Context) health.Result {
		return qualitySyncHealth(ctx, qualitySync)
	})
	requestNotify := func(ev web.RequestEvent) {
		if ev.Notification != nil {
			nt.Emit(context.WithoutCancel(ctx), *ev.Notification)
		}
	}
	backupSvc := &serveBackups{st: st, dir: *backupDir, dbPath: *dbPath, source: *settingsSource,
		keep: *backupKeep, hour: *backupHour, policyNow: backupPolicy, now: time.Now}
	uiConfig := web.Config{Backups: backupSvc, Restart: stop, Store: st, Auth: au, Policy: pol, PolicyDir: *policyDir, Egress: serveEgressSource{manager: runtimeManager, fallback: gw}, Logger: log,
		Version: version, Devices: av, Art: refresher, Journal: jr, Logs: logs, Add: add, Jobs: q, Plex: pl, Health: hc,
		Ops: serveOps{av}, OnRequest: requestNotify, LogLevel: logLevel, LogDir: *logDir, ReloadSettings: runtimeManager.Reload,
		CaptureRuntime: captureRuntime, SettingsTests: newSettingsTestLimits(), SettingsLookup: os.LookupEnv, TrashRoot: os.Getenv("THEATARR_TRASH"),
		Torrents: torrentSvc, NowPlaying: nowPlayingSnapshot, ArrImport: serveArrImport{manager: runtimeManager, st: st, log: log},
		RcloneTestGateway: rcloneTestGateway(runtimeManager, gw, log), NotifyTest: nt.SendTest,
		AnimeBuilds: func(ctx context.Context) web.AnimeBuilder {
			if _, s := currentAnime(ctx); s != nil {
				return s
			}
			return nil
		}}
	// The install preflight (POST /preflight): the current generation's gateway and views, the service's own handler.
	var rootHandler http.Handler
	uiConfig.Preflight = servePreflightHooks(notifyViews.current, func(ctx context.Context) (*gateway.Gateway, *policy.Policy) {
		if runtime, err := runtimeForSearch(captureRuntime(ctx), runtimeManager); err == nil && runtime.Gateway != nil {
			return runtime.Gateway, runtime.Policy
		}
		return gw, pol
	}, lb.current, func() http.Handler { return rootHandler }, log)
	// The admin settings API uses the active database coordinator.
	uiConfig.Settings = runtimeManager
	uiConfig.QualitySync = qualitySync
	uiConfig.SettingsTester = serveSettingsTester{manager: runtimeManager, st: st, log: log, lookup: os.LookupEnv}
	if c := serveCardigann.Load(); c != nil {
		uiConfig.Cardigann = c
	}
	ui, err := web.New(uiConfig)
	if err != nil {
		return err
	}
	httpServices := &serveHTTPServices{ctx: ctx}
	defer httpServices.Close()
	initialMux := http.NewServeMux()
	ui.Mount(initialMux)
	av.Mount(initialMux)
	initialHTTP := &serveHTTPRuntime{mux: initialMux, ui: ui, av: av, services: httpServices}
	serveSnapshot.HTTP = initialHTTP
	wireIndexers := wireRuntimeExtras
	avConfig.Notifier, avConfig.Jobs, avConfig.Metadata = nt, q, refresher
	wireRuntimeExtras = func(ctx context.Context, runtime, previous *serveRuntime) error {
		if err := wireIndexers(ctx, runtime, previous); err != nil {
			return err
		}
		meta := runtime.Metadata
		cfg := acConfig
		if meta.tv.Enabled() {
			cfg.Metadata = meta.tv
		}
		if err := acService.prepare(cfg, runtime); err != nil {
			return err
		}
		avCfg, uiCfg := avConfig, uiConfig
		avCfg.Policy, avCfg.Engine, uiCfg.Policy = runtime.Policy, runtime.Engine, runtime.Policy
		avCfg.Library = runtime.Library.lib
		avCfg.TMDb, avCfg.Metadata, avCfg.SeriesLookup = meta.viewTMDb, meta.refresher, meta.tv
		uiCfg.Art, uiCfg.Add, uiCfg.Plex = meta.refresher, meta.add, meta.plex
		if c := serveCardigann.Load(); c != nil {
			uiCfg.Cardigann = c
		}
		var err error
		runtime.HTTP, err = newServeHTTPRuntime(ctx, avCfg, uiCfg, httpServices)
		return err
	}

	if err := enqueueRestoredProviderSync(ctx, q, restoreResult); err != nil {
		log.Error("enqueue provider sync after restore", "err", err)
	}

	// The serve loops below run under errinbox.Go: a panic is written to the error inbox before it crashes the
	// process as before (the container restarts it).
	var wg sync.WaitGroup
	wg.Add(1)
	go errinbox.Go("quality-sync", func() { defer wg.Done(); qualitySync.Run(ctx) })()
	// Planner statistics: PRAGMA optimize after the startup migrations, then daily.
	wg.Add(1)
	go errinbox.Go("store-optimize", func() { defer wg.Done(); runStoreOptimize(ctx, st, log) })()
	// Scheduled torrent backups: off unless torrents.backup.every is set. Each check reads the settings in force.
	wg.Add(1)
	go errinbox.Go("torrents", func() { defer wg.Done(); torrents.RunSchedule(ctx, q, torrentManager, 10*time.Minute) })()
	// Health checks turning error go to the error inbox, and recover there automatically.
	wg.Add(1)
	go errinbox.Go("errinbox", func() { defer wg.Done(); observeHealth(ctx, hc, inbox, 2*time.Minute) })()
	// The loops whose schedule or source is a settings section run as workers: a published generation that changes
	// their settings stops and restarts them (serveliveworkers.go).
	startRuntime := serveSnapshot
	loops := serveLoops{router: rt, sync: ps, rss: rssRunner, log: log, current: func(ctx context.Context) (context.Context, *serveRuntime) {
		ctx = runtimeManager.Capture(ctx)
		if runtime, err := runtimeForSearch(ctx, runtimeManager); err == nil {
			return ctx, runtime
		}
		return ctx, startRuntime
	}}
	liveWorkers = newServeWorkers(ctx, log, loops.workers()...)
	startRuntime.Workers = liveWorkers
	hc.Add("LiveSettingsCheck", "Settings", liveWorkers.Health)
	if current, ok := runtimeManager.Current().Components.(*serveRuntime); ok && current != nil {
		startRuntime = current
	}
	warnVPNGuardOff(startRuntime.Policy, log)
	liveWorkers.Start(startRuntime)
	// Every gateway generation shares the first one's request counts (Options.Previous): one loop writes them, the
	// last time after everything that sends indexer requests has stopped.
	stopRequestFlush := startRequestFlush(ctx, gw)
	wg.Add(5)
	go errinbox.Go("ceiling", func() { defer wg.Done(); ceiling.Run(ctx, q, log) })()
	go errinbox.Go("notify", func() {
		defer wg.Done()
		nt.Run(ctx)
	})()
	go errinbox.Go("offers", func() {
		defer wg.Done()
		runOfferSweep(ctx, off, log)
	})()
	go errinbox.Go("jobs", func() {
		defer wg.Done()
		w.Run(ctx)
	})()
	go errinbox.Go("http", func() {
		defer wg.Done()
		httpServices.Start(initialHTTP)
		<-ctx.Done()
	})()

	ready := egressReadiness(server.StoreReadiness(st), gw)
	srv := server.New(server.Config{Listen: *listen, Logger: log, Registry: reg, Ready: ready, Version: version,
		MetricsGuard: web.MetricsGuard(mpol, au)})
	rootHandler = runtimeHTTPHandler(runtimeManager, initialMux)
	srv.Mux().Handle("/", rootHandler)
	log.Info("web", "metrics", string(mpol), "cookie_secure", string(csec))
	err = srv.Run(ctx)
	stop()
	liveWorkers.Close()
	wg.Wait()
	stopRequestFlush()
	if err != nil {
		return err
	}
	log.Info("stopped")
	return nil
}

// logPolicy logs what the policy configures: versions and modes, every outbound host (docs/design/gateway.md), and the
// credentials that resolve nowhere, neither stored in the settings database nor in the environment (env names or
// settings paths only, never values).
func logPolicy(log *slog.Logger, p *policy.Policy, snap *settings.Snapshot) {
	keys := make([]string, 0, len(p.Versions))
	for k := range p.Versions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := p.Versions[k]
		log.Info("version", "key", k, "mode", v.Mode, "kind", v.Kind, "storage", v.Storage, "library", v.Library)
	}
	hosts := p.OutboundHosts()
	names := make([]string, len(hosts))
	for i, h := range hosts {
		names[i] = h.Host
		log.Info("outbound host", "host", h.Host, "used_by", strings.Join(h.Users, ", "))
	}
	log.Info("policy loaded", "dir", p.Dir, "files", len(p.Files), "revision", p.Revision, "versions", len(p.Versions),
		"outbound_hosts", names)
	// A credential stored in the settings database is not an env variable: it is reported (by its settings path)
	// only when it resolves nowhere, as the running service would resolve it.
	missing := settings.Credentials{Policy: p, Snap: snap, Lookup: os.LookupEnv}.Unset()
	if len(missing) > 0 {
		log.Warn("credentials not set", "names", missing)
	}
}

// providerSync enqueues provider.sync every interval. The job's dedupe key keeps a slow or retrying sync from
// stacking: while one is queued or running, the next tick enqueues nothing.
type providerSync struct {
	syncer        *provider.Syncer
	st            *store.Store
	q             *jobs.Queue
	log           *slog.Logger
	keys          []string // providers with a key set, in fallback order
	interval      time.Duration
	runtimeSyncer func() *providerSync
	// delay is the earliest a first sync runs after start, so the egress check has run when providers need the VPN.
	delay time.Duration
}

func newProviderSync(pol *policy.Policy, st *store.Store, q *jobs.Queue, d provider.Doer, log *slog.Logger) (*providerSync, error) {
	ps := &providerSync{st: st, q: q, log: log.With("component", "provider-sync"),
		interval: pol.ProviderSyncInterval(), delay: time.Minute}
	s := &provider.Syncer{Store: st, Providers: map[string]provider.Provider{}, Log: log}
	for k := range pol.Providers {
		s.Configured = append(s.Configured, k)
	}
	sort.Slice(s.Configured, func(i, j int) bool {
		return pol.Providers[s.Configured[i]].Order < pol.Providers[s.Configured[j]].Order
	})
	var skipped []string
	for _, k := range s.Configured {
		pv := pol.Providers[k]
		key := os.Getenv(pv.APIKeyEnv)
		if key == "" {
			skipped = append(skipped, k+" ("+pv.APIKeyEnv+")")
			continue
		}
		c, err := provider.New(k, pv, key, d)
		if err != nil {
			return nil, err
		}
		s.Providers[k] = c
		ps.keys = append(ps.keys, k)
	}
	ps.syncer = s
	if len(skipped) > 0 {
		ps.log.Info("provider sync skips providers with no key set", "providers", skipped)
	}
	return ps, nil
}

// first is when the first sync after start is due: an interval after the oldest provider's latest run, or soon if a
// provider has never run.
func (p *providerSync) first(ctx context.Context, now time.Time) (time.Time, error) {
	earliest := now.Add(p.delay)
	last := map[string]int64{}
	rows, err := p.st.Reader().QueryContext(ctx,
		`SELECT provider, MAX(started_at) FROM provider_sync_run GROUP BY provider`)
	if err != nil {
		return earliest, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var at int64
		if err := rows.Scan(&k, &at); err != nil {
			return earliest, err
		}
		last[k] = at
	}
	if err := rows.Err(); err != nil {
		return earliest, err
	}
	var next time.Time
	for _, k := range p.keys {
		at, ok := last[k]
		if !ok {
			return earliest, nil
		}
		if t := store.FromMillis(at).Add(p.interval); next.IsZero() || t.Before(next) {
			next = t
		}
	}
	if next.Before(earliest) {
		return earliest, nil
	}
	return next, nil
}

// enqueue adds a sync of every provider with a key, unless one is already queued or running.
func (p *providerSync) enqueue(ctx context.Context) {
	payload := provider.SyncPayload{}
	id, created, err := p.q.Enqueue(ctx, provider.JobKind, payload,
		jobs.EnqueueOptions{DedupeKey: provider.JobKind})
	switch {
	case err != nil:
		p.log.Warn("enqueue provider sync", "err", err)
	case !created:
		p.log.Info("provider sync still queued or running; not enqueued again", "job", id)
	default:
		p.log.Debug("provider sync enqueued", "job", id)
	}
}

// run enqueues a sync when one is due, then every interval, until ctx ends. Without providers it does nothing.
func (p *providerSync) run(ctx context.Context) {
	if p.runtimeSyncer == nil && len(p.keys) == 0 {
		return
	}
	active := p
	if p.runtimeSyncer != nil {
		active = p.runtimeSyncer()
	}
	next, err := active.first(ctx, time.Now())
	if err != nil {
		active.log.Warn("reading the last provider sync", "err", err)
	}
	active.log.Info("provider sync scheduled", "providers", active.keys, "interval", active.interval.String(), "next", next.UTC())
	t := time.NewTimer(time.Until(next))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			active = p
			if p.runtimeSyncer != nil {
				active = p.runtimeSyncer()
			}
			active.enqueue(ctx)
			t.Reset(active.interval)
		}
	}
}

const nightlyKind = "db.nightly"

// urgentJobKinds are the acquisition and import jobs: leased before other kinds, and the only ones the reserved
// worker slots run (jobs.WorkerOptions.Urgent).
var urgentJobKinds = []string{acquire.JobSearch, acquire.JobWanted, router.JobResearch, router.JobRoute,
	router.JobOffer, library.ScanJobKind}

// nightly writes a backup set (database, policy, manifest; backup.go) and keeps the newest sets, prunes finished jobs
// and the journal's decision rows, and schedules itself for the next night. Each night's job has its own dedupe key,
// so a restart or a re-run never doubles it.
type nightly struct {
	st  *store.Store
	q   *jobs.Queue
	pol *policy.Policy
	// policyNow, when set, gives the policy each set records (its secret names) at backup time; else pol.
	policyNow func(context.Context) *policy.Policy
	log       *slog.Logger
	dir       string
	keep      int
	hour      int
}

func (n *nightly) next(now time.Time) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), n.hour, 0, 0, 0, now.Location())
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

func (n *nightly) schedule(ctx context.Context, now time.Time) error {
	at := n.next(now)
	_, _, err := n.q.Enqueue(ctx, nightlyKind, nil, jobs.EnqueueOptions{
		RunAt: at, DedupeKey: nightlyKind + ":" + at.Format("2006-01-02"), MaxAttempts: 3,
	})
	return err
}

func (n *nightly) run(ctx context.Context, j *jobs.Job) (err error) {
	// Keep the next night scheduled even when this run exhausts its retries.
	defer func() {
		scheduleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		err = errors.Join(err, n.schedule(scheduleCtx, time.Now()))
	}()
	// The journal's own decision rows: in full for 180 days, then trimmed or dropped (journal.Prune).
	if sum, err := journal.Prune(ctx, n.st, time.Now()); err != nil {
		n.log.Warn("journal prune", "err", err)
	} else if sum.Deleted+sum.Trimmed > 0 {
		n.log.Info("journal pruned", "deleted", sum.Deleted, "trimmed", sum.Trimmed)
	}
	// A failed backup (it leaves no partial set behind) no longer stops the rest of the night: the job fails and
	// retries, but old sets and finished jobs are still pruned.
	var backupErr error
	if n.dir != "" {
		start := time.Now()
		pol := n.pol
		if n.policyNow != nil {
			pol = n.policyNow(ctx)
		}
		base, m, err := writeBackupSet(ctx, n.st, pol, n.dir, start)
		if err != nil {
			backupErr = fmt.Errorf("backup: %w", err)
			// The error inbox records the nightly job once it fails for good (job source), not each attempt.
			n.log.ErrorContext(errinbox.Quiet(ctx), "backup failed", "attempt", j.Attempt, "max_attempts", j.MaxAttempts, "err", err)
		} else {
			n.log.Info("backup written", "set", base, "db_bytes", m.Database.Bytes, "policy_state", m.Policy.State,
				"policy_revision", m.Policy.Revision, "took_ms", time.Since(start).Milliseconds())
		}
		if err := pruneBackups(n.dir, n.keep); err != nil {
			n.log.Warn("backup prune", "err", err)
		}
	}
	if pruned, err := n.q.Prune(ctx, time.Now().Add(-14*24*time.Hour)); err != nil {
		return errors.Join(backupErr, err)
	} else if pruned > 0 {
		n.log.Info("finished jobs pruned", "count", pruned)
	}
	// The other growing tables (retention package), as their own job below every other: it runs when nothing waits,
	// and after tonight's backup is written, so pruned rows are still in that backup. A backup that fails on every
	// attempt does not hold retention back for good: the last attempt schedules it anyway.
	lastAttempt := j.MaxAttempts > 0 && j.Attempt >= j.MaxAttempts
	if backupErr == nil || lastAttempt {
		if backupErr != nil {
			n.log.Warn("retention runs without tonight's backup", "err", backupErr)
		}
		if _, _, err := n.q.Enqueue(ctx, retention.JobKind, nil, jobs.EnqueueOptions{Priority: retention.Priority,
			DedupeKey: retention.JobKind + ":" + time.Now().Format("2006-01-02"), MaxAttempts: 3}); err != nil {
			n.log.Warn("retention enqueue", "err", err)
		}
	}
	return backupErr
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// startRequestFlush runs the gateway's request-count flush loop apart from ctx: serve calls stop once every request
// producer has stopped (HTTP server shut down, workers and loops done), and stop returns after the last flush, so a
// search that completes during graceful shutdown keeps its count.
func startRequestFlush(ctx context.Context, gw *gateway.Gateway) (stop func()) {
	fctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go errinbox.Go("indexer-requests", func() { defer close(done); gw.RunRequestFlush(fctx) })()
	return func() { cancel(); <-done }
}

// runStoreOptimize runs the store's PRAGMA optimize now and then every day until ctx ends.
func runStoreOptimize(ctx context.Context, st *store.Store, log *slog.Logger) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		start := time.Now()
		err := st.Optimize(ctx)
		log.Debug("store: optimize", "took_ms", time.Since(start).Milliseconds(), "err", err)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
