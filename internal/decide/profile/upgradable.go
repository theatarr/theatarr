package profile

// Port of Radarr src/NzbDrone.Core/DecisionEngine/{Specifications/UpgradableSpecification,
// UpgradeableRejectReason}.cs @ a96bf7c. Sonarr's @ cab419a is the same.

import "github.com/theatarr/theatarr/internal/decide/format"

// UpgradeableRejectReason says why a release does not upgrade a file.
type UpgradeableRejectReason int

const (
	UpgradeNone UpgradeableRejectReason = iota
	UpgradeBetterQuality
	UpgradeBetterRevision
	UpgradeQualityCutoff
	UpgradeCustomFormatScore
	UpgradeCustomFormatCutoff
	UpgradeMinCustomFormatScore
	UpgradesNotAllowed
)

var upgradeReasonNames = []string{"None", "BetterQuality", "BetterRevision", "QualityCutoff", "CustomFormatScore",
	"CustomFormatCutoff", "MinCustomFormatScore", "UpgradesNotAllowed"}

func (r UpgradeableRejectReason) String() string { return upgradeReasonNames[r] }

// IsUpgradable is UpgradableSpecification.IsUpgradable: is newQuality with newFormats an upgrade of the current
// file? Quality first (within the cutoff), then revision, then the format score with its cutoff and increment.
func (e *Engine) IsUpgradable(p *Profile, current QualityModel, currentFormats []*format.Format, newQ QualityModel, newFormats []*format.Format) UpgradeableRejectReason {
	if c, ok := SeaDexOrder(e.Config.PreferSeaDex, newQ.SeaDexPreference, current.SeaDexPreference); ok {
		if c < 0 {
			return UpgradeBetterQuality
		}
		if !p.UpgradeAllowed {
			return UpgradesNotAllowed
		}
		return UpgradeNone
	}
	qualityCompare := p.CompareQualities(newQ.Quality, current.Quality, false)
	mode := e.Config.DownloadPropersAndRepacks

	if qualityCompare > 0 && e.QualityCutoffNotMet(p, current, &newQ) {
		return UpgradeNone
	}
	if qualityCompare < 0 {
		return UpgradeBetterQuality
	}
	revisionCompare := newQ.Revision.CompareTo(current.Revision)
	if mode != DoNotPrefer && revisionCompare > 0 {
		return UpgradeNone
	}
	if !p.UpgradeAllowed {
		return UpgradesNotAllowed
	}
	if mode != DoNotPrefer && revisionCompare < 0 {
		return UpgradeBetterRevision
	}
	if qualityCompare > 0 {
		return UpgradeQualityCutoff
	}
	currentScore := p.FormatScore(currentFormats)
	newScore := p.FormatScore(newFormats)
	if newScore <= currentScore {
		return UpgradeCustomFormatScore
	}
	if currentScore >= p.CutoffFormatScore {
		return UpgradeCustomFormatCutoff
	}
	if newScore < currentScore+p.MinUpgradeFormatScore {
		return UpgradeMinCustomFormatScore
	}
	return UpgradeNone
}

// QualityCutoffNotMet is UpgradableSpecification.QualityCutoffNotMet. Without upgrades the cutoff is the first
// allowed quality. A revision upgrade of the same quality counts as not met.
func (e *Engine) QualityCutoffNotMet(p *Profile, current QualityModel, newQ *QualityModel) bool {
	cutoff := p.Cutoff
	if !p.UpgradeAllowed {
		cutoff = p.FirstAllowedQuality().ID
	}
	if p.CompareQualityIDs(current.Quality.ID, cutoff, false) < 0 {
		return true
	}
	return newQ != nil && isRevisionUpgrade(current, *newQ)
}

func (e *Engine) customFormatCutoffNotMet(p *Profile, currentFormats []*format.Format) bool {
	score := p.FormatScore(currentFormats)
	cutoff := p.CutoffFormatScore
	if !p.UpgradeAllowed {
		cutoff = p.MinFormatScore
	}
	return score < cutoff
}

// CutoffNotMet is UpgradableSpecification.CutoffNotMet: the quality cutoff or the format-score cutoff is not met.
func (e *Engine) CutoffNotMet(p *Profile, current QualityModel, currentFormats []*format.Format, newQ *QualityModel) bool {
	if newQ != nil && p.UpgradeAllowed {
		if c, _ := SeaDexOrder(e.Config.PreferSeaDex, newQ.SeaDexPreference, current.SeaDexPreference); c > 0 {
			return true
		}
	}
	return e.QualityCutoffNotMet(p, current, newQ) || e.customFormatCutoffNotMet(p, currentFormats)
}

// IsRevisionUpgrade is UpgradableSpecification.IsRevisionUpgrade: same quality (by id), higher revision.
func (e *Engine) IsRevisionUpgrade(current, newQ QualityModel) bool {
	return isRevisionUpgrade(current, newQ)
}

func isRevisionUpgrade(current, newQ QualityModel) bool {
	return current.Quality.ID == newQ.Quality.ID && newQ.Revision.CompareTo(current.Revision) > 0
}

// IsUpgradeAllowed is UpgradableSpecification.IsUpgradeAllowed: a revision upgrade always is; a quality or score
// upgrade only when the profile allows upgrades; anything else is not an upgrade and so allowed.
func (e *Engine) IsUpgradeAllowed(p *Profile, current QualityModel, currentFormats []*format.Format, newQ QualityModel, newFormats []*format.Format) bool {
	if c, _ := SeaDexOrder(e.Config.PreferSeaDex, newQ.SeaDexPreference, current.SeaDexPreference); c > 0 {
		return p.UpgradeAllowed
	}
	isQualityUpgrade := p.CompareModels(newQ, current, false) > 0
	isFormatUpgrade := p.FormatScore(newFormats) > p.FormatScore(currentFormats)
	if isRevisionUpgrade(current, newQ) {
		return true
	}
	if (isQualityUpgrade || isFormatUpgrade) && p.UpgradeAllowed {
		return true
	}
	if isQualityUpgrade || isFormatUpgrade {
		return false
	}
	return true
}
