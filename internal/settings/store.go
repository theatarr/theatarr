package settings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Store owns settings transactions. Multiple instances and processes share the store's SQLite write lock;
// ExpectedGeneration is checked inside that lock, so stale writers never overwrite a newer generation.
type Store struct {
	db        *store.Store
	now       func() time.Time
	trashRoot string
	// lookup resolves a record's retained *_env fallback when confirmation effects ask whether a provider has a
	// credential, as the runtime building its clients does (SetLookup).
	lookup LookupSecret
	// cardigannSecrets says which settings of a definition hold credentials by its catalog entry (SetCardigannSecrets).
	cardigannSecrets CardigannSecrets
}

// CardigannSecrets reports which settings of the Cardigann definition id hold credentials, by the definition's own
// setting types (a password-type setting of any name) as well as by name. The map includes non-secret settings
// with false values: an absent setting is unverified. known is false while the catalog does not
// hold the definition.
type CardigannSecrets func(id string) (secret map[string]bool, known bool)

// SetCardigannSecrets supplies the definition catalog's view of secret settings (serve), so a save puts no
// credential in an indexer's plain settings that the name rule alone would miss.
func (s *Store) SetCardigannSecrets(f CardigannSecrets) { s.cardigannSecrets = f }

// SetLookup supplies the named-environment fallback the runtime resolves credentials with (serve: os.LookupEnv), so
// confirmation effects see the same credential presence as the adders it builds.
func (s *Store) SetLookup(lookup LookupSecret) { s.lookup = lookup }

// SetTrashRoot supplies the local TRaSH checkout for explicit imports and the one-time quality bootstrap.
func (s *Store) SetTrashRoot(root string) { s.trashRoot = root }

// SetClock replaces the wall clock that stamps records, generations and audit rows. Only seeded databases (theatarr demo
// seed, anchored at its -now) and tests use it; serve keeps time.Now.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now().UTC().Truncate(time.Millisecond)
	}
	return time.Now().UTC().Truncate(time.Millisecond)
}

type generationSnapshot struct {
	Records        Records           `json:"records"`
	Secrets        map[string]string `json:"secrets"`
	QualityManaged bool              `json:"quality_managed,omitempty"`
}

// New restricts the database and its existing SQLite sidecars before credentials can be stored.
func New(db *store.Store) (*Store, error) {
	if err := restrictDB(db); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func restrictDB(db *store.Store) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(db.Path()+suffix, 0o600); err != nil && !(suffix != "" && errors.Is(err, os.ErrNotExist)) {
			return errors.New("settings: cannot restrict database permissions")
		}
	}
	return nil
}

// row is the shared persistence/audit mechanism. New record kinds supply an encoder, decoder and table layout.
// No serialized config contains credentials; secretID is relational ownership, not part of an audit diff.
type row struct {
	Record
	Entity  string          `json:"entity"`
	Kind    string          `json:"kind,omitempty"`
	Enabled bool            `json:"enabled"`
	Rank    int             `json:"rank,omitempty"`
	Pacing  json.RawMessage `json:"pacing,omitempty"`
	Config  json.RawMessage `json:"config"`
	Secret  string          `json:"secret"`
	// SecretPaths are a section's credential paths with a value (names only).
	SecretPaths []string `json:"secret_paths,omitempty"`
	secretID    sql.NullInt64
}

type table struct {
	entity, name, columns string
}

var tables = []table{
	{"switches", "setting_global_switches", "'', 1, 0, 'null', NULL"},
	{"budgets", "setting_budget", "'', 1, 0, 'null', NULL"},
	{"indexers", "setting_indexer", "kind, enabled, priority, 'null', secret_id"},
	{"providers", "setting_provider", "kind, enabled, order_rank, pacing, secret_id"},
	{"versions", "setting_version", "'', 1, 0, 'null', NULL"},
	{"views", "setting_view_binding", "'', 1, 0, 'null', secret_id"},
	{"sections", "setting_section", "'', 1, 0, 'null', secret_id"},
	{"profiles", "setting_quality_profile", "'', 1, 0, 'null', NULL"},
	{"formats", "setting_custom_format", "'', 1, 0, 'null', NULL"},
	{"format_sets", "setting_format_set", "'', 1, 0, 'null', NULL"},
	{"naming", "setting_version_naming", "'', 1, 0, 'null', NULL"},
	{"sync_sources", "setting_quality_sync", "'', 1, 0, 'null', NULL"},
}

func rowsFrom(records Records) ([]row, error) {
	var out []row
	if records.Switches != nil {
		b, _ := json.Marshal(records.Switches)
		out = append(out, row{Record: Record{Key: "global"}, Entity: "switches", Enabled: true, Config: b})
	}
	for _, r := range records.Indexers {
		if r.Priority < 0 {
			return nil, &policy.Errors{List: []policy.FieldError{{Path: "indexers[" + r.Key + "].priority", Msg: "must not be negative"}}}
		}
		config := r.Config
		config.Kind = ""
		b, err := json.Marshal(config)
		if err != nil {
			return nil, errors.New("settings: invalid indexer config")
		}
		out = append(out, row{Record: r.Record, Entity: "indexers", Kind: r.Config.Kind, Enabled: r.Enabled, Rank: r.Priority, Config: b})
	}
	for _, r := range records.Providers {
		config := r.Config
		config.Kind, config.Order, config.Pacing = "", 0, policy.Pacing{}
		b, err := json.Marshal(config)
		if err != nil {
			return nil, errors.New("settings: invalid provider config")
		}
		pacing, _ := json.Marshal(r.Config.Pacing)
		out = append(out, row{Record: r.Record, Entity: "providers", Kind: r.Config.Kind, Enabled: r.Enabled, Rank: r.Config.Order, Pacing: pacing, Config: b})
	}
	for _, r := range records.Budgets {
		b, err := json.Marshal(r.Config)
		if err != nil {
			return nil, errors.New("settings: invalid budget config")
		}
		out = append(out, row{Record: r.Record, Entity: "budgets", Enabled: true, Config: b})
	}
	for _, r := range records.Versions {
		b, err := versionConfigJSON(r.Config, r.Roots)
		if err != nil {
			return nil, errors.New("settings: invalid library config")
		}
		out = append(out, row{Record: r.Record, Entity: "versions", Enabled: true, Config: b})
	}
	for _, r := range records.Views {
		b, err := json.Marshal(viewBindingConfig{Versions: r.Versions, Profiles: r.Profiles, Identity: r.Identity})
		if err != nil {
			return nil, errors.New("settings: invalid view binding")
		}
		out = append(out, row{Record: r.Record, Entity: "views", Enabled: true, Config: b})
	}
	for _, r := range records.Sections {
		if _, known := SectionLive(r.Key); !known {
			return nil, &policy.Errors{List: []policy.FieldError{{Path: r.Key, Msg: "not a settings section"}}}
		}
		config := r.Config
		if nullSection(config) {
			config = json.RawMessage("null")
		}
		if err := SectionOwnedElsewhere(r.Key, config); err != nil {
			return nil, err
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, config); err != nil {
			return nil, errors.New("settings: invalid section " + r.Key)
		}
		out = append(out, row{Record: r.Record, Entity: "sections", Enabled: true, Config: compact.Bytes()})
	}
	for _, r := range records.Profiles {
		b, err := profileConfigJSON(r)
		if err != nil {
			return nil, errors.New("settings: invalid quality profile")
		}
		out = append(out, row{Record: r.Record, Entity: "profiles", Enabled: true, Config: b})
	}
	for _, r := range records.Formats {
		config := r.Config
		config.File = ""
		config.Specification = nil
		b, err := json.Marshal(struct {
			Config        policy.FormatRef `json:"config"`
			Specification json.RawMessage  `json:"specification,omitempty"`
		}{config, r.Specification})
		if err != nil {
			return nil, errors.New("settings: invalid custom format")
		}
		out = append(out, row{Record: r.Record, Entity: "formats", Enabled: true, Config: b})
	}
	for _, r := range records.FormatSets {
		config := r.Config
		formats := make(map[string]*policy.FormatRef, len(config.Formats))
		for key, ref := range config.Formats {
			if ref != nil {
				copy := *ref
				copy.File = ""
				copy.Specification = nil
				formats[key] = &copy
			} else {
				formats[key] = nil
			}
		}
		config.Formats = formats
		b, err := json.Marshal(struct {
			Config         policy.FormatSet           `json:"config"`
			Specifications map[string]json.RawMessage `json:"specifications,omitempty"`
		}{config, r.Specifications})
		if err != nil {
			return nil, errors.New("settings: invalid format set")
		}
		out = append(out, row{Record: r.Record, Entity: "format_sets", Enabled: true, Config: b})
	}
	for _, r := range records.Naming {
		b, err := json.Marshal(r.Config)
		if err != nil {
			return nil, errors.New("settings: invalid library naming")
		}
		out = append(out, row{Record: r.Record, Entity: "naming", Enabled: true, Config: b})
	}
	for _, r := range records.SyncSources {
		config := r.Config
		if err := config.validate(); err != nil {
			return nil, err
		}
		b, err := json.Marshal(config)
		if err != nil {
			return nil, errors.New("settings: invalid sync source")
		}
		out = append(out, row{Record: r.Record, Entity: "sync_sources", Enabled: true, Config: b})
	}
	seen := map[string]bool{}
	for _, r := range out {
		if seen[r.ref()] {
			return nil, errors.New("settings: duplicate record key")
		}
		seen[r.ref()] = true
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ref() < out[j].ref() })
	return out, nil
}

func (r row) ref() string { return r.Entity + "/" + r.Key }

func credentialEntity(entity string) bool {
	return entity == "indexers" || entity == "providers" || entity == "views" || entity == "sections"
}

// setSecret records a row's credential presence: set or unset, and for a section the paths that have a value.
func (r *row) setSecret(secret Secret) {
	r.Secret = secret.String()
	if r.Entity == "sections" {
		r.SecretPaths = secretPaths(secret)
		if len(r.SecretPaths) == 0 {
			r.Secret, r.SecretPaths = Secret{}.String(), nil
		}
	}
}

func recordsFrom(rows []row, qualityManaged bool) (Records, error) {
	records := Records{Indexers: []Indexer{}, Providers: []Provider{}, Budgets: []Budget{}, Versions: []Version{}, Views: []ViewBinding{}}
	if qualityManaged {
		records.Profiles, records.Formats = []QualityProfile{}, []CustomFormat{}
		records.FormatSets, records.Naming = []FormatSet{}, []VersionNaming{}
		records.SyncSources = []QualitySyncSource{}
	}
	for _, r := range rows {
		switch r.Entity {
		case "switches":
			if err := json.Unmarshal(r.Config, &records.Switches); err != nil {
				return Records{}, errors.New("settings: corrupt global switches")
			}
		case "indexers":
			v := Indexer{Record: r.Record, Enabled: r.Enabled, Priority: r.Rank, SecretSet: r.Secret == "set"}
			if err := json.Unmarshal(r.Config, &v.Config); err != nil {
				return Records{}, errors.New("settings: corrupt indexer config")
			}
			v.Config.Kind = r.Kind
			v.Config.ResolvePrivacy()
			records.Indexers = append(records.Indexers, v)
		case "providers":
			v := Provider{Record: r.Record, Enabled: r.Enabled, SecretSet: r.Secret == "set"}
			if err := json.Unmarshal(r.Config, &v.Config); err != nil {
				return Records{}, errors.New("settings: corrupt provider config")
			}
			if err := json.Unmarshal(r.Pacing, &v.Config.Pacing); err != nil {
				return Records{}, errors.New("settings: corrupt pacing config")
			}
			v.Config.Kind, v.Config.Order = r.Kind, r.Rank
			records.Providers = append(records.Providers, v)
		case "budgets":
			v := Budget{Record: r.Record}
			if err := json.Unmarshal(r.Config, &v.Config); err != nil {
				return Records{}, errors.New("settings: corrupt budget config")
			}
			records.Budgets = append(records.Budgets, v)
		case "versions":
			v := Version{Record: r.Record}
			config, roots, err := versionConfigFromJSON(r.Config)
			if err != nil {
				return Records{}, errors.New("settings: corrupt library config")
			}
			v.Config, v.Roots = config, roots
			records.Versions = append(records.Versions, v)
		case "views":
			v := ViewBinding{Record: r.Record}
			var config viewBindingConfig
			if err := json.Unmarshal(r.Config, &config); err != nil {
				return Records{}, errors.New("settings: corrupt view binding")
			}
			v.Versions, v.Profiles, v.Identity, v.SecretSet = config.Versions, config.Profiles, config.Identity, r.Secret == "set"
			records.Views = append(records.Views, v)
		case "sections":
			if removedSections[r.Key] {
				continue // a section that no longer exists (removed.go): dropped at the next save
			}
			records.Sections = append(records.Sections, Section{Record: r.Record, Config: r.Config, SecretsSet: slices.Clone(r.SecretPaths)})
		case "profiles":
			if !qualityManaged {
				continue
			}
			v := QualityProfile{Record: r.Record}
			if err := json.Unmarshal(r.Config, &v.Config); err != nil {
				return Records{}, errors.New("settings: corrupt quality profile")
			}
			var origin struct {
				Origin *ProfileOrigin `json:"origin"`
			}
			if err := json.Unmarshal(r.Config, &origin); err != nil {
				return Records{}, errors.New("settings: corrupt quality profile")
			}
			v.Origin = origin.Origin
			records.Profiles = append(records.Profiles, v)
		case "formats":
			if !qualityManaged {
				continue
			}
			v := CustomFormat{Record: r.Record}
			var config struct {
				Config        policy.FormatRef `json:"config"`
				Specification json.RawMessage  `json:"specification"`
			}
			if err := json.Unmarshal(r.Config, &config); err != nil {
				return Records{}, errors.New("settings: corrupt custom format")
			}
			v.Config, v.Specification = config.Config, config.Specification
			records.Formats = append(records.Formats, v)
		case "format_sets":
			if !qualityManaged {
				continue
			}
			v := FormatSet{Record: r.Record}
			var config struct {
				Config         policy.FormatSet           `json:"config"`
				Specifications map[string]json.RawMessage `json:"specifications"`
			}
			if err := json.Unmarshal(r.Config, &config); err != nil {
				return Records{}, errors.New("settings: corrupt format set")
			}
			v.Config, v.Specifications = config.Config, config.Specifications
			records.FormatSets = append(records.FormatSets, v)
		case "naming":
			if !qualityManaged {
				continue
			}
			v := VersionNaming{Record: r.Record}
			if err := json.Unmarshal(r.Config, &v.Config); err != nil {
				return Records{}, errors.New("settings: corrupt library naming")
			}
			records.Naming = append(records.Naming, v)
		case "sync_sources":
			v := QualitySyncSource{Record: r.Record}
			if err := json.Unmarshal(r.Config, &v.Config); err != nil {
				return Records{}, errors.New("settings: corrupt sync source")
			}
			records.SyncSources = append(records.SyncSources, v)
		}
	}
	return records, nil
}

func readRows(ctx context.Context, tx *sql.Tx) ([]row, map[string]Secret, error) {
	var out []row
	secrets := map[string]Secret{}
	for _, table := range tables {
		q := "SELECT id, key, created_at, updated_at, " + table.columns + ", config FROM " + table.name + " ORDER BY key"
		rs, err := tx.QueryContext(ctx, q)
		if err != nil {
			return nil, nil, err
		}
		for rs.Next() {
			r := row{Entity: table.entity}
			var created, updated int64
			var config, pacing string
			if err := rs.Scan(&r.ID, &r.Key, &created, &updated, &r.Kind, &r.Enabled, &r.Rank, &pacing, &r.secretID, &config); err != nil {
				rs.Close()
				return nil, nil, err
			}
			r.CreatedAt, r.UpdatedAt = store.FromMillis(created), store.FromMillis(updated)
			r.Config = json.RawMessage(config)
			if r.Entity == "providers" {
				r.Pacing = json.RawMessage(pacing)
			}
			out = append(out, r)
		}
		err = rs.Err()
		rs.Close()
		if err != nil {
			return nil, nil, err
		}
	}
	for i := range out {
		r := &out[i]
		if !credentialEntity(r.Entity) {
			continue
		}
		var secret Secret
		if r.secretID.Valid {
			if err := tx.QueryRowContext(ctx, "SELECT value FROM setting_secret WHERE id = ?", r.secretID.Int64).Scan(&secret.value); err != nil {
				return nil, nil, err
			}
			secret.stored = true
		}
		r.setSecret(secret)
		secrets[r.ref()] = secret
	}
	return out, secrets, nil
}

func generation(ctx context.Context, tx interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int64, string, error) {
	var gen int64
	var rev string
	err := tx.QueryRowContext(ctx, "SELECT generation, revision FROM settings_generation ORDER BY generation DESC LIMIT 1").Scan(&gen, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	return gen, rev, err
}

func providerEpoch(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int64, error) {
	var epoch int64
	err := q.QueryRowContext(ctx, "SELECT provider_epoch FROM settings_runtime_state WHERE id = 1").Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return epoch, err
}

func qualityInitialized(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (bool, error) {
	var initialized int
	err := q.QueryRowContext(ctx, "SELECT 1 FROM settings_quality_state WHERE id = 1").Scan(&initialized)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return initialized == 1, err
}

func hasQualityRecords(records Records) bool {
	return records.Profiles != nil || records.Formats != nil || records.FormatSets != nil || records.Naming != nil || records.SyncSources != nil
}

// IncrementProviderEpoch fences routes prepared with an older provider account set.
func IncrementProviderEpoch(ctx context.Context, tx *sql.Tx) error {
	res, err := tx.ExecContext(ctx, "UPDATE settings_runtime_state SET provider_epoch = provider_epoch + 1 WHERE id = 1")
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO settings_runtime_state (id, last_published_generation, previous_published_generation, provider_epoch)
			SELECT 1, COALESCE(MAX(generation), 0), NULL, 1 FROM settings_generation`)
	}
	return err
}

// Load reads rows, secrets and generation in one reader transaction, then validates the complete candidate.
func (s *Store) Load(ctx context.Context, base *policy.Policy) (*Snapshot, error) {
	snap, err := s.LoadDiagnostic(ctx, base)
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// LoadDiagnostic reads the same consistent snapshot as Load, but retains the assembled
// policy alongside validation errors for local diagnostics. It must not be used to start services.
func (s *Store) LoadDiagnostic(ctx context.Context, base *policy.Policy) (*Snapshot, error) {
	tx, err := s.db.Reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, dbError(err)
	}
	defer tx.Rollback()
	gen, rev, err := generation(ctx, tx)
	if err != nil {
		return nil, dbError(err)
	}
	epoch, err := providerEpoch(ctx, tx)
	if err != nil {
		return nil, dbError(err)
	}
	qualityManaged, err := qualityInitialized(ctx, tx)
	if err != nil {
		return nil, dbError(err)
	}
	rows, secrets, err := readRows(ctx, tx)
	if err != nil {
		return nil, dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, dbError(err)
	}
	return s.diagnosticSnapshot(base, rows, secrets, gen, rev, epoch, qualityManaged)
}

func (s *Store) snapshot(base *policy.Policy, rows []row, secrets map[string]Secret, gen int64, rev string, epoch int64, qualityManaged bool) (*Snapshot, error) {
	snap, err := s.diagnosticSnapshot(base, rows, secrets, gen, rev, epoch, qualityManaged)
	if err != nil {
		return nil, err
	}
	return snap, nil
}

func (s *Store) diagnosticSnapshot(base *policy.Policy, rows []row, secrets map[string]Secret, gen int64, rev string, epoch int64, qualityManaged bool) (*Snapshot, error) {
	owned := map[string]Secret{}
	for _, r := range rows {
		if credentialEntity(r.Entity) {
			owned[r.ref()] = secrets[r.ref()]
		}
	}
	secrets = owned
	records, err := recordsFrom(rows, qualityManaged)
	if err != nil {
		return nil, err
	}
	if gen == 0 && records.Switches == nil {
		records.Switches = &GlobalSwitches{}
	}
	if qualityManaged {
		if err := requireQualitySpecifications(records); err != nil {
			return nil, err
		}
	}
	p, err := diagnosticCandidate(base, records)
	if p == nil {
		return nil, redactValidation(err, secrets)
	}
	var validationErr error
	if err != nil {
		validationErr = redactValidation(err, secrets)
	}
	p.Revision = ""
	b, err := json.Marshal(p)
	if err != nil {
		return nil, errors.New("settings: cannot compute policy revision")
	}
	p.Revision = revision(string(b) + "/" + rev)
	if qualityManaged {
		p.Revision = rev
	}
	snap := &Snapshot{Generation: gen, Revision: p.Revision, ProviderEpoch: epoch, Records: records, Policy: p, secrets: secrets,
		qualityManaged: qualityManaged, cardigannStore: s}
	// The generation resolves its own credentials; installing nothing globally keeps a candidate's values out of
	// the generation in force.
	p.WithSettingsSecrets(snap.secretEnvValues())
	return snap, validationErr
}

// Save validates and commits rows, credential writes, generation and audit together. A rejected candidate,
// stale generation or failed statement changes none of them. No runtime services are rebuilt here.
func (s *Store) Save(ctx context.Context, req SaveRequest) (*Snapshot, error) {
	return s.saveWithHook(ctx, req, "save", nil)
}

func (s *Store) save(ctx context.Context, req SaveRequest, operation string) (*Snapshot, error) {
	return s.saveWithHook(ctx, req, operation, nil)
}

func (s *Store) saveWithHook(ctx context.Context, req SaveRequest, operation string,
	hook func(context.Context, *sql.Tx) error) (*Snapshot, error) {
	if err := s.checkSaveRoots(ctx, req); err != nil {
		return nil, dbError(err)
	}
	var result *Snapshot
	err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if req.Records.Switches == nil {
			var raw string
			err := tx.QueryRowContext(ctx, "SELECT config FROM setting_global_switches WHERE key = 'global'").Scan(&raw)
			if err == nil {
				if err := json.Unmarshal([]byte(raw), &req.Records.Switches); err != nil {
					return err
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		storedSecs, err := storedSections(ctx, tx)
		if err != nil {
			return err
		}
		req.Records.Sections = mergeSections(storedSecs, req.Records.Sections)
		gen, _, err := generation(ctx, tx)
		if err != nil {
			return err
		}
		if gen != req.ExpectedGeneration {
			return ErrConflict
		}
		if req.seedsSections(gen) {
			if req.Records.Sections, err = withDefaultSections(req.Records.Sections, req.Base); err != nil {
				return err
			}
		}
		rows, err := rowsFrom(req.Records)
		if err != nil {
			return redactValidation(err, req.SecretWrites)
		}
		priorQuality, err := qualityInitialized(ctx, tx)
		if err != nil {
			return err
		}
		qualityManaged := priorQuality || hasQualityRecords(req.Records)
		old, secrets, err := readRows(ctx, tx)
		if err != nil {
			return err
		}
		priorSecrets := maps.Clone(secrets)
		writes, err := sectionSecretWrites(req, req.Records.Sections, secrets)
		if err != nil {
			return err
		}
		before := map[string]row{}
		for _, r := range old {
			before[r.ref()] = r
		}
		if err := refuseDerivedNames(rows, before); err != nil {
			return err
		}
		now := s.clock()
		pending := map[string]bool{}
		for i := range rows {
			r := &rows[i]
			prev, exists := before[r.ref()]
			if r.ID != 0 && (!exists || r.ID != prev.ID) {
				return errors.New("settings: record identity changed")
			}
			r.ID, r.CreatedAt, r.UpdatedAt, r.secretID = prev.ID, prev.CreatedAt, now, prev.secretID
			if !exists {
				r.CreatedAt = now
			}
			if credentialEntity(r.Entity) {
				if v, ok := writes[r.ref()]; ok {
					secrets[r.ref()] = v
					pending[r.ref()] = true
				}
				r.setSecret(secrets[r.ref()])
			}
		}
		for ref := range writes {
			if !pending[ref] {
				return errors.New("settings: secret write has no owning record")
			}
		}
		gen++
		// Revision includes the generation to distinguish credential rotations without hashing secret bytes.
		rev := contentRevision(rows, gen)
		// The provider epoch is advanced only by the hook (the prepared runtime's provider transition) and read back below.
		result, err = s.snapshot(req.Base, rows, secrets, gen, rev, 0, qualityManaged)
		if err != nil {
			return err
		}
		var prior *Snapshot
		var priorPolicy *policy.Policy
		if !req.bootstrap {
			_, priorRevision, err := generation(ctx, tx)
			if err != nil {
				return err
			}
			prior, err = s.snapshot(req.Base, old, priorSecrets, gen, priorRevision, 0, priorQuality)
			if err != nil {
				return err
			}
			priorPolicy = prior.Policy
		}
		if err := s.refuseCardigannSecretSettings(priorPolicy, result.Policy); err != nil {
			return err
		}
		// The filesystem checks of the roots ran before the transaction (checkSaveRoots), on this same generation.
		if req.bootstrap {
			if err := validateVersionRules(ctx, tx, req.Base, result.Policy, true); err != nil {
				return err
			}
		} else {
			if err := validateVersionRules(ctx, tx, prior.Policy, result.Policy, false); err != nil {
				return err
			}
			effects, err := snapshotConfirmationEffects(ctx, tx, prior, result, s.lookup)
			if err != nil {
				return err
			}
			if req.importAsConfigured {
				effects = nil
			}
			for _, effect := range effects {
				if !slices.Contains(req.confirmedEffects, effect) {
					return &ConfirmationError{Effects: effects}
				}
			}
		}
		if strings.TrimSpace(req.Actor) == "" {
			return errors.New("settings: actor required")
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO settings_generation (generation, revision, created_at) VALUES (?, ?, ?)", gen, rev, store.Millis(now)); err != nil {
			return err
		}
		for i := range rows {
			r := &rows[i]
			prev, exists := before[r.ref()]
			if exists && sameRecord(prev, *r) && !pending[r.ref()] {
				*r = prev
				delete(before, r.ref())
				continue
			}
			if pending[r.ref()] {
				value := secrets[r.ref()].value
				if r.secretID.Valid {
					_, err = tx.ExecContext(ctx, "UPDATE setting_secret SET value = ?, updated_at = ? WHERE id = ?", value, store.Millis(now), r.secretID.Int64)
				} else {
					// An explicit clear is stored too (an empty value): it ends the record's environment fallback.
					var res sql.Result
					res, err = tx.ExecContext(ctx, "INSERT INTO setting_secret (value, created_at, updated_at) VALUES (?, ?, ?)", value, store.Millis(now), store.Millis(now))
					if err == nil {
						r.secretID.Int64, err = res.LastInsertId()
						r.secretID.Valid = err == nil
					}
				}
				if err != nil {
					return err
				}
			}
			if err := writeRow(ctx, tx, r); err != nil {
				return err
			}
			var prior any
			if exists {
				prior = prev
			}
			op := "create"
			if exists {
				op = "update"
			}
			if err := writeAudit(ctx, tx, req.Actor, now, gen, r.Entity, r.Key, op, prior, r); err != nil {
				return err
			}
			delete(before, r.ref())
		}
		for _, prev := range old {
			if _, deleted := before[prev.ref()]; !deleted {
				continue
			}
			for _, table := range tables {
				if table.entity == prev.Entity {
					if _, err := tx.ExecContext(ctx, "DELETE FROM "+table.name+" WHERE id = ?", prev.ID); err != nil {
						return err
					}
				}
			}
			if prev.secretID.Valid {
				if _, err := tx.ExecContext(ctx, "DELETE FROM setting_secret WHERE id = ?", prev.secretID.Int64); err != nil {
					return err
				}
			}
			if err := writeAudit(ctx, tx, req.Actor, now, gen, prev.Entity, prev.Key, "delete", prev, nil); err != nil {
				return err
			}
		}
		if err := writeAudit(ctx, tx, req.Actor, now, gen, "settings", req.bootstrapSections, operation, nil, nil); err != nil {
			return err
		}
		// Refresh assigned IDs and timestamps without rereading credentials.
		result.Records, err = recordsFrom(rows, qualityManaged)
		if err != nil {
			return err
		}
		stored := generationSnapshot{Records: result.Records, Secrets: map[string]string{}, QualityManaged: qualityManaged}
		for _, r := range rows {
			// A record without a DB credential stays out of the map, so a restored generation keeps its fallback.
			if credentialEntity(r.Entity) && secrets[r.ref()].stored {
				stored.Secrets[r.ref()] = secrets[r.ref()].value
			}
		}
		b, err := json.Marshal(stored)
		if err != nil {
			return errors.New("settings: cannot store generation snapshot")
		}
		_, err = tx.ExecContext(ctx, "UPDATE settings_generation SET snapshot = ? WHERE generation = ?", string(b), gen)
		if err != nil {
			return err
		}
		if err := pruneSnapshots(ctx, tx, gen); err != nil {
			return err
		}
		if hook != nil {
			if err := hook(ctx, tx); err != nil {
				return err
			}
		}
		if qualityManaged {
			sections, _ := json.Marshal([]string{"profiles", "formats", "format_sets", "naming"})
			_, err = tx.ExecContext(ctx, `INSERT INTO settings_quality_state (id, initialized_at, source, sections)
				VALUES (1, ?, 'settings', ?) ON CONFLICT(id) DO NOTHING`, store.Millis(now), string(sections))
			if err != nil {
				return err
			}
		}
		result.ProviderEpoch, err = providerEpoch(ctx, tx)
		return err
	})
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

// checkSaveRoots runs a save's filesystem root checks (validateVersionRoots) before its write transaction, so a
// slow or hung root never holds the database writer. It reads the state the save expects; the transaction's own
// generation check then makes sure the save commits on top of what was checked. An invalid candidate is left to the
// transaction, which reports it with its credentials redacted.
func (s *Store) checkSaveRoots(ctx context.Context, req SaveRequest) error {
	previous := req.Base
	qualityManaged := hasQualityRecords(req.Records)
	if !req.bootstrap {
		current, err := s.Load(ctx, req.Base)
		if err != nil {
			return err
		}
		if current.Generation != req.ExpectedGeneration {
			return ErrConflict
		}
		previous, qualityManaged = current.Policy, qualityManaged || current.qualityManaged
		if req.Records.Switches == nil {
			req.Records.Switches = current.Records.Switches
		}
		req.Records.Sections = mergeSections(current.Records.Sections, req.Records.Sections)
	} else if initialized, err := qualityInitialized(ctx, s.db.Reader()); err == nil {
		qualityManaged = qualityManaged || initialized
	}
	rows, err := rowsFrom(req.Records)
	if err != nil {
		return nil
	}
	next, err := s.snapshot(req.Base, rows, nil, req.ExpectedGeneration+1, "", 0, qualityManaged)
	if err != nil {
		return nil
	}
	return validateVersionRoots(ctx, previous, next.Policy, req.bootstrap)
}

// commitTransition commits a reload's provider transition for the stored head snap: in one writer transaction it
// checks that snap is still the head and runs hook (the prepared runtime's CommitSettings and epoch check). It writes
// no generation or record; the returned copy carries the committed provider epoch.
func (s *Store) commitTransition(ctx context.Context, snap *Snapshot, hook func(context.Context, *sql.Tx) error) (*Snapshot, error) {
	var epoch int64
	err := s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		gen, _, err := generation(ctx, tx)
		if err != nil {
			return err
		}
		if gen != snap.Generation {
			return ErrConflict
		}
		if err := hook(ctx, tx); err != nil {
			return err
		}
		epoch, err = providerEpoch(ctx, tx)
		return err
	})
	if err != nil {
		return nil, dbError(err)
	}
	out := *snap
	out.ProviderEpoch, out.epochPending = epoch, false
	return &out, nil
}

// Candidate builds and validates the snapshot a save would publish without committing it. SaveAndApply prepares
// runtime components from this value, then Save repeats validation and commits under the writer lock.
func (s *Store) Candidate(ctx context.Context, req SaveRequest) (*Snapshot, error) {
	current, err := s.Load(ctx, req.Base)
	if err != nil {
		return nil, err
	}
	if current.Generation != req.ExpectedGeneration {
		return nil, ErrConflict
	}
	if req.Records.Switches == nil {
		req.Records.Switches = current.Records.Switches
	}
	req.Records.Sections = mergeSections(current.Records.Sections, req.Records.Sections)
	if req.seedsSections(current.Generation) {
		if req.Records.Sections, err = withDefaultSections(req.Records.Sections, req.Base); err != nil {
			return nil, err
		}
	}
	rows, err := rowsFrom(req.Records)
	if err != nil {
		return nil, redactValidation(err, req.SecretWrites)
	}
	currentRows, err := rowsFrom(current.Records)
	if err != nil {
		return nil, err
	}
	unchanged := map[string]row{}
	for _, r := range currentRows {
		unchanged[r.ref()] = r
	}
	if err := refuseDerivedNames(rows, unchanged); err != nil {
		return nil, err
	}
	secrets := make(map[string]Secret, len(current.secrets))
	for key, value := range current.secrets {
		secrets[key] = value
	}
	writes, err := sectionSecretWrites(req, req.Records.Sections, secrets)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		row := &rows[i]
		if credentialEntity(row.Entity) {
			if value, ok := writes[row.ref()]; ok {
				secrets[row.ref()] = value
			}
			row.setSecret(secrets[row.ref()])
		}
	}
	for key := range writes {
		if _, ok := secrets[key]; !ok {
			return nil, errors.New("settings: secret write has no owning record")
		}
	}
	gen := req.ExpectedGeneration + 1
	rev := contentRevision(rows, gen)
	// The candidate carries the stored epoch. Whether the save advances it depends on the provider transition from
	// the active runtime, which only the runtime preparing the candidate knows (RuntimeCoordinator.saveAndApply).
	qualityManaged := current.qualityManaged || hasQualityRecords(req.Records)
	next, err := s.snapshot(req.Base, rows, secrets, gen, rev, current.ProviderEpoch, qualityManaged)
	if err != nil {
		return nil, err
	}
	if err := validateVersionChanges(ctx, s.db.Reader(), current.Policy, next.Policy, false); err != nil {
		return nil, err
	}
	if err := s.refuseCardigannSecretSettings(current.Policy, next.Policy); err != nil {
		return nil, err
	}
	next.epochPending = true
	return next, nil
}

// New or changed plain settings need a catalog entry explicitly declaring them non-secret.
// Unchanged legacy values stay usable; exports and audit mask them even while the catalog is unavailable.
func (s *Store) refuseCardigannSecretSettings(current, next *policy.Policy) error {
	if next == nil {
		return nil
	}
	errs := &policy.Errors{}
	for _, key := range slices.Sorted(maps.Keys(next.Indexers)) {
		ix := next.Indexers[key]
		if ix == nil || ix.Kind != "cardigann" {
			continue
		}
		secret, known := s.definitionSettings(ix.Definition)
		for _, k := range slices.Sorted(maps.Keys(ix.Settings)) {
			if current != nil {
				if prev := current.Indexers[key]; prev != nil && prev.Kind == ix.Kind && prev.Definition == ix.Definition {
					if old, ok := prev.Settings[k]; ok && old == ix.Settings[k] {
						continue
					}
				}
			}
			isSecret, declared := secret[k]
			msg := ""
			if !known || !declared {
				msg = "cannot verify as non-secret: definition " + ix.Definition + " or its setting is unavailable; load the definition before supplying plain settings"
			} else if isSecret {
				msg = "holds a credential (definition " + ix.Definition + "): put it in the indexer's secret (a JSON object of the secret settings), not in settings"
			}
			if msg != "" {
				errs.List = append(errs.List, policy.FieldError{Path: fmt.Sprintf("indexers.%s.settings.%s", key, k), Msg: msg})
			}
		}
	}
	if len(errs.List) == 0 {
		return nil
	}
	return errs
}

func (s *Store) definitionSettings(id string) (map[string]bool, bool) {
	if s.cardigannSecrets == nil {
		return nil, false
	}
	return s.cardigannSecrets(id)
}

// LoadGeneration restores a prior committed settings snapshot for startup recovery.
func (s *Store) LoadGeneration(ctx context.Context, base *policy.Policy, gen int64) (*Snapshot, error) {
	var raw string
	var rev string
	err := s.db.Reader().QueryRowContext(ctx, "SELECT revision, snapshot FROM settings_generation WHERE generation = ?", gen).
		Scan(&rev, &raw)
	if err != nil {
		return nil, dbError(err)
	}
	var stored generationSnapshot
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return nil, errors.New("settings: stored generation snapshot is invalid")
	}
	// Generations saved before snapshots existed, or pruned ones, carry '{}'; never load them as empty settings.
	if stored.Records.Indexers == nil && stored.Records.Providers == nil && stored.Records.Budgets == nil ||
		stored.Records.Versions == nil || stored.Records.Views == nil {
		return nil, fmt.Errorf("settings: generation %d has no stored snapshot", gen)
	}
	// Pre-switch snapshots must never re-enable writes from later YAML values.
	if stored.Records.Switches == nil {
		stored.Records.Switches = &GlobalSwitches{}
	}
	// Startup recovery can retain an older enabled state only while the stored head still allows it.
	var rawSwitches string
	err = s.db.Reader().QueryRowContext(ctx, "SELECT config FROM setting_global_switches WHERE key = 'global'").Scan(&rawSwitches)
	if err == nil {
		var head GlobalSwitches
		if err := json.Unmarshal([]byte(rawSwitches), &head); err != nil {
			return nil, errors.New("settings: corrupt global switches")
		}
		stored.Records.Switches.RouterEnabled = stored.Records.Switches.RouterEnabled && head.RouterEnabled
		stored.Records.Switches.LibraryEnabled = stored.Records.Switches.LibraryEnabled && head.LibraryEnabled
		stored.Records.Switches.NotifyEnabled = stored.Records.Switches.NotifyEnabled && head.NotifyEnabled
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, dbError(err)
	}
	if err := s.clampVersionsToHead(ctx, stored.Records.Versions); err != nil {
		return nil, err
	}
	rows, err := rowsFrom(stored.Records)
	if err != nil {
		return nil, err
	}
	secrets := make(map[string]Secret, len(stored.Secrets))
	for key, value := range stored.Secrets {
		secrets[key] = NewSecret(value)
	}
	for i := range rows {
		if credentialEntity(rows[i].Entity) {
			rows[i].setSecret(secrets[rows[i].ref()])
		}
	}
	epoch, err := providerEpoch(ctx, s.db.Reader())
	if err != nil {
		return nil, dbError(err)
	}
	return s.snapshot(base, rows, secrets, gen, rev, epoch, stored.QualityManaged || hasQualityRecords(stored.Records))
}

// FallbackEffects lists what running fallback instead of the stored head would allow that the head does not: the
// confirmation effects of going from head to fallback (going Live, downloads on, Pause off, following or importing an
// arr into a grabbing library, and removing a library). A startup fallback with any is refused. A credential the
// fallback sends to its own earlier address is not one: that generation was published with it.
func (s *Store) FallbackEffects(ctx context.Context, head, fallback *Snapshot) ([]ConfirmationEffect, error) {
	return snapshotWriteEffects(ctx, s.db.Reader(), head, fallback)
}

// clampVersionsToHead keeps a recovered version no more permissive than the stored head: it stays active only while
// the head's version is active and grabs only while the head's version grabs. A version the head no longer has is
// recovered as shadow without grab.
func (s *Store) clampVersionsToHead(ctx context.Context, versions []Version) error {
	type headVersion struct {
		active bool
		grab   bool
	}
	head := map[string]headVersion{}
	rows, err := s.db.Reader().QueryContext(ctx, "SELECT key, config FROM setting_version")
	if err != nil {
		return dbError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return dbError(err)
		}
		config, _, err := versionConfigFromJSON([]byte(raw))
		if err != nil {
			return fmt.Errorf("settings: corrupt library %q", key)
		}
		head[key] = headVersion{active: config.Mode == policy.ModeActive, grab: config.Grab}
	}
	if err := rows.Err(); err != nil {
		return dbError(err)
	}
	for i := range versions {
		now := head[versions[i].Key]
		if versions[i].Config.Mode == policy.ModeActive && !now.active {
			versions[i].Config.Mode = policy.ModeShadow
		}
		versions[i].Config.Grab = versions[i].Config.Grab && now.grab
	}
	return nil
}

// LastPublishedGeneration is the last generation whose runtime was completely prepared and published.
func (s *Store) LastPublishedGeneration(ctx context.Context) (int64, error) {
	var gen int64
	err := s.db.Reader().QueryRowContext(ctx, "SELECT last_published_generation FROM settings_runtime_state WHERE id = 1").Scan(&gen)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return gen, dbError(err)
}

// PreviousPublishedGeneration returns the generation published before the current one.
func (s *Store) PreviousPublishedGeneration(ctx context.Context) (int64, error) {
	var gen sql.NullInt64
	err := s.db.Reader().QueryRowContext(ctx, "SELECT previous_published_generation FROM settings_runtime_state WHERE id = 1").Scan(&gen)
	if errors.Is(err, sql.ErrNoRows) || !gen.Valid {
		return 0, nil
	}
	return gen.Int64, dbError(err)
}

// FallbackGeneration selects the newest published generation that is older than the stored head.
func (s *Store) FallbackGeneration(ctx context.Context, stored int64) (int64, error) {
	last, err := s.LastPublishedGeneration(ctx)
	if err != nil {
		return 0, err
	}
	if last != 0 && last != stored {
		return last, nil
	}
	return s.PreviousPublishedGeneration(ctx)
}

// MarkPublished records the generation after its prepared runtime becomes current.
func (s *Store) MarkPublished(ctx context.Context, gen int64) error {
	return dbError(s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings_runtime_state (id, last_published_generation, previous_published_generation)
			VALUES (1, ?, NULL) ON CONFLICT(id) DO UPDATE SET
			previous_published_generation = CASE WHEN settings_runtime_state.last_published_generation != excluded.last_published_generation
				THEN settings_runtime_state.last_published_generation ELSE settings_runtime_state.previous_published_generation END,
			last_published_generation = excluded.last_published_generation`, gen); err != nil {
			return err
		}
		return pruneSnapshots(ctx, tx, gen)
	}))
}

// PruneSnapshots keeps the published fallbacks and the generation being prepared at startup.
func (s *Store) PruneSnapshots(ctx context.Context, gen int64) error {
	return dbError(s.db.WriteTx(ctx, func(tx *sql.Tx) error { return pruneSnapshots(ctx, tx, gen) }))
}

func pruneSnapshots(ctx context.Context, tx *sql.Tx, gen int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE settings_generation SET snapshot = '{}' WHERE snapshot != '{}'
		AND generation != ? AND generation NOT IN (
			SELECT last_published_generation FROM settings_runtime_state WHERE id = 1
			UNION SELECT previous_published_generation FROM settings_runtime_state WHERE id = 1
				AND previous_published_generation IS NOT NULL)`, gen)
	return err
}

func contentRevision(rows []row, gen int64) string {
	content := append([]row(nil), rows...)
	for i := range content {
		content[i].Record = Record{Key: content[i].Key}
		content[i].secretID = sql.NullInt64{}
	}
	b, _ := json.Marshal(content)
	return revision(fmt.Sprintf("%d/%s", gen, b))
}

func sameRecord(a, b row) bool {
	a.Record, b.Record = Record{}, Record{}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func writeRow(ctx context.Context, tx *sql.Tx, r *row) error {
	args := []any{r.Key, string(r.Config), store.Millis(r.CreatedAt), store.Millis(r.UpdatedAt)}
	columns, updates := "key, config, created_at, updated_at", "config=excluded.config, updated_at=excluded.updated_at"
	placeholders := "?, ?, ?, ?"
	var name string
	for _, t := range tables {
		if t.entity == r.Entity {
			name = t.name
		}
	}
	if r.Entity == "views" || r.Entity == "sections" {
		columns += ", secret_id"
		updates += ", secret_id=excluded.secret_id"
		placeholders += ", ?"
		args = append(args, r.secretID)
	}
	if r.Entity == "indexers" || r.Entity == "providers" {
		rank := "priority"
		if r.Entity == "providers" {
			rank = "order_rank"
		}
		columns += ", kind, enabled, " + rank + ", secret_id"
		updates += ", kind=excluded.kind, enabled=excluded.enabled, " + rank + "=excluded." + rank + ", secret_id=excluded.secret_id"
		placeholders += ", ?, ?, ?, ?"
		args = append(args, r.Kind, r.Enabled, r.Rank, r.secretID)
		if r.Entity == "providers" {
			columns += ", pacing"
			updates += ", pacing=excluded.pacing"
			placeholders += ", ?"
			args = append(args, string(r.Pacing))
		}
	}
	return tx.QueryRowContext(ctx, "INSERT INTO "+name+" ("+columns+") VALUES ("+placeholders+") ON CONFLICT(key) DO UPDATE SET "+updates+" RETURNING id", args...).Scan(&r.ID)
}

func writeAudit(ctx context.Context, tx *sql.Tx, actor string, at time.Time, gen int64, entity, key, op string, before, after any) error {
	before, after = auditValue(before), auditValue(after)
	b, err := json.Marshal(before)
	if err != nil {
		return errors.New("settings: invalid audit before")
	}
	a, err := json.Marshal(after)
	if err != nil {
		return errors.New("settings: invalid audit after")
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO settings_audit (actor, created_at, generation, entity, key, operation, before, after) VALUES (?, ?, ?, ?, ?, ?, ?, ?)", actor, store.Millis(at), gen, entity, key, op, string(b), string(a))
	return err
}

// Audit lists a bounded page after an audit ID, oldest first. A zero limit defaults to 100, maximum 1000.
func (s *Store) Audit(ctx context.Context, after int64, limit int) ([]Audit, error) {
	return s.auditPage(ctx, "WHERE id > ? ORDER BY id", after, limit)
}

// AuditBefore lists a bounded page before an audit ID, newest first; a zero before starts at the newest record.
func (s *Store) AuditBefore(ctx context.Context, before int64, limit int) ([]Audit, error) {
	if before <= 0 {
		before = math.MaxInt64
	}
	return s.auditPage(ctx, "WHERE id < ? ORDER BY id DESC", before, limit)
}

func (s *Store) auditPage(ctx context.Context, where string, cursor int64, limit int) ([]Audit, error) {
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 1000)
	rs, err := s.db.Reader().QueryContext(ctx, "SELECT id, actor, created_at, generation, entity, key, operation, before, after FROM settings_audit "+where+" LIMIT ?", cursor, limit)
	if err != nil {
		return nil, dbError(err)
	}
	defer rs.Close()
	out := []Audit{}
	for rs.Next() {
		var a Audit
		var at int64
		var before, after string
		if err := rs.Scan(&a.ID, &a.Actor, &at, &a.Generation, &a.Entity, &a.Key, &a.Operation, &before, &after); err != nil {
			return nil, dbError(err)
		}
		a.CreatedAt, a.Before, a.After = store.FromMillis(at), maskCardigannAudit(json.RawMessage(before)), maskCardigannAudit(json.RawMessage(after))
		out = append(out, a)
	}
	return out, dbError(rs.Err())
}

func revision(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])[:12]
}

// Never expose driver errors: triggers and constraint failures may include the bound credential value.
func dbError(err error) error {
	if err == nil {
		return nil
	}
	var validation *policy.Errors
	if errors.As(err, &validation) {
		return validation
	}
	var confirmation *ConfirmationError
	if errors.As(err, &confirmation) {
		return confirmation
	}
	for _, sentinel := range []error{ErrConflict, ErrUninitialized, ErrDownloadsChanged, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, sentinel) {
			return sentinel
		}
	}
	return errors.New("settings: operation failed")
}

func redactValidation(err error, secrets map[string]Secret) error {
	var validation *policy.Errors
	if !errors.As(err, &validation) {
		return errors.New("settings: invalid candidate")
	}
	out := &policy.Errors{}
	for _, fe := range validation.List {
		if strings.HasSuffix(fe.Path, "_env") {
			fe.Msg = "invalid credential reference"
		}
		for _, secret := range secrets {
			if secret.value != "" {
				fe.Path = strings.ReplaceAll(fe.Path, secret.value, "set")
				fe.Msg = strings.ReplaceAll(fe.Msg, secret.value, "set")
			}
		}
		out.List = append(out.List, fe)
	}
	return out
}

// RecordAction appends an audit row for an action that changes no settings (a source test), at the stored head
// generation. detail is the "after" document; callers pass only nonsecret fields.
func (s *Store) RecordAction(ctx context.Context, actor, entity, key, operation string, detail any) error {
	if strings.TrimSpace(actor) == "" {
		return errors.New("settings: actor required")
	}
	return dbError(s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		gen, _, err := generation(ctx, tx)
		if err != nil {
			return err
		}
		if gen == 0 {
			return ErrUninitialized
		}
		return writeAudit(ctx, tx, actor, s.clock(), gen, entity, key, operation, nil, detail)
	}))
}

// profileConfigJSON stores a profile's policy config with its origin beside it: the origin is display metadata the
// runtime never reads.
func profileConfigJSON(r QualityProfile) ([]byte, error) {
	b, err := json.Marshal(r.Config)
	if err != nil || r.Origin == nil {
		return b, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	origin, err := json.Marshal(r.Origin)
	if err != nil {
		return nil, err
	}
	m["origin"] = origin
	return json.Marshal(m)
}

// storedSections reads the stored sections a save patches (Records.Sections).
func storedSections(ctx context.Context, tx *sql.Tx) ([]Section, error) {
	rs, err := tx.QueryContext(ctx, "SELECT id, key, created_at, updated_at, config FROM setting_section ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []Section
	for rs.Next() {
		var s Section
		var created, updated int64
		var config string
		if err := rs.Scan(&s.ID, &s.Key, &created, &updated, &config); err != nil {
			return nil, err
		}
		if removedSections[s.Key] {
			continue // a section that no longer exists: left out, so the save deletes its row
		}
		s.CreatedAt, s.UpdatedAt, s.Config = store.FromMillis(created), store.FromMillis(updated), json.RawMessage(config)
		out = append(out, s)
	}
	return out, rs.Err()
}
