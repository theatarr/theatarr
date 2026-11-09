package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/secrets"
)

// Notification target URLs often carry their credential in the path or query (Discord /api/webhooks/<id>/<token>,
// Slack /services/T/B/<token>, ?token=). Like the other section credentials they are write-only: reads, the MCP
// tools and the settings audit show only the scheme and host, and a save that sends that mask back unchanged keeps
// the stored URL. Every other URL in a section shows with its credential-like parts masked (user information, a
// credential query parameter such as X-Plex-Token or apiKey, a token-like path segment), restored the
// same way. MaskSectionConfig is the one masker for section reads, the MCP tools and the audit.

var (
	notifyURLs  = secrets.URLMasker{Whole: true}
	sectionURLs = secrets.URLMasker{}
)

// sectionMasker is the masker for the string leaf at path of section key: a notify target's url keeps only scheme
// and host, any other value masks the credential-like parts of an absolute URL and is otherwise left alone.
func sectionMasker(key string, path []string) secrets.URLMasker {
	if key == "notify" && len(path) == 3 && path[0] == "targets" && path[2] == "url" {
		return notifyURLs
	}
	return sectionURLs
}

// MaskSectionConfig returns a section's config as reads show it: notify target URLs keep only scheme and host, other
// URLs lose their credentials.
func MaskSectionConfig(key string, config json.RawMessage) json.RawMessage {
	value, ok := decodeSectionValue(config)
	if !ok {
		return config
	}
	changed := false
	value = mapSectionStrings(value, nil, func(path []string, v string) string {
		if mask := sectionMasker(key, path).Mask(v); mask != v {
			changed = true
			return mask
		}
		return v
	})
	if !changed {
		return config
	}
	b, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

// UnmaskSectionConfig puts stored URLs back where the submitted config sends the mask of the stored URL at the same
// place. A URL still masked after that (another target's mask, a renamed target, another host) is refused: the mask
// keeps the host, so a stored credential never moves to another place or host.
func UnmaskSectionConfig(key string, submitted, stored json.RawMessage) (json.RawMessage, error) {
	value, ok := decodeSectionValue(submitted)
	if !ok {
		return submitted, nil
	}
	old, _ := decodeSectionValue(stored)
	changed := false
	masked := ""
	value = mapSectionStrings(value, nil, func(path []string, v string) string {
		m := sectionMasker(key, path)
		if !m.Masked(v) || (!m.Whole && !strings.Contains(v, "://")) {
			return v
		}
		if was, ok := sectionLeaf(old, path); ok {
			if restored := m.Unmask(v, was); restored != v && !m.Masked(restored) {
				changed = true
				return restored
			}
		}
		if masked == "" {
			masked = strings.Join(path, ".")
		}
		return v
	})
	if masked != "" {
		return nil, fmt.Errorf("settings: %s.%s is masked; send the full URL, or the stored URL's mask unchanged", key, masked)
	}
	if !changed {
		return submitted, nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("settings: invalid section %s", key)
	}
	return b, nil
}

// decodeSectionValue decodes a section config with numbers kept as written; false for an empty or invalid one.
func decodeSectionValue(config json.RawMessage) (any, bool) {
	if len(config) == 0 {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(config))
	dec.UseNumber()
	var value any
	if dec.Decode(&value) != nil || value == nil {
		return nil, false
	}
	return value, true
}

// mapSectionStrings replaces every string leaf of value by f(path, leaf); list items have their index as path element.
func mapSectionStrings(value any, path []string, f func([]string, string) string) any {
	switch v := value.(type) {
	case map[string]any:
		for k, child := range v {
			v[k] = mapSectionStrings(child, append(slices.Clip(path), k), f)
		}
	case []any:
		for i, child := range v {
			v[i] = mapSectionStrings(child, append(slices.Clip(path), strconv.Itoa(i)), f)
		}
	case string:
		return f(path, v)
	}
	return value
}

// sectionLeaf is the string at path of a decoded section, if there is one.
func sectionLeaf(value any, path []string) (string, bool) {
	for _, p := range path {
		switch v := value.(type) {
		case map[string]any:
			value = v[p]
		case []any:
			i, err := strconv.Atoi(p)
			if err != nil || i < 0 || i >= len(v) {
				return "", false
			}
			value = v[i]
		default:
			return "", false
		}
	}
	s, ok := value.(string)
	return s, ok
}

// auditValue is what the settings audit stores for a record: a section's config as reads show it.
func auditValue(v any) any {
	switch r := v.(type) {
	case row:
		if r.Entity == "sections" {
			r.Config = MaskSectionConfig(r.Key, r.Config)
		}
		if r.Entity == "indexers" && r.Kind == "cardigann" {
			r.Config = maskCardigannConfig(r.Config)
		}
		return r
	case *row:
		if r != nil {
			return auditValue(*r)
		}
	}
	return v
}

// Audit history keeps setting names but never Cardigann values: definitions can change or be unavailable later.
func maskCardigannConfig(raw json.RawMessage) json.RawMessage {
	var config map[string]json.RawMessage
	if json.Unmarshal(raw, &config) != nil {
		return json.RawMessage(`null`)
	}
	var settings map[string]string
	if json.Unmarshal(config["Settings"], &settings) != nil {
		// Corrupt historical settings must not disclose values through audit either.
		config["Settings"] = json.RawMessage(`"[redacted]"`)
		out, _ := json.Marshal(config)
		return out
	}
	for k := range settings {
		settings[k] = "[redacted]"
	}
	config["Settings"], _ = json.Marshal(settings)
	out, _ := json.Marshal(config)
	return out
}

func maskCardigannAudit(raw json.RawMessage) json.RawMessage {
	var r row
	if json.Unmarshal(raw, &r) != nil || r.Entity != "indexers" || r.Kind != "cardigann" {
		return raw
	}
	r.Config = maskCardigannConfig(r.Config)
	out, _ := json.Marshal(r)
	return out
}
