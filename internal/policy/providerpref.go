package policy

// Provider preferences (versions[*].provider_preference): how the ranking's cache tie-break reads the version's
// provider list (docs/design/router.md "Deviations", "Cache tie-break"). The route tries the providers in list order
// either way.
const (
	// ProviderPreferenceOrdered prefers, among equally ranked releases, the one cached on an earlier provider of the
	// list, then the one cached on more of them (the default).
	ProviderPreferenceOrdered = "ordered"
	// ProviderPreferenceNone counts only how many of the version's providers hold the release cached.
	ProviderPreferenceNone = "none"
)

// validateProviderPreference checks a version's provider preference; "" is ordered.
func validateProviderPreference(e *Errors, path string, v *VersionPolicy) {
	switch v.ProviderPreference {
	case ProviderPreferenceOrdered, ProviderPreferenceNone:
	default:
		e.add(path+".provider_preference", "must be %s or %s, got %q", ProviderPreferenceOrdered,
			ProviderPreferenceNone, v.ProviderPreference)
	}
}

// ProviderPreferenceRule is the version's provider preference, ordered when unset (a policy built in code skips
// validation).
func (v *VersionPolicy) ProviderPreferenceRule() string {
	if v == nil || v.ProviderPreference == "" {
		return ProviderPreferenceOrdered
	}
	return v.ProviderPreference
}
