package sonarr

// Port of Sonarr src/NzbDrone.Core/Languages/Language.cs and src/NzbDrone.Core/Parser/{IsoLanguage,IsoLanguages}.cs
// @ cab419a, with FileNameBuilder.Iso639BTMap from src/NzbDrone.Core/Organizer/FileNameBuilder.cs.

import "strings"

// Language is one of Sonarr's languages.
type Language struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (l Language) String() string { return l.Name }

// The languages, with Sonarr's ids.
var (
	LanguageUnknown          = Language{0, "Unknown"}
	LanguageEnglish          = Language{1, "English"}
	LanguageFrench           = Language{2, "French"}
	LanguageSpanish          = Language{3, "Spanish"}
	LanguageGerman           = Language{4, "German"}
	LanguageItalian          = Language{5, "Italian"}
	LanguageDanish           = Language{6, "Danish"}
	LanguageDutch            = Language{7, "Dutch"}
	LanguageJapanese         = Language{8, "Japanese"}
	LanguageIcelandic        = Language{9, "Icelandic"}
	LanguageChinese          = Language{10, "Chinese"}
	LanguageRussian          = Language{11, "Russian"}
	LanguagePolish           = Language{12, "Polish"}
	LanguageVietnamese       = Language{13, "Vietnamese"}
	LanguageSwedish          = Language{14, "Swedish"}
	LanguageNorwegian        = Language{15, "Norwegian"}
	LanguageFinnish          = Language{16, "Finnish"}
	LanguageTurkish          = Language{17, "Turkish"}
	LanguagePortuguese       = Language{18, "Portuguese"}
	LanguageFlemish          = Language{19, "Flemish"}
	LanguageGreek            = Language{20, "Greek"}
	LanguageKorean           = Language{21, "Korean"}
	LanguageHungarian        = Language{22, "Hungarian"}
	LanguageHebrew           = Language{23, "Hebrew"}
	LanguageLithuanian       = Language{24, "Lithuanian"}
	LanguageCzech            = Language{25, "Czech"}
	LanguageArabic           = Language{26, "Arabic"}
	LanguageHindi            = Language{27, "Hindi"}
	LanguageBulgarian        = Language{28, "Bulgarian"}
	LanguageMalayalam        = Language{29, "Malayalam"}
	LanguageUkrainian        = Language{30, "Ukrainian"}
	LanguageSlovak           = Language{31, "Slovak"}
	LanguageThai             = Language{32, "Thai"}
	LanguagePortugueseBrazil = Language{33, "Portuguese (Brazil)"}
	LanguageSpanishLatino    = Language{34, "Spanish (Latino)"}
	LanguageRomanian         = Language{35, "Romanian"}
	LanguageLatvian          = Language{36, "Latvian"}
	LanguagePersian          = Language{37, "Persian"}
	LanguageCatalan          = Language{38, "Catalan"}
	LanguageCroatian         = Language{39, "Croatian"}
	LanguageSerbian          = Language{40, "Serbian"}
	LanguageBosnian          = Language{41, "Bosnian"}
	LanguageEstonian         = Language{42, "Estonian"}
	LanguageTamil            = Language{43, "Tamil"}
	LanguageIndonesian       = Language{44, "Indonesian"}
	LanguageMacedonian       = Language{45, "Macedonian"}
	LanguageSlovenian        = Language{46, "Slovenian"}
	LanguageOriginal         = Language{-2, "Original"}
)

// AllLanguages is Language.All, in Sonarr's order.
var AllLanguages = []Language{
	LanguageUnknown, LanguageEnglish, LanguageFrench, LanguageSpanish, LanguageGerman, LanguageItalian,
	LanguageDanish, LanguageDutch, LanguageJapanese, LanguageIcelandic, LanguageChinese, LanguageRussian,
	LanguagePolish, LanguageVietnamese, LanguageSwedish, LanguageNorwegian, LanguageFinnish, LanguageTurkish,
	LanguagePortuguese, LanguageFlemish, LanguageGreek, LanguageKorean, LanguageHungarian, LanguageHebrew,
	LanguageLithuanian, LanguageCzech, LanguageArabic, LanguageHindi, LanguageBulgarian, LanguageMalayalam,
	LanguageUkrainian, LanguageSlovak, LanguageThai, LanguagePortugueseBrazil, LanguageSpanishLatino,
	LanguageRomanian, LanguageLatvian, LanguagePersian, LanguageCatalan, LanguageCroatian, LanguageSerbian,
	LanguageBosnian, LanguageEstonian, LanguageTamil, LanguageIndonesian, LanguageMacedonian, LanguageSlovenian,
	LanguageOriginal,
}

// FindLanguageByID is Language.FindById.
func FindLanguageByID(id int) (Language, bool) {
	for _, l := range AllLanguages {
		if l.ID == id {
			return l, true
		}
	}
	return Language{}, false
}

// FindLanguageByName is the explicit (Language)string conversion: a case-insensitive name match.
func FindLanguageByName(name string) (Language, bool) {
	for _, l := range AllLanguages {
		if strings.EqualFold(l.Name, name) {
			return l, true
		}
	}
	return Language{}, false
}

// IsoLanguage is Sonarr's IsoLanguage.
type IsoLanguage struct {
	TwoLetterCode   string   `json:"twoLetterCode"`
	ThreeLetterCode string   `json:"threeLetterCode"`
	CountryCode     string   `json:"countryCode"`
	Language        Language `json:"language"`
}

var isoLanguages = []IsoLanguage{
	{"en", "eng", "", LanguageEnglish},
	{"fr", "fra", "fr", LanguageFrench},
	{"es", "spa", "", LanguageSpanish},
	{"de", "deu", "de", LanguageGerman},
	{"it", "ita", "", LanguageItalian},
	{"da", "dan", "", LanguageDanish},
	{"nl", "nld", "", LanguageDutch},
	{"ja", "jpn", "", LanguageJapanese},
	{"is", "isl", "", LanguageIcelandic},
	{"zh", "zho", "cn", LanguageChinese},
	{"ru", "rus", "", LanguageRussian},
	{"pl", "pol", "", LanguagePolish},
	{"vi", "vie", "", LanguageVietnamese},
	{"sv", "swe", "", LanguageSwedish},
	{"no", "nor", "", LanguageNorwegian},
	{"nb", "nob", "", LanguageNorwegian}, // Norwegian Bokmål
	{"fi", "fin", "", LanguageFinnish},
	{"tr", "tur", "", LanguageTurkish},
	{"pt", "por", "pt", LanguagePortuguese},
	{"nl", "nld", "", LanguageFlemish},
	{"el", "ell", "", LanguageGreek},
	{"ko", "kor", "", LanguageKorean},
	{"hu", "hun", "", LanguageHungarian},
	{"he", "heb", "", LanguageHebrew},
	{"lt", "lit", "", LanguageLithuanian},
	{"cs", "ces", "", LanguageCzech},
	{"ar", "ara", "", LanguageArabic},
	{"hi", "hin", "", LanguageHindi},
	{"bg", "bul", "", LanguageBulgarian},
	{"ml", "mal", "", LanguageMalayalam},
	{"uk", "ukr", "", LanguageUkrainian},
	{"sk", "slk", "", LanguageSlovak},
	{"th", "tha", "th", LanguageThai},
	{"pt", "por", "br", LanguagePortugueseBrazil},
	{"es", "spa", "mx", LanguageSpanishLatino},
	{"ro", "ron", "", LanguageRomanian},
	{"lv", "lav", "", LanguageLatvian},
	{"fa", "fas", "", LanguagePersian},
	{"ca", "cat", "", LanguageCatalan},
	{"hr", "hrv", "", LanguageCroatian},
	{"sr", "srp", "", LanguageSerbian},
	{"bs", "bos", "", LanguageBosnian},
	{"et", "est", "", LanguageEstonian},
	{"ta", "tam", "", LanguageTamil},
	{"id", "ind", "", LanguageIndonesian},
	{"mk", "mkd", "", LanguageMacedonian},
	{"sl", "slv", "", LanguageSlovenian},
}

var alternateIsoCodeMappings = map[string]Language{
	"cze":  LanguageCzech,
	"dut":  LanguageDutch,
	"mac":  LanguageMacedonian,
	"rum":  LanguageRomanian,
	"yue":  LanguageChinese,
	"zhtw": LanguageChinese,
}

// iso639BTMap is FileNameBuilder.Iso639BTMap.
var iso639BTMap = map[string]string{
	"alb": "sqi", "arm": "hye", "baq": "eus", "bur": "mya", "chi": "zho", "cze": "ces", "dut": "nld",
	"fre": "fra", "geo": "kat", "ger": "deu", "gre": "ell", "ice": "isl", "mac": "mkd", "mao": "mri",
	"may": "msa", "per": "fas", "rum": "ron", "slo": "slk", "tib": "bod", "wel": "cym",
}

// FindIsoLanguage is IsoLanguages.Find. It returns nil where Sonarr returns null.
//
// IsoLanguages.All is a HashSet in Sonarr, iterated in insertion order (no removals), so the slice order matches.
func FindIsoLanguage(isoCode string) *IsoLanguage { return v4plusd.findIsoLanguage(isoCode) }

func (d *dialect) findIsoLanguage(isoCode string) *IsoLanguage {
	isoArray := strings.Split(isoCode, "-")
	langCode := strings.ToLower(isoArray[0])

	if len([]rune(langCode)) == 2 {
		var candidates []IsoLanguage
		for _, l := range d.isoLanguages {
			if l.TwoLetterCode == langCode {
				candidates = append(candidates, l)
			}
		}

		if len(isoArray) > 1 {
			country := strings.ToLower(isoArray[1])
			var byCountry, noCountry []IsoLanguage
			for _, l := range candidates {
				if l.CountryCode == country {
					byCountry = append(byCountry, l)
				}
				if l.CountryCode == "" {
					noCountry = append(noCountry, l)
				}
			}
			if len(byCountry) > 0 {
				candidates = byCountry
			} else {
				candidates = noCountry
			}
		}

		if len(candidates) == 0 {
			return nil
		}
		l := candidates[0]
		return &l
	} else if lang, ok := alternateIsoCodeMappings[isoCode]; ok {
		return d.getIsoLanguage(lang)
	} else if len([]rune(langCode)) == 3 {
		if mapped, ok := iso639BTMap[langCode]; ok {
			langCode = mapped
		}
		for _, l := range d.isoLanguages {
			if l.ThreeLetterCode == langCode {
				l := l
				return &l
			}
		}
	}

	return nil
}

// GetIsoLanguage is IsoLanguages.Get.
func GetIsoLanguage(language Language) *IsoLanguage { return v4plusd.getIsoLanguage(language) }

func (d *dialect) getIsoLanguage(language Language) *IsoLanguage {
	for _, l := range d.isoLanguages {
		if l.Language == language {
			l := l
			return &l
		}
	}
	return nil
}

// FindIsoLanguageByName is IsoLanguages.FindByName.
func FindIsoLanguageByName(name string) *IsoLanguage {
	for _, l := range isoLanguages {
		if strings.EqualFold(l.Language.Name, name) {
			l := l
			return &l
		}
	}
	return nil
}
