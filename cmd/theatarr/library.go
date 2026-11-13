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
	"text/tabwriter"
	"time"

	"github.com/theatarr/theatarr/internal/buildinfo"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

func init() {
	register(command{
		name:    "library",
		summary: "library: dry-run an import, verify a library's root, relink dangling links, preview renames",
		run:     func(args []string) error { return runLibrary(args, os.Stdout) },
	})
}

const libraryUsage = `usage:
  theatarr library dry-run -download ID [flags]   plan one download's import: files, probes, guard, link paths. Writes nothing
  theatarr library verify [-library KEY] [flags]  read-only scan of a library's roots: counts, dangling links, names that
                                                 differ from ours. Writes nothing
  theatarr library relink [-library KEY] [-dry-run] [flags]
                                                 repoint dangling links by info hash; through the gate (Pause off,
                                                 a Live library, the library's roots), otherwise it reports what it would do
  theatarr library rename-preview -library KEY [flags]
                                                 read-only: every link of the library whose name would change under its
                                                 naming, link_id<TAB>existing<TAB>new. Opens the database read-only
  theatarr library dangling [-scan] [-tsv] [flags]
                                                 the latest dangling-link report (links whose target is gone) from the
                                                 database; -scan walks the roots now first (read-only)
`

type libraryFlags struct {
	decideFlags
	asJSON bool
}

func runLibrary(args []string, out io.Writer) error {
	if len(args) == 0 || helpArg(args) {
		fmt.Fprint(os.Stderr, libraryUsage)
		if helpArg(args) {
			return flag.ErrHelp
		}
		return errors.New("give dry-run, verify, relink, rename-preview or dangling")
	}
	fs := flag.NewFlagSet("library "+args[0], flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), libraryUsage+"\nflags:\n"); fs.PrintDefaults() }
	var f libraryFlags
	f.register(fs)
	fs.BoolVar(&f.asJSON, "json", false, "print JSON")
	download := fs.Int64("download", 0, "dry-run: the download id")
	version := libraryFlag(fs, "verify, relink, rename-preview: one library (default every library under library.versions)")
	dryRun := fs.Bool("dry-run", false, "relink: report only")
	scan := fs.Bool("scan", false, "dangling: walk the roots now (read-only) instead of reading the last report")
	tsv := fs.Bool("tsv", false, "dangling: print path<TAB>target lines")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ctx := context.Background()
	if f.envFile != "" {
		if err := loadEnvFile(f.envFile); err != nil {
			return err
		}
	}
	// rename-preview changes nothing: every open of the database is read-only (no WAL switch, no chmod, no migration),
	// and any write fails instead of landing.
	readOnly := args[0] == "rename-preview"
	f.readOnly = readOnly
	pol, _, err := loadSettingsSnapshotOpts(ctx, f.settingsSource, f.policyDir, f.db, readOnly)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, f.db, store.Options{ReadOnly: readOnly})
	if err != nil {
		return err
	}
	defer st.Close()
	if readOnly {
		cur, target, err := st.SchemaVersion(ctx)
		if err != nil {
			return err
		}
		if cur != target {
			return fmt.Errorf("the database is at schema %d and this binary expects %d: run it with the binary theatarr "+
				"serve runs (docker compose exec theatarr theatarr library %s)", cur, target, args[0])
		}
	} else if _, err := st.Migrate(ctx); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// Writes (relink) queue plex.scan jobs for theatarr serve to run, as its own library jobs do.
	opts := library.Options{Store: st, Policy: pol, Logger: log, Queue: jobs.NewQueue(st)}
	if set := pol.LibrarySettings(); set.ZurgAPI != "" || set.Decypharr != nil {
		// zurg's read-only API or decypharr's, through a gateway of this command's own (budgets are counted in the
		// database). The command has no provider clients: on decypharr's mount it imports and relinks by name.
		gops := gateway.Options{Store: st, Logger: log, Version: buildinfo.Version}
		if readOnly {
			gops.Store = nil // budgets in memory only
		}
		gw, err := gateway.New(ctx, pol, gops)
		if err != nil {
			return err
		}
		opts.Zurg = newZurgAPI(set, gw, log)
		opts.Decypharr = newDecypharr(pol, set, gw, log)
	}
	if args[0] == "dry-run" {
		var key string
		_ = st.Reader().QueryRowContext(ctx, `SELECT version_key FROM download WHERE id = ?`, *download).Scan(&key)
		if _, eng, err := f.engine(key, io.Discard); err == nil {
			opts.Formats = decideRenameFormats(st, pol, eng)
			opts.Matched = matchedRenameFormats(pol, func() *decide.Engine { return eng })
		} else {
			fmt.Fprintf(os.Stderr, "warning: no decision engine (%v); {Custom Formats} stays empty\n", err)
		}
	}
	if args[0] == "rename-preview" && *version == "" {
		return errors.New("-library is required")
	}
	if args[0] == "rename-preview" {
		// As serve builds it, so the names match what serve proposes.
		var built strings.Builder
		_, eng, err := f.engine(*version, &built)
		if err == nil {
			err = renameProfiles(pol, eng, *version, built.String())
		}
		if err != nil {
			// Without the engine the names lack {Custom Formats}: no preview rather than a wrong one.
			return fmt.Errorf("names cannot be rebuilt: %w", err)
		}
		opts.Engine = eng
		opts.Formats = decideRenameFormats(st, pol, eng)
		opts.Matched = matchedRenameFormats(pol, func() *decide.Engine { return eng })
	}
	lib := library.New(opts)
	fmt.Fprintln(os.Stderr, lib.Gate().Summary())
	switch args[0] {
	case "dry-run":
		if *download == 0 {
			return errors.New("-download is required")
		}
		plan, err := lib.Import(ctx, *download, true)
		if err != nil {
			return err
		}
		return printPlan(out, plan, f.asJSON)
	case "verify":
		scans, err := lib.Verify(ctx, *version)
		if err != nil {
			return err
		}
		return printScans(out, scans, f.asJSON)
	case "relink":
		scans, err := lib.Relink(ctx, *version, *dryRun)
		if err != nil {
			return err
		}
		return printScans(out, scans, f.asJSON)
	case "dangling":
		if *scan {
			if _, err := lib.DanglingReport(ctx); err != nil {
				return err
			}
		}
		rows, sum, err := library.Dangling(ctx, st.Reader())
		if err != nil {
			return err
		}
		return printDangling(out, rows, sum, *tsv, f.asJSON)
	case "rename-preview":
		p, err := lib.PreviewRenameLibrary(ctx, *version)
		if err != nil {
			return err
		}
		if f.asJSON {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			if err := enc.Encode(p); err != nil {
				return err
			}
		} else {
			for _, r := range p.Files {
				fmt.Fprintf(out, "%d\t%s\t%s\n", r.LinkID, r.ExistingPath, r.NewPath)
			}
		}
		fmt.Fprintf(os.Stderr, "%d links would be renamed, %d skipped\n", len(p.Files), p.Skipped)
		return nil
	}
	fmt.Fprint(os.Stderr, libraryUsage)
	return fmt.Errorf("unknown library command %q", args[0])
}

// decideRenameFormats gives the import the formats the arr would name a file with: the release decided against the
// version's profile, keeping the matched formats that have includeCustomFormatWhenRenaming, in the profile's order.
func decideRenameFormats(st *store.Store, pol *policy.Policy, eng *decide.Engine) library.RenameFormats {
	return func(ctx context.Context, version string, titleID int64, release string) []string {
		vp := pol.Versions[version]
		if vp == nil || eng == nil {
			return nil
		}
		t, err := decide.LoadTitle(ctx, st.Reader(), titleID)
		if err != nil {
			return nil
		}
		if err := decide.LoadSeriesVersion(ctx, st.Reader(), &t, 0); err != nil {
			return nil
		}
		v, err := decide.LoadVersion(ctx, st.Reader(), pol, titleID, version)
		if err != nil {
			return nil
		}
		d, err := eng.Decide(ctx, decide.Release{Release: indexer.Release{Title: release}}, t, v, nil)
		if err != nil || d == nil {
			return nil
		}
		var names []string
		for _, f := range d.Formats {
			names = append(names, f.Name)
		}
		return renameNames(pol, eng, version, names)
	}
}

// renameProfiles checks that eng built the profile the version's names need. built is what the engine reported on the
// profiles that failed.
func renameProfiles(pol *policy.Policy, eng *decide.Engine, version, built string) error {
	for _, k := range []string{version} {
		vp := pol.Versions[k]
		if vp == nil {
			return fmt.Errorf("the policy has no library %q", k)
		}
		fl := format.Radarr
		if vp.Kind == policy.KindSeries {
			fl = format.Sonarr
		}
		if eng.Profile(vp.Profile, fl) == nil {
			return fmt.Errorf("profile %q of library %s is not built\n%s", vp.Profile, k, strings.TrimSpace(built))
		}
	}
	return nil
}

// matchedRenameFormats gives the import the {Custom Formats} names from the formats the router's decision matched
// (stored on the download), without deciding again.
func matchedRenameFormats(pol *policy.Policy, eng func() *decide.Engine) library.MatchedFormats {
	return func(_ context.Context, version string, matched []string) []string {
		return renameNames(pol, eng(), version, matched)
	}
}

// renameNames keeps the matched formats that have includeCustomFormatWhenRenaming, in the version's profile order.
func renameNames(pol *policy.Policy, eng *decide.Engine, version string, names []string) []string {
	vp := pol.Versions[version]
	if vp == nil || eng == nil {
		return nil
	}
	matched := map[string]bool{}
	for _, n := range names {
		matched[n] = true
	}
	fl := format.Radarr
	if vp.Kind == policy.KindSeries {
		fl = format.Sonarr
	}
	p := eng.Profile(vp.Profile, fl)
	if p == nil {
		return nil
	}
	var out []string
	for _, f := range p.Formats {
		if f.IncludeWhenRenaming && matched[f.Name] {
			out = append(out, f.Name)
		}
	}
	return out
}

func printPlan(out io.Writer, p *library.Plan, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	}
	fmt.Fprintf(out, "download %d  %s  version %s  naming %s\n", p.DownloadID, p.Release, p.Version, p.Naming)
	if p.Refused != "" {
		fmt.Fprintf(out, "gate: %s\n", p.Refused)
	}
	if p.Waiting {
		fmt.Fprintln(out, "not on the mount yet")
		return nil
	}
	fmt.Fprintf(out, "mount folder: %s\n", p.Folder)
	tw := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "file\tsize\truntime\tvideo\taudio\tnote")
	for _, f := range p.Files {
		rt, video, audio := "-", "-", "-"
		if f.Probe != nil {
			rt = (time.Duration(f.Probe.RunTimeSeconds) * time.Second).String()
			if v := f.Probe.Video; v != nil {
				video = fmt.Sprintf("%s %dx%d %dbit %s", v.Codec, v.Width, v.Height, v.BitDepth, v.Transfer)
			}
			if len(f.Probe.Audio) > 0 {
				a := f.Probe.Audio[0]
				audio = strings.TrimSpace(fmt.Sprintf("%s %s %s %dch", a.Codec, a.Tag, a.Profile, a.Channels))
			}
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n", f.Rel, f.Size, rt, video, audio, f.Reason)
	}
	tw.Flush()
	for _, l := range p.Links {
		state := "link"
		if l.Exists {
			state = "exists"
		}
		fmt.Fprintf(out, "%s %s\n    -> %s\n", state, l.Path, l.Target)
		for _, r := range l.Replaces {
			fmt.Fprintf(out, "    moves aside %s\n", r)
		}
	}
	for _, r := range p.Rejected {
		fmt.Fprintf(out, "rejected %s: %s\n", r.Rel, r.Reason)
	}
	for _, u := range p.Unmatched {
		fmt.Fprintf(out, "no episode of the download: %s\n", u)
	}
	return nil
}

func printScans(out io.Writer, scans []*library.LinkScan, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(scans)
	}
	for _, s := range scans {
		fmt.Fprintf(out, "%s  %s\n  dirs %d, symlinks %d (%d into the mount), regular files %d\n", s.Version, s.Root, s.Dirs,
			s.Links, s.IntoMnt, s.Files)
		if s.Mount != "" {
			fmt.Fprintf(out, "  mount: %s (dangling links not checked)\n", s.Mount)
		}
		if s.Refused != "" {
			fmt.Fprintf(out, "  gate: %s\n", s.Refused)
		}
		fmt.Fprintf(out, "  dangling: %d %v; relinked%s: %d\n", len(s.Dangling), s.Causes, map[bool]string{true: " (would be)"}[s.DryRun], len(s.Relinked))
		for _, d := range s.Dangling {
			fmt.Fprintf(out, "    %s  %s  -> %s %s\n", d.Cause, d.Path, d.Target, d.NewTarget)
		}
		for _, d := range s.Relinked {
			fmt.Fprintf(out, "    relink %s\n      %s\n   -> %s\n", d.Path, d.Target, d.NewTarget)
		}
		for _, d := range s.Revived {
			fmt.Fprintf(out, "    revived%s %s -> %s\n", map[bool]string{true: " (would be)"}[s.DryRun], d.Path, d.Target)
		}
		if n := s.Names; n != nil {
			fmt.Fprintf(out, "  names (%s): %d arr files, %d in the format's shape, %d equal to ours\n", n.Naming, n.Files, n.Shaped, n.Equal)
			if n.Error != "" {
				fmt.Fprintf(out, "    error: %s\n", n.Error)
			}
			for _, d := range n.Differs {
				fmt.Fprintf(out, "    %s\n      ours %s\n", d.Path, d.Ours)
			}
		}
	}
	return nil
}

func printDangling(out io.Writer, rows []library.DanglingRow, sum *library.DanglingSummary, tsv, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Report *library.DanglingSummary `json:"report"`
			Links  []library.DanglingRow    `json:"links"`
		}{sum, rows})
	}
	if tsv {
		for _, r := range rows {
			fmt.Fprintf(out, "%s\t%s\n", r.Path, r.Target)
		}
		return nil
	}
	if sum == nil {
		fmt.Fprintln(out, "no dangling-link report yet (theatarr serve writes one at start and every library.dangling.interval; or run with -scan)")
		return nil
	}
	fmt.Fprintf(out, "report of %s: %d dangling of %d links into the mount (took %v)\n", sum.At.UTC().Format(time.RFC3339),
		sum.Dangling, sum.Links, sum.Took.Round(time.Millisecond))
	for _, e := range sum.Errors {
		fmt.Fprintf(out, "  not walked: %s\n", e)
	}
	if z := sum.Zurg; z != nil {
		switch {
		case z.Error != "":
			fmt.Fprintf(out, "  zurg API not read: %s\n", z.Error)
		default:
			fmt.Fprintf(out, "  zurg: %d of them: %s (%d torrents in zurg have no video)\n", z.NoVideo,
				library.ReasonNoVideo, z.NoVideoTorrents)
		}
		if z.Repairing {
			fmt.Fprintln(out, "  zurg was repairing: the relink sweep gives dangling links grace")
		}
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SINCE\tVERSION\tCAUSE\tPATH\tTARGET")
	for _, r := range rows {
		v, c := r.Version, r.Cause
		if v == "" {
			v = "-"
		}
		if c == "" {
			c = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.FirstSeen.UTC().Format("2006-01-02 15:04"), v, c, r.Path, r.Target)
	}
	return tw.Flush()
}
