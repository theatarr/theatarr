package sonarr

// Helpers that give regexp2 the shape of the .NET Regex API the Sonarr code calls: Match, Matches, IsMatch,
// Replace with a format or an evaluator, Groups[name].Success and Captures, and the RegexReplace helper
// (src/NzbDrone.Core/Parser/RegexReplace.cs @ cab419a).
//
// Indices are rune offsets (regexp2 works on []rune), where .NET uses UTF-16 code units. They differ only for
// characters outside the Basic Multilingual Plane; see the package doc.

import (
	"github.com/dlclark/regexp2"

	"github.com/theatarr/theatarr/internal/netregex"
)

func re(expr string, opts regexp2.RegexOptions) *regexp2.Regexp {
	return netregex.MustCompile(expr, opts)
}

const (
	optNone = netregex.None
	optI    = netregex.IgnoreCase
	optX    = netregex.IgnorePatternWhitespace
)

// match returns the first match or nil. A match timeout counts as no match.
func match(r *regexp2.Regexp, s string) *regexp2.Match {
	m, err := r.FindStringMatch(s)
	if err != nil {
		return nil
	}
	return m
}

// matches returns every match, like Regex.Matches.
func matches(r *regexp2.Regexp, s string) []*regexp2.Match {
	var out []*regexp2.Match
	m, err := r.FindStringMatch(s)
	for err == nil && m != nil {
		out = append(out, m)
		m, err = r.FindNextMatch(m)
	}
	return out
}

func isMatch(r *regexp2.Regexp, s string) bool {
	ok, err := r.MatchString(s)
	return err == nil && ok
}

// replace is Regex.Replace(input, format). On a timeout it leaves the input unchanged.
func replace(r *regexp2.Regexp, s, format string) string {
	out, err := r.Replace(s, format, -1, -1)
	if err != nil {
		return s
	}
	return out
}

// replaceFunc is Regex.Replace(input, MatchEvaluator).
func replaceFunc(r *regexp2.Regexp, s string, f func(m regexp2.Match) string) string {
	out, err := r.ReplaceFunc(s, f, -1, -1)
	if err != nil {
		return s
	}
	return out
}

// group returns the named group of m, or nil when m is nil or the pattern has no such group.
func group(m *regexp2.Match, name string) *regexp2.Group {
	if m == nil {
		return nil
	}
	return m.GroupByName(name)
}

// success is Groups[name].Success.
func success(m *regexp2.Match, name string) bool {
	g := group(m, name)
	return g != nil && len(g.Captures) > 0
}

// value is Groups[name].Value: the last capture, or "" when the group did not take part.
func value(m *regexp2.Match, name string) string {
	g := group(m, name)
	if g == nil || len(g.Captures) == 0 {
		return ""
	}
	return g.String()
}

// captures is Groups[name].Captures.
func captures(m *regexp2.Match, name string) []regexp2.Capture {
	g := group(m, name)
	if g == nil {
		return nil
	}
	return g.Captures
}

// groupEnd is RegexExtensions.EndIndex on a group: Index + Length, 0 for a group that did not take part.
func groupEnd(m *regexp2.Match, name string) int {
	g := group(m, name)
	if g == nil || len(g.Captures) == 0 {
		return 0
	}
	return g.Index + g.Length
}

// regexReplace is Sonarr's RegexReplace.
type regexReplace struct {
	re     *regexp2.Regexp
	format string
}

func newRegexReplace(expr, format string, opts regexp2.RegexOptions) regexReplace {
	return regexReplace{re: re(expr, opts), format: format}
}

func (r regexReplace) Replace(s string) string { return replace(r.re, s, r.format) }

// TryReplace replaces in place and reports whether the pattern matched.
func (r regexReplace) TryReplace(s *string) bool {
	ok := isMatch(r.re, *s)
	*s = replace(r.re, *s, r.format)
	return ok
}
