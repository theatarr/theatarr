package sonarr

// Small equivalents of the .NET and NzbDrone.Common helpers the parser calls (string, path and number helpers from
// System, System.IO and src/NzbDrone.Common/Extensions/{StringExtensions,PathExtensions}.cs @ cab419a). Paths follow
// .NET on Linux, where the arrs run: '/' is the only directory separator and '\0' the only invalid path
// character.

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/dlclark/regexp2"
	"golang.org/x/text/unicode/norm"
)

// trimDotNet is string.Trim().
func trimDotNet(s string) string { return strings.TrimFunc(s, unicode.IsSpace) }

func containsIgnoreCase(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// containsInvalidPathChars is PathExtensions.ContainsInvalidPathChars on Linux.
func containsInvalidPathChars(s string) bool { return strings.ContainsRune(s, 0) }

// pathGetExtension is Path.GetExtension on Linux: from the last '.' after the last '/', "" when the name ends in '.'.
func pathGetExtension(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		switch p[i] {
		case '.':
			if i == len(p)-1 {
				return ""
			}
			return p[i:]
		case '/':
			return ""
		}
	}
	return ""
}

// getPathExtension is PathExtensions.GetPathExtension: from the last '.', ignoring separators.
func getPathExtension(p string) string {
	i := strings.LastIndexByte(p, '.')
	if i == -1 || i == len(p)-1 {
		return ""
	}
	return p[i:]
}

// pathFileName is FileInfo.Name / Path.GetFileName on Linux.
func pathFileName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// pathDirectoryName is FileInfo.Directory.Name on Linux, and whether there is a directory. A relative path without
// a directory reports none (.NET would resolve it against the working directory).
func pathDirectoryName(p string) (string, bool) {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return "", false
	}
	dir := p[:i]
	if dir == "" {
		// The root directory: DirectoryInfo("/").Name is "/".
		return "/", true
	}
	return pathFileName(strings.TrimRight(dir, "/")), true
}

// pathFileNameWithoutExtension is Path.GetFileNameWithoutExtension on Linux.
func pathFileNameWithoutExtension(p string) string {
	name := pathFileName(p)
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[:i]
	}
	return name
}

// parseIntDotNet is int.TryParse / long.TryParse with NumberStyles.Integer: optional surrounding white space and a
// leading sign, ASCII digits only.
func parseIntDotNet(s string, bits int) (int64, bool) {
	s = strings.TrimFunc(s, isDotNetNumberWhite)
	if s == "" {
		return 0, false
	}
	body := s
	if body[0] == '+' || body[0] == '-' {
		body = body[1:]
	}
	if body == "" {
		return 0, false
	}
	for i := 0; i < len(body); i++ {
		if body[i] < '0' || body[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, bits)
	if err != nil {
		return 0, false
	}
	return n, true
}

func tryParseInt(s string) (int, bool) {
	n, ok := parseIntDotNet(s, 32)
	return int(n), ok
}

// isDotNetNumberWhite is the white space NumberStyles.AllowLeadingWhite/AllowTrailingWhite accept.
func isDotNetNumberWhite(r rune) bool {
	return r == ' ' || (r >= '\t' && r <= '\r')
}

// convertToInt32 is Convert.ToInt32(string); it panics like the .NET call throws, and ParseTitle recovers.
func convertToInt32(s string) int {
	n, ok := tryParseInt(s)
	if !ok {
		panic(formatError(s + " isn't a valid Int32"))
	}
	return n
}

type formatError string

func (e formatError) Error() string { return string(e) }

// removeAccent is StringExtensions.RemoveAccent: drop non-spacing marks after NFD, then NFC.
func removeAccent(s string) string {
	d := norm.NFD.String(s)
	var b strings.Builder
	for _, r := range d {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return norm.NFC.String(b.String())
}

// lastMatch is matches.OfType<Match>().LastOrDefault().
func lastMatch(ms []*regexp2.Match) *regexp2.Match {
	if len(ms) == 0 {
		return nil
	}
	return ms[len(ms)-1]
}

func reverseRunes(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

// diacriticsNonDecomposing are the letters Diacritical.Net maps that Unicode decomposition leaves alone, with
// v5's AdditionalDiacriticsProvider (ð, Ð, þ) included; the same table as internal/library/naming.
var diacriticsNonDecomposing = map[rune]string{'ø': "o", 'Ø': "O", 'ł': "l", 'Ł': "L", 'đ': "d", 'Đ': "D", 'ß': "ss",
	'æ': "ae", 'Æ': "AE", 'œ': "oe", 'Œ': "OE", 'þ': "th", 'Þ': "TH", 'ð': "d", 'Ð': "D", 'ı': "i"}

// removeDiacritics is Sonarr v5's StringExtensions.RemoveDiacritics (Diacritical.Net plus
// AdditionalDiacriticsProvider): decomposition with the combining marks dropped, plus the letters that do not
// decompose. RemoveAccent (v4) leaves those letters as they are.
func removeDiacritics(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if m, ok := diacriticsNonDecomposing[r]; ok {
			b.WriteString(m)
			continue
		}
		b.WriteRune(r)
	}
	return norm.NFC.String(b.String())
}
