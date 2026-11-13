package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

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
