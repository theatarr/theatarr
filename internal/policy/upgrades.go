package policy

// Upgrade rules (versions[*].upgrades): what the router does with a release that would replace a file the version
// already has (docs/design/router.md "Upgrades").
const (
	// UpgradesAuto routes upgrades like any other release (the default, as the arrs do with upgradeAllowed on).
	UpgradesAuto = "auto"
	// UpgradesOff never replaces a file (the arrs' upgradeAllowed off, for the version rather than the profile).
	UpgradesOff = "off"
)

// legacyUpgradesApprove is the removed approve rule (held an upgrade for a manager's approval). A stored or imported
// approve reads as auto (applyDefaults).
const legacyUpgradesApprove = "approve"

// validateUpgrades checks a version's upgrade rule; "" is auto.
func validateUpgrades(e *Errors, path string, v *VersionPolicy) {
	switch v.Upgrades {
	case UpgradesAuto, UpgradesOff:
	default:
		e.add(path+".upgrades", "must be %s or %s, got %q", UpgradesAuto, UpgradesOff, v.Upgrades)
	}
}

// UpgradeRule is the version's upgrade rule, auto when unset (a policy built in code skips validation).
func (v *VersionPolicy) UpgradeRule() string {
	if v == nil || v.Upgrades == "" {
		return UpgradesAuto
	}
	return v.Upgrades
}
