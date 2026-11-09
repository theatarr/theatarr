package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

func init() {
	register(command{
		name:    "migrate",
		summary: "database migrations (up, status) and the guided move from an arr stack (inspect, plan, apply, verify)",
		run:     runMigrate,
	})
}

func runMigrate(args []string) error {
	if isMigrationCommand(args) {
		return runMigration(args, os.Stdout)
	}
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: theatarr migrate [--db path] up|status\n\n")
		fs.PrintDefaults()
		fmt.Fprint(fs.Output(), "\n"+migrationUsage)
	}
	db := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("want one of: up, status")
	}
	ctx := context.Background()
	s, err := store.Open(ctx, *db, store.Options{})
	if err != nil {
		return err
	}
	defer s.Close()

	switch fs.Arg(0) {
	case "up":
		res, err := s.Migrate(ctx)
		for _, r := range res {
			fmt.Printf("applied %05d %s (%s)\n", r.Source.Version, r.Source.Path, r.Duration.Round(time.Millisecond))
		}
		if err != nil {
			return err
		}
		cur, _, err := s.SchemaVersion(ctx)
		if err != nil {
			return err
		}
		if len(res) == 0 {
			fmt.Printf("%s: up to date at version %d\n", s.Path(), cur)
		} else {
			fmt.Printf("%s: now at version %d\n", s.Path(), cur)
		}
		return nil
	case "status":
		st, err := s.MigrationStatus(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "VERSION\tSTATE\tAPPLIED AT\tFILE")
		for _, m := range st {
			at := "-"
			if !m.AppliedAt.IsZero() {
				at = m.AppliedAt.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(tw, "%05d\t%s\t%s\t%s\n", m.Source.Version, m.State, at, m.Source.Path)
		}
		return tw.Flush()
	default:
		fs.Usage()
		return fmt.Errorf("unknown subcommand %q", fs.Arg(0))
	}
}

// defaultDBPath is $THEATARR_DB, else /data/theatarr.db (the container volume).
func defaultDBPath() string {
	if p := os.Getenv("THEATARR_DB"); p != "" {
		return p
	}
	return "/data/theatarr.db"
}
