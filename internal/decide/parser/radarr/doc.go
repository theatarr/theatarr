// Package radarr is a port of Radarr's release parser (src/NzbDrone.Core/Parser, Qualities and
// Languages @ a96bf7c, v6.4.4.10685) to Go on regexp2, through internal/netregex.
//
// It covers ParseMovieTitle and ParseMoviePath (title cleaning, year, IMDb and TMDb ids, edition,
// hardcoded subs, release hash, reversed and hashed titles, "crap" rejection), QualityParser,
// LanguageParser and IsoLanguages, ReleaseGroupParser, SceneChecker, the title helpers
// (CleanMovieTitle, NormalizeImdbId, ToUrlSlug, ...) and the roman numeral mapping. Every regex is
// Radarr's text with Radarr's RegexOptions, tried in Radarr's order with Radarr's fallbacks.
//
// The output types marshal like Radarr's GET /api/v3/parse: ParsedMovieInfo is parsedMovieInfo,
// with the computed movieTitle and primaryMovieTitle; enums are camelCased names ("bluray",
// "remux"); fields Radarr can leave null are pointers with omitempty, everything else is always
// present. Call runs one named operation and returns that JSON value, which is how the extracted
// ParserTests cases in testdata/parsertests are run (see testdata/extract.py for the schema).
//
// Not ported: ParsingService (mapping a parse to a movie in the database), the aggregation
// services that augment a parse after mapping, and logging.
//
// # Deviations
//
// Behaviour is kept identical where the .NET and Go semantics allow. Known differences:
//
//   - Reversed titles. Radarr builds the "un-reversed" title with $"{char[]}", which formats as
//     "System.Char[]", so a title matching ReversedTitleRegex becomes "System.Char[]" plus its
//     extension and never parses. Kept as is: GET /api/v3/parse returns no parsedMovieInfo for
//     such titles (checked on radarr_4k_debrid).
//   - String indices. regexp2 reports rune offsets where .NET reports UTF-16 offsets. The one
//     place an index is reused (cutting the title out of simpleReleaseTitle) works on runes, so
//     titles with characters outside the Basic Multilingual Plane (emoji) can differ by an offset.
//     Lengths compared in the port (IMDb id length, ISO code length) are rune counts.
//   - Case-insensitive comparisons. ContainsIgnoreCase and EndsWith(OrdinalIgnoreCase) use Go's
//     simple Unicode lower-casing, not .NET's invariant-culture comparison; ToLower and
//     ToLowerInvariant are strings.ToLower. Only exotic characters (ignorable code points, the
//     Turkish dotted I) can behave differently.
//   - char.IsLetterOrDigit (ValidateBeforeParsing) is tested per rune, so a letter outside the
//     BMP counts as a letter here and not in .NET.
//   - Paths follow .NET on Linux (Radarr's platform): '/' is the only separator and NUL the only
//     invalid path character. ParseMoviePath uses the path as given; .NET's FileInfo resolves a
//     bare file name against the working directory, here its directory name is empty.
//   - Exceptions. Radarr's ParseMovieTitle catches every exception and returns null. The one
//     reachable throw (String.Remove past the end when the title group's position does not fit
//     simpleReleaseTitle) returns nil here too. A regex timeout (netregex.MatchTimeout; Radarr has
//     none) counts as no match.
//   - RomanNumeral(int) checks the 1-3999 range when it is built instead of when it is formatted.
package radarr
