package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer/cardigann"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

// cardigannCatalog is theatarr serve's definition catalog (the definitions sync source, docs/design/cardigann.md) and the
// engine that every runtime generation's cardigann indexers share, so a site's session outlives a settings reload.
//
// Its settings follow the settings generation in force: the repository and branch a refresh reads, the FlareSolverr
// URL a search uses and the Prowlarr reader are read from the current runtime's cardigann section at use, and theatarr
// serve restarts the refresh loop (Run) when the refresh interval or source changes (serveliveworkers.go). The store
// and the engine stay the same across generations, so indexers built earlier keep working and keep their sessions.
type cardigannCatalog struct {
	store   *cardigann.Store
	manager *cardigann.Manager
	// pol is the policy the catalog was built from: the settings when no runtime answers (other commands, tests).
	pol     *policy.Policy
	current cardigannRuntime
}

// serveCardigann is set by theatarr serve when cardigann.definitions is configured. Other commands run no catalog: their
// cardigann indexers answer ErrNoCatalog.
var serveCardigann atomic.Pointer[cardigannCatalog]

// cardigannManager is the serve engine, or nil.
func cardigannManager() *cardigann.Manager {
	if c := serveCardigann.Load(); c != nil {
		return c.manager
	}
	return nil
}

// cardigannRuntime returns the gateway and policy that a definitions fetch goes through: the active runtime's.
type cardigannRuntime func(ctx context.Context) (*gateway.Gateway, *policy.Policy, error)

// cardigannRetry is how soon a failed definitions refresh is tried again.
const cardigannRetry = time.Hour

// newCardigannCatalog builds the catalog over <data dir>/cardigann/definitions; nil when cardigann.definitions is
// unset (nothing is fetched then).
func newCardigannCatalog(pol *policy.Policy, dataDir string, st *store.Store, current cardigannRuntime, log *slog.Logger) (*cardigannCatalog, error) {
	if pol.Cardigann == nil || pol.Cardigann.Definitions == nil {
		return nil, nil
	}
	log = log.With("component", "cardigann")
	c := &cardigannCatalog{pol: pol, current: current}
	cs := &cardigann.Store{Dir: filepath.Join(dataDir, "cardigann", "definitions"), Logger: log,
		Fetch: cardigannFetcher{catalog: c},
		InUse: func() []string {
			if _, p, err := current(context.Background()); err == nil && p != nil {
				return p.CardigannDefinitionIDs()
			}
			return pol.CardigannDefinitionIDs()
		}}
	sessions, err := settings.NewCardigannSessions(st)
	if err != nil {
		return nil, err
	}
	m := cardigann.NewManager(cs, func() string {
		if s := c.settings(context.Background()); s != nil {
			return s.FlareSolverrURL
		}
		return ""
	})
	m.Sessions, m.Logger = sessions, log
	c.store, c.manager = cs, m
	return c, nil
}

// settings is the cardigann section in force for ctx: the captured or current runtime's, else the catalog's own.
func (c *cardigannCatalog) settings(ctx context.Context) *policy.Cardigann {
	return c.policy(ctx).Cardigann
}

// policy is the policy of the runtime in force, else the one the catalog was built from.
func (c *cardigannCatalog) policy(ctx context.Context) *policy.Policy {
	if c.current != nil {
		if _, p, err := c.current(ctx); err == nil && p != nil {
			return p
		}
	}
	return c.pol
}

// source is the repository and branch a refresh reads.
func (c *cardigannCatalog) source(ctx context.Context) settings.QualitySyncConfig {
	var d *policy.CardigannDefinitions
	if s := c.settings(ctx); s != nil {
		d = s.Definitions
	}
	return settings.QualitySyncConfig{Repo: d.RepoOrDefault(), Branch: d.BranchOrDefault()}
}

// Run refreshes the catalog when due until ctx ends, every refresh interval of the settings in force at its start.
func (c *cardigannCatalog) Run(ctx context.Context) {
	var d *policy.CardigannDefinitions
	if s := c.settings(ctx); s != nil {
		d = s.Definitions
	}
	c.store.Run(ctx, d.RefreshOrDefault(), cardigannRetry)
}

// cardigannEnsure makes the serve catalog the first time a settings generation configures cardigann.definitions, so
// a section saved after start needs no restart. An existing catalog is kept: its settings follow each generation.
func cardigannEnsure(pol *policy.Policy, dataDir string, st *store.Store, current cardigannRuntime, log *slog.Logger) error {
	if serveCardigann.Load() != nil || pol == nil || pol.Cardigann == nil || pol.Cardigann.Definitions == nil {
		return nil
	}
	c, err := newCardigannCatalog(pol, dataDir, st, current, log)
	if err != nil || c == nil {
		return err
	}
	serveCardigann.CompareAndSwap(nil, c)
	return nil
}

// cardigannSecretSettings is the settings store's view of a definition's credential settings (settings.CardigannSecrets):
// the serve catalog's entry, by setting type and name; unknown while no catalog holds the definition.
func cardigannSecretSettings(id string) (map[string]bool, bool) {
	c := serveCardigann.Load()
	if c == nil || c.store == nil {
		return nil, false
	}
	sum, ok := c.store.Summary(id)
	if !ok {
		return nil, false
	}
	secret := map[string]bool{}
	for _, st := range sum.Settings {
		secret[st.Name] = st.Secret
	}
	return secret, true
}

// cardigannWorkerKey is the refresh loop's configuration in pol: its source and interval, "" when the catalog is off.
func cardigannWorkerKey(pol *policy.Policy) string {
	if pol == nil || pol.Cardigann == nil || pol.Cardigann.Definitions == nil {
		return ""
	}
	d := pol.Cardigann.Definitions
	return d.RepoOrDefault() + "@" + d.BranchOrDefault() + " every " + d.RefreshOrDefault().String()
}

// The web.CardigannCatalog methods.

func (c *cardigannCatalog) Catalog() ([]cardigann.Summary, error)       { return c.store.Catalog() }
func (c *cardigannCatalog) Summary(id string) (cardigann.Summary, bool) { return c.store.Summary(id) }
func (c *cardigannCatalog) Status() cardigann.Status                    { return c.store.Status() }
func (c *cardigannCatalog) Holds() []cardigann.Hold                     { return c.store.Holds() }
func (c *cardigannCatalog) Approve(id string) error                     { return c.store.Approve(id) }
func (c *cardigannCatalog) Refresh(ctx context.Context) error           { return c.store.Refresh(ctx) }

// errNoProwlarr answers a proposal request without cardigann.prowlarr.
var errNoProwlarr = errors.New("cardigann.prowlarr is not configured")

// ProwlarrProposals reads Prowlarr's indexer list (GET only, on the LAN, like import.arr) and proposes native
// indexers for it. Nothing is written anywhere.
func (c *cardigannCatalog) ProwlarrProposals(ctx context.Context) ([]cardigann.Proposal, error) {
	var prowlarr *policy.CardigannProwlarr
	pol := c.policy(ctx)
	if s := pol.Cardigann; s != nil {
		prowlarr = s.Prowlarr
	}
	if prowlarr == nil {
		return nil, errNoProwlarr
	}
	key := pol.Getenv(prowlarr.APIKeyEnv)
	if key == "" {
		return nil, fmt.Errorf("cardigann.prowlarr: env %s is not set", prowlarr.APIKeyEnv)
	}
	cl, err := arr.New(prowlarr.URL, key, arr.Options{Prowlarr: true, Retries: 1})
	if err != nil {
		return nil, err
	}
	var list []cardigann.ProwlarrIndexer
	if err := cl.GetJSON(ctx, "indexer", nil, &list); err != nil {
		return nil, err
	}
	return cardigann.ProposeFromProwlarr(list, c.store.Summary), nil
}

// cardigannFetcher reads the definitions repository through the gateway, on its own budget, as the quality sync's
// tracked sources do.
// The repository and branch are the runtime's at each call.
type cardigannFetcher struct {
	catalog *cardigannCatalog
}

func (f cardigannFetcher) Commit(ctx context.Context) (string, error) {
	gw, p, err := f.catalog.current(ctx)
	if err != nil {
		return "", err
	}
	return gw.CardigannCommit(ctx, p, settings.TrackedCommitURL(f.catalog.source(ctx)))
}

func (f cardigannFetcher) Archive(ctx context.Context, commit string) ([]byte, error) {
	gw, p, err := f.catalog.current(ctx)
	if err != nil {
		return nil, err
	}
	return gw.CardigannArchive(ctx, p, settings.TrackedArchiveURL(f.catalog.source(ctx), commit), cardigann.MaxArchiveBytes)
}
