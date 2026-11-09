package decide

// Movie mapping: port of Radarr src/NzbDrone.Core/Parser/ParsingService.cs (FindMovie, TryGetMovieByImDbId,
// TryGetMovieByTmdbId, TryGetMovieBySearchCriteria) @ a96bf7c, for a release searched for one title. The arr maps
// with the search criteria when it searched; a decision here is always for one title, so the search-criteria path
// is the one ported. The RSS path (MovieService.FindByTitle over the whole library) is not.

import (
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
)

// MatchType is MovieMatchType.
type MatchType string

const (
	MatchNone  MatchType = ""
	MatchID    MatchType = "Id"
	MatchTitle MatchType = "Title"
)

// Library looks titles up across our whole library, as the arr's MovieService.FindByImdbId and FindByTmdbId do. It
// lets an indexer id point at another title (the arr then maps the release there, and the search rejects it as the
// wrong movie). Nil means only the title under decision is known.
type Library interface {
	ByIMDb(imdb string) *Title
	ByTMDb(tmdb int) *Title
}

// movieMatch is FindMovieResult.
type movieMatch struct {
	title *Title
	typ   MatchType
}

var arabicRoman = radarr.ArabicRomanNumeralsMapping()

// yearFits is the arr's year tolerance: no parsed year (< 1800), the year, or the secondary year.
func yearFits(p *radarr.ParsedMovieInfo, t *Title) bool {
	return p.Year < 1800 || t.Year == p.Year || (t.SecondaryYear != 0 && t.SecondaryYear == p.Year)
}

// findMovie is ParsingService.FindMovie with search criteria for title t. imdbID is ReleaseInfo.ImdbId.ToString()
// (digits, "0" when none) and tmdbID ReleaseInfo.TmdbId.
func findMovie(p *radarr.ParsedMovieInfo, imdbID string, tmdbID int, t *Title, lib Library) *movieMatch {
	var m *movieMatch
	if strings.TrimSpace(imdbID) != "" && imdbID != "0" {
		m = byIMDb(p, imdbID, t, lib)
	}
	if m == nil && tmdbID > 0 {
		m = byTMDb(p, tmdbID, t, lib)
	}
	if m == nil {
		m = bySearchCriteria(p, imdbID, tmdbID, t)
	}
	return m
}

// byIMDb is TryGetMovieByImDbId: MovieRepository.FindByImdbId normalises the id (Parser.NormalizeImdbId).
func byIMDb(p *radarr.ParsedMovieInfo, imdbID string, t *Title, lib Library) *movieMatch {
	norm := radarr.NormalizeImdbID(imdbID)
	if norm == nil {
		return nil
	}
	var found *Title
	if lib != nil {
		found = lib.ByIMDb(*norm)
	} else if t.IMDb == *norm {
		found = t
	}
	if found != nil && yearFits(p, found) {
		return &movieMatch{found, MatchID}
	}
	return nil
}

// byTMDb is TryGetMovieByTmdbId.
func byTMDb(p *radarr.ParsedMovieInfo, tmdbID int, t *Title, lib Library) *movieMatch {
	var found *Title
	if lib != nil {
		found = lib.ByTMDb(tmdbID)
	} else if t.TMDb == tmdbID {
		found = t
	}
	if found != nil && yearFits(p, found) {
		return &movieMatch{found, MatchID}
	}
	return nil
}

// bySearchCriteria is TryGetMovieBySearchCriteria: the searched title's clean title, clean original title,
// alternative titles and translations (here Aliases) against the parsed titles, with arabic and roman numerals
// interchangeable; then the indexer ids. The last IMDb comparison is the arr's raw string comparison of
// ReleaseInfo.ImdbId (digits) with the movie's "tt…" id, which never matches; it is kept as is.
func bySearchCriteria(p *radarr.ParsedMovieInfo, imdbID string, tmdbID int, t *Title) *movieMatch {
	possible := []string{radarr.CleanMovieTitle(t.Name)}
	if t.OriginalTitle != "" {
		possible = append(possible, radarr.CleanMovieTitle(t.OriginalTitle))
	}
	for _, a := range t.Aliases {
		possible = append(possible, radarr.CleanMovieTitle(a))
	}
	clean := make([]string, len(p.MovieTitles))
	for i, mt := range p.MovieTitles {
		clean[i] = radarr.CleanMovieTitle(mt)
	}
	has := func(s string) bool {
		for _, c := range clean {
			if c == s {
				return true
			}
		}
		return false
	}
	match := false
	for _, pt := range possible {
		if has(pt) {
			match = true
			break
		}
		for _, mn := range arabicRoman {
			if has(strings.ReplaceAll(pt, mn.ArabicNumeralAsString, mn.RomanNumeralLowerCase())) {
				match = true
				break
			}
			for _, c := range clean {
				if strings.ReplaceAll(c, mn.ArabicNumeralAsString, mn.RomanNumeralLowerCase()) == pt {
					match = true
					break
				}
			}
			if match {
				break
			}
		}
		if match {
			break
		}
	}
	if match && yearFits(p, t) {
		return &movieMatch{t, MatchTitle}
	}
	if tmdbID > 0 && tmdbID == t.TMDb {
		return &movieMatch{t, MatchID}
	}
	if strings.TrimSpace(imdbID) != "" && imdbID == t.IMDb {
		return &movieMatch{t, MatchID}
	}
	return nil
}

// releaseIMDb is ReleaseInfo.ImdbId.ToString(): the indexer's IMDb id as an integer, "0" when none.
func releaseIMDb(s string) string {
	d := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "tt")
	n, err := strconv.Atoi(d)
	if err != nil || n < 0 {
		return "0"
	}
	return strconv.Itoa(n)
}
