# Formats: the custom-format engine

Package `internal/decide/format`.

## What it contains

- **Engine.** Every Radarr specification (release title, edition, language, indexer flag, quality modifier, release
  group, resolution, size, source, year) and Sonarr's (the same minus edition, modifier and year, plus release type).
  Each is a port of its arr class, with `negate`, `required`, `exceptLanguage` and Language's own negated form.
  - Matching follows `CustomFormatCalculationService` and `SpecificationMatchesGroup`: conditions are grouped by
    implementation; a group matches when no required condition fails and at least one matches; a format matches when
    every group does. A format with no conditions matches everything, as in the arr.
  - `Calculate(formats, input)` returns the matched formats ordered by name. `Format.Evaluate` keeps per-group,
    per-condition results so a later decision can explain itself.
  - `Profile.Score` is `CalculateCustomFormatScore`: the sum of the format items whose format id matched.
- **Flavour.** Formats, profiles and inputs carry `radarr` or `sonarr`. Sources, indexer flags and languages are read
  in that flavour's numbering (the tables are in `enums.go`). The importer refuses a type the flavour lacks.
- **Input** is a narrow struct with no parser dependency. `ParseResponse.Input` builds it from an arr's
  `GET /api/v3/parse` body; `internal/decide/adapt` builds it from Theatarr's parsers.
- **Import.**
  - Arr API JSON: `ParseArrFormats` (`/api/v3/customformat`, fields as a list) and `ParseArrProfiles`
    (`/api/v3/qualityprofile`: format items, min, cutoff and min-upgrade format scores).
  - TRaSH JSON: `LoadTrash` reads `metadata.json` paths (formats by `trash_id`, quality profiles, cf-groups);
    `Trash.Profile` builds a profile's format items scored from its `trash_score_set` (or an override), falling back
    to `default` like recyclarr. Default cf-groups that include the profile can be added (required and default
    formats), as recyclarr does. All 243 Radarr and 236 Sonarr TRaSH formats, and every TRaSH profile, import and
    compile.

## What the arr passes to the calculation

Read in `ParseController.cs` (Radarr `a96bf7c`, Sonarr `cab419a`) and the services it calls:

- `ParseCustomFormat(remote, size: 0)`: **size is 0** in parse context.
- `ParsingService.Map` is called with no release, so **indexer flags are 0** (`Release?.IndexerFlags ?? 0`).
- **Languages** are `remote.Languages` after `AggregateLanguages` has augmented them: the response's top-level
  `languages`, not `parsedMovieInfo.languages`. An unknown parsed language becomes the movie's original language,
  for example.
- **File name is null**. Sonarr's `?path=` only changes how the title is parsed.
- `Map` always returns a remote item, so **formats are computed whenever the title parses**, even when no movie or
  series matched. **The score** is the matched item's profile's, or 0 when nothing matched.
- **Radarr's Release Title condition matches `SimpleReleaseTitle`**, in which the parser has replaced the movie title
  with `A.Movie`. **Sonarr's matches the full `ReleaseTitle`.** Radarr's Year condition falls back to the movie's year
  when no year was parsed.
- **History** (`HistoryResource.ToResource` → `ParseCustomFormat(history, movie/series)`): the title is re-parsed
  for the group, edition and simplified title. Quality and languages come from the record, and size and indexer flags
  from `data` (`long.TryParse`, `Enum.TryParse(ignoreCase)` of names such as `"G_Freeleech, G_Scene"`); Sonarr's
  release type also comes from `data`. Radarr sets the year to the movie's year. The score uses the movie's or
  series' current profile. The corpus check takes the re-parse from `parse.jsonl`, and compares only records whose
  parse mapped to the record's own movie or series.

## Agreement

`TestCorpus` (opt-in, `THEATARR_CORPUS`, a directory of arr API snapshots: formats, profiles, `GET parse` answers
and history) compares the engine with what each arr computed: the matched format names and the score, for every
parsed title and every history record whose parse mapped to its own movie or series. Titles the arr could not parse
(no `parsedEpisodeInfo`) have no formats and are not counted. A run on several Radarr and Sonarr instances with
TRaSH-based formats agrees on every name and every score; turning off the `required` rule as a test drops agreement
to 0%, so the comparison is not empty.

The conditions such formats use are mostly release title, release group and source, with quality modifier,
resolution, indexer flag, language and release type in smaller numbers. Size, year, edition and `exceptLanguage`
are covered by unit tests only; the parse context also fixes size and indexer flags at 0.

History needs Sonarr's `data.ReleaseType`: Sonarr writes it on a grab and `ParseCustomFormat(history)` reads it, so
a corpus that drops the key disagrees on Season Pack formats.

## Deviations (also in the package doc)

- Regexes go through `netregex` (regexp2, .NET semantics) with a 250 ms match timeout. The arrs set none. A timed-out
  condition counts as not satisfied and its error is reported.
- Names are sorted ordinally; .NET sorts by culture. This never changes which formats match or the score.
- The size condition saturates where .NET's `Convert.ToInt64` would throw (absurd maxima).

## Tests

- 32 cases from the arrs' only format fixtures (`LanguageSpecification/{Single,Multi,Original}LanguageFixture`, both
  arrs), in `testdata/arr_language_cases.json`.
- The arrs have no fixture for the calculation service or the specification groups. Table tests cover each
  condition type (nulls, negation, Original language, the `HasFlag` zero case, the size bounds, the year fallback), the
  group rule, multi-group formats and scoring.
- Import: an arr-shaped format and profile sample, a small synthetic TRaSH tree (score sets, fallback, default
  groups), and the full TRaSH checkout when present (`THEATARR_TRASH`, else the ref clone; skipped if absent).
- `TestCorpus` runs the check when `THEATARR_CORPUS` is set.

## Around the engine

- Theatarr's own condition types are further `Condition` implementations: `SeaDexSpecification` (`seadex.go`, both
  flavours) matches `best`, `alt` or `any` against `Input.SeaDex`; grouping by `Implementation()` keeps the arr rules.
  The arr-style "Seadex" format (seadexerr's four indexer-flag conditions) is built as `SeaDexSpecification` `best`
  (`format.SeaDexBestForSeaDexerr`): one bonus rule, SeaDex best only, wherever the torrent was found.
- Profile evaluation (allowed qualities, cutoff, upgrades) is in `internal/decide/profile` ([profiles.md](profiles.md)).
- `internal/decide/adapt` turns each parser's output into `Input`. For Radarr it produces `SimpleReleaseTitle`, and it
  ports `AggregateLanguages`, because formats see the augmented languages: languages found only in the movie (or
  episode) title are dropped, the indexer's multi-languages are added, the original language stands in when the
  result is empty or Unknown, and Original is expanded.
