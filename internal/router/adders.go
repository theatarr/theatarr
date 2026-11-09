package router

import (
	"log/slog"
	"sort"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
)

// BuildAdders builds the write clients for Config.Adders: one per policy provider whose API key getenv returns
// (constructing one sends nothing). It returns the providers left out, with their env names. It lives here so that
// only internal/router imports internal/provider/adder (docs/dev.md; TestOnlyRouterImportsAdder).
func BuildAdders(pol *policy.Policy, d provider.Doer, getenv func(string) string,
	log *slog.Logger) (map[string]adder.Adder, []string) {
	keys := make(map[string]string, len(pol.Providers))
	for key, pv := range pol.Providers {
		keys[key] = getenv(pv.APIKeyEnv)
	}
	return BuildAddersWithKeys(pol, d, keys, log)
}

// BuildAddersWithKeys builds write clients from credentials already resolved for this runtime generation.
func BuildAddersWithKeys(pol *policy.Policy, d provider.Doer, keys map[string]string,
	log *slog.Logger) (map[string]adder.Adder, []string) {
	out := map[string]adder.Adder{}
	var skipped []string
	for k, pv := range pol.Providers {
		key := keys[k]
		if key == "" {
			skipped = append(skipped, k+" ("+pv.APIKeyEnv+")")
			continue
		}
		a, err := adder.New(k, pv, key, d)
		if err != nil {
			log.Warn("router: provider left out", "provider", k, "err", err)
			continue
		}
		out[k] = a
	}
	sort.Strings(skipped)
	return out, skipped
}
