// Package naming builds library file and folder names the way the arrs do, so links we create land at the paths
// Radarr and Sonarr would have given them (docs/design/library.md: same roots, same naming).
//
// It is a port of Radarr src/NzbDrone.Core/Organizer/FileNameBuilder.cs @ a96bf7c (movies) and Sonarr
// src/NzbDrone.Core/Organizer/FileNameBuilder.cs @ cab419a (series), plus MediaInfoFormatter.cs from both. Every
// pattern compiles through internal/netregex with the arr's options.
//
// # Inputs
//
// The builders take plain structs instead of the arrs' entities: [Movie] and [MovieFile], [Series], [Episode] and
// [EpisodeFile], and a [Config] read from the arr's GET /api/v3/config/naming body ([ParseArrConfig]). Media info
// comes in already formatted ([MediaInfo]), which is what the arrs' API returns for existing files; [Model.Format]
// formats our own probe the way MediaInfoFormatter formats ffprobe's output.
//
// # Deviations
//
//   - Token key normalization uses a direct Unicode pass with regexp2's word classes, so a match timeout cannot
//     change a handler's registration or lookup key.
//   - Custom formats are passed in by name, in the arr's order ([MovieFile.CustomFormats]); the arr computes them
//     itself with CustomFormatCalculationService and keeps those with includeCustomFormatWhenRenaming. The import
//     takes them from the grab's decision.
//   - Quality titles are passed in ([Quality.Title]): the arr reads them from its quality definitions, which a user
//     may rename. Callers give the instance's definitions (from_arr) or the default names.
//   - Translations ({Movie Title:xx}) have no data here: the token falls back to the movie title, as Radarr does when
//     a movie has no translation in that language.
//   - UpdateMediaInfoIfNeeded (re-probing old files whose media info schema is too old) has no equivalent: our probe
//     is always current.
//   - CultureInfo(code).TwoLetterISOLanguageName is golang.org/x/text/language's base language; a code it does not
//     know is upper-cased as .NET's ICU cultures do.
//   - Diacritical.Net's RemoveDiacritics is Unicode decomposition with the combining marks dropped, plus the
//     letters that do not decompose (ø, ł, đ, ß, æ, œ, þ, ð), mapped as that library does.
//   - Path lengths use Linux limits (LongPathSupport on Linux: 255 bytes per name, 4096 per path).
package naming
