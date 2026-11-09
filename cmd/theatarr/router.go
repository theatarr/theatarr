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
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/theatarr/theatarr/internal/buildinfo"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

func init() {
	register(command{
		name:    "router",
		summary: "download router: dry-run (search, decide and route, nothing written), status, offer and reconcile",
		run:     func(args []string) error { return runRouter(args, os.Stdout, nil) },
	})
}

const routerUsage = `usage:
  theatarr router dry-run [flags] -title ID|-tmdb N|-tvdb N -version KEY [-season N [-episode N]]
  theatarr router status [-policy DIR] [-db FILE] [-limit N] [-json]
  theatarr router offer [flags] -download ID | -sweep [-limit N]   [-dry-run] [-json]
  theatarr router reconcile [-db FILE] [-apply] [-json]

dry-run searches the version's indexers through the gateway (with the configured VPN requirements, like
theatarr decide -search), decides every result, and shows what the router would do with the accepted
ones: which release, which provider, and why each was skipped. It sends nothing to any provider and records nothing,
whatever router.enabled and the version's mode say. status reads the database: the kill switch, which versions the
router may write for, provider pacing, downloads by state, and recent downloads and dry-run plans. offer offers an
imported download to the title's other versions (-sweep also offers adopted links and imported arr
files): each version's own profile decides, an accepted offer queues a route that reuses the provider's torrent
(a dry-run plan for a shadow version), and outcomes are remembered in router_offer. -dry-run decides only.
reconcile lists queued route jobs, queued downloads and pending upgrade holds queued by an older build, whose
identity and origin cannot be proved (no identity validation version); -apply ends the automatic ones (job failed and
dismissed, download failed, hold expired) and queues one re-search per title version; manual rows are only listed.
Nothing is deleted and nothing is sent to any provider.
`

func runRouter(args []string, out io.Writer, dial func(context.Context, string, string) (net.Conn, error)) error {
	if len(args) == 0 || helpArg(args) {
		fmt.Fprint(os.Stderr, routerUsage)
		if helpArg(args) {
			return flag.ErrHelp
		}
		return errors.New("give dry-run or status")
	}
	switch args[0] {
	case "dry-run":
		return runRouterDryRun(args[1:], out, dial)
	case "status":
		return runRouterStatus(args[1:], out)
	case "offer":
		return runRouterOffer(args[1:], out)
	case "reconcile":
		return runRouterReconcile(args[1:], out)
	}
	fmt.Fprint(os.Stderr, routerUsage)
	return fmt.Errorf("unknown router command %q", args[0])
}

func runRouterDryRun(args []string, out io.Writer, dial func(context.Context, string, string) (net.Conn, error)) error {
	fs := flag.NewFlagSet("router dry-run", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), routerUsage+"\nflags:\n"); fs.PrintDefaults() }
	var df decideFlags
	df.register(fs)
	titleID := fs.Int64("title", 0, "our title id")
	tmdb := fs.Int("tmdb", 0, "find the movie by TMDb id")
	tvdb := fs.Int("tvdb", 0, "find the series by TVDB id")
	version := fs.String("version", "", "version key from the policy")
	season := fs.Int("season", -1, "series: the season to search")
	episode := fs.Int("episode", 0, "series: with -season, one episode")
	limit := fs.Int("limit", 10, "ranked results to print (0: all)")
	asJSON := fs.Bool("json", false, "print the plan as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" || (*titleID == 0 && *tmdb == 0 && *tvdb == 0) || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("-version and -title, -tmdb or -tvdb are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pol, e, err := df.engine(*version, os.Stderr)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, df.db, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	db := st.Reader()
	if *titleID == 0 && *tvdb > 0 {
		if err := db.QueryRowContext(ctx, `SELECT id FROM title WHERE kind = 'series' AND tvdb_id = ?`, *tvdb).
			Scan(titleID); err != nil {
			return fmt.Errorf("no series with TVDB id %d: %w", *tvdb, err)
		}
	}
	if *titleID == 0 {
		if *titleID, err = decide.FindTitle(ctx, db, *tmdb, ""); err != nil {
			return err
		}
	}
	t, err := decide.LoadTitle(ctx, db, *titleID)
	if err != nil {
		return err
	}
	v, err := decide.LoadVersion(ctx, db, pol, t.ID, *version)
	if err != nil {
		return err
	}
	var existing *decide.ExistingFile
	var ss *decide.SeriesSearch
	if t.Kind == policy.KindSeries {
		if *season < 0 {
			return errors.New("a series needs -season (and -episode for one episode)")
		}
		if err := decide.LoadSeriesVersion(ctx, db, &t, v.ID); err != nil {
			return err
		}
		ss = seriesSearch(&t, *season, *episode)
	} else if existing, err = decide.LoadExisting(ctx, db, v.ID); err != nil {
		return err
	}
	movies, err := decide.LoadMovies(ctx, db)
	if err != nil {
		return err
	}
	e.SetLibrary(decide.NewLibrary(movies))

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	gw, err := gateway.New(ctx, pol, gateway.Options{Logger: log, Version: buildinfo.Version, DialContext: dial})
	if err != nil {
		return err
	}
	svc, err := indexer.New(pol, gw, indexer.Options{Logger: log})
	if err != nil {
		return err
	}
	svc.SetMetadata(storedIndexerLookups(db))
	for _, s := range gw.CheckEgress(ctx) {
		if !s.OK && pol.Egress.RequiresVPN(policy.EgressIndexers) {
			return fmt.Errorf("%w (egress %s: %s)", errNotOnVPN, s.Path, s.Error)
		}
	}
	// Nothing is stored here: lay the search's SeaDex answers over the stored standing, as decide -search does.
	found, err := router.SearchRequest(decide.WithSeaDexOverlay(decide.WithParseMemo(ctx), db, nil), e, svc, t, v, existing, ss)
	if err != nil {
		if errors.Is(err, gateway.ErrVPNRequired) {
			return fmt.Errorf("%w (%v)", errNotOnVPN, err)
		}
		return err
	}
	found.Request.Source = "cli"
	// Adders only tell the plan which providers have a key; Plan never calls them.
	adders, _ := router.BuildAdders(pol, gw, os.Getenv, log)
	rt, err := router.New(router.Config{Store: st, Policy: pol, Adders: adders, Logger: log})
	if err != nil {
		return err
	}
	res, err := rt.Plan(ctx, found.Request)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			*router.Result
			Accepted int      `json:"accepted"`
			Results  int      `json:"results"`
			Notes    []string `json:"notes,omitempty"`
		}{res, len(found.Request.Candidates), len(found.Ranked), found.Notes})
	}
	fmt.Fprintf(out, "# %s (%d), version %s: %d result(s), %d accepted\n", t.Name, t.Year, v.Key, len(found.Ranked),
		len(found.Request.Candidates))
	for _, n := range found.Notes {
		fmt.Fprintf(out, "# %s\n", n)
	}
	gate := "the router would write for this version"
	if res.Why != "" {
		gate = "dry run in serve too: " + res.Why
	}
	fmt.Fprintf(out, "# mode %s; %s\n", res.Mode, gate)
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tOK\tQUALITY\tSCORE\tINDEXER\tTITLE")
	shown := found.Ranked
	if *limit > 0 && len(shown) > *limit {
		shown = shown[:*limit]
	}
	for i, d := range shown {
		ok := "yes"
		if !d.Accepted {
			ok = "no: " + d.Rejections[0].Reason
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\t%s\n", i+1, ok, d.Quality, d.Score, d.Indexer, d.Release)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(out, "\nroute:")
	printSteps(out, res.Steps)
	switch res.Outcome {
	case router.OutcomeWouldAdd:
		fmt.Fprintf(out, "\nwould add %s on %s (%s)\n", res.Release, res.Provider, res.Hash)
	case router.OutcomeDeferred:
		fmt.Fprintf(out, "\nwould wait until %s for a provider (%s)\n", res.RetryAt.UTC().Format(time.RFC3339), res.Release)
	default:
		fmt.Fprintf(out, "\nnothing to add: %s\n", res.Outcome)
	}
	return nil
}

func printSteps(out io.Writer, steps []router.Step) {
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	for _, s := range steps {
		p := s.Provider
		if p == "" {
			p = "-"
		}
		msg := s.Message
		if !s.RetryAt.IsZero() {
			msg += " (until " + s.RetryAt.UTC().Format(time.RFC3339) + ")"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", p, s.Outcome, s.Code, s.Release, msg)
	}
	tw.Flush()
}

func runRouterStatus(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("router status", flag.ContinueOnError)
	settingsSourceFlag(fs)
	fs.Usage = func() { fmt.Fprint(fs.Output(), routerUsage+"\nflags:\n"); fs.PrintDefaults() }
	policyDir := policyDirDefault()
	dbPath := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB); read only")
	limit := fs.Int("limit", 10, "recent downloads and plans to list")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pol, err := loadCommandPolicy(fs, policyDir)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, *dbPath, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	s, err := router.ReadStatus(ctx, st, pol, func(env string) bool { return os.Getenv(env) != "" }, time.Now(), *limit)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	}
	sw := "OFF (router.enabled is false): nothing is written to any provider"
	if s.Enabled {
		sw = "ON"
	}
	fmt.Fprintf(out, "router     %s\ncached     cached_only %v; %d unfinished cache check(s)\nactive     %s\n"+
		"misses     %d live\n", sw, s.CachedOnly, s.UnfinishedChecks, orNone(s.ActiveVersions), s.Misses)
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "\nVERSION\tMODE\tGRAB\tWRITES\tPROVIDERS")
	for _, v := range s.Versions {
		fmt.Fprintf(tw, "%s\t%s\t%v\t%v\t%s\n", v.Key, v.Mode, v.Grab, v.Writes, strings.Join(v.Providers, ", "))
	}
	fmt.Fprintln(tw, "\nPROVIDER\tKEY\tADDS/MIN\tUNCACHED/H\tCOOLDOWN\tBACKOFF\tFAILURES\tREASON")
	for _, p := range s.Providers {
		fmt.Fprintf(tw, "%s\t%v\t%d/%d\t%d\t%s\t%s\t%d\t%s\n", p.Key, p.KeySet, p.WindowAdds, p.AddsPerMinute,
			p.HourUncached, timeOrDash(p.CooldownUntil), timeOrDash(p.BackoffUntil), p.Failures, p.Reason)
	}
	var states []string
	for k, n := range s.States {
		states = append(states, fmt.Sprintf("%s %d", k, n))
	}
	sort.Strings(states)
	fmt.Fprintf(tw, "\ndownloads\t%s\n", orNone(states))
	if len(s.Recent) > 0 {
		fmt.Fprintln(tw, "\nID\tVERSION\tSTATE\tPROVIDER\tPROGRESS\tRELEASE\tERROR")
		for _, d := range s.Recent {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%.0f%%\t%s\t%s\n", d.ID, d.Version, d.State, d.Provider, d.Progress,
				d.Release, d.Error)
		}
	}
	if len(s.Plans) > 0 {
		fmt.Fprintln(tw, "\nPLAN\tVERSION\tMODE\tOUTCOME\tPROVIDER\tRELEASE\tAT")
		for _, p := range s.Plans {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", p.ID, p.Version, p.Mode, p.Outcome, p.Provider, p.Release,
				p.Created.Format(time.RFC3339))
		}
	}
	fmt.Fprintf(tw, "\nblocklist\t%d hashes never routed again for their version\n", s.BlocklistTotal)
	if len(s.Blocklist) > 0 {
		fmt.Fprintln(tw, "\nVERSION\tHASH\tBY\tDOWNLOAD\tREASON\tRELEASE\tAT")
		for _, b := range s.Blocklist {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n", b.Version, b.Hash, b.Source, b.DownloadID, b.Reason,
				b.Release, b.Created.Format(time.RFC3339))
		}
	}
	return tw.Flush()
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

func timeOrDash(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

// newServeRouter builds theatarr serve's router: write clients for providers whose key is set, the sync's read-only
// clients for status. The router itself enforces the gate (router.enabled and mode active).
func newServeRouter(pol *policy.Policy, st *store.Store, d provider.Doer, provs map[string]provider.Provider,
	log *slog.Logger) (*router.Router, error) {
	adders, skipped := router.BuildAdders(pol, d, os.Getenv, log)
	if len(skipped) > 0 {
		log.Info("router: providers with no key set are skipped", "providers", skipped)
	}
	return router.New(router.Config{Store: st, Policy: pol, Adders: adders, Providers: provs,
		Clients: router.BuildClients(pol, d, pol.Getenv, log), Logger: log})
}

func runRouterReconcile(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("router reconcile", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), routerUsage+"\nflags:\n"); fs.PrintDefaults() }
	dbPath := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	apply := fs.Bool("apply", false, "end the automatic legacy rows and queue their re-searches (default: list only)")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	st, err := store.Open(ctx, *dbPath, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	rep, err := router.ReconcileLegacyQueue(ctx, st, jobs.NewQueue(st), *apply, time.Now())
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tID\tSTATE\tVERSION\tTITLE\tSOURCE\tACTION\tRELEASE")
	for _, x := range rep.Rows {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%d\t%s\t%s\t%s\n", x.Kind, x.ID, x.State, x.Version, x.TitleID, x.Source,
			x.Action, x.Release)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	verb := "would end"
	if rep.Applied {
		verb = "ended"
	}
	fmt.Fprintf(out, "\n%d legacy rows; %s %d automatic; %d re-searches (%d newly queued)\n", len(rep.Rows), verb,
		rep.Ended, len(rep.Researches), rep.Queued)
	if !rep.Applied && rep.Ended > 0 {
		fmt.Fprintln(out, "dry run: nothing written; -apply ends them and queues the re-searches")
	}
	return nil
}
