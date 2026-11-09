package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// ProbeBudget is the budget charged to a source test that has no saved budget of its own: a brand-new candidate, or
// a saved source without a budget. Every probe of every generation shares it, so a test is never unbudgeted.
const ProbeBudget = "settings-probe"

// The probe budget: a few requests a minute across all such tests.
const (
	probeRequests = 4
	probePer      = time.Minute
	probeBurst    = 4
	// probeHealthTTL is how long a candidate's settled failure record is kept after its last update.
	probeHealthTTL = time.Hour
)

// ProbeOptions describe one source test (Probe).
type ProbeOptions struct {
	// Target is the tested indexer or provider in the probe's policy.
	Target Target
	// Budget is the saved source's budget key, charged whatever the tested configuration says. Empty (a new source,
	// or a saved one without a budget) charges ProbeBudget.
	Budget string
	// Candidate marks a configuration that is not the saved source as saved. Its failures, rate limits and
	// successes are recorded apart from the live sources' health, which still refuses it while backing off; its
	// proxy must be one of the live egress paths.
	Candidate bool
	// Secrets are masked in logs and errors, like Options.Secrets.
	Secrets []string
	Logger  *slog.Logger
	Version string
}

// probeAuth is what a probe's connections may reach beyond public addresses: the live gateway's allowlist and its
// proxies. Private, loopback and link-local addresses are dialed only for those.
type probeAuth struct {
	live     allowlist
	proxies  map[string]bool // host:port of the live egress paths' proxies
	lookupIP func(ctx context.Context, host string) ([]netip.Addr, error)
}

// Probe builds the gateway one admin source test runs through, on top of g (the live runtime's gateway), for the
// policy p holding the tested source (the stored head, or an unsaved candidate built over it). It holds the test's
// authorization, charging and health accounting in one place:
//
//   - Destinations: the live allowlist plus the tested source's own scheme-default or explicit host:port. A
//     private, loopback or link-local address is reached only when that host:port is already in the live
//     allowlist (or is a live proxy); this is checked on the resolved addresses at dial time, for every redirect
//     hop, besides the VPN guard and redirect rules every request has. A candidate's proxy must be a live path.
//   - Charging: o.Budget, the saved source's budget, whatever the configuration names; else ProbeBudget.
//   - Health: back-offs, budget windows and egress checks are the live ones. A candidate's own outcomes go to a
//     separate memory-only record shared by all probes, so it never moves a live failure streak or back-off.
//
// A refusal before any request (an unknown target, a private address or proxy that is not live) is a *Refused.
// Nothing is published or saved.
func (g *Gateway) Probe(ctx context.Context, p *policy.Policy, o ProbeOptions) (*Gateway, error) {
	if g == nil || p == nil {
		return nil, errors.New("gateway: probe needs the live gateway and a policy")
	}
	auth := &probeAuth{live: g.allow, proxies: map[string]bool{}, lookupIP: g.lookupIP}
	for _, path := range g.paths {
		if u, err := url.Parse(path.proxy); path.proxy != "" && err == nil && u.Host != "" {
			auth.proxies[hostPort(u)] = true
		}
	}
	base := g.dial
	if base == nil {
		base = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	if o.Logger == nil {
		o.Logger = g.log
	}
	// The probe's dialer wraps the live one, so the probe resolves names itself, as the live gateway would (its
	// resolver, or the system's for the real dialer): a guarded dial then goes to its checked addresses with the
	// proven families and the local exception, like a live request's, instead of a pinned dial to the name. A live
	// gateway with an injected dialer and no resolver (tests) resolves nothing, and neither does its probe.
	lookup := auth.lookup
	if g.lookupIP == nil && g.dial != nil {
		lookup = nil
	}
	pg, err := New(ctx, p, Options{Logger: o.Logger, Version: o.Version, Timeout: g.timeout, MaxResponseBytes: g.maxBody,
		MaxWait: g.maxWait, Previous: g, Generation: g.generation, Secrets: o.Secrets, LookupIP: lookup, now: g.now,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return auth.dial(ctx, base, network, addr)
		}})
	if err != nil {
		return nil, err
	}
	pg.probe = auth
	ti := pg.targets[o.Target.String()]
	if ti == nil || (ti.class != ClassIndexer && ti.class != ClassProvider) {
		return nil, &Refused{Reason: ReasonUnknownTarget, Target: o.Target.String(), Detail: "not an indexer or provider of the probe policy"}
	}
	if o.Candidate && ti.path.proxy != "" {
		if lp := g.paths[ti.path.name]; lp == nil || lp.proxy != ti.path.proxy {
			return nil, &Refused{Reason: ReasonHost, Target: ti.name, Detail: "the proxy is not one of the live egress paths"}
		}
	}
	// The tested source's own address joins the live allowlist, unless it is a private one the live list lacks.
	u, err := url.Parse(sourceURL(p, o.Target))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, &Refused{Reason: ReasonHost, Target: ti.name, Detail: "not an http(s) URL"}
	}
	host, port, _ := net.SplitHostPort(hostPort(u))
	if !auth.approved(host, port) {
		if ip, err := netip.ParseAddr(host); err == nil && !publicAddr(ip) {
			return nil, &Refused{Reason: ReasonHost, Target: ti.name,
				Detail: "a private, loopback or link-local address that the live allowlist does not hold"}
		}
	}
	pg.allow = make(allowlist, len(g.allow)+1)
	for h := range g.allow {
		pg.allow[h] = true
	}
	pg.allow[net.JoinHostPort(host, port)] = true
	// A cardigann indexer's further hosts (an API host its definition reaches) join it too, on the same terms.
	if ix := p.Indexers[o.Target.Key]; o.Target.Class == ClassIndexer && ix != nil {
		for _, h := range ix.Hosts {
			hu, err := url.Parse("https://" + h)
			if err != nil || hu.Host == "" {
				return nil, &Refused{Reason: ReasonHost, Target: ti.name, Detail: "a host of the indexer is not a host name"}
			}
			hh := strings.ToLower(hu.Hostname())
			if ip, err := netip.ParseAddr(hh); err == nil && !publicAddr(ip) && !auth.approved(hh, "443") && !auth.approved(hh, "80") {
				return nil, &Refused{Reason: ReasonHost, Target: ti.name,
					Detail: "a host of the indexer is a private, loopback or link-local address that the live allowlist does not hold"}
			}
			pg.allow[strings.ToLower(hu.Host)] = true
		}
	}
	now := g.now()
	pg.runtime.probeOnce.Do(func() {
		pg.runtime.probeHealth = &state{log: g.log, backoffs: map[string]*backoff{}, windows: map[windowKey]int{}}
		pg.runtime.probeBucket = &bucketState{tokens: probeBurst, last: now}
	})
	if o.Budget != "" && pg.buckets[o.Budget] != nil {
		if o.Budget != ti.budget {
			ti.budget, ti.limitBudget = o.Budget, false
		}
	} else {
		probe := newBucket(ProbeBudget, &policy.Budget{Requests: probeRequests, Per: policy.Duration(probePer), Burst: probeBurst}, now)
		probe.state = pg.runtime.probeBucket
		pg.buckets[ProbeBudget] = probe
		ti.budget, ti.limitBudget = ProbeBudget, false
	}
	if o.Candidate {
		// A candidate's rate limit is recorded against its own key, never a budget other sources share.
		ti.limitBudget = false
		pg.health = pg.runtime.probeHealth
		pg.health.prune(now, now.Add(-probeHealthTTL))
		// Candidate keys and hosts are the caller's: keep them out of the live metrics' label sets.
		pg.metrics = newMetrics(pg)
	}
	return pg, nil
}

// sourceURL is the configured URL of an indexer or provider target.
func sourceURL(p *policy.Policy, t Target) string {
	switch t.Class {
	case ClassIndexer:
		if ix := p.Indexers[t.Key]; ix != nil {
			return ix.URL
		}
	case ClassProvider:
		if pv := p.Providers[t.Key]; pv != nil {
			return pv.URL
		}
	}
	return ""
}

// approved: host:port is in the live allowlist or is a live proxy.
func (a *probeAuth) approved(host, port string) bool {
	host = strings.ToLower(strings.Trim(host, "[]"))
	return a.live.allows(host, port) || a.proxies[net.JoinHostPort(host, port)]
}

func (a *probeAuth) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return []netip.Addr{ip}, nil
	}
	if a.lookupIP != nil {
		return a.lookupIP(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// resolvePublic resolves host and refuses unless every address is public.
func (a *probeAuth) resolvePublic(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := a.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
	}
	for _, ip := range ips {
		if !publicAddr(ip) {
			return nil, &Refused{Reason: ReasonHost,
				Detail: "the host resolves to a private, loopback or link-local address that the live allowlist does not hold"}
		}
	}
	return ips, nil
}

// dial connects a probe: an approved host:port as configured, anything else only to the public addresses it
// resolves to, which are the addresses dialed (no second lookup).
func (a *probeAuth) dial(ctx context.Context, base func(context.Context, string, string) (net.Conn, error), network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if a.approved(host, port) {
		return base(ctx, network, addr)
	}
	// A guarded dial goes to the addresses the VPN guard resolved and checked for a host name: judged by that name and
	// all those addresses, as a lookup here would be, with no second lookup.
	if cd, ok := ctx.Value(dialHostKey{}).(checkedDial); ok {
		if ip, err := netip.ParseAddr(host); err == nil && slices.Contains(cd.addrs, ip) {
			if a.approved(cd.host, port) {
				return base(ctx, network, addr)
			}
			for _, x := range cd.addrs {
				if !publicAddr(x) {
					return nil, &Refused{Reason: ReasonHost,
						Detail: "the host resolves to a private, loopback or link-local address that the live allowlist does not hold"}
				}
			}
			return base(ctx, network, addr)
		}
	}
	ips, err := a.resolvePublic(ctx, host)
	if err != nil {
		return nil, err
	}
	var last error
	for _, ip := range ips {
		conn, err := base(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}

// checkHost is the resolved-address check for a request a proxy carries (the proxy resolves the host itself).
func (a *probeAuth) checkHost(ctx context.Context, u *url.URL) *Refused {
	host, port, _ := net.SplitHostPort(hostPort(u))
	if a.approved(host, port) {
		return nil
	}
	if _, err := a.resolvePublic(ctx, host); err != nil {
		var r *Refused
		if errors.As(err, &r) {
			return r
		}
		return &Refused{Reason: ReasonHost, Detail: "the host could not be resolved"}
	}
	return nil
}

var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),     // this network
	netip.MustParsePrefix("100.64.0.0/10"), // shared address space (carrier-grade NAT, tailnets)
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved, broadcast
	netip.MustParsePrefix("64:ff9b::/96"),  // NAT64: could reach any IPv4 address
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"), // 6to4: embeds an IPv4 address
}

// publicAddr reports whether ip is a global unicast address outside the private, loopback, link-local and other
// local or special-purpose ranges.
func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
