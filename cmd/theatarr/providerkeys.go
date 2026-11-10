package main

import (
	"context"
	"os"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/uipages"
)

// providerKeys returns each policy provider's API key as theatarr serve resolves it: the key stored in the settings
// database (entered in the web UI), else the environment variable the record names. Without a snapshot, or for a
// provider the snapshot does not know, the named environment variable. Never print the values.
func providerKeys(pol *policy.Policy, snap *settings.Snapshot) map[string]string {
	keys := make(map[string]string, len(pol.Providers))
	for k, pv := range pol.Providers {
		value, known := "", false
		if snap != nil {
			known = snap.WithSecret("providers", k, os.LookupEnv, func(v string) error { value = v; return nil }) == nil
		}
		if !known && pv.APIKeyEnv != settings.CredentialSentinel {
			value = os.Getenv(pv.APIKeyEnv)
		}
		keys[k] = value
	}
	return keys
}

// indexerKeys returns the indexer keys the settings database holds (or a record's env fallback), as serve passes
// them to the search service (indexer.Options.APIKeys); an indexer left out reads its api_key_env as before.
func indexerKeys(pol *policy.Policy, snap *settings.Snapshot) map[string]string {
	keys := map[string]string{}
	if snap == nil {
		return keys
	}
	for k := range pol.Indexers {
		_ = snap.WithSecret("indexers", k, os.LookupEnv, func(v string) error {
			if v != "" {
				keys[k] = v
			}
			return nil
		})
	}
	return keys
}

// keyedProviders lists, sorted, the providers whose key is set (providerKeys).
func keyedProviders(pol *policy.Policy, snap *settings.Snapshot) []string {
	var out []string
	for k, v := range providerKeys(pol, snap) {
		if v != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// providerKeyUnset says what is missing for a provider without a key: its env variable, or a key in Settings.
func providerKeyUnset(pv *policy.Provider) string {
	if pv.APIKeyEnv == "" || pv.APIKeyEnv == settings.CredentialSentinel {
		return "no API key is stored (" + uipages.Providers + ")"
	}
	return pv.APIKeyEnv + " is not set"
}

// settingsHeldSecret reports whether a backup manifest's secret name stands for a credential the settings database
// holds (and so the backup's database file), not an environment variable: the provider sentinel and the derived
// THEATARR_SETTINGS_SECRET_* names.
func settingsHeldSecret(env string) bool {
	return env == settings.CredentialSentinel || strings.HasPrefix(env, policy.SettingsSecretPrefix)
}

// storedCredentials reports, by policy path ("providers[rd].api_key_env", "indexers[x].api_key_env"), whether the
// settings database in st holds that record's credential, so a backup of st has it. nil when st has no settings.
func storedCredentials(ctx context.Context, st *store.Store, pol *policy.Policy) func(string) bool {
	s, err := settings.New(st)
	if err != nil {
		return nil
	}
	snap, err := s.Load(ctx, &policy.Policy{Dir: pol.Dir})
	if err != nil {
		return nil
	}
	return func(user string) bool {
		for _, entity := range []string{"providers", "indexers"} {
			if key, ok := strings.CutPrefix(user, entity+"["); ok {
				key, ok = strings.CutSuffix(key, "].api_key_env")
				// A stored value only (no env lookup): the record's own key in the database.
				return ok && snap.CredentialSet(entity, key, nil)
			}
		}
		return false
	}
}
