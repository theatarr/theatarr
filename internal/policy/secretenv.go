package policy

import (
	"maps"
	"os"
	"strings"
)

// SettingsSecretPrefix starts the names the settings database gives its credentials for policy fields that take an
// environment name (views[*].api_key_env, library.plex.token_env, ...). Such a name is never read from the process
// environment: Getenv answers it from the values of the policy's own settings generation (WithSettingsSecrets).
const SettingsSecretPrefix = "THEATARR_SETTINGS_SECRET_"

// WithSettingsSecrets sets the settings database's credentials of the generation this policy belongs to, by
// SettingsSecretPrefix name. Call it only while building the policy, before it is shared: each generation keeps
// its own values, so preparing a candidate never changes what the generation in force resolves.
func (p *Policy) WithSettingsSecrets(values map[string]string) {
	p.secrets = nil
	if len(values) > 0 {
		p.secrets = maps.Clone(values)
	}
}

// Getenv resolves a policy *_env field: a settings credential name from this policy's generation, anything else from
// the process environment.
func (p *Policy) Getenv(name string) string {
	v, _ := p.LookupEnv(name)
	return v
}

// LookupEnv is Getenv reporting whether the name is set.
func (p *Policy) LookupEnv(name string) (string, bool) {
	if strings.HasPrefix(name, SettingsSecretPrefix) {
		if p == nil {
			return "", false
		}
		v, ok := p.secrets[name]
		return v, ok
	}
	return os.LookupEnv(name)
}
