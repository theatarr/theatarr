// Package adapt turns a parser's output into the custom-format input (format.Input), the way the arrs feed
// CustomFormatCalculationService.ParseCustomFormat(remote, size) for a release that has been parsed and mapped but
// has no indexer release attached (GET /api/v3/parse, and our decisions on a bare title):
//
//   - Radarr's Release Title condition matches ParsedMovieInfo.SimpleReleaseTitle, in which the parser has replaced
//     the movie title with "A.Movie"; Sonarr's matches ParsedEpisodeInfo.ReleaseTitle.
//   - Languages are the remote item's languages after AggregateLanguages (aggregate.go), not the parsed ones.
//   - Radarr's year is the parsed year; the Year condition falls back to the movie's year (format.Media.Year).
//   - Sonarr's release type is ParsedEpisodeInfo.ReleaseType.
//   - Size and indexer flags are 0 and there is no file name: there is no release and no file.
//
// RadarrRelease and SonarrRelease attach an indexer release, as DownloadDecisionMaker does at grab time: size and
// indexer flags come from it, and AggregateLanguages takes its indexer branch (the indexer's languages replace the
// parsed ones; a MULTi title adds the indexer's Multi Languages).
//
// What the arr takes from its library (the mapped movie or series: its title, year and original language, and the
// mapped episodes' titles) comes in as Movie, Series and Episode. Nil means the title mapped to nothing; the
// languages are then the parsed ones and nothing resolves "Original".
//
// Inputs come from the Radarr and Sonarr parser ports.
//
// Deviations:
//   - AggregateLanguages' title search (IndexOf with CurrentCultureIgnoreCase) compares rune by rune with Unicode
//     simple case folding, and its String.Remove counts runes, not UTF-16 units, as the parser ports do. A Remove
//     that would run past the end throws in the arr (the parse endpoint answers 500); here it is an error.
//   - Sonarr's ReleaseTokens is null only when the parser set none; the port writes "" for both null and an empty
//     token string, and "" falls back to the release title. With empty tokens the parsed languages are [Unknown]
//     either way, so the result is the same.
package adapt
