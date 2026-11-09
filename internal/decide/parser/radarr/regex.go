package radarr

// Thin helpers that give regexp2 the shape of the .NET Regex calls the Radarr parser makes
// (IsMatch, Match, Matches, Replace, Split, Groups["name"]). A match error (only a timeout, see
// netregex.MatchTimeout) is treated as "no match", as netregex documents.

import (
	"strings"

	"github.com/dlclark/regexp2"

	"github.com/theatarr/theatarr/internal/netregex"
)

// Short names for the .NET RegexOptions used by the ported tables.
const (
	optI = netregex.IgnoreCase
	optX = netregex.IgnorePatternWhitespace
)

func re(expr string, opts regexp2.RegexOptions) *regexp2.Regexp {
	return netregex.MustCompile(expr, opts)
}

// isMatch is Regex.IsMatch.
func isMatch(r *regexp2.Regexp, s string) bool {
	ok, err := r.MatchString(s)
	return err == nil && ok
}

// match is Regex.Match: the first match, or nil.
func match(r *regexp2.Regexp, s string) *regexp2.Match {
	m, err := r.FindStringMatch(s)
	if err != nil {
		return nil
	}
	return m
}

// matches is Regex.Matches: every non-overlapping match, left to right.
func matches(r *regexp2.Regexp, s string) []*regexp2.Match {
	var out []*regexp2.Match
	m, err := r.FindStringMatch(s)
	for err == nil && m != nil {
		out = append(out, m)
		m, err = r.FindNextMatch(m)
	}
	return out
}

// group is Match.Groups[name]: its value and whether it succeeded. An unknown group name is an
// unsuccessful group, as in .NET.
func group(m *regexp2.Match, name string) (string, bool) {
	if m == nil {
		return "", false
	}
	g := m.GroupByName(name)
	if g == nil || len(g.Captures) == 0 {
		return "", false
	}
	return g.String(), true
}

// ok is Match.Groups[name].Success.
func ok(m *regexp2.Match, name string) bool {
	_, s := group(m, name)
	return s
}

// value is Match.Groups[name].Value ("" when the group did not match).
func value(m *regexp2.Match, name string) string {
	v, _ := group(m, name)
	return v
}

// captures is Match.Groups[name].Captures, as strings.
func captures(m *regexp2.Match, name string) []string {
	if m == nil {
		return nil
	}
	g := m.GroupByName(name)
	if g == nil {
		return nil
	}
	out := make([]string, len(g.Captures))
	for i, c := range g.Captures {
		out[i] = c.String()
	}
	return out
}

// replace is Regex.Replace(input, replacement) with .NET substitutions ($1, ${name}).
func replace(r *regexp2.Regexp, s, repl string) string {
	out, err := r.Replace(s, repl, -1, -1)
	if err != nil {
		return s
	}
	return out
}

// replaceFunc is Regex.Replace(input, MatchEvaluator).
func replaceFunc(r *regexp2.Regexp, s string, f func(regexp2.Match) string) string {
	out, err := r.ReplaceFunc(s, f, -1, -1)
	if err != nil {
		return s
	}
	return out
}

// split is Regex.Split for a pattern without capturing groups.
func split(r *regexp2.Regexp, s string) []string {
	runes := []rune(s)
	var out []string
	prev := 0
	for _, m := range matches(r, s) {
		out = append(out, string(runes[prev:m.Index]))
		prev = m.Index + m.Length
	}
	return append(out, string(runes[prev:]))
}

// regexReplace is Radarr's RegexReplace: a pattern with a fixed replacement.
type regexReplace struct {
	re   *regexp2.Regexp
	repl string
}

func newRegexReplace(expr, repl string, opts regexp2.RegexOptions) regexReplace {
	return regexReplace{re(expr, opts), repl}
}

func (r regexReplace) Replace(s string) string { return replace(r.re, s, r.repl) }

// trimChars is .NET String.Trim(params char[]).
func trimChars(s string, chars string) string {
	return strings.Trim(s, chars)
}
