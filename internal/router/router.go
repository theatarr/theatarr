package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/qbit"
	"github.com/theatarr/theatarr/internal/store"
)

// Config is what New needs. Store and Policy are required.
type Config struct {
	Generation int64
	// SettingsManaged marks a router built from DB-backed settings. Only such a router fences adds with the
	// settings provider epoch; standalone routers without database settings do not use an epoch.
	SettingsManaged bool
	ProviderEpoch   int64
	ProviderKeys    map[string]string
	Store           *store.Store
	Policy          *policy.Policy
	Queue           *jobs.Queue
	// IgnoreMissingUntil suppresses blocklisting ErrNotFound briefly after provider settings change.
	IgnoreMissingUntil map[string]time.Time
	// Previous shares the route/poll lock with the active generation during a settings swap.
	Previous *Router
	// Adders are the write clients, by provider key, for providers whose key is set. A provider without one is
	// skipped (no_key). Nothing else in the router can write to a provider.
	Adders map[string]adder.Adder
	// Providers are the read-only clients (status, files, TorBox's cache check), by provider key.
	Providers map[string]provider.Provider
	// Clients are the download clients a client-routed version sends its grabs to (versions[*].add_via: client;
	// BuildClients), by client key. A version whose client has none skips its releases (no_client).
	Clients map[string]*qbit.Client
	Logger  *slog.Logger
	Now     func() time.Time
}

// Router places releases on debrid providers (docs/design/router.md). See the package doc.
type Router struct {
	generation      int64
	settingsManaged bool
	providerEpoch   int64
	providerKeys    map[string]string
	current         func() *Router
	st              *store.Store
	pol             *policy.Policy
	set             policy.RouterSettings
	adders          map[string]adder.Adder
	provs           map[string]provider.Provider
	clients         map[string]*qbit.Client
	log             *slog.Logger
	now             func() time.Time
	q               *jobs.Queue // deferred routes; nil until RegisterJobs
	runtime         func(context.Context) (context.Context, *Router)
	// stateMu guards ignoreMissingUntil and mountHealthy. It is this generation's own lock, never the route lock:
	// preparing the next generation (New) or wiring its mount check must not wait for a running route, or a save
	// turning grab or the kill switch off could not be published until that route ends.
	stateMu            sync.Mutex
	ignoreMissingUntil map[string]time.Time
	mu                 *sync.Mutex      // shared across generations: one route or poll reads and writes pacing at a time
	shared             *shared          // shared across generations too (poll.go, failure.go)
	decide             DecideFunc       // decides a queued automatic route's candidates again (SetDecider)
	decideSeries       DecideSeriesFunc // the series form of decide
	profiles           ProfileFunc      // the decision engine's compiled profiles (SetProfiles)
	// sleep waits between the status reads of a cached-only add (router.cached_grace); tests replace it.
	sleep func(ctx context.Context, d time.Duration) error
	// mountHealthy, if set, says whether the debrid mount answers (SetMountCheck); ready deadlines wait while not.
	// Guarded by stateMu.
	mountHealthy func(ctx context.Context) error
	// readyNotify, if set, is called after a write that records a download as ready (SetReadyNotify). Guarded by
	// stateMu.
	readyNotify func()
}

// shared is the state the routers of every settings generation share, besides mu.
type shared struct {
	// poll lets one poll pass run at a time: a pass releases mu around provider reads.
	poll sync.Mutex
	// mount is the debrid mount's outage record for the ready deadline (failure.go); guarded by mu.
	mount mountOutages
	// slots is the last retry time handed to a route deferred on each provider (deferSlot); guarded by mu.
	slots map[string]time.Time
	// clientDown records each download client that stopped answering the poller (pollClients); guarded by poll.
	clientDown map[string]*clientOutage
}

// ProviderEpoch is the settings provider epoch this router was prepared for.
func (r *Router) ProviderEpoch() int64 { return r.providerEpoch }

// SetMountCheck gives the router the library's mount check. While the mount is down, no ready download expires:
// a zurg outage must not blocklist every release that was only waiting to appear.
func (r *Router) SetMountCheck(f func(ctx context.Context) error) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.mountHealthy = f
}

// mountCheck returns the mount check set by SetMountCheck, or nil.
func (r *Router) mountCheck() func(ctx context.Context) error {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.mountHealthy
}

// SetReadyNotify gives the router a function it calls, after the write commits, each time it records a download as
// ready: theatarr serve wakes the library's import pass with it instead of waiting for the next tick. f must not
// block.
func (r *Router) SetReadyNotify(f func()) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.readyNotify = f
}

// notifyReady calls the function set by SetReadyNotify, if any.
func (r *Router) notifyReady() {
	r.stateMu.Lock()
	f := r.readyNotify
	r.stateMu.Unlock()
	if f != nil {
		f()
	}
}

// ignoringMissing says whether a provider's ErrNotFound is still inside the account-change grace
// (PublishProviderChanges).
func (r *Router) ignoringMissing(pk string, now time.Time) bool {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return now.Before(r.ignoreMissingUntil[pk])
}

// New builds the router.
func New(cfg Config) (*Router, error) {
	if cfg.Store == nil || cfg.Policy == nil {
		return nil, errors.New("router: store and policy are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	mu, sh := &sync.Mutex{}, &shared{}
	if cfg.Previous != nil && cfg.Previous.mu != nil {
		mu = cfg.Previous.mu
	}
	if cfg.Previous != nil && cfg.Previous.shared != nil {
		sh = cfg.Previous.shared
	}
	r := &Router{st: cfg.Store, pol: cfg.Policy, set: cfg.Policy.RouterSettings(), adders: cfg.Adders, clients: cfg.Clients,
		generation: cfg.Generation, settingsManaged: cfg.SettingsManaged, providerEpoch: cfg.ProviderEpoch, providerKeys: cfg.ProviderKeys,
		provs: cfg.Providers, log: cfg.Logger.With("component", "router"), now: cfg.Now, sleep: sleepCtx, q: cfg.Queue,
		mu: mu, shared: sh, ignoreMissingUntil: map[string]time.Time{}}
	for key, until := range cfg.IgnoreMissingUntil {
		r.ignoreMissingUntil[key] = until
	}
	if r.adders == nil {
		r.adders = map[string]adder.Adder{}
	}
	if r.provs == nil {
		r.provs = map[string]provider.Provider{}
	}
	if cfg.Previous != nil {
		// The previous generation's own state lock, not the route lock.
		cfg.Previous.stateMu.Lock()
		defer cfg.Previous.stateMu.Unlock()
		for key, until := range cfg.Previous.ignoreMissingUntil {
			if r.now().Before(until) && until.After(r.ignoreMissingUntil[key]) {
				r.ignoreMissingUntil[key] = until
			}
		}
	}
	return r, nil
}

// PublishProviderChanges starts account-change grace when the prepared runtime becomes active.
func (r *Router) PublishProviderChanges(keys []string) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	for _, key := range keys {
		r.ignoreMissingUntil[key] = r.now().Add(2 * time.Minute)
	}
}

// SetRuntimeResolver captures the runtime and router used for each poll pass.
func (r *Router) SetRuntimeResolver(resolve func(context.Context) (context.Context, *Router)) {
	r.runtime = resolve
}

// SetDecider gives the router the decision engine of its runtime: an automatic route queued under another policy
// revision decides its candidates again with it. Without one such a route is cancelled.
func (r *Router) SetDecider(movie DecideFunc, series DecideSeriesFunc) {
	r.decide, r.decideSeries = movie, series
}

// ProfileFunc returns the decision engine's compiled profile for a policy profile key and flavour, or nil
// (decide.Engine.Profile).
type ProfileFunc func(key string, fl format.Flavour) *decide.Profile

// SetProfiles gives the router the compiled profiles of its runtime's decision engine: the freshness and queue checks
// judge with the profile the decision used.
func (r *Router) SetProfiles(f ProfileFunc) { r.profiles = f }

// SetCurrentRuntime supplies the published router for the transactional provider fences.
func (r *Router) SetCurrentRuntime(current func() *Router) { r.current = current }

// Settings returns the router section in force.
func (r *Router) Settings() policy.RouterSettings { return r.set }

// Errors from the write gate.
var (
	ErrDisabled = errors.New("router: router.enabled is false; nothing is written to any provider")
	ErrShadow   = errors.New("router: the library is Watch only; nothing is written for it")
	ErrNoGrab   = errors.New("router: the version's grab switch is off; nothing is written for it")
)

// Gate says whether the router may write for a version: only when router.enabled is true, the version's mode is
// active and its grab switch is on (policy.Grabs). Every provider write in this package passes through it (mayWrite), so a shadow version or the kill switch
// blocks adds and file selections alike.
func (r *Router) Gate(version string) (Mode, error) {
	if !r.set.Enabled {
		return ModeDisabled, ErrDisabled
	}
	vp := r.pol.Versions[version]
	if vp == nil {
		return ModeShadow, fmt.Errorf("router: unknown version %q", version)
	}
	if vp.Mode != policy.ModeActive {
		return ModeShadow, ErrShadow
	}
	if !vp.Grab {
		// An active version without grab is a dry run like the kill switch (router_plan.mode has no third value).
		return ModeDisabled, ErrNoGrab
	}
	return ModeActive, nil
}

// LogState logs the kill switch and the versions the router may write for, loudly (theatarr serve calls it at start).
func (r *Router) LogState() {
	active, grabbing := r.pol.ActiveVersions(), r.pol.GrabbingVersions()
	var keys []string
	for k := range r.adders {
		keys = append(keys, k)
	}
	switch {
	case !r.set.Enabled:
		r.log.Warn("ROUTER DISABLED: router.enabled is false; nothing is added to any debrid provider, every version "+
			"is dry-run only", "active_versions", active)
	case len(active) == 0:
		r.log.Warn("ROUTER ENABLED but no version is active: nothing is added; shadow versions are dry-run only")
	case len(grabbing) == 0:
		r.log.Warn("ROUTER ENABLED but no active version has grab on: nothing is added; every version is dry-run only",
			"active_versions", active)
	default:
		r.log.Warn("ROUTER ENABLED: adds go to debrid providers for the active versions with grab on",
			"grabbing_versions", grabbing, "active_versions", active, "providers_with_key", keys,
			"cached_only", r.set.CachedOnly)
	}
}

// Route places the best disjoint candidates for req's version (one for movies). For a version the gate refuses
// (shadow, or the kill switch off) it never writes: it records the dry-run plan instead (router_plan) and returns it.
func (r *Router) Route(ctx context.Context, req Request) (*Result, error) {
	req.Candidates = slices.Clone(req.Candidates)
	r.stampFresh(&req)
	return r.routeStored(ctx, req)
}

// routeStored is Route for a request whose candidates are not new: a queued route job's payload keeps the identity
// and origin it was queued with.
func (r *Router) routeStored(ctx context.Context, req Request) (*Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if mode, err := r.Gate(req.Version); err != nil {
		if r.pol.Versions[req.Version] == nil {
			return nil, err
		}
		res, err2 := r.run(ctx, req, true)
		if err2 != nil {
			return nil, err2
		}
		res.Mode, res.Why = mode, err.Error()
		if req.DownloadID != 0 {
			// A queued download resumed while the gate refuses its version (grab or the kill switch off) ends here,
			// as a switch to shadow ends it (freshCandidates): it is not resumed every strayAfter and grabbed
			// whenever the gate opens again. A new search grabs the title then.
			if eerr := r.endQueued(ctx, req.DownloadID, "route ended: "+err.Error()); eerr != nil &&
				!errors.Is(eerr, errMovedOn) {
				return nil, eerr
			}
		}
		if err := r.recordPlan(ctx, req, res); err != nil {
			return res, err
		}
		return res, nil
	}
	res, err := r.run(ctx, req, false)
	if res != nil {
		res.Mode = ModeActive
		if err == nil && res.Outcome == OutcomeExhausted && missedEverywhere(res.Steps) {
			if manualAdd(req.Candidates) {
				// The person named this one release: when it fails, nothing else is searched for in its place.
				r.log.Info("no re-search after a release added by hand missed everywhere", "title", req.TitleID,
					"version", req.Version)
			} else if dead, _, derr := r.deadLinkRoute(ctx, req.Source); derr != nil {
				r.log.Warn("re-search not queued", "title", req.TitleID, "version", req.Version, "err", derr)
			} else if dead {
				r.log.Info("no re-search after a dead link's search missed everywhere", "title", req.TitleID,
					"version", req.Version)
			} else {
				r.research(ctx, req.TitleID, req.VersionID, req.Version, candidateEpisodes(req.Candidates),
					ReasonExhausted, 0)
			}
		}
	}
	return res, err
}

// Plan is a dry run for any version: what Route would do now, recorded nowhere. It sends nothing to any provider.
func (r *Router) Plan(ctx context.Context, req Request) (*Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pol.Versions[req.Version] == nil {
		return nil, fmt.Errorf("router: unknown version %q", req.Version)
	}
	res, err := r.run(ctx, req, true)
	if res != nil {
		res.Mode = ModeActive
		if m, gerr := r.Gate(req.Version); gerr != nil {
			res.Mode, res.Why = m, gerr.Error()
		}
	}
	return res, err
}

// mayWrite is the last check before a provider write: this router's gate, and the published runtime's when this
// router is an older generation's, so turning grab or the kill switch off stops a route that is
// already running at its next write.
func (r *Router) mayWrite(version string) error {
	if _, err := r.Gate(version); err != nil {
		return err
	}
	if r.current == nil {
		return nil
	}
	if cur := r.current(); cur != nil && cur != r {
		if _, err := cur.Gate(version); err != nil {
			return err
		}
	}
	return nil
}

// verdict is how one provider ended for one release.
type verdict int

const (
	vAccepted    verdict = iota // added, reused or (dry) would add
	vContent                    // a verdict on the release here: refused name, known uncached, not cached, blocked
	vUnavailable                // paced, backing off, failed: try later (RetryAt)
	vAbsent                     // no key: this provider is not usable at all
	vPending                    // an add's outcome is unknown: the download stays in adding until settled
	vCancelled                  // the download was ended or taken while the route ran (errMovedOn): the route stops
)

// routeState carries one route through the candidates.
type routeState struct {
	files map[int64]*decide.ExistingFile
	req   Request
	vp    *policy.VersionPolicy
	dry   bool
	res   *Result
	dlID  int64           // the current candidate's download row (active only)
	pace  map[string]pace // simulated adds in a dry run
	// profile is the version's profile for the queue check (queue.go), loaded once per route; profileErr why not.
	profile    *routeProfile
	profileErr error
	profiled   bool
	// skipProvider names providers this route tries no more, with why (an uncached AllDebrid add kept).
	skipProvider map[string]string
	// redecided: the candidates were decided again under the running policy (redecide); the resumed download
	// records that revision with the refreshed candidate.
	redecided bool
	// remapped: redecide gave a candidate other episodes than it was queued with (a changed scene mapping); the
	// candidates are checked for freshness again.
	remapped bool
	// resumeID is the queued download the route was asked to resume, and resumed whether a candidate took it up
	// (openDownload). One no candidate took up is ended when the route is done (endSuperseded).
	resumeID int64
	resumed  bool
	// scores are the version's current files' scores under the running profile (currentFilesNow), loaded once per
	// route (scored).
	scores map[int64]int64
	scored bool
	// sent: this run sent an add to a provider. Only such a run counts as a deferral toward maxDeferrals; one that
	// waited for pacing without asking anything does not.
	sent bool
	// alt holds the releases (altKey) whose add on a provider selects AltFiles: the second add of a cached-only
	// route after the first selection came back uncached (altRetry). retry is the provider settle asks to add the
	// current release to again that way, once.
	alt   map[string]bool
	retry string
	// waitRelease: provider deferred the whole release, not only itself (heldReadPaced): no other provider is tried
	// for it in this route.
	waitRelease bool
	// ttl is how long this route's miss verdicts live (verdictTTL), read once.
	ttl     time.Duration
	ttlRead bool
}

// freshMissTTL is how long a miss verdict lives for new content (availability.Fresh), at most miss_ttl: debrid caches
// pick a new release up within minutes to hours, so the next RSS sync or follow-up search may ask again soon.
const freshMissTTL = time.Hour

// verdictTTL is how long a miss verdict of this route (an uncached add, an all-provider miss) lives: miss_ttl, or
// freshMissTTL when the route's content is new. A 451 always keeps miss_ttl.
func (r *Router) verdictTTL(ctx context.Context, s *routeState, now time.Time) time.Duration {
	if s.ttlRead {
		return s.ttl
	}
	var kind policy.Kind
	if s.vp != nil {
		kind = s.vp.Kind
	}
	s.ttlRead, s.ttl = true, r.missTTL(ctx, kind, s.req.TitleID, candidateEpisodes(s.req.Candidates), now)
	return s.ttl
}

// missTTL is how long an uncached or miss verdict on this content lasts: 1 h for new content (availability.Fresh),
// capped at miss_ttl; miss_ttl otherwise or when the dates cannot be read.
func (r *Router) missTTL(ctx context.Context, kind policy.Kind, titleID int64, episodes []int64, now time.Time) time.Duration {
	until, err := availability.Fresh(ctx, r.st.Reader(), kind, titleID, episodes, now)
	if err != nil {
		r.log.Warn("content date not read; miss verdicts keep miss_ttl", "title", titleID, "err", err)
		return r.set.MissTTL
	}
	if !until.IsZero() {
		return min(freshMissTTL, r.set.MissTTL)
	}
	return r.set.MissTTL
}

func (r *Router) run(ctx context.Context, req Request, dry bool) (*Result, error) {
	s := &routeState{}
	res, err := r.route(ctx, s, req, dry)
	if err != nil || dry || res == nil {
		return res, err
	}
	return res, r.endSuperseded(ctx, s, res)
}

// endSuperseded ends the queued download a route resumed when no candidate of the route took it up: the decisions
// ranked another release first and it was placed or is waiting under its own download, or the resumed release was
// skipped (blocklisted, in flight elsewhere, held, past max_candidates). Left queued, it would be resumed by the
// stray sweep every strayAfter. A route that ended it already (freshness) or found it moved on changes
// nothing.
func (r *Router) endSuperseded(ctx context.Context, s *routeState, res *Result) error {
	if s.resumeID == 0 || s.resumed || res.Outcome == OutcomeCancelled {
		return nil
	}
	err := r.endQueued(ctx, s.resumeID, fmt.Sprintf("route ended: its release was not placed by the route that "+
		"resumed it (outcome %s)", res.Outcome))
	if errors.Is(err, errMovedOn) {
		return nil
	}
	return err
}

func (r *Router) route(ctx context.Context, s *routeState, req Request, dry bool) (*Result, error) {
	vp := r.pol.Versions[req.Version]
	if vp == nil {
		return nil, fmt.Errorf("router: unknown version %q", req.Version)
	}
	req.Candidates = append([]Candidate(nil), req.Candidates...)
	*s = routeState{req: req, vp: vp, dry: dry, res: &Result{Version: req.Version, Outcome: OutcomeExhausted,
		Steps: []Step{}}, pace: map[string]pace{}, resumeID: req.DownloadID}
	var err error
	if vp.PreferSeaDex {
		ctx, err = decide.WithStoredSeaDex(ctx, r.st.Reader())
		if err != nil {
			return s.res, err
		}
	}
	ctx = decide.WithSeaDexTitle(decide.WithSeaDexPolicy(ctx, r.pol, vp), req.TitleID)
	// Every candidate is routed by its normalised identity; one whose reported hash conflicts with its magnet is
	// rejected, never added by either hash.
	kept, dropped := identityCandidates(s.req.Candidates)
	for _, c := range dropped {
		r.log.Warn("router: candidate rejected: conflicting or unverifiable torrent identity", "title_id", req.TitleID,
			"version", req.Version, "release", c.Release.Title, "info_hash", c.Release.InfoHash)
		s.res.Steps = append(s.res.Steps, Step{Release: c.Release.Title, Hash: indexer.NormalizeHash(c.Release.InfoHash),
			Outcome: "skipped", Code: "hash_conflict", Message: "conflicting or unverifiable torrent identity"})
	}
	for i := range kept {
		if vp.PreferSeaDex {
			kept[i].Release = decide.RefreshSeaDexRelease(ctx, kept[i].Release)
		}
	}
	req.Candidates, s.req.Candidates = kept, kept
	automatic, err := r.automatic(ctx, s.req.Source, s.req.DownloadID != 0)
	if err != nil {
		return s.res, err
	}
	if automatic {
		cands, err := r.freshCandidates(ctx, s)
		if err == nil && len(cands) > 0 {
			cands, err = r.redecide(ctx, s, cands)
		}
		if err == nil && s.remapped && len(cands) > 0 {
			s.req.Candidates = cands
			cands, err = r.freshCandidates(ctx, s)
		}
		if err != nil {
			return s.res, err
		}
		req.Candidates = cands
		s.req.Candidates = cands
	}
	if vp.Storage != policy.StorageDebrid || (len(vp.Providers) == 0 && !vp.ViaClient()) {
		s.res.Steps = append(s.res.Steps, Step{Outcome: "skipped", Code: "not_debrid",
			Message: "the version stores on " + string(vp.Storage) + "; only debrid versions are routed"})
		return s.res, nil
	}
	// At most max_candidates releases are tried. A release skipped without a request to any provider (a live miss
	// or known uncached everywhere, blocklisted, no hash) does not count, so a search made again after a
	// failure reaches the releases the first route never tried.
	// versions[*].upgrades: off never replaces a file (upgrades.go).
	cands, err := r.screenUpgrades(ctx, s, req.Candidates)
	if err != nil {
		return s.res, err
	}
	tried := 0
	var chosen, deferred *Result
	var covered []int64
	whole := false
	for i := range cands {
		if tried >= r.set.MaxCandidates {
			break
		}
		c := &cands[i]
		if vp.Kind == policy.KindSeries || !manualSource(s.req.Source) {
			code, msg := "", ""
			if vp.Kind != policy.KindSeries {
				// Radarr's QueueSpecification (queue.go): another release of the movie in flight for the version.
				if id, why, err := r.movieInFlight(ctx, s, c); err != nil {
					return s.res, err
				} else if id != 0 {
					code, msg = "in_flight", fmt.Sprintf("download %d holds another release of the movie: %s", id, why)
					s.res.DownloadID = id
				}
			} else if whole || (len(c.Episodes) == 0 && len(covered) > 0) || overlaps(covered, c.Episodes) {
				code, msg = "episode_covered", "episodes already covered by a release in this batch"
			} else if id, err := r.episodesInFlight(ctx, s.req, c, dry); err != nil {
				return s.res, err
			} else if id != 0 {
				code, msg = "in_flight", fmt.Sprintf("download %d holds overlapping episodes already", id)
				s.res.DownloadID = id
			}
			if code != "" {
				s.res.Steps = append(s.res.Steps, Step{Release: c.Release.Title,
					Hash: indexer.NormalizeHash(c.Release.InfoHash), Outcome: "skipped", Code: code, Message: msg})
				if chosen == nil && deferred == nil && code == "in_flight" {
					s.res.Outcome, s.res.Release, s.res.Hash = OutcomeInFlight, c.Release.Title, indexer.NormalizeHash(c.Release.InfoHash)
					copy := *s.res
					chosen = &copy
				}
				continue
			}
		}
		// Each candidate gets its own download and provider result; the steps span the batch.
		s.res.DownloadID, s.res.TorrentID, s.res.Reused, s.res.Provider, s.res.Client = 0, "", false, "", ""
		n0 := len(s.res.Steps)
		out, retryAt, err := r.candidate(ctx, s, c)
		if err != nil {
			return s.res, err
		}
		// The cap bounds failed attempts: a series placement never stops the batch from filling other episodes.
		if triedAny(s.res.Steps[n0:]) && out != vAccepted {
			tried++
		}
		switch out {
		case vCancelled:
			s.res.Outcome = OutcomeCancelled
			s.res.Release, s.res.Hash = c.Release.Title, indexer.NormalizeHash(c.Release.InfoHash)
			return s.res, nil
		case vAccepted:
			if vp.Kind != policy.KindSeries {
				return s.res, nil
			}
			if chosen == nil || (chosen.Outcome == OutcomeInFlight && s.res.Outcome != OutcomeInFlight) {
				copy := *s.res
				chosen = &copy
			}
		case vUnavailable:
			s.res.Outcome, s.res.RetryAt = OutcomeDeferred, retryAt
			s.res.Release, s.res.Hash, s.res.Indexer = c.Release.Title, indexer.NormalizeHash(c.Release.InfoHash), c.Release.Indexer
			if vp.Kind != policy.KindSeries {
				return s.res, nil
			}
			if deferred == nil {
				copy := *s.res
				deferred = &copy
			} else if retryAt.Before(deferred.RetryAt) {
				deferred.RetryAt = retryAt
			}
		}
		if out == vAccepted || out == vUnavailable {
			covered = append(covered, c.Episodes...)
			whole = len(c.Episodes) == 0
		}
		// A miss (or a release that cannot go to debrid): the next candidate, without searching again.
	}
	// The existing job retry resumes at the first deferred release, keeping every later alternative and disjoint
	// candidate. Later queued downloads are resumed by the stranded-row check; successful ones stay in flight.
	if deferred != nil {
		deferred.Steps = s.res.Steps
		return deferred, nil
	}
	if chosen != nil {
		chosen.Steps = s.res.Steps
		return chosen, nil
	}
	return s.res, nil
}

// Episode overlap selection follows Sonarr src/NzbDrone.Core/Download/ProcessDownloadDecisions.cs @ cab419a.
func overlaps(a, b []int64) bool {
	return slices.ContainsFunc(a, func(id int64) bool { return slices.Contains(b, id) })
}

// episodesInFlight checks other releases too: a different hash may already cover any of this release's episodes.
// The same hash still goes through candidate's reconciliation and stranded-row recovery on active routes.
func (r *Router) episodesInFlight(ctx context.Context, req Request, c *Candidate, dry bool) (int64, error) {
	rows, err := r.st.Writer().QueryContext(ctx, `SELECT id, episodes FROM download
		WHERE title_id = ? AND version_id = ? AND state IN `+inFlightStates+`
		AND (info_hash <> ? OR ?) AND NOT (id = ? AND state = 'queued') ORDER BY id`,
		req.TitleID, req.VersionID, indexer.NormalizeHash(c.Release.InfoHash), dry, req.DownloadID)
	if err != nil {
		return 0, fmt.Errorf("router: episode in-flight check: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var js string
		if err := rows.Scan(&id, &js); err != nil {
			return 0, err
		}
		var eps []int64
		if json.Unmarshal([]byte(js), &eps) != nil || len(eps) == 0 || len(c.Episodes) == 0 || overlaps(eps, c.Episodes) {
			return id, nil
		}
	}
	return 0, rows.Err()
}

// maxUnlanded caps the adds of one download to one provider whose answer did not say and that never landed: after
// that many, the resumed route tries the version's other providers (or the next release) instead of adding there
// again, so an answer we cannot read cannot loop the same add for every resume.
const maxUnlanded = 3

// capUnlanded marks provider pk done for this route when download dl's adds there never landed maxUnlanded times.
func (r *Router) capUnlanded(ctx context.Context, s *routeState, dl int64, pk string) error {
	var n int
	if err := r.st.Writer().QueryRowContext(ctx, `SELECT COUNT(*) FROM download_attempt WHERE download_id = ?
		AND provider = ? AND code = ? AND outcome = 'error'`, dl, pk, codeReconciled).Scan(&n); err != nil {
		return fmt.Errorf("router: download %d attempts: %w", dl, err)
	}
	if n < maxUnlanded {
		return nil
	}
	if s.skipProvider == nil {
		s.skipProvider = map[string]string{}
	}
	s.skipProvider[pk] = fmt.Sprintf("%d adds to %s answered without saying and never landed; not added there again "+
		"by this route", n, pk)
	r.log.Warn("adds never landed; provider skipped for this route", "download", dl, "provider", pk, "count", n)
	return nil
}

// candidate tries one release on the version's providers in order.
func (r *Router) candidate(ctx context.Context, s *routeState, c *Candidate) (verdict, time.Time, error) {
	rel := &c.Release
	hash := indexer.NormalizeHash(rel.InfoHash)
	step := func(st Step) {
		st.Release, st.Hash = rel.Title, hash
		s.res.Steps = append(s.res.Steps, st)
	}
	m, skip, ok, err := r.preflight(ctx, s.req.Version, rel)
	if err != nil {
		return 0, time.Time{}, err
	}
	if !ok {
		step(skip)
		return vContent, time.Time{}, nil
	}
	if !s.dry {
		id, inFlight, err := r.inFlight(ctx, s.req, hash)
		if err != nil {
			return 0, time.Time{}, err
		}
		if inFlight {
			// A row left in adding by an interrupted add: settled from the account before anything is added again
			// (reconcile.go). Found there, the route ends on that torrent; not there, the row is resumed.
			// A client download's add is settled from its client instead (client.go).
			cand := c
			c, err := r.settleClient(ctx, id)
			if err == nil && c.client == "" {
				c, err = r.reconcileAdding(ctx, id)
			}
			if err != nil {
				return 0, time.Time{}, err
			}
			switch c.rec {
			case recFound:
				step(Step{Provider: c.pk, Client: c.client, Outcome: "accepted", Code: codeReconciled,
					Message: c.why + "; nothing added"})
				s.res.Outcome, s.res.DownloadID, s.res.Release, s.res.Hash = OutcomeAdded, id, rel.Title, hash
				s.res.Indexer, s.res.Provider, s.res.TorrentID, s.res.Reused = rel.Indexer, c.pk, c.tid, true
				s.res.Client = c.client
				return vAccepted, time.Time{}, nil
			case recAbsent:
				ok, err := r.settleAdding(ctx, id, c.attempt, StateQueued, "")
				if err != nil {
					return 0, time.Time{}, err
				}
				if ok {
					r.log.Info("interrupted add not on the provider; resuming its download", "download", id,
						"provider", c.pk, "client", c.client, "hash", hash)
					if err := r.recordAttempt(ctx, id, Step{Provider: c.pk, Client: c.client, Outcome: "error",
						Code: codeReconciled, Message: c.why + "; resumed"}); err != nil {
						return 0, time.Time{}, err
					}
					s.req.DownloadID, inFlight = id, false
					if c.client == "" {
						if err := r.capUnlanded(ctx, s, id, c.pk); err != nil {
							return 0, time.Time{}, err
						}
					}
				}
			case recUnknown:
				step(Step{Provider: c.pk, Client: c.client, Outcome: "skipped", Code: codeUnsettled, Message: c.why})
				// The route that settles it is queued with the note: the poller leaves a row with a
				// pending route to that route, which resumes the download once the account can tell, where the
				// poller could only fail it.
				at := r.now().Add(reconcileAfter)
				if settled := c.sent.Add(addSettle); settled.After(at) {
					at = settled
				}
				if err := r.noteUnsettled(ctx, id, c.attempt, c.why, r.continuation(ctx, s, cand, id, at)); err != nil {
					return 0, time.Time{}, err
				}
			}
		}
		if inFlight {
			// A queued row no pending route job will pick up (its route died between queueing it and recording the
			// follow-up) is resumed here instead of being reported in flight and left stranded.
			if stranded, err := r.strandedQueued(ctx, id); err != nil {
				return 0, time.Time{}, err
			} else if stranded {
				r.log.Info("resuming a stranded queued download", "download", id, "hash", hash)
				s.req.DownloadID, inFlight = id, false
			}
		}
		if inFlight {
			step(Step{Outcome: "skipped", Code: "in_flight", Message: fmt.Sprintf("download %d holds it already", id)})
			s.res.Outcome, s.res.DownloadID, s.res.Release, s.res.Hash = OutcomeInFlight, id, rel.Title, hash
			return vAccepted, time.Time{}, nil
		}
		// A release every usable provider is known to miss (or to hold uncached under cached_only) opens no
		// download row and asks for no re-search: RSS routes an active version's items every sync.
		// A client-routed version asks its client, not the providers: its own skips (clientSkip) stand in here.
		if s.req.DownloadID == 0 && s.vp.ViaClient() {
			if code, msg, err := r.clientSkip(ctx, s, rel, hash); err != nil {
				return 0, time.Time{}, err
			} else if code != "" {
				step(Step{Client: s.vp.Client, Outcome: "skipped", Code: code, Message: msg})
				return vContent, time.Time{}, nil
			}
		} else if s.req.DownloadID == 0 {
			if known, why, err := r.knownMiss(ctx, s, c, hash); err != nil {
				return 0, time.Time{}, err
			} else if known {
				msg := "every provider missed it within miss_ttl, or has it uncached with cached_only on"
				if why != "" {
					msg += "; " + why
				}
				step(Step{Outcome: "skipped", Code: "known_miss", Message: msg})
				return vContent, time.Time{}, nil
			}
		}
		var rev any
		if s.redecided {
			rev = r.pol.Revision
		}
		id, err = r.openDownload(ctx, s.req, c, hash, rev)
		if errors.Is(err, errMovedOn) {
			return r.movedOn(s, c, err), time.Time{}, nil
		}
		if err != nil {
			return 0, time.Time{}, err
		}
		if id == s.resumeID {
			s.resumed = true
		}
		s.dlID, s.req.DownloadID = id, 0
		s.res.DownloadID = id
	}
	if s.vp.ViaClient() {
		return r.viaClient(ctx, s, c, hash)
	}
	// The indexer's provider allowlist (indexers.K.providers) narrows the version's providers; cache evidence orders
	// them and never skips one.
	provs := r.pol.ReleaseProviders(s.vp, rel.Indexer)
	order, why, err := r.providerOrder(ctx, provs, rel, hash, r.now())
	if err != nil {
		return 0, time.Time{}, err
	}
	if why != "" {
		step(Step{Outcome: outcomeOrdered, Code: codeCachedFirst, Message: why})
		r.log.Info("providers reordered by cache evidence", "release", rel.Title, "hash", hash,
			"version", s.req.Version, "why", why)
	}
	usable, content := 0, 0
	var earliest time.Time
	earliestPK := ""
	for i := 0; i < len(order); i++ {
		pk := order[i]
		s.retry, s.waitRelease = "", false
		v, retryAt, st, err := r.provider(ctx, s, c, m, pk)
		if errors.Is(err, errMovedOn) {
			return r.movedOn(s, c, err), time.Time{}, nil
		}
		if err != nil {
			return 0, time.Time{}, err
		}
		st.Provider = pk
		step(st)
		if !s.dry && s.dlID != 0 {
			if err := r.recordAttempt(ctx, s.dlID, st); err != nil {
				return 0, time.Time{}, err
			}
		}
		switch v {
		case vPending:
			// Nothing more is tried for this download: the route waits for the account to settle the add (its
			// continuation was recorded with the row: noteUnsettled).
			s.res.Provider = pk
			return vUnavailable, retryAt, nil
		case vAccepted:
			s.res.Release, s.res.Hash, s.res.Indexer, s.res.Provider = rel.Title, hash, rel.Indexer, pk
			if s.dry {
				s.res.Outcome = OutcomeWouldAdd
			} else {
				s.res.Outcome = OutcomeAdded
			}
			return vAccepted, time.Time{}, nil
		case vContent:
			usable++
			content++
			if !s.dry && (st.Code == codeUncachedDeleted || st.Code == codeUncachedKept) {
				// The settle waited: a blocklist entry made meanwhile ends the release before the next provider
				// (a removal ends the download itself: unmarkAdded).
				why, err := r.blocklisted(ctx, s.req.Version, hash, rel.Title)
				if err != nil {
					return 0, time.Time{}, err
				}
				if why != "" {
					step(Step{Outcome: "skipped", Code: "blocklisted", Message: why})
					if err := r.endQueued(ctx, s.dlID, why); err != nil && !errors.Is(err, errMovedOn) {
						return 0, time.Time{}, err
					}
					return vContent, time.Time{}, nil
				}
			}
			if !s.dry && s.retry == pk {
				// Real-Debrid caches per selected set: settle asked for one more add with the other set (altRetry).
				again, skip, err := r.altRetry(ctx, s, pk, hash)
				if err != nil {
					return 0, time.Time{}, err
				}
				if skip != "" {
					step(Step{Provider: pk, Outcome: "skipped", Code: codeAltSkipped, Message: skip})
				}
				if again {
					i-- // the same provider again
				}
			}
		case vUnavailable:
			usable++
			if earliest.IsZero() || retryAt.Before(earliest) {
				earliest, earliestPK = retryAt, pk
			}
		}
		if s.waitRelease {
			// The provider holds the hash and its read was only paced: the release waits for it.
			break
		}
	}
	now := r.now()
	switch {
	case usable == 0:
		msg := "no provider of the version has an API key set"
		if len(provs) == 0 {
			msg = fmt.Sprintf("indexer %s allows none of the version's providers (indexers.%s.providers)", rel.Indexer, rel.Indexer)
		} else if len(s.skipProvider) > 0 {
			msg = "no provider of the version is usable for this route"
		}
		step(Step{Outcome: "miss", Code: "no_provider", Message: msg})
		if !s.dry {
			return vContent, time.Time{}, r.failDownload(ctx, s.dlID, msg, StateQueued)
		}
		return vContent, time.Time{}, nil
	case content == usable:
		// A miss on every provider (docs/design/router.md): cached as a miss for miss_ttl; the next candidate is tried.
		msg := "miss on every provider"
		step(Step{Outcome: "miss", Code: "miss", Message: msg})
		if !s.dry {
			if err := r.recordMiss(ctx, hash, provs, s.skipProvider, now, r.verdictTTL(ctx, s, now)); err != nil {
				return 0, time.Time{}, err
			}
			return vContent, time.Time{}, r.failDownload(ctx, s.dlID, msg, StateQueued)
		}
		return vContent, time.Time{}, nil
	}
	if earliest.IsZero() || earliest.Before(now) {
		earliest = now.Add(BackoffBase)
	}
	if !s.dry {
		var take func()
		earliest, take = r.deferSlot(earliestPK, earliest, now)
		var err error
		if why := s.giveUp(now); why != "" {
			err = r.endQueued(ctx, s.dlID, why)
		} else {
			next := r.continuation(ctx, s, c, s.dlID, earliest)
			if err = r.queueDownload(ctx, s.dlID, earliest, next); err == nil && next != nil {
				take()
			}
		}
		if errors.Is(err, errMovedOn) {
			return r.movedOn(s, c, err), time.Time{}, nil
		}
		if err != nil {
			return 0, time.Time{}, err
		}
	}
	return vUnavailable, earliest, nil
}

// knownMiss reports whether provider() would skip the release on every provider the route could use as content
// (a live miss, known uncached with cached_only, Real-Debrid's name rule), with at least one such provider. It reads
// the cache only; nothing is written.
func (r *Router) knownMiss(ctx context.Context, s *routeState, c *Candidate, hash string) (bool, string, error) {
	now, usable := r.now(), 0
	var whys []string
	for _, pk := range r.pol.ReleaseProviders(s.vp, c.Release.Indexer) {
		pv := r.pol.Providers[pk]
		if pv == nil || r.adders[pk] == nil || s.skipProvider[pk] != "" {
			continue
		}
		usable++
		if pv.Kind == provider.KindRealDebrid && indexer.RealDebridRefusesRelease(&c.Release) {
			continue
		}
		if why, err := r.titleRefused(ctx, pk, s.req.TitleID, now); err != nil {
			return false, "", err
		} else if why != "" {
			whys = append(whys, why)
			continue
		}
		state, err := r.cacheState(ctx, hash, pk, now)
		if err != nil {
			return false, "", err
		}
		if state != "miss" && !(state == "uncached" && r.set.CachedOnly) {
			return false, "", nil
		}
	}
	return usable > 0, strings.Join(whys, "; "), nil
}

// movedOn ends a candidate whose download was ended (removed from the queue, blocklisted) or taken by another route
// while this route ran: nothing more is tried for it, and the route stops (vCancelled).
func (r *Router) movedOn(s *routeState, c *Candidate, err error) verdict {
	r.log.Info("route stopped: its download moved on", "version", s.req.Version, "release", c.Release.Title,
		"download", s.res.DownloadID, "why", err)
	s.res.Steps = append(s.res.Steps, Step{Release: c.Release.Title, Hash: indexer.NormalizeHash(c.Release.InfoHash),
		Outcome: "cancelled", Code: codeMovedOn, Message: err.Error()})
	return vCancelled
}

// movedOnIfTaken turns a guarded write on the download this route holds in queued that found it gone into
// errMovedOn: the route stops instead of failing and retrying.
func movedOnIfTaken(err error) error {
	if errors.Is(err, errAddTaken) {
		return fmt.Errorf("%w: %w", errMovedOn, err)
	}
	return err
}

// codeHeldUnread: the provider holds the hash under a known torrent id but did not answer a read of it; the id is
// not reused and the provider is deferred for the route.
const codeHeldUnread = "held_unread"

// heldReadPaced reports whether heldBy's failed read was paced rather than failed: the gateway refused to send it for
// its request budget or the VPN check, or the provider answered 429. at is when to read again, when known. A
// transport error, a 5xx, the provider saying it is unavailable or the gateway's back-off after failed requests is
// the provider likely down: then the provider alone is deferred and the route tries the next one.
func heldReadPaced(err error) (bool, time.Time) {
	var refused *gateway.Refused
	if errors.As(err, &refused) {
		return refused.Reason == gateway.ReasonBudget || refused.Reason == gateway.ReasonVPN, refused.RetryAt
	}
	var ae *provider.APIError
	return errors.As(err, &ae) && ae.Status == http.StatusTooManyRequests, time.Time{}
}

// codeMovedOn: the route's download was ended or taken while the route ran.
const codeMovedOn = "download_moved_on"

// preflight says whether a release can be placed for the version at all, before any provider is asked: a v1 hash and
// the version's blocklist. When it cannot, ok is false and skip is the step that says why. Which indexers feed the
// version is the user's choice, checked by the decision engine (IndexerNotInVersion).
func (r *Router) preflight(ctx context.Context, version string, rel *indexer.Release) (
	m adder.Magnet, skip Step, ok bool, err error) {
	m, err = adder.NewMagnet(rel, r.set.Trackers)
	if err != nil {
		return m, Step{Outcome: "skipped", Code: "no_hash", Message: err.Error()}, false, nil
	}
	// A hash the library rejected for this version (sample, runtime): never routed again for it.
	why, err := r.blocklisted(ctx, version, indexer.NormalizeHash(rel.InfoHash), rel.Title)
	if err != nil {
		return m, Step{}, false, err
	}
	if why != "" {
		return m, Step{Outcome: "skipped", Code: "blocklisted", Message: why}, false, nil
	}
	return m, Step{}, true, nil
}

// provider decides one release at one provider, and adds it when the route is not dry.
func (r *Router) provider(ctx context.Context, s *routeState, c *Candidate, m adder.Magnet, pk string) (verdict,
	time.Time, Step, error) {
	pv := r.pol.Providers[pk]
	if pv == nil {
		return vAbsent, time.Time{}, Step{Outcome: "skipped", Code: "unknown_provider"}, nil
	}
	ad := r.adders[pk]
	if ad == nil {
		return vAbsent, time.Time{}, Step{Outcome: "skipped", Code: "no_key",
			Message: "no API key set (" + pv.APIKeyEnv + ")"}, nil
	}
	if why := s.skipProvider[pk]; why != "" {
		// Not usable for the rest of this route (uncached.go: an uncached AllDebrid add kept); it reserves nothing
		// and no miss is recorded for it.
		return vAbsent, time.Time{}, Step{Outcome: "skipped", Code: codeProviderDone, Message: why}, nil
	}
	hash := m.Hash()
	// Real-Debrid's name rule.
	if pv.Kind == provider.KindRealDebrid && indexer.RealDebridRefusesRelease(&c.Release) {
		return vContent, time.Time{}, Step{Outcome: "skipped", Code: "rd_name_rule",
			Message: "Real-Debrid refuses this name"}, nil
	}
	now := r.now()
	if why, err := r.titleRefused(ctx, pk, s.req.TitleID, now); err != nil {
		return 0, time.Time{}, Step{}, err
	} else if why != "" {
		return vContent, time.Time{}, Step{Outcome: "skipped", Code: codeTitle451, Message: why}, nil
	}
	// Known misses and known uncached releases.
	state, err := r.cacheState(ctx, hash, pk, now)
	if err != nil {
		return 0, time.Time{}, Step{}, err
	}
	switch {
	case state == "miss":
		return vContent, time.Time{}, Step{Outcome: "skipped", Code: "miss",
			Message: "missed on every provider, or refused here with a 451, within miss_ttl"}, nil
	// The second add of altRetry is the one exception: the uncached verdict was on the other selection.
	case state == "uncached" && r.set.CachedOnly && !s.alt[altKey(pk, hash)]:
		return vContent, time.Time{}, Step{Outcome: "skipped", Code: "known_uncached",
			Message: "known uncached here and cached_only is on"}, nil
	}
	// Pacing and back-off. A provider backing off because it is down (unreachable, 5xx, its maintenance, downloads
	// that stalled while it was down: endUnavailable) is not used for the rest of its back-off, not even to reuse a
	// torrent it holds: a route queued again after an outage would otherwise go straight back to the provider that
	// was down, restart the stall clock on the same torrent, and come round again. A back-off from
	// refused or rate-limited adds, and the add pacing (gap, rate, cooldown, hold), are checked after the reuse
	// below: a reuse sends no add and takes no slot, and skipping it would cost a second add elsewhere.
	p, err := loadPace(ctx, r.st.Writer(), pk)
	if err != nil {
		return 0, time.Time{}, Step{}, err
	}
	if planned, ok := s.pace[pk]; s.dry && ok {
		p = planned
	}
	if p.BackoffDown && p.BackoffUntil.After(now) {
		return vUnavailable, p.BackoffUntil, Step{Outcome: "deferred", Code: "paced", Message: "backoff: " + p.Reason,
			RetryAt: p.BackoffUntil}, nil
	}
	// Already on the provider: point at that torrent; nothing is added. A known id the provider did not answer for
	// is not reused (its presence is not verified, and the provider is likely down): this provider is deferred for
	// the route, which tries the next one.
	if tid, unread, err := r.heldBy(ctx, hash, pk, !s.dry); err != nil {
		return 0, time.Time{}, Step{}, err
	} else if unread != nil {
		until := now.Add(BackoffBase)
		msg := "the provider holds this hash (torrent " + tid + ") but did not answer a read of it: " +
			unread.Error() + "; not reused while unverified"
		if paced, at := heldReadPaced(unread); paced {
			// The read was paced, not failed (the gateway's budget or VPN check, a 429): the provider is not shown
			// down, and adding the release elsewhere would leave the torrent it holds unreferenced on its account. The
			// whole release waits for the read.
			if at.After(until) {
				until = at
			}
			s.waitRelease = !s.dry
			msg += "; the release waits for it instead of going to another provider"
		}
		return vUnavailable, until, Step{Outcome: "deferred", Code: codeHeldUnread, RetryAt: until, Message: msg}, nil
	} else if tid != "" {
		st := Step{Outcome: "accepted", Code: "reused", Message: "the provider holds this hash already (torrent " +
			tid + "); nothing added"}
		if s.dry {
			st.Outcome = "would_add"
			return vAccepted, time.Time{}, st, nil
		}
		s.res.TorrentID, s.res.Reused = tid, true
		return vAccepted, time.Time{}, st, movedOnIfTaken(r.markAdded(ctx, s.dlID, pk, &adder.Added{ID: tid,
			Hash: hash}, true, fromQueued))
	}
	// Another download's add of this hash here is unsettled (adding: it may have landed): adding it again would make
	// a second entry of the hash on the account (Real-Debrid), or hand this route the other's entry, which a
	// cached-only settle could then delete (AllDebrid). Wait until that add is settled.
	var other int64
	var sent int64
	err = r.st.Writer().QueryRowContext(ctx, `SELECT id, updated_at FROM download WHERE info_hash = ? AND id != ?
		AND state = 'adding' AND client IS NULL AND (provider = ? OR COALESCE(provider, '') = '') ORDER BY updated_at DESC LIMIT 1`,
		hash, s.dlID, pk).Scan(&other, &sent)
	switch {
	case err == nil:
		until := store.FromMillis(sent).Add(addSettle)
		if floor := now.Add(reconcileAfter); until.Before(floor) {
			until = floor
		}
		return vUnavailable, until, Step{Outcome: "deferred", Code: codeUnsettled, RetryAt: until,
			Message: fmt.Sprintf("download %d's add of this hash to %s is not settled yet", other, pk)}, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, time.Time{}, Step{}, fmt.Errorf("router: unsettled adds: %w", err)
	}
	// Add pacing (p was loaded before the reuse check).
	pc := pv.EffectivePacing()
	if until, why := p.blocked(now, pc); !until.IsZero() {
		return vUnavailable, until, Step{Outcome: "deferred", Code: "paced", Message: why, RetryAt: until}, nil
	}
	if s.dry {
		note, err := r.cachedOnlyNote(ctx, pv, pk, hash)
		if err != nil {
			return 0, time.Time{}, Step{}, err
		}
		p.added(now, pc)
		s.pace[pk] = p
		return vAccepted, time.Time{}, Step{Outcome: "would_add", Code: "add",
			Message: "would add the bare magnet" + cachedNote(c, pv.Kind) + note}, nil
	}
	// TorBox's cache check is a read: cheaper than a refused add-only-if-cached, which costs an uncached add.
	if r.set.CachedOnly {
		if rp := r.provs[pk]; rp != nil && rp.Cache().Supported {
			got, err := rp.CheckCached(ctx, provider.CacheQuery{Hashes: []string{hash}})
			if err == nil && !got[hash] {
				if err := r.setCache(ctx, hash, pk, "uncached", "check", now.Add(min(indexer.CheckUncachedTTL, r.set.MissTTL)), now); err != nil {
					return 0, time.Time{}, Step{}, err
				}
				return vContent, time.Time{}, Step{Outcome: "skipped", Code: "known_uncached",
					Message: "the provider's cache check says uncached"}, nil
			}
		}
	}
	// Cached-only on Real-Debrid and AllDebrid (uncached.go): note whether the hash is on the account before the add,
	// so an uncached add is deleted only when it is provably our own new entry.
	// The stall timeout (stalled.go) deletes a stalled Real-Debrid add only when this guard proved it ours at the add
	// (download.own_add).
	var guard *ownGuard
	if settles(pv.Kind) && (r.set.CachedOnly || (r.stallAfter(pk) > 0 && pv.Kind == provider.KindRealDebrid)) {
		if guard, err = r.guardBefore(ctx, pv, pk, hash, s.dlID, r.set.CachedOnly); err != nil {
			return 0, time.Time{}, Step{}, err
		}
		if guard.reuse != "" {
			st := Step{Outcome: "accepted", Code: "reused", Message: "the account's magnet list shows this hash " +
				"(torrent " + guard.reuse + "); nothing added"}
			s.res.TorrentID, s.res.Reused = guard.reuse, true
			return vAccepted, time.Time{}, st, movedOnIfTaken(r.markAdded(ctx, s.dlID, pk,
				&adder.Added{ID: guard.reuse, Hash: hash}, true, fromQueued))
		}
	}
	if err := r.mayWrite(s.req.Version); err != nil {
		// The gate closed while the route ran (grab or the kill switch turned off, the version made shadow): the
		// download it holds ends, as a route queued under the closed gate ends it (Route), and the route stops.
		if s.dlID != 0 {
			if eerr := r.endQueued(ctx, s.dlID, "route stopped: "+err.Error()); eerr != nil &&
				!errors.Is(eerr, errMovedOn) {
				return 0, time.Time{}, Step{}, eerr
			}
		}
		return 0, time.Time{}, Step{}, fmt.Errorf("%w: %w", errMovedOn, err)
	}
	// adding names the provider and the attempt, so a crash between the add and its record, or an answer that does
	// not say, is settled from the account (reconcile.go). The add cannot be taken back: a route job that lost its
	// lease stops here, and so does a route whose download was ended meanwhile.
	// The add is reserved in pacing in one transaction, as the torrent manager's restore does
	// (ReserveAdd): an add it made since the check above is seen, and neither overwrites the other's.
	var before, after pace
	var until time.Time
	var why string
	now = r.now()
	if err := updatePace(ctx, r.st, pk, now, func(_ *sql.Tx, q *pace) (bool, error) {
		if until, why = q.blocked(now, pc); !until.IsZero() {
			return false, nil
		}
		before = *q
		q.added(now, pc)
		after = *q
		return true, nil
	}); err != nil {
		return 0, time.Time{}, Step{}, err
	}
	if !until.IsZero() {
		return vUnavailable, until, Step{Outcome: "deferred", Code: "paced", Message: why, RetryAt: until}, nil
	}
	alt := pv.Kind == provider.KindRealDebrid && s.alt[altKey(pk, hash)]
	attempt, err := r.markAdding(ctx, s.dlID, pk, alt)
	if err != nil {
		return 0, time.Time{}, Step{}, movedOnIfTaken(err)
	}
	s.sent = true
	added, aerr := ad.AddMagnet(ctx, m, adder.AddOptions{OnlyIfCached: r.set.CachedOnly})
	// What the provider accepted is recorded even if ctx ends now: an unrecorded add is what reconciliation is for.
	rctx := context.WithoutCancel(ctx)
	now = r.now()
	if aerr != nil && ctx.Err() != nil {
		// Interrupted mid-add: it may have reached the provider. It counts for pacing (reserved above); the row
		// stays adding, for reconcileAdding.
		return 0, time.Time{}, Step{}, fmt.Errorf("router: add to %s interrupted: %w", pk, context.Cause(ctx))
	}
	unsent := notSent(aerr)
	if unsent {
		s.sent = false // the gateway refused it here: no add reached the provider, and no deferral is counted
	}
	// A 451 blocks its hash here for miss_ttl, and holds the account only when a hash of another title got one
	// within holdWindow, or the account is held already (note451): the releases of one title Real-Debrid refuses
	// must not pin the account on the hold ladder. A 451 the hold explains blocks no hash: the release
	// is tried again after the hold.
	is451 := false
	if ae := (*adder.Error)(nil); errors.As(aerr, &ae) && ae.Code == adder.CodeAccountHold {
		is451 = true
	}
	var v verdict
	var retryAt time.Time
	var st Step
	held := false
	var explained []string
	if err := updatePace(rctx, r.st, pk, now, func(tx *sql.Tx, q *pace) (bool, error) {
		if unsent {
			q.unadd(before, after)
		}
		held, explained = false, nil
		if is451 {
			var err error
			if held, explained, err = note451(rctx, tx, pk, hash, s.req.TitleID, now, r.set.MissTTL,
				q.HoldUntil.After(now)); err != nil {
				return false, err
			}
		}
		v, retryAt, st = r.classify(q, pv, now, hash, aerr, held)
		return true, nil
	}); err != nil {
		return 0, time.Time{}, Step{}, err
	}
	if is451 && held {
		if err := r.liftMisses(rctx, pk, explained); err != nil {
			return 0, time.Time{}, Step{}, err
		}
	}
	if is451 && !held {
		if err := r.setCache(rctx, hash, pk, "miss", "451", now.Add(r.set.MissTTL), now); err != nil {
			return 0, time.Time{}, Step{}, err
		}
		if n, err := title451s(rctx, r.st.Writer(), pk, s.req.TitleID, now.Add(-r.set.MissTTL)); err != nil {
			return 0, time.Time{}, Step{}, err
		} else if n == title451Limit {
			r.log.Warn("the provider refused several releases of this title with 451; the title is not tried there "+
				"until they expire", "provider", pk, "title", s.req.TitleID, "version", s.req.Version, "hashes", n,
				"miss_ttl", r.set.MissTTL)
		}
	}
	if v == vContent && st.Code == string(adder.CodeNotCached) {
		if err := r.setCache(rctx, hash, pk, "uncached", "add", now.Add(r.verdictTTL(rctx, s, now)), now); err != nil {
			return 0, time.Time{}, Step{}, err
		}
	}
	if aerr != nil && addUncertain(aerr) {
		// The answer does not say whether the provider took the add: the row stays in adding and nothing more is
		// added for this download (no other provider either) until the account settles it. The route is deferred
		// past addSettle; its retry reconciles first.
		why := "the answer does not say whether " + pk + " took the add (" + aerr.Error() + ")"
		r.log.Warn("add outcome unknown; settled from the account before anything is added again", "provider", pk,
			"release", c.Release.Title, "hash", hash, "download", s.dlID, "err", aerr.Error())
		until := now.Add(addSettle)
		if retryAt.After(until) {
			until = retryAt
		}
		if err := r.noteUnsettled(rctx, s.dlID, attemptOf(attempt), why,
			r.continuation(rctx, s, c, s.dlID, until)); err != nil {
			return 0, time.Time{}, Step{}, err
		}
		return vPending, until, Step{Outcome: "error", Code: codeUnsettled, Message: why + "; settled from the " +
			"account before anything is added again", RetryAt: until}, nil
	}
	if aerr != nil {
		if err := r.unmarkAdding(rctx, s.dlID, attempt); err != nil {
			return 0, time.Time{}, Step{}, err
		}
		r.log.Info("add refused", "provider", pk, "release", c.Release.Title, "hash", hash, "code", st.Code,
			"err", aerr.Error())
		return v, retryAt, st, nil
	}
	s.res.TorrentID = added.ID
	r.log.Info("added", "provider", pk, "release", c.Release.Title, "hash", hash, "torrent", added.ID,
		"ready", added.Ready, "version", s.req.Version, "download", s.dlID)
	fence := fromAdding(attemptOf(attempt))
	// The second selection's add records its intent with the torrent: a selection that fails in the
	// route's reads is sent by the poller, after a restart too, as the largest video alone. markAdding wrote the
	// intent before the add, so an adoption by reconciliation keeps it too.
	fence.alt = alt
	if err := r.markAdded(rctx, s.dlID, pk, added, false, fence); err != nil {
		if errors.Is(err, errAddTaken) {
			// Another route settled this download while the add was out (this run lost its lease), or it was
			// removed (Dismiss): its result stands. The torrent stays on the account; nothing is deleted.
			r.log.Error("add answered after the download was taken over or ended; its result stands, nothing is "+
				"deleted; the torrent is left on the account unrecorded", "provider", pk, "torrent", added.ID,
				"hash", hash, "download", s.dlID)
			if state, serr := r.downloadState(rctx, s.dlID); serr == nil && state == StateFailed {
				// Ended while the add was out: the route stops here. A job retry would find no
				// download in flight and add the removed release again under a new row.
				err = fmt.Errorf("%w: %w", errMovedOn, err)
			}
		}
		return 0, time.Time{}, Step{}, err
	}
	if guard != nil {
		if err := r.recordOwnAdd(rctx, s.dlID, guard, pv, pk, hash, added); err != nil {
			return 0, time.Time{}, Step{}, err
		}
		if r.set.CachedOnly {
			return r.settle(ctx, s, pv, pk, hash, ad, added, guard)
		}
	}
	// Real-Debrid waits for a file selection; one status read now saves a poll when the files are known at once.
	if ad.SelectsFiles() {
		r.trySelect(ctx, s.dlID, s.req.Version, s.vp.Kind, pk, added.ID)
	}
	return vAccepted, time.Time{}, st, nil
}

// notSent reports whether err is the gateway's refusal of a request it never sent (any but ReasonTooLarge, which
// comes after the answer).
func notSent(err error) bool {
	var refused *gateway.Refused
	return errors.As(err, &refused) && refused.Reason != gateway.ReasonTooLarge
}

// addUncertain reports whether a failed add may still have been taken by the provider: anything but a provider's
// own refusal (an error answer: 4xx, or a code it gave) or a request the gateway never sent. A transport error, a
// 5xx, a body cut short or too large, or a success answer without a torrent id may follow an accepted add. An error
// envelope with a code (adder.Error.Refused: AllDebrid and TorBox answer errors with HTTP 200) is a refusal even when
// we do not know the code.
func addUncertain(err error) bool {
	var e *adder.Error
	if !errors.As(err, &e) {
		return true
	}
	if e.Refused && e.Status < 500 {
		return false
	}
	var refused *gateway.Refused
	if errors.As(e.Err, &refused) {
		return refused.Reason == gateway.ReasonTooLarge // refused after the answer came: the request was sent
	}
	switch e.Code {
	case adder.CodeUnavailable, adder.CodeUnknown:
		return e.Status < 400 || e.Status >= 500
	}
	return false
}

// classify turns an add's error into a verdict, updating pacing. A nil error is accepted. held: a 451
// (CodeAccountHold) that holds the account (note451); without it a 451 is a verdict on the release here.
func (r *Router) classify(p *pace, pv *policy.Provider, now time.Time, hash string, err error, held bool) (verdict,
	time.Time, Step) {
	pc := pv.EffectivePacing()
	if err == nil {
		p.ok()
		return vAccepted, time.Time{}, Step{Outcome: "accepted", Code: "added"}
	}
	var e *adder.Error
	if !errors.As(err, &e) {
		until := p.fail(now, pc, "error", time.Time{}, false, true) // not an answer: transport
		return vUnavailable, until, Step{Outcome: "error", Code: string(adder.CodeUnknown), Message: err.Error(),
			RetryAt: until}
	}
	st := Step{Code: string(e.Code), Message: e.Error()}
	switch e.Code {
	case adder.CodeNotCached:
		p.ok()
		p.uncached(now)
		st.Outcome = "refused"
		return vContent, time.Time{}, st
	case adder.CodeBlocked, adder.CodeInvalid:
		p.ok()
		st.Outcome = "refused"
		return vContent, time.Time{}, st
	case adder.CodeAccountHold:
		if !held {
			p.ok()
			st.Outcome = "refused"
			st.Message += "; blocked here for miss_ttl, the account is not held (one hash, or releases of one title)"
			return vContent, time.Time{}, st
		}
		st.Outcome, st.RetryAt = "refused", p.hold(now, pc, e.RetryAt)
		st.Message += "; the account is held (a 451 on another title's release within " + holdWindow.String() +
			", or during a hold); the release is tried again after it"
		return vUnavailable, st.RetryAt, st
	case adder.CodeTrafficExceeded, adder.CodeAuth:
		st.Outcome, st.RetryAt = "refused", p.fail(now, pc, string(e.Code), e.RetryAt, true, false)
		return vUnavailable, st.RetryAt, st
	case adder.CodeTooManyActive, adder.CodeRateLimited:
		st.Outcome, st.RetryAt = "refused", p.fail(now, pc, string(e.Code), e.RetryAt, false, false)
		return vUnavailable, st.RetryAt, st
	}
	// A request the gateway refused before sending it (a spent budget, its own back-off, the VPN guard) is no
	// provider failure: wait for its RetryAt (else BackoffBase) without backing the provider off.
	if refused := (*gateway.Refused)(nil); errors.As(err, &refused) && refused.Reason != gateway.ReasonTooLarge {
		until := e.RetryAt
		if refused.RetryAt.After(until) {
			until = refused.RetryAt
		}
		if !until.After(now) {
			until = now.Add(BackoffBase)
		}
		st.Outcome, st.RetryAt = "deferred", until
		return vUnavailable, until, st
	}
	// Unavailable, unknown: back off; down when the provider did not answer, answered with a 5xx or said it is
	// unavailable (ProviderDown).
	st.Outcome, st.RetryAt = "error", p.fail(now, pc, string(e.Code), e.RetryAt, false, ProviderDown(e))
	return vUnavailable, st.RetryAt, st
}

// ProviderDown says whether an add's failure means the provider is down rather than that it refused or paced the
// add: no answer (status 0: a transport error), a 5xx, or a code saying the provider is unavailable (Real-Debrid 6
// and 25, AllDebrid MAINTENANCE, TorBox's server errors). AllDebrid's GENERIC and MAGNET_FILE_UPLOAD_FAILED are
// failures of the upload and an unrecognised answer is a refusal: neither is an outage.
func ProviderDown(e *adder.Error) bool {
	if e == nil {
		return false
	}
	switch {
	case e.Status == 0 || e.Status >= 500:
		return e.Code == adder.CodeUnavailable || e.Code == adder.CodeUnknown
	case e.Code != adder.CodeUnavailable:
		return false
	}
	return e.ProviderCode != "GENERIC" && e.ProviderCode != "MAGNET_FILE_UPLOAD_FAILED"
}

func cachedNote(c *Candidate, kind string) string {
	if via, ok := c.Release.CachedCodes()[indexer.CacheCode(kind)]; ok {
		return " (cache evidence says it is cached here: " + via + ")"
	}
	return ""
}
