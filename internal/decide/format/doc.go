// Package format is the custom-format engine: the arr specification types, the arr matching rules, profile
// scores, and import from arr API JSON and TRaSH JSON.
//
// It ports Radarr src/NzbDrone.Core/CustomFormats @ a96bf7c and Sonarr src/NzbDrone.Core/CustomFormats @ cab419a,
// plus QualityProfile.CalculateCustomFormatScore from both.
//
// Matching is the arrs':
//   - each condition applies its own negate (Language has its own negated form: null languages never match);
//   - conditions of the same implementation form a group;
//   - a group matches when no required condition fails and at least one condition matches;
//   - a format matches when every group matches, so a format without conditions matches everything.
//
// A profile's score is the sum of its format items whose format matched, compared by format id.
//
// Formats, profiles and inputs carry a Flavour. Radarr and Sonarr number quality sources, indexer flags and
// languages differently, and some types exist in one arr only (Edition, Quality Modifier and Year in Radarr;
// Release Type in Sonarr). The Release Title condition matches Radarr's simplified title but Sonarr's full release
// title; Input.ReleaseTitle holds whichever the flavour uses.
//
// Deviations:
//   - Regexes compile through netregex (regexp2 with .NET semantics) with its match timeout. The arrs set no
//     timeout. A condition whose regex times out counts as not satisfied, whether negated or not, and the error is
//     returned with the result.
//   - Formats are ordered by name ordinally; .NET's OrderBy(Name) uses the current culture. Order never changes
//     which formats match or the score.
//   - The Size condition's Gigabytes() conversion saturates where .NET's Convert.ToInt64 would throw.
//   - SeaDexSpecification is ours (seadex.go), in both flavours: value best, alt or any against Input.SeaDex, the
//     release's SeaDex standing. The arrs reach SeaDex only through a tagged feed such as seadexerr's.
//   - Language "Original" resolves through Input.Media. The arr also requires the parsed info to be non-null; an
//     Input always comes from a parse, so that holds.
package format
