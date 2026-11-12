// Package policy loads and validates the YAML policy: versions, profiles, formats, indexers, providers, clients,
// views, budgets and metadata settings (docs/design/overview.md). YAML is the import/export representation;
// internal/settings overlays database-owned sections for the live policy.
//
// # Layout
//
// A policy is a directory. Every *.yaml or *.yml file directly in it is read, and their top-level sections are
// merged; how sections are spread over files is up to the author. Subdirectories are not read as policy (the
// metadata overrides directory holds a different format, read by the anime engine). A key defined in two files is an error.
//
// # Strictness
//
// Decoding is strict: an unknown field is an error, as is a second YAML document in one file. Secrets never appear
// inline: fields ending in _env name an environment variable that holds the secret. [Load] decodes and validates;
// every problem is reported with its path, e.g. `versions[movie-web].profile: unknown profile "x"`.
//
// # Private trackers
//
// An indexer's privacy is a label (badges, filters, sorting); nothing branches on it. Which indexers feed a version,
// and so which providers their releases reach, is the user's choice: the version's indexers list, narrowed per indexer
// by indexers.K.providers.
package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// Policy is a whole policy directory after merging its files.
type Policy struct {
	// ForcedOff names emergency environment overrides; never read from YAML.
	ForcedOff []string `yaml:"-" json:"forced_off,omitempty"`
	// ForceOffUnknown lists override entries ("VAR: name") that are not switches, for warnings; never read from YAML.
	ForceOffUnknown []string                  `yaml:"-" json:"force_off_unknown,omitempty"`
	Versions        map[string]*VersionPolicy `yaml:"versions"`
	Profiles        map[string]*Profile       `yaml:"profiles"`
	Formats         map[string]*FormatRef     `yaml:"formats"`
	FormatSets      map[string]*FormatSet     `yaml:"format_sets"`
	Indexers        map[string]*Indexer       `yaml:"indexers"`
	Providers       map[string]*Provider      `yaml:"providers"`
	Clients         map[string]*Client        `yaml:"clients"`
	Views           map[string]*View          `yaml:"views"`
	Budgets         map[string]*Budget        `yaml:"budgets"`
	Metadata        *Metadata                 `yaml:"metadata"`
	Import          *Import                   `yaml:"import"`
	Egress          *Egress                   `yaml:"egress"`
	// ProviderSync schedules the provider registry sync in theatarr serve.
	ProviderSync *ProviderSync `yaml:"provider_sync"`
	// Shadow tunes the live shadow runner in theatarr serve.
	Shadow *Shadow `yaml:"shadow"`
	// Library configures imports into the library roots, relinks and Plex scans. Off unless library.enabled.
	Library *Library `yaml:"library"`
	// Router configures the download router; it is off unless router.enabled is true.
	Router *Router `yaml:"router"`
	// Playlists configures the anime watch-order playlists in Plex; off unless playlists.enabled and library.enabled.
	Playlists *Playlists `yaml:"playlists"`
	// Notify configures outbound webhooks (arr-shaped per view, and native events); nothing is sent unless enabled.
	Notify *Notify `yaml:"notify"`
	// UI holds the web UI's server-side settings: the image proxy's disk cache (art.go).
	UI *UI `yaml:"ui"`
	// Cardigann configures the built-in indexer catalog (cardigann.go). Nothing is fetched unless it is set.
	Cardigann *Cardigann `yaml:"cardigann"`
	// Torrents configures the provider torrent manager (torrents.go): extra roots its usage check scans and the
	// scheduled backups. Optional; the manager works without it.
	Torrents *Torrents `yaml:"torrents"`

	// Dir is the policy directory, absolute.
	Dir string `yaml:"-"`
	// Files lists the files read, relative to Dir, sorted.
	Files []string `yaml:"-"`
	// Revision identifies the policy files as read: the first 12 hex digits of a SHA-256 over each file's name and
	// bytes, in name order. Journal decisions, acquire runs and downloads carry it, so a decision can be tied to the
	// policy that made it. Files under subdirectories (overrides) are not part of it.
	Revision string `yaml:"-"`
	// Ignored names removed settings found in the policy files or the settings database and dropped (removed.go),
	// for a note at startup.
	Ignored []string `yaml:"-"`
	// origin maps "section[key]" to the file that defined it.
	origin map[string]string
	// secrets are the settings database's credentials of the generation this policy belongs to, by
	// SettingsSecretPrefix name (WithSettingsSecrets). Never changed once the policy is shared.
	secrets map[string]string
	// copyOf is the policy directory Dir is a copy of (CopyOf), or empty.
	copyOf string
}

// Kind of title a version applies to.
type Kind string

const (
	KindMovie  Kind = "movie"
	KindSeries Kind = "series"
)

// Mode is a version's cutover switch (docs/design/shadow.md): shadow decides and reports but grabs nothing.
type Mode string

const (
	ModeShadow Mode = "shadow"
	ModeActive Mode = "active"
)

// Storage is where a version's files live.
type Storage string

const (
	StorageDebrid  Storage = "debrid"
	StorageSeedbox Storage = "seedbox"
)

// Ceiling configures the display-only source ceiling.
type Ceiling struct {
	DiscOnly bool `yaml:"disc_only"`
}

// VersionPolicy defines a version (docs/design/overview.md): which titles get it, how releases are judged, where files go.
type VersionPolicy struct {
	Ceiling Ceiling `yaml:"ceiling"`
	// Name is an optional display name for people; empty uses the label guessed from the key and profile. It names
	// nothing an arr client sees: keys, paths and arr-view profile names do not come from it.
	Name string `yaml:"name,omitempty" json:",omitempty"`
	// MinimumAvailability is the minimum availability of a movie in this library that has neither an explicit
	// choice nor an imported value: announced, inCinemas or released; unset is released. An add from an arr client
	// does not store the client's value for a library that sets it. Movie libraries only.
	MinimumAvailability string `yaml:"minimum_availability,omitempty" json:",omitempty"`
	// AvailabilityDelay is Radarr's availability delay in days for this library's movies: added to the date the
	// minimum availability compares against, negative for earlier. Announced movies and movies without a date are
	// not affected. Movie libraries only.
	AvailabilityDelay int `yaml:"availability_delay,omitempty" json:",omitempty"`
	// Mode is shadow (the default) or active.
	Mode Mode `yaml:"mode"`
	// Grab lets the router add torrents for the version (docs/design/decide.md). The router writes for a version
	// only when router.enabled is true, the version is active and Grab is true; false (the default) keeps an active
	// version searching, deciding and linking but every route is a dry-run plan. The library gate does not read it.
	Grab bool `yaml:"grab,omitempty"`
	// Kind is movie or series.
	Kind Kind `yaml:"kind"`
	// Anime limits the version to anime (true) or non-anime (false) titles. Unset means both.
	Anime *bool `yaml:"anime"`
	// Enable is when a title gets this version: "default" (every matching title) or "request" (only when a view's
	// profile asks for it).
	Enable string `yaml:"enable"`
	// Profile names a profile.
	Profile string `yaml:"profile"`
	// PreferSeaDex ranks known-quality SeaDex picks above non-SeaDex releases, independently of quality order.
	// It may replace a higher-resolution file, while keeping the actual quality and the other admission guards.
	PreferSeaDex bool `yaml:"prefer_seadex,omitempty"`
	// Library is the root folder the version links into, absolute.
	Library string `yaml:"library"`
	// Naming holds arr naming templates.
	Naming Naming `yaml:"naming"`
	// Edition is an optional Plex edition label ({edition-…}); off when empty.
	Edition string `yaml:"edition"`
	// Storage is debrid or seedbox.
	Storage Storage `yaml:"storage"`
	// Indexers the version may search, by key. Required: there is no "all".
	Indexers []string `yaml:"indexers"`
	// Providers is the debrid provider subset, listed in fallback order (providers[*].order). Debrid only.
	Providers []string `yaml:"providers"`
	// ProviderPreference is how the ranking breaks a tie between releases of equal quality and format score by cache
	// evidence: ordered (the default: cached on an earlier provider of Providers first, then on more of them) or none
	// (on more of them only). The route's provider order is the list order either way.
	ProviderPreference string `yaml:"provider_preference,omitempty"`
	// AddVia is who adds the version's grabs to debrid: providers (the default: the router adds to Providers itself)
	// or client (the router sends them to Client, a decypharr client, which picks the account, checks the cache and
	// reports the download complete; docs/design/external-clients.md).
	AddVia string `yaml:"add_via"`
	// Client is the download client key.
	Client string `yaml:"client"`
	// Import is symlink, hardlink or copy. Debrid versions symlink.
	Import string `yaml:"import"`
	// Packs is the season pack rule of a series version: all (the default, Sonarr's: a pack must upgrade every
	// episode's file) or any_upgradable (the actuallyevan fork's ACCEPT_RELEASE_ANY_UPGRADABLE: a pack is taken when
	// any of its episodes has no file or would be upgraded) or threshold (Sonarr v5's SeasonPackUpgrade = Threshold:
	// a full-season pack is taken when at least PacksThreshold percent of its episodes have no file or would be
	// upgraded; other releases keep the all rule).
	Packs string `yaml:"packs"`
	// PacksThreshold is the percentage for packs: threshold (Sonarr v5's SeasonPackUpgradeThreshold), 1 to 100.
	// Required with threshold and refused with any other rule.
	PacksThreshold *float64 `yaml:"packs_threshold"`
	// Sonarr is the Sonarr version a series version parses and maps releases as: v4 (the default: Sonarr
	// v4.0.20.3014, the parser a Sonarr v4 install runs) or v5 (Sonarr v5's parser and mapping: multi-season packs, anime season
	// packs named by season title, original-title matching, the new languages; docs/design/sonarr-v5.md). Series only.
	Sonarr string `yaml:"sonarr"`
	// Upgrades is what happens when a release would replace a file the version already has: auto (the default:
	// routed like any other release) or off (never; the file stays). A release for items with no file is never
	// refused by it (docs/design/router.md "Upgrades").
	Upgrades string `yaml:"upgrades"`
	// Follows makes this a parallel version (docs/parallel.md): it takes the catalog and monitoring of the named
	// version, whose arr instance keeps running, but never its files. It has its own roots and acquires on its own.
	Follows string `yaml:"follows"`
}

// Naming holds arr-style naming templates. Empty means the flavour's default.
type Naming struct {
	Folder string `yaml:"folder"`
	File   string `yaml:"file"`
	Season string `yaml:"season"`
	// Specials is the season 0 folder (Sonarr specialsFolderFormat).
	Specials string `yaml:"specials"`
	// Daily and Anime are Sonarr's daily and anime episode formats; File is the standard one.
	Daily string `yaml:"daily"`
	Anime string `yaml:"anime"`
	// Colon is the colon replacement: delete, dash, space_dash, space_dash_space or smart.
	Colon string `yaml:"colon"`
	// MultiEpisode is Sonarr's multi-episode style: extend, duplicate, repeat, scene, range or prefixed_range.
	MultiEpisode string `yaml:"multi_episode"`
	// FromArr takes the whole naming config (and quality titles) from an import.arr instance's
	// GET /api/v3/config/naming, so links get exactly the names that instance gives. Nothing else may be set with it.
	FromArr string `yaml:"from_arr"`
	// FromVersion takes the whole naming of another version: a parallel version naming its files like the version
	// it follows (versions[k].follows). Nothing else may be set with it.
	FromVersion string `yaml:"from_version"`
}

// Profile is a quality profile with format scores, shared by versions.
type Profile struct {
	// Qualities is ordered best first. An item is a quality name or a group of equal qualities.
	Qualities []QualityItem `yaml:"qualities"`
	// QualityOrder optionally lists every quality, including disabled ones, best first. Groups match Qualities.
	// Without it, disabled qualities keep their default order below the allowed qualities.
	QualityOrder []QualityItem `yaml:"quality_order,omitempty"`
	// Cutoff is a quality (or group) name in Qualities: upgrades stop once it is reached.
	Cutoff string `yaml:"cutoff"`
	// Upgrades allows upgrades at all (arr upgradeAllowed). Default true.
	Upgrades *bool `yaml:"upgrades"`
	// MinScore is the minimum format score to grab (arr minFormatScore).
	MinScore int `yaml:"min_score"`
	// CutoffScore is the "upgrade until" format score (arr cutoffFormatScore).
	CutoffScore int `yaml:"cutoff_score"`
	// MinUpgradeStep is the smallest score gain worth an upgrade (arr minUpgradeFormatScore). Default 1.
	MinUpgradeStep *int `yaml:"min_upgrade_step"`
	// Sizes limits size per quality, in MB per minute of runtime as the arrs measure it.
	Sizes map[string]SizeLimit `yaml:"sizes"`
	// FormatSet limits evaluation and display names to one set. Empty keeps the global formats.
	FormatSet string `yaml:"format_set,omitempty"`
	// Formats maps a format (a key in scope, or its trash_id) to its score.
	Formats map[string]int `yaml:"formats"`
	// Language is the Radarr profile language: any (the default), original (the movie's original language, from
	// TMDb's original_language) or a Radarr language name ("English", "French"). Movie profiles only: Sonarr v4
	// profiles have no language.
	Language string `yaml:"language"`

	// FromArr takes the whole profile from an arr instance's live profile instead: "<instance>/<profile name>",
	// where the instance is a key of import.arr. The profile, its formats and the instance's quality definitions are
	// read from the arr (or a snapshot of it), for shadow parity. Nothing else may be set alongside it but Propers.
	FromArr string `yaml:"from_arr"`
	// Propers is the arrs' "Propers and Repacks" setting the profile is judged with: prefer_and_upgrade (the arr
	// default), do_not_upgrade or do_not_prefer (TRaSH's advice when formats score repacks).
	Propers string `yaml:"propers"`
}

// QualityItem is a quality name, or a group written as {group: name, qualities: [...]}.
type QualityItem struct {
	Name      string
	Group     bool
	Qualities []string
}

func (q QualityItem) MarshalYAML() (any, error) {
	if !q.Group {
		return q.Name, nil
	}
	return struct {
		Group     string   `yaml:"group"`
		Qualities []string `yaml:"qualities"`
	}{q.Name, q.Qualities}, nil
}

func (q *QualityItem) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		q.Name = n.Value
		return n.Decode(&q.Name)
	}
	var g struct {
		Group     string   `yaml:"group"`
		Qualities []string `yaml:"qualities"`
	}
	if err := decodeStrict(n, &g); err != nil {
		return err
	}
	q.Name, q.Group, q.Qualities = g.Group, true, g.Qualities
	return nil
}

// SizeLimit is a size range in MB per minute. Zero Max means no upper limit.
type SizeLimit struct {
	Min       float64 `yaml:"min"`
	Preferred float64 `yaml:"preferred"`
	Max       float64 `yaml:"max"`
	// PreferredZero preserves an arr preferredSize of zero; preferred: 0 alone still means unlimited.
	PreferredZero bool `yaml:"preferred_zero,omitempty"`
}

// FormatSet holds one flavour's formats, keyed by their display names.
type FormatSet struct {
	Flavour string                `yaml:"flavour"`
	Formats map[string]*FormatRef `yaml:"formats"`
}

// FormatRef points at a TRaSH format or arr-shaped JSON file. Inside a set it may reference a global format,
// keeping the set key as its display name and optionally overriding the renaming flag.
type FormatRef struct {
	Ref                 string `yaml:"ref,omitempty"`
	IncludeWhenRenaming *bool  `yaml:"include_when_renaming,omitempty"`
	TrashID             string `yaml:"trash_id,omitempty"`
	// Flavour is radarr or sonarr: TRaSH ids differ between the two.
	Flavour string `yaml:"flavour,omitempty"`
	// File is a path relative to the policy directory.
	File string `yaml:"file,omitempty"`
	// Specification is the JSON body of File when the format comes from settings storage. It is never emitted in YAML.
	Specification json.RawMessage `yaml:"-" json:"Specification,omitempty"`
}

// Privacy of an indexer. Empty means unknown, which counts as private.
type Privacy string

const (
	PrivacyPublic      Privacy = "public"
	PrivacyPrivate     Privacy = "private"
	PrivacySemiPrivate Privacy = "semi-private"
	PrivacyUnknown     Privacy = ""
)

// IsPrivate reports whether this privacy label is private (semi-private and unknown included; only "public" is
// public). A label for display, filters and warnings: nothing refuses on it.
func (p Privacy) IsPrivate() bool { return p != PrivacyPublic }

// publicKinds are the built-in sources that are public services.
var publicKinds = map[string]bool{"torrentio": true, "dmm": true, "zilean": true, "seadex": true}

// keyedKinds are the indexer kinds that search with a key (a Prowlarr or Torznab feed, DMM). Torrentio, Zilean and
// SeaDex take none; a catalog definition's credentials are optional (public sites need none).
var keyedKinds = map[string]bool{"torznab": true, "dmm": true}

// IndexerTakesKey reports whether an indexer of this kind needs its credential set to search.
func IndexerTakesKey(kind string) bool { return keyedKinds[kind] }

// ResolvePrivacy gives a built-in public source (torrentio, dmm, zilean, seadex) with no privacy set the label public.
// An explicit label, and an unset one on any other kind, are kept. Stored settings records pass through it when read.
func (ix *Indexer) ResolvePrivacy() {
	if ix.Privacy == PrivacyUnknown && publicKinds[ix.Kind] {
		ix.Privacy = PrivacyPublic
	}
}

// Indexer is a release source.
type Indexer struct {
	// Kind: torznab (Prowlarr per-indexer feeds), dmm, torrentio, zilean, seadex, cardigann (a definition from the
	// built-in catalog, run natively).
	Kind string `yaml:"kind"`
	// URL is the endpoint. Every outbound URL is configured; nothing has a default.
	URL string `yaml:"url"`
	// APIKeyEnv names the env var holding the API key, if any.
	APIKeyEnv string `yaml:"api_key_env"`
	// Privacy: public, private or semi-private. Unset counts as private, except on the built-in public sources
	// (torrentio, dmm, zilean, seadex), where it reads as public (ResolvePrivacy).
	Privacy Privacy `yaml:"privacy"`
	// Budget names a budget shared with other indexers using the same key.
	Budget string `yaml:"budget"`
	// PageCap caps paging per search (e.g. 200 for DMM). 0 means no cap.
	PageCap int `yaml:"page_cap"`
	// MinSeeders is the torrent minimum, default 1; 0 disables the check.
	MinSeeders *int `yaml:"min_seeders"`
	// MultiLanguages are the languages a release marked MULTI holds (the arr indexer setting Multi Languages), by
	// arr language name.
	MultiLanguages []string `yaml:"multi_languages"`
	// Proxy is an optional egress path for this indexer: an http:// or socks5:// URL (for example a gluetun
	// sidecar). The egress guard checks the exit address through it (see Egress).
	Proxy string `yaml:"proxy"`
	// SeaDex tunes which SeaDex entries a seadex indexer prefers (kind seadex only), as seadexerr's scoring.toml does.
	SeaDex *SeaDexScoring `yaml:"seadex"`
	// ArrNames are other names for this indexer, as the arrs' history or a release push gives them ("DMM RD
	// (Prowlarr)"): IndexerByName matches them without regard to case. A plain alias list.
	ArrNames []string `yaml:"arr_names"`
	// Definition is the catalog id of the definition a cardigann indexer runs (kind cardigann only).
	Definition string `yaml:"definition"`
	// Settings are the definition's non-secret settings (kind cardigann only). Secret ones (password, cookie, API
	// key, passkey) go in the indexer's secret as a JSON object, or in the variable api_key_env names.
	Settings map[string]string `yaml:"settings"`
	// Hosts are hosts other than the site's that the definition reaches (an API host the catalog lists), kind
	// cardigann only. They join the gateway's allowlist.
	Hosts []string `yaml:"hosts"`
	// Providers limits which debrid providers (keys of providers) this indexer's releases may go to, for adds and
	// cache checks; empty means every provider of the version. Which versions an indexer feeds is the version's
	// indexers list.
	Providers []string `yaml:"providers"`
}

// ManualIndexer is what a release a person added by magnet or hash carries as its indexer (arrview.AddRelease): no
// search found it, so it has none. It is not an indexer of the policy and cannot be the key of one (a key has no
// space: checkKey): such a release is not held to a version's indexer list and no indexer narrows its providers.
//
// Only arrview.AddRelease writes it: every place an indexer name comes in from outside (a release push, the decide
// API) refuses this value, so it marks a release a person added by hand and nothing else.
const ManualIndexer = "manual add"

// IsManualAdd reports whether ix is the placeholder of a release a person added by magnet or hash.
func IsManualAdd(ix string) bool { return ix == ManualIndexer }

// ReleaseProviders is v's provider order limited to those indexer ix allows (indexers[ix].providers; all when empty,
// when ix is not in the policy, or for a manual add).
func (p *Policy) ReleaseProviders(v *VersionPolicy, ix string) []string {
	if v == nil {
		return nil
	}
	in := p.Indexers[ix]
	if IsManualAdd(ix) || in == nil || len(in.Providers) == 0 {
		return v.Providers
	}
	var out []string
	for _, pk := range v.Providers {
		if slices.Contains(in.Providers, pk) {
			out = append(out, pk)
		}
	}
	return out
}

// MinimumSeeders returns the indexer's minimum with the arr torrent default.
func (ix *Indexer) MinimumSeeders() int {
	if ix.MinSeeders == nil {
		return 1
	}
	return *ix.MinSeeders
}

// SeaDexScoring is seadexerr's scoring table (Ryder-C/seadexerr v3.2.0 src/scoring.rs, example_scoring.toml): each
// entry's score is the sum of the weights it matches, and the highest scorers of a search are preferred. Unset
// fields take seadexerr's defaults.
type SeaDexScoring struct {
	// Best weighs entries SeaDex marks best. Default 100.
	Best *int `yaml:"best"`
	// DualAudio weighs entries marked dual audio. Default 0.
	DualAudio int `yaml:"dual_audio"`
	// SizeWeight scores size, normalised over the candidates to 0 (smallest) .. 1 (largest): below 0 favours
	// smaller entries, above 0 larger ones. Default 0.
	SizeWeight int `yaml:"size_weight"`
	// ExcludeTags drops entries carrying any of these releases.moe tags (exact label).
	ExcludeTags []string `yaml:"exclude_tags"`
	// Tags weighs releases.moe tags by exact label.
	Tags map[string]int `yaml:"tags"`
}

// DefaultSeaDexBest is seadexerr's default weight for a best entry.
const DefaultSeaDexBest = 100

// BestWeight is Best with its default.
func (s *SeaDexScoring) BestWeight() int {
	if s == nil || s.Best == nil {
		return DefaultSeaDexBest
	}
	return *s.Best
}

// Provider is a debrid account.
type Provider struct {
	// Kind: realdebrid, alldebrid, torbox.
	Kind string `yaml:"kind"`
	// Order is the fallback rank, 1 first. Unique.
	Order int `yaml:"order"`
	// URL is the provider API base (read for the registry's torrent lists).
	URL       string `yaml:"url"`
	APIKeyEnv string `yaml:"api_key_env"`
	// Budget names the provider's API budget.
	Budget string `yaml:"budget"`
	Pacing Pacing `yaml:"pacing"`
	// StalledAfter overrides router.stalled_after for this provider's downloads (docs/design/router.md "Stalled
	// downloads"). Unset: AllDebrid waits at least DefaultAllDebridStalledAfter (25m), the others take the router's
	// value. 0s turns the stall timeout off for this provider; otherwise 1m to 24h.
	StalledAfter *Duration `yaml:"stalled_after"`
	// MagnetLimit is how many magnets the AllDebrid account may hold, for the torrents report and its health warning
	// at TorrentLimitWarnPercent (docs/design/torrents.md "Account limit"). AllDebrid only; unset is
	// DefaultAllDebridMagnetLimit. Only reported: nothing is deleted or refused because of it.
	MagnetLimit *int `yaml:"magnet_limit"`
}

// Pacing limits adds to a provider.
type Pacing struct {
	AddsPerMinute int      `yaml:"adds_per_minute"`
	UncachedPerHr int      `yaml:"uncached_adds_per_hour"`
	Cooldown      Duration `yaml:"cooldown"`
	BackoffMax    Duration `yaml:"backoff_max"`
	// MinGap is the least time between two adds to the provider (docs/design/router.md "Pacing"). Unset: the kind's
	// default (DefaultMinGap: Real-Debrid 20s, the others none). 0s turns it off; at most MaxMinGap.
	MinGap *Duration `yaml:"min_gap"`
}

// Gap is MinGap, 0 when unset. Use Provider.EffectivePacing to get the kind's default.
func (pc Pacing) Gap() time.Duration {
	if pc.MinGap == nil {
		return 0
	}
	return pc.MinGap.D()
}

// Provider pacing and stall defaults, each sourced from the zurg notes' measurements.
const (
	// DefaultRealDebridMinGap: "Two adds on one account need 20 seconds between them. Measured against the live API.
	// Six clean release names added ten seconds apart were refused six times out of six. The same six
	// twenty seconds apart were accepted six times out of six." https://notes.debridmediamanager.com/zurg/providers/realdebrid/
	DefaultRealDebridMinGap = 20 * time.Second
	// MaxMinGap bounds pacing.min_gap.
	MaxMinGap = 10 * time.Minute
	// DefaultAllDebridStalledAfter: AllDebrid shows healthy uncached jobs with size 0 and every counter at zero for
	// about ten minutes (580 s and 622 s measured, both finished), so "a ten minute no-movement timeout would abandon
	// healthy AllDebrid jobs". https://notes.debridmediamanager.com/zurg/internals/torrent-lifecycle/
	DefaultAllDebridStalledAfter = 25 * time.Minute
	// DefaultAllDebridMagnetLimit is the user's chosen default for magnet_limit. zurg's notes give a lower figure,
	// "AllDebrid takes at most 1000 magnets per account"
	// (https://notes.debridmediamanager.com/zurg/guides/local-libraries/); set magnet_limit to it if the account
	// refuses adds there.
	DefaultAllDebridMagnetLimit = 5000
	// MaxMagnetLimit bounds magnet_limit.
	MaxMagnetLimit = 1000000
	// TorrentLimitWarnPercent is the share of an account's limit from which the torrents report and the health
	// check warn.
	TorrentLimitWarnPercent = 90
)

// TorrentLimit is how many torrents the provider's account may hold, as reported (0: no known limit). AllDebrid:
// magnet_limit, default DefaultAllDebridMagnetLimit.
func (pv *Provider) TorrentLimit() int {
	if pv == nil || pv.Kind != "alldebrid" {
		return 0
	}
	if pv.MagnetLimit != nil {
		return *pv.MagnetLimit
	}
	return DefaultAllDebridMagnetLimit
}

// NearTorrentLimit reports whether count is at or above TorrentLimitWarnPercent of limit (a limit of 0 never is).
func NearTorrentLimit(count, limit int) bool {
	return limit > 0 && count*100 >= limit*TorrentLimitWarnPercent
}

// DefaultMinGap is pacing.min_gap's default for a provider kind: Real-Debrid 20s (DefaultRealDebridMinGap), none
// for AllDebrid and TorBox, whose notes give no add spacing.
func DefaultMinGap(kind string) time.Duration {
	if kind == "realdebrid" {
		return DefaultRealDebridMinGap
	}
	return 0
}

// EffectivePacing is the provider's pacing with min_gap's kind default applied.
func (pv *Provider) EffectivePacing() Pacing {
	pc := pv.Pacing
	if pc.MinGap == nil {
		d := Duration(DefaultMinGap(pv.Kind))
		pc.MinGap = &d
	}
	return pc
}

// ProviderStalledAfter is the stall timeout for downloads on provider key: router.stalled_after with the provider's
// override (Provider.EffectiveStalledAfter); the router's value for an unknown key. 0 = off.
func (p *Policy) ProviderStalledAfter(key string) time.Duration {
	return p.Providers[key].EffectiveStalledAfter(p.RouterSettings().StalledAfter)
}

// EffectiveStalledAfter is the stall timeout for the provider's downloads, given router.stalled_after (global, 0 =
// off): the provider's own stalled_after when set; on AllDebrid otherwise at least DefaultAllDebridStalledAfter
// while the global timeout is on; else the global value.
func (pv *Provider) EffectiveStalledAfter(global time.Duration) time.Duration {
	if pv == nil {
		return global
	}
	if pv.StalledAfter != nil {
		return pv.StalledAfter.D()
	}
	if pv.Kind == "alldebrid" && global > 0 && global < DefaultAllDebridStalledAfter {
		return DefaultAllDebridStalledAfter
	}
	return global
}

// Client is a download client.
type Client struct {
	// Kind selects decypharr (a debrid client: it calls a view back, and takes the grabs of versions with add_via:
	// client) or qbittorrent (a seedbox client: accepted, but nothing sends grabs to it yet).
	Kind string `yaml:"kind"`
	// URL is the download client endpoint.
	URL string `yaml:"url"`
	// UsernameEnv and PasswordEnv name env vars with the client's login. decypharr reads them as an arr's: the
	// username is the callback view's URL, the password a Theatarr key for it (any login works without its use_auth).
	UsernameEnv string `yaml:"username_env"`
	// PasswordEnv names the environment variable holding the client password.
	PasswordEnv string `yaml:"password_env"`
	// Category is the download category Theatarr's adds go under; required for an add_via: client version. decypharr
	// keys its arr settings on it: one client entry per view flavour.
	Category string `yaml:"category"`
	// CallbackView is the view decypharr calls back (decypharr only).
	CallbackView string `yaml:"callback_view"`
	// ImportFrom is where a finished download is imported from: mount (the default: its release folder on Theatarr's
	// debrid mount) or download_folder (the client's folder of symlinks into the mount).
	ImportFrom string `yaml:"import_from"`
	// RemotePath and LocalPath map the client's paths to Theatarr's (an arr's remote path mapping): a content_path
	// under RemotePath is read under LocalPath. Both or neither, absolute.
	RemotePath string `yaml:"remote_path"`
	// LocalPath is where Theatarr sees the client's RemotePath.
	LocalPath string `yaml:"local_path"`
}

// Who adds a version's grabs (versions[*].add_via), and where a client's downloads are imported from
// (clients[*].import_from).
const (
	AddViaProviders          = "providers"
	AddViaClient             = "client"
	ImportFromMount          = "mount"
	ImportFromDownloadFolder = "download_folder"
)

// ViaClient reports whether the version sends its grabs to its download client instead of adding to the providers.
func (v *VersionPolicy) ViaClient() bool { return v != nil && v.AddVia == AddViaClient }

// FromDownloadFolder reports whether the client's downloads are imported from its download folder.
func (c *Client) FromDownloadFolder() bool {
	return c != nil && c.ImportFrom == ImportFromDownloadFolder
}

// LocalContentPath maps a path the client reported (content_path) to Theatarr's view of it: RemotePath replaced by
// LocalPath when the path is under it, else the path as it is.
func (c *Client) LocalContentPath(p string) string {
	if c == nil || c.RemotePath == "" || c.LocalPath == "" {
		return p
	}
	rel, err := filepath.Rel(c.RemotePath, filepath.Clean(p))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return filepath.Join(c.LocalPath, rel)
}

// View is an arr v3 API over a set of versions (docs/design/arrview.md).
type View struct {
	// Flavour: radarr or sonarr.
	Flavour string `yaml:"flavour"`
	// Path is the base URL path, e.g. /radarr.
	Path string `yaml:"path"`
	// Versions served, by key.
	Versions []string `yaml:"versions"`
	// Profiles are the "quality profiles" the view lists: each is a set of versions to turn on.
	Profiles map[string][]string `yaml:"profiles"`
	// APIKeyEnv names the env var holding this view's API key.
	APIKeyEnv string `yaml:"api_key_env"`
	// AliasFrom lists the arr instances whose ids this view answers (Seerr's stored ids; docs/migration.md).
	AliasFrom []string `yaml:"alias_from"`
}

// Budget is a token bucket, plus optional hourly and daily caps. The bucket refills at Requests per Per and holds
// Burst tokens (default 1, so requests are evenly paced). The caps count requests in fixed UTC clock hours and
// days, persisted, so a restart doesn't reset them. 0 means no cap.
type Budget struct {
	Requests int      `yaml:"requests"`
	Per      Duration `yaml:"per"`
	Burst    int      `yaml:"burst"`
	Hourly   int      `yaml:"hourly"`
	Daily    int      `yaml:"daily"`
}

// Metadata holds metadata sources and the overrides directory.
type Metadata struct {
	// OverridesDir points at external data. Legacy relative values resolve beneath the policy base.
	OverridesDir string `yaml:"overrides_dir"`
	// Sources lists metadata services by name.
	Sources map[string]*MetadataSource `yaml:"sources"`
	// Art schedules the meta.art job (art.go).
	Art *Art `yaml:"art"`
	// Series schedules the series metadata refresh (seriesmeta.go).
	Series *SeriesMeta `yaml:"series"`
	// Lists schedules Radarr collections and TMDb import lists.
	Lists *Lists `yaml:"lists"`
	// Anime schedules the automatic anime franchise builds (animebuild.go).
	Anime *AnimeBuild `yaml:"anime"`
}

// MetadataSource is one metadata API.
type MetadataSource struct {
	// URL is the metadata service endpoint.
	URL string `yaml:"url"`
	// APIKeyEnv names the environment variable holding the service API key.
	APIKeyEnv string `yaml:"api_key_env"`
	// Budget names the request budget for this service.
	Budget string `yaml:"budget"`
}

// Duration is a time.Duration written as "90s", "5m" or "1h".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// D returns the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Load reads every policy file in dir, merges them and validates the result. On a validation failure it returns the
// policy along with an *Errors.
func Load(dir string) (*Policy, error) {
	p, err := Read(dir)
	if err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return p, err
	}
	return p, nil
}

// Read reads and merges the policy files in dir without validating.
func Read(dir string) (*Policy, error) {
	return read(dir, false, false)
}

// ErrNoPolicy is wrapped by Read when the policy directory holds no policy files.
var ErrNoPolicy = errors.New("no policy files")

// ReadOrEmpty is Read, except that a missing directory or one with no policy files is an empty policy: a first start
// with nothing configured yet. Parse errors still fail.
func ReadOrEmpty(dir string) (*Policy, error) {
	return orEmpty(dir, Read)
}

// ReadSettingsOrEmpty is ReadSettings with ReadOrEmpty's handling of a missing or empty directory.
func ReadSettingsOrEmpty(dir string, qualityManaged bool) (*Policy, error) {
	return orEmpty(dir, func(dir string) (*Policy, error) { return ReadSettings(dir, qualityManaged) })
}

func orEmpty(dir string, read func(string) (*Policy, error)) (*Policy, error) {
	p, err := read(dir)
	if err == nil || !(errors.Is(err, ErrNoPolicy) || errors.Is(err, fs.ErrNotExist)) {
		return p, err
	}
	abs, absErr := filepath.Abs(dir)
	if absErr != nil {
		return nil, absErr
	}
	rev := sha256.New()
	return &Policy{Dir: abs, origin: map[string]string{}, Revision: hex.EncodeToString(rev.Sum(nil))[:12]}, nil
}

// ReadSettings reads only YAML-owned settings. Database sections are removed before strict decoding; version
// naming is removed only after its one-time import. Switch booleans are retained for the one-time seed and startup
// mismatch warnings; the settings overlay replaces them before validation. YAML syntax and other fields stay strict.
func ReadSettings(dir string, qualityManaged bool) (*Policy, error) {
	return read(dir, true, qualityManaged)
}

func read(dir string, settingsManaged, qualityManaged bool) (*Policy, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	p := &Policy{Dir: abs, origin: map[string]string{}}
	errs := &Errors{}
	rev := sha256.New()
	for _, e := range entries {
		if !isPolicyFile(e) {
			continue
		}
		p.Files = append(p.Files, e.Name())
		var part Policy
		b, err := os.ReadFile(filepath.Join(abs, e.Name()))
		if err == nil {
			hashFile(rev, e.Name(), b)
			if settingsManaged {
				b, err = unmanagedYAML(b, qualityManaged)
			}
		}
		var dropped []string
		if err == nil {
			b, dropped, err = dropRemoved(b)
		}
		if err == nil {
			err = decodeBytes(b, &part)
		}
		for _, d := range dropped {
			p.Ignored = append(p.Ignored, e.Name()+": "+d+" "+IgnoredNote)
		}
		if err != nil {
			errs.add(e.Name(), "%v", err)
			continue
		}
		p.merge(e.Name(), &part, errs)
	}
	sort.Strings(p.Files)
	p.Revision = hex.EncodeToString(rev.Sum(nil))[:12]
	if len(p.Files) == 0 {
		return nil, fmt.Errorf("policy: no .yaml files in %s: %w", abs, ErrNoPolicy)
	}
	if err := errs.err(); err != nil {
		return nil, err
	}
	return p, nil
}

// isPolicyFile: the policy is the .yaml and .yml files directly in its directory, dotfiles left out.
func isPolicyFile(e os.DirEntry) bool {
	ext := strings.ToLower(filepath.Ext(e.Name()))
	return !e.IsDir() && (ext == ".yaml" || ext == ".yml") && !strings.HasPrefix(e.Name(), ".")
}

func hashFile(h io.Writer, name string, b []byte) {
	fmt.Fprintf(h, "%s\x00%d\x00", name, len(b))
	h.Write(b)
}

// RevisionOf is the Revision of the policy files in dir as they are on disk now, without reading them as a policy
// (backups record it for the files they archive, which may differ from the loaded policy).
func RevisionOf(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("policy: %w", err)
	}
	rev := sha256.New()
	for _, e := range entries {
		if !isPolicyFile(e) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return "", fmt.Errorf("policy: %w", err)
		}
		hashFile(rev, e.Name(), b)
	}
	return hex.EncodeToString(rev.Sum(nil))[:12], nil
}

func decodeBytes(b []byte, out *Policy) error {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // empty file
		}
		return hintSecrets(err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("more than one YAML document; use one document per file")
	}
	return nil
}

// decodeStrict decodes a node with unknown fields refused, for custom unmarshalers (yaml.Node.Decode is lenient).
func decodeStrict(n *yaml.Node, out any) error {
	b, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	return nil
}

var secretFieldWords = []string{"field api_key ", "field apikey ", "field password ", "field token ", "field secret ",
	"field username ", "field key "}

// hintSecrets adds a pointer to the _env convention when someone wrote a secret inline.
func hintSecrets(err error) error {
	msg := err.Error()
	for _, w := range secretFieldWords {
		if strings.Contains(msg, w) {
			return fmt.Errorf("%w (secrets are never inline: name an environment variable with the matching _env field, e.g. api_key_env)", err)
		}
	}
	return err
}

func (p *Policy) merge(file string, part *Policy, errs *Errors) {
	mergeMap(file, "versions", &p.Versions, part.Versions, p.origin, errs)
	mergeMap(file, "profiles", &p.Profiles, part.Profiles, p.origin, errs)
	mergeMap(file, "formats", &p.Formats, part.Formats, p.origin, errs)
	mergeMap(file, "format_sets", &p.FormatSets, part.FormatSets, p.origin, errs)
	mergeMap(file, "indexers", &p.Indexers, part.Indexers, p.origin, errs)
	mergeMap(file, "providers", &p.Providers, part.Providers, p.origin, errs)
	mergeMap(file, "clients", &p.Clients, part.Clients, p.origin, errs)
	mergeMap(file, "views", &p.Views, part.Views, p.origin, errs)
	mergeMap(file, "budgets", &p.Budgets, part.Budgets, p.origin, errs)
	if part.Metadata != nil {
		if p.Metadata != nil {
			errs.add("metadata", "defined in both %s and %s", p.origin["metadata"], file)
		} else {
			p.Metadata = part.Metadata
			p.origin["metadata"] = file
		}
	}
	if part.Import != nil {
		if p.Import != nil {
			errs.add("import", "defined in both %s and %s", p.origin["import"], file)
		} else {
			p.Import = part.Import
			p.origin["import"] = file
		}
	}
	if part.Egress != nil {
		if p.Egress != nil {
			errs.add("egress", "defined in both %s and %s", p.origin["egress"], file)
		} else {
			p.Egress = part.Egress
			p.origin["egress"] = file
		}
	}
	if part.ProviderSync != nil {
		if p.ProviderSync != nil {
			errs.add("provider_sync", "defined in both %s and %s", p.origin["provider_sync"], file)
		} else {
			p.ProviderSync = part.ProviderSync
			p.origin["provider_sync"] = file
		}
	}
	if part.Shadow != nil {
		if p.Shadow != nil {
			errs.add("shadow", "defined in both %s and %s", p.origin["shadow"], file)
		} else {
			p.Shadow = part.Shadow
			p.origin["shadow"] = file
		}
	}
	if part.Library != nil {
		if p.Library != nil {
			errs.add("library", "defined in both %s and %s", p.origin["library"], file)
		} else {
			p.Library = part.Library
			p.origin["library"] = file
		}
	}
	if part.Router != nil {
		if p.Router != nil {
			errs.add("router", "defined in both %s and %s", p.origin["router"], file)
		} else {
			p.Router = part.Router
			p.origin["router"] = file
		}
	}
	if part.Playlists != nil {
		if p.Playlists != nil {
			errs.add("playlists", "defined in both %s and %s", p.origin["playlists"], file)
		} else {
			p.Playlists = part.Playlists
			p.origin["playlists"] = file
		}
	}
	if part.Notify != nil {
		if p.Notify != nil {
			errs.add("notify", "defined in both %s and %s", p.origin["notify"], file)
		} else {
			p.Notify = part.Notify
			p.origin["notify"] = file
		}
	}
	if part.UI != nil {
		if p.UI != nil {
			errs.add("ui", "defined in both %s and %s", p.origin["ui"], file)
		} else {
			p.UI = part.UI
			p.origin["ui"] = file
		}
	}
	if part.Cardigann != nil {
		if p.Cardigann != nil {
			errs.add("cardigann", "defined in both %s and %s", p.origin["cardigann"], file)
		} else {
			p.Cardigann = part.Cardigann
			p.origin["cardigann"] = file
		}
	}
	if part.Torrents != nil {
		if p.Torrents != nil {
			errs.add("torrents", "defined in both %s and %s", p.origin["torrents"], file)
		} else {
			p.Torrents = part.Torrents
			p.origin["torrents"] = file
		}
	}
}

func mergeMap[T any](file, section string, dst *map[string]*T, src map[string]*T, origin map[string]string, errs *Errors) {
	if len(src) == 0 {
		return
	}
	if *dst == nil {
		*dst = map[string]*T{}
	}
	for k, v := range src {
		path := fmt.Sprintf("%s[%s]", section, k)
		if prev, dup := origin[path]; dup {
			errs.add(path, "defined in both %s and %s", prev, file)
			continue
		}
		if v == nil {
			v = new(T)
		}
		(*dst)[k] = v
		origin[path] = file
	}
}

// File returns the file that defined path (e.g. "versions[movie-web]"), or "".
func (p *Policy) File(path string) string { return p.origin[path] }

// Errors is every validation problem, each with its path.
type Errors struct {
	List []FieldError
}

// FieldError is one problem at one path.
type FieldError struct {
	Path string
	Msg  string
}

func (e FieldError) Error() string {
	if e.Path == "" {
		return e.Msg
	}
	return e.Path + ": " + e.Msg
}

func (e *Errors) add(path, format string, args ...any) {
	e.List = append(e.List, FieldError{Path: path, Msg: fmt.Sprintf(format, args...)})
}

func (e *Errors) err() error {
	if len(e.List) == 0 {
		return nil
	}
	sort.SliceStable(e.List, func(i, j int) bool { return e.List[i].Path < e.List[j].Path })
	return e
}

func (e *Errors) Error() string {
	var b strings.Builder
	for i, fe := range e.List {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(fe.Error())
	}
	return b.String()
}

// IndexerByName is the policy key of the indexer an arr's history or a release push (autobrr) names: the key
// itself, the indexer whose arr_names list the name (without regard to case), or the one indexer whose key matches the name once both are normalized (case and punctuation ignored, a
// trailing " (Prowlarr)"-style suffix of the name and a "prowlarr-" prefix of the key dropped: Prowlarr's
// "TheRARBG" as an arr names it, "TheRARBG (Prowlarr)", matches the migrated key prowlarr-therarbg). "" when none
// or more than one does.
func (p *Policy) IndexerByName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if p.Indexers[name] != nil {
		return name
	}
	for _, k := range sortedKeys(p.Indexers) {
		for _, alias := range p.Indexers[k].ArrNames {
			if strings.EqualFold(strings.TrimSpace(alias), name) {
				return k
			}
		}
	}
	want := normalIndexerName(name, true)
	if want == "" {
		return ""
	}
	found := ""
	for _, k := range sortedKeys(p.Indexers) {
		if normalIndexerName(k, false) != want && normalIndexerName(strings.TrimPrefix(k, "prowlarr-"), false) != want {
			continue
		}
		if found != "" && found != k {
			return ""
		}
		found = k
	}
	return found
}

// normalIndexerName lower-cases s and keeps letters and digits only; a name (not a key) loses a trailing
// parenthesised suffix first.
func normalIndexerName(s string, name bool) string {
	s = strings.TrimSpace(s)
	if name && strings.HasSuffix(s, ")") {
		if i := strings.LastIndex(s, " ("); i > 0 {
			s = s[:i]
		}
	}
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r > 127 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// validateArrNames: an arr name is not blank and is listed once (by one indexer, and not another indexer's key), so
// it resolves to one indexer.
func (p *Policy) validateArrNames(e *Errors) {
	owner := map[string]string{}
	for _, k := range sortedKeys(p.Indexers) {
		owner[strings.ToLower(k)] = k
	}
	for _, k := range sortedKeys(p.Indexers) {
		for i, name := range p.Indexers[k].ArrNames {
			path := fmt.Sprintf("indexers[%s].arr_names[%d]", k, i)
			key := strings.ToLower(strings.TrimSpace(name))
			switch prev, taken := owner[key]; {
			case key == "":
				e.add(path, "must not be blank")
			case taken && prev != k:
				e.add(path, "%q already names indexer %s", name, prev)
			case taken:
				e.add(path, "%q is listed twice", name)
			default:
				owner[key] = k
			}
		}
	}
}

// OverridesPath resolves a configured external data directory: a relative one under the policy directory (imports
// store it as written), an absolute one as is (older imports stored it so), except that one inside the
// directory Dir is a copy of (CopyOf) resolves to the same place under Dir.
func (p *Policy) OverridesPath() string {
	if p.Metadata == nil || p.Metadata.OverridesDir == "" {
		return ""
	}
	if filepath.IsAbs(p.Metadata.OverridesDir) {
		if rel, ok := p.inCopy(p.Metadata.OverridesDir); ok {
			return filepath.Join(p.Dir, rel)
		}
		return p.Metadata.OverridesDir
	}
	return filepath.Join(p.Dir, p.Metadata.OverridesDir)
}

// CopyOf says Dir holds a copy of the policy directory src (a restore's staged copy of the backed-up one): absolute
// data paths inside src resolve under Dir. The stored settings are not changed.
func (p *Policy) CopyOf(src string) { p.copyOf = src }

// inCopy gives an absolute path's place relative to the directory Dir is a copy of, if it lies inside it.
func (p *Policy) inCopy(abs string) (string, bool) {
	if p.copyOf == "" {
		return "", false
	}
	rel, err := filepath.Rel(p.copyOf, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// versionNameMax is the longest display name of a version, in characters.
const versionNameMax = 40

// validateVersionName checks a version's optional display name: at most versionNameMax characters without the
// spaces around it, and no control characters. Two versions may share a name; the label then gets the key.
func validateVersionName(e *Errors, path, name string) {
	if utf8.RuneCountInString(strings.TrimSpace(name)) > versionNameMax {
		e.add(path+".name", "must be at most %d characters", versionNameMax)
	}
	if strings.ContainsFunc(name, unicode.IsControl) {
		e.add(path+".name", "must not contain control characters")
	}
}
