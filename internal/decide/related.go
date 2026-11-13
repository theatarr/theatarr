package decide

import (
	"context"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// Related reports whether a release an id search returned is about title t, by the decision engine's own mapping:
// it carries one of t's ids, or its name maps to t (findMovie for a film; the series parse, FindSeries with scene
// mappings, or a franchise name for a series). A release it refuses is one Decide rejects as UnableToParse,
// UnknownMovie or UnknownSeries.
func Related(ctx context.Context, rel indexer.Release, t *Title) bool {
	if id := releaseIMDb(rel.IMDb); id != "0" && id == releaseIMDb(t.IMDb) {
		return true
	}
	if (rel.TMDb > 0 && rel.TMDb == t.TMDb) || (rel.TVDB > 0 && rel.TVDB == t.TVDB) {
		return true
	}
	if t.Kind != policy.KindSeries {
		p := ParseMovieRelease(ctx, rel.Title)
		if p == nil || p.PrimaryMovieTitle() == nil || strings.TrimSpace(*p.PrimaryMovieTitle()) == "" {
			return false
		}
		return findMovie(p, releaseIMDb(rel.IMDb), rel.TMDb, t, nil) != nil
	}
	s := seriesFor(ctx, t)
	parsed := s.parseRelease(rel, nil)
	if parsed == nil || strings.TrimSpace(parsed.SeriesTitle) == "" {
		return false
	}
	if ok, _ := s.findSeries(parsed, rel.TVDB, rel.IMDb, s.findSceneMapping(parsed.SeriesTitle, parsed.SeasonNumber), nil); ok {
		return true
	}
	return t.AnimeMap.Named(sonarr.CleanSeriesTitle(parsed.SeriesTitle), rel.Title, sonarr.CleanSeriesTitle)
}

// seriesFor is the mapping's series for t, parsing release names through the search's parse memo.
func seriesFor(ctx context.Context, t *Title) *series {
	s := newSeries(t)
	s.parseTitle = func(title string) *sonarr.ParsedEpisodeInfo { return ParseSeriesRelease(ctx, title) }
	return s
}

// parseRelease is decideSeries' parse of a release name: the parse and the special-episode fallback.
func (s *series) parseRelease(rel indexer.Release, search *SeriesSearch) *sonarr.ParsedEpisodeInfo {
	parsed := s.parseTitle(rel.Title)
	if parsed == nil || parsed.IsPossibleSpecialEpisode() {
		if sp := s.parseSpecialEpisodeTitle(parsed, rel.Title, rel.TVDB, rel.IMDb, search); sp != nil {
			parsed = sp
		}
	}
	return parsed
}
