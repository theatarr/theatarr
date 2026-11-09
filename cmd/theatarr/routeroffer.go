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
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

// offerSweepEvery is how often the offer sweep runs.
const offerSweepEvery = 6 * time.Hour

// newServeOfferer builds the offerer of theatarr serve; without a decision engine it decides nothing (every offer stays
// pending and is retried).
func newServeOfferer(st *store.Store, pol *policy.Policy, eng *decide.Engine, rt interface {
	Enqueue(context.Context, router.Request, time.Time) (int64, error)
}, q *jobs.Queue, log *slog.Logger, manager *settings.RuntimeCoordinator) *router.Offerer {
	dec := router.DecideFunc(func(context.Context, decide.Release, decide.Title, decide.Version, *decide.ExistingFile) (*decide.Decision, error) {
		return nil, errors.New("no decision engine")
	})
	decSeries := router.DecideSeriesFunc(func(context.Context, decide.Release, decide.Title, decide.Version,
		*decide.SeriesSearch) (*decide.Decision, error) {
		return nil, errors.New("no decision engine")
	})
	if eng != nil {
		dec = eng.Decide
		decSeries = eng.DecideSearch
	}
	o := &router.Offerer{Store: st, Policy: pol, Decide: dec, DecideSeries: decSeries, Router: rt, Queue: q,
		Logger: log.With("component", "offer")}
	if manager != nil {
		o.CaptureRuntime = manager.Capture
		o.Runtime = func(ctx context.Context) (*policy.Policy, router.DecideFunc, router.DecideSeriesFunc, error) {
			active, err := runtimeForSearch(ctx, manager)
			if err != nil {
				return nil, nil, nil, err
			}
			return active.Policy, active.Engine.Decide, active.Engine.DecideSearch, nil
		}
	}
	return o
}

// runOfferSweep is the 6-hourly sweep of downloads, adopted/followed links and imported arr files.
func runOfferSweep(ctx context.Context, off *router.Offerer, log *slog.Logger) {
	t := time.NewTicker(offerSweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sr, err := off.Sweep(ctx, false, 0)
			if err != nil {
				log.Warn("offer sweep", "err", err)
			}
			if sr != nil {
				log.Info("offer sweep", "downloads", sr.Downloads, "outcomes", sr.Outcomes, "errors", sr.Errors)
			}
		}
	}
}

func runRouterOffer(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("router offer", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), routerUsage); fs.PrintDefaults() }
	var df decideFlags
	df.register(fs)
	download := fs.Int64("download", 0, "offer this imported download")
	sweep := fs.Bool("sweep", false, "offer imported downloads, adopted links and arr files (backfill)")
	limit := fs.Int("limit", 0, "-sweep: at most this many sources (0: all)")
	dryRun := fs.Bool("dry-run", false, "decide and print; record and queue nothing")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*download == 0) == !*sweep {
		return errors.New("give -download ID or -sweep")
	}
	ctx := context.Background()
	if df.envFile != "" {
		if err := loadEnvFile(df.envFile); err != nil {
			return err
		}
	}
	pol, err := loadSettingsPolicy(ctx, df.settingsSource, df.policyDir, df.db)
	if err != nil {
		return err
	}
	src := liveArr(pol)
	if df.arrSnapshot != "" {
		src = decide.SnapshotDir(df.arrSnapshot)
	}
	eng, err := decide.New(pol, decide.Options{TrashRoot: df.trash, Arr: src})
	if eng == nil {
		return err
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "# some profiles failed to build (offers to their versions fail):\n%s\n", indent(err.Error()))
	}
	st, err := store.Open(ctx, df.db, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		return err
	}
	q := jobs.NewQueue(st)
	rt, err := router.New(router.Config{Store: st, Policy: pol})
	if err != nil {
		return err
	}
	rt.SetDecider(eng.Decide, eng.DecideSearch)
	rt.SetProfiles(eng.Profile)
	w := jobs.NewWorker(q, jobs.WorkerOptions{})
	rt.RegisterJobs(w, q) // gives the router its queue: routes are queued for theatarr serve to run
	off := &router.Offerer{Store: st, Policy: pol, Decide: eng.Decide, DecideSeries: eng.DecideSearch, Router: rt, Queue: q}
	if *sweep {
		sr, err := off.Sweep(ctx, *dryRun, *limit)
		if *asJSON {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			if e := enc.Encode(sr); e != nil {
				return e
			}
			return err
		}
		if sr != nil {
			fmt.Fprintf(out, "offered %d sources: %v\n", sr.Downloads, sr.Outcomes)
			printOffers(out, sr.Offers)
			for _, e := range sr.Errors {
				fmt.Fprintf(out, "  error: %s\n", e)
			}
		}
		return err
	}
	res, err := off.Offer(ctx, *download, *dryRun)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	if len(res) == 0 {
		fmt.Fprintln(out, "nothing to offer (not an imported download, or no other version; outcomes already recorded are not offered again)")
	}
	printOffers(out, res)
	return nil
}

func printOffers(out io.Writer, res []router.OfferResult) {
	for _, r := range res {
		fmt.Fprintf(out, "%-14s %-9s %s", r.Version, r.Outcome, r.Release)
		if len(r.Reasons) > 0 {
			fmt.Fprintf(out, "  (%s)", strings.Join(r.Reasons, "; "))
		}
		fmt.Fprintln(out)
	}
}
