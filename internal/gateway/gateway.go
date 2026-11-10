package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"weak"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

// Class is what kind of policy entity a request is for.
type Class string

const (
	ClassIndexer  Class = "indexer"
	ClassProvider Class = "provider"
	ClassMetadata Class = "metadata"
	ClassClient   Class = "client"
	// ClassEgress is the egress check itself; only the gateway uses it.
	ClassEgress Class = "egress"
	// ClassPlex is the Plex server (library.plex); its one target is PlexKey.
	ClassPlex Class = "plex"
	// ClassZurgLog is zurg's /logs endpoint on the LAN (library.mount.zurg_log as a URL); its one target is
	// ZurgLogKey.
	ClassZurgLog Class = "zurglog"
	// ClassZurgAPI is zurg's read-only API on the LAN (library.mount.zurg_api); its one target is ZurgAPIKey.
	ClassZurgAPI Class = "zurgapi"
	// ClassRcloneRC is the mount's rclone remote control on the LAN (library.mount.rclone_rc); its one target is
	// RcloneRCKey.
	ClassRcloneRC Class = "rclonerc"
	// ClassDecypharr is decypharr's read-only surface on the LAN (library.mount.decypharr: the browse API, the
	// per-hash file list and WebDAV listings); its one target is DecypharrKey.
	ClassDecypharr Class = "decypharr"
	// ClassWebhook is an outbound webhook (notify): a policy target by key, or InstalledWebhookKey for the
	// notifications clients install through a view.
	ClassWebhook Class = "webhook"
)

// InstalledWebhookKey is the webhook target of notifications installed through a view (their hosts must be in
// notify.allow_hosts, which joins the allowlist).
const InstalledWebhookKey = "installed"

// PlexKey is the key of the Plex target.
const PlexKey = "server"

// ZurgLogKey is the key of the zurg log target.
const ZurgLogKey = "log"

// ZurgAPIKey is the key of the zurg API target.
const ZurgAPIKey = "api"

// RcloneRCKey is the key of the rclone rc target.
const RcloneRCKey = "rc"

// DecypharrKey is the key of the decypharr target.
const DecypharrKey = "api"

// Target says who a request is for.
type Target struct {
	Class Class
	// Key is the policy key: indexers[Key], providers[Key], metadata.sources[Key] or clients[Key].
	Key string
	// MaxBytes overrides Options.MaxResponseBytes for this request when set.
	MaxBytes int64
	// NoFollow returns a redirect as the response instead of following it: the caller (the cardigann engine) follows
	// it itself, each hop a new Do with every check again.
	NoFollow bool
	// UserAgent replaces theatarr/<version> for this request (a cardigann site that only answers browsers, or the browser
	// FlareSolverr passed a Cloudflare check with).
	UserAgent string
	// Wait, when set, is how long the answer may take: it replaces the 30 s wait for headers and the total timeout
	// (a FlareSolverr solve sends nothing until its browser is done, up to its maxTimeout). A request that runs out
	// of it is not counted as the target's failure.
	Wait time.Duration
}

func (t Target) String() string { return string(t.Class) + ":" + t.Key }

// Options configure New. Zero values get the defaults.
type Options struct {
	// Store persists budget windows and back-offs. Nil keeps them in memory only.
	Store *store.Store
	// Registerer gets the gateway's metrics. Nil registers nothing.
	Registerer prometheus.Registerer
	Logger     *slog.Logger
	// Version goes into the User-Agent, "theatarr/<Version>".
	Version string
	// Timeout bounds a whole request including reading the body. Default 60s.
	Timeout time.Duration
	// MaxResponseBytes caps a response body. Default 32 MiB.
	MaxResponseBytes int64
	// MaxWait is how long a request waits for budget before it is refused instead. Default 60s.
	MaxWait time.Duration
	// DialContext replaces the dialer (tests point host names at local servers with it).
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// LookupIP replaces host name resolution where the gateway checks resolved addresses itself (source probes;
	// tests). Default net.DefaultResolver.
	LookupIP func(ctx context.Context, host string) ([]netip.Addr, error)
	// Now replaces the clock (tests). Default time.Now.
	Now func() time.Time
	// Previous carries live back-offs, budget counters and metrics into a prepared runtime generation.
	Previous *Gateway
	// Generation identifies this gateway in the runtime set when serve publishes it.
	Generation int64
	// Secrets are credentials resolved from DB-backed settings for this runtime. They are masked in logs and errors
	// like the environment-backed ones the policy names.
	Secrets []string

	now func() time.Time // tests in this package; overrides Now
}

// Defaults.
const (
	DefaultTimeout          = 60 * time.Second
	DefaultMaxResponseBytes = 32 << 20
	DefaultMaxWait          = 60 * time.Second
	maxRedirects            = 5
	failuresBeforeBackoff   = 3
	firstFailureBackoff     = time.Minute
	defaultFailureBackoff   = time.Hour
	first429Backoff         = 30 * time.Second
	max429Backoff           = 15 * time.Minute
	maxRetryAfter           = time.Hour
)

// Gateway is the outbound HTTP path. It is safe for concurrent use.
type Gateway struct {
	pol      *policy.Policy
	previous *Gateway
	runtime  *runtimeSet
	// activated: this generation has been the runtime's current one (Activate); once another replaced it, it is
	// retired (trustedExit).
	activated  atomic.Bool
	generation int64
	log        *slog.Logger
	ua         string
	timeout    time.Duration
	maxBody    int64
	maxWait    time.Duration
	allow      allowlist
	redact     *redactor
	state      *state
	// health is where this gateway records failures, rate limits and successes: state, except in a probe of an
	// unsaved candidate (Probe), whose outcomes never touch the live sources' health.
	health   *state
	metrics  *metrics
	dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	lookupIP func(ctx context.Context, host string) ([]netip.Addr, error)
	// probe is set on a source probe's gateway (Probe): what its connections may reach.
	probe *probeAuth
	now   func() time.Time
	sleep func(context.Context, time.Duration) error

	targets map[string]*targetInfo // by Target.String()
	buckets map[string]*bucket
	users   map[string][]string    // budget key -> target names using it, sorted
	paths   map[string]*egressPath // by name; "direct" always exists
	egress  *egressConfig          // nil without an egress section
	// vpnHosts maps the host:port of every VPN-required target to the egress paths those targets use. A redirect
	// into one of them is followed only by a request already guarded on one of those paths.
	vpnHosts map[string]map[*egressPath]bool
}

type vpnTargetKey struct{}

// plainTargetKey carries the target of a request sent without the VPN guard, so each later boundary (redirect, dial,
// hop) can refuse it when the target needs the VPN by then: a request pinned to a retired generation follows the
// current enforcement (vpnRule).
type plainTargetKey struct{}

// noFollowKey marks a request whose redirects come back as the response (Target.NoFollow).
type noFollowKey struct{}

// userAgentKey carries Target.UserAgent to the guard.
type userAgentKey struct{}

// echoKey marks the egress check's own request, which is not VPN-guarded and may only follow redirects that stay
// on the check_url endpoint.
type echoKey struct{}

// dialHostKey carries a checkedDial: a guarded dial that goes to its checked addresses (a probe's dialer judges the
// host by its name and addresses).
type dialHostKey struct{}

// checkedDial is a guarded dial's host name and the addresses provenDial let through.
type checkedDial struct {
	host  string
	addrs []netip.Addr
}

// echoFamilyKey carries the network ("tcp4" or "tcp6") the egress check's second echo is pinned to.
type echoFamilyKey struct{}

type runtimeSet struct {
	current atomic.Pointer[Gateway]

	// Source probes (Probe) of every generation share one probe budget and one candidate health record.
	probeOnce     sync.Once
	probeHealth   *state
	probeBucket   *bucketState
	qualityOnce   sync.Once
	qualityBucket *bucketState
	// The cardigann definitions fetch has a budget of its own, also shared by every generation.
	cardigannOnce   sync.Once
	cardigannBucket *bucketState
	// lineages: every generation's checks of each egress path (egressLineage).
	lineageMu sync.Mutex
	lineages  map[lineageKey]*egressLineage
}

func (g *Gateway) forContext(ctx context.Context) *Gateway {
	if g == nil || g.probe != nil {
		return g // a source probe (Probe) always sends through itself
	}
	if runtime := settings.ContextRuntime(ctx); runtime != nil {
		if active, ok := runtime.Components.(interface{ EgressGateway() *Gateway }); ok && active.EgressGateway() != nil {
			return active.EgressGateway()
		}
	}
	if g.runtime == nil {
		return g
	}
	if active := g.runtime.current.Load(); active != nil {
		return active
	}
	return g
}

func (g *Gateway) current() *Gateway {
	if g == nil || g.runtime == nil || g.probe != nil {
		return g
	}
	if active := g.runtime.current.Load(); active != nil {
		return active
	}
	return g
}

// targetInfo is a target resolved against the policy.
type targetInfo struct {
	name       string // "indexer:dmm"
	class      Class
	key        string
	budget     string
	path       *egressPath
	requireVPN bool
	maxBackoff time.Duration
	// endpoint is the configured URL without query, for deciding who a 429 is about.
	endpoint string
	// limitBudget: a 429 or 503 with Retry-After backs off the whole budget, not just this target (see scopeRateLimits).
	limitBudget bool
}

// New builds the gateway from a validated policy. ctx bounds loading persisted state.
func New(ctx context.Context, p *policy.Policy, o Options) (*Gateway, error) {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Version == "" {
		o.Version = "dev"
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.MaxResponseBytes <= 0 {
		o.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if o.MaxWait <= 0 {
		o.MaxWait = DefaultMaxWait
	}
	if o.now == nil {
		o.now = o.Now
	}
	if o.now == nil {
		o.now = time.Now
	}
	g := &Gateway{
		pol: p, previous: o.Previous, generation: o.Generation, log: o.Logger.With("component", "gateway"), ua: "theatarr/" + o.Version,
		timeout: o.Timeout, maxBody: o.MaxResponseBytes, maxWait: o.MaxWait,
		redact: newRedactor(p, o.Secrets), now: o.now, sleep: sleepCtx, dial: o.DialContext, lookupIP: o.LookupIP,
		targets: map[string]*targetInfo{}, buckets: map[string]*bucket{}, paths: map[string]*egressPath{},
		users: map[string][]string{},
	}
	if o.Previous != nil && o.Previous.runtime != nil {
		g.runtime = o.Previous.runtime
	} else {
		g.runtime = &runtimeSet{}
	}
	if o.Previous != nil {
		g.metrics = o.Previous.metrics
	} else {
		g.metrics = newMetrics(g)
	}
	if o.Previous == nil && o.Registerer != nil {
		if err := g.metrics.register(o.Registerer); err != nil {
			return nil, fmt.Errorf("gateway: metrics: %w", err)
		}
	}
	var err error
	if g.allow, err = newAllowlist(p); err != nil {
		return nil, err
	}
	if o.Previous != nil {
		g.state = o.Previous.state
	} else {
		if g.state, err = newState(ctx, o.Store, g.log, g.now()); err != nil {
			return nil, fmt.Errorf("gateway: loading state: %w", err)
		}
	}
	g.health = g.state
	for k, b := range p.Budgets {
		var previous *bucket
		if o.Previous != nil {
			previous = o.Previous.buckets[k]
		}
		g.buckets[k] = newBucketWithState(k, b, g.now(), previous)
	}
	if g.egress, err = newEgressConfig(p.Egress); err != nil {
		return nil, err
	}

	direct, err := g.newPath("direct", "", o.DialContext)
	if err != nil {
		return nil, err
	}
	add := func(t *targetInfo, proxy string) error {
		t.name = string(t.class) + ":" + t.key
		t.path = direct
		if proxy != "" {
			if t.path = g.paths[proxy]; t.path == nil {
				if t.path, err = g.newPath(proxy, proxy, o.DialContext); err != nil {
					return err
				}
			}
		}
		if t.maxBackoff <= 0 {
			t.maxBackoff = defaultFailureBackoff
		}
		g.targets[t.name] = t
		return nil
	}
	for k, ix := range p.Indexers {
		if err := add(&targetInfo{class: ClassIndexer, key: k, budget: ix.Budget,
			endpoint: endpoint(ix.URL), requireVPN: p.Egress.RequiresVPN(policy.EgressIndexers)}, ix.Proxy); err != nil {
			return nil, err
		}
	}
	for k, pv := range p.Providers {
		if err := add(&targetInfo{class: ClassProvider, key: k, budget: pv.Budget, maxBackoff: pv.Pacing.BackoffMax.D(),
			endpoint:   endpoint(pv.URL),
			requireVPN: p.Egress.RequiresVPN(policy.EgressProviders)}, ""); err != nil {
			return nil, err
		}
	}
	if p.Metadata != nil {
		for k, s := range p.Metadata.Sources {
			if err := add(&targetInfo{class: ClassMetadata, key: k, budget: s.Budget, endpoint: endpoint(s.URL),
				requireVPN: p.Egress.RequiresVPN(policy.EgressMetadata)}, ""); err != nil {
				return nil, err
			}
		}
	}
	for k := range p.Clients {
		if err := add(&targetInfo{class: ClassClient, key: k}, ""); err != nil {
			return nil, err
		}
	}
	if p.Library != nil && p.Library.Plex != nil {
		if err := add(&targetInfo{class: ClassPlex, key: PlexKey, budget: p.Library.Plex.Budget,
			endpoint: endpoint(p.Library.Plex.URL)}, ""); err != nil {
			return nil, err
		}
	}
	if p.Library != nil && policy.ZurgLogIsURL(p.Library.Mount.ZurgLog) {
		if err := add(&targetInfo{class: ClassZurgLog, key: ZurgLogKey, budget: p.Library.Mount.ZurgLogBudget,
			endpoint: endpoint(p.Library.Mount.ZurgLog)}, ""); err != nil {
			return nil, err
		}
	}
	if p.Library != nil && p.Library.Mount.ZurgAPI != "" {
		if err := add(&targetInfo{class: ClassZurgAPI, key: ZurgAPIKey, budget: p.Library.Mount.ZurgAPIBudget,
			endpoint: endpoint(p.Library.Mount.ZurgAPI)}, ""); err != nil {
			return nil, err
		}
	}
	if p.Library != nil && p.Library.Mount.Decypharr != nil {
		if err := add(&targetInfo{class: ClassDecypharr, key: DecypharrKey, budget: p.Library.Mount.Decypharr.Budget,
			endpoint: endpoint(p.Library.Mount.Decypharr.URL)}, ""); err != nil {
			return nil, err
		}
	}
	if p.Library != nil && p.Library.Mount.RcloneRC != nil {
		if err := add(&targetInfo{class: ClassRcloneRC, key: RcloneRCKey, budget: p.Library.Mount.RcloneRC.Budget,
			endpoint: endpoint(p.Library.Mount.RcloneRC.URL)}, ""); err != nil {
			return nil, err
		}
	}
	if p.Notify != nil {
		for k, t := range p.Notify.Targets {
			if err := add(&targetInfo{class: ClassWebhook, key: k, budget: p.Notify.Budget, endpoint: endpoint(t.URL)},
				""); err != nil {
				return nil, err
			}
		}
		if err := add(&targetInfo{class: ClassWebhook, key: InstalledWebhookKey, budget: p.Notify.Budget}, ""); err != nil {
			return nil, err
		}
	}
	g.scopeRateLimits()
	g.vpnHosts = map[string]map[*egressPath]bool{}
	for _, t := range g.targets {
		if u, err := url.Parse(t.endpoint); t.requireVPN && err == nil && u.Host != "" {
			hop := hostPort(u)
			if g.vpnHosts[hop] == nil {
				g.vpnHosts[hop] = map[*egressPath]bool{}
			}
			g.vpnHosts[hop][t.path] = true
		}
	}
	if o.Previous == nil {
		g.Activate()
	}
	g.previous = nil
	return g, nil
}

// RegisterMetrics registers a successfully prepared startup gateway.
func (g *Gateway) RegisterMetrics(reg prometheus.Registerer) error {
	if reg == nil {
		return nil
	}
	return g.metrics.register(reg)
}

// Activate makes this prepared generation the metrics collector's current gateway.
func (g *Gateway) Activate() {
	g.activated.Store(true)
	g.runtime.current.Store(g)
	g.metrics.current.Store(g)
}

// scopeRateLimits decides, per target, whether a 429 (or a 503 with Retry-After) is about the target or about its
// whole budget. The budget is backed off when the limit is clearly the account's or key's:
//   - the budget has one user, so the two are the same;
//   - the target is a debrid provider: RD, AD and TorBox count their limits per account or key, and TorBox's 429
//     locks the whole account;
//   - every user of the budget calls the same endpoint (URL without query), so the server can't tell them apart.
//
// Otherwise the budget is shared by targets with their own endpoints, such as Prowlarr's per-indexer feeds
// (/1/api, /7/api): a 429 there is that indexer's limit, so only the target backs off and the others keep going.
func (g *Gateway) scopeRateLimits() {
	byBudget := map[string][]*targetInfo{}
	for _, t := range g.targets {
		if t.budget != "" {
			byBudget[t.budget] = append(byBudget[t.budget], t)
		}
	}
	for b, ts := range byBudget {
		same := true
		for _, t := range ts {
			same = same && t.endpoint == ts[0].endpoint
			g.users[b] = append(g.users[b], t.name)
		}
		sort.Strings(g.users[b])
		for _, t := range ts {
			t.limitBudget = len(ts) == 1 || same || t.class == ClassProvider || b == "dmm-key"
		}
	}
}

// endpoint normalises a configured URL for comparison: scheme, lower-case host and path without a trailing slash.
func endpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimSuffix(u.Path, "/")
}

// Hosts lists the allowlist, sorted (for logs).
func (g *Gateway) Hosts() []string { return g.current().allow.list() }

// Do sends req for t through every guard (see the package doc) and returns the response. The caller closes the body.
// A refusal is a *Refused; transport errors come back as *url.Error with the URL redacted.
func (g *Gateway) Do(ctx context.Context, t Target, req *http.Request) (*http.Response, error) {
	return g.forContext(ctx).do(ctx, t, req)
}

func (g *Gateway) do(ctx context.Context, t Target, req *http.Request) (*http.Response, error) {
	ti := g.targets[t.String()]
	if ti == nil {
		return nil, g.refuse(t.Class, t.String(), req, &Refused{Reason: ReasonUnknownTarget, Target: t.String(),
			Detail: "not in the policy"})
	}
	if err := g.vpnOK(ti); err != nil {
		return nil, g.refuse(ti.class, ti.name, req, err)
	}
	if err := g.allow.check(req.URL); err != nil {
		err.Target = ti.name
		return nil, g.refuse(ti.class, ti.name, req, err)
	}
	now := g.now()
	if until, why, ok := g.backedOff(ti.name, now); ok {
		if !g.beginHealthCheck(ctx, ti.name, now) {
			return nil, g.refuse(ti.class, ti.name, req, &Refused{Reason: ReasonBackoff, Target: ti.name,
				Detail: "backing off: " + why, RetryAt: until})
		}
		defer g.health.endCheck(ti.name)
		g.log.Info("health check through the failure back-off", "target", ti.name, "backoff", g.redact.String(why))
	}
	waited, err := g.acquire(ctx, ti)
	if err != nil {
		var r *Refused
		if errors.As(err, &r) {
			return nil, g.refuse(ti.class, ti.name, req, r)
		}
		return nil, err
	}
	if r := g.vpnRule(ti); r.need {
		if err := r.ok(ti, g.now()); err != nil {
			return nil, g.refuse(ti.class, ti.name, req, err)
		}
		ctx = context.WithValue(ctx, vpnTargetKey{}, ti)
	} else {
		ctx = context.WithValue(ctx, plainTargetKey{}, ti)
	}
	return g.send(ctx, ti, t, req, waited)
}

// Get is Do for a plain GET.
func (g *Gateway) Get(ctx context.Context, t Target, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errors.New("gateway: bad URL") // the URL may hold a key: don't echo it
	}
	return g.Do(ctx, t, req)
}

// healthCheckKey marks a health check (HealthCheck).
type healthCheckKey struct{}

// HealthCheck marks the requests sent with the returned context as health checks of their target: whether it
// answers at all while some of its requests keep failing. A health check goes through the target's back-off for
// failures in a row, one at a time per target, so a caller can tell a failing resource (one torrent) from a failing
// target (the provider) even after the resource's failures backed the target off. Everything else still applies:
// budgets and their caps, a rate limit or Retry-After back-off, an out-of-band back-off (BackOff), and every guard.
// Its outcome counts like any request's: a success ends the failure back-off, a failure extends it.
func HealthCheck(ctx context.Context) context.Context {
	return context.WithValue(ctx, healthCheckKey{}, true)
}

// beginHealthCheck reports whether a request backed off on subject may go through as a health check, and claims the
// subject's one check in flight (the caller releases it with health.endCheck).
func (g *Gateway) beginHealthCheck(ctx context.Context, subject string, now time.Time) bool {
	if ctx.Value(healthCheckKey{}) == nil || !g.state.streakOnly(subject, now) || !g.health.streakOnly(subject, now) {
		return false
	}
	return g.health.beginCheck(subject)
}

// BackOff backs a target off until until (for callers that learn about limits out of band, e.g. decypharr's
// too_many_active_downloads).
func (g *Gateway) BackOff(ctx context.Context, t Target, until time.Time, reason string) {
	g = g.forContext(ctx)
	g.state.setBackoff(ctx, t.String(), until, reason, false, g.now())
	g.log.Warn("back-off set", "subject", t.String(), "until", until.UTC(), "reason", reason)
}

// backedOff reports an active back-off on subject: the live one, and for a candidate probe also its own.
func (g *Gateway) backedOff(subject string, now time.Time) (time.Time, string, bool) {
	if until, why, ok := g.state.active(subject, now); ok {
		return until, why, ok
	}
	if g.health != g.state {
		return g.health.active(subject, now)
	}
	return time.Time{}, "", false
}

func (g *Gateway) refuse(class Class, target string, req *http.Request, r *Refused) error {
	g.metrics.refused.WithLabelValues(target, string(r.Reason)).Inc()
	g.log.Warn("request refused", "target", target, "url", g.redact.TargetURL(class, req.URL), "reason", r.Reason,
		"detail", g.redact.String(r.Detail))
	return r
}

func (g *Gateway) send(ctx context.Context, ti *targetInfo, t Target, req *http.Request, waited time.Duration) (*http.Response, error) {
	if t.NoFollow {
		ctx = context.WithValue(ctx, noFollowKey{}, true)
	}
	if t.UserAgent != "" {
		ctx = context.WithValue(ctx, userAgentKey{}, t.UserAgent)
	}
	client, slow := ti.path.client, ti.path.slow
	if _, guarded := ctx.Value(vpnTargetKey{}).(*targetInfo); guarded && ti.path.guarded != nil {
		client, slow = ti.path.guarded, ti.path.guardedSlow
	}
	cancel := context.CancelFunc(func() {})
	if t.Wait > 0 {
		ctx, cancel = context.WithTimeout(ctx, t.Wait)
		client = slow
	}
	hops := new(atomic.Int32)
	ctx = context.WithValue(ctx, hopsKey{}, hops)
	req = req.WithContext(ctx)
	host := strings.ToLower(req.URL.Host)
	sent := g.health.mark()
	start := time.Now()
	resp, err := client.Do(req)
	took := time.Since(start)
	now := g.now()
	g.countRequest(ctx, ti, int(hops.Load()))
	bg := context.WithoutCancel(ctx)
	attrs := []any{"target", ti.name, "method", req.Method, "url", g.redact.TargetURL(ti.class, req.URL),
		"egress", ti.path.name, "took_ms", took.Milliseconds()}
	if waited > 0 {
		attrs = append(attrs, "budget_wait_ms", waited.Milliseconds())
	}
	g.metrics.latency.WithLabelValues(host).Observe(took.Seconds())
	if err != nil {
		cancel()
		var r *Refused
		if errors.As(err, &r) { // a redirect hop refused by the transport
			r.Target = ti.name
			g.metrics.refused.WithLabelValues(ti.name, string(r.Reason)).Inc()
			g.log.Warn("request refused", append(attrs, "reason", r.Reason, "detail", g.redact.String(r.Detail))...)
			return nil, &url.Error{Op: urlErrorOp(req.Method), URL: g.redact.TargetURL(ti.class, req.URL), Err: r}
		}
		// Nested transport errors can quote server-supplied text, such as a malformed redirect Location holding a
		// credential: only a safe category is returned or logged.
		err = &url.Error{Op: urlErrorOp(req.Method), URL: g.redact.TargetURL(ti.class, req.URL), Err: safeTransportError(err)}
		g.metrics.requests.WithLabelValues(host, ti.name, "error").Inc()
		g.log.Warn("request failed", append(attrs, "err", g.redact.String(err.Error()))...)
		if ctx.Err() == nil {
			g.targetFailure(bg, ti, "transport error", now)
		}
		return nil, err
	}
	status := strconv.Itoa(resp.StatusCode/100) + "xx"
	g.metrics.requests.WithLabelValues(host, ti.name, status).Inc()
	attrs = append(attrs, "code", resp.StatusCode)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get("Retry-After") != ""):
		g.rateLimited(bg, ti, resp, now)
		g.log.Warn("rate limited", attrs...)
	case resp.StatusCode >= 500:
		g.targetFailure(bg, ti, "HTTP "+strconv.Itoa(resp.StatusCode), now)
		g.log.Warn("request", attrs...)
	default:
		g.health.success(bg, ti.name, sent)
		if ti.budget != "" && resp.StatusCode < 400 {
			g.health.success(bg, "budget:"+ti.budget, sent)
		}
		g.log.Debug("request", attrs...)
	}
	limit := g.maxBody
	if t.MaxBytes > 0 {
		limit = t.MaxBytes
	}
	if resp.ContentLength > limit {
		resp.Body.Close()
		cancel()
		g.metrics.refused.WithLabelValues(ti.name, string(ReasonTooLarge)).Inc()
		return nil, &Refused{Reason: ReasonTooLarge, Target: ti.name,
			Detail: fmt.Sprintf("Content-Length %d over the %d byte limit", resp.ContentLength, limit)}
	}
	resp.Body = &limitedBody{rc: resp.Body, left: limit, target: ti.name, g: g, cancel: cancel}
	return resp, nil
}

// rateLimited backs off the target's budget, or the target alone, after a 429 or a 503 with Retry-After (see
// scopeRateLimits for which).
func (g *Gateway) rateLimited(ctx context.Context, ti *targetInfo, resp *http.Response, now time.Time) {
	subject := ti.name
	if ti.budget != "" && ti.limitBudget {
		subject = "budget:" + ti.budget
		g.metrics.budgetBackoffs.WithLabelValues(ti.budget).Inc()
	} else {
		g.metrics.targetBackoffs.WithLabelValues(ti.name).Inc()
	}
	d, ok := parseRetryAfter(resp.Header.Get("Retry-After"), now)
	reason := fmt.Sprintf("HTTP %d", resp.StatusCode)
	if ok {
		d = min(d, maxRetryAfter)
		reason += ", Retry-After " + d.String()
	} else {
		n := g.health.failures(subject)
		d = min(first429Backoff<<min(n, 10), max429Backoff)
	}
	g.health.setBackoff(ctx, subject, now.Add(d), reason, true, now)
	g.log.Warn("back-off", "subject", subject, "for", d.String(), "reason", reason)
}

// targetFailure counts a failure and backs the target off after failuresBeforeBackoff in a row.
func (g *Gateway) targetFailure(ctx context.Context, ti *targetInfo, reason string, now time.Time) {
	n := g.health.failure(ctx, ti.name, reason, now)
	if n < failuresBeforeBackoff {
		return
	}
	d := min(firstFailureBackoff<<min(n-failuresBeforeBackoff, 10), ti.maxBackoff)
	g.health.failureBackoff(ctx, ti.name, now.Add(d), fmt.Sprintf("%d"+streakReason+"%s", n, reason), now)
	g.log.Warn("back-off", "subject", ti.name, "for", d.String(), "failures", n, "reason", reason)
}

func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if s, err := strconv.Atoi(v); err == nil {
		if s < 0 {
			return 0, false
		}
		return time.Duration(s) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

// limitedBody fails a read past the limit with ErrResponseTooLarge instead of truncating silently.
type limitedBody struct {
	rc     io.ReadCloser
	left   int64
	target string
	g      *Gateway
	once   sync.Once
	cancel context.CancelFunc // ends a Wait request's own deadline once the body is closed
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		// One more byte decides between a body of exactly the limit and one over it.
		var one [1]byte
		n, err := b.rc.Read(one[:])
		if n > 0 {
			b.once.Do(func() { b.g.metrics.refused.WithLabelValues(b.target, string(ReasonTooLarge)).Inc() })
			return 0, &Refused{Reason: ReasonTooLarge, Target: b.target, Detail: "body over the size limit"}
		}
		return 0, err
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	return n, err
}

// Write writes to an upgraded connection (a 101 response's body is its read and write side): a websocket client's
// control frames. Any other body refuses it.
func (b *limitedBody) Write(p []byte) (int, error) {
	if w, ok := b.rc.(io.Writer); ok {
		return w.Write(p)
	}
	return 0, errors.ErrUnsupported
}

func (b *limitedBody) Close() error {
	err := b.rc.Close()
	if b.cancel != nil {
		b.cancel()
	}
	return err
}

// egressPath is one way out: direct, or through an indexer's proxy. Each has its own transport and its own egress
// check state.
type egressPath struct {
	name   string
	proxy  string
	client *http.Client
	// slow is client without the header and total timeouts, for a Target with Wait (its own deadline bounds it).
	slow *http.Client
	// guarded and guardedSlow are client and slow with pools of their own, for VPN-guarded requests on the direct path
	// (nil through a proxy): a guarded request never reuses a connection an unguarded one opened, whose family no
	// check judged.
	guarded, guardedSlow *http.Client
	// echoClient is client without kept-alive connections, for every egress echo (a fresh route each time; the second
	// echo is pinned to one family by its dial).
	echoClient *http.Client
	check      *egressCheck
	// transports are those of client, slow, guarded and guardedSlow, whose idle connections close when the observed exit address changes. The
	// guarded connections are also tracked on check, which closes them, busy ones too, in every generation, once it
	// stops proving their family (revoke): a kept-alive connection must not outlive the check that allowed it.
	transports []*http.Transport
}

// egressCheck is one path's check state. Successive runtime generations share it, and periodic, immediate and
// per-generation checks all order their results through it.
type egressCheck struct {
	mu    sync.Mutex
	state EgressState
	// started numbers observations as they begin; applied is the newest one whose result is in state. An
	// observation that started before the applied one is discarded, so a late answer never replaces a newer result.
	started, applied uint64
	// conns are the open guarded connections of every generation sharing this check (the direct path only), so a
	// check that stops proving a family closes them all, idle or busy (revoke).
	conns map[*provenConn]struct{}
	// set is the VPN set of the generations sharing this check (one trust configuration; nil without egress).
	set policy.VPNSet
	// lineage holds every generation's check of the same path (name and proxy), this one included: an answer here
	// also revokes the connections dialled on their proofs, and their newer observations override this one's older
	// verdict (setState, vpnRule.proof). Set once, when the path is built.
	lineage *egressLineage
}

// egressLineage is the set of checks, one per trust configuration, that successive generations kept of one egress
// path. It holds them weakly: a check goes when no generation, connection or pinned operation holds it any more.
type egressLineage struct {
	mu     sync.Mutex
	checks []weak.Pointer[egressCheck]
}

// lineageKey names an egress path across generations.
type lineageKey struct{ name, proxy string }

// lineage returns the runtime's lineage of the path name through proxy.
func (rs *runtimeSet) lineage(name, proxy string) *egressLineage {
	rs.lineageMu.Lock()
	defer rs.lineageMu.Unlock()
	if rs.lineages == nil {
		rs.lineages = map[lineageKey]*egressLineage{}
	}
	k := lineageKey{name, proxy}
	l := rs.lineages[k]
	if l == nil {
		l = &egressLineage{}
		rs.lineages[k] = l
	}
	return l
}

func (l *egressLineage) add(c *egressCheck) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.checks = append(l.checks, weak.Make(c))
	c.lineage = l
}

// all returns the lineage's live checks (none for a nil lineage).
func (l *egressLineage) all() []*egressCheck {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]*egressCheck, 0, len(l.checks))
	live := l.checks[:0]
	for _, w := range l.checks {
		if c := w.Value(); c != nil {
			out = append(out, c)
			live = append(live, w)
		}
	}
	clear(l.checks[len(live):])
	l.checks = live
	return out
}

// provenConn is a guarded direct connection, with the address family it was dialled over.
type provenConn struct {
	net.Conn
	check *egressCheck
	// v4 is the family; local: the address cannot leave the LAN, so no check's verdict concerns it.
	v4, local bool
	once      sync.Once
}

func (c *provenConn) Close() error {
	c.once.Do(func() {
		c.check.mu.Lock()
		delete(c.check.conns, c)
		c.check.mu.Unlock()
	})
	return c.Conn.Close()
}

// allowedBy reports whether a check proving families v4 and v6 still allows c.
func (c *provenConn) allowedBy(v4, v6 bool) bool {
	return c.local || (c.v4 && v4) || (!c.v4 && v6)
}

// guardedDial dials a guarded request on the direct path: only addresses provenDial checked, and the connection is
// tracked on the proving check so a later check that no longer proves its family closes it. Once the
// connection is registered the path is judged again under the rule in force then (vpnRule, not the one the dial
// started with: a generation activated during the dial adds its check and its settings): a check
// that publishes after that judgement revokes the registered connection (setState), one that published before it is
// seen by it.
func (g *Gateway) guardedDial(ctx context.Context, pr proof, dial func(context.Context, string, string) (net.Conn, error),
	ti *targetInfo, network, addr string) (net.Conn, error) {
	network, addrs, err := g.provenDial(ctx, pr, ti, network, addr)
	if err != nil {
		return nil, err
	}
	var c net.Conn
	pc := &provenConn{check: pr.check}
	if len(addrs) > 0 {
		host, port, _ := net.SplitHostPort(addr)
		var a netip.Addr
		c, a, err = dialAddrs(context.WithValue(ctx, dialHostKey{}, checkedDial{host: host, addrs: addrs}), dial, addrs, port)
		pc.v4, pc.local = a.Is4(), localAddr(a)
	} else {
		c, err = dial(ctx, network, addr)
		if err == nil {
			pc.v4 = network == "tcp4"
			if network != "tcp4" && network != "tcp6" {
				if ap, perr := netip.ParseAddrPort(c.RemoteAddr().String()); perr == nil {
					pc.v4 = ap.Addr().Unmap().Is4()
				}
			}
		}
	}
	if err != nil {
		return nil, err
	}
	pc.Conn = c
	check := pr.check
	check.mu.Lock()
	if check.conns == nil {
		check.conns = map[*provenConn]struct{}{}
	}
	check.conns[pc] = struct{}{}
	check.mu.Unlock()
	if err := g.rejudge(ti, pc.allowedBy); err != nil {
		pc.Close()
		return nil, err
	}
	return pc, nil
}

// rejudge refuses a connection dialled for ti unless the rule in force now still proves its route; allowed (nil
// for a proxied connection, which is not tracked by family) says whether the proven families still cover it.
func (g *Gateway) rejudge(ti *targetInfo, allowed func(v4, v6 bool) bool) *Refused {
	r := g.vpnRule(ti)
	if !r.need {
		return nil
	}
	pr, err := r.proof(ti, g.now())
	if err != nil {
		return err
	}
	if allowed != nil && !allowed(pr.v4, pr.v6) {
		return &Refused{Reason: ReasonVPN, Target: ti.name, Detail: "the egress check stopped proving the address family of the connection"}
	}
	return nil
}

// revoke closes every tracked connection that families v4 and v6 no longer allow, in every generation sharing the
// check. Closing the connection itself, not only the transport's idle pool, also ends a busy HTTP/2 connection,
// which would otherwise take new streams over a family the check no longer proves.
func (c *egressCheck) revoke(v4, v6 bool) int {
	c.mu.Lock()
	var gone []*provenConn
	for pc := range c.conns {
		if !pc.allowedBy(v4, v6) {
			gone = append(gone, pc)
		}
	}
	c.mu.Unlock()
	for _, pc := range gone {
		pc.Close()
	}
	return len(gone)
}

// begin numbers a new observation of this path.
func (c *egressCheck) begin() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.started++
	return c.started
}

// provenFamilies reports which address families s proves for a guarded direct dial: the first echo's family, and
// the other one while its echo exits inside the VPN set. A state that is not OK proves none.
func (g *Gateway) provenFamilies(s EgressState) (v4, v6 bool) {
	ip, err := netip.ParseAddr(s.IP)
	if err != nil || !s.OK || !g.trustedExit(ip) {
		return false, false
	}
	ip = ip.Unmap()
	v4, v6 = ip.Is4(), !ip.Is4()
	if o := s.other.Unmap(); o.IsValid() && g.trustedExit(o) {
		v4, v6 = v4 || o.Is4(), v6 || !o.Is4()
	}
	return v4, v6
}

// localAddr reports whether a cannot leave the LAN: a private, loopback or link-local address.
func localAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast()
}

// provenDial narrows a guarded direct dial to what the egress check proved. The check proves the exit of a family
// only by an echo over it (an IPv4-only echo such as api.ipify.org, or a VPN that routes one family): a dual-stack
// dial could otherwise reach an indexer over the other family, outside the tunnel. The direct path's check echoes over
// each family (checkPath), so a dual-stack VPN proves both.
//
// The host is resolved once here and the addresses that pass are the ones dialled: the dialer never
// resolves the name again, so a changed DNS answer cannot swap in an address that was not checked. An address passes
// when its family is proven, or when it is private, loopback or link-local and so cannot leave the LAN over either
// family (a Prowlarr feed, trawl, a LAN Zilean): the local exception is judged on each address actually dialled. A
// host with no passing address is refused (ReasonVPN, no back-off) rather than failing to dial. An
// unresolvable host keeps the first family's pin and no addresses: the dial then goes to the name and fails as it
// would anyway, and whatever it reaches is of a proven family. Through a proxy the dial reaches the proxy, whose own
// onward route the check covers.
func (g *Gateway) provenDial(ctx context.Context, pr proof, ti *targetInfo, network, addr string) (string, []netip.Addr, error) {
	if network != "tcp" {
		return network, nil, nil
	}
	ip, err := netip.ParseAddr(pr.state.IP)
	if err != nil {
		return network, nil, nil
	}
	v4, v6 := pr.v4, pr.v6
	pin := "tcp6"
	if ip.Unmap().Is4() {
		pin = "tcp4"
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return pin, nil, nil
	}
	ips, err := g.lookupHost(ctx, host)
	if err != nil || len(ips) == 0 {
		return pin, nil, nil
	}
	var ok []netip.Addr
	for _, a := range ips {
		u := a.Unmap()
		if localAddr(u) || (u.Is4() && v4) || (!u.Is4() && v6) {
			ok = append(ok, u)
		}
	}
	if len(ok) > 0 {
		return network, ok, nil
	}
	// Only one family is proven here: with both, any address would have passed.
	proven, missing := "IPv4", "IPv6"
	if v6 {
		proven, missing = "IPv6", "IPv4"
	}
	return "", nil, &Refused{Reason: ReasonVPN, Target: ti.name, Detail: "host " + host + " has only " + missing +
		" addresses; the egress check proved only " + proven}
}

// dialStagger is how long a guarded dial waits on one checked address before it also tries the next (RFC 8305's
// connection attempt delay; net.Dialer races the families 300 ms apart).
var dialStagger = 250 * time.Millisecond

// dialAddrs connects to the first of addrs that answers, each with the network of its own family, on port. A guarded
// dial uses it so the connection goes to an address provenDial checked, not to a fresh resolution of the name; the
// request's URL still carries the name, so the Host header and the TLS server name are unchanged. The attempts are
// staggered in order: the next address is tried when the one before fails or has not answered within
// dialStagger, so an unreachable leading address does not hold back a reachable one for a whole dial timeout. Only
// the checked addresses are ever dialled; the first connection wins and the others are cancelled or closed.
func dialAddrs(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), addrs []netip.Addr,
	port string) (net.Conn, netip.Addr, error) {
	type result struct {
		c   net.Conn
		a   netip.Addr
		err error
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan result, len(addrs))
	next, running := 0, 0
	attempt := func() bool {
		if next >= len(addrs) || ctx.Err() != nil {
			return false
		}
		a := addrs[next].Unmap()
		next++
		running++
		network := "tcp6"
		if a.Is4() {
			network = "tcp4"
		}
		go func() {
			c, err := dial(ctx, network, net.JoinHostPort(a.String(), port))
			results <- result{c, a, err}
		}()
		return true
	}
	if !attempt() {
		if err := ctx.Err(); err != nil {
			return nil, netip.Addr{}, err
		}
		return nil, netip.Addr{}, errors.New("no address to dial")
	}
	timer := time.NewTimer(dialStagger)
	defer timer.Stop()
	var errs []error
	for running > 0 {
		select {
		case r := <-results:
			running--
			if r.err == nil {
				cancel()
				if running > 0 {
					go func(n int) { // a slower attempt that still connects is closed
						for range n {
							if late := <-results; late.c != nil {
								late.c.Close()
							}
						}
					}(running)
				}
				return r.c, r.a, nil
			}
			errs = append(errs, r.err)
			if attempt() {
				timer.Reset(dialStagger)
			}
		case <-timer.C:
			if attempt() {
				timer.Reset(dialStagger)
			}
		}
	}
	if err := ctx.Err(); err != nil && len(errs) == 0 {
		return nil, netip.Addr{}, err
	}
	return nil, netip.Addr{}, errors.Join(errs...)
}

// lookupHost resolves a dial's host (an IP literal stands for itself).
func (g *Gateway) lookupHost(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return []netip.Addr{ip}, nil
	}
	if g.lookupIP != nil {
		return g.lookupIP(ctx, host)
	}
	if g.dial != nil {
		// An injected dialer (tests) resolves names its own way: the system resolver would not agree with it.
		return nil, errors.New("no resolver for an injected dialer")
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// responseHeaderTimeout is how long a request waits for the answer's headers (a Target's Wait replaces it).
var responseHeaderTimeout = 30 * time.Second

func (g *Gateway) newPath(name, proxy string, dial func(context.Context, string, string) (net.Conn, error)) (*egressPath, error) {
	if dial == nil {
		dial = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	p := &egressPath{name: name, proxy: proxy, check: &egressCheck{state: EgressState{Path: name}}}
	tr := &http.Transport{
		// Proxy stays nil for the direct path: HTTP_PROXY and friends in the environment are ignored, so the route
		// is what the policy says.
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if ti, ok := ctx.Value(vpnTargetKey{}).(*targetInfo); ok {
				if r := g.vpnRule(ti); r.need {
					pr, err := r.proof(ti, g.now())
					if err != nil {
						return nil, err
					}
					if proxy == "" {
						return g.guardedDial(ctx, pr, dial, ti, network, addr)
					}
					// Through a proxy the connection is not tracked by family; still judge the path again once it
					// is open, so an observation published during the dial stops it.
					c, derr := dial(ctx, network, addr)
					if derr != nil {
						return nil, derr
					}
					if err := g.rejudge(ti, nil); err != nil {
						c.Close()
						return nil, err
					}
					return c, nil
				}
			} else if ti, ok := ctx.Value(plainTargetKey{}).(*targetInfo); ok {
				if err := g.vpnLate(ti); err != nil {
					return nil, err
				}
			} else if n, ok := ctx.Value(echoFamilyKey{}).(string); ok && network == "tcp" {
				network = n
			}
			return dial(ctx, network, addr)
		},
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   4,
	}
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("gateway: proxy %q: %w", proxy, err)
		}
		tr.Proxy = http.ProxyURL(u)
	}
	p.client = &http.Client{
		Transport: &guard{base: tr, g: g, proxied: proxy != ""},
		Timeout:   g.timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) (err error) {
			defer func() {
				var refused *Refused
				if errors.As(err, &refused) {
					refused.Sent = true
				}
			}()
			if req.Context().Value(noFollowKey{}) != nil {
				return http.ErrUseLastResponse
			}
			ti, guarded := req.Context().Value(vpnTargetKey{}).(*targetInfo)
			if guarded {
				if err := g.vpnOK(ti); err != nil {
					return err
				}
			} else if plain, ok := req.Context().Value(plainTargetKey{}).(*targetInfo); ok {
				if err := g.vpnLate(plain); err != nil {
					return err
				}
			}
			if err := g.checkRedirectTarget(req, via, ti, guarded); err != nil {
				return err
			}
			if len(via) >= maxRedirects {
				return errTooManyRedirects
			}
			return nil
		},
	}
	slowTr := tr.Clone()
	slowTr.ResponseHeaderTimeout = 0
	slow := *p.client
	slow.Transport, slow.Timeout = &guard{base: slowTr, g: g, proxied: proxy != ""}, 0
	p.slow = &slow
	p.transports = []*http.Transport{tr, slowTr}
	if proxy == "" {
		gTr, gSlowTr := tr.Clone(), slowTr.Clone()
		guarded, guardedSlow := *p.client, slow
		guarded.Transport = &guard{base: gTr, g: g}
		guardedSlow.Transport = &guard{base: gSlowTr, g: g}
		p.guarded, p.guardedSlow = &guarded, &guardedSlow
		p.transports = append(p.transports, gTr, gSlowTr)
	}
	// The second echo is pinned to one family by its dial: it must not reuse a connection of the first.
	echoTr := tr.Clone()
	echoTr.DisableKeepAlives = true
	echo := *p.client
	echo.Transport = &guard{base: echoTr, g: g, proxied: proxy != ""}
	p.echoClient = &echo
	if g.egress != nil {
		p.check.set = g.egress.set
	}
	if old := g.previousPath(name, proxy); old != nil && g.previous.egress.sameTrust(g.egress) {
		p.check = old.check
	} else {
		g.runtime.lineage(name, proxy).add(p.check)
	}
	g.paths[name] = p
	return p, nil
}

// checkRedirectTarget classifies a redirect hop by its destination. A quality sync fetch follows none. The egress check may only stay on its check_url
// endpoint. A hop into a VPN-required target's host is refused unless the request is already VPN-guarded on one of
// that target's egress paths (or the hop stays on the request's own host), so an unguarded request, or one on
// another transport, never carries traffic to a protected indexer.
func (g *Gateway) checkRedirectTarget(req *http.Request, via []*http.Request, ti *targetInfo, guarded bool) *Refused {
	hop := hostPort(req.URL)
	if req.Context().Value(noRedirectKey{}) != nil {
		return &Refused{Reason: ReasonHost, Detail: "redirect to " + hop + " refused: this request follows no redirects"}
	}
	if req.Context().Value(echoKey{}) != nil {
		if g.egress != nil && hop == hostPort(mustURL(g.egress.checkURL)) {
			return nil
		}
		return &Refused{Reason: ReasonHost, Target: string(ClassEgress), Detail: "egress check redirected off its check_url endpoint"}
	}
	paths, protected := g.vpnHosts[hop]
	// A generation still pinned after another replaced it also keeps out of the hosts the current settings protect,
	// matching the paths by name and proxy.
	var curPaths map[*egressPath]bool
	if cur := g.current(); g.activated.Load() && cur != nil && cur != g {
		curPaths = cur.vpnHosts[hop]
	}
	if (!protected && curPaths == nil) || (len(via) > 0 && hop == hostPort(via[0].URL)) {
		return nil
	}
	ok := guarded
	if protected {
		ok = ok && paths[ti.path]
	}
	if curPaths != nil {
		ok = ok && samePath(curPaths, ti.path)
	}
	if ok {
		return nil
	}
	return &Refused{Reason: ReasonVPN, Detail: "redirect to " + hop + ", a VPN-required target, from a request not guarded on its egress path"}
}

// samePath reports whether paths holds a path with p's name and proxy (paths of another generation).
func samePath(paths map[*egressPath]bool, p *egressPath) bool {
	for q := range paths {
		if q.name == p.name && q.proxy == p.proxy {
			return true
		}
	}
	return false
}

// vpnLate refuses a request sent without the VPN guard whose target needs the VPN by now (a pinned request after
// enforcement was turned on): it holds no proof for its connections, so it may not go on.
func (g *Gateway) vpnLate(ti *targetInfo) *Refused {
	if !g.vpnRule(ti).need {
		return nil
	}
	return &Refused{Reason: ReasonVPN, Target: ti.name, Detail: "the VPN became required for " + ti.name +
		" while its request was under way"}
}

// hostPort is u's lower-case host with its explicit or scheme-default port.
func hostPort(u *url.URL) string {
	if u == nil {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[strings.ToLower(u.Scheme)]
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

func urlErrorOp(method string) string {
	if method == "" {
		return "Get"
	}
	return method[:1] + strings.ToLower(method[1:])
}

// previousPath is the previous generation's path with the same name and proxy, if any. New generations are
// constructed with a separate allowlist and transport, while a matching egress route keeps its latest VPN check until
// the next scheduled check. The verdict carries over only under the same trust configuration (check_url and VPN set):
// otherwise the path starts unchecked, with check state of its own that the retired generation's checks never write,
// linked to the older check so its answers also govern the connections dialled on the older proof (newPath).
func (g *Gateway) previousPath(name, proxy string) *egressPath {
	if g.previous == nil {
		return nil
	}
	path := g.previous.paths[name]
	if path == nil || path.proxy != proxy {
		return nil
	}
	return path
}

// guard runs on every hop, redirects included: the allowlist is checked before dialing, and the User-Agent set.
type guard struct {
	base    http.RoundTripper
	g       *Gateway
	proxied bool
}

func (t *guard) RoundTrip(req *http.Request) (*http.Response, error) {
	var vpnErr *Refused
	if ti, ok := req.Context().Value(vpnTargetKey{}).(*targetInfo); ok {
		vpnErr = t.g.vpnOK(ti)
	} else if ti, ok := req.Context().Value(plainTargetKey{}).(*targetInfo); ok {
		vpnErr = t.g.vpnLate(ti)
	}
	if vpnErr != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		vpnErr.Sent = req.Response != nil
		return nil, vpnErr
	}
	r := t.g.allow.check(req.URL)
	if r == nil && t.g.probe != nil && t.proxied {
		// A proxy resolves the host itself, so a probe checks the resolved addresses here as well as at dial time.
		r = t.g.probe.checkHost(req.Context(), req.URL)
	}
	if r != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		r.Sent = req.Response != nil
		return nil, r
	}
	req = req.Clone(req.Context())
	ua := t.g.ua
	if v, ok := req.Context().Value(userAgentKey{}).(string); ok && v != "" {
		ua = v
	}
	req.Header.Set("User-Agent", ua)
	resp, err := t.base.RoundTrip(req)
	if hops, ok := req.Context().Value(hopsKey{}).(*atomic.Int32); ok {
		hops.Add(1) // sent, or tried: a transport error counts as sent
	}
	var refused *Refused
	if req.Response != nil && errors.As(err, &refused) {
		sent := *refused
		sent.Sent = true
		return resp, &sent
	}
	return resp, err
}

// allowlist is the set of hosts the policy configures. An entry with a port allows that port only; an entry without
// one allows the scheme default ports 80 and 443.
type allowlist map[string]bool

func newAllowlist(p *policy.Policy) (allowlist, error) {
	a := allowlist{}
	for _, h := range p.OutboundHosts() {
		a[strings.ToLower(h.Host)] = true
	}
	return a, nil
}

func (a allowlist) check(u *url.URL) *Refused {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") {
		return &Refused{Reason: ReasonHost, Detail: "not an http(s) URL"}
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	if a.allows(host, port) {
		return nil
	}
	return &Refused{Reason: ReasonHost, Detail: "host " + net.JoinHostPort(host, port) + " is not configured in the policy"}
}

// allows reports whether host (lower case, no brackets) and port are in the set.
func (a allowlist) allows(host, port string) bool {
	return a[net.JoinHostPort(host, port)] || ((port == "80" || port == "443") && a[hostKey(host)])
}

// hostKey is how a portless host appears in the set (IPv6 literals keep their brackets, as url.URL.Host has them).
func hostKey(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

func (a allowlist) list() []string {
	out := make([]string, 0, len(a))
	for h := range a {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}
