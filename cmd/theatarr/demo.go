package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/theatarr/theatarr/internal/demo"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

func init() {
	register(command{
		name:    "demo",
		summary: "demo data: seed (an invented library for trying Theatarr, screenshots and reviews)",
		run:     runDemo,
	})
}

func runDemo(args []string) error {
	if len(args) == 0 || args[0] != "seed" {
		fmt.Fprint(os.Stderr, "usage: theatarr demo seed -db FILE [-policy DIR] [-seed N] [-now YYYY-MM-DD] [-art=false] [-force]\n")
		if helpArg(args) {
			return flag.ErrHelp
		}
		return errors.New("want: seed")
	}
	return demoSeed(args[1:], os.Stdout)
}

// demoSeed builds a new database with the demo dataset (docs/demo.md). It refuses an existing file unless -force.
func demoSeed(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("demo seed", flag.ContinueOnError)
	dbPath := fs.String("db", "", "database file to create (required)")
	policyDir := fs.String("policy", envOr("THEATARR_POLICY", "examples/policy"), "policy directory the demo runs with (env THEATARR_POLICY)")
	seed := fs.Uint64("seed", 1, "seed for the generated details; the same seed and -now give the same data")
	nowFlag := fs.String("now", "", "anchor time, YYYY-MM-DD (noon UTC) or RFC 3339 (default: now): titles are added before it, releases and air dates fall around it")
	art := fs.Bool("art", true, "write generated poster and backdrop PNGs into the policy's image cache (ui.images.dir)")
	force := fs.Bool("force", false, "replace an existing database file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if *dbPath == "" {
		return errors.New("-db is required")
	}
	var now time.Time
	if *nowFlag != "" {
		d, err := time.Parse("2006-01-02", *nowFlag)
		if err == nil {
			d = d.Add(12 * time.Hour)
		} else if d, err = time.Parse(time.RFC3339, *nowFlag); err != nil {
			return fmt.Errorf("-now: want YYYY-MM-DD or RFC 3339: %w", err)
		}
		now = d
	}
	pol, err := policy.Load(*policyDir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(*dbPath); err == nil {
		if !*force {
			return fmt.Errorf("%s exists; the demo writes a new database (use -force to replace it)", *dbPath)
		}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Remove(*dbPath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o755); err != nil {
		return err
	}
	ctx := context.Background()
	st, err := store.Open(ctx, *dbPath, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		return err
	}
	var images string
	if *art {
		dataDir, err := filepath.Abs(filepath.Dir(*dbPath))
		if err != nil {
			return err
		}
		if pol.Metadata != nil && pol.Metadata.Sources[policy.TMDbImagesSource] != nil {
			images = pol.ImageSettings(dataDir).Dir
		} else {
			fmt.Fprintf(out, "no image cache in this policy (metadata.sources.%s): titles get generated SVG art\n",
				policy.TMDbImagesSource)
		}
	}
	res, err := demo.Seed(ctx, st, demo.Options{Policy: pol, Seed: *seed, Now: now, ImagesDir: images,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "demo database written: %s (policy %s, seed %d, -now %s)\n", *dbPath, pol.Dir, *seed,
		res.Now.Format(time.RFC3339))
	keys := make([]string, 0, len(res.Counts))
	for k := range res.Counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(out, "  %-18s %d\n", k, res.Counts[k])
	}
	switch {
	case res.ArtError != "":
		fmt.Fprintf(out, "art: not written to %s (%s); titles get generated SVG art\n", images, res.ArtError)
	case images != "":
		fmt.Fprintf(out, "art: %d generated images in %s\n", res.Art, images)
	}
	fmt.Fprintf(out, "\nDEMO ONLY: sign in as %q, %q, %q or %q with password %q.\n", demo.Username,
		demo.ManagerUsername, demo.AutoApproveUsername, demo.LimitedUsername, demo.Password)
	fmt.Fprint(out, "This password is public and this data is invented; never use either for a real instance.\n")
	fmt.Fprintf(out, "Run it with the network off: theatarr serve -db %s -policy %s\n", *dbPath, *policyDir)
	fmt.Fprint(out, "Its settings are in the database; -policy is the directory for its data files and backups.\n")
	return nil
}
