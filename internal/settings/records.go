// Package settings persists validated policy sections, credentials, generations and redacted audit diffs, and
// coordinates immutable runtime generation swaps. Callers supply the YAML policy for sections not stored in the DB.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

var (
	ErrDownloadsChanged = errors.New("settings: downloads changed during save; retry")
	ErrConflict         = errors.New("settings: generation changed")
	ErrUninitialized    = errors.New("settings: no generation")
)

// Secret is a write-only credential. NewSecret("") clears a credential; omitting a write preserves it.
// Formatting, JSON and YAML expose only "set" or "unset", including Go's %#v formatting.
type Secret struct {
	value string
	// stored: the record has a DB credential, set or explicitly cleared. Only a record that never had one may use
	// its retained environment fallback.
	stored bool
}

func NewSecret(value string) Secret { return Secret{value: value, stored: true} }
func (s Secret) String() string {
	if s.value != "" {
		return "set"
	}
	return "unset"
}
func (s Secret) GoString() string              { return s.String() }
func (s Secret) Format(f fmt.State, verb rune) { fmt.Fprint(f, s.String()) }
func (s Secret) MarshalJSON() ([]byte, error)  { return json.Marshal(s.String()) }
func (s Secret) MarshalYAML() (any, error)     { return s.String(), nil }

// Record identity and timestamps are assigned by Save. Keys cannot be renamed; delete and create instead.
type Record struct {
	ID        int64     `json:"id"`
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Indexer struct {
	Record
	Enabled   bool           `json:"enabled"`
	Priority  int            `json:"priority"`
	Config    policy.Indexer `json:"config"`
	SecretSet bool           `json:"secret_set"`
}

type Provider struct {
	Record
	Enabled   bool            `json:"enabled"`
	Config    policy.Provider `json:"config"`
	SecretSet bool            `json:"secret_set"`
}

type Budget struct {
	Record
	Config policy.Budget `json:"config"`
}

// Version stores one policy version and its library write roots.
type Version struct {
	Record
	Config policy.VersionPolicy `json:"config"`
	Roots  []string             `json:"roots"`
}

// ViewBinding stores a view: the version sets it exposes and, once seeded from YAML or created here, its identity.
// A binding without an identity takes it from the policy files (databases before the views moved). The view's API
// key is a write-only credential; SecretSet says whether one is stored.
type ViewBinding struct {
	Record
	Versions  []string            `json:"versions"`
	Profiles  map[string][]string `json:"profiles"`
	Identity  *ViewIdentity       `json:"identity,omitempty"`
	SecretSet bool                `json:"secret_set,omitempty"`
}

type QualityProfile struct {
	Record
	Config policy.Profile `json:"config"`
	// Origin is the TRaSH preset the profile was built from, or the one an administrator said it started from. It is
	// display metadata: the runtime policy never reads it.
	Origin *ProfileOrigin `json:"origin,omitempty"`
}

type CustomFormat struct {
	Record
	Config        policy.FormatRef `json:"config"`
	Specification json.RawMessage  `json:"specification,omitempty"`
}

type FormatSet struct {
	Record
	Config         policy.FormatSet           `json:"config"`
	Specifications map[string]json.RawMessage `json:"specifications,omitempty"`
}

type VersionNaming struct {
	Record
	Config policy.Naming `json:"config"`
}

// Records is the complete replacement of the DB-owned sections. Empty slices delete that section.
// Disabled sources remain in Policy so references and validation still apply. Consumers must be gated
// using Enabled and apply indexer Priority when building services; policy has no corresponding fields today.
type Records struct {
	Switches    *GlobalSwitches     `json:"switches,omitempty"`
	Indexers    []Indexer           `json:"indexers"`
	Providers   []Provider          `json:"providers"`
	Budgets     []Budget            `json:"budgets"`
	Versions    []Version           `json:"versions"`
	Views       []ViewBinding       `json:"views"`
	Profiles    []QualityProfile    `json:"profiles"`
	Formats     []CustomFormat      `json:"formats"`
	FormatSets  []FormatSet         `json:"format_sets"`
	Naming      []VersionNaming     `json:"naming"`
	SyncSources []QualitySyncSource `json:"sync_sources"`
	// Sections are the database-owned policy sections (sections.go). A save patches them by key: a section the
	// request leaves out, or a nil list, keeps its stored value.
	Sections []Section `json:"sections,omitempty"`
}

// Snapshot is one consistent generation. Policy is a fresh, validated copy, including the YAML-only sections.
// Treat it as immutable. Secrets are never attached to Policy.
type Snapshot struct {
	Generation     int64          `json:"generation"`
	Revision       string         `json:"revision"`
	ProviderEpoch  int64          `json:"provider_epoch"`
	Records        Records        `json:"records"`
	Policy         *policy.Policy `json:"-"`
	secrets        map[string]Secret
	qualityManaged bool
	// cardigannStore supplies live definition classification for redacting old snapshots.
	cardigannStore *Store
	// epochPending marks a save/import candidate, or a stored head being reloaded over an older active runtime. Its
	// ProviderEpoch is the stored epoch before the transition; the runtime preparing it decides whether its provider
	// transition advances the epoch.
	epochPending bool
}

// EpochPending reports whether the provider epoch of s is decided by the runtime preparing it: s is a save or import
// candidate, or the stored head being reloaded (RuntimeCoordinator.Reload). Startup snapshots carry their epoch.
func (s *Snapshot) EpochPending() bool { return s.epochPending }

// LookupSecret resolves only a named reference, never enumerating the process environment. Callers can supply
// os.LookupEnv for the one-release fallback; tests and offline imports can supply an isolated map.
type LookupSecret func(string) (string, bool)

// WithSecret passes a credential to a runtime constructor. entity is "indexers" or "providers".
// A stored value wins, and an explicitly cleared one means no credential. Only a record that never had a DB
// credential may use the supplied one-release environment fallback (its retained api_key_env).
// Constructor errors are deliberately opaque, since third-party errors can contain credentials.
func (s *Snapshot) WithSecret(entity, key string, lookup LookupSecret, use func(string) error) error {
	if use == nil {
		return errors.New("settings: credential consumer required")
	}
	value, ok := s.resolve(entity, key, lookup)
	if !ok {
		return errors.New("settings: unknown credential reference")
	}
	if err := use(value); err != nil {
		return errors.New("settings: credential consumer failed")
	}
	return nil
}

// Ready counts the indexers and providers that would run: enabled, and with their credential set when they take one
// (every provider; an indexer per policy.IndexerTakesKey). Setup's steps, its "Off until set up" list
// (preflight.Missing) and SetupCheck all count these, as the running service builds only enabled records.
func (s *Snapshot) Ready(lookup LookupSecret) (indexers, providers int) {
	for _, r := range s.Records.Indexers {
		if r.Enabled && (!policy.IndexerTakesKey(r.Config.Kind) || s.CredentialSet("indexers", r.Key, lookup)) {
			indexers++
		}
	}
	for _, r := range s.Records.Providers {
		if r.Enabled && s.CredentialSet("providers", r.Key, lookup) {
			providers++
		}
	}
	return indexers, providers
}

// CredentialSet reports whether the record resolves to a credential as WithSecret would, without exposing it: the
// effective presence the admin API shows as secret_set.
func (s *Snapshot) CredentialSet(entity, key string, lookup LookupSecret) bool {
	value, _ := s.resolve(entity, key, lookup)
	return value != ""
}

func (s *Snapshot) resolve(entity, key string, lookup LookupSecret) (string, bool) {
	secret, ok := s.secrets[entity+"/"+key]
	if !ok {
		return "", false
	}
	if secret.value != "" || secret.stored || lookup == nil {
		return secret.value, true
	}
	name := ""
	if entity == "indexers" {
		for _, r := range s.Records.Indexers {
			if r.Key == key {
				name = r.Config.APIKeyEnv
			}
		}
	}
	if entity == "providers" {
		for _, r := range s.Records.Providers {
			if r.Key == key {
				name = r.Config.APIKeyEnv
			}
		}
	}
	if name != "" {
		if value, ok := lookup(name); ok {
			return value, true
		}
	}
	return "", true
}

// SameSecret compares two snapshots without exposing the credential values.
func (s *Snapshot) SameSecret(other *Snapshot, entity, key string) bool {
	if s == nil || other == nil {
		return false
	}
	ref := entity + "/" + key
	return s.secrets[ref].value == other.secrets[ref].value
}

// SaveRequest replaces records at ExpectedGeneration. SecretWrites keys are "indexers/<key>" or
// "providers/<key>". Missing entries preserve credentials, NewSecret("") clears them. SecretSet is read-only.
// Base supplies all other policy sections; Save clones it before validating, since Validate applies defaults.
type SaveRequest struct {
	ExpectedGeneration int64
	Actor              string
	Base               *policy.Policy
	Records            Records
	SecretWrites       map[string]Secret
	// SectionSecretWrites sets (NewSecret(value)) or clears (NewSecret("")) a section's credentials by section key
	// and path (SectionSecretPaths); other paths keep their stored value.
	SectionSecretWrites   map[string]map[string]Secret
	OrphanActiveDownloads bool
	ConfirmationToken     string
	// Bootstrap skips filesystem existence and writability checks while preserving validated policy root separation.
	bootstrap         bool
	bootstrapSections string
	// importAsConfigured is the one-time import of the quality sections (BootstrapQuality): it copies the YAML the
	// service already runs on into the database, so its effects need no confirmation. It changes no version, switch
	// or other section, so nothing grabs that did not before.
	importAsConfigured bool
	confirmedEffects   []ConfirmationEffect
}

// seedsSections reports whether the generation this write creates over generation gen stores every settings section
// it lacks, at the base's value. A head's missing sections run on its base's. Over the policy directory alone (serve's
// base, which no YAML was read into) that is the default, so the first write and the next write over an older head
// that lacks some complete the generation: every generation serve writes carries all 13. A base read from YAML (the
// demo's and tests' YAML bootstrap, the CLI's legacy recovery) keeps the sections its head lacks in its files: the
// bootstrap writes leave them to BootstrapSections, and an import fills them from the files (importOver).
func (r SaveRequest) seedsSections(gen int64) bool {
	if r.bootstrap || r.bootstrapSections != "" {
		return false
	}
	return gen == 0 || r.Base == nil || r.Base.Revision == ""
}

// Audit is a redacted before/after record, with no secret values or hashes, even for credential-only changes.
type Audit struct {
	ID         int64           `json:"id"`
	Actor      string          `json:"actor"`
	CreatedAt  time.Time       `json:"created_at"`
	Generation int64           `json:"generation"`
	Entity     string          `json:"entity"`
	Key        string          `json:"key"`
	Operation  string          `json:"operation"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
}
