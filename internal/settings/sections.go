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
	"log/slog"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
	"go.yaml.in/yaml/v3"
)

// SectionSpec is one policy section the settings database owns as a whole (docs/design/settings.md "Sections"): its
// policy key and whether the running services follow a change without a restart.
type SectionSpec struct {
	Key string `json:"key"`
	// Live: the next runtime generation uses the change. Otherwise the services built at startup keep the old value
	// until the service restarts. Every section is live in theatarr serve: each generation builds the services that
	// read it, and the loops it schedules restart on a change (cmd/theatarr/serveliveworkers.go).
	Live bool `json:"live"`
}

// Sections lists the database-owned sections in their editor order. Their switches (library.enabled,
// notify.enabled, router.enabled) belong to the global switches record and the library roots to the version records.
var Sections = []SectionSpec{
	{Key: "metadata", Live: true}, {Key: "library", Live: true}, {Key: "notify", Live: true}, {Key: "clients", Live: true},
	{Key: "egress", Live: true}, {Key: "torrents", Live: true}, {Key: "cardigann", Live: true}, {Key: "shadow", Live: true},
	{Key: "import", Live: true}, {Key: "playlists", Live: true}, {Key: "ui", Live: true}, {Key: "provider_sync", Live: true},
	{Key: "router", Live: true},
}

// SectionLive reports whether key is a database-owned section that applies without a restart.
func SectionLive(key string) (live, known bool) {
	for _, spec := range Sections {
		if spec.Key == key {
			return spec.Live, true
		}
	}
	return false, false
}

// Section is one database-owned policy section as YAML-shaped JSON (policy field names); null is a section the
// policy leaves out. Credentials of its *_env fields are stored beside it, write-only: SecretsSet lists the paths
// (an *_env field's path without "_env", e.g. "plex.token") that have one.
type Section struct {
	Record
	Config     json.RawMessage `json:"config"`
	SecretsSet []string        `json:"secrets_set,omitempty"`
}

// ViewIdentity is a view's own settings besides its version sets: where it answers, as which arr, and the arr
// instances whose ids it answers. Its API key is the binding's credential; api_key_env is import-only, as for the
// indexers and providers.
type ViewIdentity struct {
	Flavour   string   `json:"flavour"`
	Path      string   `json:"path"`
	AliasFrom []string `json:"alias_from,omitempty"`
}

func viewIdentity(v *policy.View) *ViewIdentity {
	return &ViewIdentity{Flavour: v.Flavour, Path: v.Path, AliasFrom: slices.Clone(v.AliasFrom)}
}

// settingsSecretEnv is the environment-shaped name a stored credential takes in the runtime policy. It is derived
// from the credential's place only, never its value, and the generation's Policy.Getenv answers it.
func settingsSecretEnv(ref, path string) string {
	sum := sha256.Sum256([]byte(ref + "\x00" + path))
	return policy.SettingsSecretPrefix + strings.ToUpper(hex.EncodeToString(sum[:8]))
}

// policyField is the Policy field whose YAML name is key.
func policyField(p *policy.Policy, key string) (reflect.Value, bool) {
	v := reflect.ValueOf(p).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if yamlName(t.Field(i)) == key {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

func yamlName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
	if name == "-" || !f.IsExported() {
		return ""
	}
	return name
}

// yamlValue renders v as the policy files would hold it, keeping nil slices and maps as null: an omitted list can
// mean something else than an empty one (egress.require_vpn_for).
func yamlValue(v reflect.Value) (any, error) {
	if !v.IsValid() {
		return nil, nil
	}
	if m, ok := v.Interface().(yaml.Marshaler); ok && (v.Kind() != reflect.Pointer || !v.IsNil()) {
		out, err := m.MarshalYAML()
		if err != nil {
			return nil, err
		}
		return yamlValue(reflect.ValueOf(out))
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil, nil
		}
		return yamlValue(v.Elem())
	case reflect.Struct:
		out := map[string]any{}
		for i := 0; i < v.NumField(); i++ {
			name := yamlName(v.Type().Field(i))
			if name == "" {
				continue
			}
			value, err := yamlValue(v.Field(i))
			if err != nil {
				return nil, err
			}
			out[name] = value
		}
		return out, nil
	case reflect.Slice:
		if v.IsNil() {
			return nil, nil
		}
		out := make([]any, v.Len())
		for i := range out {
			value, err := yamlValue(v.Index(i))
			if err != nil {
				return nil, err
			}
			out[i] = value
		}
		return out, nil
	case reflect.Map:
		if v.IsNil() {
			return nil, nil
		}
		out := map[string]any{}
		iter := v.MapRange()
		for iter.Next() {
			value, err := yamlValue(iter.Value())
			if err != nil {
				return nil, err
			}
			out[fmt.Sprint(iter.Key().Interface())] = value
		}
		return out, nil
	default:
		return v.Interface(), nil
	}
}

// sectionJSON is p's section key as stored: YAML-shaped JSON without the parts other records own.
func sectionJSON(p *policy.Policy, key string) (json.RawMessage, error) {
	f, ok := policyField(p, key)
	if !ok {
		return nil, fmt.Errorf("settings: unknown section %s", key)
	}
	value, err := yamlValue(f)
	if err != nil {
		return nil, errors.New("settings: cannot encode section " + key)
	}
	if m, ok := value.(map[string]any); ok {
		stripOwned(key, m)
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("settings: cannot encode section " + key)
	}
	return b, nil
}

// stripOwned removes what other settings records own: the global switches and the library write roots.
func stripOwned(key string, m map[string]any) {
	switch key {
	case "library", "notify", "router":
		delete(m, "enabled")
	}
	if key != "library" {
		return
	}
	if versions, ok := m["versions"].(map[string]any); ok {
		for _, v := range versions {
			if fields, ok := v.(map[string]any); ok {
				delete(fields, "roots")
			}
		}
	}
}

// SectionOwnedElsewhere refuses fields of a submitted section that other editors own.
func SectionOwnedElsewhere(key string, config json.RawMessage) error {
	var m map[string]any
	if json.Unmarshal(config, &m) != nil || m == nil {
		return nil
	}
	var list []policy.FieldError
	switch key {
	case "library", "notify", "router":
		if _, ok := m["enabled"]; ok {
			list = append(list, policy.FieldError{Path: key + ".enabled", Msg: "set by the global switch (Admin › Settings)"})
		}
	}
	if versions, ok := m["versions"].(map[string]any); ok && key == "library" {
		for _, name := range sortedPolicyKeys(versions) {
			if fields, ok := versions[name].(map[string]any); ok {
				if _, ok := fields["roots"]; ok {
					list = append(list, policy.FieldError{Path: "library.versions[" + name + "].roots", Msg: "set in the version's settings (roots)"})
				}
			}
		}
	}
	if len(list) > 0 {
		return &policy.Errors{List: list}
	}
	return nil
}

// decodeSection decodes a stored or submitted section strictly, as the policy files are decoded.
func decodeSection(key string, raw json.RawMessage, into reflect.Value) error {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(into.Addr().Interface()); err != nil {
		msg := err.Error()
		if i := strings.LastIndex(msg, ": "); i >= 0 && strings.Contains(msg, "not found in type") {
			msg = "unknown field" + msg[i:]
		} else {
			msg = "invalid value"
		}
		return &policy.Errors{List: []policy.FieldError{{Path: key, Msg: msg}}}
	}
	return nil
}

func nullSection(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || string(trimmed) == "null"
}

// applySection replaces p's section with the stored one, keeping the switch the global switches record owns.
func applySection(p *policy.Policy, s Section) error {
	f, ok := policyField(p, s.Key)
	if !ok {
		return &policy.Errors{List: []policy.FieldError{{Path: s.Key, Msg: "unknown section"}}}
	}
	enabled := false
	switch s.Key {
	case "library":
		enabled = p.Library != nil && p.Library.Enabled
	case "notify":
		enabled = p.Notify != nil && p.Notify.Enabled
	case "router":
		enabled = p.Router != nil && p.Router.Enabled
	}
	next := reflect.New(f.Type()).Elem()
	if !nullSection(s.Config) {
		if err := decodeSection(s.Key, s.Config, next); err != nil {
			return err
		}
	}
	f.Set(next)
	switch {
	case s.Key == "library" && p.Library != nil:
		p.Library.Enabled = enabled
	case s.Key == "notify" && p.Notify != nil:
		p.Notify.Enabled = enabled
	case s.Key == "router" && p.Router != nil:
		p.Router.Enabled = enabled
	}
	ref := "sections/" + s.Key
	fields := envFields(f, "")
	for _, path := range s.SecretsSet {
		if set, ok := fields[path]; ok {
			set(settingsSecretEnv(ref, path))
		}
	}
	return nil
}

// envFields maps each *_env field under v, by its path without "_env" ("plex.token", "targets.discord.auth_header"),
// to a setter.
func envFields(v reflect.Value, path string) map[string]func(string) {
	out := map[string]func(string){}
	var walk func(v reflect.Value, path string)
	walk = func(v reflect.Value, path string) {
		switch v.Kind() {
		case reflect.Pointer:
			if !v.IsNil() {
				walk(v.Elem(), path)
			}
		case reflect.Map:
			iter := v.MapRange()
			for iter.Next() {
				if iter.Value().Kind() == reflect.Pointer {
					walk(iter.Value(), joinPath(path, fmt.Sprint(iter.Key().Interface())))
				}
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				name := yamlName(v.Type().Field(i))
				field := v.Field(i)
				if name == "" {
					continue
				}
				if field.Kind() == reflect.String && strings.HasSuffix(name, "_env") {
					if field.CanSet() {
						out[joinPath(path, strings.TrimSuffix(name, "_env"))] = field.SetString
					}
					continue
				}
				walk(field, joinPath(path, name))
			}
		}
	}
	walk(v, path)
	return out
}

func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// SectionSecretPaths lists the credential paths a section's config has, sorted.
func SectionSecretPaths(key string, config json.RawMessage) ([]string, error) {
	p := &policy.Policy{}
	if err := applySection(p, Section{Record: Record{Key: key}, Config: config}); err != nil {
		return nil, err
	}
	f, _ := policyField(p, key)
	return sortedPolicyKeys(envFields(f, "")), nil
}

// sectionSecretValues decodes a section's stored credential: a JSON object of path to value.
func sectionSecretValues(secret Secret) map[string]string {
	values := map[string]string{}
	if secret.value != "" {
		_ = json.Unmarshal([]byte(secret.value), &values)
	}
	return values
}

func sectionSecret(values map[string]string) Secret {
	for path, value := range values {
		if value == "" {
			delete(values, path)
		}
	}
	if len(values) == 0 {
		return NewSecret("")
	}
	b, _ := json.Marshal(values)
	return NewSecret(string(b))
}

// secretPaths is the paths with a value in a section's stored credential.
func secretPaths(secret Secret) []string {
	return sortedPolicyKeys(sectionSecretValues(secret))
}

// mergeSections keeps every stored section a request leaves out: a section, once stored, is never dropped back to
// the policy files.
func mergeSections(stored, req []Section) []Section {
	if req == nil {
		return slices.Clone(stored)
	}
	out := slices.Clone(req)
	for _, s := range stored {
		if !slices.ContainsFunc(req, func(r Section) bool { return r.Key == s.Key }) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// withDefaultSections adds every settings section that sections lacks as base has it: serve's base is the policy
// directory alone, so that is the section's default (what an empty policy has). SaveRequest.seedsSections says which
// writes use it: the write that creates generation 1 (a first save of any kind, or the first import, which fills them
// from its files) seeds those it does not set, and serve's next write over an older head that lacks some completes it.
func withDefaultSections(sections []Section, base *policy.Policy) ([]Section, error) {
	if base == nil {
		base = &policy.Policy{}
	}
	var missing []Section
	for _, spec := range Sections {
		if slices.ContainsFunc(sections, func(s Section) bool { return s.Key == spec.Key }) {
			continue
		}
		config, err := sectionJSON(base, spec.Key)
		if err != nil {
			return nil, err
		}
		missing = append(missing, Section{Record: Record{Key: spec.Key}, Config: config})
	}
	return mergeSections(missing, sections), nil
}

// DefaultSection is section key's default config: what a head without that section runs with, and what the next
// write stores for it unless it sets the section.
func DefaultSection(key string) (json.RawMessage, error) { return sectionJSON(&policy.Policy{}, key) }

// sectionSecretWrites turns SectionSecretWrites into whole-section credential writes over the current values, and
// drops stored values whose field the new config no longer has.
func sectionSecretWrites(req SaveRequest, sections []Section, current map[string]Secret) (map[string]Secret, error) {
	writes := map[string]Secret{}
	for ref, secret := range req.SecretWrites {
		writes[ref] = secret
	}
	for key := range req.SectionSecretWrites {
		if !slices.ContainsFunc(sections, func(s Section) bool { return s.Key == key }) {
			return nil, errors.New("settings: secret write has no owning record")
		}
	}
	for _, s := range sections {
		ref := "sections/" + s.Key
		patch := req.SectionSecretWrites[s.Key]
		values := sectionSecretValues(current[ref])
		if len(patch) == 0 && len(values) == 0 {
			continue
		}
		paths, err := SectionSecretPaths(s.Key, s.Config)
		if err != nil {
			return nil, err
		}
		changed := false
		for path, secret := range patch {
			if !slices.Contains(paths, path) {
				return nil, &policy.Errors{List: []policy.FieldError{{Path: s.Key + "." + path, Msg: "no credential field here"}}}
			}
			if values[path] != secret.value {
				values[path], changed = secret.value, true
			}
		}
		for path := range values {
			if !slices.Contains(paths, path) {
				delete(values, path)
				changed = true
			}
		}
		if changed {
			writes[ref] = sectionSecret(values)
		}
	}
	return writes, nil
}

// secretEnvValues are the stored credentials the runtime policy names by settingsSecretEnv.
func (s *Snapshot) secretEnvValues() map[string]string {
	out := map[string]string{}
	for ref, secret := range s.secrets {
		switch {
		case strings.HasPrefix(ref, "views/") && secret.value != "":
			out[settingsSecretEnv(ref, "api_key")] = secret.value
		case strings.HasPrefix(ref, "sections/"):
			for path, value := range sectionSecretValues(secret) {
				out[settingsSecretEnv(ref, path)] = value
			}
		}
	}
	return out
}

// BootstrapSections copies each database-owned section and every view's identity from YAML once, in one audited
// generation, including on an existing database. Later starts warn about each YAML section that differs.
func (s *Store) BootstrapSections(ctx context.Context, base *policy.Policy, actor string, log *slog.Logger) (*Snapshot, error) {
	current, err := s.Load(ctx, base)
	if err != nil {
		return nil, err
	}
	seeded := map[string]bool{}
	rows, err := s.db.Reader().QueryContext(ctx, "SELECT section FROM settings_sections_bootstrap")
	if err != nil {
		return nil, dbError(err)
	}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, dbError(err)
		}
		seeded[key] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	records := current.Records
	var marks []string
	for _, spec := range Sections {
		yamlConfig, err := sectionJSON(base, spec.Key)
		if err != nil {
			return nil, err
		}
		if seeded[spec.Key] {
			for _, stored := range records.Sections {
				if stored.Key == spec.Key && !nullSection(yamlConfig) && !sameJSON(stored.Config, yamlConfig) && log != nil {
					log.Warn("YAML setting ignored; database owns section", "key", spec.Key)
				}
			}
			continue
		}
		records.Sections = append(slices.DeleteFunc(slices.Clone(records.Sections), func(r Section) bool { return r.Key == spec.Key }),
			Section{Record: Record{Key: spec.Key}, Config: yamlConfig})
		marks = append(marks, spec.Key)
	}
	writes := map[string]Secret{}
	if !seeded["views"] {
		views := slices.Clone(records.Views)
		for i := range views {
			yamlView := base.Views[views[i].Key]
			if views[i].Identity != nil || yamlView == nil {
				continue
			}
			views[i].Identity = viewIdentity(yamlView)
			// The key moves into the database as the indexers' and providers' keys did at their import.
			if !views[i].SecretSet {
				value, ok := "", false
				if s.lookup != nil && yamlView.APIKeyEnv != "" {
					value, ok = s.lookup(yamlView.APIKeyEnv)
				}
				if ok && value != "" {
					writes["views/"+views[i].Key] = NewSecret(value)
				} else if log != nil {
					log.Warn("view API key not set; the view accepts no key until one is set in Admin › Settings", "view", views[i].Key, "env", yamlView.APIKeyEnv)
				}
			}
		}
		records.Views = views
		marks = append(marks, "views")
	} else if log != nil {
		for _, view := range records.Views {
			if yaml := base.Views[view.Key]; yaml != nil && view.Identity != nil && !reflect.DeepEqual(viewIdentity(yaml), view.Identity) {
				log.Warn("YAML setting ignored; database owns view", "key", "views."+view.Key)
			}
		}
	}
	if len(marks) == 0 {
		return current, nil
	}
	next, err := s.saveWithHook(ctx, SaveRequest{ExpectedGeneration: current.Generation, Actor: actor, Base: base,
		Records: records, SecretWrites: writes, bootstrap: true, bootstrapSections: strings.Join(marks, ",")}, "bootstrap-import", func(ctx context.Context, tx *sql.Tx) error {
		for _, key := range marks {
			if _, err := tx.ExecContext(ctx, "INSERT INTO settings_sections_bootstrap (section, generation) VALUES (?, ?)", key, current.Generation+1); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, ErrConflict) {
		return s.BootstrapSections(ctx, base, actor, log)
	}
	return next, err
}

func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// SectionConfig is p's section key as the settings database stores it: YAML-shaped JSON without the switches and
// library roots other records own.
func SectionConfig(p *policy.Policy, key string) (json.RawMessage, error) { return sectionJSON(p, key) }

// removedSections are stored sections the policy no longer has: release_push went when privacy became a label only.
// A stored row of one is ignored when the records are read.
var removedSections = map[string]bool{"release_push": true}
