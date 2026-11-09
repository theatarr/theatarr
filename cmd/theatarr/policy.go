package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/policy"
)

func init() {
	register(command{
		name:    "policy",
		summary: "policy tools: validate <dir>",
		run:     runPolicy,
	})
}

func runPolicy(args []string) error {
	if len(args) == 0 || args[0] != "validate" {
		fmt.Fprint(os.Stderr, "usage: theatarr policy validate <dir>\n")
		if helpArg(args) {
			return flag.ErrHelp
		}
		return errors.New("want: validate <dir>")
	}
	fs := flag.NewFlagSet("policy validate", flag.ContinueOnError)
	quiet := fs.Bool("q", false, "print nothing on success")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: theatarr policy validate [-q] <dir>")
	}
	p, err := policy.Load(fs.Arg(0))
	if err != nil {
		var verr *policy.Errors
		if errors.As(err, &verr) {
			for _, fe := range verr.List {
				fmt.Fprintln(os.Stderr, fe.Error())
			}
			return fmt.Errorf("%s: %d problem(s)", fs.Arg(0), len(verr.List))
		}
		return err
	}
	// The arr hosts get their API keys: a name must resolve, and only to LAN addresses. Validation itself makes no
	// DNS lookups (it checks IP literals); this command does.
	if p.Import != nil {
		var bad []string
		keys := make([]string, 0, len(p.Import.Arr))
		for k := range p.Import.Arr {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			u, err := url.Parse(p.Import.Arr[k].URL)
			if err != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := arr.CheckHost(ctx, u.Hostname()); err != nil {
				bad = append(bad, fmt.Sprintf("import.arr[%s].url: %v", k, err))
			}
			cancel()
		}
		for _, b := range bad {
			fmt.Fprintln(os.Stderr, b)
		}
		if len(bad) > 0 {
			return fmt.Errorf("%s: %d problem(s)", fs.Arg(0), len(bad))
		}
	}
	if *quiet {
		return nil
	}
	fmt.Printf("%s: ok (%s)\n", p.Dir, strings.Join(p.Files, ", "))
	fmt.Printf("  %d versions, %d profiles, %d formats, %d indexers, %d providers, %d clients, %d views, %d budgets\n",
		len(p.Versions), len(p.Profiles), len(p.Formats), len(p.Indexers), len(p.Providers), len(p.Clients),
		len(p.Views), len(p.Budgets))
	fmt.Println("  outbound hosts:")
	for _, h := range p.OutboundHosts() {
		fmt.Printf("    %-40s %s\n", h.Host, strings.Join(h.Users, ", "))
	}
	return nil
}
