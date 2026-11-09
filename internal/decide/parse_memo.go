package decide

import (
	"context"
	"slices"
	"sync"

	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
)

type parseMemoKey struct{}

type seriesParseKey struct {
	title       string
	version     sonarr.Version
	seasonTitle bool
}

type parseMemo struct {
	mu     sync.Mutex
	movies map[string]*radarr.ParsedMovieInfo
	series map[seriesParseKey]*sonarr.ParsedEpisodeInfo
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

// ParseSeriesRelease parses a release title in the version's Sonarr dialect, returning a caller-owned value.
func ParseSeriesRelease(ctx context.Context, version sonarr.Version, title string) *sonarr.ParsedEpisodeInfo {
	return parseSeriesRelease(ctx, version, title, false)
}

func parseSeriesRelease(ctx context.Context, version sonarr.Version, title string, seasonTitle bool) *sonarr.ParsedEpisodeInfo {
	parse := version.ParseTitle
	if seasonTitle {
		parse = version.ParseSeasonTitle
	}
	m := releaseParseMemo(ctx)
	if m == nil {
		return parse(title)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.series == nil {
		m.series = map[seriesParseKey]*sonarr.ParsedEpisodeInfo{}
	}
	key := seriesParseKey{title: title, version: version, seasonTitle: seasonTitle}
	p, ok := m.series[key]
	if !ok {
		p = parse(title)
		m.series[key] = p
	}
	if p == nil {
		return nil
	}
	out := *p
	out.EpisodeNumbers, out.AbsoluteEpisodeNumbers = slices.Clone(p.EpisodeNumbers), slices.Clone(p.AbsoluteEpisodeNumbers)
	out.SpecialAbsoluteEpisodeNumbers = slices.Clone(p.SpecialAbsoluteEpisodeNumbers)
	out.Languages, out.SeasonNumbers = slices.Clone(p.Languages), slices.Clone(p.SeasonNumbers)
	out.ReleaseGroup, out.AirDate = copyParsePointer(p.ReleaseGroup), copyParsePointer(p.AirDate)
	out.DailyPart = copyParsePointer(p.DailyPart)
	if p.SeriesTitleInfo != nil {
		info := *p.SeriesTitleInfo
		info.AllTitles = slices.Clone(info.AllTitles)
		out.SeriesTitleInfo = &info
	}
	return &out
}

func copyParsePointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
