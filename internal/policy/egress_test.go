package policy

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The example ships the guard commented out (off); uncommented, it is a valid guard on indexers.
func TestExampleEgress(t *testing.T) {
	t.Parallel()
	off, err := Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	if off.Egress != nil || off.Egress.RequiresVPN(EgressIndexers) {
		t.Fatalf("example egress is on: %+v", off.Egress)
	}
	p, err := Load(egressOn(t, copyExample(t)))
	if err != nil {
		t.Fatal(err)
	}
	g := p.Egress
	if g == nil {
		t.Fatal("uncommented example has no egress section")
	}
	if g.Interval.D() != 2*time.Minute || g.MaxAge.D() != 6*time.Minute || g.Timeout.D() != 10*time.Second {
		t.Errorf("durations %v %v %v", g.Interval.D(), g.MaxAge.D(), g.Timeout.D())
	}
	if !g.RequiresVPN(EgressIndexers) || g.RequiresVPN(EgressProviders) {
		t.Errorf("require_vpn_for %v", g.RequireVPNFor)
	}
	set := g.VPNSet()
	if set.Empty() || !set.Contains(netip.MustParseAddr("198.51.100.7")) {
		t.Errorf("vpn set %+v", set)
	}
	found := false
	for _, h := range p.OutboundHosts() {
		if h.Host == "api.ipify.org" && strings.Join(h.Users, ",") == "egress.check_url" {
			found = true
		}
	}
	if !found {
		t.Error("check_url host not in the outbound hosts")
	}
	if b := p.Budgets["torrentio"]; b.Daily != 5000 || b.Burst != 5 {
		t.Errorf("torrentio budget %+v", b)
	}
}

func TestEgressDefaults(t *testing.T) {
	t.Parallel()
	dir := copyExample(t)
	path := filepath.Join(dir, "egress.yaml")
	if err := os.WriteFile(path, []byte("egress:\n  check_url: https://echo.example\n  vpn_cidrs: [10.8.0.0/16]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	g := p.Egress
	if g.Interval.D() != DefaultEgressInterval || g.MaxAge.D() != 3*DefaultEgressInterval || g.Timeout.D() != DefaultEgressTimeout {
		t.Errorf("defaults %v %v %v", g.Interval.D(), g.MaxAge.D(), g.Timeout.D())
	}
	if strings.Join(g.RequireVPNFor, ",") != "indexers" {
		t.Errorf("require_vpn_for default %v", g.RequireVPNFor)
	}
	set := g.VPNSet()
	if !set.Contains(netip.MustParseAddr("10.8.3.4")) || set.Contains(netip.MustParseAddr("10.9.0.1")) {
		t.Error("cidr membership")
	}
	if !set.Contains(netip.MustParseAddr("::ffff:10.8.0.1")) {
		t.Error("4in6 address not unmapped")
	}
}

func TestNilEgressGuardOff(t *testing.T) {
	t.Parallel()
	var g *Egress
	if g.RequiresVPN(EgressIndexers) || g.RequiresVPN(EgressProviders) || g.RequiresVPN(EgressMetadata) {
		t.Error("nil egress must not require the VPN")
	}
	if !g.VPNSet().Empty() {
		t.Error("nil egress has an empty VPN set")
	}
}

func TestEgressOmittedClassesRequireIndexers(t *testing.T) {
	t.Parallel()
	g := &Egress{}
	if !g.RequiresVPN(EgressIndexers) || g.RequiresVPN(EgressProviders) || g.RequiresVPN(EgressMetadata) {
		t.Fatal("omitted classes must default to indexers")
	}
}

func TestBadEgress(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		file     string
		old, new string
		want     []string
	}{
		{
			name: "no vpn addresses",
			file: "egress.yaml", old: "vpn_ips: [198.51.100.7]", new: "vpn_ips: []",
			want: []string{"egress: set vpn_ips or vpn_cidrs"},
		},
		{
			name: "bad ip",
			file: "egress.yaml", old: "vpn_ips: [198.51.100.7]", new: "vpn_ips: [198.51.100]",
			want: []string{`egress.vpn_ips[0]: "198.51.100" is not an IP address`},
		},
		{
			name: "catch-all cidr",
			file: "egress.yaml", old: "vpn_cidrs: []", new: "vpn_cidrs: [0.0.0.0/0]",
			want: []string{`egress.vpn_cidrs[0]: "0.0.0.0/0" matches every address`},
		},
		{
			name: "bad cidr",
			file: "egress.yaml", old: "vpn_cidrs: []", new: "vpn_cidrs: [10.1.0.0/33]",
			want: []string{`egress.vpn_cidrs[0]: "10.1.0.0/33" is not a CIDR prefix`},
		},
		{
			name: "check url missing",
			file: "egress.yaml", old: "  check_url: https://api.ipify.org", new: "",
			want: []string{"egress.check_url: required"},
		},
		{
			name: "check url with a secret",
			file: "egress.yaml", old: "check_url: https://api.ipify.org", new: "check_url: https://echo.example/?token=x",
			want: []string{"egress.check_url: secrets in the URL are not allowed"},
		},
		{
			name: "max age below interval",
			file: "egress.yaml", old: "max_age: 6m", new: "max_age: 1m",
			want: []string{"egress.max_age: 1m0s is shorter than interval 2m0s"},
		},
		{
			name: "interval too short",
			file: "egress.yaml", old: "interval: 2m", new: "interval: 1s",
			want: []string{"egress.interval: 1s is too short"},
		},
		{
			name: "unknown class",
			file: "egress.yaml", old: "require_vpn_for: [indexers]", new: "require_vpn_for: [indexers, clients]",
			want: []string{`egress.require_vpn_for[1]: must be one of indexers, metadata, providers, got "clients"`},
		},
		{
			name: "egress twice",
			file: "extra.yaml", new: "egress:\n  check_url: https://x.example\n  vpn_ips: [1.2.3.4]\n",
			want: []string{"egress: defined in both egress.yaml and extra.yaml"},
		},
		{
			name: "proxy with credentials",
			file: "indexers.yaml", old: "    budget: torrentio\n", new: "    budget: torrentio\n    proxy: socks5://u:p@gluetun:1080\n",
			want: []string{"indexers[torrentio].proxy: credentials in the proxy URL are not allowed"},
		},
		{
			name: "proxy bad scheme",
			file: "indexers.yaml", old: "    budget: torrentio\n", new: "    budget: torrentio\n    proxy: ftp://gluetun:21\n",
			want: []string{`indexers[torrentio].proxy: scheme must be http, https, socks5 or socks5h, got "ftp"`},
		},
		{
			name: "proxy with a path",
			file: "indexers.yaml", old: "    budget: torrentio\n", new: "    budget: torrentio\n    proxy: http://gluetun:8888/x\n",
			want: []string{"indexers[torrentio].proxy: a proxy URL is scheme://host:port only"},
		},
		{
			name: "negative cap",
			file: "budgets.yaml", old: "daily: 5000", new: "daily: -1",
			want: []string{"budgets[torrentio]: hourly and daily caps must not be negative"},
		},
		{
			name: "daily below hourly",
			file: "budgets.yaml", old: "daily: 5000", new: "daily: 10, hourly: 20",
			want: []string{"budgets[torrentio].daily: 10 is below the hourly cap 20"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := egressOn(t, copyExample(t))
			path := filepath.Join(dir, c.file)
			b, _ := os.ReadFile(path)
			s := string(b)
			switch {
			case c.old == "":
				s += c.new
			case !strings.Contains(s, c.old):
				t.Fatalf("test setup: %q not found in %s", c.old, c.file)
			default:
				s = strings.Replace(s, c.old, c.new, 1)
			}
			if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(dir)
			if err == nil {
				t.Fatal("bad policy accepted")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("missing %q in:\n%v", w, err)
				}
			}
		})
	}
}

func TestEgressOptionalWithIndexers(t *testing.T) {
	t.Parallel()
	dir := copyExample(t)
	if err := os.Remove(filepath.Join(dir, "egress.yaml")); err != nil {
		t.Fatal(err)
	}
	p, err := Load(dir)
	if err != nil || p.Egress != nil {
		t.Fatalf("policy %v, error %v", p, err)
	}
}

func TestEgressOptionalClasses(t *testing.T) {
	t.Parallel()
	for _, classes := range []string{"[]", "[providers]", "[metadata]", "[providers, metadata]"} {
		t.Run(classes, func(t *testing.T) {
			dir := egressOn(t, copyExample(t))
			path := filepath.Join(dir, "egress.yaml")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(strings.Replace(string(b), "require_vpn_for: [indexers]", "require_vpn_for: "+classes, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if p.Egress.RequireVPNFor == nil || p.Egress.RequiresVPN(EgressIndexers) ||
				p.Egress.RequiresVPN(EgressProviders) != strings.Contains(classes, "providers") ||
				p.Egress.RequiresVPN(EgressMetadata) != strings.Contains(classes, "metadata") {
				t.Fatalf("classes %v", p.Egress.RequireVPNFor)
			}
			clone, err := p.Clone()
			if err != nil {
				t.Fatal(err)
			}
			if clone.Egress.RequireVPNFor == nil || clone.Egress.RequiresVPN(EgressIndexers) {
				t.Fatalf("clone classes %v", clone.Egress.RequireVPNFor)
			}
		})
	}
}

func TestProxyAccepted(t *testing.T) {
	t.Parallel()
	dir := copyExample(t)
	path := filepath.Join(dir, "indexers.yaml")
	b, _ := os.ReadFile(path)
	s := strings.Replace(string(b), "    budget: torrentio\n", "    budget: torrentio\n    proxy: socks5h://gluetun:1080\n", 1)
	os.WriteFile(path, []byte(s), 0o644)
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Indexers["torrentio"].Proxy != "socks5h://gluetun:1080" {
		t.Errorf("proxy %q", p.Indexers["torrentio"].Proxy)
	}
}
