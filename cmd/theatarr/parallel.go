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
	"time"

	"github.com/theatarr/theatarr/internal/shadow"
	"github.com/theatarr/theatarr/internal/store"
)

func init() {
	register(command{name: "parallel", summary: "parallel: read-only follower tools", run: func(args []string) error { return runParallel(args, os.Stdout) }})
}

func runParallel(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "compare" {
		const usage = "usage: theatarr parallel compare [-version K] [-since 7d] [-json]"
		if helpArg(args) {
			fmt.Fprintln(os.Stderr, usage)
			return flag.ErrHelp
		}
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("parallel compare", flag.ContinueOnError)
	settingsSourceFlag(fs)
	fs.SetOutput(io.Discard)
	version := fs.String("version", "", "follower version (default: every follower)")
	since := fs.String("since", "7d", "comparison window, Go duration (d is supported)")
	asJSON := fs.Bool("json", false, "print JSON")
	policyDir := policyDirDefault()
	dbPath := fs.String("db", defaultDBPath(), "database file, opened read only")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	d, err := parseCompareWindow(*since)
	if err != nil || d <= 0 {
		return fmt.Errorf("invalid -since %q", *since)
	}
	p, err := loadCommandPolicy(fs, policyDir)
	if err != nil {
		return err
	}
	st, err := store.Open(context.Background(), *dbPath, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	var reports []*shadow.FollowComparison
	for k, v := range p.Versions {
		if v.Follows == "" || (*version != "" && *version != k) {
			continue
		}
		var instances []string
		for inst, a := range p.Import.Arr {
			if a.Version == v.Follows && p.ArrFed(inst) {
				instances = append(instances, inst)
			}
		}
		for _, inst := range instances {
			r, err := shadow.CompareFollow(context.Background(), st.Reader(), inst, v.Follows, k, time.Now().Add(-d))
			if err != nil {
				return err
			}
			reports = append(reports, r)
		}
	}
	if *version != "" && len(reports) == 0 {
		return fmt.Errorf("no arr source feeds follower %q", *version)
	}
	if *asJSON {
		b, err := json.MarshalIndent(reports, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(b))
		return nil
	}
	for _, r := range reports {
		fmt.Fprintf(out, "%s <- %s: ", r.Version, r.Instance)
		first := true
		for k, n := range r.Counts {
			if !first {
				fmt.Fprint(out, ", ")
			}
			fmt.Fprintf(out, "%s=%d", k, n)
			first = false
		}
		fmt.Fprintln(out)
		for _, p := range r.Pairs {
			fmt.Fprintf(out, "  %s: %s arr=%s (%s) theatarr=%s (%s) first=%s by=%s\n", p.Title, p.Outcome, p.ArrHash, p.ArrQuality, p.TheatarrHash, p.TheatarrQuality, p.First, p.By)
		}
	}
	return nil
}

// parseCompareWindow accepts Go durations and a number of 24-hour days.
func parseCompareWindow(value string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(value, "d"); ok {
		if _, err := strconv.ParseFloat(days, 64); err != nil {
			return 0, fmt.Errorf("invalid days %q", value)
		}
		hours, err := time.ParseDuration(days + "h")
		if err != nil || hours > time.Duration(1<<63-1)/24 || hours < 0 {
			return 0, fmt.Errorf("invalid days %q", value)
		}
		return hours * 24, nil
	}
	return time.ParseDuration(value)
}
