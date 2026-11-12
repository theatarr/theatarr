package settings

import (
	"fmt"
	"slices"

	"github.com/theatarr/theatarr/internal/policy"
)

// Importing titles and monitoring into a grabbing version (active with grab on) puts them in wanted searches. An
// unchanged mapping does not. It counts whatever router.enabled is:
// turning the router on later names no import source. Check the effective policies so startup fallback uses the same
// rule.
func importAcquisitionEffects(previous, next *policy.Policy) []ConfirmationEffect {
	if next.Import == nil {
		return nil
	}
	routerEnabled := next.Router != nil && next.Router.Enabled
	var effects []ConfirmationEffect
	for _, key := range sortedPolicyKeys(next.Import.Arr) {
		now := next.Import.Arr[key]
		if now == nil || !slices.Contains(next.GrabbingVersions(), now.Version) {
			continue
		}
		var old *policy.ArrSource
		if previous.Import != nil {
			old = previous.Import.Arr[key]
		}
		if old != nil && old.Version == now.Version {
			continue
		}
		effects = append(effects, ConfirmationEffect{Effect: fmt.Sprintf("Import titles into Live library %s, with its downloads on, from import.arr.%s: monitored missing titles can be searched, and downloaded when Allow new downloads (router.enabled) is on (currently %t).", now.Version, key, routerEnabled)})
	}
	return effects
}
