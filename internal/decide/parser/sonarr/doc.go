// Package sonarr is a port of Sonarr's release parser (src/NzbDrone.Core/Parser/, Qualities/ and Languages/ at
// Sonarr v4.0.20.3014, commit cab419a) to Go on regexp2.
//
// It covers Parser (ParseTitle, ParsePath, ParseReleaseGroup, CleanSeriesTitle and the other normalisers),
// QualityParser, LanguageParser, IsoLanguages, SceneChecker and ValidateParsedEpisodeInfo, with Sonarr's quality and
// language ids and names. ParsingService (series and episode lookup in the database) is out of scope.
//
// The output types marshal like parsedEpisodeInfo in Sonarr's GET /api/v3/parse: camelCase names, Sonarr's enum
// strings, null fields left out and the computed properties (isDaily, isAbsoluteNumbering, isPossibleSpecialEpisode,
// isPossibleSceneSeasonSpecial, releaseType) appended. Every pattern compiles through internal/netregex with the
// .NET options of the original.
//
// # Versions
//
// The plain functions are V4Plus, ours (parser_v4plus.go): the Sonarr v4 port (v4d, the base) with the Sonarr v5
// changes we chose one by one and a few fixes of our own, listed there with their sources. Its parses have the v4
// shape. TestV4Plus covers each change against the v4 base.
//
// Tests: testdata/parsertests holds Sonarr's ParserTests cases as data (regenerate with testdata/extract.py), run by
// TestParserTests on the v4 base. TestRecordedSamples compares the JSON byte for byte with answers recorded from Sonarr. TestCorpusAgreement
// compares with the collected corpus when THEATARR_CORPUS is set.
//
// # Deviations from Sonarr
//
//   - Positions are rune offsets, as regexp2 works on runes; .NET uses UTF-16 code units. They differ only for
//     characters outside the Basic Multilingual Plane (emoji, rare CJK), where releaseTokens could start at a
//     different place and a reversed title would keep surrogate pairs whole.
//   - Paths follow .NET on Linux, where the arrs run: '/' is the only separator. For a relative path with no
//     directory, .NET would use the name of the process's working directory in ParsePath's folder fallbacks; the port
//     uses an empty directory name.
//   - ConvertToNumerals (used after NFKC normalisation of episode numbers) maps Unicode decimal digits (Nd) to ASCII.
//     .NET also maps other numeric characters (Nl, No) through char.GetNumericValue; after NFKC only rare ones are
//     left (Roman numerals become letters either way), and here such a number fails to parse.
//   - In ParsePath, a number that cannot be parsed in the season-folder branch throws out of ParsePath in Sonarr;
//     the port returns nil.
//   - Each match has the netregex timeout (250 ms). A timeout counts as no match; Sonarr has no timeout.
//   - Sonarr logs (debug, warnings in ValidateForSeriesType); the port does not log.
//   - Null strings: ParseReleaseGroup returns nil and ParsedEpisodeInfo.ReleaseGroup is a nil pointer where Sonarr
//     has null; NormalizeImdbID returns "" for null.
//   - Case-insensitive matching uses regexp2's Unicode simple case folding; .NET 6+ uses its own case equivalence
//     table. They agree on the ASCII and Latin text the patterns target.
package sonarr
