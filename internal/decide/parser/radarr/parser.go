// Port of Radarr src/NzbDrone.Core/Parser/{Parser,SceneChecker}.cs and Parser/Model/ParsedMovieInfo.cs @ a96bf7c.

package radarr

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/dlclark/regexp2"
)

const editionPattern = `\(?\b(?<edition>(((Recut.|Extended.|Ultimate.)?(Director.?s|Collector.?s|Theatrical|Ultimate|Extended|Despecialized|(Special|Rouge|Final|Assembly|Imperial|Diamond|Signature|Hunter|Rekall)(?=(.(Cut|Edition|Version)))|\d{2,3}(th)?.Anniversary)(?:.(Cut|Edition|Version))?(.(Extended|Uncensored|Remastered|Unrated|Uncut|Open.?Matte|IMAX|Fan.?Edit))?|((Uncensored|Remastered|Unrated|Uncut|Open?.Matte|IMAX|Fan.?Edit|Restored|((2|3|4)in1))))))\b\)?`

var (
	//lint:ignore U1000 kept to mirror Radarr's Parser.EditionRegex, which the parser itself never reads either
	editionRegex = re(editionPattern, optI)

	reportEditionRegex = re(`^.+?`+editionPattern, optI)

	hardcodedSubsRegex = re(`\b((?<hcsub>(\w+(?<!SOFT|MULTI|HORRIBLE)SUBS?))|(?<hc>(HC|SUBBED)))\b`, optI|optX)

	reportMovieTitleRegex = []*regexp2.Regexp{
		// Anime [Subgroup] with year explicitly in parentheses, e.g. [Sub] Title 1993 (1993) [VHS]
		// This must come before the generic anime+year regex to correctly handle titles that contain a year as part of the name
		re(`^(?:\[(?<subgroup>.+?)\][-_. ]?)(?<title>(?![(\[]).+?)\s*\((?<year>(1(8|9)|20)\d{2})\).*?(?<hash>\[\w{8}\])?(?:$|\.)`, optI),

		// Anime [Subgroup] and Year
		re(`^(?:\[(?<subgroup>.+?)\][-_. ]?)(?<title>(?![(\[]).+?)?(?:(?:[-_\W](?<![)\[!]))*(?<year>(1(8|9)|20)\d{2}(?!p|i|x|\d+|\]|\W\d+)))+.*?(?<hash>\[\w{8}\])?(?:$|\.)`, optI),

		// Anime [Subgroup] no year, versioned title, hash
		re(`^(?:\[(?<subgroup>.+?)\][-_. ]?)(?<title>(?![(\[]).+?)((v)(?:\d{1,2})(?:([-_. ])))(\[.*)?(?:[\[(][^])])?.*?(?<hash>\[\w{8}\])(?:$|\.)`, optI),

		// Anime [Subgroup] no year, info in double sets of brackets, hash
		re(`^(?:\[(?<subgroup>.+?)\][-_. ]?)(?<title>(?![(\[]).+?)(\[.*).*?(?<hash>\[\w{8}\])(?:$|\.)`, optI),

		// Anime [Subgroup] no year, info in parentheses or brackets, hash
		re(`^(?:\[(?<subgroup>.+?)\][-_. ]?)(?<title>(?![(\[]).+)(?:[\[(][^])]).*?(?<hash>\[\w{8}\])(?:$|\.)`, optI),

		// Some german or french tracker formats (missing year, ...) (Only applies to german and TrueFrench releases) - see ParserFixture for examples and tests - french removed as it broke all movies w/ french titles
		re(`^(?<title>(?![(\[]).+?)((\W|_))(`+editionPattern+`.{1,3})?(?:(?<!(19|20)\d{2}.*?)(?<!(?:Good|The)[_ .-])(German|TrueFrench))(.+?)(?=((19|20)\d{2}|$))(?<year>(19|20)\d{2}(?!p|i|\d+|\]|\W\d+))?(\W+|_|$)(?!\\)`, optI),

		// Special, Despecialized, etc. Edition Movies, e.g: Mission.Impossible.3.Special.Edition.2011
		re(`^(?<title>(?![(\[]).+?)?(?:(?:[-_\W](?<![)\[!]))*`+editionPattern+`.{1,3}(?<year>(1(8|9)|20)\d{2}(?!p|i|\d+|\]|\W\d+)))+(\W+|_|$)(?!\\)`, optI),

		// Normal movie format, e.g: Mission.Impossible.3.2011
		re(`^(?<title>(?![(\[]).+?)?(?:(?:[-_\W](?<![)\[!]))*(?<year>(1(8|9)|20)\d{2}(?!p|i|(1(8|9)|20)\d{2}|\]|\W(1(8|9)|20)\d{2})))+(\W+|_|$)(?!\\)`, optI),

		// PassThePopcorn Torrent names: Star.Wars[PassThePopcorn]
		re(`^(?<title>.+?)?(?:(?:[-_\W](?<![()\[!]))*(?<year>(\[\w *\])))+(\W+|_|$)(?!\\)`, optI),

		// That did not work? Maybe some tool uses [] for years. Who would do that?
		re(`^(?<title>(?![(\[]).+?)?(?:(?:[-_\W](?<![)!]))*(?<year>(1(8|9)|20)\d{2}(?!p|i|\d+|\W\d+)))+(\W+|_|$)(?!\\)`, optI),

		// As a last resort for movies that have ( or [ in their title.
		re(`^(?<title>.+?)?(?:(?:[-_\W](?<![)\[!]))*(?<year>(1(8|9)|20)\d{2}(?!p|i|\d+|\]|\W\d+)))+(\W+|_|$)(?!\\)`, optI),
	}

	reportMovieTitleFolderRegex = []*regexp2.Regexp{
		// When year comes first.
		re(`^(?:(?:[-_\W](?<![)!]))*(?<year>(19|20)\d{2}(?!p|i|\d+|\W\d+)))+(\W+|_|$)(?<title>.+?)?$`, 0),
	}

	rejectHashedReleasesRegex = []*regexp2.Regexp{
		// Generic match for md5 and mixed-case hashes.
		re(`^[0-9a-zA-Z]{32}`, 0),

		// Generic match for shorter lower-case hashes.
		re(`^[a-z0-9]{24}$`, 0),

		// Format seen on some NZBGeek releases
		// Be very strict with these coz they are very close to the valid 101 ep numbering.
		re(`^[A-Z]{11}\d{3}$`, 0),
		re(`^[a-z]{12}\d{3}$`, 0),

		// Backup filename (Unknown origins)
		re(`^Backup_\d{5,}S\d{2}-\d{2}$`, 0),

		// 123 - Started appearing December 2014
		re(`^123$`, 0),

		// abc - Started appearing January 2015
		re(`^abc$`, optI),

		// abc - Started appearing 2020
		re(`^abc[-_. ]xyz`, optI),

		// b00bs - Started appearing January 2015
		re(`^b00bs$`, optI),
	}

	// Regex to detect whether the title was reversed.
	reversedTitleRegex = re(`(?:^|[-._ ])(p027|p0801)[-._ ]`, 0)

	// Regex to split movie titles that contain `AKA`.
	alternativeTitleRegex = re(`[ ]+(?:AKA|\/)[ ]+`, optI)

	// Regex to unbracket alternative titles.
	bracketedAlternativeTitleRegex = re(`(.*) \([ ]*AKA[ ]+(.*)\)`, optI)

	normalizeAlternativeTitleRegex = re(`[ ]+(?:A\.K\.A\.)[ ]+`, optI)

	normalizeRegex = re(`((?:\b|_)(?<!^|[^a-zA-Z0-9_']\w[^a-zA-Z0-9_'])([aà](?!$|[^a-zA-Z0-9_']\w[^a-zA-Z0-9_'])|an|the|and|or|of)(?!$)(?:\b|_))|\W|_`, optI)

	reportImdbID = re(`(?<imdbid>tt\d{7,8})`, optI)
	reportTmdbID = re(`tmdb(id)?-(?<tmdbid>\d+)`, optI)

	simpleTitleRegex = newRegexReplace(`(?:(480|540|576|720|1080|2160)[ip]|[xh][\W_]?26[45]|DD\W?5\W1|[<>?*]|848x480|1280x720|1920x1080|3840x2160|4096x2160|(8|10)b(it)?|10-bit)\s*?(?![a-b0-9])`, "", optI)

	simpleReleaseTitleRegex = re(`\s*(?:[<>?*|])`, optI)

	cleanQualityBracketsRegex = re(`\[[a-z0-9 ._-]+\]$`, optI)

	specialCharRegex        = re(`(\&|\:|\\|\/)+`, 0)
	punctuationRegex        = re(`[^\w\s]`, 0)
	articleWordRegex        = re(`^(a|an|the)\s`, optI)
	specialEpisodeWordRegex = re(`\b(part|special|edition|christmas)\b\s?`, optI)
	duplicateSpacesRegex    = re(`\s{2,}`, 0)

	requestInfoRegex = re(`^(?:\[.+?\])+`, 0)

	multiRegex = re(`[_. ](?<multi>multi)[_. ]`, optI)

	normalizeImdbIDRegex = re(`^(\d{1,10}|(tt)\d{1,10})$`, 0)
)

// ParsedMovieInfo is Radarr's ParsedMovieInfo. It marshals like the parsedMovieInfo object of
// GET /api/v3/parse, with the computed movieTitle and primaryMovieTitle. Only the fields that the
// parser can leave null are pointers.
type ParsedMovieInfo struct {
	MovieTitles        []string     `json:"movieTitles"`
	OriginalTitle      string       `json:"originalTitle"`
	ReleaseTitle       string       `json:"releaseTitle"`
	SimpleReleaseTitle string       `json:"simpleReleaseTitle"`
	Quality            QualityModel `json:"quality"`
	Languages          []Language   `json:"languages"`
	ReleaseGroup       *string      `json:"releaseGroup,omitempty"`
	ReleaseHash        string       `json:"releaseHash"`
	Edition            string       `json:"edition"`
	Year               int          `json:"year"`
	ImdbID             string       `json:"imdbId"`
	TmdbID             int          `json:"tmdbId"`
	HardcodedSubs      *string      `json:"hardcodedSubs,omitempty"`
}

// PrimaryMovieTitle is ParsedMovieInfo.PrimaryMovieTitle: the first title, or nil.
func (p *ParsedMovieInfo) PrimaryMovieTitle() *string {
	if len(p.MovieTitles) > 0 {
		return &p.MovieTitles[0]
	}
	return nil
}

// MovieTitle is ParsedMovieInfo.MovieTitle, the same as PrimaryMovieTitle.
func (p *ParsedMovieInfo) MovieTitle() *string { return p.PrimaryMovieTitle() }

// MarshalJSON adds the computed properties, as System.Text.Json does for get-only properties.
func (p ParsedMovieInfo) MarshalJSON() ([]byte, error) {
	type plain ParsedMovieInfo
	titles := p.MovieTitles
	if titles == nil {
		titles = []string{}
	}
	langs := p.Languages
	if langs == nil {
		langs = []Language{}
	}
	p.MovieTitles, p.Languages = titles, langs
	return json.Marshal(struct {
		plain
		MovieTitle        *string `json:"movieTitle,omitempty"`
		PrimaryMovieTitle *string `json:"primaryMovieTitle,omitempty"`
	}{plain(p), p.MovieTitle(), p.PrimaryMovieTitle()})
}

// ParseMoviePath is Parser.ParseMoviePath: the file name, then directory and file name together,
// then the directory name with the file's extension.
func ParseMoviePath(path string) *ParsedMovieInfo {
	name := pathGetFileName(path)
	dir := pathDirectoryName(path)

	result := ParseMovieTitle(name, true)

	if result == nil {
		result = ParseMovieTitle(dir+" "+name, false)
	}

	if result == nil {
		result = ParseMovieTitle(dir+pathGetExtension(name), false)
	}

	return result
}

// ParseMovieTitle is Parser.ParseMovieTitle. It returns nil when the title does not parse.
func ParseMovieTitle(title string, isDir bool) *ParsedMovieInfo {
	originalTitle := title

	if !validateBeforeParsing(title) {
		return nil
	}

	if isMatch(reversedTitleRegex, title) {
		// Radarr interpolates the reversed char[] into a string, which yields its type name; the
		// reversal never reaches the title. Kept as is (see the package doc).
		withoutExt := []rune(RemoveFileExtension(title))
		title = "System.Char[]" + string([]rune(title)[len(withoutExt):])
	}

	releaseTitle := RemoveFileExtension(title)

	// Trim dashes from end
	releaseTitle = trimChars(releaseTitle, "-_")

	releaseTitle = strings.ReplaceAll(strings.ReplaceAll(releaseTitle, "【", "["), "】", "]")

	for _, r := range preSubstitutionRegex {
		releaseTitle = r.Replace(releaseTitle)
	}

	simpleTitle := simpleTitleRegex.Replace(releaseTitle)

	// TODO: Quick fix stripping [url] - prefixes.
	simpleTitle = websitePrefixRegex.Replace(simpleTitle)
	simpleTitle = websitePostfixRegex.Replace(simpleTitle)

	simpleTitle = cleanTorrentSuffixRegex.Replace(simpleTitle)

	simpleTitle = replaceFunc(cleanQualityBracketsRegex, simpleTitle, func(m regexp2.Match) string {
		if ParseQualityName(m.String()).Quality.ID != QualityUnknown.ID {
			return ""
		}
		return m.String()
	})

	allRegexes := reportMovieTitleRegex
	if isDir {
		allRegexes = append(append([]*regexp2.Regexp{}, reportMovieTitleRegex...), reportMovieTitleFolderRegex...)
	}

	for _, regex := range allRegexes {
		m := match(regex, simpleTitle)
		if m == nil {
			continue
		}

		result := parseMovieMatch(m)
		if result == nil {
			continue
		}

		// TODO: Add tests for this!
		simpleReleaseTitle := replace(simpleReleaseTitleRegex, releaseTitle, "")

		titleGroup := m.GroupByName("title")
		titleOK := titleGroup != nil && len(titleGroup.Captures) > 0
		var replaceString string
		if titleOK {
			replaceString = titleGroup.String()
		} else if p := result.PrimaryMovieTitle(); p != nil {
			replaceString = *p
		}

		if !isNullOrWhiteSpace(replaceString) {
			placeholder := "A Movie"
			if strings.Contains(replaceString, ".") {
				placeholder = "A.Movie"
			}
			if titleOK {
				// The title group's position in simpleTitle is applied to simpleReleaseTitle, as
				// Radarr does. String.Remove throws when it runs past the end, which makes Radarr's
				// outer catch return null.
				runes := []rune(simpleReleaseTitle)
				idx, n := titleGroup.Index, titleGroup.Length
				if idx+n > len(runes) {
					return nil
				}
				simpleReleaseTitle = string(runes[:idx]) + placeholder + string(runes[idx+n:])
			} else {
				simpleReleaseTitle = strings.ReplaceAll(simpleReleaseTitle, replaceString, placeholder)
			}
		}

		result.ReleaseGroup = ParseReleaseGroup(simpleReleaseTitle)

		if subGroup := value(m, "subgroup"); !isNullOrWhiteSpace(subGroup) {
			result.ReleaseGroup = &subGroup
		}

		result.HardcodedSubs = ParseHardcodeSubs(title)

		languageTitle := simpleReleaseTitle
		if result.ReleaseGroup != nil && !isNullOrWhiteSpace(*result.ReleaseGroup) {
			languageTitle = strings.ReplaceAll(simpleReleaseTitle, *result.ReleaseGroup, "RlsGrp")
		}
		result.Languages = ParseLanguages(languageTitle)

		result.Quality = ParseQuality(title)

		if isNullOrWhiteSpace(result.Edition) {
			result.Edition = ParseEdition(simpleReleaseTitle)
		}

		result.ReleaseHash = getReleaseHash(m)

		result.OriginalTitle = originalTitle
		result.ReleaseTitle = releaseTitle
		result.SimpleReleaseTitle = simpleReleaseTitle

		result.ImdbID = ParseImdbID(simpleReleaseTitle)
		result.TmdbID = ParseTmdbID(simpleReleaseTitle)

		return result
	}

	return nil
}

// ParseImdbID is Parser.ParseImdbId.
func ParseImdbID(title string) string {
	if m := match(reportImdbID, title); m != nil {
		v := value(m, "imdbid")
		if n := len([]rune(v)); n == 9 || n == 10 {
			return v
		}
	}
	return ""
}

// ParseTmdbID is Parser.ParseTmdbId.
func ParseTmdbID(title string) int {
	if m := match(reportTmdbID, title); m != nil {
		if n, ok := tryParseInt(value(m, "tmdbid"), 32); ok {
			return int(n)
		}
	}
	return 0
}

// ParseEdition is Parser.ParseEdition.
func ParseEdition(languageTitle string) string {
	if m := match(reportEditionRegex, languageTitle); m != nil {
		if v := value(m, "edition"); !isNullOrWhiteSpace(v) {
			return strings.ReplaceAll(v, ".", " ")
		}
	}
	return ""
}

var umlautReplacer = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "Ä", "Ae", "Ö", "Oe", "Ü", "Ue", "ß", "ss")

// ReplaceGermanUmlauts is Parser.ReplaceGermanUmlauts.
func ReplaceGermanUmlauts(s string) string {
	return umlautReplacer.Replace(s)
}

// NormalizeImdbID is Parser.NormalizeImdbId. It returns nil for an invalid id.
func NormalizeImdbID(imdbID string) *string {
	if !isMatch(normalizeImdbIDRegex, imdbID) {
		return nil
	}

	if len([]rune(imdbID)) > 2 {
		id := strings.ReplaceAll(imdbID, "tt", "")
		if n := len([]rune(id)); n < 7 {
			id = strings.Repeat("0", 7-n) + id
		}
		s := "tt" + id
		return &s
	}

	return nil
}

var (
	slugSpaceRegex   = re(`\s`, 0)
	slugInvalidRegex = re(`[^a-z0-9\s-_]`, 0)
)

// ToURLSlug is Parser.ToUrlSlug. A nil trimEndChars or deduplicateChars is .NET's null.
func ToURLSlug(value string, invalidDashReplacement bool, trimEndChars, deduplicateChars *string) string {
	// First to lower case
	value = toLowerInvariant(value)

	// Remove all accents
	value = removeAccent(value)

	// Replace spaces
	value = replace(slugSpaceRegex, value, "-")

	// Should invalid characters be replaced with dash or empty string?
	replaceCharacter := ""
	if invalidDashReplacement {
		replaceCharacter = "-"
	}

	// Remove invalid chars
	value = replace(slugInvalidRegex, value, replaceCharacter)

	// Trim dashes or underscores from end, or user defined character set
	if trimEndChars != nil && *trimEndChars != "" {
		value = trimChars(value, *trimEndChars)
	}

	// Replace double occurrences of - or _, or user defined character set
	if deduplicateChars != nil && *deduplicateChars != "" {
		value = replace(re(`([`+*deduplicateChars+`]){2,}`, 0), value, "$1")
	}

	return value
}

// DefaultSlugChars is the default trimEndChars and deduplicateChars of ToUrlSlug.
const DefaultSlugChars = "-_"

// CleanMovieTitle is Parser.CleanMovieTitle.
func CleanMovieTitle(title string) string {
	if isNullOrWhiteSpace(title) {
		return title
	}

	// If Title only contains numbers return it as is.
	if _, ok := tryParseInt(title, 64); ok {
		return title
	}

	return removeAccent(ReplaceGermanUmlauts(toLowerInvariant(replace(normalizeRegex, title, ""))))
}

// NormalizeMovieTitle is Parser.NormalizeMovieTitle.
func NormalizeMovieTitle(title string) string {
	title = replace(specialEpisodeWordRegex, title, "")
	title = replace(punctuationRegex, title, " ")
	title = replace(duplicateSpacesRegex, title, " ")

	return strings.ToLower(netTrim(title))
}

// NormalizeTitle is Parser.NormalizeTitle.
func NormalizeTitle(title string) string {
	title = replace(punctuationRegex, title, "")
	title = replace(articleWordRegex, title, "")
	title = replace(duplicateSpacesRegex, title, " ")
	title = replace(specialCharRegex, title, "")

	return strings.ToLower(netTrim(title))
}

// SimplifyReleaseTitle is Parser.SimplifyReleaseTitle.
func SimplifyReleaseTitle(title string) string {
	return replace(simpleReleaseTitleRegex, title, "")
}

// ParseHardcodeSubs is Parser.ParseHardcodeSubs. It returns nil when there are none.
func ParseHardcodeSubs(title string) *string {
	m := lastMatch(matches(hardcodedSubsRegex, title))
	if m == nil {
		return nil
	}
	if v, s := group(m, "hcsub"); s {
		return &v
	}
	if ok(m, "hc") {
		return strPtr("Generic Hardcoded Subs")
	}
	return nil
}

// HasMultipleLanguages is Parser.HasMultipleLanguages.
func HasMultipleLanguages(title string) bool {
	return isMatch(multiRegex, title)
}

func parseMovieMatch(m *regexp2.Match) *ParsedMovieInfo {
	titleValue, titleOK := group(m, "title")
	if !titleOK || titleValue == "(" {
		return nil
	}

	movieName := strings.ReplaceAll(titleValue, "_", " ")
	movieName = replace(normalizeAlternativeTitleRegex, movieName, " AKA ")
	movieName = trimChars(replace(requestInfoRegex, movieName, ""), " ")

	parts := strings.Split(movieName, ".")
	movieName = ""
	previousAcronym := false
	for n, part := range parts {
		nextPart := ""
		if len(parts) >= n+2 {
			nextPart = parts[n+1]
		}

		partLen := len([]rune(part))
		nextLen := len([]rune(nextPart))
		lower := toLowerInvariant(part)

		switch {
		case partLen == 1 && lower != "a" && !isInt32(part) &&
			(previousAcronym || n < len(parts)-1) &&
			(previousAcronym || nextLen != 1 || !isInt32(nextPart)):
			movieName += part + "."
			previousAcronym = true
		case lower == "a" && (previousAcronym || nextLen == 1):
			movieName += part + "."
			previousAcronym = true
		case lower == "dr":
			movieName += part + "."
			previousAcronym = true
		default:
			if previousAcronym {
				movieName += " "
				previousAcronym = false
			}
			movieName += part + " "
		}
	}

	movieName = trimChars(movieName, " ")

	year, _ := tryParseInt(value(m, "year"), 32)

	result := &ParsedMovieInfo{Year: int(year)}

	if v, s := group(m, "edition"); s {
		result.Edition = strings.ReplaceAll(v, ".", " ")
	}

	movieTitles := []string{movieName}

	// Delete parentheses of the form (aka ...).
	unbracketedName := replace(bracketedAlternativeTitleRegex, movieName, "$1 AKA $2")

	// Split by AKA and filter out empty and duplicate names.
	for _, alt := range split(alternativeTitleRegex, unbracketedName) {
		if !isNullOrWhiteSpace(alt) && alt != movieName {
			movieTitles = append(movieTitles, alt)
		}
	}

	result.MovieTitles = movieTitles

	return result
}

func validateBeforeParsing(title string) bool {
	lower := strings.ToLower(title)
	if strings.Contains(lower, "password") && strings.Contains(lower, "yenc") {
		return false
	}

	hasLetterOrDigit := false
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.Is(unicode.Nd, r) {
			hasLetterOrDigit = true
			break
		}
	}
	if !hasLetterOrDigit {
		return false
	}

	withoutExt := RemoveFileExtension(title)
	for _, r := range rejectHashedReleasesRegex {
		if isMatch(r, withoutExt) {
			return false
		}
	}

	return true
}

func getReleaseHash(m *regexp2.Match) string {
	if v, s := group(m, "hash"); s {
		h := trimChars(v, "[]")
		if h == "1280x720" {
			return ""
		}
		return h
	}
	return ""
}

// toLowerInvariant is String.ToLowerInvariant.
func toLowerInvariant(s string) string { return strings.ToLower(s) }

// GetSceneTitle is SceneChecker.GetSceneTitle. It prefers false negatives over false positives:
// better not to use a title that might be scene than to use one that isn't.
func GetSceneTitle(title string) *string {
	if !strings.Contains(title, ".") {
		return nil
	}

	if strings.Contains(title, " ") {
		return nil
	}

	parsed := ParseMovieTitle(title, false)

	if parsed == nil ||
		parsed.ReleaseGroup == nil ||
		parsed.Quality.Quality.ID == QualityUnknown.ID ||
		parsed.PrimaryMovieTitle() == nil || isNullOrWhiteSpace(*parsed.PrimaryMovieTitle()) ||
		isNullOrWhiteSpace(parsed.ReleaseTitle) {
		return nil
	}

	rt := parsed.ReleaseTitle
	return &rt
}

// IsSceneTitle is SceneChecker.IsSceneTitle.
func IsSceneTitle(title string) bool {
	return GetSceneTitle(title) != nil
}
