package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/buildinfo"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/importarr"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

func init() {
	register(command{
		name:    "import-arr",
		summary: "read arr instances (GET only) into the database: titles, versions, files, history, registry",
		run:     runImportArr,
	})
}

func runImportArr(args []string) error {
	fs := flag.NewFlagSet("import-arr", flag.ContinueOnError)
	settingsSourceFlag(fs)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: theatarr import-arr [flags] [instance ...]\n\n"+
			"Imports every instance under the policy's import.arr, or the ones named. The arrs get GET requests only.\n\n")
		fs.PrintDefaults()
	}
	db := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	policyDir := policyDirDefault()
	envFile := fs.String("env-file", "", "read KEY=VALUE secrets from this file (variables already set win)")
	enrich := fs.Bool("enrich", false, "fill missing metadata from TMDb (live, paced)")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envFile != "" {
		if err := loadEnvFile(*envFile); err != nil {
			return err
		}
	}
	pol, err := loadCommandPolicy(fs, policyDir)
	if err != nil {
		return err
	}
	if pol.Import == nil || len(pol.Import.Arr) == 0 {
		return errors.New("the policy has no import.arr section")
	}
	names := fs.Args()
	if len(names) == 0 {
		for n := range pol.Import.Arr {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if pol.Import.Arr[n] == nil {
			return fmt.Errorf("instance %q is not under import.arr in the policy", n)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, err := store.Open(ctx, *db, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		return err
	}

	im := &importarr.Importer{Store: st}
	var tc *tmdb.Client
	if *enrich {
		src := (*policy.MetadataSource)(nil)
		if pol.Metadata != nil {
			src = pol.Metadata.Sources["tmdb"]
		}
		if src == nil || src.URL == "" {
			return errors.New("--enrich needs metadata.sources.tmdb.url in the policy")
		}
		if src.APIKeyEnv == "" {
			return errors.New("--enrich needs metadata.sources.tmdb.api_key_env in the policy")
		}
		key := pol.Getenv(src.APIKeyEnv)
		if key == "" {
			return fmt.Errorf("--enrich: %s is not set", src.APIKeyEnv)
		}
		// TMDb goes through the gateway: host allowlist, the tmdb budget, back-off and metrics.
		gw, err := gateway.New(ctx, pol, gateway.Options{Store: st, Version: buildinfo.Version,
			Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))})
		if err != nil {
			return err
		}
		tc, err = tmdb.New(tmdb.Options{BaseURL: src.URL, APIKey: key,
			Doer:  gatewayDoer{gw: gw, target: gateway.Target{Class: gateway.ClassMetadata, Key: "tmdb"}},
			Cache: &tmdb.SQLCache{Store: st, Source: "tmdb"}})
		if err != nil {
			return err
		}
		im.ResolveIMDb = tc.FindByIMDb
	}

	rep := &importarr.Report{}
	for _, n := range names {
		a := pol.Import.Arr[n]
		key := pol.Getenv(a.APIKeyEnv)
		if key == "" {
			return fmt.Errorf("%s: %s is not set", n, a.APIKeyEnv)
		}
		g, err := arr.New(a.URL, key, arr.Options{Timeout: 120 * time.Second})
		if err != nil {
			return err
		}
		v := pol.Versions[a.Version]
		fmt.Fprintf(os.Stderr, "importing %s -> %s ...\n", n, a.Version)
		t0 := time.Now()
		sum, err := im.Run(ctx, importarr.Source{
			Instance: n, Flavour: a.Flavour, Version: a.Version, Anime: v.Anime, Library: v.Library,
			Followers: pol.Followers(a.Version), CollectionTargets: pol.CollectionTargets(),
		}, g)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "  done in %s\n", time.Since(t0).Round(time.Millisecond))
		rep.Instances = append(rep.Instances, sum)
	}
	if tc != nil {
		fmt.Fprintln(os.Stderr, "enriching from TMDb ...")
		es, err := importarr.Enrich(ctx, st, tc, func() int64 { return store.Millis(time.Now()) })
		if es != nil {
			es.Requests, es.CacheHits = tc.Requests, tc.Hits
			rep.Enrich = es
		}
		if err != nil {
			return err
		}
	}
	if rep.Versions, err = importarr.VersionReport(ctx, st); err != nil {
		return err
	}
	if rep.Conflicts, err = importarr.Conflicts(ctx, st); err != nil {
		return err
	}
	if rep.Totals.Titles, rep.Totals.Contents, err = importarr.Totals(ctx, st); err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	rep.WriteText(os.Stdout)
	return nil
}

// gatewayDoer sends a request through the gateway for one target (tmdb.Doer).
type gatewayDoer struct {
	gw     *gateway.Gateway
	target gateway.Target
}

func (d gatewayDoer) Do(r *http.Request) (*http.Response, error) {
	return d.gw.Do(r.Context(), d.target, r)
}

// loadEnvFile sets KEY=VALUE lines from path into the environment, leaving variables that are already set. Values
// are never printed.
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		s = strings.TrimPrefix(s, "export ")
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			return fmt.Errorf("%s:%d: not KEY=VALUE", path, line)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
	return sc.Err()
}
