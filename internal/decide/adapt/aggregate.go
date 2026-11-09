package adapt

// Port of Radarr src/NzbDrone.Core/Download/Aggregation/Aggregators/AggregateLanguages.cs @ a96bf7c and
// Sonarr src/NzbDrone.Core/Download/Aggregation/Aggregators/AggregateLanguages.cs @ cab419a.
//
// The two differ only in what they take from the library: Radarr removes the languages found in the movie title,
// Sonarr those found in each mapped episode's title, and each normalises with its own parser's function. The
// indexer branch (release languages, indexer multi-languages) runs when a Release is given; the parse context has
// none (Release is null).

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
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
		normalizedReleaseTokens := ops.normalize(releaseTokens)
		var languagesToRemove []int
		for _, title := range titles {
			titleLanguage := ops.parse(title)
			if contains(titleLanguage, langUnknown) {
				continue
			}
			normalizedTitle := ops.normalize(title)
			index := indexFold(normalizedReleaseTokens, normalizedTitle)
			if index >= 0 {
				// The index is into the normalised tokens but is applied to the raw ones, as in the arr.
				r := []rune(releaseTokens)
				n := utf8.RuneCountInString(normalizedTitle)
				if index+n > len(r) {
					return nil, false, fmt.Errorf("adapt: aggregate languages: Remove(%d, %d) on %d characters", index, n, len(r))
				}
				releaseTokens = string(r[:index]) + string(r[index+n:])
				languagesToRemove = append(languagesToRemove, titleLanguage...)
			}
		}

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
	if len(languages) == 0 || (len(languages) == 1 && languages[0] == langUnknown) {
		languages, assumed = []int{original}, true
	}

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

// indexFold is String.IndexOf(value, CurrentCultureIgnoreCase) as a rune index: the first position where needle
// occurs in s, ignoring case; 0 for an empty needle; -1 when absent.
func indexFold(s, needle string) int {
	h, n := []rune(s), []rune(needle)
	if len(n) == 0 {
		return 0
	}
	for i := 0; i+len(n) <= len(h); i++ {
		ok := true
		for j := range n {
			if !equalFold(h[i+j], n[j]) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func equalFold(a, b rune) bool {
	if a == b {
		return true
	}
	return unicode.ToLower(a) == unicode.ToLower(b) || unicode.ToUpper(a) == unicode.ToUpper(b)
}
