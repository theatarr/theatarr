// Port of the helpers shared by Radarr and Sonarr src/NzbDrone.Core/Organizer/FileNameBuilder.cs (Radarr @ a96bf7c,
// Sonarr @ cab419a), FileNameBuilderTokenEqualityComparer.cs and NzbDrone.Core/Fluent.cs (Truncate).

package naming

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/dlclark/regexp2"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"

	"github.com/theatarr/theatarr/internal/netregex"
)

// Linux limits (NzbDrone.Common.Disk.LongPathSupport).
const (
	maxFileNameLength = 255
	maxFilePathLength = 4096
)

var (
	fileNameCleanupRegex = netregex.MustCompile(`([- ._])(\1)+`, netregex.None)
	trimSeparatorsRegex  = netregex.MustCompile(`[- ._]+$`, netregex.None)
	scenifyRemoveChars   = netregex.MustCompile(`(?<=\s)(,|<|>|\/|\\|;|:|'|"|\||`+"`"+`|’|~|!|\?|@|$|%|^|\*|-|_|=){1}(?=\s)|('|`+"`"+`|’|:|\?|,)(?=(?:(?:s|m|t|ve|ll|d|re)\s)|\s|$)|(\(|\)|\[|\]|\{|\})`, netregex.IgnoreCase)
	scenifyReplaceChars  = netregex.MustCompile(`[\/]`, netregex.IgnoreCase)
	titlePrefixRegex     = netregex.MustCompile(`^(The|An|A) (.*?)((?: *\([^)]+\))*)$`, netregex.IgnoreCase)
	reservedDeviceNames  = netregex.MustCompile(`^(?:aux|com[1-9]|con|lpt[1-9]|nul|prn)\.`, netregex.IgnoreCase)
	editionOrdinalRegex  = netregex.MustCompile(`((?:\b|_)\d{1,3}(?:st|th|rd|nd)(?:\b|_))`, netregex.IgnoreCase)
	editionUpperRegex    = netregex.MustCompile(`((?:\b|_)(?:IMAX|3D|SDR|HDR|DV)(?:\b|_))`, netregex.IgnoreCase)
)

var (
	badCharacters  = []string{"\\", "/", "<", ">", "?", "*", "|", "\""}
	goodCharacters = []string{"+", "+", "", "", "!", "-", "", ""}
)

// iso639BT maps ISO 639-2/B codes to /T (Radarr's table; Sonarr's lacks gsw, khk and mvf).
var iso639BT = map[string]string{"alb": "sqi", "arm": "hye", "baq": "eus", "bur": "mya", "chi": "zho", "cze": "ces",
	"dut": "nld", "fre": "fra", "geo": "kat", "ger": "deu", "gre": "ell", "gsw": "deu", "ice": "isl", "mac": "mkd",
	"mao": "mri", "may": "msa", "per": "fas", "rum": "ron", "slo": "slk", "tib": "bod", "wel": "cym", "khk": "mon",
	"mvf": "mon"}

var sonarrNoBT = map[string]bool{"gsw": true, "khk": true, "mvf": true}

// matchError is a regex match that failed (a timeout under a starved CPU). The helpers below panic with it and the
// exported entry points recover it into their error (catch): a timeout fails the naming, never yields a wrong name.
type matchError struct{ err error }

// catch turns a matchError panic into *err; any other panic goes on.
func catch(err *error) {
	if r := recover(); r != nil {
		me, ok := r.(matchError)
		if !ok {
			panic(r)
		}
		*err = fmt.Errorf("naming: %w", me.err)
	}
}

func mustMatch(m *regexp2.Match, err error) *regexp2.Match {
	if err != nil {
		panic(matchError{err})
	}
	return m
}

// firstMatch is re's first match in s, or nil.
func firstMatch(re *regexp2.Regexp, s string) *regexp2.Match { return mustMatch(re.FindStringMatch(s)) }

// replaceFunc runs a regexp2 replacement. regexp2's own ReplaceFunc returns "" and no error when a match after the
// first times out, so the loop is ours.
func replaceFunc(re *regexp2.Regexp, in string, f func(regexp2.Match) string) string {
	m := firstMatch(re, in)
	if m == nil {
		return in
	}
	runes := []rune(in) // match indexes count runes
	var b strings.Builder
	at := 0
	for ; m != nil; m = mustMatch(re.FindNextMatch(m)) {
		b.WriteString(string(runes[at:m.Index]))
		b.WriteString(f(*m))
		at = m.Index + m.Length
	}
	b.WriteString(string(runes[at:]))
	return b.String()
}

// emptyName refuses a file name whose base (without extension) is empty, e.g. "Season 01/.mkv": the format rendered
// nothing for it. ext is appended.
func emptyName(name, ext string) (string, error) {
	if strings.TrimSpace(name[strings.LastIndex(name, "/")+1:]) == "" {
		return "", fmt.Errorf("naming: the format gives an empty file name")
	}
	return name + ext, nil
}

func replace(re *regexp2.Regexp, in, with string) string {
	return replaceFunc(re, in, func(regexp2.Match) string { return with })
}

func isMatch(re *regexp2.Regexp, in string) bool {
	ok, err := re.MatchString(in)
	if err != nil {
		panic(matchError{err})
	}
	return ok
}

func group(m *regexp2.Match, name string) string {
	if g := m.GroupByName(name); g != nil {
		return g.String()
	}
	return ""
}

func groupSuccess(m *regexp2.Match, name string) bool {
	g := m.GroupByName(name)
	return g != nil && len(g.Captures) > 0
}

// allMatches returns every match of re in s.
func allMatches(re *regexp2.Regexp, s string) []*regexp2.Match {
	var out []*regexp2.Match
	for m := firstMatch(re, s); m != nil; m = mustMatch(re.FindNextMatch(m)) {
		out = append(out, m)
	}
	return out
}

// simplifyToken is FileNameBuilderTokenEqualityComparer.SimplifyToken: drop whitespace, '_' and non-word
// characters, lower-case.
func simplifyToken(token string) string {
	// Handler keys must not depend on whether a regex match times out under load.
	return strings.ToLower(strings.Map(func(r rune) rune {
		if r != '_' && (unicode.In(r, unicode.L, unicode.Mn, unicode.Nd, unicode.Pc) || r == '\u200c' || r == '\u200d') {
			return r
		}
		return -1
	}, token))
}

// handlers is the arrs' token handler dictionary, keyed through simplifyToken.
type handlers map[string]func(*tokenMatch) *string

func (h handlers) set(token string, f func(*tokenMatch) string) {
	h[simplifyToken(token)] = func(m *tokenMatch) *string { s := f(m); return &s }
}

// setNull registers a handler that returns null (Sonarr's placeholder tokens: the token is kept as written).
func (h handlers) setNull(token string) {
	h[simplifyToken(token)] = func(*tokenMatch) *string { return nil }
}

func (h handlers) get(token string) func(*tokenMatch) *string {
	if f, ok := h[simplifyToken(token)]; ok {
		return f
	}
	return func(*tokenMatch) *string { s := ""; return &s }
}

type tokenMatch struct {
	Tag, Prefix, Separator, Suffix, Token, CustomFormat string
}

// defaultValue is TokenMatch.DefaultValue.
func (m *tokenMatch) defaultValue(v string) string {
	if m.Prefix == "" && m.Suffix == "" {
		return v
	}
	return ""
}

// applyCase is ReplaceToken's case rule: an all-lower token lower-cases its value, an all-upper one upper-cases it.
func applyCase(token, text string) string {
	allLower, allUpper := true, true
	for _, r := range token {
		if !unicode.IsLetter(r) {
			continue
		}
		if !unicode.IsLower(r) {
			allLower = false
		}
		if !unicode.IsUpper(r) {
			allUpper = false
		}
	}
	switch {
	case allLower:
		return strings.ToLower(text)
	case allUpper:
		return strings.ToUpper(text)
	}
	return text
}

// cleanFileName is FileNameBuilder.CleanFileName(name, namingConfig).
func cleanFileName(name string, c *Config) string {
	result := name
	if c.ReplaceIllegalCharacters {
		if c.ColonReplacement == ColonSmart {
			result = strings.ReplaceAll(result, ": ", " - ")
			result = strings.ReplaceAll(result, ":", "-")
		} else {
			replacement := ""
			switch c.ColonReplacement {
			case ColonDash:
				replacement = "-"
			case ColonSpaceDash:
				replacement = " -"
			case ColonSpaceDashSpace:
				replacement = " - "
			case ColonCustom:
				replacement = c.CustomColonReplacement
			}
			result = strings.ReplaceAll(result, ":", replacement)
		}
	} else {
		result = strings.ReplaceAll(result, ":", "")
	}
	for i, bad := range badCharacters {
		with := ""
		if c.ReplaceIllegalCharacters {
			with = goodCharacters[i]
		}
		result = strings.ReplaceAll(result, bad, with)
	}
	return strings.TrimRight(strings.TrimLeft(result, " ."), " ")
}

// cleanFolderName is FileNameBuilder.CleanFolderName.
func cleanFolderName(name string) string {
	name = collapseSeparators(name)
	return strings.Trim(name, " .")
}

// collapseSeparators is FileNameCleanupRegex.Replace(s, first character).
func collapseSeparators(s string) string {
	return replaceFunc(fileNameCleanupRegex, s, func(m regexp2.Match) string { return m.String()[:1] })
}

func trimSeparators(s string) string { return replace(trimSeparatorsRegex, s, "") }

func replaceReservedDeviceNames(s string) string {
	return replaceFunc(reservedDeviceNames, s, func(m regexp2.Match) string { return strings.ReplaceAll(m.String(), ".", "_") })
}

// cleanTitle is Sonarr's CleanTitle (Radarr's adds RemoveDiacritics).
func cleanTitle(title string) string {
	title = strings.ReplaceAll(title, "&", "and")
	title = replace(scenifyReplaceChars, title, " ")
	return replace(scenifyRemoveChars, title, "")
}

// titleThe is TitleThe.
func titleThe(title string) string {
	m := firstMatch(titlePrefixRegex, title)
	if m == nil {
		return title
	}
	g := m.Groups()
	return g[2].String() + ", " + g[1].String() + g[3].String()
}

// cleanTitleThe is CleanTitleThe with the flavour's CleanTitle.
func cleanTitleThe(title string, clean func(string) string) string {
	if m := firstMatch(titlePrefixRegex, title); m != nil {
		g := m.Groups()
		return strings.TrimSpace(clean(g[2].String())) + ", " + g[1].String() + clean(g[3].String())
	}
	return clean(title)
}

// titleFirstCharacter is TitleFirstCharacter.
func titleFirstCharacter(title string) string {
	rs := []rune(title)
	for i := 0; i < 2 && i < len(rs); i++ {
		if unicode.IsLetter(rs[i]) || unicode.IsDigit(rs[i]) {
			s := []rune(removeDiacritics(strings.ToUpper(string(rs[i]))))
			if len(s) > 0 {
				return string(s[0])
			}
		}
	}
	return "_"
}

// nonDecomposing are the letters Diacritical.Net maps that Unicode decomposition leaves alone.
var nonDecomposing = map[rune]string{'ø': "o", 'Ø': "O", 'ł': "l", 'Ł': "L", 'đ': "d", 'Đ': "D", 'ß': "ss",
	'æ': "ae", 'Æ': "AE", 'œ': "oe", 'Œ': "OE", 'þ': "th", 'Þ': "TH", 'ð': "d", 'Ð': "D", 'ı': "i"}

// removeDiacritics is Diacritical.Net's RemoveDiacritics (see the package doc).
func removeDiacritics(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if m, ok := nonDecomposing[r]; ok {
			b.WriteString(m)
			continue
		}
		b.WriteRune(r)
	}
	return norm.NFC.String(b.String())
}

// utf16Len is .NET string.Length.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// truncateBytes is NzbDrone's string.Truncate(maxLength): the longest prefix of whole characters that fits in
// maxLength UTF-8 bytes.
func truncateBytes(s string, maxLength int) string {
	if len(s) <= maxLength {
		return s
	}
	n := 0
	for i, r := range s {
		l := utf8.RuneLen(r)
		if n+l > maxLength {
			return s[:i]
		}
		n += l
	}
	return s
}

// reverse is NzbDrone's string.Reverse (by UTF-16 unit in .NET; by rune here, which only differs for characters
// outside the BMP).
func reverse(s string) string {
	rs := []rune(s)
	for i, j := 0, len(rs)-1; i < j; i, j = i+1, j-1 {
		rs[i], rs[j] = rs[j], rs[i]
	}
	return string(rs)
}

// maxLengthFromFormatter is GetMaxLengthFromFormatter: int.TryParse, 0 when it isn't a number.
func maxLengthFromFormatter(f string) int {
	n, err := strconv.Atoi(strings.TrimSpace(f))
	if err != nil {
		return 0
	}
	return n
}

// truncate is FileNameBuilder.Truncate(input, formatter).
func truncate(input, formatter string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	maxLength := maxLengthFromFormatter(formatter)
	abs := maxLength
	if abs < 0 {
		abs = -abs
	}
	if maxLength == 0 || utf16Len(input) <= abs {
		return input
	}
	if maxLength < 0 {
		return "{ellipsis}" + reverse(strings.TrimRight(truncateBytes(reverse(input), abs-3), " ."))
	}
	return strings.TrimRight(truncateBytes(input, maxLength-3), " .") + "{ellipsis}"
}

// formatNumber is int.ToString(format) for the formats naming uses: empty or "0" plain, "00", "000" zero-padded.
func formatNumber(v int, format string) string {
	if format == "" {
		return strconv.Itoa(v)
	}
	if strings.Trim(format, "0") == "" {
		s := strconv.Itoa(abs(v))
		for len(s) < len(format) {
			s = "0" + s
		}
		if v < 0 {
			s = "-" + s
		}
		return s
	}
	return strconv.Itoa(v)
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// languagesToken is GetLanguagesToken.
func languagesToken(langs []string, filter string, skipEnglishOnly, quoted, sonarr bool) string {
	var tokens []string
	for _, item := range langs {
		if strings.TrimSpace(item) != "" && item != "und" {
			tokens = append(tokens, strings.TrimSpace(item))
		}
	}
	for i, t := range tokens {
		token := strings.ToLower(t)
		if mapped, ok := iso639BT[token]; ok && !(sonarr && sonarrNoBT[token]) {
			token = mapped
		}
		if two, ok := twoLetter(token); ok {
			tokens[i] = strings.ToUpper(two)
		}
	}
	tokens = distinct(tokens)
	filtered := tokens
	if strings.TrimSpace(filter) != "" {
		if strings.HasPrefix(filter, "-") {
			ex := map[string]bool{}
			for _, f := range strings.Split(filter, "-") {
				ex[f] = true
			}
			filtered = nil
			for _, t := range tokens {
				if !ex[t] {
					filtered = append(filtered, t)
				}
			}
		} else {
			have := map[string]bool{}
			for _, t := range tokens {
				have[t] = true
			}
			filtered = nil
			for _, f := range distinct(strings.Split(filter, "+")) {
				if have[f] {
					filtered = append(filtered, f)
				}
			}
		}
	}
	if strings.TrimSpace(filter) != "" && strings.HasSuffix(filter, "+") && len(filtered) != len(tokens) {
		filtered = append(filtered, "--")
	}
	if skipEnglishOnly && len(filtered) == 1 && filtered[0] == "EN" {
		return ""
	}
	resp := strings.Join(filtered, "+")
	if quoted && strings.TrimSpace(resp) != "" {
		return "[" + resp + "]"
	}
	return resp
}

// twoLetter is new CultureInfo(code).TwoLetterISOLanguageName. ok is false where .NET throws (not a well-formed
// language code), which leaves the token as it was.
func twoLetter(code string) (string, bool) {
	if code == "" {
		return "", false
	}
	b, err := language.ParseBase(code)
	if err != nil {
		for _, r := range code {
			if r < 'a' || r > 'z' {
				return "", false
			}
		}
		if len(code) < 2 || len(code) > 3 {
			return "", false
		}
		return code, true
	}
	return b.String(), true
}

func distinct(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// customFormatsToken is GetCustomFormatsToken over the names the arr would include when renaming.
func customFormatsToken(names []string, filter string) string {
	out := names
	if strings.TrimSpace(filter) != "" {
		var split []string
		neg := strings.HasPrefix(filter, "-")
		if neg {
			split = strings.Split(filter[1:], ",")
		} else {
			split = strings.Split(filter, ",")
		}
		in := map[string]bool{}
		for _, s := range split {
			in[s] = true
		}
		out = nil
		for _, n := range names {
			if in[n] != neg {
				out = append(out, n)
			}
		}
	}
	return strings.Join(out, " ")
}

// editionToken is Radarr's GetEditionToken: title case, ordinals lower-cased, IMAX/3D/SDR/HDR/DV upper-cased.
func editionToken(edition string) string {
	e := toTitleCase(strings.ToLower(edition))
	e = replaceFunc(editionOrdinalRegex, e, func(m regexp2.Match) string { return strings.ToLower(m.GroupByNumber(1).String()) })
	e = replaceFunc(editionUpperRegex, e, func(m regexp2.Match) string { return strings.ToUpper(m.GroupByNumber(1).String()) })
	return e
}

// toTitleCase is TextInfo.ToTitleCase on an already lower-cased string: the first letter of each word upper-cased.
// Words are runs of letters, digits and apostrophes.
func toTitleCase(s string) string {
	rs := []rune(s)
	inWord := false
	for i, r := range rs {
		isWord := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' || r == '’'
		if isWord && !inWord && unicode.IsLetter(r) {
			rs[i] = unicode.ToUpper(r)
		}
		inWord = isWord
	}
	return string(rs)
}
