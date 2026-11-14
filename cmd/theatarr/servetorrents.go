package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/torrents"
)

// serveTorrents gives the torrent manager (docs/design/torrents.md) of the settings generation a request or job runs
// under: its policy, gateway, provider clients and adders. Without a settings runtime it uses the start-up ones.
type serveTorrents struct {
	manager *settings.RuntimeCoordinator
	st      *store.Store
	q       *jobs.Queue
	dir     string
	log     *slog.Logger
	pol     *policy.Policy
	gw      *gateway.Gateway
	ps      *providerSync
	// lib is the start-up library service, whose decypharr client the manager reads without a settings runtime.
	lib *serveLibrary
	// addLock is the router's add lock, shared by every settings generation: restores add under it.
	addLock sync.Locker
	// usage works out the record's usage again after a delete (Manager.UsageStale); nil does nothing.
	usage *torrentUsageWorker
}

func (s *serveTorrents) Manager(ctx context.Context) (*torrents.Manager, error) {
	pol, gw := s.pol, s.gw
	var provs = s.ps.syncer.Providers
	syncer := s.ps.syncer
	var keys map[string]string
	lib := s.lib
	if s.manager != nil {
		rt := settings.ContextRuntime(ctx)
		if rt == nil {
			rt = s.manager.Current()
		}
		var active *serveRuntime
		if rt != nil {
			active, _ = rt.Components.(*serveRuntime)
		}
		if active == nil || active.Policy == nil || active.Gateway == nil || active.Providers == nil {
			return nil, errors.New("torrents: the active settings runtime is unavailable")
		}
		pol, gw, provs, keys = active.Policy, active.Gateway, active.Providers.syncer.Providers, active.ProviderKeys
		syncer = active.Providers.syncer
		lib = active.Library
	} else {
		keys = map[string]string{}
		for k, pv := range pol.Providers {
			keys[k] = os.Getenv(pv.APIKeyEnv)
		}
	}
	adders, _ := router.BuildAddersWithKeys(pol, gw, keys, s.log)
	m := &torrents.Manager{Store: s.st, Policy: pol, Providers: provs, Adders: adders, Dir: s.dir, Queue: s.q,
		Log: s.log, AddLock: s.addLock, Syncer: syncer, UsageStale: s.usage.Signal}
	if lib != nil && lib.lib != nil {
		// The library's own decypharr client (library.mount.decypharr), so its index is read once for both.
		m.Decypharr = lib.lib.Decypharr()
		m.Zurg = lib.lib.Zurg()
	}
	return m, nil
}

// torrentUsageWorker works out the usage of the record of the debrid accounts (torrents.Manager.StoreUsage) outside
// the job queue: after a provider sync job, a delete or an account change. Signal marks a run pending (one slot, so
// bursts coalesce); run does one at a time until serve's context ends.
type torrentUsageWorker struct{ pending chan struct{} }

func newTorrentUsageWorker() *torrentUsageWorker {
	return &torrentUsageWorker{pending: make(chan struct{}, 1)}
}

// Signal asks for a run; nil does nothing.
func (u *torrentUsageWorker) Signal() {
	if u == nil {
		return
	}
	select {
	case u.pending <- struct{}{}:
	default:
	}
}

func (u *torrentUsageWorker) run(ctx context.Context, s *serveTorrents, capture func(context.Context) context.Context,
	log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-u.pending:
			storeTorrentUsage(capture(ctx), s, log)
		}
	}
}

// torrentUsageMissing: some recorded torrent, or some v1 hash an account holds, has no usage worked out.
func torrentUsageMissing(ctx context.Context, st *store.Store) bool {
	var missing bool
	err := st.Reader().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM provider_torrent WHERE usage = '')
		OR EXISTS (SELECT 1 FROM provider_torrent pt WHERE length(pt.info_hash) = 40
			AND NOT EXISTS (SELECT 1 FROM provider_hash_usage hu WHERE hu.info_hash = pt.info_hash))`).Scan(&missing)
	return err == nil && missing
}

// storeTorrentUsage works out and stores the record's usage; a failure is logged, and the screens keep the usage of
// the last time it worked.
func storeTorrentUsage(ctx context.Context, s *serveTorrents, log *slog.Logger) {
	if s == nil {
		return
	}
	m, err := s.Manager(ctx)
	if err == nil {
		err = m.StoreUsage(ctx)
	}
	if err != nil {
		log.Warn("torrents: working out the usage of the record", "err", err)
	}
}
