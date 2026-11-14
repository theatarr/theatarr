package web

import (
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/textsearch"
)

// The library's name search (list text filter and the search overlay's library group): each word of the query is
// found in the title's name, one of its other names (title_alias: TMDb's original title, alternative titles and
// translations, an arr's alternate titles) or, for a four-digit word, its year. Punctuation and spaces do not count,
// so "spiderman" finds "Spider-Man" and "alien 1979" finds Alien (1979). Results rank exact, then prefix, then word
// matches.

// titleSearchDrop are the characters a name match ignores, in the name and in the query alike.
var titleSearchDrop = []string{" ", "-", ":", ".", ",", "'", "’", "!", "?", "&", "·", "/", "_", "(", ")"}

// foldSQL is col with titleSearchDrop removed and ASCII lower-cased, as foldName does in Go.
func foldSQL(col string) string {
	expr := "lower(" + col + ")"
	for _, c := range titleSearchDrop {
		expr = "replace(" + expr + ", '" + strings.ReplaceAll(c, "'", "''") + "', '')"
	}
	return expr
}

// foldName is s as foldSQL folds a column: SQLite's lower() changes ASCII letters only.
func foldName(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	s = string(b)
	for _, c := range titleSearchDrop {
		s = strings.ReplaceAll(s, c, "")
	}
	return s
}

var (
	foldedTitle = foldSQL("t.name")
	foldedAlias = foldSQL("a.name")
)

// titleTextWhere is the condition that every word of q matches the title (see above); "" when q has no word.
func titleTextWhere(q string) (string, []any) {
	var conds []string
	var args []any
	for _, w := range textsearch.Words(q) {
		f := foldName(w)
		if f == "" {
			continue
		}
		like := "%" + textsearch.LikeEscape(f) + "%"
		cond := foldedTitle + ` LIKE ? ESCAPE '\' OR EXISTS (SELECT 1 FROM title_alias a WHERE a.title_id = t.id AND ` +
			foldedAlias + ` LIKE ? ESCAPE '\')`
		args = append(args, like, like)
		if y, err := strconv.Atoi(w); err == nil && len(w) == 4 {
			cond += " OR t.year = ?"
			args = append(args, y)
		}
		conds = append(conds, "("+cond+")")
	}
	return strings.Join(conds, " AND "), args
}

// titleTextRank orders a name search: 0 the whole query is the name (or the name and year, or another name), 1 the
// name or another name starts with it, 2 the words match. "" when q has no word.
func titleTextRank(q string) (string, []any) {
	f := foldName(q)
	if f == "" {
		return "", nil
	}
	prefix := textsearch.LikeEscape(f) + "%"
	alias := func(op string) string {
		return `EXISTS (SELECT 1 FROM title_alias a WHERE a.title_id = t.id AND ` + foldedAlias + ` ` + op + `)`
	}
	expr := `CASE WHEN ` + foldedTitle + ` = ? OR ` + foldedTitle + ` || COALESCE(t.year, '') = ? OR ` + alias("= ?") +
		` THEN 0 WHEN ` + foldedTitle + ` LIKE ? ESCAPE '\' OR ` + alias(`LIKE ? ESCAPE '\'`) + ` THEN 1 ELSE 2 END`
	return expr, []any{f, f, f, prefix, prefix}
}
