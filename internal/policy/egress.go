package policy

import (
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Egress configures the optional VPN egress guard (internal/gateway). The gateway asks an IP-echo endpoint which
// address it is seen from, through each egress path it uses, and refuses requests of the classes in RequireVPNFor
// unless the latest check is fresh and saw an address in the VPN set.
//
// With no egress section the guard is off.
type Egress struct {
	// CheckURL is an IP-echo endpoint. It answers the caller's address as plain text, or as JSON with an "ip" field.
	// Its host is allowlisted automatically.
	CheckURL string `yaml:"check_url"`
	// VPNIPs and VPNCIDRs are the expected exit addresses. At least one entry is required.
	VPNIPs []string `yaml:"vpn_ips"`
	// VPNCIDRs lists the allowed VPN exit address ranges.
	VPNCIDRs []string `yaml:"vpn_cidrs"`
	// Interval is the time between checks. Default 2m.
	Interval Duration `yaml:"interval"`
	// MaxAge is how old the last good check may be before guarded requests are refused. Default 3 × Interval.
	MaxAge Duration `yaml:"max_age"`
	// Timeout bounds one check. Default 10s.
	Timeout Duration `yaml:"timeout"`
	// RequireVPNFor lists the classes that need a good check: indexers, providers, metadata.
	// Omitted defaults to indexers; an explicit empty list monitors without refusing requests.
	RequireVPNFor []string `yaml:"require_vpn_for"`
}

// Request classes the egress guard can cover.
const (
	EgressIndexers  = "indexers"
	EgressProviders = "providers"
	EgressMetadata  = "metadata"
)

var egressClasses = setOf(EgressIndexers, EgressProviders, EgressMetadata)

// Egress defaults, applied by Validate.
const (
	DefaultEgressInterval = 2 * time.Minute
	DefaultEgressTimeout  = 10 * time.Second
)

// RequiresVPN reports whether requests of class (EgressIndexers, EgressProviders, EgressMetadata) need a good egress
// check. With no egress section the guard is off.
func (e *Egress) RequiresVPN(class string) bool {
	if e == nil {
		return false
	}
	if e.RequireVPNFor == nil {
		return class == EgressIndexers
	}
	return slices.Contains(e.RequireVPNFor, class)
}

// VPNSet is the parsed exit address set. Call it on a validated policy; entries that don't parse are skipped.
func (e *Egress) VPNSet() VPNSet {
	var s VPNSet
	if e == nil {
		return s
	}
	for _, v := range e.VPNIPs {
		if a, err := netip.ParseAddr(v); err == nil {
			s.IPs = append(s.IPs, a.Unmap())
		}
	}
	for _, v := range e.VPNCIDRs {
		if p, err := netip.ParsePrefix(v); err == nil {
			s.Prefixes = append(s.Prefixes, p.Masked())
		}
	}
	return s
}

// VPNSet is a set of expected exit addresses.
type VPNSet struct {
	IPs      []netip.Addr
	Prefixes []netip.Prefix
}

// Contains reports whether a is an expected exit address.
func (s VPNSet) Contains(a netip.Addr) bool {
	a = a.Unmap()
	if slices.Contains(s.IPs, a) {
		return true
	}
	for _, p := range s.Prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Empty reports whether the set has no entries.
func (s VPNSet) Empty() bool { return len(s.IPs) == 0 && len(s.Prefixes) == 0 }

func (p *Policy) validateEgress(e *Errors) {
	for _, k := range sortedKeys(p.Indexers) {
		if px := p.Indexers[k].Proxy; px != "" {
			checkProxyURL(e, fmt.Sprintf("indexers[%s].proxy", k), px)
		}
	}
	g := p.Egress
	if g == nil {
		return
	}
	checkURL(e, "egress.check_url", g.CheckURL)
	if len(g.VPNIPs) == 0 && len(g.VPNCIDRs) == 0 {
		e.add("egress", "set vpn_ips or vpn_cidrs: the exit addresses the VPN uses")
	}
	for i, v := range g.VPNIPs {
		if _, err := netip.ParseAddr(v); err != nil {
			e.add(fmt.Sprintf("egress.vpn_ips[%d]", i), "%q is not an IP address", v)
		}
	}
	for i, v := range g.VPNCIDRs {
		pr, err := netip.ParsePrefix(v)
		switch {
		case err != nil:
			e.add(fmt.Sprintf("egress.vpn_cidrs[%d]", i), "%q is not a CIDR prefix", v)
		case pr.Bits() == 0:
			e.add(fmt.Sprintf("egress.vpn_cidrs[%d]", i), "%q matches every address", v)
		}
	}
	if g.Interval < 0 || g.MaxAge < 0 || g.Timeout < 0 {
		e.add("egress", "interval, max_age and timeout must not be negative")
	}
	if g.Interval.D() < 10*time.Second {
		e.add("egress.interval", "%v is too short; at least 10s", g.Interval.D())
	}
	if g.MaxAge < g.Interval {
		e.add("egress.max_age", "%v is shorter than interval %v: every request would be refused between checks",
			g.MaxAge.D(), g.Interval.D())
	}
	for i, c := range g.RequireVPNFor {
		if !egressClasses[c] {
			e.add(fmt.Sprintf("egress.require_vpn_for[%d]", i), "must be one of %s, got %q; omit for [indexers], use [] to monitor only, or omit egress to turn the guard off", list(egressClasses), c)
		}
	}
}

func checkProxyURL(e *Errors, path, v string) {
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		e.add(path, "%q is not a proxy URL", v)
		return
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		e.add(path, "scheme must be http, https, socks5 or socks5h, got %q", u.Scheme)
	}
	if u.User != nil {
		e.add(path, "credentials in the proxy URL are not allowed")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		e.add(path, "a proxy URL is scheme://host:port only")
	}
}
