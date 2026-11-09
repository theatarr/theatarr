// Package golive is the read-only preflight for letting one version grab (`theatarr golive check`, docs/golive.md):
// a follower library's trial and cutover. It reads the policy, the names (never the values) of the secrets the
// active version will use, the health checks, the shadow or follower tables, the library roots and the arr instance
// that feeds it (GET only), and prints one PASS, INFO, WARN or FAIL line per check with the fix. A FAIL means the
// version must not grab yet.
//
// Nothing here writes: the database is read through the store's reader pool, the filesystem with stat and
// access(2), the arr with the GET-only client in internal/arr.
//
// Every input is an option, so tests run on a fake policy directory, a temp database and a fake arr; a check whose
// input is missing reports WARN "not checked" rather than passing.
package golive

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/shadow"
)

// Status of one check. INFO is a fact the runbook acts on later; it never blocks.
type Status string

const (
	Pass Status = "PASS"
	Info Status = "INFO"
	Warn Status = "WARN"
	Fail Status = "FAIL"
)

// Sections, in the order they print.
const (
	SecPolicy   = "policy"
	SecSecrets  = "secrets"
	SecHealth   = "health"
	SecShadow   = "shadow"
	SecLibrary  = "library"
	SecHandover = "handover"
)

// Result is one check's line.
type Result struct {
	Section string `json:"section"`
	Check   string `json:"check"`
	Status  Status `json:"status"`
	Message string `json:"message"`
	// Fix says what to do about a WARN or FAIL (or, for INFO, what the runbook does with it).
	Fix string `json:"fix,omitempty"`
}

// Report is one version's preflight.
type Report struct {
	Version        string    `json:"version"`
	Kind           string    `json:"kind"`
	Mode           string    `json:"mode"`
	PolicyRevision string    `json:"policy_revision"`
	At             time.Time `json:"at"`
	Results        []Result  `json:"results"`
}

// Failed reports whether any check failed.
func (r *Report) Failed() bool { return r.Count(Fail) > 0 }

// Count is the number of results with status s.
func (r *Report) Count(s Status) int {
	n := 0
	for _, x := range r.Results {
		if x.Status == s {
			n++
		}
	}
	return n
}

func (r *Report) add(sec, check string, st Status, fix, format string, args ...any) {
	r.Results = append(r.Results, Result{Section: sec, Check: check, Status: st, Message: fmt.Sprintf(format, args...),
		Fix: fix})
}

// FS is what the checks read from the filesystem. OSFS is the real one.
type FS interface {
	Stat(path string) (fs.FileInfo, error)
	// Writable is nil when this process may create and remove entries in the directory (access(2) W_OK|X_OK, which
	// also says EROFS for a read-only bind mount).
	Writable(path string) error
	// Readable is nil when this process may list and traverse the directory.
	Readable(path string) error
}

// ArrGetter is the GET-only arr client (*arr.Client).
type ArrGetter interface {
	GetJSON(ctx context.Context, endpoint string, query url.Values, v any) error
}

// Options are the inputs. Policy and Version are required; every other input is optional and its checks report
// WARN "not checked" without it.
type Options struct {
	Policy  *policy.Policy
	Version string
	// DB is the database, read only (the store's reader pool). nil skips the shadow, library-data and metadata checks.
	DB *sql.DB
	// Env looks up an environment variable; only whether it is empty is used. Default: none set.
	Env func(string) string
	// Credential reports whether a credential resolves for the checked generation (settings.Credentials.Resolves):
	// entity and key name a provider or indexer record ("providers", "rd"), whose key the settings database may
	// hold; other credentials pass only env, the policy's *_env name. Default: Env for env names, and the policy's
	// own settings credentials for the database's derived names (policy.SettingsSecretPrefix).
	Credential func(entity, key, env string) bool
	FS         FS
	// Arr returns the GET-only client of an import.arr instance; nil (or an error) skips the arr reads.
	Arr func(instance string) (ArrGetter, error)
	// Health is the service's health results (theatarr health's set, MountCheck run even with the library off).
	Health []health.Result
	// Service is what the running theatarr serve exposes on /metrics: the VPN egress state and indexer back-offs. nil
	// when no service was reachable.
	Service *ServiceState
	// Scan walks the version's roots read-only (library.Verify). nil skips it.
	Scan func(ctx context.Context, version string) ([]*library.LinkScan, error)
	// Profile builds a profile by name for a kind (the decision engine), nil when it builds. nil skips the build.
	Profile func(name string, kind policy.Kind) error
	// PublicURL is Theatarr's address as clients will use it, for the client changes. Default http://<theatarr host>:7700.
	PublicURL string
	// Days is the shadow evidence window. Default 14 (docs/golive.md).
	Days int
	// Bar is the agreement the window must reach. Default shadow.AgreementBar (0.95).
	Bar float64
	// ProviderExpiry is each provider's paid-plan end, by provider key, as known to the operator (theatarr golive check
	// -provider-expiry tb=2027-01-31). A provider in the fallback that ends within 30 days is a WARN, never a FAIL:
	// the router skips a provider that refuses and tries the next.
	ProviderExpiry map[string]time.Time
	// MinGrabs is the fewest arr grabs in the window worth judging. Default 20.
	MinGrabs int
	Now      time.Time
}

// ServiceState is read from the running service's /metrics.
type ServiceState struct {
	// VPNOK is theatarr_egress_vpn_ok per egress path; empty when the policy has no egress section.
	VPNOK map[string]bool
	// Backoffs are the subjects of active gateway back-offs (indexer:<key>, budget:<key>, ...).
	Backoffs []string
}

// Run checks one version. The error is for a version the policy does not have; everything else is a result.
func Run(ctx context.Context, o Options) (*Report, error) {
	if o.Policy == nil {
		return nil, fmt.Errorf("golive: no policy")
	}
	v := o.Policy.Versions[o.Version]
	if v == nil {
		return nil, fmt.Errorf("golive: the policy has no version %q (have %s)", o.Version,
			strings.Join(Versions(o.Policy), ", "))
	}
	if o.Env == nil {
		o.Env = func(string) string { return "" }
	}
	if o.Credential == nil {
		credentials := settings.Credentials{Policy: o.Policy, Lookup: func(name string) (string, bool) {
			v := o.Env(name)
			return v, v != ""
		}}
		o.Credential = credentials.Resolves
	}
	if o.Days <= 0 {
		o.Days = 14
	}
	if o.Bar <= 0 {
		o.Bar = shadow.AgreementBar
	}
	if o.MinGrabs <= 0 {
		o.MinGrabs = 20
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.PublicURL == "" {
		o.PublicURL = "http://<theatarr host>:7700"
	}
	o.PublicURL = strings.TrimRight(o.PublicURL, "/")
	r := &Report{Version: o.Version, Kind: string(v.Kind), Mode: string(v.Mode), PolicyRevision: o.Policy.Revision,
		At: o.Now.UTC()}
	if r.Mode == "" {
		r.Mode = string(policy.ModeShadow)
	}
	c := &checker{o: o, v: v, r: r}
	c.policy(ctx)
	c.secrets()
	c.health(ctx)
	if v.Follows != "" {
		c.parallel(ctx) // a parallel version runs beside the arr: no shadow window (parallel.go)
	} else {
		c.shadow(ctx)
	}
	c.library(ctx)
	c.handover(ctx)
	return r, nil
}

// Versions lists the policy's versions, sorted.
func Versions(p *policy.Policy) []string {
	out := make([]string, 0, len(p.Versions))
	for k := range p.Versions {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type checker struct {
	o Options
	v *policy.VersionPolicy
	r *Report
}

func (c *checker) add(sec, check string, st Status, fix, format string, args ...any) {
	c.r.add(sec, check, st, fix, format, args...)
}

// owners are the import.arr instances that feed the version, sorted.
func (c *checker) owners() []string {
	var out []string
	if c.o.Policy.Import != nil {
		for k, a := range c.o.Policy.Import.Arr {
			if a.Version == c.o.Version {
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Print writes the report as text: a head line, one line per result with its fix indented below, and the counts.
func Print(w io.Writer, r *Report) {
	fmt.Fprintf(w, "== %s (%s, mode %s, policy %s)\n", r.Version, r.Kind, r.Mode, r.PolicyRevision)
	for _, x := range r.Results {
		fmt.Fprintf(w, "%-4s  %-8s  %-22s %s\n", x.Status, x.Section, x.Check, x.Message)
		if x.Fix != "" {
			fmt.Fprintf(w, "%38s fix: %s\n", "", x.Fix)
		}
	}
	fmt.Fprintf(w, "-- %s: %d FAIL, %d WARN, %d INFO, %d PASS\n", r.Version, r.Count(Fail), r.Count(Warn),
		r.Count(Info), r.Count(Pass))
}

// list joins at most n items, saying how many more there are.
func list(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:n], ", "), len(items)-n)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
