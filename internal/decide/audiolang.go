package decide

import (
	"strings"

	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
)

// The one mapping of language codes and names that the import's wrong-audio rule (library/audio.go) and the
// journal's file audio check share, so the two cannot disagree. A language is held as the three-letter code of the
// arrs' language table (Radarr's IsoLanguages): "eng", "fra", "kor", "nor". A code the table lacks (und, zxx, mul,
// mis, a private qaa-qtz code, myn, ...) is unknown.

// AudioLanguage reads an audio track's language tag (ISO 639-1, 639-2/B or /T, or a BCP 47 tag whose region is cut:
// ja-JP is Japanese): the table's three-letter code, or false when the tag is empty or the table lacks it. Norwegian
// Bokmål and Nynorsk (nb, nn, nob, nno) are Norwegian.
func AudioLanguage(tag string) (string, bool) {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_"); i >= 0 {
		tag = tag[:i]
	}
	switch tag {
	case "nn", "nno":
		tag = "nor"
	}
	if len(tag) != 2 && len(tag) != 3 {
		return "", false
	}
	iso := radarr.FindIsoLanguage(tag)
	if iso == nil {
		return "", false
	}
	return canonicalCode(iso.Language, iso.TwoLetterCode)
}

// LanguageCode reads a language held by name ("Korean", "Portuguese (Brazil)", as the arrs store a title's original
// language and a profile's) or by code ("ko", "kor", as the metadata stores it): its three-letter code, or false
// for an empty value, Unknown, Any, Original or a language the table lacks.
func LanguageCode(s string) (string, bool) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "", "unknown", "any", "original":
		return "", false
	}
	if l, ok := radarr.LanguageByName(s); ok {
		if iso := radarr.GetIsoLanguage(l); iso != nil {
			return canonicalCode(l, iso.TwoLetterCode)
		}
		return "", false
	}
	if iso := radarr.FindIsoLanguageByName(s); iso != nil {
		return canonicalCode(iso.Language, iso.TwoLetterCode)
	}
	if strings.EqualFold(s, "cn") {
		return canonicalCode(radarr.LanguageChinese, "zh")
	}
	return AudioLanguage(s)
}

// LanguageName is the table's name of a code LanguageCode or AudioLanguage gave ("fra": French); the code itself
// when the table lacks it.
func LanguageName(code string) string {
	if iso := radarr.FindIsoLanguage(code); iso != nil {
		return iso.Language.Name
	}
	return code
}

// canonicalCode is the three-letter code of a language's first table entry (Norwegian Bokmål: nor), else of its
// two-letter code's (Portuguese (Brazil) has no three-letter code of its own: por).
func canonicalCode(l radarr.Language, two string) (string, bool) {
	if iso := radarr.GetIsoLanguage(l); iso != nil && iso.ThreeLetterCode != "" {
		return iso.ThreeLetterCode, true
	}
	if iso := radarr.FindIsoLanguage(two); iso != nil && iso.ThreeLetterCode != "" {
		return iso.ThreeLetterCode, true
	}
	return "", false
}
