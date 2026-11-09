package sonarr

// Port of Sonarr src/NzbDrone.Core/Parser/LanguageParser.cs and Parser/Model/SubtitleTitleInfo.cs @ cab419a.

import (
	"strconv"
	"strings"

	"github.com/dlclark/regexp2"
)

var (
	languageCleanSeriesTitleRegex = []regexReplace{
		newRegexReplace(`.*?[_. ](S\d{2}(?:E\d{2,4})*[_. ].*)`, "$1", optI),
	}

	languageRegex = re(`(?:\W|_)(?<english>\b(?:ing|eng)\b)|(?<italian>\b(?:ita|italian)\b)|(?<german>german\b|videomann|ger[. ]dub)|(?<flemish>flemish)|(?<greek>greek)|(?<french>(?:\W|_)(?:FR|VF|VF2|VFF|VFI|VFQ|TRUEFRENCH|FRENCH)(?:\W|_))|(?<russian>\b(?:rus|ru)\b)|(?<hungarian>\b(?:HUNDUB|HUN)\b)|(?<hebrew>\bHebDub\b)|(?<polish>\b(?:PL\W?DUB|DUB\W?PL|LEK\W?PL|PL\W?LEK)\b)|(?<chinese>\[(?:CH[ST]|BIG5|GB)\]|简|繁|字幕)|(?<bulgarian>\bbgaudio\b)|(?<spanish>\b(?:español|castellano|esp|spa(?!\(Latino\)))\b)|(?<ukrainian>\b(?:\dx?)?(?:ukr))|(?<thai>\b(?:THAI)\b)|(?<romanian>\b(?:RoDubbed|ROMANIAN)\b)|(?<catalan>[-,. ]cat[. ](?:DD|subs)|\b(?:catalan|catalán)\b)|(?<latvian>\b(?:lat|lav|lv)\b)|(?<turkish>\b(?:tur)\b)|(?<original>\b(?:orig|original)\b)`, optI)

	caseSensitiveLanguageRegex = re(`(?:(?i)(?<!SUB[\W|_|^]))(?:(?<lithuanian>\bLT\b)|(?<czech>\bCZ\b)|(?<polish>\bPL\b)|(?<bulgarian>\bBG\b)|(?<slovak>\bSK\b))(?:(?i)(?![\W|_|^]SUB))`, optNone)

	germanDualLanguageRegex  = re(`(?<!WEB[-_. ]?)\bDL\b`, optI)
	germanMultiLanguageRegex = re(`\bML\b`, optI)

	subtitleLanguageRegex = re(`.+?([-_. ](?<tags>forced|foreign|default|cc|psdh|sdh))*[-_. ](?<iso_code>[a-z]{2,3})([-_. ](?<tags>forced|foreign|default|cc|psdh|sdh))*$`, optI)

	subtitleLanguageTitleRegex = re(`.+?(\.((?<tags1>forced|foreign|default|cc|psdh|sdh)|(?<iso_code>[a-z]{2,3})))*[-_. ](?<title>[^.]*)(\.((?<tags2>forced|foreign|default|cc|psdh|sdh)|(?<iso_code>[a-z]{2,3})))*$`, optI)

	subtitleTitleRegex = re(`^((?<title>.+) - )?(?<copy>(?<!\d+)\d{1,3}(?!\d+))$`, optNone)
)

// ParseLanguages is LanguageParser.ParseLanguages. The result is never empty.
func ParseLanguages(title string) []Language { return v4d.parseLanguages(title) }

func (d *dialect) parseLanguages(title string) []Language {
	for _, r := range languageCleanSeriesTitleRegex {
		if r.TryReplace(&title) {
			break
		}
	}

	lowerTitle := strings.ToLower(title)

	var languages []Language
	add := func(cond bool, l Language) {
		if cond {
			languages = append(languages, l)
		}
	}
	has := func(s string) bool { return strings.Contains(lowerTitle, s) }

	add(has("spanish"), LanguageSpanish)
	add(has("danish"), LanguageDanish)
	add(has("dutch"), LanguageDutch)
	add(has("japanese"), LanguageJapanese)
	add(has("icelandic"), LanguageIcelandic)
	add(has("mandarin") || has("cantonese") || has("chinese"), LanguageChinese)
	add(has("korean"), LanguageKorean)
	add(has("russian"), LanguageRussian)
	add(has("polish"), LanguagePolish)
	add(has("vietnamese"), LanguageVietnamese)
	add(has("swedish"), LanguageSwedish)
	add(has("norwegian"), LanguageNorwegian)
	add(has("finnish"), LanguageFinnish)
	add(has("turkish"), LanguageTurkish)
	add(has("portuguese"), LanguagePortuguese)
	add(has("hungarian"), LanguageHungarian)
	add(has("hebrew"), LanguageHebrew)
	add(has("arabic"), LanguageArabic)
	add(has("hindi"), LanguageHindi)
	add(has("malayalam"), LanguageMalayalam)
	add(has("ukrainian"), LanguageUkrainian)
	add(has("bulgarian"), LanguageBulgarian)
	add(has("slovak"), LanguageSlovak)
	add(has("brazilian") || has("dublado"), LanguagePortugueseBrazil)
	add(has("latino"), LanguageSpanishLatino)
	add(has("latvian"), LanguageLatvian)
	if d.v5 {
		add(has("azerbaijani") || has("azerbaijan"), LanguageAzerbaijani)
		add(has("uzbek") || has("uzbekistan"), LanguageUzbek)
	}

	languages = append(languages, d.regexLanguage(title)...)

	add(has("english"), LanguageEnglish)

	if len(languages) == 0 {
		languages = append(languages, LanguageUnknown)
	}

	if len(languages) == 1 && languages[0] == LanguageGerman {
		if isMatch(germanDualLanguageRegex, title) {
			languages = append(languages, LanguageOriginal)
		} else if isMatch(germanMultiLanguageRegex, title) {
			languages = append(languages, LanguageOriginal, LanguageEnglish)
		}
	}

	// DistinctBy(l => (int)l)
	seen := map[int]bool{}
	out := make([]Language, 0, len(languages))
	for _, l := range languages {
		if !seen[l.ID] {
			seen[l.ID] = true
			out = append(out, l)
		}
	}
	return out
}

func (d *dialect) regexLanguage(title string) []Language {
	var languages []Language

	// Case sensitive
	cs := []*regexp2.Match{match(d.caseSensitiveLanguage, title)}
	if d.v4plus {
		cs = matches(d.caseSensitiveLanguage, title) // every match, not the first only (ours)
	}
	for _, m := range cs {
		for _, x := range d.caseSensitiveGroups {
			if success(m, x.group) {
				languages = append(languages, x.lang)
			}
		}
	}

	// Case insensitive. "dutch" is checked in Sonarr but the pattern has no such group, so it never matches.
	ms := matches(d.language, title)
	var subs []bool
	if d.v4plus {
		subs = subtitleMatches4p(title, ms)
	}
	for i, m := range ms {
		if subs != nil && subs[i] {
			continue // v4plus: a subtitle language
		}
		for _, x := range d.languageGroups {
			if success(m, x.group) {
				languages = append(languages, x.lang)
			}
		}
	}

	return languages
}

// SubtitleTitleInfo is Sonarr's SubtitleTitleInfo.
type SubtitleTitleInfo struct {
	LanguageTags []string `json:"languageTags"`
	Language     Language `json:"language"`
	RawTitle     *string  `json:"rawTitle,omitempty"`
	Title        *string  `json:"title,omitempty"`
	Copy         int      `json:"copy"`
	TitleFirst   bool     `json:"titleFirst"`
}

// languageGroup is one named group of a language pattern and the language it stands for.
type languageGroup struct {
	group string
	lang  Language
}

// The groups RegexLanguage checks, in its order (v4).
var (
	caseSensitiveGroups4 = []languageGroup{
		{"lithuanian", LanguageLithuanian},
		{"czech", LanguageCzech},
		{"polish", LanguagePolish},
		{"bulgarian", LanguageBulgarian},
		{"slovak", LanguageSlovak},
	}
	languageGroups4 = []languageGroup{
		{"english", LanguageEnglish},
		{"italian", LanguageItalian},
		{"german", LanguageGerman},
		{"flemish", LanguageFlemish},
		{"greek", LanguageGreek},
		{"french", LanguageFrench},
		{"russian", LanguageRussian},
		{"dutch", LanguageDutch},
		{"hungarian", LanguageHungarian},
		{"hebrew", LanguageHebrew},
		{"polish", LanguagePolish},
		{"chinese", LanguageChinese},
		{"bulgarian", LanguageBulgarian},
		{"ukrainian", LanguageUkrainian},
		{"spanish", LanguageSpanish},
		{"thai", LanguageThai},
		{"romanian", LanguageRomanian},
		{"catalan", LanguageCatalan},
		{"latvian", LanguageLatvian},
		{"turkish", LanguageTurkish},
		{"original", LanguageOriginal},
	}
)

// ParseSubtitleLanguage is LanguageParser.ParseSubtitleLanguage.
func ParseSubtitleLanguage(fileName string) Language {
	simpleFilename := pathFileNameWithoutExtension(fileName)
	m := match(subtitleLanguageRegex, simpleFilename)

	if m != nil {
		isoCode := value(m, "iso_code")
		if iso := FindIsoLanguage(strings.ToLower(isoCode)); iso != nil {
			return iso.Language
		}
		return LanguageUnknown
	}

	for _, l := range AllLanguages {
		if strings.HasSuffix(strings.ToLower(simpleFilename), strings.ToLower(l.Name)) {
			return l
		}
	}

	return LanguageUnknown
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
	simpleFilename := pathFileNameWithoutExtension(fileName)
	m := match(subtitleLanguageTitleRegex, simpleFilename)

	if !success(m, "title") || len(captures(m, "iso_code")) != 1 {
		return ParseBasicSubtitle(fileName)
	}

	isoCode := value(m, "iso_code")
	language := LanguageUnknown
	if iso := FindIsoLanguage(strings.ToLower(isoCode)); iso != nil {
		language = iso.Language
	}

	// tags1.Captures.Union(tags2.Captures): Capture has reference equality, so Union only drops nothing here.
	tags := []string{}
	for _, name := range []string{"tags1", "tags2"} {
		for _, c := range captures(m, name) {
			if v := c.String(); v != "" {
				tags = append(tags, strings.ToLower(v))
			}
		}
	}
	rawTitle := value(m, "title")

	info := SubtitleTitleInfo{
		TitleFirst:   len(captures(m, "tags1")) == 0,
		LanguageTags: tags,
		RawTitle:     &rawTitle,
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
	} else if m := match(subtitleTitleRegex, *info.RawTitle); m != nil {
		if success(m, "title") {
			t := value(m, "title")
			info.Title = &t
		} else {
			info.Title = nil
		}
		info.Copy, _ = strconv.Atoi(value(m, "copy"))
	} else {
		t := *info.RawTitle
		info.Title = &t
		info.Copy = 0
	}
}

// ParseLanguageTags is LanguageParser.ParseLanguageTags.
func ParseLanguageTags(fileName string) []string {
	simpleFilename := pathFileNameWithoutExtension(fileName)
	m := match(subtitleLanguageRegex, simpleFilename)
	tags := []string{}
	for _, c := range captures(m, "tags") {
		if v := c.String(); v != "" {
			tags = append(tags, strings.ToLower(v))
		}
	}
	return tags
}
