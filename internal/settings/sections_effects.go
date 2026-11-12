package settings

import (
	"fmt"
	"slices"

	"github.com/theatarr/theatarr/internal/policy"
)

// Importing titles and monitoring into a Live version puts them in wanted searches. An
// unchanged mapping does not. It counts whatever Pause is:
// turning Pause off later names no import source. Check the effective policies so startup fallback uses the same
// rule.
func importAcquisitionEffects(previous, next *policy.Policy) []ConfirmationEffect {
	if next.Import == nil {
		return nil
	}
	paused := next.Router == nil || !next.Router.Enabled
	var effects []ConfirmationEffect
	for _, key := range sortedPolicyKeys(next.Import.Arr) {
		now := next.Import.Arr[key]
		if now == nil || !slices.Contains(next.ActiveVersions(), now.Version) {
			continue
		}
		var old *policy.ArrSource
		if previous.Import != nil {
			old = previous.Import.Arr[key]
		}
		if old != nil && old.Version == now.Version {
			continue
		}
		effects = append(effects, ConfirmationEffect{Effect: fmt.Sprintf("Import titles into Live library %s from import.arr.%s: monitored missing titles can be searched, and downloaded when Pause is off (currently paused: %t).", now.Version, key, paused)})
	}
	return effects
}
