package policy

// Policy settings the decision engine (internal/decide) reads beyond the profile's own qualities and scores: the
// propers setting and an indexer's multi languages.

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

func (p *Policy) validatePropers(e *Errors, path string, pr *Profile) {
	switch pr.Propers {
	case "", PropersPreferAndUpgrade, PropersDoNotUpgrade, PropersDoNotPrefer:
	default:
		e.add(path+".propers", "must be %s, %s or %s, got %q", PropersPreferAndUpgrade, PropersDoNotUpgrade,
			PropersDoNotPrefer, pr.Propers)
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
