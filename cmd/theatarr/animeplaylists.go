package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"text/tabwriter"

	"github.com/theatarr/theatarr/internal/buildinfo"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/metadata/anime/playlists"
	"github.com/theatarr/theatarr/internal/plex"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// newPlaylistSyncer builds the playlist syncer: the Plex client (library.plex, its token from the environment)
// only when the policy's gate allows writes, so a gated sync has no client to call.
func newPlaylistSyncer(ctx context.Context, pol *policy.Policy, st *store.Store, gw *gateway.Gateway, log *slog.Logger) (*playlists.Syncer, error) {
	s := &playlists.Syncer{Store: st, Policy: pol}
	if ok, _ := pol.PlaylistGate(); !ok {
		return s, nil
	}
	p := pol.Library.Plex
	tok := pol.Getenv(p.TokenEnv)
	if tok == "" {
		log.Warn("playlists: library.plex token is not set; dry runs only", "env", p.TokenEnv)
		return s, nil
	}
	if gw == nil {
		var err error
		if gw, err = gateway.New(ctx, pol, gateway.Options{Store: st, Version: buildinfo.Version, Logger: log}); err != nil {
			return nil, err
		}
	}
	c, err := plex.New(gw, p.URL, tok)
	if err != nil {
		return nil, err
	}
	s.Plex = c
	return s, nil
}

func runAnimePlaylists(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("anime playlists", flag.ContinueOnError)
	settingsSourceFlag(fs)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: theatarr anime playlists [-franchise SLUG] [-dry-run] [flags]\n\n"+
			"Plans the watch-order playlists (policy playlists.orders, one per route) from the library's linked episodes\n"+
			"and movies, and syncs them to Plex when library.enabled and playlists.enabled are both true and the Plex\n"+
			"token is set. Otherwise, or with -dry-run, it calls nothing and prints what it would create.\n\n")
		fs.PrintDefaults()
	}
	policyDir := policyDirDefault()
	db := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	envFile := fs.String("env-file", "", "read KEY=VALUE secrets from this file (variables already set win)")
	franchise := fs.String("franchise", "", "one franchise (default every built franchise, or playlists.franchises)")
	dryRun := fs.Bool("dry-run", false, "plan only, even when the gate is open")
	asJSON := fs.Bool("json", false, "print JSON")
	items := fs.Bool("items", false, "list each playlist's items")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envFile != "" {
		if err := loadEnvFile(*envFile); err != nil {
			return err
		}
	}
	ctx := context.Background()
	pol, err := loadCommandPolicy(fs, policyDir)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, *db, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	s, err := newPlaylistSyncer(ctx, pol, st, nil, log)
	if err != nil {
		return err
	}
	rep, err := s.Sync(ctx, *franchise, *dryRun)
	if err != nil && rep == nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if e := enc.Encode(rep); e != nil {
			return e
		}
		return err
	}
	if rep.DryRun {
		fmt.Fprintf(out, "DRY RUN (%s): nothing was sent to Plex\n", rep.Refused)
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PLAYLIST\tITEMS\tMISSING\tPLEX")
	for _, pl := range rep.Playlists {
		state := "would create or update"
		switch {
		case pl.Error != "":
			state = "failed: " + pl.Error
		case pl.Sync != nil:
			state = fmt.Sprintf("%s: created %v, +%d -%d moved %d", pl.Sync.RatingKey, pl.Sync.Created, pl.Sync.Added,
				pl.Sync.Removed, pl.Sync.Moved)
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\n", pl.Name, pl.Ready, pl.Missing, state)
	}
	tw.Flush()
	if *items {
		for _, pl := range rep.Playlists {
			fmt.Fprintf(out, "\n%s\n", pl.Name)
			for _, it := range pl.Items {
				what := it.Episode
				if it.Kind == "movie" {
					what = fmt.Sprintf("tmdb %d", it.TMDb)
				}
				fmt.Fprintf(out, "  %3d %-14s %-10s %s", it.Row, it.Entry, what, it.Title)
				if it.Missing != "" {
					fmt.Fprintf(out, "  [%s]", it.Missing)
				}
				fmt.Fprintln(out)
			}
		}
	}
	return err
}
