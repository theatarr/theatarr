package adapt

import (
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
)

// Movie is what the format input needs from the movie a release mapped to.
type Movie struct {
	Title            string
	Year             int
	OriginalLanguage int // Radarr language id
}

// Series is what the format input needs from the series a release mapped to.
type Series struct {
	Year             int // no Sonarr condition reads it; kept so the input matches the arr's
	OriginalLanguage int // Sonarr language id
}

// Episode is a mapped episode.
type Episode struct {
	Title string
}

// Release is what the format input and AggregateLanguages read from an indexer release (ReleaseInfo), in the
// flavour's numbering.
type Release struct {
	// Title is ReleaseInfo.Title, the title as the indexer gave it.
	Title string
	// Languages are ReleaseInfo.Languages: what the indexer said (torznab language attributes), after the
	// indexer's own clean-up. Empty when it said nothing.
	Languages []int
	// MultiLanguages is the indexer's Multi Languages setting; nil when the indexer has none or is unknown.
	MultiLanguages []int
	// Size in bytes and IndexerFlags as the arr's IndexerFlags bits.
	Size         int64
	IndexerFlags int
}

var radarrLangs = langOps{
	parse:     func(s string) []int { return radarrIDs(radarr.ParseLanguages(s)) },
	normalize: radarr.NormalizeMovieTitle,
	multi:     radarr.HasMultipleLanguages,
}

var sonarrLangs = langOps{
	parse:     func(s string) []int { return sonarrIDs(sonarr.ParseLanguages(s)) },
	normalize: sonarr.NormalizeEpisodeTitle,
	multi:     sonarr.HasMultipleLanguages,
}

// Radarr builds the format input for a Radarr parse, as ParseCustomFormat(remoteMovie, 0) sees it after mapping and
// augmentation. m is the mapped movie, or nil. A nil parse gives a nil input: the arr computes no formats then.
func Radarr(p *radarr.ParsedMovieInfo, m *Movie) (*format.Input, error) {
	return RadarrRelease(p, m, nil)
}

// RadarrRelease is Radarr with an indexer release attached, as DownloadDecisionMaker sees it:
// ParseCustomFormat(remoteMovie, release.Size) with the release's indexer flags, and languages aggregated with the
// indexer branch. A nil release is the parse context.
func RadarrRelease(p *radarr.ParsedMovieInfo, m *Movie, rel *Release) (*format.Input, error) {
	if p == nil {
		return nil, nil
	}
	q := p.Quality.Quality
	in := &format.Input{
		Flavour:      format.Radarr,
		ReleaseTitle: format.Str(p.SimpleReleaseTitle),
		ReleaseGroup: p.ReleaseGroup,
		Edition:      format.Str(p.Edition),
		Resolution:   q.Resolution,
		Year:         p.Year,
	}
	var err error
	if in.Source, err = format.Radarr.SourceValue(q.Source.String()); err != nil {
		return nil, err
	}
	if in.Modifier, err = format.ModifierValue(q.Modifier.String()); err != nil {
		return nil, err
	}
	tokens := p.SimpleReleaseTitle // SimpleReleaseTitle ?? ReleaseTitle
	if tokens == "" {
		tokens = p.ReleaseTitle
	}
	var titles []string
	original := langUnknown
	if m != nil {
		titles = []string{m.Title}
		original = m.OriginalLanguage
		in.Media = &format.Media{Year: m.Year, OriginalLanguage: m.OriginalLanguage}
	}
	if in.Languages, in.LanguagesAssumed, err = aggregateLanguages(radarrLangs, radarrIDs(p.Languages), tokens, m != nil, titles,
		original, rel); err != nil {
		return nil, err
	}
	if rel != nil {
		in.Size, in.IndexerFlags = rel.Size, rel.IndexerFlags
	}
	return in, nil
}

// Sonarr builds the format input for a Sonarr parse, as ParseCustomFormat(remoteEpisode, 0) sees it after mapping
// and augmentation. s is the mapped series, or nil; episodes are the mapped episodes. A nil parse gives a nil input.
func Sonarr(p *sonarr.ParsedEpisodeInfo, s *Series, episodes []Episode) (*format.Input, error) {
	return SonarrRelease(p, s, episodes, nil)
}

// SonarrRelease is Sonarr with an indexer release attached (see RadarrRelease).
func SonarrRelease(p *sonarr.ParsedEpisodeInfo, s *Series, episodes []Episode, rel *Release) (*format.Input, error) {
	if p == nil {
		return nil, nil
	}
	q := p.Quality.Quality
	in := &format.Input{
		Flavour:      format.Sonarr,
		ReleaseTitle: format.Str(p.ReleaseTitle),
		ReleaseGroup: p.ReleaseGroup,
		Resolution:   q.Resolution,
	}
	var err error
	if in.Source, err = format.Sonarr.SourceValue(q.Source.String()); err != nil {
		return nil, err
	}
	if in.ReleaseType, err = format.ReleaseTypeValue(p.ReleaseType().String()); err != nil {
		return nil, err
	}
	tokens := p.ReleaseTokens // ReleaseTokens ?? ReleaseTitle
	if tokens == "" {
		tokens = p.ReleaseTitle
	}
	var titles []string
	original := langUnknown
	if s != nil {
		for _, e := range episodes {
			titles = append(titles, e.Title)
		}
		original = s.OriginalLanguage
		in.Media = &format.Media{Year: s.Year, OriginalLanguage: s.OriginalLanguage}
	}
	if in.Languages, in.LanguagesAssumed, err = aggregateLanguages(sonarrLangs, sonarrIDs(p.Languages), tokens, s != nil, titles,
		original, rel); err != nil {
		return nil, err
	}
	if rel != nil {
		in.Size, in.IndexerFlags = rel.Size, rel.IndexerFlags
	}
	return in, nil
}

func radarrIDs(ls []radarr.Language) []int {
	out := make([]int, len(ls))
	for i, l := range ls {
		out[i] = l.ID
	}
	return out
}

func sonarrIDs(ls []sonarr.Language) []int {
	out := make([]int, len(ls))
	for i, l := range ls {
		out[i] = l.ID
	}
	return out
}
