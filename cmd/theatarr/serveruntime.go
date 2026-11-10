package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/buildinfo"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/metadata/anime/autobuild"
	"github.com/theatarr/theatarr/internal/metadata/anime/playlists"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/shadow"
	"github.com/theatarr/theatarr/internal/store"
)

type serveRuntime struct {
	Generation    int64
	ProviderEpoch int64
	Policy        *policy.Policy
	Gateway       *gateway.Gateway
	Indexers      *indexer.Service
	Providers     *providerSync
	Engine        *decide.Engine
	Acquirer      *acquire.Acquirer
	Router        *router.Router
	ProviderKeys  map[string]string
	// ReadyIndexers and ReadyProviders count the sources that would run (settings.Snapshot.Ready), for SetupCheck.
	ReadyIndexers, ReadyProviders int
	Continuity                    *providerContinuity
	EgressChecks                  *serveEgressChecks
	HTTP                          *serveHTTPRuntime
	Library                       *serveLibrary
	Playlists                     *playlists.Syncer
	Metadata                      *serveMetadata
	// Anime builds and refreshes anime franchises (metadata.anime, the anime metadata sources).
	Anime *autobuild.Service
	// Workers restarts the long-running loops whose settings this generation changed (serveliveworkers.go).
	Workers *serveWorkers
}

type serveEgressSource struct {
	manager  *settings.RuntimeCoordinator
	fallback *gateway.Gateway
}

func (s serveEgressSource) current() *gateway.Gateway {
	if s.manager != nil {
		if runtime, ok := s.manager.Current().Components.(*serveRuntime); ok && runtime != nil {
			return runtime.Gateway
		}
	}
	return s.fallback
}

func (s serveEgressSource) forContext(ctx context.Context) *gateway.Gateway {
	if s.manager != nil {
		if active := settings.ContextRuntime(ctx); active != nil {
			if runtime, ok := active.Components.(*serveRuntime); ok && runtime != nil {
				return runtime.Gateway
			}
		}
	}
	return s.current()
}

func (s serveEgressSource) EgressSummary() map[string]any { return s.current().EgressSummary() }
func (s serveEgressSource) State() gateway.State          { return s.current().State() }
func (s serveEgressSource) StateFor(ctx context.Context) gateway.State {
	return s.forContext(ctx).StateFor(ctx)
}

type shadowRuntimeSource struct{ manager *settings.RuntimeCoordinator }

func (s shadowRuntimeSource) Resolve(ctx context.Context, _ int64) (shadow.Runtime, bool) {
	current := settings.ContextRuntime(ctx)
	if current == nil {
		current = s.manager.Current()
	}
	components, ok := current.Components.(*serveRuntime)
	if !ok || components == nil {
		return shadow.Runtime{}, false
	}
	out := shadow.Runtime{Generation: current.Generation, Settings: current, Policy: current.Policy, Engine: components.Engine,
		Indexers: components.Indexers, Budgets: components.Gateway, Router: components.Router}
	// The arr import reads the instances of the import section in force.
	if current.Policy != nil && current.Policy.Import != nil && len(current.Policy.Import.Arr) > 0 {
		out.Arr = arrGetters(current.Policy)
	}
	return out, true
}

type serveRouter struct {
	manager  *settings.RuntimeCoordinator
	fallback *router.Router
}

func (s *serveRouter) forRequest(ctx context.Context, req *router.Request) (*router.Router, error) {
	if s.manager != nil {
		active := settings.ContextRuntime(ctx)
		if active == nil {
			active = s.manager.Current()
		}
		if active != nil {
			serve, ok := active.Components.(*serveRuntime)
			if ok && serve != nil && serve.Router != nil {
				return serve.Router, nil
			}
		}
	}
	if s.fallback == nil {
		return nil, errors.New("router: active settings runtime is unavailable")
	}
	return s.fallback, nil
}

func (s *serveRouter) forOperation(ctx context.Context) (context.Context, *router.Router) {
	if s.manager != nil {
		ctx = s.manager.Capture(ctx)
	}
	target, err := s.forRequest(ctx, nil)
	if err != nil {
		return ctx, nil
	}
	return ctx, target
}

// currentServeRouter is the published runtime's router (router.SetCurrentRuntime), or nil before one is published.
func currentServeRouter(m *settings.RuntimeCoordinator) *router.Router {
	if m == nil {
		return nil
	}
	active := m.Current()
	if active == nil {
		return nil
	}
	serve, _ := active.Components.(*serveRuntime)
	if serve == nil {
		return nil
	}
	return serve.Router
}

func (s *serveRouter) Enqueue(ctx context.Context, req router.Request, at time.Time) (int64, error) {
	target, err := s.forRequest(ctx, &req)
	if err != nil {
		return 0, err
	}
	return target.Enqueue(ctx, req, at)
}

func (s *serveRouter) EnqueueTx(ctx context.Context, tx *sql.Tx, req router.Request, at time.Time) (int64, error) {
	target, err := s.forRequest(ctx, &req)
	if err != nil {
		return 0, err
	}
	return target.EnqueueTx(ctx, tx, req, at)
}

func (s *serveRouter) Route(ctx context.Context, req router.Request) (*router.Result, error) {
	target, err := s.forRequest(ctx, &req)
	if err != nil {
		return nil, err
	}
	return target.Route(ctx, req)
}

func (r *serveRuntime) Activate() {
	if r != nil && r.Gateway != nil {
		if r.Router != nil && r.Continuity != nil {
			r.Router.PublishProviderChanges(r.Continuity.changed)
		}
		r.Gateway.Activate()
	}
}

func (r *serveRuntime) AfterPublish() {
	if r != nil && r.HTTP != nil {
		r.HTTP.services.Start(r.HTTP)
	}
	if r != nil && r.Gateway != nil {
		if r.EgressChecks != nil {
			r.EgressChecks.Start(r.Gateway)
		}
	}
	if r != nil {
		r.Workers.Apply(r)
	}
	// Turning router.wanted.retry_new off cancels what its chains still have queued (it does nothing while on).
	if r != nil && r.Acquirer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := r.Acquirer.CancelRetryNew(ctx); err != nil {
			slog.Warn("retry_new: queued searches not cancelled", "err", err)
		}
	}
}

func (r *serveRuntime) EgressGateway() *gateway.Gateway { return r.Gateway }

func runtimeForSearch(ctx context.Context, manager *settings.RuntimeCoordinator) (*serveRuntime, error) {
	if manager == nil {
		return nil, errors.New("settings runtime is unavailable")
	}
	active := settings.ContextRuntime(ctx)
	if active == nil {
		active = manager.Current()
	}
	runtime, ok := active.Components.(*serveRuntime)
	if !ok || runtime == nil {
		return nil, errors.New("settings runtime components are unavailable")
	}
	return runtime, nil
}

// runtimePolicy resolves the captured settings generation, else the active one. A missing coordinator returns nil.
func runtimePolicy(manager *settings.RuntimeCoordinator) func(context.Context) *policy.Policy {
	if manager == nil {
		return nil
	}
	return func(ctx context.Context) *policy.Policy {
		active := settings.ContextRuntime(ctx)
		if active == nil {
			active = manager.Current()
		}
		if active == nil {
			return nil
		}
		return active.Policy
	}
}

func inheritDecisionLibrary(next, previous *decide.Engine) {
	if next != nil && previous != nil {
		next.SetLibrary(previous.Library())
	}
}

func runtimeSearchers(manager *settings.RuntimeCoordinator) (acquire.SearchFunc, acquire.SearchBatchFunc,
	func(context.Context) *decide.Engine) {
	search := func(ctx context.Context, title decide.Title, version decide.Version, existing *decide.ExistingFile,
		series *decide.SeriesSearch) (*router.Searched, error) {
		runtime, err := runtimeForSearch(ctx, manager)
		if err != nil {
			return nil, err
		}
		return router.SearchRequest(decide.WithSearch(ctx), runtime.Engine, runtime.Indexers, title, version, existing, series)
	}
	batch := func(ctx context.Context, title decide.Title, targets []router.SearchTarget) (map[string]*router.Searched, error) {
		runtime, err := runtimeForSearch(ctx, manager)
		if err != nil {
			return nil, err
		}
		return router.SearchRequests(decide.WithSearch(ctx), runtime.Engine, runtime.Indexers, title, targets)
	}
	engine := func(ctx context.Context) *decide.Engine {
		runtime, err := runtimeForSearch(ctx, manager)
		if err != nil {
			return nil
		}
		return runtime.Engine
	}
	return search, batch, engine
}

func loadServePolicy(ctx context.Context, source, policyDir string, base *policy.Policy, db *store.Store) (*settings.Store, *settings.Snapshot, *policy.Policy, error) {
	if err := checkSettingsSource(source); err != nil {
		return nil, nil, nil, err
	}
	settingsStore, err := settings.New(db)
	if err != nil {
		return nil, nil, nil, err
	}
	settingsStore.SetTrashRoot(os.Getenv("THEATARR_TRASH"))
	// Confirmation effects resolve credential presence as the runtime's adders do, env fallback included.
	settingsStore.SetLookup(os.LookupEnv)
	snap, err := settingsStore.Load(ctx, base)
	if err != nil {
		return nil, nil, nil, err
	}
	settings.WarnForceOffUnknown(snap.Policy, slog.Default())
	return settingsStore, snap, snap.Policy, nil
}

func prepareStartupRuntime(ctx context.Context, settingsStore *settings.Store, base *policy.Policy, initial *settings.Snapshot,
	log *slog.Logger, prepare func(context.Context, *settings.Snapshot) (any, error)) (*settings.Snapshot, any, bool, error) {
	components, err := prepare(ctx, initial)
	if err == nil {
		return initial, components, false, nil
	}
	prepareErr := err
	previous, stateErr := settingsStore.FallbackGeneration(ctx, initial.Generation)
	if stateErr != nil {
		return nil, nil, false, errors.Join(prepareErr, stateErr)
	}
	if previous == 0 || previous == initial.Generation {
		return nil, nil, false, prepareErr
	}
	fallback, loadErr := settingsStore.LoadGeneration(ctx, base, previous)
	if loadErr != nil {
		return nil, nil, false, errors.Join(prepareErr, loadErr)
	}
	// LoadGeneration clamps the switches and versions to the head; anything else the fallback would loosen (a
	// safety section, a credential, a view, an indexer the head took away) refuses it, and the start fails with
	// both reasons instead of running looser settings than the ones saved.
	effects, effectsErr := settingsStore.FallbackEffects(ctx, initial, fallback)
	if effectsErr != nil {
		return nil, nil, false, errors.Join(prepareErr, effectsErr)
	}
	if len(effects) > 0 {
		list := make([]string, 0, len(effects))
		for _, e := range effects {
			list = append(list, strings.TrimSpace(e.Version+" "+e.Effect))
		}
		return nil, nil, false, errors.Join(prepareErr, fmt.Errorf("settings: not falling back to generation %d: it "+
			"would allow what the stored generation %d does not: %s", previous, initial.Generation, strings.Join(list, "; ")))
	}
	log.Error("SETTINGS STARTUP FALLBACK: stored generation could not be prepared; using last published generation",
		"stored_generation", initial.Generation, "fallback_generation", previous, "err", prepareErr)
	components, err = prepare(ctx, fallback)
	if err != nil {
		return nil, nil, true, errors.Join(prepareErr, err)
	}
	if err := settingsStore.PruneSnapshots(ctx, fallback.Generation); err != nil {
		return nil, nil, true, err
	}
	return fallback, components, true, nil
}

func prepareServeRuntime(ctx context.Context, snap *settings.Snapshot, previous *serveRuntime, st *store.Store,
	q *jobs.Queue, reg prometheus.Registerer, log *slog.Logger) (*serveRuntime, error) {
	if snap == nil || snap.Policy == nil {
		return nil, errors.New("serve settings snapshot required")
	}
	pol := snap.Policy
	active, priorities, err := runtimeServicePolicy(snap)
	if err != nil {
		return nil, err
	}
	apiKeys := map[string]string{}
	for _, record := range snap.Records.Indexers {
		if !record.Enabled {
			continue
		}
		var key string
		if err := snap.WithSecret("indexers", record.Key, os.LookupEnv, func(value string) error {
			key = value
			return nil
		}); err != nil {
			return nil, err
		}
		if key != "" {
			apiKeys[record.Key] = key
		}
	}
	providerKeys := map[string]string{}
	for _, record := range snap.Records.Providers {
		if !record.Enabled {
			continue
		}
		var key string
		if err := snap.WithSecret("providers", record.Key, os.LookupEnv, func(value string) error {
			key = value
			return nil
		}); err != nil {
			return nil, err
		}
		providerKeys[record.Key] = key
	}
	// The gateway masks the DB-backed credentials of this generation in its logs and errors.
	var secrets []string
	for _, keys := range []map[string]string{apiKeys, providerKeys} {
		for _, key := range keys {
			if key != "" {
				secrets = append(secrets, key)
			}
		}
	}
	// So are the passwords of the download clients a client-routed version sends to (router.BuildClients).
	for _, c := range active.Clients {
		if v := active.Getenv(c.PasswordEnv); c.Kind == "decypharr" && c.PasswordEnv != "" && v != "" {
			secrets = append(secrets, v)
		}
	}
	var oldGateway *gateway.Gateway
	if previous != nil {
		oldGateway = previous.Gateway
	}
	gw, err := gateway.New(ctx, active, gateway.Options{Store: st, Logger: log,
		Version: buildinfo.Version, Previous: oldGateway, Generation: snap.Generation, Secrets: secrets})
	if err != nil {
		return nil, err
	}
	ps, err := newProviderSyncFromSettings(snap, st, q, gw, log, os.LookupEnv)
	if err != nil {
		return nil, err
	}
	idx, err := indexer.New(active, gw, indexer.Options{Logger: log, APIKeys: apiKeys, Priority: priorities,
		Cardigann: cardigannManager()})
	if err != nil {
		return nil, err
	}
	var previousEngine *decide.Engine
	if previous != nil {
		previousEngine = previous.Engine
	}
	eng, err := newShadowEngine(pol, log, previousEngine)
	if err != nil {
		return nil, err
	}
	if previous != nil {
		inheritDecisionLibrary(eng, previous.Engine)
	}
	probeCtx := settings.WithRuntime(ctx, &settings.Runtime{Generation: snap.Generation, ProviderEpoch: snap.ProviderEpoch, Components: &serveRuntime{Gateway: gw}})
	continuity, err := validateProviderContinuity(probeCtx, st, snap, previous, providerKeys, ps.syncer.Providers)
	if err != nil {
		return nil, err
	}
	epoch := preparedProviderEpoch(snap, continuity)
	// The registry sync of this runtime writes only while its epoch is current: a listing an account change
	// overtook is discarded.
	ps.syncer.SettingsManaged, ps.syncer.ProviderEpoch = true, epoch
	adders, skipped := router.BuildAddersWithKeys(active, gw, providerKeys, log)
	if len(skipped) > 0 {
		log.Info("router: providers with no key set are skipped", "providers", skipped)
	}
	// The ranking's cache tie-break counts the providers routes can add to: enabled, with a key.
	eng.SetUsableProviders(func(key string) bool { return adders[key] != nil })
	var previousRouter *router.Router
	if previous != nil {
		previousRouter = previous.Router
	}
	rt, err := router.New(router.Config{Store: st, Policy: active, Adders: adders, Previous: previousRouter,
		Generation: snap.Generation, SettingsManaged: true, ProviderEpoch: epoch, ProviderKeys: providerKeys,
		Providers: ps.syncer.Providers, Clients: router.BuildClients(active, gw, active.Getenv, log), Logger: log,
		Queue: q})
	if err != nil {
		return nil, err
	}
	rt.SetDecider(eng.Decide, eng.DecideSearch)
	rt.SetProfiles(eng.Profile)
	if err := gw.RegisterMetrics(reg); err != nil {
		return nil, err
	}
	readyIndexers, readyProviders := snap.Ready(os.LookupEnv)
	return &serveRuntime{Generation: snap.Generation, ProviderEpoch: epoch, Policy: pol, Gateway: gw, Indexers: idx, Providers: ps, Engine: eng,
		Router: rt, ProviderKeys: providerKeys, ReadyIndexers: readyIndexers, ReadyProviders: readyProviders,
		Continuity: continuity}, nil
}

func runtimeServicePolicy(snap *settings.Snapshot) (*policy.Policy, map[string]int, error) {
	active, err := snap.Policy.Clone()
	if err != nil {
		return nil, nil, errors.New("serve: cannot copy runtime policy")
	}
	priorities := map[string]int{}
	for _, record := range snap.Records.Indexers {
		if !record.Enabled {
			delete(active.Indexers, record.Key)
			continue
		}
		priorities[record.Key] = record.Priority
	}
	for _, record := range snap.Records.Providers {
		if !record.Enabled {
			delete(active.Providers, record.Key)
		}
	}
	return active, priorities, nil
}

func newProviderSyncFromSettings(snap *settings.Snapshot, st *store.Store, q *jobs.Queue, d provider.Doer,
	log *slog.Logger, lookup settings.LookupSecret) (*providerSync, error) {
	if snap == nil || snap.Policy == nil {
		return nil, errors.New("provider sync: settings snapshot required")
	}
	ps := &providerSync{st: st, q: q, log: log.With("component", "provider-sync"),
		interval: snap.Policy.ProviderSyncInterval(), delay: time.Minute}
	s := &provider.Syncer{Store: st, Providers: map[string]provider.Provider{}, Log: log, Queue: q}
	for _, r := range snap.Records.Providers {
		if r.Enabled {
			s.Configured = append(s.Configured, r.Key)
		}
	}
	sort.Slice(s.Configured, func(i, j int) bool {
		return snap.Policy.Providers[s.Configured[i]].Order < snap.Policy.Providers[s.Configured[j]].Order
	})
	var skipped []string
	for _, key := range s.Configured {
		pv := snap.Policy.Providers[key]
		if pv == nil {
			continue
		}
		apiKey := ""
		if err := snap.WithSecret("providers", key, lookup, func(value string) error {
			apiKey = value
			return nil
		}); err != nil {
			return nil, err
		}
		if apiKey == "" {
			skipped = append(skipped, key+" ("+pv.APIKeyEnv+")")
			continue
		}
		client, err := provider.New(key, pv, apiKey, d)
		if err != nil {
			return nil, err
		}
		s.Providers[key] = client
		ps.keys = append(ps.keys, key)
	}
	ps.syncer = s
	if len(skipped) > 0 {
		ps.log.Info("provider sync skips providers with no key set", "providers", skipped)
	}
	return ps, nil
}
