package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/theatarr/theatarr/internal/arrview"
	"github.com/theatarr/theatarr/internal/buildinfo"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/plex"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/preflight"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/web"
)

func init() {
	register(command{
		name: "preflight",
		summary: "check a fresh or running install: mount, roots and trash, providers, indexers, metadata keys, " +
			"Plex, the VPN egress (docs/quickstart.md)",
		run: func(args []string) error { return runPreflight(args, os.Stdout) },
	})
}

func runPreflight(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	settingsSourceFlag(fs)
	dbPath := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB); read only, never migrated")
	asJSON := fs.Bool("json", false, "print JSON")
	sources := fs.Bool("sources", true, "test the providers (a read-only account call), the indexers (one probe "+
		"query each, through the VPN guard) and Plex; false checks only what needs no outbound call")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	source := fs.Lookup("settings-source").Value.String()
	pol, snap, db, err := preflightPolicy(ctx, source, policyDirDefault(), *dbPath)
	if db != nil {
		defer db.Close()
	}
	var validation *policy.Errors
	if err != nil && (pol == nil || !errors.As(err, &validation)) {
		return err
	}
	validationErr := err
	lib := pol.LibrarySettings()
	o := preflight.Options{Policy: pol, ValidationError: validationErr, FS: preflight.OSFS{}, Device: preflight.OSDevice, APIKeys: -1,
		Credentials: settings.Credentials{Policy: pol, Snap: snap, Lookup: os.LookupEnv},
		Mount: func(ctx context.Context, dir string) health.Result {
			return health.Mount(dir, lib.StatTimeout)(ctx)
		}}
	if snap != nil {
		// pol is the running set, without the disabled records: preflight names them when none is enabled.
		o.Disabled = map[string][]string{}
		for _, r := range snap.Records.Providers {
			if !r.Enabled {
				o.Disabled["providers"] = append(o.Disabled["providers"], r.Key)
			}
		}
		for _, r := range snap.Records.Indexers {
			if !r.Enabled {
				o.Disabled["indexers"] = append(o.Disabled["indexers"], r.Key)
			}
		}
	}
	if db != nil {
		var n int
		if err := db.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM api_key WHERE revoked_at IS NULL`).Scan(&n); err == nil {
			o.APIKeys = n
		}
	}
	if *sources && validationErr == nil {
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		// No store: an offline check must not touch the live budget counters; the gateway keeps them in memory.
		gw, err := gateway.New(ctx, pol, gateway.Options{Logger: log, Version: buildinfo.Version})
		if err != nil {
			return err
		}
		if pol.Egress != nil {
			gw.CheckEgress(ctx)
			res := health.Egress(gw.EgressSummary, true)(ctx)
			res.Source = "VpnEgressCheck"
			o.Health = append(o.Health, res)
		}
		secret := func(entity, key, env string) string {
			var v string
			if snap != nil {
				_ = snap.WithSecret(entity, key, os.LookupEnv, func(s string) error { v = s; return nil })
				return v
			}
			if env != "" {
				v = pol.Getenv(env)
			}
			return v
		}
		o.Sources = func(ctx context.Context, entity string) []preflight.Source {
			return offlineSources(ctx, pol, gw, entity, secret, log)
		}
		o.Plex = func(ctx context.Context) error { return preflightPlex(ctx, pol, gw) }
	}
	r := preflight.Run(ctx, o)
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(r); err != nil {
			return err
		}
	} else {
		preflight.Print(out, r)
	}
	if r.Fail > 0 {
		return fmt.Errorf("%d check(s) failed", r.Fail)
	}
	return nil
}

// preflightPolicy is the policy the service would run with. Unlike the other offline commands, a missing database,
// a database with no settings yet, or an empty policy directory is a fresh install, not an error. The database is
// opened read only in effect: nothing is migrated or written.
func preflightPolicy(ctx context.Context, source, dir, path string) (*policy.Policy, *settings.Snapshot, *store.Store, error) {
	if err := checkSettingsSource(source); err != nil {
		return nil, nil, nil, err
	}
	fresh := func() (*policy.Policy, error) { return databasePolicyBase(dir) }
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		p, err := fresh()
		return p, nil, nil, err
	} else if err != nil {
		return nil, nil, nil, err
	}
	db, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		return nil, nil, nil, err
	}
	var generation int64
	if err := db.Reader().QueryRowContext(ctx, "SELECT COALESCE(MAX(generation),0) FROM settings_generation").Scan(&generation); err != nil || generation == 0 {
		p, err := fresh()
		return p, nil, db, err
	}
	base, err := databasePolicyBase(dir)
	if err == nil {
		var st *settings.Store
		if st, err = settings.New(db); err == nil {
			var snap *settings.Snapshot
			if snap, err = st.LoadDiagnostic(ctx, base); snap != nil {
				validationErr := err
				settings.ApplyForceOff(snap.Policy)
				active, _, runtimeErr := runtimeServicePolicy(snap)
				return active, snap, db, errors.Join(validationErr, runtimeErr)
			}
		}
	}
	db.Close()
	return nil, nil, nil, err
}

// offlineSources tests every provider or indexer of pol as the settings tests do, through gw.
func offlineSources(ctx context.Context, pol *policy.Policy, gw *gateway.Gateway, entity string,
	secret func(entity, key, env string) string, log *slog.Logger) []preflight.Source {
	var out []preflight.Source
	switch entity {
	case "providers":
		for _, k := range sortedPolicyKeys(pol.Providers) {
			res := testProviderSource(ctx, pol, gw, k, secret(entity, k, pol.Providers[k].APIKeyEnv))
			out = append(out, preflightSource(k, res))
		}
	case "indexers":
		for _, k := range sortedPolicyKeys(pol.Indexers) {
			res := testIndexerSource(ctx, pol, gw, k, secret(entity, k, pol.Indexers[k].APIKeyEnv), log)
			s := preflightSource(k, res)
			s.Caps = indexerCaps(pol, gw, k, log)
			out = append(out, s)
		}
	}
	return out
}

func sortedPolicyKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func preflightSource(key string, res web.SettingsTestOutcome) preflight.Source {
	s := preflight.Source{Key: key, Status: res.Status, Category: res.Category, Message: web.SettingsTestMessage(res.Category),
		Results: res.Results}
	if res.Account != nil {
		s.Premium, s.Expires = res.Account.Premium, res.Account.Expires
	}
	return s
}

// indexerCapsQueries are the query types preflight asks an indexer about, with what a search of each would carry.
var indexerCapsQueries = []struct {
	name string
	q    indexer.Query
}{
	{"rss", indexer.Query{Type: indexer.QueryRSS, RSSLimit: 5}},
	{"movie", indexer.Query{Type: indexer.QueryMovie, IMDb: "tt0111161", TMDb: 278, Title: "The Shawshank Redemption", Year: 1994}},
	{"series", indexer.Query{Type: indexer.QuerySeries, TVDB: 81189, IMDb: "tt0903747", Title: "Breaking Bad", Season: 1}},
	{"text", indexer.Query{Type: indexer.QueryText, Title: "The Shawshank Redemption"}},
}

// indexerCaps lists the query types an indexer's adapter answers. Nothing is sent.
func indexerCaps(pol *policy.Policy, gw *gateway.Gateway, key string, log *slog.Logger) []string {
	ix := pol.Indexers[key]
	if ix == nil {
		return nil
	}
	svc, err := indexer.New(&policy.Policy{Indexers: map[string]*policy.Indexer{key: ix}}, gw,
		indexer.Options{Logger: log, CacheTTL: -1, Cardigann: cardigannManager().Throwaway()})
	if err != nil {
		return nil
	}
	sr := svc.Searcher(key)
	if sr == nil {
		return nil
	}
	var out []string
	for _, c := range indexerCapsQueries {
		if sr.Supports(c.q) {
			out = append(out, c.name)
		}
	}
	return out
}

// preflightPlex asks the Plex server for its identity (no token needed, but the client wants one) through gw.
func preflightPlex(ctx context.Context, pol *policy.Policy, gw *gateway.Gateway) error {
	p := pol.LibrarySettings().Plex
	if p == nil {
		return errors.New("library.plex is not set")
	}
	c, err := plex.New(gw, p.URL, pol.Getenv(p.TokenEnv))
	if err != nil {
		return err
	}
	_, err = c.Identity(ctx)
	return err
}

// servePreflightHooks are the preflight checks of the running service: the mount through the library's guard, Plex
// and the indexers through the current generation's gateway, and the arr views on the service's own handler.
func servePreflightHooks(views func(ctx context.Context) *arrview.Service, gwFor func(ctx context.Context) (*gateway.Gateway, *policy.Policy),
	lib func() *library.Library, handler func() http.Handler, log *slog.Logger) web.PreflightHooks {
	return web.PreflightHooks{
		Mount: func(ctx context.Context, dir string) health.Result {
			if l := lib(); l != nil && l.MountBreaker().Helper != nil {
				return health.MountAnswering(dir, l.MountAnswers)(ctx)
			}
			_, pol := gwFor(ctx)
			return health.Mount(dir, pol.LibrarySettings().StatTimeout)(ctx)
		},
		Plex: func(ctx context.Context) error {
			gw, pol := gwFor(ctx)
			return preflightPlex(ctx, pol, gw)
		},
		Caps: func(key string) []string {
			gw, pol := gwFor(context.Background())
			return indexerCaps(pol, gw, key, log)
		},
		Views: func(ctx context.Context) []preflight.View {
			av, h := views(ctx), handler()
			if av == nil || h == nil {
				return nil
			}
			var out []preflight.View
			for _, v := range av.Views() {
				out = append(out, probeView(ctx, h, v.Base))
			}
			return out
		},
	}
}

// probeView asks one view's system/status on the service's own handler, without a key (the arrs answer 401).
func probeView(ctx context.Context, h http.Handler, base string) preflight.View {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, filepath.ToSlash(base)+"/api/v3/system/status", nil)
	if err != nil {
		return preflight.View{Path: base, Err: err.Error()}
	}
	req.RemoteAddr = "127.0.0.1:0"
	rec := &statusRecorder{header: http.Header{}}
	h.ServeHTTP(rec, req)
	return preflight.View{Path: base, Code: rec.code}
}

// statusRecorder keeps only the status code of a response.
type statusRecorder struct {
	header http.Header
	code   int
}

func (r *statusRecorder) Header() http.Header { return r.header }

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return len(b), nil
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
}
