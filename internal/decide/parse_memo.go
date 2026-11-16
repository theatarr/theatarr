package decide

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/indexer"
)

type parseMemoKey struct{}

type parseMemo struct {
	mu     sync.Mutex
	movies map[string]*radarr.ParsedMovieInfo
	series map[string]*sonarr.ParsedEpisodeInfo
}

// WithParseMemo scopes release parsing to one search or RSS item, shared across versions and criteria.
// Nested callers reuse that scope. No parse or release title survives in a global or engine-level cache.
func WithParseMemo(ctx context.Context) context.Context {
	if releaseParseMemo(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, parseMemoKey{}, &parseMemo{})
}

func releaseParseMemo(ctx context.Context) *parseMemo {
	if ctx == nil {
		return nil
	}
	m, _ := ctx.Value(parseMemoKey{}).(*parseMemo)
	return m
}

// ParseMovieRelease parses a release title, returning a caller-owned value.
func ParseMovieRelease(ctx context.Context, title string) *radarr.ParsedMovieInfo {
	m := releaseParseMemo(ctx)
	if m == nil {
		return radarr.ParseMovieTitle(title, false)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.movies == nil {
		m.movies = map[string]*radarr.ParsedMovieInfo{}
	}
	p, ok := m.movies[title]
	if !ok {
		p = radarr.ParseMovieTitle(title, false)
		m.movies[title] = p // nil parses are memoized too.
	}
	if p == nil {
		return nil
	}
	out := *p
	out.MovieTitles, out.Languages = slices.Clone(p.MovieTitles), slices.Clone(p.Languages)
	out.ReleaseGroup, out.HardcodedSubs = copyParsePointer(p.ReleaseGroup), copyParsePointer(p.HardcodedSubs)
	return &out
}

// ParseSeriesRelease parses a release title with the Sonarr parser, returning a caller-owned value.
func ParseSeriesRelease(ctx context.Context, title string) *sonarr.ParsedEpisodeInfo {
	m := releaseParseMemo(ctx)
	if m == nil {
		return parseSeriesTitle(title)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.series == nil {
		m.series = map[string]*sonarr.ParsedEpisodeInfo{}
	}
	p, ok := m.series[title]
	if !ok {
		p = parseSeriesTitle(title)
		m.series[title] = p
	}
	if p == nil {
		return nil
	}
	out := *p
	out.EpisodeNumbers, out.AbsoluteEpisodeNumbers = slices.Clone(p.EpisodeNumbers), slices.Clone(p.AbsoluteEpisodeNumbers)
	out.SpecialAbsoluteEpisodeNumbers = slices.Clone(p.SpecialAbsoluteEpisodeNumbers)
	out.Languages = slices.Clone(p.Languages)
	out.ReleaseGroup, out.AirDate = copyParsePointer(p.ReleaseGroup), copyParsePointer(p.AirDate)
	out.DailyPart = copyParsePointer(p.DailyPart)
	if p.SeriesTitleInfo != nil {
		info := *p.SeriesTitleInfo
		info.AllTitles = slices.Clone(info.AllTitles)
		out.SeriesTitleInfo = &info
	}
	return &out
}

// parseSeriesTitle is the Sonarr parse with the complete-series reading: a name with "Complete Series", "All
// Seasons" or "The Complete Collection" that does not parse, or parses to absolute numbers only (a year read as an
// episode), is parsed again with the phrase read as "S01"; a season pack from that is a complete-series pack
// (CompleteSeries). Anime series do not use it (series.parseRelease). Bare "Complete" is not read.
func parseSeriesTitle(title string) *sonarr.ParsedEpisodeInfo {
	p := sonarr.ParseTitle(title)
	if p != nil && (len(p.AbsoluteEpisodeNumbers) == 0 || len(p.EpisodeNumbers) > 0 || p.FullSeason) {
		return p
	}
	named, ok := indexer.CompleteSeriesName(title, "S01")
	if !ok {
		return p
	}
	c := sonarr.ParseTitle(named)
	if c == nil || !c.FullSeason || c.IsMultiSeason || len(c.EpisodeNumbers) > 0 || c.SeasonNumber != 1 ||
		strings.TrimSpace(c.SeriesTitle) == "" {
		return p
	}
	c.ReleaseTitle, c.CompleteSeries = title, true
	return c
}

func copyParsePointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
