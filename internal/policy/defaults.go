package policy

// ApplyDefaults fills in what an empty field means (a version's mode, enable, import, add_via, packs, upgrades,
// provider preference and Sonarr version; egress timing and classes) without checking anything. Validate starts with
// it; a settings import uses it alone on files that may be valid only once merged with the stored settings.
func (p *Policy) ApplyDefaults() {
	for _, v := range p.Versions {
		if v != nil {
			v.applyDefaults()
		}
	}
	if g := p.Egress; g != nil {
		if g.Interval == 0 {
			g.Interval = Duration(DefaultEgressInterval)
		}
		if g.MaxAge == 0 {
			g.MaxAge = 3 * g.Interval
		}
		if g.Timeout == 0 {
			g.Timeout = Duration(DefaultEgressTimeout)
		}
		if g.RequireVPNFor == nil {
			g.RequireVPNFor = []string{EgressIndexers}
		}
	}
}

func (v *VersionPolicy) applyDefaults() {
	if v.Mode == "" {
		v.Mode = ModeShadow
	}
	if v.Enable == "" {
		v.Enable = "default"
	}
	if v.Import == "" {
		v.Import = "symlink"
	}
	if v.AddVia == "" {
		v.AddVia = AddViaProviders
	}
	if v.Packs == "" {
		v.Packs = PacksAll
	}
	// approve was removed (2026-11): a stored or imported approve is automatic.
	if v.Upgrades == legacyUpgradesApprove || v.Upgrades == "" {
		v.Upgrades = UpgradesAuto
	}
	if v.ProviderPreference == "" {
		v.ProviderPreference = ProviderPreferenceOrdered
	}
	if v.Sonarr == "" && v.Kind == KindSeries {
		v.Sonarr = SonarrV4
	}
}
