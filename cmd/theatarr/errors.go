package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/web"
)

func init() {
	register(command{name: "errors", summary: "list the error inbox; show, resolve or ignore an entry",
		run: func(args []string) error { return runErrors(args, os.Stdout) }})
}

const errorsUsage = `usage: theatarr errors list [flags]                 grouped errors, newest sighting first (default: open ones)
       theatarr errors show [flags] ID              one entry with its context and stack
       theatarr errors resolve [flags] ID...        mark resolved (it comes back as new if it happens again)
       theatarr errors seen [flags] ID...           mark seen: still open, no longer new
       theatarr errors ignore [flags] ID...         mark ignored: counted, never reopened
       theatarr errors new [flags] ID...            reopen

The error inbox (docs/design/errors.md) holds the unexpected failures of theatarr serve: Error logs, panics, 5xx answers,
failed jobs and failing health checks, one row per fingerprint with a count. These work on the database directly,
so they also work while the service runs (docker exec). The same actions are in the API (/api/theatarr/v1/errors)
and the System › Errors page.
`

func runErrors(args []string, out io.Writer) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprint(os.Stderr, errorsUsage)
		if helpArg(args) {
			return flag.ErrHelp
		}
		return errors.New("want a subcommand: list, show, resolve, seen, ignore or new")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("errors "+sub, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), errorsUsage+"\nflags:\n"); fs.PrintDefaults() }
	dbPath := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	asJSON := fs.Bool("json", false, "JSON output")
	var (
		status, source, since, note *string
		limit                       *int
	)
	switch sub {
	case "list":
		status = fs.String("status", "open", "new, seen, resolved, ignored, open (new and seen) or all")
		source = fs.String("source", "", "only this source: "+strings.Join(errinbox.Sources, ", "))
		since = fs.String("since", "", "only rows seen in this window (24h) or after this RFC 3339 time")
		limit = fs.Int("limit", 100, "rows to list (at most 500)")
	case "show":
	case errinbox.StatusResolved, "resolve", errinbox.StatusSeen, errinbox.StatusIgnored, "ignore", errinbox.StatusNew:
		note = fs.String("note", "", "a note to keep with the entry (a fix branch, say)")
	default:
		fmt.Fprint(os.Stderr, errorsUsage)
		return fmt.Errorf("unknown subcommand %q", sub)
	}
	// Flags may come after the ids (theatarr errors show 12 -json).
	var ids []int64
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		a := fs.Arg(0)
		id, err := strconv.ParseInt(strings.TrimPrefix(a, "#"), 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("not an error id: %q", a)
		}
		ids = append(ids, id)
		args = fs.Args()[1:]
	}
	switch {
	case sub == "list" && len(ids) > 0:
		return errors.New("list takes no ids")
	case sub == "show" && len(ids) != 1:
		return errors.New("show: give one id")
	case sub != "list" && len(ids) == 0:
		return fmt.Errorf("%s: give one or more ids", sub)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	st, err := store.Open(ctx, *dbPath, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()

	switch sub {
	case "list":
		f := errinbox.Filter{Status: *status, Source: *source, Limit: *limit}
		if f.Status == "all" {
			f.Status = ""
		}
		if !errinbox.ValidStatus(f.Status, true) {
			return errors.New("-status must be new, seen, resolved, ignored, open or all")
		}
		if !errinbox.ValidSource(f.Source) {
			return errors.New("-source must be one of " + strings.Join(errinbox.Sources, ", "))
		}
		if f.Since, err = web.ParseSince(*since, time.Now()); err != nil {
			return err
		}
		p, err := errinbox.List(ctx, st, f)
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(out).Encode(p)
		}
		return writeErrorList(out, p)
	case "show":
		r, err := errinbox.Get(ctx, st, ids[0])
		if err != nil {
			return fmt.Errorf("error %d: %w", ids[0], err)
		}
		if *asJSON {
			return json.NewEncoder(out).Encode(r)
		}
		return writeErrorRow(out, r)
	default:
		to := map[string]string{"resolve": errinbox.StatusResolved, "ignore": errinbox.StatusIgnored}[sub]
		if to == "" {
			to = sub
		}
		var notePtr *string
		if *note != "" {
			notePtr = note
		}
		var rows []errinbox.Row
		for _, id := range ids {
			r, err := errinbox.Update(ctx, st, id, to, notePtr, time.Now())
			if err != nil {
				return fmt.Errorf("error %d: %w", id, err)
			}
			rows = append(rows, r)
		}
		if *asJSON {
			return json.NewEncoder(out).Encode(rows)
		}
		for _, r := range rows {
			fmt.Fprintf(out, "error %d: %s\n", r.ID, r.Status)
		}
		return nil
	}
}

func writeErrorList(out io.Writer, p errinbox.Page) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tSOURCE\tCOMPONENT\tCOUNT\tLAST SEEN\tMESSAGE")
	for _, r := range p.Items {
		msg := r.Sample
		if len(msg) > 100 {
			msg = msg[:100] + "…"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%d\t%s\t%s\n", r.ID, r.Status, r.Source, r.Component, r.Count,
			r.LastSeen.Local().Format("2006-01-02 15:04"), strings.ReplaceAll(msg, "\n", " "))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "%d shown of %d; new %d, seen %d, resolved %d, ignored %d; %d dropped\n", len(p.Items),
		p.Total, p.Counts[errinbox.StatusNew], p.Counts[errinbox.StatusSeen], p.Counts[errinbox.StatusResolved],
		p.Counts[errinbox.StatusIgnored], p.Drops)
	return err
}

func writeErrorRow(out io.Writer, r errinbox.Row) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "id\t%d\nstatus\t%s\nsource\t%s\ncomponent\t%s\nlevel\t%s\ncount\t%d\n", r.ID, r.Status, r.Source,
		r.Component, r.Level, r.Count)
	fmt.Fprintf(tw, "first seen\t%s (%s)\nlast seen\t%s (%s)\n", r.FirstSeen.Local().Format(time.RFC3339),
		r.FirstVersion, r.LastSeen.Local().Format(time.RFC3339), r.LastVersion)
	if r.ResolvedAt != nil {
		fmt.Fprintf(tw, "resolved\t%s\n", r.ResolvedAt.Local().Format(time.RFC3339))
	}
	if r.Regressions > 0 {
		fmt.Fprintf(tw, "regressions\t%d\n", r.Regressions)
	}
	if r.Note != "" {
		fmt.Fprintf(tw, "note\t%s\n", r.Note)
	}
	fmt.Fprintf(tw, "fingerprint\t%s\nmessage\t%s\n", r.Fingerprint, r.Message)
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nsample:\n  %s\n", r.Sample)
	var ctx map[string]any
	if json.Unmarshal(r.Context, &ctx) == nil && len(ctx) > 0 {
		b, _ := json.MarshalIndent(ctx, "  ", "  ")
		fmt.Fprintf(out, "\ncontext:\n  %s\n", b)
	}
	if r.Stack != "" {
		fmt.Fprintf(out, "\nstack:\n%s\n", strings.TrimRight(r.Stack, "\n"))
	}
	return nil
}
