package settings

import (
	"encoding/json"
	"errors"
	"sort"

	"github.com/theatarr/theatarr/internal/policy"
	"go.yaml.in/yaml/v3"
)

type versionRowConfig struct {
	Config json.RawMessage `json:"config"`
	Roots  []string        `json:"roots"`
}

type viewBindingConfig struct {
	Versions []string            `json:"versions"`
	Profiles map[string][]string `json:"profiles"`
	Identity *ViewIdentity       `json:"identity,omitempty"`
}

func versionConfigJSON(config policy.VersionPolicy, roots []string) ([]byte, error) {
	config.Naming = policy.Naming{}
	y, err := yaml.Marshal(config)
	if err != nil {
		return nil, err
	}
	var value any
	if err := yaml.Unmarshal(y, &value); err != nil {
		return nil, err
	}
	if fields, ok := value.(map[string]any); ok {
		delete(fields, "naming")
	}
	configJSON, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(versionRowConfig{Config: configJSON, Roots: roots})
}

func versionConfigFromJSON(raw []byte) (policy.VersionPolicy, []string, error) {
	var stored versionRowConfig
	if err := json.Unmarshal(raw, &stored); err != nil {
		return policy.VersionPolicy{}, nil, err
	}
	var config policy.VersionPolicy
	if err := yaml.Unmarshal(stored.Config, &config); err != nil {
		return policy.VersionPolicy{}, nil, err
	}
	return config, stored.Roots, nil
}

// yamlVersions imports only the version records' policy data; view credentials and identity remain in the supplied
// base.
// requireGroups refuses files without versions or views (an import over stored ones).
func yamlVersions(dir string, firstRun, requireGroups bool) (Records, error) {
	read := policy.Read
	if firstRun {
		read = policy.ReadOrEmpty
	}
	p, err := read(dir)
	if err != nil {
		return Records{}, errors.New("settings: cannot parse policy import")
	}
	if requireGroups && (p.Versions == nil || p.Views == nil) {
		return Records{}, errors.New("settings: import requires versions and views sections")
	}
	p.ApplyDefaults()
	return versionRecords(p), nil
}

// versionRecords is the version records of p's versions, roots and view bindings.
func versionRecords(p *policy.Policy) Records {
	records := Records{Versions: []Version{}, Views: []ViewBinding{}}
	for _, key := range sortedPolicyKeys(p.Versions) {
		config := *p.Versions[key]
		var roots []string
		if p.Library != nil && p.Library.Versions[key] != nil {
			roots = append([]string(nil), p.Library.Versions[key].Roots...)
		}
		records.Versions = append(records.Versions, Version{Record: Record{Key: key}, Config: config, Roots: roots})
	}
	for _, key := range sortedPolicyKeys(p.Views) {
		v := p.Views[key]
		records.Views = append(records.Views, ViewBinding{Record: Record{Key: key},
			Versions: append([]string(nil), v.Versions...), Profiles: cloneVersionSets(v.Profiles)})
	}
	return records
}

func sortedPolicyKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneVersionSets(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for key, versions := range in {
		out[key] = append([]string(nil), versions...)
	}
	return out
}
