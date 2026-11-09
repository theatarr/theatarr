package format

// Building an Input from an arr's GET /api/v3/parse response, the way Radarr.Api.V3/Parse/ParseController.cs
// @ a96bf7c and Sonarr.Api.V3/Parse/ParseController.cs @ cab419a feed the calculation:
//
//   - Both call ParseCustomFormat(remote, size: 0) whenever the title parses: ParsingService.Map always returns a
//     remote movie/episode, with Movie/Series null when nothing matched. Map is called without a release, so
//     IndexerFlags is 0 (Release?.IndexerFlags ?? 0).
//   - Languages are remote.Languages after the aggregation service has augmented them (the response's top-level
//     "languages"), not parsedInfo.languages.
//   - The file name is null: the parse endpoint scores a title, not a file. Sonarr's optional ?path= changes how the
//     title is parsed, not the input fields.
//   - Movie/Series is the mapped one (null in Radarr when no movie matched), which resolves Original language and
//     Radarr's Year fallback. The score is the mapped item's quality profile's, else 0.

import (
	"fmt"
)

// ParseResponse is the part of a GET /api/v3/parse body the format check reads.
type ParseResponse struct {
	Title             string          `json:"title"`
	ParsedMovieInfo   *ParsedInfoJSON `json:"parsedMovieInfo"`
	ParsedEpisodeInfo *ParsedInfoJSON `json:"parsedEpisodeInfo"`
	Movie             *MediaJSON      `json:"movie"`
	Series            *MediaJSON      `json:"series"`
	Languages         []LanguageJSON  `json:"languages"`
	CustomFormats     []FormatRefJSON `json:"customFormats"`
	CustomFormatScore *int            `json:"customFormatScore"`
}

// ParsedInfoJSON is parsedMovieInfo or parsedEpisodeInfo, the fields formats use.
type ParsedInfoJSON struct {
	ReleaseTitle       *string `json:"releaseTitle"`
	SimpleReleaseTitle *string `json:"simpleReleaseTitle"`
	Quality            *struct {
		Quality struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			Source     any    `json:"source"`
			Resolution int    `json:"resolution"`
			Modifier   any    `json:"modifier"`
		} `json:"quality"`
	} `json:"quality"`
	Languages    []LanguageJSON `json:"languages"`
	ReleaseGroup *string        `json:"releaseGroup"`
	Edition      *string        `json:"edition"`
	Year         int            `json:"year"`
	ReleaseType  any            `json:"releaseType"`
}

// MediaJSON is the movie or series of a parse response.
type MediaJSON struct {
	ID               int           `json:"id"`
	Title            string        `json:"title"`
	Year             int           `json:"year"`
	QualityProfileID int           `json:"qualityProfileId"`
	OriginalLanguage *LanguageJSON `json:"originalLanguage"`
}

// LanguageJSON is an arr language.
type LanguageJSON struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// FormatRefJSON is a matched format as the parse response lists it.
type FormatRefJSON struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Mapped reports whether the title mapped to a movie or series. Only then does the arr report a non-zero score.
func (r *ParseResponse) Mapped() bool {
	return r.media() != nil
}

func (r *ParseResponse) media() *MediaJSON {
	if r.Movie != nil {
		return r.Movie
	}
	return r.Series
}

// QualityProfileID is the mapped item's profile, 0 when none.
func (r *ParseResponse) QualityProfileID() int {
	if m := r.media(); m != nil {
		return m.QualityProfileID
	}
	return 0
}

// Input rebuilds the CustomFormatInput the arr used for this response. ok is false when the response has no parsed
// info (the title did not parse), in which case the arr computed no formats.
//
// Languages come from the top-level "languages" (remote.Languages after augmentation). When that is absent the arr
// built no input; the parsed languages are used then so the result is still defined.
func (r *ParseResponse) Input(fl Flavour) (in *Input, ok bool, err error) {
	pi := r.ParsedMovieInfo
	if fl == Sonarr {
		pi = r.ParsedEpisodeInfo
	}
	if pi == nil {
		return nil, false, nil
	}
	in = &Input{Flavour: fl, ReleaseGroup: pi.ReleaseGroup}
	if fl == Radarr {
		in.ReleaseTitle = pi.SimpleReleaseTitle
		in.Edition = pi.Edition
		in.Year = pi.Year
	} else {
		in.ReleaseTitle = pi.ReleaseTitle
		if pi.ReleaseType != nil {
			if in.ReleaseType, err = enumAny(pi.ReleaseType, ReleaseTypeValue); err != nil {
				return nil, false, err
			}
		}
	}
	if q := pi.Quality; q != nil {
		in.Resolution = q.Quality.Resolution
		if q.Quality.Source != nil {
			if in.Source, err = enumAny(q.Quality.Source, fl.SourceValue); err != nil {
				return nil, false, err
			}
		}
		if fl == Radarr && q.Quality.Modifier != nil {
			if in.Modifier, err = enumAny(q.Quality.Modifier, ModifierValue); err != nil {
				return nil, false, err
			}
		}
	}
	langs := r.Languages
	if langs == nil {
		langs = pi.Languages
	}
	if langs != nil {
		in.Languages = make([]int, len(langs))
		for i, l := range langs {
			in.Languages[i] = l.ID
		}
	}
	if m := r.media(); m != nil {
		in.Media = &Media{Year: m.Year}
		if m.OriginalLanguage != nil {
			in.Media.OriginalLanguage = m.OriginalLanguage.ID
		}
	}
	return in, true, nil
}

func enumAny(v any, read func(string) (int, error)) (int, error) {
	switch x := v.(type) {
	case float64:
		return int(x), nil
	case string:
		return read(x)
	default:
		return 0, fmt.Errorf("unexpected enum value %v", v)
	}
}
