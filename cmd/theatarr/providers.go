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
	"os/signal"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/theatarr/theatarr/internal/buildinfo"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/store"
)

func init() {
	register(command{
		name:    "providers",
		summary: "debrid providers, read-only: sync the content registry from their torrent lists, show status",
		run:     runProviders,
	})
}

func runProviders(args []string) error {
	if len(args) == 0 || helpArg(args) {
		const usage = "usage: theatarr providers sync|status [flags]"
		if helpArg(args) {
			fmt.Fprintln(os.Stderr, usage)
			return flag.ErrHelp
		}
		return errors.New(usage)
	}
	switch args[0] {
	case "sync":
		return runProvidersSync(args[1:])
	case "status":
		return runProvidersStatus(args[1:])
	}
	return fmt.Errorf("unknown subcommand %q (sync, status)", args[0])
}

// providerEnv is what both subcommands set up: policy, database, gateway and the clients whose keys are set.
type providerEnv struct {
	pol        *policy.Policy
	st         *store.Store
	gw         *gateway.Gateway
	configured []string // policy keys in fallback order
	clients    map[string]provider.Provider
	missing    []string          // configured providers whose key is unset
	keys       map[string]string // each provider's key as serve resolves it (providerKeys)
}

func (e *providerEnv) Close() {
	if e.st != nil {
		e.st.Close()
	}
}

func openProviderEnv(ctx context.Context, db, policyDir, envFile string, verbose, needGateway bool, source ...string) (*providerEnv, error) {
	if policyDir == "" {
		return nil, errors.New("-policy is required")
	}
	if envFile != "" {
		if err := loadEnvFile(envFile); err != nil {
			return nil, err
		}
	}
	settingsSource := settingsSourceDefault()
	if len(source) > 0 {
		settingsSource = source[0]
	}
	pol, snap, err := loadSettingsSnapshot(ctx, settingsSource, policyDir, db)
	if err != nil {
		return nil, err
	}
	if len(pol.Providers) == 0 {
		return nil, errors.New("the policy has no providers")
	}
	e := &providerEnv{pol: pol, clients: map[string]provider.Provider{}, keys: providerKeys(pol, snap)}
	for k := range pol.Providers {
		e.configured = append(e.configured, k)
	}
	sort.Slice(e.configured, func(i, j int) bool {
		return pol.Providers[e.configured[i]].Order < pol.Providers[e.configured[j]].Order
	})
	if e.st, err = store.Open(ctx, db, store.Options{}); err != nil {
		return nil, err
	}
	if _, err := e.st.Migrate(ctx); err != nil {
		e.Close()
		return nil, err
	}
	if !needGateway {
		return e, nil
	}
	level := slog.LevelWarn
	if verbose {
		level = slog.LevelDebug
	}
	e.gw, err = gateway.New(ctx, pol, gateway.Options{Store: e.st, Version: buildinfo.Version,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))})
	if err != nil {
		e.Close()
		return nil, err
	}
	// The VPN guard needs a fresh egress check when the policy puts providers behind the VPN.
	if pol.Egress.RequiresVPN(policy.EgressProviders) {
		for _, s := range e.gw.CheckEgress(ctx) {
			if !s.OK {
				e.Close()
				return nil, fmt.Errorf("egress path %s is not on the VPN (%s); providers require it", s.Path, s.Error)
			}
		}
	}
	for _, k := range e.configured {
		pv := pol.Providers[k]
		key := e.keys[k]
		if key == "" {
			e.missing = append(e.missing, k)
			continue
		}
		c, err := provider.New(k, pv, key, e.gw)
		if err != nil {
			e.Close()
			return nil, err
		}
		e.clients[k] = c
	}
	return e, nil
}

// pickProviders resolves -provider values: policy keys, or rd/ad/tb and the kind names as aliases.
func (e *providerEnv) pickProviders(names []string) ([]string, error) {
	alias := map[string]string{"rd": provider.KindRealDebrid, "ad": provider.KindAllDebrid, "tb": provider.KindTorBox}
	var out []string
	for _, n := range names {
		found := ""
		if e.pol.Providers[n] != nil {
			found = n
		} else {
			kind := n
			if a, ok := alias[n]; ok {
				kind = a
			}
			for _, k := range e.configured {
				if e.pol.Providers[k].Kind == kind {
					found = k
					break
				}
			}
		}
		if found == "" {
			return nil, fmt.Errorf("provider %q is not in the policy", n)
		}
		if e.clients[found] == nil {
			return nil, fmt.Errorf("provider %q: %s", found, providerKeyUnset(e.pol.Providers[found]))
		}
		out = append(out, found)
	}
	return out, nil
}

type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

func runProvidersSync(args []string) error {
	fs := flag.NewFlagSet("providers sync", flag.ContinueOnError)
	settingsSourceFlag(fs)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: theatarr providers sync [flags]\n\n"+
			"Lists each provider's torrents (GET only) and updates the content registry. Providers whose key is not\n"+
			"set are skipped. Nothing is added to or removed from any provider.\n\n")
		fs.PrintDefaults()
	}
	db := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	policyDir := policyDirDefault()
	envFile := fs.String("env-file", "", "read KEY=VALUE secrets from this file (variables already set win)")
	var only stringList
	fs.Var(&only, "provider", "sync only this provider: rd, ad, tb or a policy key (repeatable)")
	dry := fs.Bool("dry-run", false, "list and compute, but write nothing")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	verbose := fs.Bool("v", false, "log the gateway's requests to stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	e, err := openProviderEnv(ctx, *db, policyDir, *envFile, *verbose, true, fs.Lookup("settings-source").Value.String())
	if err != nil {
		return err
	}
	defer e.Close()
	keys, err := e.pickProviders(only)
	if err != nil {
		return err
	}
	if len(only) == 0 {
		for _, k := range e.missing {
			fmt.Fprintf(os.Stderr, "skipping %s: %s (no content can become absent until it is listed)\n",
				k, providerKeyUnset(e.pol.Providers[k]))
		}
		if len(e.clients) == 0 {
			return errors.New("no provider has its key set")
		}
	}
	s := &provider.Syncer{Store: e.st, Providers: e.clients, Configured: e.configured,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	res, runErr := s.Run(ctx, provider.SyncOptions{Providers: keys, DryRun: *dry})
	if res != nil {
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(res); err != nil {
				return err
			}
		} else {
			writeSyncResult(os.Stdout, res)
		}
	}
	return runErr
}

func writeSyncResult(w io.Writer, res *provider.SyncResult) {
	if res.DryRun {
		fmt.Fprintln(w, "DRY RUN: nothing was written")
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "provider\tok\ttorrents\thashes\tdup\tbad hash\tnew\tpresent+\tupdated\tabsent+\tunchanged\tlist s\t")
	for _, p := range res.Providers {
		ok := "yes"
		if !p.OK {
			ok = "NO"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%.1f\t\n", p.Provider, ok, p.Torrents, p.Hashes,
			p.Duplicates, p.InvalidHashes, p.NewContents, p.NowPresent, p.Updated, p.NowAbsent, p.Unchanged,
			float64(p.ListMS)/1000)
	}
	tw.Flush()
	for _, p := range res.Providers {
		if p.Error != "" {
			fmt.Fprintf(w, "%s: %s\n", p.Provider, p.Error)
		}
		if len(p.ByState) > 0 {
			fmt.Fprintf(w, "%s torrents by state: %s\n", p.Provider, kv(p.ByState))
		}
	}
	if len(res.Transitions) > 0 {
		fmt.Fprintf(w, "content state changes: %s\n", kv(res.Transitions))
	} else {
		fmt.Fprintln(w, "content state changes: none")
	}
	fmt.Fprintf(w, "contents by state: %s\n", kv(res.Contents))
}

func kv[K ~string](m map[K]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, m[K(k)])
	}
	return strings.Join(parts, ", ")
}

// retryText is a provider's back-off: when it is tried again, or "-" when it is not failing.
func retryText(p provider.ProviderStatus) string {
	if p.Failures == 0 || p.RetryAt == nil {
		return "-"
	}
	if d := time.Until(*p.RetryAt); d > 0 {
		return fmt.Sprintf("%d failed, next %s (in %s)", p.Failures, p.RetryAt.Local().Format("15:04"),
			d.Round(time.Second))
	}
	return fmt.Sprintf("%d failed, due now", p.Failures)
}

func runProvidersStatus(args []string) error {
	fs := flag.NewFlagSet("providers status", flag.ContinueOnError)
	settingsSourceFlag(fs)
	db := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	policyDir := policyDirDefault()
	envFile := fs.String("env-file", "", "read KEY=VALUE secrets from this file (variables already set win)")
	account := fs.Bool("account", false, "also read each account live (GET): plan, expiry, limits, cache support")
	asJSON := fs.Bool("json", false, "print as JSON")
	verbose := fs.Bool("v", false, "log the gateway's requests to stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	e, err := openProviderEnv(ctx, *db, policyDir, *envFile, *verbose, *account, fs.Lookup("settings-source").Value.String())
	if err != nil {
		return err
	}
	defer e.Close()
	st, err := provider.ReadStatus(ctx, e.st.Reader(), e.configured)
	if err != nil {
		return err
	}
	type live struct {
		Account *provider.Account     `json:"account,omitempty"`
		Cache   provider.CacheSupport `json:"cache"`
		Error   string                `json:"error,omitempty"`
	}
	accounts := map[string]*live{}
	var errs []error
	if *account {
		for _, k := range e.configured {
			c := e.clients[k]
			if c == nil {
				accounts[k] = &live{Error: providerKeyUnset(e.pol.Providers[k])}
				continue
			}
			a, err := c.Account(ctx)
			l := &live{Account: a, Cache: c.Cache()}
			if err != nil {
				l.Error = err.Error()
				errs = append(errs, err)
			}
			accounts[k] = l
		}
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(struct {
			*provider.Status
			Accounts map[string]*live `json:"accounts,omitempty"`
		}{st, accounts}); err != nil {
			return err
		}
		return errors.Join(errs...)
	}
	w := os.Stdout
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "provider\tkind\tpresent\tabsent\tlast sync\tlast complete sync\tretry")
	for _, p := range st.Providers {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%s\t%s\n", p.Provider, e.pol.Providers[p.Provider].Kind, p.Present, p.Absent,
			runText(p.LastRun), runText(p.LastOK), retryText(p))
	}
	tw.Flush()
	for _, p := range st.Providers {
		if p.Failures > 0 {
			fmt.Fprintf(w, "%s is backing off after %d failed sync(s); the other providers sync on schedule. "+
				"Last error: %s\n", p.Provider, p.Failures, p.LastRun.Error)
		}
	}
	fmt.Fprintf(w, "contents by state: %s\n", kv(st.Contents))
	if len(st.Unlisted) > 0 {
		fmt.Fprintf(w, "never listed completely: %s (contents stay unknown rather than absent until they are)\n",
			strings.Join(st.Unlisted, ", "))
	}
	for _, k := range e.configured {
		l := accounts[k]
		if l == nil {
			continue
		}
		if l.Account == nil {
			fmt.Fprintf(w, "%s: %s\n", k, l.Error)
			continue
		}
		a := l.Account
		fmt.Fprintf(w, "%s: plan %s, premium %v", k, a.Plan, a.Premium)
		if !a.Expires.IsZero() {
			fmt.Fprintf(w, ", expires %s (%d days)", a.Expires.Format("2006-01-02"),
				int(time.Until(a.Expires).Hours()/24))
		}
		if a.MaxActiveTorrents >= 0 {
			fmt.Fprintf(w, ", active %s of %d", intOr(a.ActiveTorrents), a.MaxActiveTorrents)
		}
		if a.CooldownUntil.After(time.Now()) {
			fmt.Fprintf(w, ", cooldown until %s", a.CooldownUntil.Format(time.RFC3339))
		}
		fmt.Fprintln(w)
		for _, n := range a.Notes {
			fmt.Fprintf(w, "  %s\n", n)
		}
		if l.Cache.Supported {
			fmt.Fprintf(w, "  cache check: %s\n", l.Cache.Endpoint)
		} else {
			fmt.Fprintf(w, "  cache check: none (%s)\n", l.Cache.Note)
		}
	}
	return errors.Join(errs...)
}

func intOr(n int) string {
	if n < 0 {
		return "?"
	}
	return fmt.Sprint(n)
}

func runText(r *provider.RunInfo) string {
	if r == nil {
		return "never"
	}
	s := r.Finished.Local().Format("2006-01-02 15:04")
	if !r.OK {
		s += " FAILED"
	}
	return s
}
