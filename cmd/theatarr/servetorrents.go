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
}

func (s *serveTorrents) Manager(ctx context.Context) (*torrents.Manager, error) {
	pol, gw := s.pol, s.gw
	var provs = s.ps.syncer.Providers
	var keys map[string]string
	var lib *serveLibrary = s.lib
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
		lib = active.Library
	} else {
		keys = map[string]string{}
		for k, pv := range pol.Providers {
			keys[k] = os.Getenv(pv.APIKeyEnv)
		}
	}
	adders, _ := router.BuildAddersWithKeys(pol, gw, keys, s.log)
	m := &torrents.Manager{Store: s.st, Policy: pol, Providers: provs, Adders: adders, Dir: s.dir, Queue: s.q,
		Log: s.log, AddLock: s.addLock}
	if lib != nil && lib.lib != nil {
		// The library's own decypharr client (library.mount.decypharr), so its index is read once for both.
		m.Decypharr = lib.lib.Decypharr()
	}
	return m, nil
}
