package main

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/importarr"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/shadow"
	"github.com/theatarr/theatarr/internal/store"
)

// arrGetters gives the import's arr clients, with the keys from the environment.
func arrGetters(pol *policy.Policy) shadow.ArrSourceFunc {
	return func(instance string) (importarr.Getter, error) {
		var a *policy.ArrSource
		if pol.Import != nil {
			a = pol.Import.Arr[instance]
		}
		if a == nil {
			return nil, fmt.Errorf("import.arr has no instance %q", instance)
		}
		key := pol.Getenv(a.APIKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("%s is not set", a.APIKeyEnv)
		}
		return arr.New(a.URL, key, arr.Options{Timeout: 120 * time.Second})
	}
}

// newShadowRunner builds the periodic arr import theatarr serve runs (job arr.import). Without an import.arr section it
// imports nothing.
func newShadowRunner(pol *policy.Policy, st *store.Store, q *jobs.Queue, log *slog.Logger,
	runtime shadow.RuntimeSource) *shadow.Runner {
	cfg := shadow.Config{Store: st, Policy: pol, Queue: q, Logger: log, Runtime: runtime}
	if pol.Import != nil && len(pol.Import.Arr) > 0 {
		cfg.Arr = arrGetters(pol)
	}
	return shadow.New(cfg)
}
