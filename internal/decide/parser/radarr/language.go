// Port of Radarr src/NzbDrone.Core/Languages/Language.cs and src/NzbDrone.Core/Parser/{IsoLanguage,
// IsoLanguages}.cs @ a96bf7c (with FileNameBuilder.Iso639BTMap from src/NzbDrone.Core/Organizer).

package radarr

import (
	"strings"
)

// Language is Radarr's Language: {"id": 1, "name": "English"}. Equality is by Id.
type Language struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (l Language) String() string { return l.Name }

// The languages, with Radarr's ids and names.
var (
	LanguageUnknown       = Language{0, "Unknown"}
	LanguageEnglish       = Language{1, "English"}
	LanguageFrench        = Language{2, "French"}
	LanguageSpanish       = Language{3, "Spanish"}
	LanguageGerman        = Language{4, "German"}
	LanguageItalian       = Language{5, "Italian"}
	LanguageDanish        = Language{6, "Danish"}
	LanguageDutch         = Language{7, "Dutch"}
	LanguageJapanese      = Language{8, "Japanese"}
	LanguageIcelandic     = Language{9, "Icelandic"}
	LanguageChinese       = Language{10, "Chinese"}
	LanguageRussian       = Language{11, "Russian"}
	LanguagePolish        = Language{12, "Polish"}
	LanguageVietnamese    = Language{13, "Vietnamese"}
	LanguageSwedish       = Language{14, "Swedish"}
	LanguageNorwegian     = Language{15, "Norwegian"}
	LanguageFinnish       = Language{16, "Finnish"}
	LanguageTurkish       = Language{17, "Turkish"}
	LanguagePortuguese    = Language{18, "Portuguese"}
	LanguageFlemish       = Language{19, "Flemish"}
	LanguageGreek         = Language{20, "Greek"}
	LanguageKorean        = Language{21, "Korean"}
	LanguageHungarian     = Language{22, "Hungarian"}
	LanguageHebrew        = Language{23, "Hebrew"}
	LanguageLithuanian    = Language{24, "Lithuanian"}
	LanguageCzech         = Language{25, "Czech"}
	LanguageHindi         = Language{26, "Hindi"}
	LanguageRomanian      = Language{27, "Romanian"}
	LanguageThai          = Language{28, "Thai"}
	LanguageBulgarian     = Language{29, "Bulgarian"}
	LanguagePortugueseBR  = Language{30, "Portuguese (Brazil)"}
	LanguageArabic        = Language{31, "Arabic"}
	LanguageUkrainian     = Language{32, "Ukrainian"}
	LanguagePersian       = Language{33, "Persian"}
	LanguageBengali       = Language{34, "Bengali"}
	LanguageSlovak        = Language{35, "Slovak"}
	LanguageLatvian       = Language{36, "Latvian"}
	LanguageSpanishLatino = Language{37, "Spanish (Latino)"}
	LanguageCatalan       = Language{38, "Catalan"}
	LanguageCroatian      = Language{39, "Croatian"}
	LanguageSerbian       = Language{40, "Serbian"}
	LanguageBosnian       = Language{41, "Bosnian"}
	LanguageEstonian      = Language{42, "Estonian"}
	LanguageTamil         = Language{43, "Tamil"}
	LanguageIndonesian    = Language{44, "Indonesian"}
	LanguageTelugu        = Language{45, "Telugu"}
	LanguageMacedonian    = Language{46, "Macedonian"}
	LanguageSlovenian     = Language{47, "Slovenian"}
	LanguageMalayalam     = Language{48, "Malayalam"}
	LanguageKannada       = Language{49, "Kannada"}
	LanguageAlbanian      = Language{50, "Albanian"}
	LanguageAfrikaans     = Language{51, "Afrikaans"}
	LanguageMarathi       = Language{52, "Marathi"}
	LanguageTagalog       = Language{53, "Tagalog"}
	LanguageUrdu          = Language{54, "Urdu"}
	LanguageRomansh       = Language{55, "Romansh"}
	LanguageMongolian     = Language{56, "Mongolian"}
	LanguageGeorgian      = Language{57, "Georgian"}
	LanguageAny           = Language{-1, "Any"}
	LanguageOriginal      = Language{-2, "Original"}
)

// AllLanguages is Language.All, in Radarr's order (Romanian before Hindi, as in Radarr).
var AllLanguages = []Language{
	LanguageUnknown, LanguageEnglish, LanguageFrench, LanguageSpanish, LanguageGerman, LanguageItalian,
	LanguageDanish, LanguageDutch, LanguageJapanese, LanguageIcelandic, LanguageChinese, LanguageRussian,
	LanguagePolish, LanguageVietnamese, LanguageSwedish, LanguageNorwegian, LanguageFinnish, LanguageTurkish,
	LanguagePortuguese, LanguageFlemish, LanguageGreek, LanguageKorean, LanguageHungarian, LanguageHebrew,
	LanguageLithuanian, LanguageCzech, LanguageRomanian, LanguageHindi, LanguageThai, LanguageBulgarian,
	LanguagePortugueseBR, LanguageArabic, LanguageUkrainian, LanguagePersian, LanguageBengali, LanguageSlovak,
	LanguageLatvian, LanguageSpanishLatino, LanguageCatalan, LanguageCroatian, LanguageSerbian, LanguageBosnian,
	LanguageEstonian, LanguageTamil, LanguageIndonesian, LanguageTelugu, LanguageMacedonian, LanguageSlovenian,
	LanguageMalayalam, LanguageKannada, LanguageAlbanian, LanguageAfrikaans, LanguageMarathi, LanguageTagalog,
	LanguageUrdu, LanguageRomansh, LanguageMongolian, LanguageGeorgian, LanguageAny, LanguageOriginal,
}

// LanguageByID is Language.FindById.
func LanguageByID(id int) (Language, bool) {
	for _, l := range AllLanguages {
		if l.ID == id {
			return l, true
		}
	}
	return Language{}, false
}

// LanguageByName is the explicit (Language)string conversion: a case-insensitive name lookup.
func LanguageByName(name string) (Language, bool) {
	for _, l := range AllLanguages {
		if strings.EqualFold(l.Name, name) {
			return l, true
		}
	}
	return Language{}, false
}

// IsoLanguage is Radarr's IsoLanguage.
type IsoLanguage struct {
	TwoLetterCode   string   `json:"twoLetterCode"`
	ThreeLetterCode string   `json:"threeLetterCode"`
	CountryCode     string   `json:"countryCode"`
	EnglishName     string   `json:"englishName"`
	Language        Language `json:"language"`
}

// isoLanguages is IsoLanguages.All, in declaration order (the .NET HashSet enumerates in insertion order).
var isoLanguages = []IsoLanguage{
	{"en", "eng", "", "English", LanguageEnglish},
	{"fr", "fra", "fr", "French", LanguageFrench},
	{"es", "spa", "", "Spanish", LanguageSpanish},
	{"de", "deu", "de", "German", LanguageGerman},
	{"it", "ita", "", "Italian", LanguageItalian},
	{"da", "dan", "", "Danish", LanguageDanish},
	{"nl", "nld", "", "Dutch", LanguageDutch},
	{"ja", "jpn", "", "Japanese", LanguageJapanese},
	{"is", "isl", "", "Icelandic", LanguageIcelandic},
	{"zh", "zho", "cn", "Chinese", LanguageChinese},
	{"ru", "rus", "", "Russian", LanguageRussian},
	{"pl", "pol", "", "Polish", LanguagePolish},
	{"vi", "vie", "", "Vietnamese", LanguageVietnamese},
	{"sv", "swe", "", "Swedish", LanguageSwedish},
	{"no", "nor", "", "Norwegian", LanguageNorwegian},
	{"nb", "nob", "", "Norwegian Bokmal", LanguageNorwegian},
	{"fi", "fin", "", "Finnish", LanguageFinnish},
	{"tr", "tur", "", "Turkish", LanguageTurkish},
	{"pt", "por", "pt", "Portuguese", LanguagePortuguese},
	{"el", "ell", "", "Greek", LanguageGreek},
	{"ko", "kor", "", "Korean", LanguageKorean},
	{"hu", "hun", "", "Hungarian", LanguageHungarian},
	{"he", "heb", "", "Hebrew", LanguageHebrew},
	{"cs", "ces", "", "Czech", LanguageCzech},
	{"hi", "hin", "", "Hindi", LanguageHindi},
	{"th", "tha", "", "Thai", LanguageThai},
	{"bg", "bul", "", "Bulgarian", LanguageBulgarian},
	{"ro", "ron", "", "Romanian", LanguageRomanian},
	{"pt", "", "br", "Portuguese (Brazil)", LanguagePortugueseBR},
	{"ar", "ara", "", "Arabic", LanguageArabic},
	{"uk", "ukr", "", "Ukrainian", LanguageUkrainian},
	{"fa", "fas", "", "Persian", LanguagePersian},
	{"bn", "ben", "", "Bengali", LanguageBengali},
	{"lt", "lit", "", "Lithuanian", LanguageLithuanian},
	{"sk", "slk", "", "Slovak", LanguageSlovak},
	{"lv", "lav", "", "Latvian", LanguageLatvian},
	{"es", "spa", "mx", "Spanish (Latino)", LanguageSpanishLatino},
	{"ca", "cat", "", "Catalan", LanguageCatalan},
	{"hr", "hrv", "", "Croatian", LanguageCroatian},
	{"sr", "srp", "", "Serbian", LanguageSerbian},
	{"bs", "bos", "", "Bosnian", LanguageBosnian},
	{"et", "est", "", "Estonian", LanguageEstonian},
	{"ta", "tam", "", "Tamil", LanguageTamil},
	{"id", "ind", "", "Indonesian", LanguageIndonesian},
	{"te", "tel", "", "Telugu", LanguageTelugu},
	{"mk", "mkd", "", "Macedonian", LanguageMacedonian},
	{"sl", "slv", "", "Slovenian", LanguageSlovenian},
	{"ml", "mal", "", "Malayalam", LanguageMalayalam},
	{"kn", "kan", "", "Kannada", LanguageKannada},
	{"sq", "sqi", "", "Albanian", LanguageAlbanian},
	{"af", "afr", "", "Afrikaans", LanguageAfrikaans},
	{"mr", "mar", "", "Marathi", LanguageMarathi},
	{"tl", "tgl", "", "Tagalog", LanguageTagalog},
	{"ur", "urd", "", "Urdu", LanguageUrdu},
	{"rm", "roh", "", "Romansh", LanguageRomansh},
	{"mn", "mon", "", "Mongolian", LanguageMongolian},
	{"ka", "kat", "", "Georgian", LanguageGeorgian},
}

var alternateIsoCodeMappings = map[string]Language{"cn": LanguageChinese}

// iso639BTMap is FileNameBuilder.Iso639BTMap: ISO 639-2/B codes to 639-2/T.
var iso639BTMap = map[string]string{
	"alb": "sqi", "arm": "hye", "baq": "eus", "bur": "mya", "chi": "zho", "cze": "ces", "dut": "nld",
	"fre": "fra", "geo": "kat", "ger": "deu", "gre": "ell", "gsw": "deu", "ice": "isl", "mac": "mkd",
	"mao": "mri", "may": "msa", "per": "fas", "rum": "ron", "slo": "slk", "tib": "bod", "wel": "cym",
	"khk": "mon", "mvf": "mon",
}

// FindIsoLanguage is IsoLanguages.Find: nil when the code is unknown.
func FindIsoLanguage(isoCode string) *IsoLanguage {
	isoArray := strings.Split(isoCode, "-")
	langCode := strings.ToLower(isoArray[0])

	if alt, ok := alternateIsoCodeMappings[isoCode]; ok {
		return GetIsoLanguage(alt)
	}

	switch len([]rune(langCode)) {
	case 2:
		// Lookup ISO639-1 code
		var found []IsoLanguage
		for _, l := range isoLanguages {
			if l.TwoLetterCode == langCode {
				found = append(found, l)
			}
		}
		if len(isoArray) > 1 {
			country := strings.ToLower(isoArray[1])
			var byCountry, noCountry []IsoLanguage
			for _, l := range found {
				if l.CountryCode == country {
					byCountry = append(byCountry, l)
				}
				if l.CountryCode == "" {
					noCountry = append(noCountry, l)
				}
			}
			if len(byCountry) > 0 {
				found = byCountry
			} else {
				found = noCountry
			}
		}
		if len(found) == 0 {
			return nil
		}
		l := found[0]
		return &l
	case 3:
		// Lookup ISO639-2T code
		if mapped, ok := iso639BTMap[langCode]; ok {
			langCode = mapped
		}
		for _, l := range isoLanguages {
			if l.ThreeLetterCode == langCode {
				l := l
				return &l
			}
		}
	}
	return nil
}

// GetIsoLanguage is IsoLanguages.Get.
func GetIsoLanguage(language Language) *IsoLanguage {
	for _, l := range isoLanguages {
		if l.Language.ID == language.ID {
			l := l
			return &l
		}
	}
	return nil
}

// FindIsoLanguageByName is IsoLanguages.FindByName.
func FindIsoLanguageByName(name string) *IsoLanguage {
	name = netTrim(name)
	for _, l := range isoLanguages {
		if strings.EqualFold(l.EnglishName, name) {
			l := l
			return &l
		}
	}
	return nil
}
