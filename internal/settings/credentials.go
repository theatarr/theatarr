package settings

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
)

// CredentialSentinel is the env name a provider record without one gets in the assembled policy: policy validation
// wants an env-shaped reference, and the key itself is a settings credential. It is never resolved through the
// environment.
const CredentialSentinel = "THEATARR_SETTINGS_CREDENTIAL"

// Credentials answers one question for every credential of a generation, for golive, migrate verify and the startup
// log: does it resolve? It never exposes a value. snap is the generation's settings snapshot, nil under YAML
// settings; lookup is the process environment (os.LookupEnv).
type Credentials struct {
	Policy *policy.Policy
	Snap   *Snapshot
	Lookup LookupSecret
}

// Resolves reports whether a credential resolves as the running service would resolve it. A provider or indexer
// record (entity "providers" or "indexers" and its key) resolves through its stored key or its env name
// (Snapshot.WithSecret); any other credential by its *_env name through the policy, which answers the settings
// database's derived names (policy.SettingsSecretPrefix) from the generation and the rest from Lookup.
func (c Credentials) Resolves(entity, key, env string) bool {
	if c.Snap != nil && entity != "" {
		if _, known := c.Snap.secrets[entity+"/"+key]; known {
			return c.Snap.CredentialSet(entity, key, c.Lookup)
		}
	}
	if env == "" || env == CredentialSentinel {
		return false
	}
	if strings.HasPrefix(env, policy.SettingsSecretPrefix) {
		if c.Policy == nil {
			return false
		}
		v, _ := c.Policy.LookupEnv(env)
		return v != ""
	}
	if c.Lookup == nil {
		return false
	}
	v, _ := c.Lookup(env)
	return v != ""
}

var recordUse = regexp.MustCompile(`^(providers|indexers)\[([^\]]+)\]\.api_key_env$`)

// Unset lists the policy's credentials (policy.SecretEnvs) that resolve nowhere, each named by what an admin can
// act on: the env name for a credential read from the environment, the settings path (without _env) for one the
// settings database holds.
func (c Credentials) Unset() []string {
	if c.Policy == nil {
		return nil
	}
	seen := map[string]bool{}
	for env, uses := range c.Policy.SecretEnvs() {
		for _, use := range uses {
			entity, key := "", ""
			if m := recordUse.FindStringSubmatch(use); m != nil {
				entity, key = m[1], m[2]
			}
			if c.Resolves(entity, key, env) {
				continue
			}
			name := env
			if env == CredentialSentinel || strings.HasPrefix(env, policy.SettingsSecretPrefix) {
				name = strings.TrimSuffix(use, "_env") + " (settings)"
			}
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// refuseDerivedNames refuses a changed record whose config names a settings credential by its derived name
// (policy.SettingsSecretPrefix) in a *_env field. A record's own stored credential is attached at assembly and never
// appears in its config, so such a name could only point at another record's secret: a webhook header sending the
// Plex token, say. Unchanged rows (same config as in unchanged) pass, so an old record cannot block other saves.
func refuseDerivedNames(rows []row, unchanged map[string]row) error {
	for _, r := range rows {
		if prev, ok := unchanged[r.ref()]; ok && string(prev.Config) == string(r.Config) {
			continue
		}
		var config any
		if len(r.Config) == 0 || json.Unmarshal(r.Config, &config) != nil {
			continue
		}
		if path := derivedNamePath(config, ""); path != "" {
			return &policy.Errors{List: []policy.FieldError{{Path: r.Entity + "[" + r.Key + "]." + path,
				Msg: "names another record's stored credential; store this credential here instead, or name an environment variable"}}}
		}
	}
	return nil
}

func derivedNamePath(v any, path string) string {
	join := func(k string) string {
		if path == "" {
			return k
		}
		return path + "." + k
	}
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s, ok := x[k].(string); ok && strings.HasSuffix(k, "_env") && strings.HasPrefix(s, policy.SettingsSecretPrefix) {
				return join(k)
			}
			if p := derivedNamePath(x[k], join(k)); p != "" {
				return p
			}
		}
	case []any:
		for i, e := range x {
			if p := derivedNamePath(e, join(strconv.Itoa(i))); p != "" {
				return p
			}
		}
	}
	return ""
}
