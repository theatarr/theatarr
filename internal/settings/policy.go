package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/secrets"
)

func candidate(base *policy.Policy, records Records) (*policy.Policy, error) {
	p, err := diagnosticCandidate(base, records)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// diagnosticCandidate retains an assembled policy when validation fails.
func diagnosticCandidate(base *policy.Policy, records Records) (*policy.Policy, error) {
	if base == nil {
		return nil, errors.New("settings: base policy required")
	}
	p, err := base.Clone()
	if err != nil {
		return nil, errors.New("settings: cannot clone policy")
	}
	for _, r := range records.Sections {
		if err := applySection(p, r); err != nil {
			return nil, err
		}
	}
	p.Indexers = map[string]*policy.Indexer{}
	p.Providers = map[string]*policy.Provider{}
	p.Budgets = map[string]*policy.Budget{}
	p.Versions = map[string]*policy.VersionPolicy{}
	views := make(map[string]*policy.View, len(p.Views))
	for key, view := range p.Views {
		copy := *view
		copy.Versions = append([]string(nil), view.Versions...)
		copy.Profiles = cloneVersionSets(view.Profiles)
		views[key] = &copy
	}
	p.Views = views
	for _, r := range records.Indexers {
		config := r.Config
		p.Indexers[r.Key] = &config
	}
	for _, r := range records.Providers {
		config := r.Config
		// Validate still requires an env-shaped reference. DB-only accounts use the credential adapter;
		// this sentinel is never resolved through the environment or stored as a fallback name.
		if config.APIKeyEnv == "" {
			config.APIKeyEnv = CredentialSentinel
		}
		p.Providers[r.Key] = &config
	}
	for _, r := range records.Budgets {
		config := r.Config
		p.Budgets[r.Key] = &config
	}
	rootConfigs := make(map[string]*policy.LibraryVersion, len(records.Versions))
	for _, r := range records.Versions {
		config := r.Config
		if existing := base.Versions[r.Key]; existing != nil {
			config.Naming = existing.Naming
		}
		p.Versions[r.Key] = &config
		if r.Roots == nil {
			continue
		}
		roots := append([]string(nil), r.Roots...)
		var section *policy.LibraryVersion
		if p.Library != nil {
			section = p.Library.Versions[r.Key]
		}
		libraryVersion := &policy.LibraryVersion{Roots: roots}
		if section != nil {
			libraryVersion.PlexSection = section.PlexSection
		}
		rootConfigs[r.Key] = libraryVersion
	}
	if p.Library == nil && len(rootConfigs) > 0 {
		p.Library = &policy.Library{Versions: rootConfigs}
	} else if p.Library != nil {
		p.Library.Versions = rootConfigs
	}
	for _, r := range records.Views {
		view := p.Views[r.Key]
		if r.Identity != nil {
			// The key is the binding's credential; without one the view accepts no key, as with an unset variable.
			view = &policy.View{Flavour: r.Identity.Flavour, Path: r.Identity.Path, AliasFrom: slices.Clone(r.Identity.AliasFrom),
				APIKeyEnv: settingsSecretEnv("views/"+r.Key, "api_key")}
			p.Views[r.Key] = view
		}
		if view == nil {
			return nil, &policy.Errors{List: []policy.FieldError{{Path: "views[" + r.Key + "]", Msg: "view metadata is missing from the policy files"}}}
		}
		view.Versions = append([]string(nil), r.Versions...)
		view.Profiles = cloneVersionSets(r.Profiles)
	}
	for key := range p.Views {
		if !hasViewBinding(records.Views, key) {
			delete(p.Views, key)
		}
	}
	if records.Profiles != nil {
		p.Profiles = map[string]*policy.Profile{}
		for _, r := range records.Profiles {
			config := r.Config
			p.Profiles[r.Key] = &config
		}
	}
	if records.Formats != nil {
		p.Formats = map[string]*policy.FormatRef{}
		for _, r := range records.Formats {
			config := r.Config
			config.Specification = append(json.RawMessage(nil), r.Specification...)
			p.Formats[r.Key] = &config
		}
	}
	if records.FormatSets != nil {
		p.FormatSets = map[string]*policy.FormatSet{}
		for _, r := range records.FormatSets {
			config := r.Config
			config.Formats = cloneFormatRefs(config.Formats)
			for key, ref := range config.Formats {
				if ref != nil {
					ref.Specification = append(json.RawMessage(nil), r.Specifications[key]...)
				}
			}
			p.FormatSets[r.Key] = &config
		}
	}
	if records.Naming != nil {
		for _, version := range p.Versions {
			version.Naming = policy.Naming{}
		}
		for _, r := range records.Naming {
			version := p.Versions[r.Key]
			if version == nil {
				return nil, &policy.Errors{List: []policy.FieldError{{Path: "versions[" + r.Key + "].naming", Msg: "unknown library"}}}
			}
			version.Naming = r.Config
		}
	}
	if records.Switches != nil {
		records.Switches.apply(p)
	}
	if err := p.Validate(); err != nil {
		return p, err
	}
	if err := validateSpecifications(p); err != nil {
		return p, err
	}
	ApplyForceOff(p)
	return p, nil
}

func hasViewBinding(bindings []ViewBinding, key string) bool {
	for _, binding := range bindings {
		if binding.Key == key {
			return true
		}
	}
	return false
}

// ImportRequest is an explicit whole-policy YAML import. Dir uses policy.Load's directory layout and strictness.
// The source records (indexers, providers, budgets), the quality records (profiles, formats, format sets, naming)
// and the version records (versions and view bindings) come from the files; settings sections and switches are
// kept as stored (importOver).
type ImportRequest struct {
	Dir       string
	TrashRoot string
	// Base is the policy the stored settings load over (its Dir resolves relative data paths). Bootstrap may omit
	// it to use the imported policy.
	Base                  *policy.Policy
	ExpectedGeneration    int64
	Actor                 string
	Lookup                LookupSecret
	OrphanActiveDownloads bool
	ConfirmationToken     string
	// RequireQuality preflights quality materialization before even the first source bootstrap transaction.
	RequireQuality bool
	// firstRun is the bootstrap of an empty database: a missing policy directory or missing sections import as empty.
	firstRun bool
}

func importRequest(req ImportRequest) (SaveRequest, error) {
	// A whole base (a full policy) must be valid on its own. With the database as the settings source the base is
	// the YAML without the DB-owned sections (ReadBase): its references to indexers, providers and budgets resolve
	// only against the imported records, so it is checked merged, by candidate below.
	if b := req.Base; b != nil && b.Indexers != nil && b.Providers != nil && b.Budgets != nil {
		check, err := b.Clone()
		if err != nil {
			return SaveRequest{}, errors.New("settings: cannot clone base")
		}
		if err := check.Validate(); err != nil {
			return SaveRequest{}, safeImportValidation(err)
		}
	}
	// Read separately so parse errors cannot echo malformed inline secret values.
	read := policy.Read
	if req.firstRun {
		read = policy.ReadOrEmpty
	}
	p, err := read(req.Dir)
	if err != nil {
		return SaveRequest{}, errors.New("settings: cannot parse policy import")
	}
	// An omitted record group would replace the stored records with none. A database without a generation has none
	// to lose: its first import (or the first run) may lack one.
	requireGroups := !req.firstRun && req.ExpectedGeneration > 0
	if requireGroups && (p.Indexers == nil || p.Providers == nil || p.Budgets == nil || p.Versions == nil || p.Views == nil) {
		return SaveRequest{}, errors.New("settings: import requires indexers, providers, budgets, versions and views sections")
	}
	// The files alone need not be valid: they may name what only the stored sections hold (a client, a budget), so
	// they are validated once merged (importOver; the first run below). Their records still carry the defaults.
	p.ApplyDefaults()
	// Materializing the formats resolves and then clears their source fields, so their own input is checked first.
	if err := p.ValidateFormats(); err != nil {
		return SaveRequest{}, safeImportValidation(err)
	}
	versions, err := yamlVersions(req.Dir, req.firstRun, requireGroups)
	if err != nil {
		return SaveRequest{}, err
	}
	records := versions
	writes := map[string]Secret{}
	for key, config := range p.Indexers {
		records.Indexers = append(records.Indexers, Indexer{Record: Record{Key: key}, Enabled: true, Config: *config})
	}
	for key, config := range p.Providers {
		records.Providers = append(records.Providers, Provider{Record: Record{Key: key}, Enabled: true, Config: *config})
	}
	for key, config := range p.Budgets {
		records.Budgets = append(records.Budgets, Budget{Record: Record{Key: key}, Config: *config})
	}
	quality, err := qualityRecordsWithTrash(p, req.TrashRoot)
	if err != nil {
		return SaveRequest{}, err
	}
	if req.RequireQuality || req.TrashRoot != "" {
		if err := requireQualitySpecifications(quality); err != nil {
			return SaveRequest{}, err
		}
	}
	records.Profiles, records.Formats, records.FormatSets, records.Naming = quality.Profiles, quality.Formats,
		quality.FormatSets, quality.Naming
	resolve := func(ref, name string) error {
		if name == "" {
			return nil
		}
		if req.Lookup == nil {
			return errors.New("settings: import requires secret lookup")
		}
		value, ok := req.Lookup(name)
		if !ok || value == "" {
			return errors.New("settings: import requires a missing credential")
		}
		writes[ref] = NewSecret(value)
		return nil
	}
	for _, r := range records.Indexers {
		if err := resolve("indexers/"+r.Key, r.Config.APIKeyEnv); err != nil {
			return SaveRequest{}, err
		}
	}
	for _, r := range records.Providers {
		if err := resolve("providers/"+r.Key, r.Config.APIKeyEnv); err != nil {
			return SaveRequest{}, err
		}
	}
	// An import carries the views' identity and keys from the files; a view without api_key_env keeps its stored
	// key. The first start leaves both to BootstrapSections, which seeds them once with the other sections.
	for i, r := range records.Views {
		if v := p.Views[r.Key]; v != nil && !req.firstRun {
			records.Views[i].Identity = viewIdentity(v)
			if v.APIKeyEnv != "" && req.Lookup != nil {
				if value, ok := req.Lookup(v.APIKeyEnv); ok && value != "" {
					writes["views/"+r.Key] = NewSecret(value)
				}
			}
		}
	}
	if !req.firstRun {
		// Every section from the files; importOver keeps the stored ones and fills only those missing.
		for _, spec := range Sections {
			config, err := sectionJSON(p, spec.Key)
			if err != nil {
				return SaveRequest{}, err
			}
			records.Sections = append(records.Sections, Section{Record: Record{Key: spec.Key}, Config: config})
		}
		// Explicit imports never turn Pause off.
		records.Switches = &GlobalSwitches{Paused: true}
	}
	base := req.Base
	if base == nil {
		base = p
	}
	// The first run stores exactly this; any other import is validated by importOver, after the stored state
	// is merged in.
	if req.firstRun {
		if _, err := candidate(base, records); err != nil {
			return SaveRequest{}, importValidation(err, writes)
		}
	}
	return SaveRequest{ExpectedGeneration: req.ExpectedGeneration, Actor: req.Actor, Base: base, Records: records,
		SecretWrites: writes, ConfirmationToken: req.ConfirmationToken}, nil
}

// importOver turns an import's records into the one candidate every import path saves or previews, over the
// stored records current: record groups come from the files (keeping each source's stored state), the stored
// sections are kept and only the sections current lacks are filled from the files, and the global switches and sync
// sources stay as stored (a new database has its switches off). The result is validated only after that merge. It
// returns the keys of the sections filled from the files.
func importOver(write *SaveRequest, current Records) ([]string, error) {
	keepSourceState(&write.Records, current)
	filled := []string{}
	for _, section := range write.Records.Sections {
		if !slices.ContainsFunc(current.Sections, func(s Section) bool { return s.Key == section.Key }) {
			filled = append(filled, section.Key)
		}
	}
	sort.Strings(filled)
	write.Records.Sections = mergeSections(write.Records.Sections, current.Sections)
	write.Records.SyncSources = current.SyncSources
	if current.Switches != nil {
		write.Records.Switches = current.Switches
	}
	if _, err := candidate(write.Base, write.Records); err != nil {
		return nil, importValidation(err, write.SecretWrites)
	}
	return filled, nil
}

// importValidation is the refusal of an import's merged settings: field paths only, since the files' values can
// carry what was never meant as a value (an inline credential in a wrong field).
func importValidation(err error, writes map[string]Secret) error {
	var validation *policy.Errors
	if !errors.As(err, &validation) {
		return redactValidation(err, writes)
	}
	return safeImportValidation(redactValidation(err, writes))
}

func safeImportValidation(err error) error {
	var validation *policy.Errors
	if !errors.As(err, &validation) {
		return errors.New("settings: invalid import")
	}
	out := &policy.Errors{}
	for _, fe := range validation.List {
		out.List = append(out.List, policy.FieldError{Path: fe.Path, Msg: "invalid policy value"})
	}
	return out
}

// importAt is importRequest over the stored head at req.ExpectedGeneration, keeping each existing source's enabled
// state and indexer priority (keepSourceState). The save rechecks the generation inside its transaction.
func (s *Store) importAt(ctx context.Context, req ImportRequest) (SaveRequest, error) {
	if req.TrashRoot == "" {
		req.TrashRoot = s.trashRoot
	}
	write, err := importRequest(req)
	if err != nil {
		return SaveRequest{}, err
	}
	current, err := s.Load(ctx, req.Base)
	if err != nil {
		return SaveRequest{}, err
	}
	if current.Generation != req.ExpectedGeneration {
		return SaveRequest{}, ErrConflict
	}
	if err := requireQualitySpecifications(write.Records); err != nil {
		return SaveRequest{}, err
	}
	if _, err := importOver(&write, current.Records); err != nil {
		return SaveRequest{}, err
	}
	return write, nil
}

// keepSourceState carries each existing source's enabled state and indexer priority into an import. The policy
// files have no field for either (the strict loader refuses unknown fields), so an import never sets them
// explicitly: they stay as stored, and sources new to the database are enabled with priority 0, as at bootstrap.
// Provider order is a required policy field (order >= 1), so it always comes from the file. A profile's origin (the
// TRaSH preset it started from) has no policy field either and stays as stored for a profile the import keeps.
func keepSourceState(records *Records, current Records) {
	origins := map[string]*ProfileOrigin{}
	for _, r := range current.Profiles {
		origins[r.Key] = r.Origin
	}
	for i := range records.Profiles {
		if records.Profiles[i].Origin == nil {
			records.Profiles[i].Origin = origins[records.Profiles[i].Key]
		}
	}
	indexers := map[string]Indexer{}
	for _, r := range current.Indexers {
		indexers[r.Key] = r
	}
	for i := range records.Indexers {
		if prev, ok := indexers[records.Indexers[i].Key]; ok {
			records.Indexers[i].Enabled, records.Indexers[i].Priority = prev.Enabled, prev.Priority
		}
	}
	providers := map[string]Provider{}
	for _, r := range current.Providers {
		providers[r.Key] = r
	}
	for i := range records.Providers {
		if prev, ok := providers[records.Providers[i].Key]; ok {
			records.Providers[i].Enabled = prev.Enabled
		}
	}
}

// Bootstrap imports once. An initialized database is returned before reading Dir or resolving any environment
// names. Later starts must use Load with their current YAML-only base.
func (s *Store) Bootstrap(ctx context.Context, base *policy.Policy, req ImportRequest) (*Snapshot, error) {
	var marker int64
	err := s.db.Reader().QueryRowContext(ctx, "SELECT generation FROM settings_e3_bootstrap WHERE id = 1").Scan(&marker)
	if err == nil {
		return s.Load(ctx, base)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, dbError(err)
	}
	gen, _, err := generation(ctx, s.db.Reader())
	if err != nil {
		return nil, dbError(err)
	}
	req.ExpectedGeneration, req.Base = gen, base
	var write SaveRequest
	sections := "versions,views"
	if gen == 0 {
		if req.TrashRoot == "" {
			req.TrashRoot = s.trashRoot
		}
		req.firstRun = true
		write, err = importRequest(req)
		// Keep the initial source import separate: BootstrapQuality adds the quality sections after it, including on
		// upgrades.
		write.Records.Profiles, write.Records.Formats, write.Records.FormatSets, write.Records.Naming = nil, nil, nil, nil
		sections = "indexers,providers,budgets,versions,views"
	} else {
		var existing Records
		tx, beginErr := s.db.Reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if beginErr != nil {
			return nil, dbError(beginErr)
		}
		rows, _, readErr := readRows(ctx, tx)
		if readErr == nil {
			gen, _, readErr = generation(ctx, tx)
		}
		qualityManaged := false
		if readErr == nil {
			qualityManaged, readErr = qualityInitialized(ctx, tx)
		}
		_ = tx.Rollback()
		if readErr != nil {
			return nil, dbError(readErr)
		}
		existing, err = recordsFrom(rows, qualityManaged)
		if err != nil {
			return nil, err
		}
		// The upgrade takes versions and bindings from base (the caller's YAML read, whose source sections the
		// database already owns and ignores), so stale or removed source files cannot block it; candidate validates
		// the result against the stored source records.
		if base == nil || base.Versions == nil || base.Views == nil {
			return nil, errors.New("settings: import requires versions and views sections")
		}
		versions := versionRecords(base)
		existing.Versions, existing.Views = versions.Versions, versions.Views
		if _, err := candidate(base, existing); err != nil {
			return nil, err
		}
		// Preflight the quality import as the fresh path does, so a start that would fail BootstrapQuality fails
		// before the version records commit.
		if req.RequireQuality && !qualityManaged {
			trashRoot := req.TrashRoot
			if trashRoot == "" {
				trashRoot = s.trashRoot
			}
			quality, err := qualityRecordsWithTrash(base, trashRoot)
			if err != nil {
				return nil, err
			}
			if err := requireQualitySpecifications(quality); err != nil {
				return nil, err
			}
			check := existing
			check.Profiles, check.Formats, check.FormatSets, check.Naming = quality.Profiles, quality.Formats, quality.FormatSets, quality.Naming
			if _, err := candidate(base, check); err != nil {
				return nil, err
			}
		}
		write = SaveRequest{ExpectedGeneration: gen, Actor: req.Actor, Base: base, Records: existing, bootstrap: true,
			bootstrapSections: sections}
	}
	if err != nil {
		return nil, err
	}
	write.bootstrap, write.bootstrapSections = true, sections
	commitMarker := func(ctx context.Context, tx *sql.Tx) error {
		var current int64
		if err := tx.QueryRowContext(ctx, "SELECT generation FROM settings_generation ORDER BY generation DESC LIMIT 1").Scan(&current); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO settings_e3_bootstrap (id, generation) VALUES (1, ?)", current)
		return err
	}
	snap, err := s.saveWithHook(ctx, write, "bootstrap-import", commitMarker)
	if errors.Is(err, ErrConflict) {
		return s.Bootstrap(ctx, write.Base, req)
	}
	return snap, err
}

// Import is for bootstrap/offline use; live callers use RuntimeCoordinator.ImportAndApply.
// It explicitly replaces the source, quality and version sections from a fully valid policy directory. Preview
// first for review.
// Imports preserve global switches. Write-enabling version changes require the live confirmation flow.
func (s *Store) Import(ctx context.Context, req ImportRequest) (*Snapshot, error) {
	if req.Base == nil {
		return nil, errors.New("settings: import base required")
	}
	write, err := s.importAt(ctx, req)
	if err != nil {
		return nil, err
	}
	return s.save(ctx, write, "import")
}

// Preview contains only nonsecret records and credential presence markers. It is not an apply token;
// Import rechecks ExpectedGeneration and rereads/validates its input.
type Preview struct {
	Generation int64                      `json:"generation"`
	Before     Records                    `json:"before"`
	After      Records                    `json:"after"`
	YAML       string                     `json:"yaml"`
	Files      map[string]json.RawMessage `json:"files"`
	// CredentialChanged lists the records ("indexers/<key>", "providers/<key>") that exist before and after the
	// import and whose resolved credential the import replaces. It is computed here, before redaction; no value or
	// hash leaves this package.
	CredentialChanged map[string]bool `json:"credential_changed,omitempty"`
	// SectionsFromFiles lists the settings sections the stored generation lacks, which the import fills from the
	// files; every other section stays as stored.
	SectionsFromFiles []string `json:"sections_from_files"`
}

// PreviewImport validates and resolves credentials exactly as Import, without changing any DB state.
func (s *Store) PreviewImport(ctx context.Context, req ImportRequest) (*Preview, error) {
	if req.TrashRoot == "" {
		req.TrashRoot = s.trashRoot
	}
	if req.Base == nil {
		return nil, errors.New("settings: import base required")
	}
	write, err := importRequest(req)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Reader().BeginTx(ctx, nil)
	if err != nil {
		return nil, dbError(err)
	}
	defer tx.Rollback()
	gen, _, err := generation(ctx, tx)
	if err != nil {
		return nil, dbError(err)
	}
	if gen != req.ExpectedGeneration {
		return nil, ErrConflict
	}
	rows, secrets, err := readRows(ctx, tx)
	if err != nil {
		return nil, dbError(err)
	}
	qualityManaged, err := qualityInitialized(ctx, tx)
	if err != nil {
		return nil, dbError(err)
	}
	before, err := recordsFrom(rows, qualityManaged)
	if err != nil {
		return nil, err
	}
	filled, err := importOver(&write, before)
	if err != nil {
		return nil, err
	}
	if qualityManaged {
		if err := requireQualitySpecifications(write.Records); err != nil {
			return nil, err
		}
	}
	rows, err = rowsFrom(write.Records)
	if err != nil {
		return nil, err
	}
	// The credential each existing record resolves to now: the stored value (an explicit clear is no credential),
	// else, for a record that never had a DB credential, its environment fallback.
	envs := map[string]string{}
	for _, r := range before.Indexers {
		envs["indexers/"+r.Key] = r.Config.APIKeyEnv
	}
	for _, r := range before.Providers {
		envs["providers/"+r.Key] = r.Config.APIKeyEnv
	}
	resolved := func(secret Secret, env string) string {
		if secret.value == "" && !secret.stored && env != "" && req.Lookup != nil {
			if v, ok := req.Lookup(env); ok {
				return v
			}
		}
		return secret.value
	}
	old := map[string]string{}
	for ref, env := range envs {
		old[ref] = resolved(secrets[ref], env)
	}
	for ref, secret := range write.SecretWrites {
		secrets[ref] = secret
	}
	for i := range rows {
		if credentialEntity(rows[i].Entity) {
			rows[i].setSecret(secrets[rows[i].ref()])
		}
	}
	after, err := recordsFrom(rows, true)
	if err != nil {
		return nil, err
	}
	snap, err := s.snapshot(write.Base, rows, secrets, gen, "preview", 0, true)
	if err != nil {
		return nil, err
	}
	b, err := snap.Export()
	if err != nil {
		return nil, err
	}
	files, _, err := snap.ExportFiles()
	if err != nil {
		return nil, err
	}
	// secret_set is the effective credential, as the admin API reports it for stored records.
	for i, r := range before.Indexers {
		before.Indexers[i].SecretSet = old["indexers/"+r.Key] != ""
	}
	for i, r := range before.Providers {
		before.Providers[i].SecretSet = old["providers/"+r.Key] != ""
	}
	for i, r := range after.Indexers {
		after.Indexers[i].SecretSet = resolved(secrets["indexers/"+r.Key], r.Config.APIKeyEnv) != ""
	}
	for i, r := range after.Providers {
		after.Providers[i].SecretSet = resolved(secrets["providers/"+r.Key], r.Config.APIKeyEnv) != ""
	}
	changed := map[string]bool{}
	for _, r := range after.Indexers {
		ref := "indexers/" + r.Key
		if prev, ok := old[ref]; ok && prev != resolved(secrets[ref], r.Config.APIKeyEnv) {
			changed[ref] = true
		}
	}
	for _, r := range after.Providers {
		ref := "providers/" + r.Key
		if prev, ok := old[ref]; ok && prev != resolved(secrets[ref], r.Config.APIKeyEnv) {
			changed[ref] = true
		}
	}
	return &Preview{Generation: gen, Before: before, After: after, YAML: string(b), Files: files, CredentialChanged: changed,
		SectionsFromFiles: filled}, nil
}

// SectionsNote says in one line which settings sections the import takes from the files.
func (p *Preview) SectionsNote() string {
	if len(p.SectionsFromFiles) == 0 {
		return "Settings sections are kept as stored; none is filled from the files."
	}
	return "Settings sections are kept as stored; filled from the files: " + strings.Join(p.SectionsFromFiles, ", ") + "."
}

// Export emits a review document for the whole policy, stripping every *_env field, including YAML-owned
// sections, and redacting URLs that can carry credentials (redactExport). Credential presence is listed separately. It deliberately requires credentials to be supplied
// again before strict import; it is not a compatibility bundle. Enabled/priority are DB-only source metadata.
func (s *Snapshot) Export() ([]byte, error) {
	_, exported, err := s.ExportFiles()
	if err != nil {
		return nil, err
	}
	b, err := yaml.Marshal(exported)
	if err != nil {
		return nil, errors.New("settings: cannot export policy")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, errors.New("settings: cannot export policy")
	}
	redactExport(&doc)
	b, err = yaml.Marshal(&doc)
	if err != nil {
		return nil, errors.New("settings: cannot export policy")
	}
	var header strings.Builder
	header.WriteString("# Review export: credentials, environment names and webhook URL paths omitted. Supply credentials before import.\n")
	refs := make([]string, 0, len(s.secrets))
	for ref := range s.secrets {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		if strings.HasPrefix(ref, "sections/") {
			for _, path := range secretPaths(s.secrets[ref]) {
				fmt.Fprintf(&header, "# %s %s credential: set\n", ref, path)
			}
			continue
		}
		fmt.Fprintf(&header, "# %s credential: %s\n", ref, s.secrets[ref].String())
	}
	var required []string
	for env, paths := range s.Policy.SecretEnvs() {
		if strings.HasPrefix(env, policy.SettingsSecretPrefix) {
			continue
		}
		for _, path := range paths {
			if strings.HasPrefix(path, "indexers[") || strings.HasPrefix(path, "providers[") {
				continue
			}
			// Credentials named by an environment variable: their values/presence are unknown to this adapter.
			required = append(required, strings.TrimSuffix(path, "_env"))
		}
	}
	sort.Strings(required)
	for _, path := range required {
		fmt.Fprintf(&header, "# %s: supply separately (environment)\n", path)
	}
	return append([]byte(header.String()), b...), nil
}

// redactExport removes every *_env field and redacts URLs: a notify target's URL (webhook URLs often carry their
// token in the path or query) keeps only scheme and host, and any other URL loses its user information and query
// values; token-like path segments use the section-read masker. Validation already refuses credentials in most
// policy URLs; the export does not rely on it.
func redactExport(n *yaml.Node) {
	redactNode(n, "")
}

func redactNode(n *yaml.Node, path string) {
	switch n.Kind {
	case yaml.MappingNode:
		content := n.Content[:0]
		for i := 0; i < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if strings.HasSuffix(key.Value, "_env") {
				continue
			}
			child := path + "/" + key.Value
			if value.Kind == yaml.ScalarNode && key.Value == "url" && strings.HasPrefix(path, "/notify/targets/") {
				value.Value = redactURL(value.Value, true)
			} else {
				redactNode(value, child)
			}
			content = append(content, key, value)
		}
		n.Content = content
	case yaml.ScalarNode:
		n.Value = redactURL(n.Value, false)
	default:
		for _, child := range n.Content {
			redactNode(child, path)
		}
	}
}

// redactURL redacts an absolute URL: whole keeps only scheme and host; otherwise user information is dropped and
// query values replaced, and token-like path segments are masked as in section reads.
// Anything that is not an absolute URL is returned unchanged.
func redactURL(raw string, whole bool) string {
	if whole {
		return (secrets.URLMasker{Whole: true}).Mask(raw)
	}
	raw = sectionURLs.Mask(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}

	if u.User == nil && u.RawQuery == "" {
		return raw
	}
	u.User = nil
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			q[k] = []string{"redacted"}
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// String and GoString prevent formatting private snapshot fields from exposing credential bytes.
func (s Snapshot) String() string                { b, _ := json.Marshal(s); return string(b) }
func (s Snapshot) GoString() string              { return s.String() }
func (s Snapshot) Format(f fmt.State, verb rune) { fmt.Fprint(f, s.String()) }
