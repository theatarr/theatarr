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
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/theatarr/theatarr/internal/buildinfo"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/metadata/tvdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

func init() {
	register(command{
		name:    "tvdb",
		summary: "TVDB v4 lookups through the gateway and cache: series, episodes per order, search, /updates refresh",
		run:     runTVDB,
	})
}

// tvdbPINEnv names the env var with the subscriber PIN, needed only for a user-supported key.
const tvdbPINEnv = "THEATARR_TVDB_PIN"

// newTVDBClient builds the TVDB client from the policy's metadata.sources.tvdb (url, api_key_env). It returns nil
// and no error when the policy has no tvdb source or the key is not set, so callers fall back to other backends.
func newTVDBClient(pol *policy.Policy, st *store.Store, gw *gateway.Gateway) (*tvdb.Client, error) {
	if pol == nil || pol.Metadata == nil || pol.Metadata.Sources[tvdb.Source] == nil {
		return nil, nil
	}
	src := pol.Metadata.Sources[tvdb.Source]
	if src.URL == "" || src.APIKeyEnv == "" {
		return nil, nil
	}
	key := pol.Getenv(src.APIKeyEnv)
	if key == "" {
		return nil, nil
	}
	return tvdb.New(tvdb.Options{BaseURL: src.URL, APIKey: key, PIN: os.Getenv(tvdbPINEnv), Doer: gw, Store: st})
}

func runTVDB(args []string) error {
	usage := "usage: theatarr tvdb series ID | episodes ID [-type default|absolute|dvd|alternate|...] [-lang eng] | show ID |\n" +
		"       search QUERY [-type series] | remote IMDB-OR-TMDB-ID | updates |\n" +
		"       refresh TITLE-ID [-force] (store a series' episodes now, docs/design/tv-metadata.md)\n" +
		"       [-policy DIR -env-file FILE -db FILE -json]\n"
	if len(args) == 0 || helpArg(args) {
		fmt.Fprint(os.Stderr, usage)
		if helpArg(args) {
			return flag.ErrHelp
		}
		return errors.New("no subcommand")
	}
	sub := args[0]
	fs := flag.NewFlagSet("tvdb "+sub, flag.ContinueOnError)
	settingsSourceFlag(fs)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usage+"\nLive lookups (on the service host): every request goes through the gateway and the database cache.\n\n")
		fs.PrintDefaults()
	}
	db := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB): the response cache and the /updates watermark")
	policyDir := policyDirDefault()
	envFile := fs.String("env-file", "", "read KEY=VALUE secrets from this file (variables already set win)")
	asJSON := fs.Bool("json", false, "print JSON")
	typ := fs.String("type", "", "episodes: the order (default, official, absolute, dvd, alternate, regional); search: series, movie, ...")
	lang := fs.String("lang", "", "episodes: names in this language (ISO 639-2, e.g. eng)")
	force := fs.Bool("force", false, "refresh: read TVDB past the response cache")
	// flags may come before or after the positional argument
	var pos []string
	rest := args[1:]
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		rest = fs.Args()
		if len(rest) > 0 {
			pos = append(pos, rest[0])
			rest = rest[1:]
		}
	}
	if *envFile != "" {
		if err := loadEnvFile(*envFile); err != nil {
			return err
		}
	}
	if policyDir == "" {
		return errors.New("-policy is required")
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
	gw, err := gateway.New(ctx, pol, gateway.Options{Store: st, Version: buildinfo.Version,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))})
	if err != nil {
		return err
	}
	c, err := newTVDBClient(pol, st, gw)
	if err != nil {
		return err
	}
	if sub == "refresh" {
		// The series refresh writes episodes; TMDb stands in for TVDB as the service does.
		if len(pos) != 1 {
			return errors.New("tvdb refresh takes one title id")
		}
		id, err := strconv.ParseInt(pos[0], 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("bad title id %q", pos[0])
		}
		tv, err := newServeSeriesMeta(pol, st, nil, gw, c, slog.New(slog.NewTextHandler(os.Stderr, nil)))
		if err != nil {
			return err
		}
		res, err := tv.RefreshTitle(ctx, id, *force)
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(res, "", "  ")
		_, err = os.Stdout.Write(append(b, '\n'))
		return err
	}
	if c == nil {
		return errors.New("no TVDB: the policy needs metadata.sources.tvdb (url, api_key_env) and the key set")
	}
	id := func() (int, error) {
		if len(pos) != 1 {
			return 0, fmt.Errorf("tvdb %s takes one id", sub)
		}
		n, err := strconv.Atoi(pos[0])
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("bad id %q", pos[0])
		}
		return n, nil
	}
	var out any
	switch sub {
	case "series":
		n, err := id()
		if err != nil {
			return err
		}
		if out, err = c.Series(ctx, n); err != nil {
			return err
		}
	case "episodes":
		n, err := id()
		if err != nil {
			return err
		}
		s, eps, err := c.Episodes(ctx, n, *typ, *lang)
		if err != nil {
			return err
		}
		if !*asJSON {
			printTVDBEpisodes(os.Stdout, s, eps)
			break
		}
		out = map[string]any{"series": s, "episodes": eps}
	case "show":
		n, err := id()
		if err != nil {
			return err
		}
		if out, err = c.Show(ctx, n); err != nil {
			return err
		}
	case "search":
		if len(pos) == 0 {
			return errors.New("tvdb search takes a query")
		}
		if out, err = c.Search(ctx, strings.Join(pos, " "), *typ); err != nil {
			return err
		}
	case "remote":
		if len(pos) != 1 {
			return errors.New("tvdb remote takes one IMDb or TMDb id")
		}
		if out, err = c.SearchRemoteID(ctx, pos[0]); err != nil {
			return err
		}
	case "updates":
		if out, err = c.Refresh(ctx); err != nil {
			return err
		}
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown subcommand %q", sub)
	}
	if out != nil {
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		if _, err := os.Stdout.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "tvdb: %d requests, %d cached, %d stale, %d logins\n", c.F.Requests, c.F.Hits, c.F.Stale, c.Logins())
	return nil
}

func printTVDBEpisodes(w io.Writer, s *tvdb.SeriesBase, eps []tvdb.Episode) {
	if s != nil {
		fmt.Fprintf(w, "%d %s (%s, %s)\n", s.ID, s.Name, s.Year, s.Status.Name)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSxxEyy\tABS\tAIRED\tAIRS\tNAME")
	for _, e := range eps {
		abs := ""
		if e.AbsoluteNumber != nil && *e.AbsoluteNumber > 0 {
			abs = strconv.Itoa(*e.AbsoluteNumber)
		}
		var airs []string
		if e.AirsBeforeSeason != nil {
			b := fmt.Sprintf("before S%02d", *e.AirsBeforeSeason)
			if e.AirsBeforeEpisode != nil {
				b += fmt.Sprintf("E%02d", *e.AirsBeforeEpisode)
			}
			airs = append(airs, b)
		}
		if e.AirsAfterSeason != nil {
			airs = append(airs, fmt.Sprintf("after S%02d", *e.AirsAfterSeason))
		}
		fmt.Fprintf(tw, "%d\tS%02dE%02d\t%s\t%s\t%s\t%s\n", e.ID, e.SeasonNumber, e.Number, abs, tvdbDeref(e.Aired),
			strings.Join(airs, ", "), tvdbDeref(e.Name))
	}
	tw.Flush()
}

func tvdbDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
