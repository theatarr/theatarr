package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/buildinfo"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/decide/shadow"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

func init() {
	register(command{
		name:    "decide",
		summary: "decide a release for a title's library and print the decision with its score breakdown (-search: search first, where the VPN routes indexer traffic); decide shadow: replay arr grab history",
		run:     func(args []string) error { return runDecide(args, os.Stdout, nil) },
	})
}

const decideUsage = `usage:
  theatarr decide [flags] -title ID|-tmdb N -library KEY 'Release.Title.2024.2160p.WEB-DL-GRP'
  theatarr decide [flags] -title ID|-tmdb N -library KEY -search
  theatarr decide [flags] -tvdb N -library KEY [-season N [-episode N]] 'Release.Title'   (a series library)
  theatarr decide shadow [flags] -instance NAME -library KEY

The first decides one release title, with optional indexer attributes (-size, -flags, -languages, -seeders,
-indexer, -imdb). The second searches the library's indexers through the gateway first and ranks what
comes back; like theatarr search it applies the configured VPN requirements. The third replays an
arr instance's grab history from the database (or -corpus) through the engine and prints the agreement.

Profiles with from_arr read the arr's profile from -arr-snapshot (a corpus directory) or, without it, with GET
requests to the instance under import.arr (key from the environment or -env-file).

flags:
`

type decideFlags struct {
	policyDir, db, envFile, trash, arrSnapshot string
	settingsSource                             string
	snap                                       *settings.Snapshot // the stored settings engine loaded
	// readOnly opens the database read-only (store.Options.ReadOnly) to load the settings.
	readOnly bool
}

func (f *decideFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.settingsSource, "settings-source", settingsSourceDefault(), "deprecated: only db is supported (env THEATARR_SETTINGS_SOURCE)")
	f.policyDir = policyDirDefault()
	fs.StringVar(&f.db, "db", defaultDBPath(), "database file (env THEATARR_DB); read only")
	fs.StringVar(&f.envFile, "env-file", "", "read KEY=VALUE secrets from this file (variables already set win)")
	fs.StringVar(&f.trash, "trash", os.Getenv("THEATARR_TRASH"), "TRaSH-Guides checkout, for trash_id formats (env THEATARR_TRASH)")
	fs.StringVar(&f.arrSnapshot, "arr-snapshot", os.Getenv("THEATARR_ARR_SNAPSHOT"),
		"corpus directory with arr snapshots for from_arr profiles (env THEATARR_ARR_SNAPSHOT); empty reads the arrs live (GET)")
}

// engine loads the policy and builds the engine. A profile that fails to build is fatal only when version needs it.
func (f *decideFlags) engine(version string, warn io.Writer) (*policy.Policy, *decide.Engine, error) {
	if f.envFile != "" {
		if err := loadEnvFile(f.envFile); err != nil {
			return nil, nil, err
		}
	}
	pol, snap, err := loadSettingsSnapshotOpts(context.Background(), f.settingsSource, f.policyDir, f.db, f.readOnly)
	if err != nil {
		return nil, nil, err
	}
	f.snap = snap
	v := pol.Versions[version]
	if v == nil {
		return nil, nil, fmt.Errorf("the policy has no library %q", version)
	}
	var src decide.ArrSource
	if f.arrSnapshot != "" {
		src = decide.SnapshotDir(f.arrSnapshot)
	} else {
		src = liveArr(pol)
	}
	e, err := decide.New(pol, decide.Options{TrashRoot: f.trash, Arr: src})
	fl := format.Radarr
	if v.Kind == policy.KindSeries {
		fl = format.Sonarr
	}
	if err != nil {
		if e == nil || e.Profile(v.Profile, fl) == nil {
			return nil, nil, err
		}
		fmt.Fprintf(warn, "# other profiles failed to build:\n%s\n", indent(err.Error()))
	}
	for _, w := range e.Warnings {
		fmt.Fprintf(warn, "# %s\n", w)
	}
	return pol, e, nil
}

func indent(s string) string { return "#   " + strings.ReplaceAll(s, "\n", "\n#   ") }

// liveArr reads from_arr snapshots with GET requests to the instances under import.arr.
func liveArr(pol *policy.Policy) decide.ArrSource {
	return decide.ArrSourceFunc(func(instance string) (*decide.ArrSnapshot, error) {
		if pol.Import == nil || pol.Import.Arr[instance] == nil {
			return nil, fmt.Errorf("instance %q is not under import.arr", instance)
		}
		a := pol.Import.Arr[instance]
		key := pol.Getenv(a.APIKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("%s is not set (use -env-file, or -arr-snapshot for a corpus)", a.APIKeyEnv)
		}
		cl, err := arr.New(a.URL, key, arr.Options{})
		if err != nil {
			return nil, err
		}
		s := &decide.ArrSnapshot{Flavour: format.Flavour(a.Flavour)}
		for _, x := range []struct {
			ep  string
			dst *[]byte
		}{{"qualityprofile", &s.QualityProfile}, {"customformat", &s.CustomFormat},
			{"qualitydefinition", &s.QualityDefinition}, {"config/mediamanagement", &s.MediaManagement},
			{"config/indexer", &s.IndexerConfig}} {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			r, err := cl.Get(ctx, x.ep, nil)
			cancel()
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", instance, x.ep, err)
			}
			*x.dst = r.Body
		}
		return s, nil
	})
}

func runDecide(args []string, out io.Writer, dial func(context.Context, string, string) (net.Conn, error)) error {
	if len(args) > 0 && args[0] == "shadow" {
		return runDecideShadow(args[1:], out)
	}
	fs := flag.NewFlagSet("decide", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), decideUsage); fs.PrintDefaults() }
	var df decideFlags
	df.register(fs)
	titleID := fs.Int64("title", 0, "our title id")
	tmdb := fs.Int("tmdb", 0, "find the title by TMDb id")
	tvdb := fs.Int("tvdb", 0, "find the series by TVDB id")
	season := fs.Int("season", -1, "series: decide as found by a search for this season (with -search: search it)")
	episode := fs.Int("episode", 0, "series: with -season, a search for this one episode")
	version := libraryFlag(fs, "library key from the policy")
	search := fs.Bool("search", false, "search the library's indexers through the gateway and decide every result")
	size := fs.String("size", "", "release size: bytes, or with a unit (25GB, 700MiB)")
	flags := fs.String("flags", "", "indexer flags: arr names (G_Freeleech,G_Scene) or torznab tags (freeleech,internal,scene)")
	languages := fs.String("languages", "", "languages the indexer gave, comma-separated (English,French)")
	seeders := fs.Int("seeders", -1, "seeders")
	idx := fs.String("indexer", "", "indexer key from the policy (privacy and multi languages come from it)")
	imdb := fs.String("imdb", "", "IMDb id the indexer gave (tt…)")
	noFile := fs.Bool("no-existing", false, "decide as if the library had no file")
	asJSON := fs.Bool("json", false, "print JSON")
	limit := fs.Int("limit", 30, "-search: decisions to print (0: all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" || (*titleID == 0 && *tmdb == 0 && *tvdb == 0) {
		fs.Usage()
		return errors.New("-library and -title, -tmdb or -tvdb are required")
	}
	if *search == (fs.NArg() == 1) || fs.NArg() > 1 {
		fs.Usage()
		return errors.New("give either one release title or -search")
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
	ctx, err = decide.WithStoredSeaDex(ctx, db)
	if err != nil {
		return err
	}
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
		if err := decide.LoadSeriesVersion(ctx, db, &t, v.ID); err != nil {
			return err
		}
		if *noFile {
			for i := range t.Episodes {
				t.Episodes[i].File, t.Episodes[i].FileID = nil, 0
			}
		}
		if *season >= 0 {
			ss = seriesSearch(&t, *season, *episode)
		} else if *search {
			return errors.New("-search for a series needs -season (and -episode for one episode)")
		}
	} else if !*noFile {
		if existing, err = decide.LoadExisting(ctx, db, v.ID); err != nil {
			return err
		}
	}
	movies, err := decide.LoadMovies(ctx, db)
	if err != nil {
		return err
	}
	e.SetLibrary(decide.NewLibrary(movies))

	if *search {
		return decideSearch(decide.WithSearch(ctx), db, out, pol, indexerKeys(pol, df.snap), e, t, v, existing, ss, *limit, *asJSON, dial)
	}

	r := decide.Release{Release: indexer.Release{Indexer: *idx, Title: fs.Arg(0), Seeders: *seeders, Peers: *seeders,
		IMDb: *imdb, Privacy: policy.PrivacyPublic}}
	if *idx != "" {
		ix := pol.Indexers[*idx]
		if ix == nil {
			return fmt.Errorf("the policy has no indexer %q", *idx)
		}
		r.Kind, r.Privacy = ix.Kind, ix.Privacy
	}
	if *size != "" {
		if r.Size, err = parseSize(*size); err != nil {
			return err
		}
	}
	if *flags != "" {
		if err := applyFlags(&r, *flags); err != nil {
			return err
		}
	}
	for _, l := range strings.Split(*languages, ",") {
		if l = strings.TrimSpace(l); l != "" {
			r.Languages = append(r.Languages, l)
		}
	}
	var d *decide.Decision
	if t.Kind == policy.KindSeries {
		d, err = e.DecideSearch(ctx, r, t, v, ss)
	} else {
		d, err = e.Decide(decide.WithSearch(ctx), r, t, v, existing)
	}
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(d)
	}
	printDecision(out, t, v, e, d)
	return nil
}

func printDecision(out io.Writer, t decide.Title, v decide.Version, e *decide.Engine, d *decide.Decision) {
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	src := ""
	fl := format.Radarr
	if t.Kind == policy.KindSeries {
		fl = format.Sonarr
	}
	if p := e.Profile(d.Profile, fl); p != nil {
		src = " (" + p.Source + ")"
	}
	fmt.Fprintf(tw, "release\t%s\n", d.Release)
	fmt.Fprintf(tw, "title\t#%d %s (%d)\tversion %s (#%d), profile %s%s\n", t.ID, t.Name, t.Year, v.Key, v.ID, d.Profile, src)
	if d.Parsed && t.Kind == policy.KindSeries {
		fmt.Fprintf(tw, "parsed\t%s, %s, group %s, match %s, languages %s\n", d.SeriesTitle, d.ReleaseType,
			orDash(d.ReleaseGroup), orDash(string(d.MatchType)), orDash(strings.Join(d.Languages, ", ")))
		fmt.Fprintf(tw, "episodes\t%s (%d without a file)\n", orDash(strings.Join(d.Episodes, " ")), d.Missing)
	} else if d.Parsed {
		fmt.Fprintf(tw, "parsed\t%s %d, group %s, edition %s, match %s, languages %s\n", strings.Join(d.MovieTitles, " / "),
			d.Year, orDash(d.ReleaseGroup), orDash(d.Edition), orDash(string(d.MatchType)), orDash(strings.Join(d.Languages, ", ")))
	}
	fmt.Fprintf(tw, "quality\t%s %s\n", d.Quality, d.Revision)
	fmt.Fprintf(tw, "formats\t%s\n", formatList(d.Formats, d.Score))
	if d.Existing != nil {
		fmt.Fprintf(tw, "existing\t%s, %s\n", d.Existing.Quality, formatList(d.Existing.Formats, d.Existing.Score))
		yes := "no"
		if d.Upgrade {
			yes = "yes"
		}
		fmt.Fprintf(tw, "upgrade\t%s: %s\n", yes, d.UpgradeReason)
	} else {
		fmt.Fprintf(tw, "existing\tnone\n")
	}
	if d.Accepted {
		fmt.Fprintf(tw, "decision\tACCEPTED\n")
	} else {
		fmt.Fprintf(tw, "decision\tREJECTED\n")
		for _, r := range d.Rejections {
			fmt.Fprintf(tw, "\t%s\n", r)
		}
	}
	tw.Flush()
}

func formatList(fs []decide.FormatScore, total int) string {
	var parts []string
	for _, f := range fs {
		parts = append(parts, fmt.Sprintf("%s %+d", f.Name, f.Score))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("none, score %d", total)
	}
	return fmt.Sprintf("%s = %d", strings.Join(parts, ", "), total)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// decideSearch searches the version's indexers for the title, decides every release and prints them ranked.
func decideSearch(ctx context.Context, db *sql.DB, out io.Writer, pol *policy.Policy, apiKeys map[string]string, e *decide.Engine, t decide.Title, v decide.Version,
	existing *decide.ExistingFile, ss *decide.SeriesSearch, limit int, asJSON bool,
	dial func(context.Context, string, string) (net.Conn, error)) error {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	gw, err := gateway.New(ctx, pol, gateway.Options{Logger: log, Version: buildinfo.Version, DialContext: dial})
	if err != nil {
		return err
	}
	svc, err := indexer.New(pol, gw, indexer.Options{Logger: log, APIKeys: apiKeys})
	if err != nil {
		return err
	}
	svc.SetMetadata(storedIndexerLookups(db))
	for _, s := range gw.CheckEgress(ctx) {
		if !s.OK && pol.Egress.RequiresVPN(policy.EgressIndexers) {
			return fmt.Errorf("%w (egress %s: %s)", errNotOnVPN, s.Path, s.Error)
		}
	}
	q := indexer.Query{Type: indexer.QueryMovie, Title: t.Name, Year: t.Year, IMDb: t.IMDb, TMDb: t.TMDb, Anime: t.Anime}
	if t.Kind == policy.KindSeries {
		q = indexer.Query{Type: indexer.QuerySeries, Title: t.Name, IMDb: t.IMDb, TVDB: t.TVDB, Season: ss.Season,
			Episode: ss.Episode, Anime: t.SeriesType == "anime" || t.Anime}
		for _, m := range t.SceneMappings {
			q.Aliases = append(q.Aliases, m.Title)
		}
	}
	var rels []indexer.Release
	var fresh []indexer.SeaDexSnapshot
	for _, k := range v.Policy.Indexers {
		sr := svc.Searcher(k)
		if sr == nil || !sr.Supports(q) {
			continue
		}
		res, err := svc.Search(ctx, k, q)
		if err != nil {
			if errors.Is(err, gateway.ErrVPNRequired) {
				return fmt.Errorf("%w (%v)", errNotOnVPN, err)
			}
			fmt.Fprintf(out, "# %s: %v\n", k, err)
			continue
		}
		rels = append(rels, res.Releases...)
		fresh = append(fresh, res.SeaDex...)
	}
	// This search's answers are not stored (no evidence store here); lay them over the stored standing so the
	// decisions see what was just fetched rather than the older rows.
	ctx = decide.WithSeaDexOverlay(ctx, db, fresh)
	// A torrent SeaDex lists is SeaDex's pick whichever indexer found it.
	indexer.MarkSeaDex(rels)
	var ds []*decide.Decision
	for _, rel := range rels {
		var d *decide.Decision
		var err error
		if t.Kind == policy.KindSeries {
			d, err = e.DecideSearch(ctx, decide.Release{Release: rel}, t, v, ss)
		} else {
			d, err = e.Decide(ctx, decide.Release{Release: rel}, t, v, existing)
		}
		if err != nil {
			return err
		}
		ds = append(ds, d)
	}
	ranked := decide.Rank(ds)
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	if asJSON {
		enc := json.NewEncoder(out)
		for _, d := range ranked {
			if err := enc.Encode(d); err != nil {
				return err
			}
		}
		return nil
	}
	fmt.Fprintf(out, "# %s (%d), version %s: %d release(s)\n", t.Name, t.Year, v.Key, len(ds))
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tOK\tQUALITY\tSCORE\tINDEXER\tTITLE\tWHY")
	for i, d := range ranked {
		ok, why := "yes", ""
		if !d.Accepted {
			ok, why = "no", d.Rejections[0].String()
		} else if d.Existing != nil {
			why = d.UpgradeReason
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\t%s\t%s\n", i+1, ok, d.Quality, d.Score, d.Indexer, d.Release, why)
	}
	return tw.Flush()
}

// seriesSearch is the search criteria for -season and -episode: one episode, or the season's episodes (anime
// kinds for an anime series).
func seriesSearch(t *decide.Title, season, episode int) *decide.SeriesSearch {
	anime := t.SeriesType == "anime"
	ss := &decide.SeriesSearch{Season: season, Episode: episode, Kind: profile.SearchSeason}
	switch {
	case episode > 0 && anime:
		ss.Kind = profile.SearchAnimeEpisode
	case episode > 0:
		ss.Kind = profile.SearchSingleEpisode
	case anime:
		ss.Kind = profile.SearchAnimeSeason
	}
	for _, e := range t.Episodes {
		if e.Season == season && (episode == 0 || e.Number == episode) {
			ss.Episodes = append(ss.Episodes, e.ID)
		}
	}
	return ss
}

func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	units := []struct {
		suffix string
		mult   float64
	}{{"TIB", 1 << 40}, {"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10}, {"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6},
		{"KB", 1e3}, {"B", 1}}
	up := strings.ToUpper(s)
	for _, u := range units {
		if strings.HasSuffix(up, u.suffix) {
			f, err := strconv.ParseFloat(strings.TrimSpace(s[:len(s)-len(u.suffix)]), 64)
			if err != nil || f < 0 {
				return 0, fmt.Errorf("bad size %q", s)
			}
			return int64(f * u.mult), nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("bad size %q (bytes, or 25GB, 700MiB)", s)
	}
	return n, nil
}

// applyFlags reads -flags: the arr's flag names (or a number), else torznab tags and volume factors.
func applyFlags(r *decide.Release, s string) error {
	if v, ok := format.Radarr.IndexerFlagsValue(s); ok {
		r.IndexerFlags = &v
		return nil
	}
	for _, t := range strings.Split(s, ",") {
		switch t = strings.ToLower(strings.TrimSpace(t)); t {
		case "internal", "scene":
			r.Flags = append(r.Flags, t)
		case "freeleech", "halfleech", "freeleech25", "freeleech75", "doubleupload":
			if r.Extras == nil {
				r.Extras = map[string]string{}
			}
			switch t {
			case "freeleech":
				r.Extras["downloadvolumefactor"] = "0"
			case "halfleech":
				r.Extras["downloadvolumefactor"] = "0.5"
			case "freeleech25":
				r.Extras["downloadvolumefactor"] = "0.75"
			case "freeleech75":
				r.Extras["downloadvolumefactor"] = "0.25"
			case "doubleupload":
				r.Extras["uploadvolumefactor"] = "2"
			}
		case "":
		default:
			return fmt.Errorf("unknown indexer flag %q", t)
		}
	}
	return nil
}

func runDecideShadow(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("decide shadow", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), decideUsage); fs.PrintDefaults() }
	var df decideFlags
	df.register(fs)
	instance := fs.String("instance", "", "arr instance whose grabs to replay")
	version := libraryFlag(fs, "library (with a from_arr profile of that instance) to decide them for")
	corpus := fs.String("corpus", "", "read the grabs from this corpus instead of the database (no existing files)")
	aliases := fs.String("aliases", "", "corpus directory to take alternative titles from (database mode)")
	seriesAliases := fs.String("series-aliases", "", "series: a saved GET series answer (JSON) to take scene mappings from")
	dump := fs.String("dump", "", "write every disagreement as a TSV line to this file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *instance == "" || *version == "" {
		fs.Usage()
		return errors.New("-instance and -library are required")
	}
	ctx := context.Background()
	_, e, err := df.engine(*version, os.Stderr)
	if err != nil {
		return err
	}
	var grabs []shadow.Grab
	series := false
	if v := e.Policy.Versions[*version]; v != nil && v.Kind == policy.KindSeries {
		series = true
	}
	var sm map[int][]decide.SceneMapping
	if *seriesAliases != "" {
		if sm, err = shadow.SeriesAliases(*seriesAliases); err != nil {
			return err
		}
	}
	if *corpus != "" {
		if series {
			grabs, err = shadow.LoadCorpusSeries(*corpus, *instance, sm)
		} else {
			grabs, err = shadow.LoadCorpus(*corpus, *instance)
		}
		if err != nil {
			return err
		}
	} else if series {
		st, err := store.Open(ctx, df.db, store.Options{})
		if err != nil {
			return err
		}
		defer st.Close()
		if grabs, err = shadow.LoadSeriesDB(ctx, st.Reader(), *instance, sm); err != nil {
			return err
		}
	} else {
		st, err := store.Open(ctx, df.db, store.Options{})
		if err != nil {
			return err
		}
		defer st.Close()
		var al map[int][]string
		if *aliases != "" {
			if al, err = shadow.CorpusAliases(*aliases, *instance); err != nil {
				return err
			}
		}
		if grabs, err = shadow.LoadDB(ctx, st.Reader(), *instance, al); err != nil {
			return err
		}
		lib, err := shadow.LoadLibrary(ctx, st.Reader())
		if err != nil {
			return err
		}
		e.SetLibrary(lib)
	}
	rep, results := shadow.Run(ctx, e, *version, grabs)
	fmt.Fprint(out, rep.Format())
	if *dump != "" {
		f, err := os.Create(*dump)
		if err != nil {
			return err
		}
		defer f.Close()
		for _, r := range results {
			if r.Cause == "" {
				continue
			}
			ours := ""
			if r.Decision != nil {
				ours = fmt.Sprintf("%s %d", r.Decision.Quality, r.Decision.Score)
				if len(r.Decision.Rejections) > 0 {
					ours += " " + r.Decision.Rejections[0].String()
				}
			}
			if len(r.Grab.Episodes) > 0 && r.Decision != nil {
				ours += fmt.Sprintf(" episodes %s (arr %d)", strings.Join(r.Decision.Episodes, ","), len(r.Grab.Episodes))
			}
			fmt.Fprintf(f, "%d\t%s\t%s\t%s\t%s\t%s\n", r.Grab.HistoryID, r.Grab.Date.Format(time.DateOnly),
				r.Grab.ReleaseSource, r.Cause, r.Grab.Release.Title, ours)
		}
	}
	return nil
}
