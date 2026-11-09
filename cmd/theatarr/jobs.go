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

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/store"
)

func init() {
	register(command{name: "jobs", summary: "list the job queue; retry or dismiss failed jobs",
		run: func(args []string) error { return runJobs(args, os.Stdout) }})
}

const jobsUsage = `usage: theatarr jobs list [flags]               the queue: running, queued, then the latest finished
       theatarr jobs retry [flags] ID...         queue a fresh run of failed jobs (same kind and payload, linked)
       theatarr jobs dismiss [flags] ID...       hide failed jobs from "needs attention"
       theatarr jobs dismiss -resolved [flags]   dismiss every failed job a later done job resolved

A failed job needs attention until a later job of the same kind finishes done with the same payload (or with the
empty payload, a run over everything), it is retried, or it is dismissed. Nothing here deletes a job. These work on
the database directly, so they also work while the service runs; its workers pick up a retry on their next poll.
The same actions are in the API (POST /api/theatarr/v1/jobs/{id}/retry, /dismiss, /jobs/dismiss-resolved).
`

func runJobs(args []string, out io.Writer) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprint(os.Stderr, jobsUsage)
		if helpArg(args) {
			return flag.ErrHelp
		}
		return errors.New("want a subcommand: list, retry or dismiss")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("jobs "+sub, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), jobsUsage+"\nflags:\n"); fs.PrintDefaults() }
	dbPath := fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	format := fs.String("format", "text", "text or json")
	var (
		state, kind          *string
		attention, dismissed *bool
		limit                *int
		resolved             *bool
	)
	switch sub {
	case "list":
		state = fs.String("state", "", "queued, running, done or failed")
		kind = fs.String("kind", "", "only this kind")
		attention = fs.Bool("attention", false, "only failed jobs that still need attention")
		dismissed = fs.Bool("dismissed", true, "include dismissed jobs (-dismissed=false leaves them out)")
		limit = fs.Int("limit", 50, "jobs to list (at most 500)")
	case "retry":
	case "dismiss":
		resolved = fs.Bool("resolved", false, "dismiss every resolved failed job instead of the given ids")
	default:
		fmt.Fprint(os.Stderr, jobsUsage)
		return fmt.Errorf("unknown subcommand %q", sub)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *format != "text" && *format != "json" {
		return errors.New("-format must be text or json")
	}
	var ids []int64
	for _, a := range fs.Args() {
		id, err := strconv.ParseInt(strings.TrimPrefix(a, "#"), 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("not a job id: %q", a)
		}
		ids = append(ids, id)
	}
	switch {
	case sub == "list" && len(ids) > 0:
		return errors.New("list takes no ids")
	case sub == "retry" && len(ids) == 0:
		return errors.New("retry: give one or more job ids")
	case sub == "dismiss" && *resolved && len(ids) > 0:
		return errors.New("dismiss: give ids or -resolved, not both")
	case sub == "dismiss" && !*resolved && len(ids) == 0:
		return errors.New("dismiss: give one or more job ids, or -resolved")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	st, err := store.Open(ctx, *dbPath, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	q := jobs.NewQueue(st)
	by := "cli"
	if u := os.Getenv("USER"); u != "" {
		by = "cli:" + u
	}

	switch sub {
	case "list":
		items, err := q.List(ctx, jobs.ListFilter{State: *state, Kind: *kind, Attention: *attention,
			HideDismissed: !*dismissed, Limit: *limit})
		if err != nil {
			return err
		}
		return writeJobs(out, *format, items)
	case "retry":
		var res []jobResult
		for _, id := range ids {
			newID, created, err := q.Retry(ctx, id)
			if err != nil {
				return fmt.Errorf("job %d: %w", id, err)
			}
			res = append(res, jobResult{Job: id, Retry: newID, Created: created})
		}
		return writeResults(out, *format, res)
	default:
		if *resolved {
			n, err := q.DismissResolved(ctx, by)
			if err != nil {
				return err
			}
			if *format == "json" {
				return json.NewEncoder(out).Encode(map[string]int64{"dismissed": n})
			}
			_, err = fmt.Fprintf(out, "dismissed %d resolved job(s)\n", n)
			return err
		}
		var res []jobResult
		for _, id := range ids {
			s, err := q.Dismiss(ctx, id, by)
			if err != nil {
				return fmt.Errorf("job %d: %w", id, err)
			}
			res = append(res, jobResult{Job: id, DismissedAt: s.DismissedAt, DismissedBy: s.DismissedBy})
		}
		return writeResults(out, *format, res)
	}
}

type jobResult struct {
	Job         int64      `json:"job"`
	Retry       int64      `json:"retry,omitempty"`
	Created     bool       `json:"created,omitempty"`
	DismissedAt *time.Time `json:"dismissed_at,omitempty"`
	DismissedBy string     `json:"dismissed_by,omitempty"`
}

func writeResults(out io.Writer, format string, res []jobResult) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(res)
	}
	for _, r := range res {
		switch {
		case r.Retry != 0 && r.Created:
			fmt.Fprintf(out, "job %d: retry queued as #%d\n", r.Job, r.Retry)
		case r.Retry != 0:
			fmt.Fprintf(out, "job %d: already being retried as #%d\n", r.Job, r.Retry)
		default:
			fmt.Fprintf(out, "job %d: dismissed by %s at %s\n", r.Job, r.DismissedBy, r.DismissedAt.Format(time.RFC3339))
		}
	}
	return nil
}

// jobOutcome says what became of a failed job, for the text listing.
func jobOutcome(s jobs.Status) string {
	var parts []string
	if s.RetryOf != 0 {
		parts = append(parts, fmt.Sprintf("retry of #%d", s.RetryOf))
	}
	if s.State == jobs.StateFailed {
		switch {
		case s.Attention:
			parts = append(parts, "needs attention")
		case s.ResolvedBy != 0:
			parts = append(parts, fmt.Sprintf("resolved by #%d", s.ResolvedBy))
		}
		if s.RetriedAs != 0 {
			parts = append(parts, fmt.Sprintf("retried as #%d", s.RetriedAs))
		}
		if s.DismissedAt != nil {
			parts = append(parts, "dismissed")
		}
	}
	return strings.Join(parts, ", ")
}

func writeJobs(out io.Writer, format string, items []jobs.Status) error {
	if format == "json" {
		enc := json.NewEncoder(out)
		for _, s := range items {
			if err := enc.Encode(s); err != nil {
				return err
			}
		}
		return nil
	}
	tw := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tKIND\tSTATE\tTRIES\tWHEN\tNOTE\tERROR")
	for _, s := range items {
		when := s.RunAt
		if s.FinishedAt != nil {
			when = *s.FinishedAt
		}
		errText := strings.ReplaceAll(s.LastError, "\n", " ")
		if len(errText) > 80 {
			errText = errText[:80] + "…"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%d/%d\t%s\t%s\t%s\n", s.ID, s.Kind, s.State, s.Attempts, s.MaxAttempts,
			when.Local().Format("2006-01-02 15:04"), jobOutcome(s), errText)
	}
	return tw.Flush()
}
