package sonarr

// V4Plus (the package's plain functions, what every series library parses with): Sonarr v4.0.20.3014 with the
// Sonarr v5 parser changes we chose one by one, and a few fixes of our own, measured on a corpus of real release titles.
// Everything not listed here is v4: the pattern order, the mapping, the languages (ids to 46), CleanSeriesTitle and
// the parse shape (SeasonNumber, no SeasonNumbers).
//
// | Change                                         | Source (ref/Sonarr_v5)               | Where                      |
// |------------------------------------------------|--------------------------------------|----------------------------|
// | Multi-episode anime ranges with a subgroup     | 91c4ae17c (#8948)                    | reportTitleRegex4p         |
// | GER, DE, Swiss German as German                | 9e5ebdc62, 030910bab                 | language, case-sensitive   |
// | FRA, FRE as French; FR/ENG next to each other  | 3e2e8e938, f055e8a3e                 | language                   |
// | JPN, JAP, (JA) as Japanese                     | dacd46962, b376b63c9; ours: \b       | language                   |
// | POR as Portuguese; 国语音轨 as Chinese          | a4e3be721, f2f4edad0                 | language                   |
// | A language right before SUBS is a subtitle    | 224e74605, e06c6ba64; ours: lists    | subtitleMatches4p          |
// | Languages after "[Multiple Subtitle]" are subs | ours                                 | subtitleMatches4p          |
// | EN, JA (upper case) as English, Japanese       | ours                                 | case-sensitive languages   |
// | Every case-sensitive language, not the first   | ours                                 | regexLanguage              |
// | GER only after a separator                     | ours                                 | language                   |
// | TAoE, QxR, Vialle as groups; encoders are not  | 83b2c9e97, b00229e53, 5eb18fe27      | exception group patterns   |
// | -GER/-FRA/-FRE/-ITA and 10-bit are not groups  | 9e5ebdc62, 3e2e8e938, d09e7893b, 350dea10d | releaseGroupRegex4p  |
// | WEB with EAC3 is WEB-DL                        | 6421bb621 (#8923)                    | sourceRegex4p              |
//
// Left out on purpose (each behind a regression on the corpus, or needing v5's mapping): the pattern order of
// 7b0db46c2 (E12.2020 read as S20E20), the seven new languages 47-53 (romaji "ka" read as Georgian, "Uzbekistan" as
// Uzbek), e06c6ba64's reading of a whole language list before SUBS as subtitles ("ITA ENG SUBS" lost Italian),
// v5's unbounded JAP/JPN ("Japan" read as Japanese), multi-season packs, season-title packs, partial seasons,
// original titles and diacritics (they need v5's mapping), and the rest of v5's table changes, which the corpus did
// not show to matter.

import (
	"fmt"
	"strings"

	"github.com/dlclark/regexp2"
)

// patched compiles r's pattern with old replaced by new, which must occur exactly once (a v4 pattern changed by one
// upstream commit).
func patched(r *regexp2.Regexp, opts regexp2.RegexOptions, old, new string) *regexp2.Regexp {
	s := r.String()
	if strings.Count(s, old) != 1 {
		panic(fmt.Sprintf("sonarr v4plus: %q is not in the v4 pattern exactly once", old))
	}
	return re(strings.Replace(s, old, new, 1), opts)
}

// reportTitleRegex4p is v4's ReportTitleRegex with 91c4ae17c: "Anime - [SubGroup] Title Season+Episode + Absolute
// Episode Number" split into a multi-episode pattern with optional absolute numbers and a single-episode one, so
// `[PMR] Title S03E01-12 (BD ...)` is S03E01-12 and not S01E12 with absolute 12. The hash keeps v4's \w{8} (v5
// changed it in 808d48361, not taken).
var reportTitleRegex4p = func() []*regexp2.Regexp {
	const v4 = `^(?:\[(?<subgroup>.+?)\](?:_|-|\s|\.)?)(?<title>.+?)(?:[-_\W](?<![()\[!]))+(?:S?(?<season>(?<!\d+)\d{1,2}(?!\d+))(?:(?:\-|[ex]|\W[ex]){1,2}(?<episode>\d{2}(?!\d+)))+)(?:(?:_|-|\s|\.)+(?<absoluteepisode>(?<!\d+)\d{2,3}(\.\d{1,2})?(?!\d+|\-[a-z])))+.*?(?<hash>[(\[]\w{8}[)\]])?$`
	var out []*regexp2.Regexp
	found := 0
	for _, r := range reportTitleRegex {
		if r.String() != v4 {
			out = append(out, r)
			continue
		}
		found++
		out = append(out,
			// Anime - [SubGroup] Title Multi-episode + optional Absolute Episode Number
			re(`^(?:\[(?<subgroup>.+?)\](?:_|-|\s|\.)?)(?<title>.+?)(?:[-_\W](?<![()\[!]))+(?:S?(?<season>(?<!\d+)\d{1,2}(?!\d+))(?:(?:\-|[ex]|\W[ex]){1,2}(?<episode>\d{2}(?!\d+))){2,})(?:(?:_|-|\s|\.)+(?<absoluteepisode>(?<!\d+)\d{2,3}(\.\d{1,2})?(?!\d+|\-[a-z])))*.*?(?<hash>[(\[]\w{8}[)\]])?$`, optI),
			// Anime - [SubGroup] Title Single episode + Absolute Episode Number
			re(`^(?:\[(?<subgroup>.+?)\](?:_|-|\s|\.)?)(?<title>.+?)(?:[-_\W](?<![()\[!]))+(?:S?(?<season>(?<!\d+)\d{1,2}(?!\d+))(?:(?:\-|[ex]|\W[ex]){1,2}(?<episode>\d{2}(?!\d+))))(?:(?:_|-|\s|\.)+(?<absoluteepisode>(?<!\d+)\d{2,3}(\.\d{1,2})?(?!\d+|\-[a-z])))+.*?(?<hash>[(\[]\w{8}[)\]])?$`, optI))
	}
	if found != 1 {
		panic("sonarr v4plus: the v4 anime Season+Episode + Absolute pattern was not found once")
	}
	return out
}()

var (
	// releaseGroupRegex4p is v4's ReleaseGroupRegex with -GER (9e5ebdc62), -FRA/-FRE (3e2e8e938), -ITA (d09e7893b)
	// and \d{1,2}-bit (350dea10d) in the look-behind: a language or a bit depth after a dash is not a group.
	releaseGroupRegex4p = patched(releaseGroupRegex, optI, `|-CAT|`, `|-CAT|-GER|-FRA|-FRE|-ITA|\d{1,2}-bit|`)

	// exceptionReleaseGroupRegexExact4p: TAoE and QxR (b00229e53) and Vialle (5eb18fe27) are groups.
	exceptionReleaseGroupRegexExact4p = patched(exceptionReleaseGroupRegexExact, optI, `|BEN[_. ]THE[_. ]MEN)`,
		`|BEN[_. ]THE[_. ]MEN|TAoE|QxR|Vialle)`)

	// exceptionReleaseGroupRegex4p: the encoders of TAoE and QxR releases (Silence, afm72, Panda, Ghost, MONOLITH,
	// Tigole, RZeroX) are no longer read as groups (b00229e53); `... Silence) [QxR]` is QxR.
	exceptionReleaseGroupRegex4p = re(`(?<=[._ \[])(?<releasegroup>(Joy|ImE|UTR|t3nzin|Anime Time|Project Angel|Hakata Ramen|HONE|Vyndros|SEV|Garshasp|Kappa|Natty|RCVR|SAMPA|YOGI|r00t|EDGE2020)(?=\]|\)))`, optI)

	// sourceRegex4p is v4's source pattern with EAC3 after [. ]WEB[. ] (6421bb621): `HDR.WEB.EAC3` is WEB-DL.
	sourceRegex4p = patched(sourceRegex, optI|optX, `|AVC|HEVC|DDP?5[. ]1)`, `|AVC|HEVC|EAC3|DDP?5[. ]1)`)

	// languageRegex4p is v5's LanguagesOnlyRegex (e06c6ba64 and the commits in the table above) without the seven
	// new languages and without the SUBS look-ahead (subtitleMatches4p does that check), with Japanese bounded
	// (v5's bare JAP|JPN matches "Japan"; ours: \b(?:JAP|JPN)\b) and GER after a separator (v5's \bger\b matches
	// the mojibake "ProtÃÂ©ger"; ours).
	languageRegex4p = re(`(?<english>\b(?:ing|eng)\b)|(?<italian>\b(?:ita|italian)\b)|(?<german>(?:swiss)?german\b|videomann|ger[. ]dub|(?<=^|[-_. \[(+,])ger\b)|(?<flemish>flemish)|(?<greek>greek)|(?<french>(?:_|\b)(?:FR|VF|VF2|VFF|VFI|VFQ|TRUEFRENCH|FRENCH|FRE|FRA)(?:_|\b))|(?<russian>\b(?:rus|ru)\b)|(?<hungarian>\b(?:HUNDUB|HUN)\b)|(?<hebrew>\bHebDub\b)|(?<polish>\b(?:PL\W?DUB|DUB\W?PL|LEK\W?PL|PL\W?LEK)\b)|(?<chinese>\[(?:CH[ST]|BIG5|GB)\]|简|繁|字幕|国语音轨[.+])|(?<bulgarian>\bbgaudio\b)|(?<spanish>\b(?:español|castellano|esp|spa(?!\(Latino\)))\b)|(?<ukrainian>\b(?:\dx?)?(?:ukr))|(?<thai>\b(?:THAI)\b)|(?<romanian>\b(?:RoDubbed|ROMANIAN)\b)|(?<catalan>[-,. ]cat[. ](?:DD|subs)|\b(?:catalan|catalán)\b)|(?<latvian>\b(?:lat|lav|lv)\b)|(?<turkish>\b(?:tur)\b)|(?<japanese>\(JA\)|\b(?:JAP|JPN)\b)|(?<portuguese>[_. ]por[_. ])|(?<original>\b(?:orig|original)\b)`, optI)

	// caseSensitiveLanguageRegex4p is v5's CaseSensitiveLanguageRegex (DE, 9e5ebdc62) with EN as English and JA as
	// Japanese (ours): the upper-case ISO 639-1 codes Sonarr's own {MediaInfo AudioLanguages} naming writes
	// (`[JA+EN]`) and TURG's `DE-EN-TR`. On the corpus every upper-case EN and JA word is such a tag.
	caseSensitiveLanguageRegex4p = re(`(?:(?i)(?<!SUB[\W|_|^]))(?:(?<lithuanian>\bLT\b)|(?<czech>\bCZ\b)|(?<polish>\bPL\b)|(?<bulgarian>\bBG\b)|(?<slovak>\bSK\b)|(?<german>\bDE\b)|(?<english>\bEN\b)|(?<japanese>\bJA\b))(?:(?i)(?![\W|_|^]SUB))`, optNone)

	// subsAfterRegex is what follows a subtitle language: v5's [-_. ]subs? look-ahead.
	subsAfterRegex = re(`^[-_. ]subs?`, optI)

	// subtitleListRegex marks a list of subtitle languages after it (ours): Erai-raws names its subtitles
	// `[Multiple Subtitle] [ENG][POR-BR][SPA-LA]...`, which v4 and v5 read as audio.
	subtitleListRegex = re(`\bMultiple[-_. ]Subtitles?\b`, optI)
)

// languageGroups4p is v4's groups with Japanese and Portuguese before Original, as v5 orders them.
var languageGroups4p = append(append([]languageGroup{}, languageGroups4[:len(languageGroups4)-1]...),
	languageGroup{"japanese", LanguageJapanese},
	languageGroup{"portuguese", LanguagePortuguese},
	languageGroups4[len(languageGroups4)-1])

// v4plusd is V4Plus: v4d with the tables above. The package's plain functions use it.
var v4plusd = func() *dialect {
	d := *v4d
	d.v4plus = true
	d.reportTitle = reportTitleRegex4p
	d.releaseGroup = releaseGroupRegex4p
	d.exceptionReleaseGroup = exceptionReleaseGroupRegex4p
	d.exceptionReleaseGroupExact = exceptionReleaseGroupRegexExact4p
	d.language = languageRegex4p
	d.caseSensitiveLanguage = caseSensitiveLanguageRegex4p
	d.languageGroups = languageGroups4p
	d.caseSensitiveGroups = append(append([]languageGroup{}, caseSensitiveGroups4...), languageGroup{"german", LanguageGerman},
		languageGroup{"english", LanguageEnglish}, languageGroup{"japanese", LanguageJapanese})
	d.source = sourceRegex4p
	return &d
}()

// subtitleMatches4p marks the language matches that are subtitles: a language directly followed by "sub"/"subs"
// (`[Eng-Subs]`, `ENG.SUBS`), as v5 reads it (224e74605), unless another language directly precedes it in a list.
// v5 (e06c6ba64) takes the whole list before SUBS as subtitles; on the corpus that list is the audio of Italian
// releases (`E-AC3+AC3 ITA ENG SUBS`), so a list keeps v4's reading (ours). Every language after a
// "Multiple Subtitle" marker is a subtitle too (ours).
func subtitleMatches4p(title string, ms []*regexp2.Match) []bool {
	runes := []rune(title)
	listFrom := len(runes) + 1
	if m := match(subtitleListRegex, title); m != nil {
		listFrom = m.Index + m.Length
	}
	type span struct{ start, end int }
	core := make([]span, len(ms))
	for i, m := range ms {
		s, e := m.Index, m.Index+m.Length
		for s < e && strings.ContainsRune("-_. ", runes[s]) {
			s++
		}
		for e > s && strings.ContainsRune("-_. ", runes[e-1]) {
			e--
		}
		core[i] = span{s, e}
	}
	subs := make([]bool, len(ms))
	for i := range ms {
		if core[i].start >= listFrom {
			subs[i] = true
			continue
		}
		if !isMatch(subsAfterRegex, string(runes[core[i].end:])) {
			continue
		}
		listed := i > 0 && core[i-1].end+1 == core[i].start && strings.ContainsRune("-_. ", runes[core[i-1].end])
		subs[i] = !listed
	}
	return subs
}
