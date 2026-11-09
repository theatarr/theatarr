package settings

import (
	"slices"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// Without a snapshot (YAML settings) a credential resolves by its env name; the provider sentinel never does, and a
// settings credential's derived name resolves from the generation's own values, not the environment. Unset names
// a stored credential by its settings path.
func TestCredentialsResolve(t *testing.T) {
	t.Parallel()
	derived := policy.SettingsSecretPrefix + "ABC"
	p := &policy.Policy{
		Providers: map[string]*policy.Provider{"rd": {APIKeyEnv: CredentialSentinel}},
		Views:     map[string]*policy.View{"radarr": {APIKeyEnv: derived}, "sonarr": {APIKeyEnv: policy.SettingsSecretPrefix + "DEF"}},
		Metadata:  &policy.Metadata{Sources: map[string]*policy.MetadataSource{"tmdb": {APIKeyEnv: "THEATARR_TMDB_KEY"}}},
	}
	p.WithSettingsSecrets(map[string]string{derived: "stored"})
	env := map[string]string{CredentialSentinel: "leaked", "THEATARR_TMDB_KEY": "k", policy.SettingsSecretPrefix + "DEF": "from-env"}
	c := Credentials{Policy: p, Lookup: func(n string) (string, bool) { v, ok := env[n]; return v, ok }}
	if c.Resolves("providers", "rd", CredentialSentinel) || !c.Resolves("", "", derived) || !c.Resolves("", "", "THEATARR_TMDB_KEY") ||
		c.Resolves("", "", policy.SettingsSecretPrefix+"DEF") {
		t.Fatal("resolves")
	}
	want := []string{"providers[rd].api_key (settings)", "views[sonarr].api_key (settings)"}
	if got := c.Unset(); !slices.Equal(got, want) {
		t.Fatalf("unset %v, want %v", got, want)
	}
}
