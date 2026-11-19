package adapt

// Port of Radarr src/NzbDrone.Core/Download/Aggregation/Aggregators/AggregateLanguages.cs @ a96bf7c and
// Sonarr src/NzbDrone.Core/Download/Aggregation/Aggregators/AggregateLanguages.cs @ cab419a.
//
// The two differ only in what they take from the library: Radarr removes the languages found in the movie title,
// Sonarr those found in each mapped episode's title, and each normalises with its own parser's function. The
// indexer branch (release languages, indexer multi-languages) runs when a Release is given; the parse context has
// none (Release is null).

import (
	"strings"
	"unicode"
)

// Language ids shared by both arrs.
const (
	langUnknown  = 0
	langOriginal = -2
)

// langOps is one flavour's LanguageParser.ParseLanguages (as ids), title normaliser (Parser.NormalizeMovieTitle /
// Parser.NormalizeEpisodeTitle) and Parser.HasMultipleLanguages.
type langOps struct {
	parse     func(string) []int
	normalize func(string) string
	multi     func(string) bool
}

// aggregateLanguages is AggregateLanguages.Aggregate. parsed are the parsed languages, releaseTokens Radarr's
// SimpleReleaseTitle ?? ReleaseTitle or Sonarr's ReleaseTokens ?? ReleaseTitle. mapped is whether a movie or series
// matched; titles are the movie title or the episodes' titles; original is the movie's or series' original
// language. rel is the indexer release, nil for none (the parse context). assumed is whether the original language
// stood in for none found.
func aggregateLanguages(ops langOps, parsed []int, releaseTokens string, mapped bool, titles []string,
	original int, rel *Release) (languages []int, assumed bool, err error) {
	languages = parsed
	releaseLanguages := rel != nil && len(rel.Languages) > 0
	if !mapped {
		// "Unable to aggregate languages, using parsed values"
		if releaseLanguages {
			return append([]int{}, rel.Languages...), false, nil
		}
		return languages, false, nil
	}

	if releaseLanguages {
		// "Languages provided by indexer, using release values"
		languages = append([]int{}, rel.Languages...)
	} else {
		// The arr finds the normalised title in the normalised tokens and cuts that index out of the raw tokens,
		// which cuts the wrong text, or past the end, once the two differ in length. Ours: the normalised title's
		// words are found in the raw tokens themselves, case-insensitively and with any separators between them,
		// and cut there, so each title is found in the tokens as the cuts before it left them and the rest keeps
		// its case.
		tokens := []rune(releaseTokens)
		var languagesToRemove []int
		for _, title := range titles {
			titleLanguage := ops.parse(title)
			if contains(titleLanguage, langUnknown) {
				continue
			}
			if start, end := findWords(tokens, strings.Fields(ops.normalize(title))); start >= 0 {
				tokens = append(tokens[:start:start], tokens[end:]...)
				languagesToRemove = append(languagesToRemove, titleLanguage...)
			}
		}
		releaseTokens = string(tokens)

		// Remove any languages still in the title that would normally be removed
		languagesToRemove = except(languagesToRemove, ops.parse(releaseTokens))
		// Remove all languages that aren't part of the updated releaseTokens
		languages = except(languages, languagesToRemove)
	}

	// Use indexer setting for Multi-languages
	if rel != nil && strings.TrimSpace(rel.Title) != "" && len(rel.MultiLanguages) > 0 && ops.multi(rel.Title) {
		if len(languages) == 0 || (len(languages) == 1 && languages[0] == langUnknown) {
			languages = append([]int{}, rel.MultiLanguages...)
		} else {
			languages = append(append([]int{}, languages...), except(rel.MultiLanguages, languages)...)
		}
	}

	// Use the movie's or series' language as fallback if we couldn't parse a language
	languages, assumed = FallbackLanguage(languages, original)

	if contains(languages, langOriginal) {
		languages = removeFirst(languages, langOriginal)
		if !contains(languages, original) {
			languages = append(languages, original)
		} else {
			languages = append(languages, langUnknown)
		}
	}
	return languages, assumed, nil
}

// FallbackLanguage is AggregateLanguages' fallback: no language, or Unknown alone, becomes the original language.
// assumed is whether it did.
func FallbackLanguage(languages []int, original int) ([]int, bool) {
	if len(languages) == 0 || (len(languages) == 1 && languages[0] == langUnknown) {
		return []int{original}, true
	}
	return languages, false
}

func contains(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// except is Enumerable.Except: the distinct elements of a, in order, that are not in b.
func except(a, b []int) []int {
	out := []int{}
	seen := map[int]bool{}
	for _, x := range b {
		seen[x] = true
	}
	for _, x := range a {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// removeFirst is List.Remove: the first occurrence only.
func removeFirst(s []int, v int) []int {
	for i, x := range s {
		if x == v {
			return append(append([]int{}, s[:i]...), s[i+1:]...)
		}
	}
	return s
}

// findWords finds words (a normalised title: word characters only) in the tokens, case-insensitively, one after the
// other with one or more separators (anything a normaliser turns into a space) between them; the first word may
// start and the last end inside a token word, as a substring of the normalised tokens would. It returns the span,
// or -1, -1 when there are no words or no match.
func findWords(tokens []rune, words []string) (start, end int) {
	if len(words) == 0 {
		return -1, -1
	}
	ws := make([][]rune, len(words))
	for i, w := range words {
		ws[i] = []rune(w)
	}
	for i := range tokens {
		if e := matchWords(tokens, i, ws); e >= 0 {
			return i, e
		}
	}
	return -1, -1
}

// matchWords matches ws at tokens[i:] and returns the end of the match, or -1.
func matchWords(tokens []rune, i int, ws [][]rune) int {
	for k, w := range ws {
		if k > 0 {
			j := i
			for j < len(tokens) && !wordRune(tokens[j]) {
				j++
			}
			if j == i {
				return -1
			}
			i = j
		}
		if i+len(w) > len(tokens) {
			return -1
		}
		for j, r := range w {
			if !equalFold(tokens[i+j], r) {
				return -1
			}
		}
		i += len(w)
	}
	return i
}

// wordRune is .NET's \w: what a normaliser's [^\w\s] keeps.
func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || unicode.Is(unicode.Pc, r)
}

func equalFold(a, b rune) bool {
	if a == b {
		return true
	}
	return unicode.ToLower(a) == unicode.ToLower(b) || unicode.ToUpper(a) == unicode.ToUpper(b)
}
