package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/theatarr/theatarr/internal/buildinfo"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

func init() {
	register(command{
		name:    "search",
		summary: "search indexers through the gateway and print the releases (with configured VPN requirements)",
		run:     func(args []string) error { return runSearch(args, os.Stdout, nil) },
	})
}

// errNotOnVPN is what theatarr search says when it may not search from here.
var errNotOnVPN = errors.New("refusing to search: indexer traffic must leave through the VPN, and it does not from " +
	"here. Run theatarr search where the VPN routes it (in the Theatarr container)")

// runSearch is theatarr search. dial replaces the gateway's dialer (tests only).
func runSearch(args []string, out io.Writer, dial func(context.Context, string, string) (net.Conn, error)) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	settingsSourceFlag(fs)
	policyDir := policyDirDefault()
	dbPath := fs.String("db", os.Getenv("THEATARR_DB"), "database for budget caps, back-offs and the metadata lookups (AniList ids, episode counts) (env THEATARR_DB); empty keeps them in memory")
	indexers := fs.String("indexer", "", "indexer key(s), comma-separated, or \"all\" for every indexer that can answer")
	typ := fs.String("type", "", "movie, series, text or rss (default: series with -season, movie with an id, else text)")
	title := fs.String("title", "", "title, or the text for -type text")
	year := fs.Int("year", 0, "year (movies)")
	imdb := fs.String("imdb", "", "IMDb id (tt…)")
	tmdb := fs.Int("tmdb", 0, "TMDb id")
	tvdb := fs.Int("tvdb", 0, "TVDB id")
	anilist := fs.Int("anilist", 0, "AniList id (SeaDex)")
	season := fs.Int("season", 0, "season")
	episode := fs.Int("episode", 0, "episode (0: the whole season)")
	anime := fs.Bool("anime", false, "anime categories")
	asJSON := fs.Bool("json", false, "print JSON lines (links are never printed)")
	limit := fs.Int("limit", 50, "releases to print per indexer (0: all)")
	tbCheck := fs.Bool("torbox-check", true, "ask TorBox's checkcached (our key, its budget) about the first hashes, per router.cache_evidence.torbox_check")
	verbose := fs.Bool("v", false, "log the gateway's and adapters' lines to stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if *indexers == "" {
		return errors.New("-indexer is required (a key from the policy, several comma-separated, or all)")
	}
	q := indexer.Query{Type: indexer.QueryType(*typ), Title: *title, Year: *year, IMDb: *imdb, TMDb: *tmdb, TVDB: *tvdb,
		AniList: *anilist, Season: *season, Episode: *episode, Anime: *anime}
	if q.Type == "" {
		switch {
		case q.Season > 0:
			q.Type = indexer.QuerySeries
		case q.IMDb != "" || q.TMDb > 0 || q.AniList > 0:
			q.Type = indexer.QueryMovie
		default:
			q.Type = indexer.QueryText
		}
	}
	if err := q.Validate(); err != nil {
		return err
	}

	pol, err := loadCommandPolicy(fs, policyDir)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if *verbose {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var st *store.Store
	if *dbPath != "" {
		if st, err = store.Open(ctx, *dbPath, store.Options{}); err != nil {
			return err
		}
		defer st.Close()
		if _, err := st.Migrate(ctx); err != nil {
			return err
		}
	}
	gw, err := gateway.New(ctx, pol, gateway.Options{Store: st, Logger: log, Version: buildinfo.Version, DialContext: dial})
	if err != nil {
		return err
	}
	defer gw.FlushRequests(context.WithoutCancel(ctx)) // the indexer request counts (docs/design/gateway.md)
	svc, err := indexer.New(pol, gw, indexer.Options{Logger: log})
	if err != nil {
		return err
	}
	if st != nil {
		// With the database, the lookups theatarr serve uses: SeaDex finds AniList ids from -tvdb/-season or -tmdb, and
		// Torrentio scales season packs by the episode counts.
		tvc, err := newTVDBClient(pol, st, gw)
		if err != nil {
			return err
		}
		wireIndexerLookups(pol, st, gw, svc, tvc, log)
	}
	// Cache evidence for cachedOn: cross-feed always, stored evidence with -db, TorBox's check with its key set.
	if *tbCheck {
		wireCacheEvidence(pol, st, svc, torboxCheckers(pol, gw), log)
	} else {
		wireCacheEvidence(pol, st, svc, nil, log)
	}
	keys, err := searchKeys(svc, *indexers, q)
	if err != nil {
		return err
	}

	// Check first so a required VPN refusal is clear; monitor-only checks still record their observations.
	for _, s := range gw.CheckEgress(ctx) {
		if !s.OK && pol.Egress.RequiresVPN(policy.EgressIndexers) {
			return fmt.Errorf("%w (egress %s: %s)", errNotOnVPN, s.Path, s.Error)
		}
	}

	failed := 0
	var results []*indexer.Result
	var done []string
	for _, k := range keys {
		res, err := svc.Search(ctx, k, q)
		if err != nil {
			if errors.Is(err, gateway.ErrVPNRequired) {
				return fmt.Errorf("%w (%v)", errNotOnVPN, err)
			}
			failed++
			fmt.Fprintf(out, "# %s: %v\n", k, err)
			continue
		}
		results, done = append(results, res), append(done, k)
	}
	cacheEvidence(ctx, pol, svc, results, *limit)
	for i, res := range results {
		if err := printReleases(out, done[i], res, *limit, *asJSON); err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d indexer(s) failed", failed, len(keys))
	}
	return nil
}

// cacheEvidence fills CachedOn across every indexer's results (one search), asks DMM's availability, then runs the
// TorBox check on the releases printed, in print order. Every version counts: a hash goes to DMM or TorBox only when
// a debrid version that lists its indexer allows that provider kind.
func cacheEvidence(ctx context.Context, pol *policy.Policy, svc *indexer.Service, results []*indexer.Result, limit int) {
	var all []indexer.Release
	for _, r := range results {
		all = append(all, r.Releases...)
	}
	versions := make([]*policy.VersionPolicy, 0, len(pol.Versions))
	for _, k := range slices.Sorted(maps.Keys(pol.Versions)) {
		versions = append(versions, pol.Versions[k])
	}
	svc.ApplyCachedOn(ctx, all)
	svc.CheckAvailability(ctx, versions, all)
	var shown, rest []*indexer.Release
	n := 0
	for _, r := range results {
		r.Releases = all[n : n+len(r.Releases)]
		for i := range r.Releases {
			if limit <= 0 || i < limit {
				shown = append(shown, &r.Releases[i])
			} else {
				rest = append(rest, &r.Releases[i])
			}
		}
		n += len(r.Releases)
	}
	svc.CheckCached(ctx, versions, shown, rest)
}

func searchKeys(svc *indexer.Service, arg string, q indexer.Query) ([]string, error) {
	if arg == "all" {
		var keys []string
		for _, k := range svc.Keys() {
			if svc.Searcher(k).Supports(q) {
				keys = append(keys, k)
			}
		}
		if len(keys) == 0 {
			return nil, fmt.Errorf("no indexer can answer a %s search with the ids given", q.Type)
		}
		return keys, nil
	}
	var keys []string
	for _, k := range strings.Split(arg, ",") {
		k = strings.TrimSpace(k)
		if svc.Searcher(k) == nil {
			return nil, fmt.Errorf("unknown indexer %q (policy has %s)", k, strings.Join(svc.Keys(), ", "))
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// printReleases writes one indexer's result. Links never appear: Release marshals them as null, and the table
// shows only whether a link or magnet exists.
func printReleases(out io.Writer, key string, res *indexer.Result, limit int, asJSON bool) error {
	rel := res.Releases
	if limit > 0 && len(rel) > limit {
		rel = rel[:limit]
	}
	if asJSON {
		enc := json.NewEncoder(out)
		for i := range rel {
			if err := enc.Encode(&rel[i]); err != nil {
				return err
			}
		}
		return enc.Encode(map[string]any{"indexer": key, "results": len(res.Releases), "requests": res.Requests,
			"truncated": res.Truncated, "cached": res.Cached, "notes": res.Notes})
	}
	fmt.Fprintf(out, "# %s: %d release(s), %d request(s)", key, len(res.Releases), res.Requests)
	if res.Truncated {
		fmt.Fprint(out, ", truncated")
	}
	fmt.Fprintln(out)
	for _, n := range res.Notes {
		fmt.Fprintf(out, "#   %s\n", n)
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SIZE\tSEED\tPRIV\tHASH\tGET\tCACHED\tTITLE")
	for _, r := range rel {
		priv := "pub"
		if r.Privacy.IsPrivate() {
			priv = "PRIV"
		}
		seed := "-"
		if r.Seeders >= 0 {
			seed = fmt.Sprint(r.Seeders)
		}
		hash := "-"
		if r.InfoHash != "" {
			hash = r.InfoHash[:12]
		}
		get := []string{}
		if !r.Magnet.Empty() {
			get = append(get, "magnet")
		}
		if !r.Link.Empty() {
			get = append(get, "link")
		}
		cached := "-"
		if on := r.CachedList(); len(on) > 0 {
			cached = strings.Join(on, ",")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", humanSize(r.Size), seed, priv, hash, strings.Join(get, "+"),
			cached, r.Title)
	}
	if len(rel) < len(res.Releases) {
		fmt.Fprintf(tw, "…\t\t\t\t\t\t%d more (-limit 0 shows all)\n", len(res.Releases)-len(rel))
	}
	return tw.Flush()
}

func humanSize(n int64) string {
	if n <= 0 {
		return "-"
	}
	f := float64(n)
	for _, u := range []string{"B", "KiB", "MiB", "GiB"} {
		if f < 1024 {
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return fmt.Sprintf("%.2f TiB", f)
}
