package policy

// Policy settings the decision engine (internal/decide) reads beyond the profile's own qualities and scores: a
// profile taken from an arr instance (from_arr), the propers setting, and an indexer's multi languages.

import (
	"fmt"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
)

// Propers settings, as written in the policy.
const (
	PropersPreferAndUpgrade = "prefer_and_upgrade"
	PropersDoNotUpgrade     = "do_not_upgrade"
	PropersDoNotPrefer      = "do_not_prefer"
)

// SplitFromArr splits a from_arr reference "<instance>/<profile name>". The profile name may itself contain a
// slash; the instance is everything before the first one.
func SplitFromArr(ref string) (instance, profile string, ok bool) {
	instance, profile, ok = strings.Cut(ref, "/")
	instance, profile = strings.TrimSpace(instance), strings.TrimSpace(profile)
	return instance, profile, ok && instance != "" && profile != ""
}

func (p *Policy) validatePropers(e *Errors, path string, pr *Profile) {
	switch pr.Propers {
	case "", PropersPreferAndUpgrade, PropersDoNotUpgrade, PropersDoNotPrefer:
	default:
		e.add(path+".propers", "must be %s, %s or %s, got %q", PropersPreferAndUpgrade, PropersDoNotUpgrade,
			PropersDoNotPrefer, pr.Propers)
	}
}

// validateFromArr checks a profile taken from an arr: the reference names an import.arr instance and a profile,
// and nothing that the arr's profile would replace is set.
func (p *Policy) validateFromArr(e *Errors, path string, pr *Profile) {
	fp := path + ".from_arr"
	inst, _, ok := SplitFromArr(pr.FromArr)
	if !ok {
		e.add(fp, "%q must be <instance>/<profile name>", pr.FromArr)
	} else if p.Import == nil || p.Import.Arr[inst] == nil {
		e.add(fp, "unknown arr instance %q; declare it under import.arr", inst)
	}
	var set []string
	if pr.FormatSet != "" {
		set = append(set, "format_set")
	}
	if len(pr.Qualities) > 0 {
		set = append(set, "qualities")
	}
	if len(pr.QualityOrder) > 0 {
		set = append(set, "quality_order")
	}
	if pr.Cutoff != "" {
		set = append(set, "cutoff")
	}
	if pr.Upgrades != nil {
		set = append(set, "upgrades")
	}
	if pr.MinScore != 0 {
		set = append(set, "min_score")
	}
	if pr.CutoffScore != 0 {
		set = append(set, "cutoff_score")
	}
	if pr.MinUpgradeStep != nil {
		set = append(set, "min_upgrade_step")
	}
	if len(pr.Sizes) > 0 {
		set = append(set, "sizes")
	}
	if len(pr.Formats) > 0 {
		set = append(set, "formats")
	}
	if pr.Language != "" {
		set = append(set, "language")
	}
	if len(set) > 0 {
		e.add(path, "from_arr takes the whole profile from the arr; remove %s", strings.Join(set, ", "))
	}
}

// checkFromArrFits checks that the arr instance a profile comes from is of the version's kind.
func (p *Policy) checkFromArrFits(e *Errors, path string, pr *Profile, kind Kind) {
	inst, _, ok := SplitFromArr(pr.FromArr)
	if !ok || p.Import == nil || p.Import.Arr[inst] == nil {
		return
	}
	if k, known := flavours[p.Import.Arr[inst].Flavour]; known && k != kind {
		e.add(path, "profile comes from %s instance %q, but the library is a %s", p.Import.Arr[inst].Flavour, inst, kind)
	}
}

// validateProfileLanguage checks a profile's language: any, original or a Radarr language name. Radarr's
// QualityProfile.Language takes every language of Language.All, Unknown included.
func validateProfileLanguage(e *Errors, path string, pr *Profile) {
	if pr.Language == "" {
		return
	}
	if strings.TrimSpace(pr.Language) != pr.Language {
		e.add(path+".language", "%q has surrounding spaces", pr.Language)
		return
	}
	if _, ok := format.Radarr.LanguageID(pr.Language); !ok {
		e.add(path+".language", "unknown language %q (any, original, or the arr's name, e.g. English, French)", pr.Language)
	}
}

// ProfileLanguage is the profile's language as a Radarr language id: Any when unset.
func (pr *Profile) ProfileLanguage() int {
	if pr.Language == "" {
		return format.LanguageAny
	}
	if id, ok := format.Radarr.LanguageID(pr.Language); ok {
		return id
	}
	return format.LanguageAny
}

func validateMultiLanguages(e *Errors, path string, langs []string) {
	seen := map[string]bool{}
	for i, l := range langs {
		lp := fmt.Sprintf("%s[%d]", path, i)
		_, inRadarr := format.Radarr.LanguageID(l)
		_, inSonarr := format.Sonarr.LanguageID(l)
		switch {
		case strings.TrimSpace(l) == "":
			e.add(lp, "empty language name")
		case !inRadarr && !inSonarr:
			e.add(lp, "unknown language %q (use the arr's name, e.g. English, French)", l)
		case strings.EqualFold(l, "Unknown") || strings.EqualFold(l, "Any"):
			e.add(lp, "%q is not a language a release can hold", l)
		case seen[l]:
			e.add(lp, "%q listed twice", l)
		}
		seen[l] = true
	}
}
