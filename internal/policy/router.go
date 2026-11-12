package policy

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Router configures the download router (internal/router, docs/design/router.md). It acts
// only for versions in mode active, and only while Enabled is true: Enabled is the global kill switch and is off
// unless the policy turns it on. Every other field is optional.
type Router struct {
	// Enabled lets the router write to debrid providers (add a magnet, select files). Default false: every version
	// is then dry-run only, whatever its mode.
	Enabled bool `yaml:"enabled"`
	// CachedOnly skips a provider where a release is known to be uncached (an earlier refusal, a cache check) and asks
	// TorBox to add only if cached. On Real-Debrid and AllDebrid an add is read back at once: one that is not ready
	// within CachedGrace is deleted again when it is provably ours (on AllDebrid only with AllDebridDeleteUncached),
	// and the next provider is tried. Default true.
	CachedOnly *bool `yaml:"cached_only"`
	// AllDebridDeleteUncached lets "Only add cached releases" delete an uncached AllDebrid add the router believes is
	// its own. Default false: the add is kept, it finishes downloading on AllDebrid, and the next provider is tried. The reason is a race the router cannot close: AllDebrid answers an upload of a hash already on the
	// account with the existing magnet's id, and the router's proof that the magnet is new is a list read made just
	// before the upload. Another client adding the same hash between that read and our upload hands us its magnet,
	// which would then look like ours and be deleted, though debrid torrents are shared and never deleted. Real-Debrid
	// creates a new torrent id per add, so its delete needs no switch. The race is acceptable only on an account
	// nothing else writes to, so the delete is an opt-in.
	AllDebridDeleteUncached bool `yaml:"alldebrid_delete_uncached"`
	// CachedGrace is how long the router waits before reading a fresh Real-Debrid or AllDebrid add again when the
	// first read shows it still converting or downloading (one re-read; on Real-Debrid also after selecting files),
	// before it calls the add uncached. Default 2s (decypharr's), 1s to 30s.
	CachedGrace Duration `yaml:"cached_grace"`
	// StalledAfter fails a download still in state added (accepted, not ready, nothing fetched yet) this long after
	// its add, as stalled (decypharr's remove_stalled_after): its hash is blocklisted for the version, our own
	// Real-Debrid add is deleted, and the title is searched again. Default 10m, 1m to 24h; 0s turns it off.
	// providers.<key>.stalled_after overrides it per provider; AllDebrid defaults to at least 25m
	// (Provider.EffectiveStalledAfter).
	StalledAfter *Duration `yaml:"stalled_after"`
	// DownloadingTimeout fails a download in state downloading whose provider progress has not risen for this long
	// (a dead swarm, a magnet that never leaves the provider's queue): its hash is blocklisted for the version, our
	// own Real-Debrid add is deleted as a stalled one is, and the title is searched again. Default 24h, 1h to 7d; 0s
	// turns it off.
	DownloadingTimeout *Duration `yaml:"downloading_timeout"`
	// ReadyTimeout fails a download still ready this long after ready_at, blocklists it and searches again.
	// Default 6h, 1m to 7d. No torrent is deleted on the provider.
	ReadyTimeout Duration `yaml:"ready_timeout"`
	// MissTTL is how long a release missed on every provider stays a miss. Default 6h, 10m to 7d.
	MissTTL Duration `yaml:"miss_ttl"`
	// PollInterval is how often added downloads are read back from their provider. Default 1m, 15s to 1h.
	PollInterval Duration `yaml:"poll_interval"`
	// MaxCandidates caps how many accepted releases of one search are tried, best first. Default 5, 1 to 50.
	MaxCandidates int `yaml:"max_candidates"`
	// Trackers lists public tracker announce URLs added to every debrid magnet. Default none: the magnet is bare.
	// Each must be udp, http or https with no credentials, no query and a path of "" or /announce.
	Trackers []string `yaml:"trackers"`
	// Wanted is the search loop for the active versions' missing and cutoff-unmet titles and episodes.
	Wanted *Wanted `yaml:"wanted"`
	// CacheEvidence is what the router and the release views know about which providers hold a release cached.
	CacheEvidence *CacheEvidence `yaml:"cache_evidence"`
}

// CacheEvidence configures cache evidence (docs/design/router.md "Cache evidence"): DMM's cached feeds and TorBox's own
// cache check say which providers hold a hash cached. Evidence orders the providers a release is tried on; its
// absence never skips one. Every field is optional.
type CacheEvidence struct {
	// TTL is how long a provider's answer that a release is cached is remembered, per release and provider. Default
	// 24h, 1h to 7d.
	TTL Duration `yaml:"ttl"`
	// TorBoxCheck asks TorBox, with our own key, whether the best releases of a search are cached.
	TorBoxCheck *TorBoxCheck `yaml:"torbox_check"`
}

// TorBoxCheck bounds the search-time TorBox cache check. It is a read through the gateway (the provider's budget
// applies too), only for hashes with no live TorBox evidence.
type TorBoxCheck struct {
	// Enabled turns it on. Default true; it does nothing without a TorBox provider whose key is set.
	Enabled *bool `yaml:"enabled"`
	// Top is how many hashes of one search are checked, best first. Default 10, 1 to 100.
	Top int `yaml:"top"`
	// PerHour caps checkcached requests in any hour. Default 60, 1 to 1000.
	PerHour int `yaml:"per_hour"`
}

// Wanted paces the wanted search loop (internal/acquire): every Interval it searches the active versions' missing
// and cutoff-unmet items, at most PerHour searches in any hour, an item no sooner than AgainAfter since its last
// search, and hands the accepted releases to the router. It runs only while router.enabled is true. Every field is
// optional.
type Wanted struct {
	// Enabled turns the loop on. Default: on when some library is active, off otherwise.
	Enabled *bool `yaml:"enabled"`
	// Interval is the time between rounds. Default 15m, 5m to 24h.
	Interval Duration `yaml:"interval"`
	// PerHour caps the loop's searches (one title, season or episode each) in any hour. Default 10, 1 to 200.
	PerHour int `yaml:"per_hour"`
	// AgainAfter is how long an item waits before it is searched again. Default 24h, 1h to 30d.
	AgainAfter Duration `yaml:"again_after"`
	// Missing searches items with no file (default true). Which items with a file are searched for an upgrade is the
	// upgrade search target below.
	Missing *bool `yaml:"missing"`
	// CutoffUnmet chooses automatic upgrade searches: quality, score or off.
	CutoffUnmet CutoffMode `yaml:"cutoff_unmet"`
	// RetryNew searches again 1, 2, 4 and 8 hours (and so on) after an automatic search for a missing episode or film
	// that came out in the last 7 days finds nothing, instead of a day later. Each retry counts against the hourly
	// limit. On by default; off waits for the next regular search.
	RetryNew *bool `yaml:"retry_new,omitempty" json:",omitempty"`
}

// CutoffMode is router.wanted.cutoff_unmet: which files the wanted loop searches an upgrade for. It changes only the
// loop's selection; version and episode states, feeds (RSS) and every upgrade decision keep the full cutoff, quality
// and custom-format score, as Radarr's and Sonarr's RSS sync and searches do (UpgradableSpecification.CutoffNotMet).
type CutoffMode string

const (
	// CutoffQuality (the default) searches files whose quality is below the profile's quality cutoff, like the arrs'
	// Wanted > Cutoff Unmet list and its search (Radarr MovieCutoffService.MoviesWhereCutoffUnmet, Sonarr
	// EpisodeCutoffService.EpisodesWhereCutoffUnmet, used by CutoffUnmetMoviesSearch / CutoffUnmetEpisodeSearch).
	// A file at the cutoff quality but below the cutoff format score is left to feeds and other searches.
	CutoffQuality CutoffMode = "quality"
	// CutoffScore searches every file below the full cutoff (state filled): also those below the cutoff score.
	CutoffScore CutoffMode = "score"
	// CutoffOff searches no file for an upgrade.
	CutoffOff CutoffMode = "off"
)

// UnmarshalYAML takes quality, score or off, and the booleans of the earlier schema: true is quality, false off.
func (m *CutoffMode) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!bool" {
		var b bool
		if err := n.Decode(&b); err != nil {
			return err
		}
		*m = map[bool]CutoffMode{true: CutoffQuality, false: CutoffOff}[b]
		return nil
	}
	var s string
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("line %d: cutoff_unmet: %w", n.Line, err)
	}
	*m = CutoffMode(strings.ToLower(strings.TrimSpace(s)))
	return nil
}

func (m CutoffMode) valid() bool {
	return m == "" || m == CutoffQuality || m == CutoffScore || m == CutoffOff
}

// Router defaults and limits.
const (
	DefaultRouterMissTTL            = 6 * time.Hour
	MinRouterMissTTL                = 10 * time.Minute
	MaxRouterMissTTL                = 7 * 24 * time.Hour
	DefaultRouterPollInterval       = time.Minute
	MinRouterPollInterval           = 15 * time.Second
	MaxRouterPollInterval           = time.Hour
	DefaultRouterMaxCandidates      = 5
	MaxRouterMaxCandidates          = 50
	DefaultRouterCachedGrace        = 2 * time.Second
	MinRouterCachedGrace            = time.Second
	MaxRouterCachedGrace            = 30 * time.Second
	DefaultRouterStalledAfter       = 10 * time.Minute
	MinRouterStalledAfter           = time.Minute
	MaxRouterStalledAfter           = 24 * time.Hour
	DefaultRouterDownloadingTimeout = 24 * time.Hour
	MinRouterDownloadingTimeout     = time.Hour
	MaxRouterDownloadingTimeout     = 7 * 24 * time.Hour
	DefaultRouterReadyTimeout       = 6 * time.Hour
	MinRouterReadyTimeout           = time.Minute
	MaxRouterReadyTimeout           = 7 * 24 * time.Hour

	DefaultWantedInterval   = 15 * time.Minute
	MinWantedInterval       = 5 * time.Minute
	MaxWantedInterval       = 24 * time.Hour
	DefaultWantedPerHour    = 10
	MaxWantedPerHour        = 200
	DefaultWantedAgainAfter = 24 * time.Hour
	MinWantedAgainAfter     = time.Hour
	MaxWantedAgainAfter     = 30 * 24 * time.Hour

	DefaultCacheEvidenceTTL = 24 * time.Hour
	MinCacheEvidenceTTL     = time.Hour
	MaxCacheEvidenceTTL     = 7 * 24 * time.Hour
	DefaultTorBoxCheckTop   = 10
	MaxTorBoxCheckTop       = 100
	DefaultTorBoxCheckHour  = 60
	MaxTorBoxCheckHour      = 1000
)

// CacheEvidenceSettings is router.cache_evidence with every default applied. UncachedTTL is router.miss_ttl: a
// check that says uncached is kept as long as the router keeps its own.
type CacheEvidenceSettings struct {
	TTL          time.Duration
	UncachedTTL  time.Duration
	CheckEnabled bool
	CheckTop     int
	CheckPerHour int
}

// CacheEvidenceSettings returns router.cache_evidence with defaults.
func (p *Policy) CacheEvidenceSettings() CacheEvidenceSettings {
	s := CacheEvidenceSettings{TTL: DefaultCacheEvidenceTTL, UncachedTTL: p.RouterSettings().MissTTL,
		CheckEnabled: true, CheckTop: DefaultTorBoxCheckTop, CheckPerHour: DefaultTorBoxCheckHour}
	if p.Router == nil || p.Router.CacheEvidence == nil {
		return s
	}
	c := p.Router.CacheEvidence
	if c.TTL != 0 {
		s.TTL = c.TTL.D()
	}
	if t := c.TorBoxCheck; t != nil {
		if t.Enabled != nil {
			s.CheckEnabled = *t.Enabled
		}
		if t.Top != 0 {
			s.CheckTop = t.Top
		}
		if t.PerHour != 0 {
			s.CheckPerHour = t.PerHour
		}
	}
	return s
}

// WantedSettings is router.wanted with every default applied.
type WantedSettings struct {
	Enabled     bool
	Interval    time.Duration
	PerHour     int
	AgainAfter  time.Duration
	Missing     bool
	CutoffUnmet CutoffMode // never ""
	RetryNew    bool
}

// WantedSettings returns router.wanted with defaults: on when some version is active (and the section does not turn
// it off), but it searches only while router.enabled is true.
func (p *Policy) WantedSettings() WantedSettings {
	s := WantedSettings{Enabled: len(p.ActiveVersions()) > 0, Interval: DefaultWantedInterval,
		PerHour: DefaultWantedPerHour, AgainAfter: DefaultWantedAgainAfter, Missing: true, CutoffUnmet: CutoffQuality,
		RetryNew: true}
	if p.Router == nil || p.Router.Wanted == nil {
		return s
	}
	w := p.Router.Wanted
	if w.Enabled != nil {
		s.Enabled = *w.Enabled
	}
	if w.Interval != 0 {
		s.Interval = w.Interval.D()
	}
	if w.PerHour != 0 {
		s.PerHour = w.PerHour
	}
	if w.AgainAfter != 0 {
		s.AgainAfter = w.AgainAfter.D()
	}
	if w.Missing != nil {
		s.Missing = *w.Missing
	}
	if w.CutoffUnmet != "" {
		s.CutoffUnmet = w.CutoffUnmet
	}
	if w.RetryNew != nil {
		s.RetryNew = *w.RetryNew
	}
	return s
}

// RouterSettings is the router section with every default applied.
type RouterSettings struct {
	Enabled    bool
	CachedOnly bool
	// AllDebridDeleteUncached: cached_only may delete an uncached AllDebrid add of ours (default false).
	AllDebridDeleteUncached bool
	MissTTL                 time.Duration
	PollInterval            time.Duration
	MaxCandidates           int
	Trackers                []string
	CachedGrace             time.Duration
	// StalledAfter is 0 when the stall timeout is off.
	StalledAfter time.Duration
	// DownloadingTimeout is 0 when the no-progress timeout is off.
	DownloadingTimeout time.Duration
	ReadyTimeout       time.Duration
}

// RouterSettings returns the router section with defaults. With no section the router is disabled.
func (p *Policy) RouterSettings() RouterSettings {
	s := RouterSettings{CachedOnly: true, MissTTL: DefaultRouterMissTTL, PollInterval: DefaultRouterPollInterval,
		MaxCandidates: DefaultRouterMaxCandidates, CachedGrace: DefaultRouterCachedGrace,
		StalledAfter: DefaultRouterStalledAfter, DownloadingTimeout: DefaultRouterDownloadingTimeout,
		ReadyTimeout: DefaultRouterReadyTimeout}
	c := p.Router
	if c == nil {
		return s
	}
	s.Enabled = c.Enabled
	s.AllDebridDeleteUncached = c.AllDebridDeleteUncached
	if c.CachedOnly != nil {
		s.CachedOnly = *c.CachedOnly
	}
	if c.MissTTL != 0 {
		s.MissTTL = c.MissTTL.D()
	}
	if c.PollInterval != 0 {
		s.PollInterval = c.PollInterval.D()
	}
	if c.MaxCandidates != 0 {
		s.MaxCandidates = c.MaxCandidates
	}
	if c.CachedGrace != 0 {
		s.CachedGrace = c.CachedGrace.D()
	}
	if c.StalledAfter != nil {
		s.StalledAfter = c.StalledAfter.D()
	}
	if c.DownloadingTimeout != nil {
		s.DownloadingTimeout = c.DownloadingTimeout.D()
	}
	if c.ReadyTimeout != 0 {
		s.ReadyTimeout = c.ReadyTimeout.D()
	}
	s.Trackers = append([]string(nil), c.Trackers...)
	return s
}

// ActiveVersions lists the versions in mode active, sorted. The router writes for these only, and only while
// router.enabled is true.
func (p *Policy) ActiveVersions() []string {
	var out []string
	for _, k := range sortedKeys(p.Versions) {
		if p.Versions[k].Mode == ModeActive {
			out = append(out, k)
		}
	}
	return out
}

// Grabs reports whether the router may add torrents for a version: router.enabled is true, the version is active
// and its grab switch is on. Router.Gate, the offer gate, hold approval and the status read use it.
func (p *Policy) Grabs(version string) bool {
	v := p.Versions[version]
	return p.RouterSettings().Enabled && v != nil && v.Mode == ModeActive && v.Grab
}

// GrabbingVersions lists the active versions whose grab switch is on, sorted. With router.enabled true the router
// writes for these only.
func (p *Policy) GrabbingVersions() []string {
	var out []string
	for _, k := range sortedKeys(p.Versions) {
		if v := p.Versions[k]; v.Mode == ModeActive && v.Grab {
			out = append(out, k)
		}
	}
	return out
}

// SearchVersions lists the versions automatic searches (the wanted loop, add-time and re-searches) run for: the ones
// Grabs lets the router write for, sorted. None while router.enabled is false. A version that is active with grab off
// would only get dry runs, so it spends no search traffic. RSS is not limited by it: the feeds are read for the
// shadowed versions anyway, and an active version's picks get dry-run plans (shadow.Runner.ActiveVersions).
// User-invoked searches are not limited by it either.
func (p *Policy) SearchVersions() []string {
	if !p.RouterSettings().Enabled {
		return nil
	}
	return p.GrabbingVersions()
}

func (p *Policy) validateRouter(e *Errors) {
	c := p.Router
	if c == nil {
		return
	}
	if d := c.MissTTL.D(); d != 0 && (d < MinRouterMissTTL || d > MaxRouterMissTTL) {
		e.add("router.miss_ttl", "%v is outside %v to %v", d, MinRouterMissTTL, MaxRouterMissTTL)
	}
	if d := c.PollInterval.D(); d != 0 && (d < MinRouterPollInterval || d > MaxRouterPollInterval) {
		e.add("router.poll_interval", "%v is outside %v to %v", d, MinRouterPollInterval, MaxRouterPollInterval)
	}
	if d := c.CachedGrace.D(); d != 0 && (d < MinRouterCachedGrace || d > MaxRouterCachedGrace) {
		e.add("router.cached_grace", "%v is outside %v to %v", d, MinRouterCachedGrace, MaxRouterCachedGrace)
	}
	if c.StalledAfter != nil {
		if d := c.StalledAfter.D(); d != 0 && (d < MinRouterStalledAfter || d > MaxRouterStalledAfter) {
			e.add("router.stalled_after", "%v is outside %v to %v (0s turns it off)", d, MinRouterStalledAfter,
				MaxRouterStalledAfter)
		}
	}
	if c.DownloadingTimeout != nil {
		if d := c.DownloadingTimeout.D(); d != 0 && (d < MinRouterDownloadingTimeout || d > MaxRouterDownloadingTimeout) {
			e.add("router.downloading_timeout", "%v is outside %v to %v (0s turns it off)", d,
				MinRouterDownloadingTimeout, MaxRouterDownloadingTimeout)
		}
	}
	if c.MaxCandidates < 0 || c.MaxCandidates > MaxRouterMaxCandidates {
		e.add("router.max_candidates", "must be 1 to %d, got %d", MaxRouterMaxCandidates, c.MaxCandidates)
	}
	if d := c.ReadyTimeout.D(); d != 0 && (d < MinRouterReadyTimeout || d > MaxRouterReadyTimeout) {
		e.add("router.ready_timeout", "%v is outside %v to %v", d, MinRouterReadyTimeout, MaxRouterReadyTimeout)
	}
	if w := c.Wanted; w != nil {
		if d := w.Interval.D(); d != 0 && (d < MinWantedInterval || d > MaxWantedInterval) {
			e.add("router.wanted.interval", "%v is outside %v to %v", d, MinWantedInterval, MaxWantedInterval)
		}
		if w.PerHour < 0 || w.PerHour > MaxWantedPerHour {
			e.add("router.wanted.per_hour", "must be 1 to %d, got %d", MaxWantedPerHour, w.PerHour)
		}
		if d := w.AgainAfter.D(); d != 0 && (d < MinWantedAgainAfter || d > MaxWantedAgainAfter) {
			e.add("router.wanted.again_after", "%v is outside %v to %v", d, MinWantedAgainAfter, MaxWantedAgainAfter)
		}
		if !w.CutoffUnmet.valid() {
			e.add("router.wanted.cutoff_unmet", "must be quality, score or off, got %q", string(w.CutoffUnmet))
		}
	}
	if ce := c.CacheEvidence; ce != nil {
		if d := ce.TTL.D(); d != 0 && (d < MinCacheEvidenceTTL || d > MaxCacheEvidenceTTL) {
			e.add("router.cache_evidence.ttl", "%v is outside %v to %v", d, MinCacheEvidenceTTL, MaxCacheEvidenceTTL)
		}
		if t := ce.TorBoxCheck; t != nil {
			if t.Top < 0 || t.Top > MaxTorBoxCheckTop {
				e.add("router.cache_evidence.torbox_check.top", "must be 1 to %d, got %d", MaxTorBoxCheckTop, t.Top)
			}
			if t.PerHour < 0 || t.PerHour > MaxTorBoxCheckHour {
				e.add("router.cache_evidence.torbox_check.per_hour", "must be 1 to %d, got %d", MaxTorBoxCheckHour,
					t.PerHour)
			}
		}
	}
	for i, t := range c.Trackers {
		if why := publicTrackerProblem(t); why != "" {
			e.add(fmt.Sprintf("router.trackers[%d]", i), "%s", why)
		}
	}
}

// publicTrackerProblem says why a tracker URL may not go into a debrid magnet, or "".
func publicTrackerProblem(t string) string {
	u, err := url.Parse(strings.TrimSpace(t))
	if err != nil {
		return "not a URL"
	}
	switch u.Scheme {
	case "udp", "http", "https":
	default:
		return "scheme must be udp, http or https"
	}
	if u.Hostname() == "" {
		return "no host"
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "credentials, a query or a fragment can carry a passkey; public trackers need none"
	}
	if p := strings.TrimSuffix(u.Path, "/"); p != "" && p != "/announce" {
		return "path must be empty or /announce (a longer path can carry a passkey)"
	}
	return ""
}
