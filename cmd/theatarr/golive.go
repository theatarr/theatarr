package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/golive"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

func init() {
	register(command{
		name:    "golive",
		summary: "golive check: read-only preflight before a library grabs (docs/migration.md#4-the-go-live-check)",
		run:     func(args []string) error { return runGolive(args, os.Stdout) },
	})
}

const goliveUsage = `usage:
  theatarr golive check [-library KEY] [flags]   read-only preflight before a library grabs (default: every library):
                                                PASS/INFO/WARN/FAIL lines with the fix for each, exit 1 on any FAIL.
                                                In the container: docker exec theatarr theatarr golive check
`

func runGolive(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprint(os.Stderr, goliveUsage)
		if helpArg(args) {
			return flag.ErrHelp
		}
		return errors.New("give check")
	}
	fs := flag.NewFlagSet("golive check", flag.ContinueOnError)
	settingsSourceFlag(fs)
	fs.Usage = func() { fmt.Fprint(fs.Output(), goliveUsage+"\nflags:\n"); fs.PrintDefaults() }
	policyDir := policyDirDefault()
	dbPath := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB), opened read only; 'none' skips the "+
		"database checks")
	envFile := fs.String("env-file", "", "read KEY=VALUE secrets from this file (variables already set win); only "+
		"whether each is set is printed")
	backupDir := fs.String("backup-dir", envOr("THEATARR_BACKUP_DIR", ""), "backup directory, as theatarr serve's (default "+
		"backups/ next to the database)")
	trash := fs.String("trash", os.Getenv("THEATARR_TRASH"), "TRaSH-Guides docs/json, for trash_id formats (env THEATARR_TRASH)")
	version := libraryFlag(fs, "the library to check (default: every library)")
	service := fs.String("service", "http://127.0.0.1:7700", "the running theatarr serve, for VPN egress and indexer "+
		"back-offs from /metrics; 'none' skips")
	publicURL := fs.String("public-url", envOr("THEATARR_PUBLIC_URL", ""), "Theatarr's address as clients use it (env "+
		"THEATARR_PUBLIC_URL), for the client changes")
	days := fs.Int("days", 14, "shadow evidence window in days")
	bar := fs.Float64("bar", 0, "agreement bar, 0 to 1 (default the shadow report's, 0.95)")
	minGrabs := fs.Int("min-grabs", 20, "fewest arr grabs in the window worth judging")
	noArr := fs.Bool("no-arr", false, "do not read the owning arr instances (GET)")
	noEngine := fs.Bool("no-engine", false, "do not build the profiles (from_arr profiles GET the arr)")
	scan := fs.Bool("scan", true, "walk the library's roots read-only (library verify)")
	expiry := fs.String("provider-expiry", envOr("THEATARR_PROVIDER_EXPIRY", ""), "known plan ends, key=YYYY-MM-DD "+
		"comma-separated (env THEATARR_PROVIDER_EXPIRY), e.g. tb=2027-01-31: a WARN within 30 days")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if *envFile != "" {
		if err := loadEnvFile(*envFile); err != nil {
			return err
		}
	}
	ctx := context.Background()
	pol, snap, err := loadCommandSettings(fs, policyDir)
	if err != nil {
		return err
	}
	ends, err := parseExpiry(*expiry)
	if err != nil {
		return err
	}
	versions := golive.Versions(pol)
	if *version != "" {
		if pol.Versions[*version] == nil {
			return fmt.Errorf("the policy has no library %q (have %s)", *version, strings.Join(versions, ", "))
		}
		versions = []string{*version}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Credentials resolve as serve resolves them: stored in the settings database, or named env variables.
	creds := settings.Credentials{Policy: pol, Snap: snap, Lookup: os.LookupEnv}
	o := golive.Options{Policy: pol, Env: os.Getenv, Credential: creds.Resolves, FS: golive.OSFS{},
		PublicURL: *publicURL, ProviderExpiry: ends, Days: *days, Bar: *bar, MinGrabs: *minGrabs}

	var st *store.Store
	if *dbPath != "none" {
		if _, err := os.Stat(*dbPath); err != nil {
			return fmt.Errorf("%w (give -db none to skip the database checks)", err)
		}
		// The store's reader pool is mode=ro and query_only; nothing here uses the writer, and nothing migrates.
		if st, err = store.Open(ctx, *dbPath, store.Options{}); err != nil {
			return err
		}
		defer st.Close()
		cur, target, err := st.SchemaVersion(ctx)
		if err != nil {
			return err
		}
		if cur != target {
			return fmt.Errorf("the database is at schema %d and this binary expects %d: run the check with the binary "+
				"theatarr serve runs (docker exec theatarr theatarr golive check)", cur, target)
		}
		o.DB = st.Reader()
		o.Health = goliveHealth(ctx, pol, snap, st, *dbPath, *backupDir, log)
		if *scan {
			lib := library.New(library.Options{Store: st, Policy: pol, Logger: log})
			o.Scan = lib.Verify
		}
	}
	if *service != "none" {
		if s, err := readServiceState(ctx, *service); err == nil {
			o.Service = s
		} else {
			fmt.Fprintf(os.Stderr, "note: no service at %s (%v); VPN egress is not checked\n", *service, err)
		}
	}
	if !*noArr {
		o.Arr = goliveArr(pol)
	}
	if !*noEngine {
		e, berr := decide.New(pol, decide.Options{TrashRoot: *trash, Arr: liveArr(pol)})
		o.Profile = func(name string, kind policy.Kind) error {
			fl := format.Radarr
			if kind == policy.KindSeries {
				fl = format.Sonarr
			}
			if e != nil && e.Profile(name, fl) != nil {
				return nil
			}
			if berr != nil && strings.Contains(berr.Error(), name) {
				return profileError(berr, name)
			}
			return errors.New("not built")
		}
	}

	var reports []*golive.Report
	failed := false
	for _, k := range versions {
		o.Version = k
		r, err := golive.Run(ctx, o)
		if err != nil {
			return err
		}
		reports = append(reports, r)
		failed = failed || r.Failed()
		if !*asJSON {
			golive.Print(out, r)
			fmt.Fprintln(out)
		}
	}
	if *asJSON {
		b, _ := json.MarshalIndent(reports, "", "  ")
		fmt.Fprintln(out, string(b))
	}
	if failed {
		return errors.New("a check failed: not ready to go live")
	}
	return nil
}

// goliveArr opens the import.arr instances the check reads (GET only), with the keys their api_key_env name.
func goliveArr(pol *policy.Policy) func(string) (golive.ArrGetter, error) {
	return func(inst string) (golive.ArrGetter, error) {
		a := pol.Import.Arr[inst]
		key := pol.Getenv(a.APIKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("%s is not set", a.APIKeyEnv)
		}
		return arr.New(a.URL, key, arr.Options{Retries: 1})
	}
}

// profileError keeps the lines of the engine's build error that name the profile.
func profileError(err error, name string) error {
	var lines []string
	for _, l := range strings.Split(err.Error(), "\n") {
		if strings.Contains(l, name) {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	if len(lines) == 0 {
		return err
	}
	return errors.New(strings.Join(lines, "; "))
}

// goliveHealth runs theatarr health's checks, and the mount check even with the library off.
func goliveHealth(ctx context.Context, pol *policy.Policy, snap *settings.Snapshot, st *store.Store, dbPath, backupDir string,
	log *slog.Logger) []health.Result {
	switch backupDir {
	case "":
		backupDir = filepath.Join(filepath.Dir(dbPath), "backups")
	case "off":
		backupDir = ""
	}
	var providers []string
	for k, pv := range pol.Providers {
		if os.Getenv(pv.APIKeyEnv) != "" {
			providers = append(providers, k)
		}
	}
	sort.Strings(providers)
	on := func(name string) bool {
		src := metadataSource(pol, name)
		return src != nil && src.URL != "" && src.APIKeyEnv != "" && os.Getenv(src.APIKeyEnv) != ""
	}
	hc := newServeHealth(serveHealthConfig{pol: pol, snap: snap, st: st, q: jobs.NewQueue(st), dbPath: dbPath,
		backupDir: backupDir, providers: providers, providerInterval: pol.ProviderSyncInterval(), tmdb: on("tmdb"),
		tvdb: on("tvdb"), started: time.Now().Add(-48 * time.Hour), log: log})
	rs := hc.Results(ctx)
	lib := pol.LibrarySettings()
	if !lib.Enabled && lib.MountPath != "" {
		mc := health.New(health.Options{Logger: log})
		mc.Add("MountCheck", "Mount", health.Mount(filepath.Join(lib.MountPath, lib.AllDir), lib.StatTimeout))
		m := mc.Results(ctx)
		for i := range rs {
			if rs[i].Source == "MountCheck" {
				rs[i] = m[0]
			}
		}
	}
	return rs
}

// readServiceState reads the running service's /metrics (open, no login): theatarr_egress_vpn_ok per path and the
// active back-offs.
func readServiceState(ctx context.Context, base string) (*golive.ServiceState, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/metrics", nil)
	if err != nil {
		return nil, err
	}
	// Only the service on this machine: this is not the gateway, so it must never reach anything else.
	if ip := net.ParseIP(req.URL.Hostname()); req.URL.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("-service must be a loopback address, got %q", req.URL.Hostname())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /metrics: %s", resp.Status)
	}
	return parseServiceMetrics(resp.Body)
}

func parseServiceMetrics(r io.Reader) (*golive.ServiceState, error) {
	s := &golive.ServiceState{VPNOK: map[string]bool{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		name, rest, ok := strings.Cut(line, "{")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		labels, val, ok := strings.Cut(rest, "} ")
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if err != nil {
			continue
		}
		switch name {
		case "theatarr_egress_vpn_ok":
			s.VPNOK[metricLabel(labels, "path")] = v == 1
		case "theatarr_gateway_backoff_seconds":
			if v > 0 {
				s.Backoffs = append(s.Backoffs, metricLabel(labels, "subject"))
			}
		}
	}
	return s, sc.Err()
}

func metricLabel(labels, name string) string {
	for _, kv := range strings.Split(labels, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if ok && strings.TrimSpace(k) == name {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// parseExpiry reads "tb=2027-01-31,ad=2027-01-01".
func parseExpiry(v string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		k, d, ok := strings.Cut(part, "=")
		t, err := time.Parse("2006-01-02", strings.TrimSpace(d))
		if !ok || err != nil {
			return nil, fmt.Errorf("-provider-expiry: %q is not key=YYYY-MM-DD", part)
		}
		out[strings.TrimSpace(k)] = t
	}
	return out, nil
}
