package radarr

// Equivalents of the .NET string, path and number helpers the Radarr parser leans on
// (NzbDrone.Common.Extensions and System.IO.Path on Linux).

import (
	"strings"
	"unicode"

	"github.com/dlclark/regexp2"
	"golang.org/x/text/unicode/norm"
)

// isNullOrWhiteSpace is string.IsNullOrWhiteSpace.
func isNullOrWhiteSpace(s string) bool {
	return strings.TrimFunc(s, unicode.IsSpace) == ""
}

// netTrim is string.Trim(): strips Unicode white space from both ends.
func netTrim(s string) string {
	return strings.TrimFunc(s, unicode.IsSpace)
}

// containsIgnoreCase is StringExtensions.ContainsIgnoreCase.
func containsIgnoreCase(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// containsInvalidPathChars is PathExtensions.ContainsInvalidPathChars; on Linux the only invalid
// path character is NUL.
func containsInvalidPathChars(s string) bool {
	return strings.ContainsRune(s, 0)
}

// pathGetExtension is System.IO.Path.GetExtension on Linux.
func pathGetExtension(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		switch p[i] {
		case '.':
			if i != len(p)-1 {
				return p[i:]
			}
			return ""
		case '/':
			return ""
		}
	}
	return ""
}

// getPathExtension is PathExtensions.GetPathExtension.
func getPathExtension(p string) string {
	idx := strings.LastIndexByte(p, '.')
	if idx == -1 || idx == len(p)-1 {
		return ""
	}
	return p[idx:]
}

// pathGetFileName is System.IO.Path.GetFileName on Linux.
func pathGetFileName(p string) string {
	return p[strings.LastIndexByte(p, '/')+1:]
}

// pathGetFileNameWithoutExtension is System.IO.Path.GetFileNameWithoutExtension on Linux.
func pathGetFileNameWithoutExtension(p string) string {
	name := pathGetFileName(p)
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[:i]
	}
	return name
}

// pathGetDirectoryName returns the last segment of the parent directory, as FileInfo.Directory.Name.
func pathDirectoryName(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return pathGetFileName(strings.TrimRight(p[:i], "/"))
}

// removeAccent is StringExtensions.RemoveAccent: decompose, drop non-spacing marks, recompose.
func removeAccent(s string) string {
	d := norm.NFD.String(s)
	var b strings.Builder
	for _, r := range d {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return norm.NFC.String(b.String())
}

// tryParseInt is int.TryParse / long.TryParse with NumberStyles.Integer: optional surrounding
// white space and one leading sign, then ASCII digits that fit in bits.
func tryParseInt(s string, bits int) (int64, bool) {
	s = strings.TrimFunc(s, isNetNumberSpace)
	if s == "" {
		return 0, false
	}
	neg := false
	switch s[0] {
	case '-':
		neg = true
		s = s[1:]
	case '+':
		s = s[1:]
	}
	if s == "" {
		return 0, false
	}
	var n uint64
	limit := uint64(1) << (bits - 1)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
		if n > limit {
			return 0, false
		}
	}
	if !neg && n == limit {
		return 0, false
	}
	if neg {
		return -int64(n), true
	}
	return int64(n), true
}

// NumberStyles.AllowLeadingWhite/AllowTrailingWhite accept only these characters.
func isNetNumberSpace(r rune) bool {
	return r == ' ' || (r >= '\t' && r <= '\r')
}

func isInt32(s string) bool {
	_, ok := tryParseInt(s, 32)
	return ok
}

// lastMatch is MatchCollection.OfType<Match>().LastOrDefault().
func lastMatch(ms []*regexp2.Match) *regexp2.Match {
	if len(ms) == 0 {
		return nil
	}
	return ms[len(ms)-1]
}
