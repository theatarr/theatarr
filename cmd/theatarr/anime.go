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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/buildinfo"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animestore"
	"github.com/theatarr/theatarr/internal/metadata/anime/autobuild"
	"github.com/theatarr/theatarr/internal/metadata/anime/live"
	"github.com/theatarr/theatarr/internal/metadata/anime/offlinecache"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

func init() {
	register(command{
		name:    "anime",
		summary: "anime franchises: build mappings and watch orders (offline from a cache, or live), review queue",
		run:     runAnime,
	})
}

func runAnime(args []string) error {
	if len(args) == 0 || helpArg(args) {
		fmt.Fprint(os.Stderr, "usage: theatarr anime build|review|playlists [flags]\n")
		if helpArg(args) {
			return flag.ErrHelp
		}
		return errors.New("no subcommand")
	}
	switch args[0] {
	case "build":
		return runAnimeBuild(args[1:])
	case "review":
		return runAnimeReview(args[1:])
	case "playlists":
		return runAnimePlaylists(args[1:], os.Stdout)
	}
	return fmt.Errorf("unknown subcommand %q (build, review, playlists)", args[0])
}

func runAnimeBuild(args []string) error {
	fs := flag.NewFlagSet("anime build", flag.ContinueOnError)
	settingsSourceFlag(fs)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: theatarr anime build -franchise <slug> [flags]\n\n"+
			"Builds one franchise and prints its orders, mappings and the decisions that need review, as Markdown like\n"+
			"out/<slug>.md. With -cache it runs offline from an offline cache directory; without, it fetches live\n"+
			"through the gateway (needs -policy) and reads TVDB episodes from the imported Sonarr in the database.\n\n")
		fs.PrintDefaults()
	}
	slug := fs.String("franchise", "", "franchise slug (from -franchises)")
	seed := fs.Int("seed", 0, "build an ad-hoc franchise from this AniList id instead (with -franchise as its slug)")
	scope := fs.String("scope", "", `with -seed: "stem" limits the line to same-stem works`)
	cacheDir := fs.String("cache", "", "offline: an offline cache directory of the source responses (layout as internal/metadata/anime/testdata/offline/cache)")
	franchises := fs.String("franchises", "", "franchise list (JSON or YAML); default <cache>/../franchises.json, else <policy>/<overrides_dir>/franchises.yaml")
	ovDir := fs.String("overrides", "", "overrides directory; default <cache>/../overrides, else the policy's metadata.overrides_dir")
	comDir := fs.String("community", "", "community orders to compare with (a community/ directory); default <cache>/../community")
	today := fs.String("today", "", "date for the unaired flag and the header (YYYY-MM-DD); default today")
	asJSON := fs.Bool("json", false, "print the full result as JSON instead of Markdown")
	outDir := fs.String("out", "", "also write <slug>.json and <slug>.md here")
	save := fs.Bool("save", false, "store the build in the database (held for review if it drops episodes or moves linked mappings)")
	db := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB); live builds and -save use it")
	policyDir := policyDirDefault()
	envFile := fs.String("env-file", "", "read KEY=VALUE secrets from this file (variables already set win)")
	instance := fs.String("sonarr-instance", "", "live: limit the \"in Sonarr\" check to this imported Sonarr instance (default: any)")
	tvdbCache := fs.String("tvdb-cache", "", "live: also read TVDB series the Sonarr lacks from an offline cache (its skyhook/)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *slug == "" {
		fs.Usage()
		return errors.New("-franchise is required")
	}
	if *envFile != "" {
		if err := loadEnvFile(*envFile); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var pol *policy.Policy
	if *cacheDir == "" {
		p, err := loadCommandPolicy(fs, policyDir)
		if err != nil {
			return err
		}
		pol = p
	}
	pocRoot := ""
	if *cacheDir != "" {
		pocRoot = filepath.Dir(filepath.Clean(*cacheDir))
	}
	policyOverrides := ""
	if pol != nil && pol.Metadata != nil && pol.Metadata.OverridesDir != "" {
		policyOverrides = pol.OverridesPath()
	}
	pick := func(flagVal, pocSub, fallback string) string {
		if flagVal != "" {
			return flagVal
		}
		if pocRoot != "" {
			if _, err := os.Stat(filepath.Join(pocRoot, pocSub)); err == nil {
				return filepath.Join(pocRoot, pocSub)
			}
		}
		return fallback
	}
	var fr anime.Franchise
	if *seed != 0 {
		fr = anime.Franchise{Slug: *slug, Name: *slug, SeedAniList: *seed, Scope: *scope}
	} else {
		fallback := ""
		if policyOverrides != "" {
			fallback = filepath.Join(policyOverrides, "franchises.yaml")
		}
		path := pick(*franchises, "franchises.json", fallback)
		if path == "" {
			return errors.New("no franchise list: pass -franchises or -cache with a franchises.json beside it, or set metadata.overrides_dir in the settings (relative to the policy directory, env THEATARR_POLICY)")
		}
		list, err := autobuild.LoadFranchises(path)
		if err != nil {
			return err
		}
		found := false
		for _, f := range list {
			if f.Slug == *slug {
				fr, found = f, true
			}
		}
		if !found {
			return fmt.Errorf("franchise %q is not in %s", *slug, path)
		}
	}
	ov, err := anime.LoadOverrides(pick(*ovDir, "overrides", policyOverrides), fr.Slug)
	if err != nil {
		return err
	}
	var com *anime.Community
	if d := pick(*comDir, "community", ""); d != "" {
		if b, err := os.ReadFile(filepath.Join(d, fr.Slug+".json")); err == nil {
			var c anime.Community
			if err := json.Unmarshal(b, &c); err != nil {
				return err
			}
			com = &c
		}
	}

	var st *store.Store
	openDB := func() error {
		if st != nil {
			return nil
		}
		s, err := store.Open(ctx, *db, store.Options{})
		if err != nil {
			return err
		}
		if _, err := s.Migrate(ctx); err != nil {
			s.Close()
			return err
		}
		st = s
		return nil
	}
	defer func() {
		if st != nil {
			st.Close()
		}
	}()

	var src *anime.Sources
	source := "cache"
	var fetcher *live.Fetcher
	if *cacheDir != "" {
		if src, _, err = offlinecache.Sources(*cacheDir); err != nil {
			return err
		}
	} else {
		if pol == nil {
			return errors.New("a live build needs -policy (or -cache DIR for an offline build)")
		}
		if err := openDB(); err != nil {
			return err
		}
		source = "live"
		if src, fetcher, err = liveSources(ctx, pol, st, *instance); err != nil {
			return err
		}
		if *tvdbCache != "" {
			c, err := offlinecache.Open(*tvdbCache)
			if err != nil {
				return err
			}
			src.TVDB = anime.TVDBChain{src.TVDB, offlinecache.SkyHook{C: c}}
		}
	}
	t0 := time.Now()
	res, err := anime.Build(ctx, src, fr, anime.Options{Today: *today, Overrides: ov, Community: com})
	if err != nil {
		return err
	}
	if fetcher != nil {
		fmt.Fprintf(os.Stderr, "built %s in %s: %d requests, %d cached, %d stale\n", fr.Slug,
			time.Since(t0).Round(time.Millisecond), fetcher.Requests, fetcher.Hits, fetcher.Stale)
	}
	ovPath := "(none)"
	if ov != nil {
		ovPath = ov.File
	}
	md := anime.Markdown(res, "theatarr anime build", ovPath) + reviewSection(res)
	body, err := json.MarshalIndent(res, "", " ")
	if err != nil {
		return err
	}
	if *outDir != "" {
		if err := os.MkdirAll(*outDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*outDir, fr.Slug+".json"), body, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*outDir, fr.Slug+".md"), []byte(md), 0o644); err != nil {
			return err
		}
	}
	if *asJSON {
		_, err = os.Stdout.Write(append(body, '\n'))
	} else {
		_, err = io.WriteString(os.Stdout, md)
	}
	if err != nil {
		return err
	}
	if *save {
		if err := openDB(); err != nil {
			return err
		}
		s := &animestore.Store{DB: st}
		saved, err := s.Save(ctx, res, source, false)
		if err != nil {
			return err
		}
		if saved.Applied {
			fmt.Fprintf(os.Stderr, "saved %s; %d new review items\n", fr.Slug, saved.NewReview)
		} else {
			fmt.Fprintf(os.Stderr, "held for review (%d new items): %s\n", saved.NewReview, strings.Join(saved.Held, "; "))
		}
	}
	return nil
}

// reviewSection lists the mapping decisions a person should look at (not in the golden Markdown).
func reviewSection(res *anime.Result) string {
	var lines []string
	counts := map[string]int{}
	for _, e := range res.Entries {
		m := e.Mapping
		if m == nil {
			continue
		}
		counts[m.Status]++
		if m.Status != anime.StatusConflict && m.Status != anime.StatusHeuristic && m.Status != anime.StatusArbitrated {
			continue
		}
		rng := ""
		if e.TVDB != nil {
			rng = fmt.Sprintf("TVDB %d %s", e.TVDB.SeriesID, e.TVDB.Range)
		}
		var cands []string
		for _, c := range m.Candidates {
			s := fmt.Sprintf("%s %s score %.2f", c.Source, c.Range, c.Score)
			if c.Dropped != "" {
				s += " (dropped: " + c.Dropped + ")"
			}
			if c.Anchor != "" {
				s += " anchor " + c.Anchor
			}
			cands = append(cands, s)
		}
		lines = append(lines, fmt.Sprintf("| %s | `%s` | %s | %s | %s |", m.Status, e.Key, rng, m.Reason, strings.Join(cands, "; ")))
	}
	var b strings.Builder
	b.WriteString("\n## Mapping decisions\n\n")
	var parts []string
	for _, s := range []string{anime.StatusAuto, anime.StatusArbitrated, anime.StatusConflict, anime.StatusHeuristic,
		anime.StatusOverride, anime.StatusRejected} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", s, counts[s]))
		}
	}
	b.WriteString(strings.Join(parts, ", ") + ". Conflict and rejected entries import through Sonarr's numbering; accepting one in review re-links its files.\n")
	if len(lines) > 0 {
		b.WriteString("\n| Status | Entry | Mapping | Why | Candidates |\n|---|---|---|---|---|\n")
		b.WriteString(strings.Join(lines, "\n") + "\n")
	}
	return b.String()
}

// liveSources wires the live sources the service uses too (autobuild.LiveSources), with the TVDB client when its key
// is set, and loads the mapping lists.
func liveSources(ctx context.Context, pol *policy.Policy, st *store.Store, instance string) (*anime.Sources, *live.Fetcher, error) {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	gw, err := gateway.New(ctx, pol, gateway.Options{Store: st, Version: buildinfo.Version, Logger: log})
	if err != nil {
		return nil, nil, err
	}
	o := autobuild.SourceOptions{Store: st, Doer: gw, SonarrInstance: instance, Logger: log}
	// TVDB v4 first when the policy has it and the key is set (cmd/theatarr/tvdb.go); the imported Sonarr when TVDB is
	// down with nothing cached, or does not have the series
	if tc, err := newTVDBClient(pol, st, gw); err != nil {
		return nil, nil, err
	} else if tc != nil {
		o.TVDB = tc
	} else {
		fmt.Fprintln(os.Stderr, "no TVDB key: TVDB episodes come from the imported Sonarr")
	}
	l, err := autobuild.LiveSources(pol, o)
	if err != nil {
		return nil, nil, err
	}
	fmt.Fprintln(os.Stderr, "loading the mapping lists (daily) ...")
	src, err := l.Sources(ctx)
	if err != nil {
		return nil, nil, err
	}
	if src.Movies == nil {
		fmt.Fprintln(os.Stderr, "no TMDb key: TMDb ids are not checked")
	}
	return src, l.Fetcher, nil
}

func runAnimeReview(args []string) error {
	fs := flag.NewFlagSet("anime review", flag.ContinueOnError)
	settingsSourceFlag(fs)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: theatarr anime review [flags]\n\n"+
			"Lists the metadata review queue: mapping conflicts, heuristic mappings, and refreshes held back.\n"+
			"-accept/-reject ID resolves an item (accepting a held refresh applies it).\n\n")
		fs.PrintDefaults()
	}
	db := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	franchise := fs.String("franchise", "", "only this franchise")
	state := fs.String("state", "open", "open, accepted, rejected, superseded, or all")
	accept := fs.Int64("accept", 0, "accept this item")
	candidate := fs.Int("candidate", -1, "with -accept: accept this candidate (0-based, as -json lists them) instead of the proposed mapping")
	snippet := fs.Int64("snippet", 0, "print the overrides YAML that pins this item's mapping")
	reject := fs.Int64("reject", 0, "reject this item")
	note := fs.String("note", "", "note to keep with the decision")
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
	if _, err := st.Migrate(ctx); err != nil {
		return err
	}
	s := &animestore.Store{DB: st}
	if *snippet != 0 {
		y, err := s.Snippet(ctx, *snippet)
		if err != nil {
			return err
		}
		fmt.Print(y)
		return nil
	}
	if *accept != 0 && *candidate >= 0 {
		if err := s.ResolveCandidate(ctx, *accept, *candidate, *note); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "item %d: candidate %d accepted\n", *accept, *candidate)
		return nil
	}
	if *accept != 0 || *reject != 0 {
		id, ok := *accept, true
		if *reject != 0 {
			id, ok = *reject, false
		}
		if err := s.Resolve(ctx, id, ok, *note); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "item %d %s\n", id, map[bool]string{true: "accepted", false: "rejected"}[ok])
		return nil
	}
	if *state == "all" {
		*state = ""
	}
	items, err := s.Items(ctx, *state, *franchise)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(items)
	}
	if len(items) == 0 {
		fmt.Println("no review items")
		return nil
	}
	for _, it := range items {
		fmt.Printf("%s  %-20s %-16s %-21s %s\n", strconv.FormatInt(it.ID, 10), it.Franchise, it.State, it.Kind, it.Summary)
	}
	return nil
}
