package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/buildinfo"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/server"
)

func init() {
	register(command{
		name:    "egress-check",
		summary: "run one VPN egress check per egress path and print the exit address and verdict",
		run:     runEgressCheck,
	})
}

func runEgressCheck(args []string) error {
	fs := flag.NewFlagSet("egress-check", flag.ContinueOnError)
	settingsSourceFlag(fs)
	fs.String("db", defaultDBPath(), "database file (env THEATARR_DB)")
	policyDir := policyDirDefault()
	verbose := fs.Bool("v", false, "log the gateway's lines to stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	pol, err := loadCommandPolicy(fs, policyDir)
	if err != nil {
		return err
	}
	if pol.Egress == nil {
		return printEgress(os.Stdout, pol, nil)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if *verbose {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	ctx, cancel := context.WithTimeout(context.Background(), pol.Egress.Timeout.D()+5*time.Second)
	defer cancel()
	gw, err := gateway.New(ctx, pol, gateway.Options{Logger: log, Version: buildinfo.Version})
	if err != nil {
		return err
	}
	return printEgress(os.Stdout, pol, gw.CheckEgress(ctx))
}

// printEgress writes one line per path and fails if a required check is not on the VPN.
func printEgress(w io.Writer, pol *policy.Policy, states []gateway.EgressState) error {
	if pol.Egress == nil {
		fmt.Fprintln(w, "WARNING: VPN guard off (no egress section)")
		return nil
	}
	classes := pol.Egress.RequireVPNFor
	if classes == nil {
		classes = []string{policy.EgressIndexers}
	}
	if len(classes) == 0 {
		fmt.Fprintln(w, "VPN guard monitor-only: no requests require VPN")
	} else {
		fmt.Fprintf(w, "VPN required for: %s\n", strings.Join(classes, ", "))
	}
	fmt.Fprintf(w, "vpn set: %s\n", strings.Join(append(append([]string{}, pol.Egress.VPNIPs...), pol.Egress.VPNCIDRs...), ", "))
	bad := 0
	for _, s := range states {
		verdict := "VPN OK"
		if !s.OK {
			verdict, bad = "NOT VPN: "+s.Error, bad+1
		}
		ip := s.IP
		if ip == "" {
			ip = "-"
		}
		fmt.Fprintf(w, "%-30s %-40s %s\n", s.Path, ip, verdict)
	}
	if bad > 0 && len(classes) > 0 {
		return fmt.Errorf("%d of %d egress path(s) not on the VPN: requests for %s through them are refused", bad, len(states), strings.Join(classes, ", "))
	}
	return nil
}

// egressReadiness adds the egress guard's state to /readyz. It does not fail readiness: the guard already refuses
// the requests that need the VPN, and the API, views and jobs that need no VPN keep working (see docs/design/gateway.md).
func egressReadiness(base server.Readiness, gw *gateway.Gateway) server.Readiness {
	return func(ctx context.Context) (map[string]any, error) {
		detail, err := base(ctx)
		if detail == nil {
			detail = map[string]any{}
		}
		detail["egress"] = gw.EgressSummary()
		return detail, err
	}
}
