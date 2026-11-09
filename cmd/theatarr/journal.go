package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

func init() {
	register(command{name: "journal", summary: "read the grab journal: decisions, what followed, suspect flags (read only)",
		run: func(args []string) error { return runJournal(args, os.Stdout) }})
}

const journalUsage = `usage: theatarr journal [flags]          list entries, newest first (JSONL by default)
       theatarr journal -id ID [flags]    one entry

The grab journal (docs/journal.md): one entry per decision Theatarr or an arr made (grab, upgrade, shadow, plan, search,
research) and per thing that followed (failure, stall, blocklist, import, link, verdict), with the reasoning,
provider attempts, cache evidence, import results and suspect flags. It reads the database only. Verdicts are given
through the API (POST /api/theatarr/v1/journal/{id}/verdict).

flags:
`

func runJournal(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("journal", flag.ContinueOnError)
	settingsSourceFlag(fs)
	fs.Usage = func() { fmt.Fprint(fs.Output(), journalUsage); fs.PrintDefaults() }
	policyDir := policyDirDefault()
	dbPath := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB); read only")
	id := fs.String("id", "", "print one entry")
	since := fs.String("since", "", "entries at or after: RFC 3339, YYYY-MM-DD, or a duration back from now (72h)")
	title := fs.String("title", "", "a title id, or part of a title's name")
	version := fs.String("version", "", "a version key")
	kind := fs.String("kind", "", "kinds, comma-separated: "+strings.Join(journal.Kinds, ", "))
	suspect := fs.Bool("suspect", false, "only entries with an unacknowledged warn or error flag")
	severity := fs.String("severity", "", "only entries with an unacknowledged flag of at least: info, warn, error")
	cursor := fs.String("cursor", "", "continue after a previous page (its next_cursor line)")
	limit := fs.Int("limit", 50, "entries per page (at most 500)")
	all := fs.Bool("all", false, "follow the cursor to the end")
	scan := fs.Int("scan", 0, "entries one page may look at for -suspect/-severity (default: no limit)")
	format := fs.String("format", "jsonl", "jsonl (one entry per line, then {\"next_cursor\"} when more), json, or text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *format != "jsonl" && *format != "json" && *format != "text" {
		return errors.New("-format must be jsonl, json or text")
	}
	if *scan == 0 {
		*scan = 1 << 30 // offline: look as far as it takes
	}
	f := journal.Filter{Title: *title, Version: *version, Suspect: *suspect, Severity: *severity, Cursor: *cursor,
		Limit: *limit, ScanLimit: *scan}
	if *kind != "" {
		f.Kinds = strings.Split(*kind, ",")
	}
	if *since != "" {
		t, err := parseSince(*since, time.Now())
		if err != nil {
			return err
		}
		f.Since = t
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var pol *policy.Policy
	if policyDir != "" {
		p, err := loadCommandPolicy(fs, policyDir)
		if err != nil {
			return err
		}
		pol = p
	}
	st, err := store.Open(ctx, *dbPath, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	j := journal.New(journal.Config{Store: st, Policy: pol})
	if *id != "" {
		e, err := j.Get(ctx, *id)
		if err != nil {
			return err
		}
		return writeEntries(out, *format, []*journal.Entry{e}, "")
	}
	for {
		page, err := j.List(ctx, f)
		if err != nil {
			return err
		}
		next := page.NextCursor
		if *all {
			next = ""
		}
		if err := writeEntries(out, *format, page.Items, next); err != nil {
			return err
		}
		if !*all || page.NextCursor == "" {
			return nil
		}
		f.Cursor = page.NextCursor
	}
}

func writeEntries(out io.Writer, format string, es []*journal.Entry, next string) error {
	switch format {
	case "text":
		for _, e := range es {
			fmt.Fprintln(out, e.Text())
		}
		if next != "" {
			fmt.Fprintf(out, "more: -cursor %s\n", next)
		}
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"items": es, "next_cursor": next})
	default:
		enc := json.NewEncoder(out)
		for _, e := range es {
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		if next != "" {
			return enc.Encode(map[string]string{"next_cursor": next})
		}
	}
	return nil
}

// parseSince reads RFC 3339, a date, or a duration back from now.
func parseSince(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(s, "%dd", &n); err == nil && n > 0 {
			return now.AddDate(0, 0, -n), nil
		}
	}
	return time.Time{}, fmt.Errorf("-since %q: want RFC 3339, YYYY-MM-DD, or a duration such as 72h or 30d", s)
}
