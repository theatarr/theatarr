// Package textsearch is the one rule for free-text filters (docs/design/ui.md, Product rules, "Filter boxes"): the
// typed text is split on whitespace and every word must be found, in any order, case-insensitively, as a substring.
// Empty text matches everything. SQLite's LIKE folds case for ASCII only, so that is all the SQL form promises.
package textsearch

import "strings"

// MaxWords caps how many words a filter uses; the rest are ignored.
const MaxWords = 10

// Words splits text on whitespace, at most MaxWords words.
func Words(text string) []string {
	w := strings.Fields(text)
	if len(w) > MaxWords {
		w = w[:MaxWords]
	}
	return w
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// LikeEscape escapes s for a LIKE pattern written with ESCAPE '\'.
func LikeEscape(s string) string { return likeEscaper.Replace(s) }

// Where returns one condition per word of text, joined with AND, and its arguments: each word must be a substring
// of at least one of cols. It is "" with no arguments when text has no words.
func Where(text string, cols ...string) (string, []any) {
	var conds []string
	var args []any
	for _, w := range Words(text) {
		ors := make([]string, len(cols))
		for i, c := range cols {
			ors[i] = c + ` LIKE ? ESCAPE '\'`
			args = append(args, "%"+LikeEscape(w)+"%")
		}
		conds = append(conds, "("+strings.Join(ors, " OR ")+")")
	}
	return strings.Join(conds, " AND "), args
}

// Match reports whether every word of text is a case-insensitive substring of at least one haystack.
func Match(text string, haystacks ...string) bool {
	all := strings.ToLower(strings.Join(haystacks, "\x00"))
	for _, w := range Words(text) {
		if !strings.Contains(all, strings.ToLower(w)) {
			return false
		}
	}
	return true
}
