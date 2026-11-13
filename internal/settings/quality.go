package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

func qualityRecordsFromPolicy(p *policy.Policy) (Records, error) {
	return qualityRecordsWithTrash(p, "")
}

func requireQualitySpecifications(records Records) error {
	validation := &policy.Errors{}
	for _, r := range records.Formats {
		if len(r.Specification) == 0 {
			validation.List = append(validation.List, policy.FieldError{Path: "formats[" + r.Key + "]", Msg: "full specification content required; configure THEATARR_TRASH for TRaSH imports"})
		}
	}
	for _, set := range records.FormatSets {
		for key, ref := range set.Config.Formats {
			if ref != nil && ref.Ref == "" && len(set.Specifications[key]) == 0 {
				validation.List = append(validation.List, policy.FieldError{Path: "format_sets[" + set.Key + "].formats[" + key + "]", Msg: "full specification content required"})
			}
		}
	}
	if len(validation.List) > 0 {
		return validation
	}
	return nil
}

func qualityRecordsWithTrash(p *policy.Policy, trashRoot string) (Records, error) {
	if p == nil {
		return Records{}, errors.New("settings: policy required")
	}
	out := Records{Profiles: []QualityProfile{}, Formats: []CustomFormat{}, FormatSets: []FormatSet{}, Naming: []VersionNaming{}}
	for _, key := range policyKeys(p.Profiles) {
		out.Profiles = append(out.Profiles, QualityProfile{Record: Record{Key: key}, Config: *p.Profiles[key]})
	}
	for _, key := range policyKeys(p.Formats) {
		ref := *p.Formats[key]
		spec, err := importSpecification(p.Dir, trashRoot, ref)
		if err != nil {
			return Records{}, formatError("formats["+key+"]", err)
		}
		ref.File = ""
		ref.Specification = nil
		out.Formats = append(out.Formats, CustomFormat{Record: Record{Key: key}, Config: ref, Specification: spec})
	}
	for _, key := range policyKeys(p.FormatSets) {
		set := *p.FormatSets[key]
		set.Formats = cloneFormatRefs(set.Formats)
		specs := map[string]json.RawMessage{}
		for name, ref := range set.Formats {
			if ref == nil {
				continue
			}
			input := *ref
			if input.Flavour == "" {
				input.Flavour = set.Flavour
			}
			spec, err := importSpecification(p.Dir, trashRoot, input)
			if err != nil {
				return Records{}, formatError("format_sets["+key+"].formats["+name+"]", err)
			}
			if len(spec) != 0 {
				specs[name] = spec
			}
			ref.File = ""
			ref.Specification = nil
		}
		out.FormatSets = append(out.FormatSets, FormatSet{Record: Record{Key: key}, Config: set, Specifications: specs})
	}
	for _, key := range policyKeys(p.Versions) {
		out.Naming = append(out.Naming, VersionNaming{Record: Record{Key: key}, Config: p.Versions[key].Naming})
	}
	return out, nil
}

func formatSpecification(dir string, ref policy.FormatRef) (json.RawMessage, error) {
	if len(ref.Specification) != 0 {
		return append(json.RawMessage(nil), ref.Specification...), nil
	}
	if ref.File == "" {
		return nil, nil
	}
	if filepath.IsAbs(ref.File) || strings.HasPrefix(filepath.Clean(ref.File), "..") {
		return nil, errors.New("settings: custom format file must stay inside the policy directory")
	}
	base, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, errors.New("settings: cannot read policy directory")
	}
	target, err := filepath.EvalSymlinks(filepath.Join(dir, ref.File))
	if err != nil {
		return nil, errors.New("settings: cannot read custom format JSON")
	}
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("settings: custom format file must stay inside the policy directory")
	}
	b, err := os.ReadFile(target)
	if err != nil {
		return nil, errors.New("settings: cannot read custom format JSON")
	}
	if !json.Valid(b) {
		return nil, errors.New("settings: custom format file is not valid JSON")
	}
	return json.RawMessage(b), nil
}

func cloneFormatRefs(in map[string]*policy.FormatRef) map[string]*policy.FormatRef {
	out := make(map[string]*policy.FormatRef, len(in))
	for key, ref := range in {
		if ref == nil {
			out[key] = nil
			continue
		}
		copy := *ref
		copy.Specification = append(json.RawMessage(nil), ref.Specification...)
		out[key] = &copy
	}
	return out
}

func policyKeys[V any](items map[string]*V) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// BootstrapQuality imports the quality sections once when upgrading a database that already has a source generation.
// It writes one new generation and an audit row naming the imported sections; provider epoch and source records stay
// put.
func (s *Store) BootstrapQuality(ctx context.Context, base *policy.Policy, actor string) (*Snapshot, error) {
	current, err := s.Load(ctx, base)
	if err != nil {
		return nil, err
	}
	if current.qualityManaged {
		return current, nil
	}
	quality, err := qualityRecordsWithTrash(base, s.trashRoot)
	if err != nil {
		return nil, err
	}
	if err := requireQualitySpecifications(quality); err != nil {
		return nil, err
	}
	records := current.Records
	records.Profiles, records.Formats, records.FormatSets, records.Naming = quality.Profiles, quality.Formats, quality.FormatSets, quality.Naming
	// Until this import the service scores with these YAML sections: storing them is no change to confirm, even for
	// the profiles of active versions (whose formats gain their stored specification). Only the four quality
	// sections change.
	req := SaveRequest{ExpectedGeneration: current.Generation, Actor: actor, Base: base, Records: records,
		bootstrapSections: "profiles,formats,format_sets,naming", importAsConfigured: true}
	next, err := s.saveWithHook(ctx, req, "bootstrap-import", func(ctx context.Context, tx *sql.Tx) error {
		initialized, err := qualityInitialized(ctx, tx)
		if err != nil {
			return err
		}
		if initialized {
			return ErrConflict
		}
		sections, _ := json.Marshal([]string{"profiles", "formats", "format_sets", "naming"})
		now := s.clock()
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings_quality_state (id, initialized_at, source, sections)
			VALUES (1, ?, 'bootstrap-import', ?)`, store.Millis(now), string(sections)); err != nil {
			return err
		}
		return writeAudit(ctx, tx, actor, now, req.ExpectedGeneration+1, "settings", "quality_sections", "bootstrap-import", nil,
			map[string]any{"sections": []string{"profiles", "formats", "format_sets", "naming"}})
	})
	if errors.Is(err, ErrConflict) {
		return s.Load(ctx, base)
	}
	return next, err
}

// formatError is a format that cannot be materialized, refused at its field path. err is one of
// importSpecification's fixed messages: it never carries a value from the files.
func formatError(path string, err error) error {
	return &policy.Errors{List: []policy.FieldError{{Path: path, Msg: strings.TrimPrefix(err.Error(), "settings: ")}}}
}

// importSpecification returns a format's full specification. Its errors are fixed messages, without the supplied
// values (formatError).
func importSpecification(dir, trashRoot string, ref policy.FormatRef) (json.RawMessage, error) {
	if ref.TrashID != "" && len(ref.Specification) == 0 && trashRoot != "" {
		t, err := format.LoadTrash(trashRoot, format.Flavour(ref.Flavour))
		if err != nil {
			return nil, errors.New("settings: cannot load the TRaSH formats (check THEATARR_TRASH)")
		}
		raw := t.Raw(ref.TrashID)
		if len(raw) == 0 {
			return nil, errors.New("settings: TRaSH format is missing")
		}
		return append(json.RawMessage(nil), raw...), nil
	}
	return formatSpecification(dir, ref)
}

// validateSpecifications uses the existing arr specification implementations, including their regex compiler.
func validateSpecifications(p *policy.Policy) error {
	validation := &policy.Errors{}
	check := func(path string, ref *policy.FormatRef) {
		if ref == nil || len(ref.Specification) == 0 {
			return
		}
		var spec format.FormatJSON
		if err := json.Unmarshal(ref.Specification, &spec); err != nil {
			validation.List = append(validation.List, policy.FieldError{Path: path, Msg: "invalid custom format JSON"})
			return
		}
		flavours := []format.Flavour{format.Radarr, format.Sonarr}
		if ref.Flavour != "" {
			flavours = []format.Flavour{format.Flavour(ref.Flavour)}
		}
		var buildErr error
		for _, flavour := range flavours {
			_, buildErr = spec.Build(flavour)
			if buildErr == nil {
				return
			}
		}
		validation.List = append(validation.List, policy.FieldError{Path: path, Msg: buildErr.Error()})
	}
	for key, ref := range p.Formats {
		check("formats["+key+"].specification", ref)
	}
	for key, set := range p.FormatSets {
		for name, ref := range set.Formats {
			if ref != nil && ref.Flavour == "" {
				copy := *ref
				copy.Flavour = set.Flavour
				ref = &copy
			}
			check("format_sets["+key+"].formats["+name+"].specification", ref)
		}
	}
	// Unspecified global flavours may be shared; scored specifications must build for each actual consumer.
	for version, v := range p.Versions {
		pr := p.Profiles[v.Profile]
		if pr == nil {
			continue
		}
		flavour := format.Radarr
		if v.Kind == policy.KindSeries {
			flavour = format.Sonarr
		}
		for key := range pr.Formats {
			_, ref := p.ProfileFormat(pr, key)
			if ref == nil || ref.Flavour != "" || len(ref.Specification) == 0 {
				continue
			}
			var spec format.FormatJSON
			if json.Unmarshal(ref.Specification, &spec) != nil {
				continue
			}
			if _, err := spec.Build(flavour); err != nil {
				validation.List = append(validation.List, policy.FieldError{Path: "profiles[" + v.Profile + "].formats[" + key + "]", Msg: "used by versions[" + version + "]: " + err.Error()})
			}
		}
	}
	if len(validation.List) > 0 {
		return validation
	}
	return nil
}

// ExportFiles restores embedded format JSON to relative files for a policy-directory export.
func (s *Snapshot) ExportFiles() (map[string]json.RawMessage, *policy.Policy, error) {
	p, err := s.Policy.Clone()
	if err != nil {
		return nil, nil, err
	}
	for _, ix := range p.Indexers {
		if ix.Kind != "cardigann" {
			continue
		}
		var secret map[string]bool
		var known bool
		if s.cardigannStore != nil {
			secret, known = s.cardigannStore.definitionSettings(ix.Definition)
		}
		for key := range ix.Settings {
			isSecret, declared := secret[key]
			if !known || !declared || isSecret {
				ix.Settings[key] = "[redacted]"
			}
		}
	}
	files := map[string]json.RawMessage{}
	// File references carry trash_id inside the JSON. Policy score lookups use display keys for those files.
	for _, profile := range p.Profiles {
		scores := map[string]int{}
		for key, score := range profile.Formats {
			name, ref := p.ProfileFormat(profile, key)
			if ref != nil && len(ref.Specification) > 0 && key == ref.TrashID {
				key = name
			}
			scores[key] = score
		}
		if profile.Formats != nil {
			profile.Formats = scores
		}
	}
	write := func(scope, key string, ref *policy.FormatRef) {
		if ref == nil || len(ref.Specification) == 0 {
			return
		}
		path := "formats/" + revision(scope+"/"+key) + ".json"
		files[path] = append(json.RawMessage(nil), ref.Specification...)
		ref.File, ref.TrashID, ref.Specification = path, "", nil
	}
	for key, ref := range p.Formats {
		write("global", key, ref)
	}
	for key, set := range p.FormatSets {
		for name, ref := range set.Formats {
			write(key, name, ref)
		}
	}
	return files, p, nil
}
