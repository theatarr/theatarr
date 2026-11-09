package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/policy"
)

// EgressState is the latest egress check on one path.
type EgressState struct {
	// Path is "direct" or the proxy URL.
	Path string `json:"path"`
	// OK: the check succeeded and saw an address in the VPN set.
	OK bool `json:"ok"`
	// IP is the address the echo endpoint saw; empty if the check failed before it answered.
	IP string `json:"ip,omitempty"`
	// CheckedAt is when the check finished; zero if none has yet.
	CheckedAt time.Time `json:"checked_at"`
	// Error says why the check is not OK.
	Error string `json:"error,omitempty"`
	// other is the exit address a second echo pinned to the other address family saw (the direct path only, after
	// a good check); invalid when that family has no route or the echo does not answer over it. A guarded dial may
	// use that family only while other is in the VPN set (provenFamilies, provenDial).
	other netip.Addr
}

type egressConfig struct {
	checkURL string
	set      policy.VPNSet
	interval time.Duration
	maxAge   time.Duration
	timeout  time.Duration
	classes  []string
}

func newEgressConfig(e *policy.Egress) (*egressConfig, error) {
	if e == nil {
		return nil, nil
	}
	c := &egressConfig{checkURL: e.CheckURL, set: e.VPNSet(), interval: e.Interval.D(), maxAge: e.MaxAge.D(),
		timeout: e.Timeout.D(), classes: e.RequireVPNFor}
	if c.classes == nil {
		c.classes = []string{policy.EgressIndexers}
	}
	if c.interval <= 0 {
		c.interval = policy.DefaultEgressInterval
	}
	if c.maxAge <= 0 {
		c.maxAge = 3 * c.interval
	}
	if c.timeout <= 0 {
		c.timeout = policy.DefaultEgressTimeout
	}
	if c.set.Empty() {
		return nil, fmt.Errorf("gateway: egress has no VPN addresses")
	}
	return c, nil
}

// sameTrust reports whether a check under c proves what a check under o would: the same echo endpoint and the same
// accepted exit addresses. Only then may a new gateway generation keep the previous one's verdict on a path.
func (c *egressConfig) sameTrust(o *egressConfig) bool {
	if c == nil || o == nil {
		return c == o
	}
	return c.checkURL == o.checkURL && slices.Equal(c.set.IPs, o.set.IPs) && slices.Equal(c.set.Prefixes, o.set.Prefixes)
}

// vpnRule is what a request of one generation must satisfy, at one request, redirect or dial boundary, to reach a
// target through the VPN. A generation that is current (or only prepared) follows its own settings. One that an
// operation is still pinned to after another replaced it follows the stricter of its own and the current settings:
// the target needs the VPN when either requires it, the proof may be no older than the shorter
// max_age, and the exit address must be in every VPN set involved. The request keeps the pinned transport (its
// proxy, credentials and address); what may prove that route is the current generation's check of the same path
// (same name and proxy) or the pinned path's own check when it echoes the current check_url, whichever observed the
// path last (proof). Without either, nothing proves the route under the current settings and the request is refused.
type vpnRule struct {
	need   bool
	path   string
	checks []*egressCheck // candidates, in order; none: no check proves the route under the settings in force
	// lineage holds every generation's check of the same egress path (name and proxy): those that are not
	// candidates cannot prove the route, but a newer observation of theirs still overrides an older good one.
	lineage *egressLineage
	maxAge  time.Duration
	sets    []policy.VPNSet // the exit address must be in each
}

// vpnRule computes ti's rule now; settings may change between two boundaries of one request.
func (g *Gateway) vpnRule(ti *targetInfo) vpnRule {
	r := vpnRule{path: ti.path.name, checks: []*egressCheck{ti.path.check}, lineage: ti.path.check.lineage}
	if g.egress != nil {
		r.need, r.maxAge, r.sets = ti.requireVPN, g.egress.maxAge, []policy.VPNSet{g.egress.set}
	}
	cur := g.current()
	if !g.activated.Load() || cur == g || cur == nil || cur.egress == nil {
		return r
	}
	r.need = r.need || cur.pol.Egress.RequiresVPN(egressClass(ti.class))
	if g.egress == nil || g.egress.checkURL != cur.egress.checkURL {
		r.checks = nil
	}
	if cp := cur.paths[ti.path.name]; cp != nil && cp.proxy == ti.path.proxy && cp.check != ti.path.check {
		r.checks = append([]*egressCheck{cp.check}, r.checks...)
	}
	if g.egress == nil || cur.egress.maxAge < r.maxAge {
		r.maxAge = cur.egress.maxAge
	}
	if g.egress == nil || !g.egress.sameTrust(cur.egress) {
		r.sets = append(r.sets, cur.egress.set)
	}
	return r
}

// egressClass is the egress.require_vpn_for class of a target class ("" for classes that never need the VPN).
func egressClass(c Class) string {
	switch c {
	case ClassIndexer:
		return policy.EgressIndexers
	case ClassProvider:
		return policy.EgressProviders
	case ClassMetadata:
		return policy.EgressMetadata
	}
	return ""
}

// trusted reports whether ip is in every VPN set of the rule.
func (r vpnRule) trusted(ip netip.Addr) bool {
	for _, s := range r.sets {
		if !s.Contains(ip) {
			return false
		}
	}
	return len(r.sets) > 0
}

// families reports which address families s proves under the rule (see provenFamilies).
func (r vpnRule) families(s EgressState) (v4, v6 bool) {
	ip, err := netip.ParseAddr(s.IP)
	if err != nil || !s.OK || !r.trusted(ip) {
		return false, false
	}
	ip = ip.Unmap()
	v4, v6 = ip.Is4(), !ip.Is4()
	if o := s.other.Unmap(); o.IsValid() && r.trusted(o) {
		v4, v6 = v4 || o.Is4(), v6 || !o.Is4()
	}
	return v4, v6
}

// proof is what a guarded request goes out on: the candidate check that proves the route, its observation, and the
// address families the path's observations prove (r.families narrowed by a newer observation of another generation).
type proof struct {
	check  *egressCheck
	state  EgressState
	v4, v6 bool
}

// proof judges the newest observation of the path. Among the candidate checks the latest CheckedAt decides (the
// current generation's on a tie): both watch the same egress path, so a newer check that failed or saw an exit
// outside the VPN set overrides an older good one, which must not stand in for it. Only a check that
// has not run yet leaves the other candidate to decide. The path's checks of other generations (an intermediate
// settings generation, an older one an operation still runs checks on, a prepared one) cannot prove the route, but
// an observation of theirs newer than the candidate's refuses unless it saw a fresh exit every VPN set of the rule
// trusts, and narrows the proven families to its own.
func (r vpnRule) proof(ti *targetInfo, now time.Time) (proof, *Refused) {
	if len(r.checks) == 0 {
		return proof{}, &Refused{Reason: ReasonVPN, Target: ti.name, Detail: "egress " + r.path +
			": no check under the current settings proves this route"}
	}
	best, bs := r.checks[0], r.checks[0].snapshot()
	for _, c := range r.checks[1:] {
		if s := c.snapshot(); s.CheckedAt.After(bs.CheckedAt) {
			best, bs = c, s
		}
	}
	if err := r.judge(ti, bs, now); err != nil {
		return proof{}, err
	}
	p := proof{check: best, state: bs}
	p.v4, p.v6 = r.families(bs)
	var newest EgressState
	for _, c := range r.lineage.all() {
		if slices.Contains(r.checks, c) {
			continue
		}
		if s := c.snapshot(); s.CheckedAt.After(bs.CheckedAt) && s.CheckedAt.After(newest.CheckedAt) {
			newest = s
		}
	}
	if !newest.CheckedAt.IsZero() {
		if err := r.judge(ti, newest, now); err != nil {
			err.Detail += " (a newer check under other settings)"
			return proof{}, err
		}
		v4, v6 := r.families(newest)
		p.v4, p.v6 = p.v4 && v4, p.v6 && v6
	}
	return p, nil
}

// ok refuses unless one of the rule's checks proves the route (proof).
func (r vpnRule) ok(ti *targetInfo, now time.Time) *Refused {
	_, err := r.proof(ti, now)
	return err
}

func (c *egressCheck) snapshot() EgressState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// judge refuses unless s is a fresh good check whose exit address the rule trusts.
func (r vpnRule) judge(ti *targetInfo, s EgressState, now time.Time) *Refused {
	ip, ipErr := netip.ParseAddr(s.IP)
	switch {
	case s.CheckedAt.IsZero():
		return &Refused{Reason: ReasonVPN, Target: ti.name, Detail: "egress " + s.Path + " not checked yet"}
	case !s.OK:
		return &Refused{Reason: ReasonVPN, Target: ti.name, Detail: "egress " + s.Path + ": " + s.Error}
	case ipErr != nil || !r.trusted(ip):
		return &Refused{Reason: ReasonVPN, Target: ti.name, Detail: "egress " + s.Path + ": exit address " + s.IP +
			" is not in the VPN set"}
	case now.Sub(s.CheckedAt) > r.maxAge:
		return &Refused{Reason: ReasonVPN, Target: ti.name,
			Detail: fmt.Sprintf("egress %s: last good check is %v old (max_age %v)", s.Path,
				now.Sub(s.CheckedAt).Round(time.Second), r.maxAge)}
	}
	return nil
}

// vpnOK refuses when ti needs the VPN now (vpnRule) and its route has no fresh good check with a trusted exit.
func (g *Gateway) vpnOK(ti *targetInfo) *Refused {
	r := g.vpnRule(ti)
	if !r.need {
		return nil
	}
	return r.ok(ti, g.now())
}

// familiesIn reports which address families s saw exiting inside set: the first echo's and the other one's.
func familiesIn(s EgressState, set policy.VPNSet) (v4, v6 bool) {
	first, _ := netip.ParseAddr(s.IP)
	for _, a := range []netip.Addr{first, s.other} {
		if a = a.Unmap(); a.IsValid() && set.Contains(a) {
			v4, v6 = v4 || a.Is4(), v6 || !a.Is4()
		}
	}
	return v4, v6
}

// trustedExit reports whether ip is an exit address a check of this generation proves (provenFamilies, for the
// connections its check keeps): in its own VPN set and, for a retired generation an operation is still pinned to, in the current generation's too. A trust change
// (a narrowed VPN set, another check_url) takes effect at once for every generation, not when the retired
// generation's last good check passes max_age: the retired verdict stands only for an exit the current
// settings also accept from the same check endpoint. A current generation without an egress section requires nothing
// more, and neither does a prepared generation not yet activated (its own settings are the new ones). Requests and
// dials follow vpnRule, which also applies the current enforcement and max_age.
func (g *Gateway) trustedExit(ip netip.Addr) bool {
	if g.egress == nil || !g.egress.set.Contains(ip) {
		return false
	}
	cur := g.current()
	if !g.activated.Load() || cur == g || cur == nil || cur.egress == nil || g.egress.sameTrust(cur.egress) {
		return true
	}
	return cur.egress.checkURL == g.egress.checkURL && cur.egress.set.Contains(ip)
}

func (p *egressPath) state() EgressState { return p.check.snapshot() }

// EgressPaths lists the egress path names: "direct" and one per distinct indexer proxy, sorted.
func (g *Gateway) EgressPaths() []string {
	return g.current().egressPathNames()
}

func (g *Gateway) egressPathNames() []string {
	out := make([]string, 0, len(g.paths))
	for n := range g.paths {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// CheckEgress runs one check on every path, in parallel, updates the state and returns the results by path name.
func (g *Gateway) CheckEgress(ctx context.Context) []EgressState {
	return g.forContext(ctx).checkEgress(ctx)
}

func (g *Gateway) checkEgress(ctx context.Context) []EgressState {
	if g.egress == nil {
		return nil
	}
	names := g.egressPathNames()
	out := make([]EgressState, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = g.checkPath(ctx, g.paths[n])
		}()
	}
	wg.Wait()
	return out
}

// RunEgress checks every path now and then every egress.interval until ctx ends. Without an egress section it
// returns at once (the guard is off).
func (g *Gateway) RunEgress(ctx context.Context) {
	g = g.current()
	if g.egress == nil {
		g.log.Warn("VPN guard off: no egress section; requests are not restricted by VPN status")
		return
	}
	g.log.Info("egress guard", "check_url", g.redact.URL(mustURL(g.egress.checkURL)), "interval", g.egress.interval.String(),
		"max_age", g.egress.maxAge.String(), "require_vpn_for", g.egress.classes, "paths", g.EgressPaths())
	t := time.NewTicker(g.egress.interval)
	defer t.Stop()
	for {
		g.CheckEgress(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (g *Gateway) checkPath(ctx context.Context, p *egressPath) EgressState {
	s := EgressState{Path: p.name}
	seq := p.check.begin()
	if g.egress == nil {
		s.Error = "no egress section in the policy"
		s.CheckedAt = g.now()
		return g.setState(p, s, "", seq)
	}
	ip, err := g.echo(ctx, p)
	s.CheckedAt = g.now()
	result := "ok"
	switch {
	case err != nil:
		s.Error, result = g.redact.String(echoError(err)), "error"
	case !g.egress.set.Contains(ip):
		s.IP, s.Error, result = ip.String(), "exit address "+ip.String()+" is not in the VPN set", "not_vpn"
	default:
		s.IP, s.OK = ip.String(), true
		if p.proxy == "" {
			s.other = g.echoOther(ctx, p, ip)
		}
	}
	return g.setState(p, s, result, seq)
}

// echoOther asks the check URL again over the address family first did not use. It returns the address seen when it
// is of that family; an invalid address when the echo fails (no route, an echo endpoint of one family) or answers
// with a first-family address. An address outside the VPN set is returned and logged: provenDial then keeps
// guarded dials off that family.
func (g *Gateway) echoOther(ctx context.Context, p *egressPath, first netip.Addr) netip.Addr {
	network := "tcp4"
	if first.Unmap().Is4() {
		network = "tcp6"
	}
	ip, err := g.echo(context.WithValue(ctx, echoFamilyKey{}, network), p)
	if err != nil || ip.Unmap().Is4() != (network == "tcp4") {
		return netip.Addr{}
	}
	if !g.egress.set.Contains(ip) && p.state().other != ip {
		g.log.Warn("egress: the other address family exits outside the VPN; guarded requests keep to the checked one",
			"path", p.name, "ip", first.String(), "other", ip.String())
	}
	return ip
}

// echoError is the text kept for a failed check. Transport errors are reduced to a safe category; the check's own
// errors (bad status, unparsable answer) are kept.
func echoError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		var r *Refused
		if errors.As(err, &r) {
			return r.Error()
		}
		return safeTransportError(err).Error()
	}
	return err.Error()
}

// setState publishes observation seq unless a newer-started observation is already published; then the newer
// state stays and is returned.
func (g *Gateway) setState(p *egressPath, s EgressState, result string, seq uint64) EgressState {
	p.check.mu.Lock()
	if seq < p.check.applied {
		current := p.check.state
		p.check.mu.Unlock()
		if result != "" {
			g.metrics.egressChecks.WithLabelValues(p.name, "superseded").Inc()
		}
		return current
	}
	p.check.applied = seq
	prev := p.check.state
	p.check.state = s
	p.check.mu.Unlock()
	if result != "" {
		g.metrics.egressChecks.WithLabelValues(p.name, result).Inc()
	}
	if prev.IP != s.IP || prev.other != s.other {
		for _, tr := range p.transports {
			tr.CloseIdleConnections()
		}
	}
	// A check that answered decides which guarded connections may stay, across every generation sharing it: a good
	// one keeps those of the families it proves; one that saw an exit outside the VPN set keeps none (but those
	// that cannot leave the LAN). A failed check proves nothing new: guarded requests are refused until a good one.
	if s.OK || s.IP != "" {
		v4, v6 := false, false
		if s.OK {
			v4, v6 = g.provenFamilies(s)
		}
		n := p.check.revoke(v4, v6)
		// The same path's checks of other generations with other trust settings, older or newer, hold connections
		// dialled on their proofs: this newer observation decides for those too,
		// within each one's own VPN set. A dial registers its connection before it judges the path again
		// (guardedDial), so either it sees this state or this revocation sees its connection.
		for _, o := range p.check.lineage.all() {
			if o == p.check {
				continue
			}
			o4, o6 := familiesIn(s, o.set)
			n += o.revoke(v4 && o4, v6 && o6)
		}
		if n > 0 {
			g.log.Warn("egress: closed guarded connections of an address family the check no longer proves",
				"path", p.name, "connections", n)
		}
	}
	if prev.CheckedAt.IsZero() || prev.OK != s.OK || prev.IP != s.IP || prev.Error != s.Error {
		attrs := []any{"path", s.Path, "vpn_ok", s.OK, "ip", s.IP}
		if s.Error != "" {
			attrs = append(attrs, "error", s.Error)
		}
		if s.OK {
			g.log.Info("egress changed", attrs...)
		} else {
			// An outage, not a bug: the egress health check reports it, and the error inbox records that check.
			g.log.ErrorContext(errinbox.Quiet(context.Background()), "egress changed", attrs...)
		}
	}
	return s
}

// echo asks the check URL which address it sees, through p's own transport.
func (g *Gateway) echo(ctx context.Context, p *egressPath) (netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, g.egress.timeout)
	defer cancel()
	ctx = context.WithValue(ctx, echoKey{}, true)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.egress.checkURL, nil)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("check_url: %w", err)
	}
	req.Header.Set("Accept", "text/plain, application/json")
	start := time.Now()
	// Every echo goes on a new connection: a kept-alive one could still carry the route it was opened on (connmark
	// routing) and report an exit address the path no longer has.
	c := p.client
	if p.echoClient != nil {
		c = p.echoClient
	}
	resp, err := c.Do(req)
	host := strings.ToLower(req.URL.Host)
	g.metrics.latency.WithLabelValues(host).Observe(time.Since(start).Seconds())
	if err != nil {
		g.metrics.requests.WithLabelValues(host, "egress:"+p.name, "error").Inc()
		return netip.Addr{}, err
	}
	defer resp.Body.Close()
	g.metrics.requests.WithLabelValues(host, "egress:"+p.name, fmt.Sprintf("%dxx", resp.StatusCode/100)).Inc()
	if resp.StatusCode != http.StatusOK {
		return netip.Addr{}, fmt.Errorf("check_url answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return netip.Addr{}, err
	}
	return parseEcho(body)
}

// parseEcho reads an IP-echo answer: a bare address, or JSON with an "ip" field.
func parseEcho(body []byte) (netip.Addr, error) {
	s := strings.TrimSpace(string(body))
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap(), nil
	}
	var j struct {
		IP string `json:"ip"`
	}
	if err := json.Unmarshal(body, &j); err == nil && j.IP != "" {
		if a, err := netip.ParseAddr(strings.TrimSpace(j.IP)); err == nil {
			return a.Unmap(), nil
		}
	}
	return netip.Addr{}, fmt.Errorf("check_url answer is not an IP address or JSON with an ip field")
}

// EgressStates returns the latest check on every path, sorted by path.
func (g *Gateway) EgressStates() []EgressState {
	return g.current().egressStates()
}

func (g *Gateway) egressStates() []EgressState {
	names := g.egressPathNames()
	out := make([]EgressState, len(names))
	for i, n := range names {
		out[i] = g.paths[n].state()
	}
	return out
}

// EgressSummary is the egress part of /readyz: whether every path is fresh and good, what each path saw, and which
// request classes need it.
func (g *Gateway) EgressSummary() map[string]any {
	return g.current().egressSummary()
}

// EgressSummaryFor returns the summary for the gateway generation pinned to ctx.
func (g *Gateway) EgressSummaryFor(ctx context.Context) map[string]any {
	return g.forContext(ctx).egressSummary()
}

func (g *Gateway) egressSummary() map[string]any {
	if g.egress == nil {
		return map[string]any{"mode": "off", "type": "warning", "message": "VPN guard off", "vpn_ok": false,
			"require_vpn_for": []string{}, "paths": []map[string]any{}}
	}
	now := g.now()
	all := g.egress != nil
	var paths []map[string]any
	for _, s := range g.egressStates() {
		m := map[string]any{"path": s.Path, "ok": s.OK}
		if s.IP != "" {
			m["ip"] = s.IP
		}
		if s.Error != "" {
			m["error"] = s.Error
		}
		if s.CheckedAt.IsZero() {
			all = false
		} else {
			age := now.Sub(s.CheckedAt)
			m["checked_at"] = s.CheckedAt.UTC().Format(time.RFC3339)
			m["age_s"] = int(age.Seconds())
			if g.egress != nil && age > g.egress.maxAge {
				all = false
				m["stale"] = true
			}
		}
		if !s.OK {
			all = false
		}
		paths = append(paths, m)
	}
	mode := "required"
	if len(g.egress.classes) == 0 {
		mode = "monitor-only"
	}
	out := map[string]any{"mode": mode, "vpn_ok": all, "paths": paths, "require_vpn_for": g.egress.classes}
	return out
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		return &url.URL{}
	}
	return u
}
