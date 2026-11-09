package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/buildinfo"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/importarr"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/shadow"
	"github.com/theatarr/theatarr/internal/store"
)

func init() {
	register(command{
		name:    "shadow",
		summary: "shadow runner: status (the diff report's summary), run-once (-grab N, -import, -rss) for manual checks, reclassify",
		run:     func(args []string) error { return runShadow(args, os.Stdout, nil) },
	})
}

const shadowUsage = `usage:
  theatarr shadow status [-db FILE] [-json]
  theatarr shadow run-once [flags] -grab N [-instance NAME] [-no-search]
  theatarr shadow run-once [flags] -import
  theatarr shadow run-once [flags] -rss
  theatarr shadow reclassify [-policy DIR] [-db FILE] [-json]

status prints the 14-day agreement, the buckets and reasons, the cursors and the last runs from the database.
run-once does one step now, in this process: re-decide one arr grab by history id (replacing what is stored for
it), one arr import (new grabs are decided at once), or one RSS read. Searches and RSS need the VPN;
off it, -grab decides the arr's release only and -rss refuses.
reclassify buckets every stored grab again from its stored decisions and our RSS picks, with no search: an RSS
grab against our RSS picks near it, a hand grab against our decision of it. theatarr serve does the same at start.
`

// newShadowEngine builds the decision engine for the shadow runner: TRaSH formats from THEATARR_TRASH, from_arr
// profiles with GETs to the import.arr instances.
func newShadowEngine(pol *policy.Policy, log *slog.Logger, previous *decide.Engine) (*decide.Engine, error) {
	e, err := decide.New(pol, decide.Options{TrashRoot: os.Getenv("THEATARR_TRASH"), Arr: liveArr(pol), Previous: previous})
	if err != nil {
		if previous != nil || e == nil {
			return nil, err
		}
		log.Warn("shadow: some profiles failed to build", "err", err)
	}
	for _, w := range e.Warnings {
		log.Info("shadow: format left out", "warning", w)
	}
	return e, nil
}

// arrGetters gives the import's arr clients, with the keys from the environment.
func arrGetters(pol *policy.Policy) shadow.ArrSourceFunc {
	return func(instance string) (importarr.Getter, error) {
		var a *policy.ArrSource
		if pol.Import != nil {
			a = pol.Import.Arr[instance]
		}
		if a == nil {
			return nil, fmt.Errorf("import.arr has no instance %q", instance)
		}
		key := pol.Getenv(a.APIKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("%s is not set", a.APIKeyEnv)
		}
		return arr.New(a.URL, key, arr.Options{Timeout: 120 * time.Second})
	}
}

// newShadowRunner builds the runner theatarr serve runs. Without an import.arr section it imports nothing; without
// indexers it only decides the arr's releases. rt (the download router) gets a dry-run plan for every shadow grab
// decision and RSS pick, and the RSS picks of active versions. It also returns the engine and the indexer service,
// which the acquirer shares.
func newShadowRunner(pol *policy.Policy, st *store.Store, q *jobs.Queue, gw *gateway.Gateway, reg prometheus.Registerer,
	log *slog.Logger, rt shadow.Router, prepared *serveRuntime, runtime shadow.RuntimeSource) (*shadow.Runner, *decide.Engine, *indexer.Service, error) {
	var svc *indexer.Service
	var eng *decide.Engine
	if prepared != nil {
		svc, eng = prepared.Indexers, prepared.Engine
	} else {
		var err error
		svc, err = indexer.New(pol, gw, indexer.Options{Logger: log, Cardigann: cardigannManager()})
		if err != nil {
			return nil, nil, nil, err
		}
		eng, err = newShadowEngine(pol, log, nil)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	cfg := shadow.Config{Store: st, Policy: pol, Engine: eng, Queue: q, Indexers: svc, Budgets: gw, Router: rt,
		Logger: log, Registerer: reg, Runtime: runtime}
	if pol.Import != nil && len(pol.Import.Arr) > 0 {
		cfg.Arr = arrGetters(pol)
	}
	r, warns := shadow.New(cfg)
	for _, w := range warns {
		log.Info("shadow: " + w)
	}
	log.Info("shadow runner", "versions", r.Versions(), "rss_to_router", r.ActiveVersions())
	return r, eng, svc, nil
}

func runShadow(args []string, out io.Writer, dial func(context.Context, string, string) (net.Conn, error)) error {
	if len(args) == 0 || helpArg(args) {
		fmt.Fprint(os.Stderr, shadowUsage)
		if helpArg(args) {
			return flag.ErrHelp
		}
		return errors.New("want status or run-once")
	}
	switch args[0] {
	case "status":
		return runShadowStatus(args[1:], out)
	case "run-once":
		return runShadowOnce(args[1:], out, dial)
	case "reclassify":
		return runShadowReclassify(args[1:], out)
	}
	fmt.Fprint(os.Stderr, shadowUsage)
	return fmt.Errorf("unknown shadow command %q", args[0])
}

func runShadowStatus(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("shadow status", flag.ContinueOnError)
	settingsSourceFlag(fs)
	fs.Usage = func() { fmt.Fprint(fs.Output(), shadowUsage+"\nflags:\n"); fs.PrintDefaults() }
	db := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB); read only")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	st, err := store.Open(ctx, *db, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	s, err := shadow.LoadSummary(ctx, st.Reader(), time.Now())
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	}
	printShadowSummary(out, s)
	rows, _, err := shadow.ListDiffs(ctx, st.Reader(), shadow.DiffFilter{Limit: 10})
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		fmt.Fprintln(out, "\nlatest diffs:")
		tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		for _, d := range rows {
			extra := ""
			if d.Basis != "" && d.Basis != shadow.BasisSearch {
				extra = "vs " + d.Basis
			}
			if b := d.SearchFoundBetter; b != nil {
				extra += fmt.Sprintf("; search found better: %s %d", b.Quality, b.Score)
			}
			if d.Basis == shadow.BasisLive {
				extra += "; " + d.Detail
			}
			if d.RouterPlan != "" {
				extra += "; router: " + d.RouterPlan
			}
			fmt.Fprintf(tw, "  #%d\t%s\t%s (%d)\t%s\t%s\t%s\t%s\n", d.ID, d.At.Format("2006-01-02 15:04"), d.TitleName, d.Year,
				d.Version, d.Bucket, d.Reason, strings.TrimPrefix(extra, "; "))
		}
		tw.Flush()
	}
	return nil
}

func ratioPct(p *float64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100**p)
}

func printShadowSummary(out io.Writer, s *shadow.Summary) {
	fmt.Fprintf(out, "agreement (%s)  %s of %d grabs (bar %.0f%%), %d searched\n", s.Window, ratioPct(s.Agreement),
		s.Grabs, 100*s.Bar, s.Searched)
	for _, k := range s.Kinds {
		fmt.Fprintf(out, "  %-24s %s of %d\n", k.Kind, ratioPct(k.Agreement), k.Grabs)
	}
	for _, v := range s.Versions {
		fmt.Fprintf(out, "  %-24s %s of %d\n", v.Version, ratioPct(v.Agreement), v.Grabs)
	}
	fmt.Fprintf(out, "hand grabs %d (not in the ratio): %d accepted, %d rejected\n", s.HandGrabs.Grabs,
		s.HandGrabs.Accepted, s.HandGrabs.Rejected)
	fmt.Fprintln(out, "buckets:")
	for _, b := range shadow.Buckets {
		fmt.Fprintf(out, "  %-24s %d\n", b, s.Buckets[b])
	}
	if len(s.Reasons) > 0 {
		fmt.Fprintln(out, "reasons:")
		for _, r := range s.Reasons {
			fmt.Fprintf(out, "  %-24s %-24s %d", r.Bucket, r.Reason, r.Count)
			if r.SearchFoundBetter > 0 {
				fmt.Fprintf(out, " (search found better: %d)", r.SearchFoundBetter)
			}
			fmt.Fprintln(out)
		}
	}
	if s.SearchFoundBetter > 0 {
		fmt.Fprintf(out, "search found better on %d RSS or hand grabs (information; not counted against agreement)\n",
			s.SearchFoundBetter)
	}
	fmt.Fprintf(out, "pending grabs %d, open RSS picks %d, titles searched this hour %d\n", s.Pending, s.OpenPicks,
		s.SearchedThisHour)
	for _, c := range s.Cursors {
		fmt.Fprintf(out, "cursor %-16s history id %d (%s), %d grabs behind\n", c.Instance, c.HistoryID,
			c.UpdatedAt.Format(time.RFC3339), c.Behind)
	}
	for _, r := range s.LastRuns {
		ok := "ok"
		if !r.OK {
			ok = "FAILED"
		}
		fmt.Fprintf(out, "last %-12s %s %s: %s\n", r.Kind, r.FinishedAt.Format(time.RFC3339), ok, r.Summary)
	}
}

func runShadowOnce(args []string, out io.Writer, dial func(context.Context, string, string) (net.Conn, error)) error {
	fs := flag.NewFlagSet("shadow run-once", flag.ContinueOnError)
	settingsSourceFlag(fs)
	fs.Usage = func() { fmt.Fprint(fs.Output(), shadowUsage+"\nflags:\n"); fs.PrintDefaults() }
	db := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	policyDir := policyDirDefault()
	envFile := fs.String("env-file", "", "read KEY=VALUE secrets from this file (variables already set win)")
	grab := fs.Int("grab", 0, "re-decide the arr grab with this history id")
	instance := fs.String("instance", "", "-grab: the arr instance (needed when both have the id)")
	noSearch := fs.Bool("no-search", false, "-grab: decide the arr's release only")
	doImport := fs.Bool("import", false, "run one arr import; new grabs are decided at once")
	doRSS := fs.Bool("rss", false, "run one RSS read")
	asJSON := fs.Bool("json", false, "print JSON")
	verbose := fs.Bool("v", false, "log to stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	n := 0
	for _, b := range []bool{*grab > 0, *doImport, *doRSS} {
		if b {
			n++
		}
	}
	if n != 1 {
		fs.Usage()
		return errors.New("give one of -grab N, -import, -rss")
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
	var logOut io.Writer = io.Discard
	if *verbose {
		logOut = os.Stderr
	}
	log := slog.New(slog.NewTextHandler(logOut, nil))
	gw, err := gateway.New(ctx, pol, gateway.Options{Store: st, Logger: log, Version: buildinfo.Version, DialContext: dial})
	if err != nil {
		return err
	}
	defer gw.FlushRequests(context.WithoutCancel(ctx)) // the indexer request counts (docs/design/gateway.md)
	onVPN := true
	if pol.Egress != nil {
		for _, s := range gw.CheckEgress(ctx) {
			if !s.OK && pol.Egress.RequiresVPN(policy.EgressIndexers) {
				onVPN = false
				fmt.Fprintf(os.Stderr, "# egress %s is not the VPN (%s): no indexer is asked\n", s.Path, s.Error)
			}
		}
	}
	if *doRSS && !onVPN {
		return errNotOnVPN
	}
	eng, err := newShadowEngine(pol, log, nil)
	if err != nil {
		return err
	}
	cfg := shadow.Config{Store: st, Policy: pol, Engine: eng, Budgets: gw, Logger: log}
	if onVPN {
		if cfg.Indexers, err = indexer.New(pol, gw, indexer.Options{Logger: log}); err != nil {
			return err
		}
	}
	if pol.Import != nil && len(pol.Import.Arr) > 0 {
		cfg.Arr = arrGetters(pol)
	}
	r, warns := shadow.New(cfg)
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "# %s\n", w)
	}
	var res any
	var runErr error
	switch {
	case *grab > 0:
		g, err := r.GrabByID(ctx, *instance, *grab, *noSearch || !onVPN)
		if err != nil {
			return err
		}
		res = g
		if !*asJSON {
			printGrab(out, g)
			return nil
		}
	case *doImport:
		if cfg.Arr == nil {
			return errors.New("the policy has no import.arr section")
		}
		s, err := r.Import(ctx)
		if s == nil {
			return err
		}
		res, runErr = s, err
	case *doRSS:
		s, err := r.RSS(ctx)
		if s == nil {
			return err
		}
		res, runErr = s, err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		return err
	}
	return runErr
}

func runShadowReclassify(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("shadow reclassify", flag.ContinueOnError)
	settingsSourceFlag(fs)
	fs.Usage = func() { fmt.Fprint(fs.Output(), shadowUsage+"\nflags:\n"); fs.PrintDefaults() }
	db := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	policyDir := policyDirDefault()
	asJSON := fs.Bool("json", false, "print JSON")
	verbose := fs.Bool("v", false, "log to stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("unexpected arguments %v", fs.Args())
	}
	pol, err := loadCommandPolicy(fs, policyDir)
	if err != nil {
		return err
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
	var logOut io.Writer = io.Discard
	if *verbose {
		logOut = os.Stderr
	}
	log := slog.New(slog.NewTextHandler(logOut, nil))
	// The engine only ranks stored decisions here (the profiles' quality order); nothing is decided or searched.
	eng, err := newShadowEngine(pol, log, nil)
	if err != nil {
		return err
	}
	r, warns := shadow.New(shadow.Config{Store: st, Policy: pol, Engine: eng, Logger: log})
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "# %s\n", w)
	}
	sum, err := r.Reclassify(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(sum)
	}
	fmt.Fprintf(out, "reclassified %d grabs: %d changed, %d skipped (version not shadowed)\n", sum.Grabs, sum.Changed,
		sum.Skipped)
	moves := make([]string, 0, len(sum.Moves))
	for m := range sum.Moves {
		moves = append(moves, m)
	}
	sort.Strings(moves)
	for _, m := range moves {
		fmt.Fprintf(out, "  %-70s %d\n", m, sum.Moves[m])
	}
	return nil
}

func printGrab(out io.Writer, g *shadow.GrabResult) {
	if g.Skipped != "" {
		fmt.Fprintf(out, "skipped: %s\n", g.Skipped)
		return
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "grab\t%s #%d, %s, version %s\n", g.Instance, g.History, g.Title, g.Version)
	fmt.Fprintf(tw, "arr's release\t%s\n", g.Arr.Release)
	fmt.Fprintf(tw, "  ours for it\t%s %s\n", acceptWord(g.Arr.Accepted), decidedLine(&g.Arr))
	if g.Pick != nil {
		fmt.Fprintf(tw, "our pick\t%s\n\t%s\n", g.Pick.Release, decidedLine(g.Pick))
	} else {
		fmt.Fprintf(tw, "our pick\tnone\n")
	}
	s := "no"
	if g.Searched {
		s = fmt.Sprintf("yes: %d results, %d accepted", g.Results, g.Accepted)
	}
	fmt.Fprintf(tw, "searched\t%s\n", s)
	if g.Note != "" {
		fmt.Fprintf(tw, "notes\t%s\n", g.Note)
	}
	fmt.Fprintf(tw, "diff\t%s / %s (vs %s)\n\t%s\n", g.Outcome.Bucket, g.Outcome.Reason, g.Basis, g.Outcome.Detail)
	if b := g.SearchFoundBetter; b != nil {
		fmt.Fprintf(tw, "search found better\t%s\n\t%s\n", b.Release, decidedLine(b))
	}
	tw.Flush()
}

func acceptWord(ok bool) string {
	if ok {
		return "ACCEPTED"
	}
	return "REJECTED"
}

func decidedLine(d *shadow.Decided) string {
	s := d.Quality + ", " + formatList(d.Formats, d.Score)
	if len(d.Rejections) > 0 {
		var rs []string
		for _, r := range d.Rejections {
			rs = append(rs, r.String())
		}
		s += "; " + strings.Join(rs, "; ")
	}
	return s
}
