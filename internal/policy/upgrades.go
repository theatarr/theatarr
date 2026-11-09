package policy

// Upgrade rules (versions[*].upgrades): what the router does with a release that would replace a file the version
// already has (docs/design/router.md "Upgrade approval").
const (
	// UpgradesAuto routes upgrades like any other release (the default, as the arrs do with upgradeAllowed on).
	UpgradesAuto = "auto"
	// UpgradesApprove holds an upgrade until a manager approves it; nothing is added before then.
	UpgradesApprove = "approve"
	// UpgradesOff never replaces a file (the arrs' upgradeAllowed off, for the version rather than the profile).
	UpgradesOff = "off"
)

// validateUpgrades checks a version's upgrade rule; "" is auto.
func validateUpgrades(e *Errors, path string, v *VersionPolicy) {
	switch v.Upgrades {
	case UpgradesAuto, UpgradesApprove, UpgradesOff:
	default:
		e.add(path+".upgrades", "must be %s, %s or %s, got %q", UpgradesAuto, UpgradesApprove, UpgradesOff, v.Upgrades)
	}
}

// UpgradeRule is the version's upgrade rule, auto when unset (a policy built in code skips validation).
func (v *VersionPolicy) UpgradeRule() string {
	if v == nil || v.Upgrades == "" {
		return UpgradesAuto
	}
	return v.Upgrades
}
