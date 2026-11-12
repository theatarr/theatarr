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
// UnknownMovie or UnknownSeries. sonarrParser is the version's Sonarr parser (versions[*].sonarr; "" is v4).
func Related(ctx context.Context, rel indexer.Release, t *Title, sonarrParser string) bool {
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
	ver, err := sonarr.ParseVersion(sonarrParser)
	if err != nil {
		return true
	}
	s := seriesFor(ctx, t, ver)
	parsed := s.parseRelease(ctx, rel, nil)
	if parsed == nil || strings.TrimSpace(parsed.SeriesTitle) == "" {
		return false
	}
	if ok, _ := s.findSeries(parsed, rel.TVDB, rel.IMDb, s.findSceneMapping(parsed.SeriesTitle, s.lookupSeason(parsed)), nil); ok {
		return true
	}
	return t.AnimeMap.Named(ver.CleanSeriesTitle(parsed.SeriesTitle), rel.Title, ver.CleanSeriesTitle)
}

// seriesFor is the mapping's series for t, parsing release names through the search's parse memo.
func seriesFor(ctx context.Context, t *Title, ver sonarr.Version) *series {
	s := newSeries(t, ver)
	s.parseTitle = func(title string) *sonarr.ParsedEpisodeInfo { return ParseSeriesRelease(ctx, ver, title) }
	return s
}

// parseRelease is decideSeries' parse of a release name: the parse, the special-episode fallback, and v5's parse of
// a season's own title.
func (s *series) parseRelease(ctx context.Context, rel indexer.Release, search *SeriesSearch) *sonarr.ParsedEpisodeInfo {
	parsed := s.parseTitle(rel.Title)
	if parsed == nil || parsed.IsPossibleSpecialEpisode() {
		if sp := s.parseSpecialEpisodeTitle(parsed, rel.Title, rel.TVDB, rel.IMDb, search); sp != nil {
			parsed = sp
		}
	}
	if parsed == nil {
		// v5: a release named by a season's own title (an anime season pack), ParseSeasonTitle (nil in v4).
		parsed = parseSeriesRelease(ctx, s.ver, rel.Title, true)
	}
	return parsed
}
