package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/preflight"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/uipages"
)

func init() {
	register(command{
		name: "health",
		summary: "run the health checks that need no running service (mount, metadata, jobs, backups, providers, " +
			"disk) against a database and policy, and print them",
		run: func(args []string) error { return runHealth(args, os.Stdout) },
	})
}

// serveHealthConfig is what the checks read. gw is nil outside theatarr serve: live gateway checks are left out,
// but an unconfigured VPN guard still reports off.
type serveHealthConfig struct {
	pol     *policy.Policy
	manager *settings.RuntimeCoordinator
	// snap is the settings snapshot a command without a manager loaded (golive, theatarr health): SetupCheck counts
	// its ready sources.
	snap             *settings.Snapshot
	st               *store.Store
	q                *jobs.Queue
	gw               *gateway.Gateway
	lib              *library.Library // theatarr serve's library: the mount check goes through its guard and helpers
	dbPath           string
	backupDir        string
	providers        []string
	providerInterval time.Duration
	// accounts are the provider clients ProviderAccountCheck reads (theatarr serve's); without them and a manager the
	// check is left out.
	accounts   map[string]provider.Provider
	tmdb, tvdb bool
	started    time.Time
	log        *slog.Logger
}

// newServeHealth builds the health checks (internal/health) in the order the System screen lists them.
func newServeHealth(c serveHealthConfig) *health.Checker {
	if c.started.IsZero() {
		c.started = time.Now()
	}
	hc := health.New(health.Options{Logger: c.log})
	hc.Add("SetupCheck", "Setup", func(ctx context.Context) health.Result {
		p := c.pol
		// The sources that would run, as Setup counts them; without a snapshot, every one the policy names.
		var indexers, providers int
		if p != nil {
			indexers, providers = len(p.Indexers), len(p.Providers)
		}
		if c.snap != nil {
			indexers, providers = c.snap.Ready(os.LookupEnv)
		}
		if c.manager != nil {
			if runtime, err := runtimeForSearch(c.manager.Capture(ctx), c.manager); err == nil && runtime.Policy != nil {
				p, indexers, providers = runtime.Policy, runtime.ReadyIndexers, runtime.ReadyProviders
			}
		}
		return setupHealth(p, indexers, providers)
	})
	if c.manager != nil {
		hc.Add("SettingsFallbackCheck", "Settings generation", settingsFallbackHealth(c.manager))
	}
	lib := c.pol.LibrarySettings()
	hc.Add("MountCheck", "Mount", func(ctx context.Context) health.Result {
		pol, libService := c.pol, c.lib
		if c.manager != nil {
			ctx = c.manager.Capture(ctx)
			if runtime, err := runtimeForSearch(ctx, c.manager); err == nil {
				pol = runtime.Policy
				if runtime.Library != nil {
					libService = runtime.Library.lib
				}
			}
		}
		settings := pol.LibrarySettings()
		if !settings.Enabled || settings.MountPath == "" {
			return health.Result{Type: health.OK, Message: "Library off (library.enabled); the mount is not checked"}
		}
		path := filepath.Join(settings.MountPath, settings.AllDir)
		if settings.Decypharr != nil && libService != nil {
			// The mount is decypharr's: while it loads, refuses the credentials or does not answer, imports wait.
			if err := libService.DecypharrState(ctx); err != nil {
				return health.Result{Type: health.Error, Detail: map[string]any{"path": path},
					Message: decypharrMountMessage(path, err)}
			}
		}
		if libService != nil && libService.MountBreaker().Helper != nil {
			return health.MountAnswering(path, libService.MountAnswers)(ctx)
		}
		return health.Mount(path, settings.StatTimeout)(ctx)
	})
	// With settings in the database the helpers are the current generation's: an install that starts without a
	// mount and sets one later gets the check too.
	if c.manager != nil || c.lib != nil && c.lib.MountBreaker().Helper != nil {
		hc.Add("MountHelperCheck", "Mount helpers", func(ctx context.Context) health.Result {
			libService := c.lib
			if c.manager != nil {
				ctx = c.manager.Capture(ctx)
				if runtime, err := runtimeForSearch(ctx, c.manager); err == nil && runtime.Library != nil {
					libService = runtime.Library.lib
				}
			}
			return health.MountHelper(func() *health.MountHelperState {
				if libService == nil {
					return nil
				}
				h := libService.MountBreaker().Helper
				if h == nil {
					return nil
				}
				return &health.MountHelperState{RecentKills: h.RecentKills, Lingering: h.Lingering, LastError: h.LastError, RetryAt: h.RetryAt}
			})(ctx)
		})
	}
	// The report is read-only and runs whatever the gate says, so it is checked whenever the mount is set. With
	// settings in the database the mount is the current generation's.
	switch {
	case c.manager != nil:
		dangling := health.Dangling(c.st)
		hc.Add("DanglingLinksCheck", "Dangling links", func(ctx context.Context) health.Result {
			ctx = c.manager.Capture(ctx)
			if runtime, err := runtimeForSearch(ctx, c.manager); err == nil && runtime.Policy.LibrarySettings().MountPath == "" {
				return health.Result{Type: health.OK, Message: "No library mount set (library.mount.path); no dangling-link report"}
			}
			return dangling(ctx)
		})
	case lib.MountPath != "":
		hc.Add("DanglingLinksCheck", "Dangling links", health.Dangling(c.st))
	}
	hc.Add("VanishFailsafeCheck", "Library links missing at once", health.VanishFailsafe(c.st, time.Now))
	hc.Add("MetadataRefreshCheck", "Metadata", func(ctx context.Context) health.Result {
		o := health.MetadataOptions{TMDb: c.tmdb, TMDbInterval: c.pol.ArtSettings().Interval, TVDB: c.tvdb}
		if c.manager != nil {
			ctx = c.manager.Capture(ctx)
			if runtime, err := runtimeForSearch(ctx, c.manager); err == nil && runtime.Metadata != nil {
				o = health.MetadataOptions{TMDb: runtime.Metadata.art.Enabled(), TMDbInterval: runtime.Policy.ArtSettings().Interval,
					TVDB: runtime.Metadata.tvc != nil}
			}
		}
		return health.Metadata(c.st, o)(ctx)
	})
	hc.Add("JobBacklogCheck", "Job backlog", health.Backlog(c.q, health.BacklogOptions{Urgent: urgentJobKinds}))
	hc.Add("FailedJobsCheck", "Failed jobs", health.FailedJobs(c.q))
	hc.Add("BackupCheck", "Backups", health.Backup(health.BackupOptions{Dir: c.backupDir, Started: c.started}))
	hc.Add("ProviderSyncCheck", "Debrid providers", func(ctx context.Context) health.Result {
		keys, interval := c.providers, c.providerInterval
		if c.manager != nil {
			ctx = c.manager.Capture(ctx)
			if runtime, err := runtimeForSearch(ctx, c.manager); err == nil && runtime.Providers != nil {
				keys, interval = runtime.Providers.keys, runtime.Providers.interval
			}
		}
		return health.Providers(c.st, health.ProviderOptions{Providers: keys, Interval: interval})(ctx)
	})
	if c.manager != nil || c.accounts != nil {
		hc.Add("ProviderAccountCheck", "Debrid accounts", health.Accounts(func(ctx context.Context) map[string]health.AccountSource {
			// The static clients never change, so need no credential; a settings generation's clients carry their keys.
			clients, credential := c.accounts, func(string) string { return "" }
			if c.manager != nil {
				if runtime, err := runtimeForSearch(c.manager.Capture(ctx), c.manager); err == nil && runtime.Providers != nil {
					clients, credential = runtime.Providers.syncer.Providers, func(k string) string { return runtime.ProviderKeys[k] }
				}
			}
			out := map[string]health.AccountSource{}
			for k, p := range clients {
				out[k] = health.AccountSource{Reader: p, Fingerprint: health.Fingerprint(credential(k))}
			}
			return out
		}, health.AccountOptions{}))
	}
	hc.Add("UnsettledAddsCheck", "Provider adds", health.UnsettledAdds(c.st, 0, nil))
	hc.Add("CachedOnlyCheck", "Cached-only checks", health.UnfinishedCacheChecks(c.st, 0, nil))
	hc.Add("TorrentLimitCheck", "Torrent limits", health.TorrentLimits(c.st, func(ctx context.Context) map[string]int {
		p := c.pol
		if c.manager != nil {
			if runtime, err := runtimeForSearch(c.manager.Capture(ctx), c.manager); err == nil && runtime.Policy != nil {
				p = runtime.Policy
			}
		}
		out := map[string]int{}
		for k, pv := range p.Providers {
			out[k] = pv.TorrentLimit()
		}
		return out
	}, policy.TorrentLimitWarnPercent))
	if c.gw != nil {
		var indexers []string
		for k := range c.pol.Indexers {
			indexers = append(indexers, k)
		}
		sort.Strings(indexers)
		hc.Add("IndexerStatusCheck", "Indexers", func(ctx context.Context) health.Result {
			if c.manager != nil {
				ctx = c.manager.Capture(ctx)
				if runtime, err := runtimeForSearch(ctx, c.manager); err == nil {
					return health.Indexers(func() gateway.State { return runtime.Gateway.StateFor(ctx) }, runtime.Indexers.Keys())(ctx)
				}
			}
			return health.Indexers(func() gateway.State { return c.gw.StateFor(ctx) }, indexers)(ctx)
		})
		hc.Add("VpnEgressCheck", "VPN egress", func(ctx context.Context) health.Result {
			if c.manager != nil {
				ctx = c.manager.Capture(ctx)
			}
			return health.Egress(func() map[string]any { return c.gw.EgressSummaryFor(ctx) }, c.pol.Egress != nil)(ctx)
		})
	} else if c.pol.Egress == nil {
		hc.Add("VpnEgressCheck", "VPN egress", health.Egress(nil, false))
	}
	hc.Add("DiskSpaceCheck", "Disk space", health.Disk(filepath.Dir(c.dbPath), health.DiskOptions{}))
	return hc
}

func runHealth(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	settingsSourceFlag(fs)
	dbPath := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	policyDir := policyDirDefault()
	backupDir := fs.String("backup-dir", envOr("THEATARR_BACKUP_DIR", ""), "backup directory, as theatarr serve's (default backups/ next to the database; off: none)")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	switch *backupDir {
	case "":
		*backupDir = filepath.Join(filepath.Dir(*dbPath), "backups")
	case "off":
		*backupDir = ""
	}
	pol, snap, err := loadCommandSettings(fs, policyDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if _, err := os.Stat(*dbPath); err != nil {
		return err
	}
	st, err := store.Open(ctx, *dbPath, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	var providers []string
	for k, pv := range pol.Providers {
		if os.Getenv(pv.APIKeyEnv) != "" {
			providers = append(providers, k)
		}
	}
	sort.Strings(providers)
	// The metadata sources count as on when their keys are set here, as theatarr serve would see them.
	on := func(name string) bool {
		src := metadataSource(pol, name)
		return src != nil && src.URL != "" && src.APIKeyEnv != "" && pol.Getenv(src.APIKeyEnv) != ""
	}
	hc := newServeHealth(serveHealthConfig{pol: pol, snap: snap, st: st, q: jobs.NewQueue(st), dbPath: *dbPath,
		backupDir: *backupDir, providers: providers, providerInterval: pol.ProviderSyncInterval(), tmdb: on("tmdb"),
		tvdb: on("tvdb"), started: time.Now().Add(-48 * time.Hour), log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	rs := hc.Results(ctx)
	if *asJSON {
		b, _ := json.MarshalIndent(map[string]any{"status": health.Worst(rs), "checks": rs}, "", "  ")
		fmt.Fprintln(out, string(b))
	} else {
		for _, r := range rs {
			fmt.Fprintf(out, "%-8s %-20s %s\n", strings.ToUpper(r.Type), r.Source, r.Message)
		}
	}
	if health.Rank(health.Worst(rs)) >= health.Rank(health.Error) {
		return errors.New("a health check reports an error")
	}
	return nil
}

// setupHealth is SetupCheck: what a fresh install still lacks (preflight.Missing), as a notice. Each missing piece
// turns one feature off; nothing it turns off can grab, write or add on its own. theatarr preflight has the details.
func setupHealth(p *policy.Policy, indexers, providers int) health.Result {
	missing := preflight.Missing(p, indexers, providers)
	if len(missing) == 0 {
		return health.Result{Type: health.OK, Message: "Versions, indexers, providers, TMDb and the debrid mount are configured"}
	}
	return health.Result{Type: health.Notice, Message: "Setup incomplete: " + strings.Join(missing, "; ") +
		". Run the preflight (" + uipages.Setup + ", or theatarr preflight)", Detail: map[string]any{"missing": missing}}
}

// settingsFallbackHealth is an error while the running settings generation is older than the stored head: the head
// could not be prepared at start (startup fallback) and no reload has replaced it, so saved settings are not in force.
func settingsFallbackHealth(m *settings.RuntimeCoordinator) health.Func {
	return func(ctx context.Context) health.Result {
		active, head, behind, err := m.Behind(ctx)
		switch {
		case err != nil:
			return health.Result{Type: health.Warning, Message: "Cannot read the stored settings generation"}
		case behind:
			return health.Result{Type: health.Error, Detail: map[string]int64{"active_generation": active, "stored_generation": head},
				Message: fmt.Sprintf("Running settings generation %d, not the stored %d: the stored one could not be prepared "+
					"at start (startup fallback). Saved settings are not in force until it loads; see the log, then load it "+
					"again from Settings.", active, head)}
		}
		return health.Result{Type: health.OK, Message: fmt.Sprintf("Running the stored settings generation %d", active)}
	}
}

// observeHealth hands the health results to the error inbox every interval (the checks' own cache applies), so a
// check turning error is recorded and resolved again when it recovers. The first look waits one interval: checks
// that are still settling after the start (the mount, the egress check) are not recorded.
func observeHealth(ctx context.Context, hc *health.Checker, inbox *errinbox.Inbox, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rs := hc.Results(ctx)
		if ctx.Err() != nil {
			return
		}
		checks := make([]errinbox.HealthCheck, len(rs))
		for i, r := range rs {
			checks[i] = errinbox.HealthCheck{Source: r.Source, Name: r.Name, Type: r.Type, Message: r.Message, At: r.CheckedAt}
		}
		inbox.ObserveHealth(checks)
	}
}

// decypharrMountMessage is the mount check's message while decypharr (library.mount.decypharr) cannot be read.
func decypharrMountMessage(path string, err error) string {
	switch {
	case errors.Is(err, library.ErrDecypharrLoading):
		return "The mount at " + path + " is not ready: decypharr is still loading"
	case errors.Is(err, library.ErrDecypharrAuth):
		return "decypharr refuses the credentials (library.mount.decypharr); imports from the mount at " + path + " wait"
	}
	return "decypharr does not answer (library.mount.decypharr); imports from the mount at " + path + " wait"
}
