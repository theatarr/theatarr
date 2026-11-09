// Port of Radarr src/NzbDrone.Core/Parser/LanguageParser.cs and Parser/Model/SubtitleTitleInfo.cs @ a96bf7c.

package radarr

import (
	"strconv"
	"strings"
)

var (
	languageRegex = re(`(?:\W|_|^)(?<english>\beng\b)|
                                                                            (?<italian>\b(?:ita|italian)\b)|
                                                                            (?<german>(?:swiss)?german\b|videomann|ger[. ]dub|\bger\b)|
                                                                            (?<flemish>flemish)|
                                                                            (?<bulgarian>bgaudio)|
                                                                            (?<romanian>rodubbed)|
                                                                            (?<brazilian>\b(dublado|pt-BR)\b)|
                                                                            (?<greek>greek)|
                                                                            (?<french>\b(?:FR|VO|VF|VFF|VFQ|VFI|VF2|TRUEFRENCH|FRENCH|FRE|FRA)\b)|
                                                                            (?<russian>\b(?:rus|ru)\b)|
                                                                            (?<hungarian>\b(?:HUNDUB|HUN)\b)|
                                                                            (?<hebrew>\b(?:HebDub|HebDubbed)\b)|
                                                                            (?<polish>\b(?:PL\W?DUB|DUB\W?PL|LEK\W?PL|PL\W?LEK)\b)|
                                                                            (?<chinese>\[(?:CH[ST]|BIG5|GB)\]|简|繁|字幕)|
                                                                            (?<ukrainian>(?:(?:\dx)?UKR))|
                                                                            (?<spanish>\b(?:español|castellano)\b)|
                                                                            (?<catalan>\b(?:catalan?|catalán|català)\b)|
                                                                            (?<latvian>\b(?:lat|lav|lv)\b)|
                                                                            (?<telugu>\btel\b)|
                                                                            (?<vietnamese>\bVIE\b)|
                                                                            (?<japanese>\bJAP\b)|
                                                                            (?<korean>\bKOR\b)|
                                                                            (?<urdu>\burdu\b)|
                                                                            (?<romansh>\b(?:romansh|rumantsch|romansch)\b)|
                                                                            (?<mongolian>\b(?:mongolian|khalkha)\b)|
                                                                            (?<georgian>\b(?:georgian|geo|ka|kat)\b)|
                                                                            (?<original>\b(?:orig|original)\b)`, optI|optX)

	caseSensitiveLanguageRegex = re(`(?:(?i)(?<!SUB[\W|_|^]))(?:(?<english>\bEN\b)|
                                                                                                          (?<lithuanian>\bLT\b)|
                                                                                                          (?<czech>\bCZ\b)|
                                                                                                          (?<polish>\bPL\b)|
                                                                                                          (?<bulgarian>\bBG\b)|
                                                                                                          (?<slovak>\bSK\b)|
                                                                                                          (?<german>\bDE\b)|
                                                                                                          (?<spanish>\b(?<!DTS[._ -])ES\b))(?:(?i)(?![\W|_|^]SUB))`, optX)

	germanDualLanguageRegex  = re(`(?<!WEB[-_. ]?)\bDL\b`, optI)
	germanMultiLanguageRegex = re(`\bML\b`, optI)

	subtitleLanguageRegex = re(`.+?([-_. ](?<tags>forced|foreign|default|cc|psdh|sdh))*[-_. ](?<iso_code>[a-z]{2,3})([-_. ](?<tags>forced|foreign|default|cc|psdh|sdh))*$`, optI)

	subtitleLanguageTitleRegex = re(`.+?(\.((?<tags1>forced|foreign|default|cc|psdh|sdh)|(?<iso_code>[a-z]{2,3})))*[-_. ](?<title>[^.]*)(\.((?<tags2>forced|foreign|default|cc|psdh|sdh)|(?<iso_code>[a-z]{2,3})))*$`, optI)

	subtitleTitleRegex = re(`^((?<title>.+) - )?(?<copy>(?<!\d+)\d{1,3}(?!\d+))$`, 0)
)

// Word checks run on the lower-cased title, in Radarr's order.
var languageWords = []struct {
	words []string
	lang  Language
}{
	{[]string{"english"}, LanguageEnglish},
	{[]string{"spanish"}, LanguageSpanish},
	{[]string{"danish"}, LanguageDanish},
	{[]string{"dutch"}, LanguageDutch},
	{[]string{"japanese"}, LanguageJapanese},
	{[]string{"icelandic"}, LanguageIcelandic},
	{[]string{"mandarin", "cantonese", "chinese"}, LanguageChinese},
	{[]string{"korean"}, LanguageKorean},
	{[]string{"russian"}, LanguageRussian},
	{[]string{"romanian"}, LanguageRomanian},
	{[]string{"hindi"}, LanguageHindi},
	{[]string{"arabic"}, LanguageArabic},
	{[]string{"thai"}, LanguageThai},
	{[]string{"bulgarian"}, LanguageBulgarian},
	{[]string{"polish"}, LanguagePolish},
	{[]string{"vietnamese"}, LanguageVietnamese},
	{[]string{"swedish"}, LanguageSwedish},
	{[]string{"norwegian"}, LanguageNorwegian},
	{[]string{"finnish"}, LanguageFinnish},
	{[]string{"turkish"}, LanguageTurkish},
	{[]string{"portuguese"}, LanguagePortuguese},
	{[]string{"brazilian"}, LanguagePortugueseBR},
	{[]string{"hungarian"}, LanguageHungarian},
	{[]string{"hebrew"}, LanguageHebrew},
	{[]string{"ukrainian"}, LanguageUkrainian},
	{[]string{"persian"}, LanguagePersian},
	{[]string{"bengali"}, LanguageBengali},
	{[]string{"slovak"}, LanguageSlovak},
	{[]string{"latvian"}, LanguageLatvian},
	{[]string{"latino"}, LanguageSpanishLatino},
	{[]string{"tamil"}, LanguageTamil},
	{[]string{"telugu"}, LanguageTelugu},
	{[]string{"malayalam"}, LanguageMalayalam},
	{[]string{"kannada"}, LanguageKannada},
	{[]string{"albanian"}, LanguageAlbanian},
	{[]string{"afrikaans"}, LanguageAfrikaans},
	{[]string{"marathi"}, LanguageMarathi},
	{[]string{"tagalog"}, LanguageTagalog},
}

type groupLanguage struct {
	group string
	lang  Language
}

// Group checks per case-sensitive match, in Radarr's order.
var caseSensitiveGroups = []groupLanguage{
	{"english", LanguageEnglish},
	{"lithuanian", LanguageLithuanian},
	{"czech", LanguageCzech},
	{"polish", LanguagePolish},
	{"bulgarian", LanguageBulgarian},
	{"slovak", LanguageSlovak},
	{"spanish", LanguageSpanish},
	{"german", LanguageGerman},
}

// Group checks per case-insensitive match, in Radarr's order. "dutch" has no group in the
// pattern; Radarr checks it anyway and it never succeeds.
var caseInsensitiveGroups = []groupLanguage{
	{"english", LanguageEnglish},
	{"italian", LanguageItalian},
	{"german", LanguageGerman},
	{"flemish", LanguageFlemish},
	{"greek", LanguageGreek},
	{"french", LanguageFrench},
	{"russian", LanguageRussian},
	{"bulgarian", LanguageBulgarian},
	{"brazilian", LanguagePortugueseBR},
	{"dutch", LanguageDutch},
	{"hungarian", LanguageHungarian},
	{"hebrew", LanguageHebrew},
	{"polish", LanguagePolish},
	{"chinese", LanguageChinese},
	{"spanish", LanguageSpanish},
	{"catalan", LanguageCatalan},
	{"ukrainian", LanguageUkrainian},
	{"latvian", LanguageLatvian},
	{"romanian", LanguageRomanian},
	{"telugu", LanguageTelugu},
	{"vietnamese", LanguageVietnamese},
	{"japanese", LanguageJapanese},
	{"korean", LanguageKorean},
	{"urdu", LanguageUrdu},
	{"romansh", LanguageRomansh},
	{"mongolian", LanguageMongolian},
	{"georgian", LanguageGeorgian},
	{"original", LanguageOriginal},
}

// ParseLanguages is LanguageParser.ParseLanguages.
func ParseLanguages(title string) []Language {
	lowerTitle := strings.ToLower(title)
	var languages []Language

	for _, w := range languageWords {
		for _, word := range w.words {
			if strings.Contains(lowerTitle, word) {
				languages = append(languages, w.lang)
				break
			}
		}
	}

	// Case-sensitive
	for _, m := range matches(caseSensitiveLanguageRegex, title) {
		for _, g := range caseSensitiveGroups {
			if ok(m, g.group) {
				languages = append(languages, g.lang)
			}
		}
	}

	// Case-insensitive
	for _, m := range matches(languageRegex, title) {
		for _, g := range caseInsensitiveGroups {
			if ok(m, g.group) {
				languages = append(languages, g.lang)
			}
		}
	}

	if len(languages) == 0 {
		languages = append(languages, LanguageUnknown)
	}

	if len(languages) == 1 && languages[0].ID == LanguageGerman.ID {
		if isMatch(germanDualLanguageRegex, title) {
			languages = append(languages, LanguageOriginal)
		} else if isMatch(germanMultiLanguageRegex, title) {
			languages = append(languages, LanguageOriginal, LanguageEnglish)
		}
	}

	return distinctLanguages(languages)
}

func distinctLanguages(in []Language) []Language {
	seen := map[int]bool{}
	out := make([]Language, 0, len(in))
	for _, l := range in {
		if !seen[l.ID] {
			seen[l.ID] = true
			out = append(out, l)
		}
	}
	return out
}

// ParseLanguageTags is LanguageParser.ParseLanguageTags.
func ParseLanguageTags(fileName string) []string {
	simple := pathGetFileNameWithoutExtension(fileName)
	m := match(subtitleLanguageRegex, simple)
	tags := []string{}
	for _, t := range captures(m, "tags") {
		if t != "" {
			tags = append(tags, strings.ToLower(t))
		}
	}
	return tags
}

// ParseSubtitleLanguage is LanguageParser.ParseSubtitleLanguage.
func ParseSubtitleLanguage(fileName string) Language {
	simple := pathGetFileNameWithoutExtension(fileName)
	if m := match(subtitleLanguageRegex, simple); m != nil {
		iso := FindIsoLanguage(strings.ToLower(value(m, "iso_code")))
		if iso != nil {
			return iso.Language
		}
		return LanguageUnknown
	}

	for _, l := range AllLanguages {
		if strings.HasSuffix(strings.ToLower(simple), strings.ToLower(l.Name)) {
			return l
		}
	}

	return LanguageUnknown
}

// SubtitleTitleInfo is Radarr's SubtitleTitleInfo.
type SubtitleTitleInfo struct {
	LanguageTags []string `json:"languageTags"`
	Language     Language `json:"language"`
	RawTitle     *string  `json:"rawTitle,omitempty"`
	Title        *string  `json:"title,omitempty"`
	Copy         int      `json:"copy"`
	TitleFirst   bool     `json:"titleFirst"`
}

// ParseBasicSubtitle is LanguageParser.ParseBasicSubtitle.
func ParseBasicSubtitle(fileName string) SubtitleTitleInfo {
	return SubtitleTitleInfo{
		TitleFirst:   false,
		LanguageTags: ParseLanguageTags(fileName),
		Language:     ParseSubtitleLanguage(fileName),
	}
}

// ParseSubtitleLanguageInformation is LanguageParser.ParseSubtitleLanguageInformation.
func ParseSubtitleLanguageInformation(fileName string) SubtitleTitleInfo {
	simple := pathGetFileNameWithoutExtension(fileName)
	m := match(subtitleLanguageTitleRegex, simple)

	if !ok(m, "title") || len(captures(m, "iso_code")) != 1 {
		return ParseBasicSubtitle(fileName)
	}

	iso := FindIsoLanguage(strings.ToLower(value(m, "iso_code")))
	language := LanguageUnknown
	if iso != nil {
		language = iso.Language
	}

	tags := []string{}
	for _, t := range append(captures(m, "tags1"), captures(m, "tags2")...) {
		if t != "" {
			tags = append(tags, strings.ToLower(t))
		}
	}
	raw := value(m, "title")

	info := SubtitleTitleInfo{
		TitleFirst:   len(captures(m, "tags1")) == 0,
		LanguageTags: tags,
		RawTitle:     &raw,
		Language:     language,
	}

	UpdateTitleAndCopyFromTitle(&info)

	return info
}

// UpdateTitleAndCopyFromTitle is LanguageParser.UpdateTitleAndCopyFromTitle.
func UpdateTitleAndCopyFromTitle(info *SubtitleTitleInfo) {
	if info.RawTitle == nil {
		info.Title = nil
		info.Copy = 0
		return
	}
	if m := match(subtitleTitleRegex, *info.RawTitle); m != nil {
		if t, s := group(m, "title"); s {
			info.Title = &t
		} else {
			info.Title = nil
		}
		info.Copy, _ = strconv.Atoi(value(m, "copy"))
		return
	}
	t := *info.RawTitle
	info.Title = &t
	info.Copy = 0
}
