# The decision engine (`internal/decide`, `theatarr decide`)

One call decides a release for one version of a title. The engine builds on the Radarr and Sonarr parser ports,
`adapt`, `format` and `profile`; series decisions are in [series.md](series.md). Where this sits:
[overview.md](overview.md).

Search decisions carry explicit search context so movie minimum-seeder and RSS-only rules match arr behaviour;
episode title checks are made before download ([below](#arr-rules-propers-seeders-episode-titles-repeats)).

Search callers establish a request-scoped parse memo, shared across versions and search criteria. Movie release
parses and each Sonarr dialect's title/season-title parses are cached separately, including failed parses. Every
caller receives an independent copy of slices, nested title info and optional values, so manual overrides and
episode mapping cannot change another decision. Shadow grabs and each RSS item use the same scoped mechanism;
there is no global cache. `BenchmarkSearchParse` reads 300 releases from a scratch SQLite database under `/tmp`
and models three versions with two criteria each.

## How a release is decided

- **Policy profiles to engine profiles** (`decide.New`):
  - **A policy profile** is built from:
    - its qualities, best first; groups are kept;
    - its cutoff and upgrade flag;
    - the three format scores (`min_score`, `cutoff_score`, `min_upgrade_step`, default 1);
    - its sizes, as MB per minute, which override the default quality definitions for that profile;
    - format scores by policy key or by trash_id.
  - **Quality items** are organized from the arr's default profile as recyclarr does (`profile.FromItems`).
    Unlisted qualities stay in the profile, not allowed, so upgrade comparisons see the arr's order.
  - **Formats:**
    - every format of the version's flavour is computed, and the profile scores some of them;
    - TRaSH formats come by trash_id from a TRaSH-Guides checkout (`-trash`, `THEATARR_TRASH`);
    - Theatarr's own formats come from arr-shaped JSON files;
    - a format that fails to build is an error when the profile scores it, otherwise a warning.
  - **`from_arr: <instance>/<profile>`** takes the whole profile from an arr instance:
    - the instance's profile, all its formats and its quality definitions;
    - `config/mediamanagement` and `config/indexer` when they are available (propers, maximum size, preferred
      flags);
    - the source is a snapshot directory in the corpus layout, or live GETs through `internal/arr` with the
      `import.arr` key.
  - Every problem is a `*policy.Errors` entry with its path, such as `profiles[x].formats[y]` or
    `profiles[shadow].from_arr`.
- **`Engine.Decide(ctx, release, title, version, existing)`**, pure and deterministic. For a movie version it
  follows Radarr's `DownloadDecisionMaker` with search criteria:
  1. **Parse** with the Radarr port. A title it cannot read is rejected `UnableToParse`.
  2. **Map** the release to the title (port of `ParsingService.FindMovie`):
     - the indexer's IMDb id, then its TMDb id; through `Library`, these can name another title in the library;
     - then the searched title's clean title, original title and aliases, with arabic and roman numerals
       interchangeable;
     - year tolerance: no year, the year, or the secondary year;
     - no match is rejected `UnknownMovie`, and an id naming another title is rejected `WrongMovie`.
  3. **Languages:**
     - `IndexerBase.CleanupReleases`: a MULTi release the indexer gave no languages for gets the indexer's Multi
       Languages;
     - then `AggregateLanguages` with its indexer branch, ported
       (`adapt.RadarrRelease`). Release languages replace the parsed ones; a MULTi title adds the indexer's
       `multi_languages`.
  4. **Formats and score:**
     - the format input carries the release's size and indexer flags. Flags come from torznab tags and volume
       factors, as in `TorznabRssParser.GetFlags`;
     - formats are calculated, and each matched format reports its score in the profile.
     - Movies with a known release size and runtime-based size bounds temporarily reject with `UnknownRuntime`
       until real runtime is loaded. They never judge size against a guessed 110 minutes. RSS leaves these items
       eligible for reconsideration; the lower-level Radarr specification keeps its fallback for parity tests.
  5. **Specifications:** `profile.Engine.Evaluate` runs with the version's active Theatarr link, falling back to the arr's
     `version_file`; series merge the two per episode, with Theatarr's link winning.
     - Its formats are computed as `ParseCustomFormat(MovieFile)` does: release title from the scene name, else
       the original file name, else the relative path.
     - Theatarr links carry the import's original parse, languages, size, flags, formats and score (`file_metadata`,
       migration 00038); comparisons re-score the saved inputs against the current profile, without parsing a
       renamed library file. Upgrade reasons and approval holds therefore see Theatarr's own imported files.
  6. **Theatarr's rule**, listed first and marked `ours`: an indexer the version does not list is refused
     (`IndexerNotInVersion`). That list is the user's choice of where releases come from; privacy is a label and
     refuses nothing.
  7. **Upgrade:** yes or no, with the reason.
     - Yes says "better quality / revision / format score: A over B".
     - No gives the upgrade specification's message, or "not an upgrade: …".
- **`Related(ctx, release, title, sonarr)`:** whether a release an id search returned is about the title, for the
  indexer adapters' padding check (indexers.md, "Id searches and padding"). It keeps a release that carries one of
  the title's ids, else runs the same parse and mapping as `Decide` (`findMovie`; the series parse, `FindSeries` with
  scene mappings, or a franchise name). A release it refuses is one `Decide` rejects as `UnableToParse`,
  `UnknownMovie` or `UnknownSeries`.
- **`Rank(decisions)`:** per version, accepted decisions first, ordered by `profile.Engine.Prioritize` (the arr's
  comparer, with Theatarr's cache tie-break after the format score: docs/design/router.md "Deviations"); then the
  rejected ones. The decision fills the tie-break's cache standing from the release's cache evidence (`Subject.Cached`).
- **Shadow parity** (`internal/decide/shadow`) replays arr grabs:
  - from Theatarr's database: `arr_history`, with the file before each grab rebuilt from import and delete events, and the
    grab-time score from `data.customFormatScore`;
  - or from the corpus: `history.jsonl`, with the movie from `parse.jsonl`.
- **`theatarr decide`:**
  - `theatarr decide -title ID|-tmdb N -version KEY [-size 25GB] [-flags freeleech,internal|G_Freeleech]
    [-languages French] [-seeders N] [-indexer KEY] [-imdb tt…] [-no-existing] [-json] 'Release.Title'`
    prints the decision and its score breakdown.
  - `-search` searches the version's indexers through the gateway, decides every result and prints them ranked. It
    refuses off the VPN with `theatarr search`'s error.
  - `theatarr decide shadow -instance NAME -version KEY [-corpus DIR | -db FILE -aliases DIR] [-dump FILE]` prints the
    parity report.

```
release   Argylle.2024.2160p.UHD.BluRay.REMUX.DV.HDR.HEVC.TrueHD.Atmos.7.1-FraMeSToR
title     #87 Argylle (2024)  version movie-remux (#596), profile remux (from_arr radarr_4k/Remux + WEB 2160p)
parsed    Argylle 2024, group FraMeSToR, edition -, match Title, languages English
quality   Remux-2160p v1
formats   DV Boost +1000, HDR +500, Remux Tier 01 +1950, TrueHD ATMOS +5000 = 8450
existing  WEBDL-2160p, DD+ ATMOS +3000, DV Boost +1000, HDR +500, WEB Tier 01 +1700 = 6200
upgrade   yes: better quality: Remux-2160p over WEBDL-2160p
decision  ACCEPTED
```

## Release check

Settings › Profiles › **Check a release** (`/admin/settings/profiles/check`) is the admin screen for `POST /decide`.
Paste a release name, find a title in the library, pick one of its libraries, and optionally give a size and an info
hash (sent as `info_hash`, so a SeaDex-curated release gets its standing). It shows:

- accepted or rejected, with every rejection's reason and message (`ours` marks Theatarr's own rules, `temporary`
  those that may pass later);
- the parsed name, quality and revision, and the quality's rank among the profile's allowed qualities
  (`profile_rank` of `profile_ranks`, best first; a group's qualities share a rank, 0 is not allowed);
- the matched formats with their scores and the total;
- the library's current file (quality, rank, formats, score) and whether the release would upgrade it, with the
  engine's reason. A name that could not be parsed or did not match the title says the comparison is not available,
  not that the library has no file.

Size, indexer, seeders and info hash it was not given show as unknown. A name and indexer with no language get the
title's original language, as in the arrs; the decision marks that (`languages_assumed`) and the screen labels it as
assumed (unknown when the title's language is unknown too). The screen says how the comparer orders releases
([profiles.md](profiles.md), Ranking): quality rank, then revision, then format score, so a higher score never
beats a better quality; for a library with `prefer_seadex` (the decision's `prefer_seadex`), SeaDex standing first
(configured winner, SeaDex best, other listed, the rest), then that order. With `prefer_seadex` and no info hash it
warns that a SeaDex-curated release may be judged differently. It records, searches and grabs nothing.

## Frozen arr profiles

The migration wizard's quality step (`internal/migration`, `freezeArr`) freezes a stored arr snapshot into native
profiles and a format set, so a version decides with the arr's own profile and formats without `from_arr`.

Native profiles may select a set with `format_set`. Its keys are the arr's display names, so the same name can
exist in several sets, and zero-score formats outside the selected set cannot enter names or rejection text:

```yaml
format_sets:
  radarr-4k-web:
    flavour: radarr
    formats:
      Remux Tier 01: {ref: Remux Tier 01, include_when_renaming: true}
      Local: {file: formats/local.json, flavour: radarr}
profiles:
  radarr-4k-web:
    format_set: radarr-4k-web
    qualities: [WEBDL-2160p]
    cutoff: WEBDL-2160p
    formats: {Remux Tier 01: 1950}
```

`ref` names an existing global `formats` definition while keeping the set key as its display name. Set entries
can also use `trash_id` or `file` directly, including a shared file from another set. Unknown sets/references,
mixed flavours and duplicate names within a set are errors. Profiles without `format_set` keep evaluating all
same-flavour global formats; set-only entries do not enter those profiles.

A frozen profile includes `quality_order`: every quality and group, best first, including disabled entries. `qualities`
still selects allowed items and must match their order and group membership in that list. Omitting `quality_order`
keeps existing policies' default disabled-quality ordering. Size entries use `preferred_zero: true` to preserve
arr `preferredSize: 0` (which ranks smaller releases first); plain `preferred: 0` keeps its existing unlimited
meaning. Verification includes disabled-quality files and standard/daily/anime, specials and multi-episode naming.

Unsupported arr settings are refused rather than silently dropped: indexer maximum size/preferred flags,
nested groups, unknown languages, custom quality naming titles, disabled renaming/illegal-character replacement
and custom colon replacement. Profiles with upgrades disabled can still change the profile display name in
messages.

## History parity

`theatarr decide shadow` replays an instance's grab history against the engine, with the instance's own profile
through `from_arr`. The policy is `internal/decide/shadow/testdata/policy`; the data is a copy of the import-arr
database, or a corpus of exported arr answers. For each grab it compares acceptance, quality and score with the
arr's (the grab-time score from `data.customFormatScore`, else today's).

The disagreements such a replay finds come from the data or the profile's history, not the engine:

| Cause | Engine or data? |
|---|---|
| **The file rebuilt from history is not the arr's file at the time** (`DiskCustomFormatScore`, `DiskHigherPreference`, `Repack…`): for example a mass re-grab when the arr had no file, though history shows no delete | Data. Deciding those grabs with no file makes them agree |
| **The profile changed since the grab:** same score as the arr's today, different from the grab-time score | Profile history |
| Below today's minimum score; the grab-time score was ≥ 0 | Profile history |
| `QualityNotWanted`: the quality is not in today's profile | Profile history |
| `UnknownMovie`: the release year is the arr's then-year or secondary year, or a translated or alternative title the database does not hold | Data: names for mapping ([below](#names-for-mapping)) |
| Grabbed by hand although rejected: below the minimum score, the wrong language, below the minimum size | The arr allows grabbing a rejected release by hand |
| The movie is on another arr profile than the one compared | Comparison scope |
| Languages from the indexer (torznab language attributes) not in history | Data |

History has no rejected releases, so specifications the arr enforced (queue, blocklist, delay, availability,
release profiles: [profiles.md](profiles.md)) show no disagreement here. Releases the arr rejects and Theatarr
accepts would show in an interactive-search comparison.

Reproduce:
```
THEATARR_CORPUS=<corpus> THEATARR_DECIDE_DB=<copy of the import-arr database> \
  go test -run Parity -v ./internal/decide/shadow/
bin/theatarr decide shadow -db <copy> -arr-snapshot <corpus> \
  -aliases <corpus> -instance radarr_4k -version movie-remux -dump replay.tsv
```
`decide shadow` reads its settings from `<copy>`: import the replay policy into it first if it has none
(`bin/theatarr settings import -policy <copy of internal/decide/shadow/testdata/policy> -db <copy> -confirm`, prepared as in
[series.md](series.md), season pack rule, Reproduce).

## Arr rules: propers, seeders, episode titles, repeats

- **Proper (RSS only):** revision upgrades reject when propers are disabled, or the held file was added before
  local midnight seven days ago. `doNotPrefer` and all searches skip this check. Arr files use `added_at`; Theatarr
  links keep a recorded file-added time, falling back to `link.created_at`. Movie search callers mark the context
  with `decide.WithSearch`; series callers supply `SeriesSearch`.
- **Torrent seeders:** every torrent indexer kind defaults `min_seeders` to 1. An explicit 0 disables it; negative
  values fail validation. Known counts below the minimum reject in RSS and every search type; unknown counts
  (`-1`) and removed indexers pass. SeaDex's synthetic counts and Torrentio/DMM counts use the same comparison.
- **Episode title:** a naming pattern using Episode Title/CleanTitle temporarily holds missing or exactly `TBA`
  titles until exactly 48 hours after airing; unknown/future dates remain held. Standard, daily and anime
  patterns follow Sonarr's selection, including the anime absolute-number requirement and rename-off exemption.
  The arr's `episodeTitleRequired` Always/Never/BulkSeasonReleases setting is honored (default Always), including
  its bulk date/count conditions. Sonarr checks this at import; Theatarr checks before download. A missing `from_arr`
  naming snapshot skips the hold rather than guessing the naming pattern.
- **Already imported:** the latest grab and an import with its download ID block the same hash or release name
  only when their qualities differ and the item still has a file. This prevents the arr's quality-mismatch loop;
  equal-quality repeats remain subject to the usual upgrade rules. Theatarr and imported arr history are scoped to the
  version. Legacy links use their saved quality/title when parsed metadata is absent.
  Imported arr history stores no revisions, so revision-only differences cannot be reconstructed. Optional
  `config-downloadclient.json` snapshots honor disabled completed-download handling; otherwise it defaults on.
- **Movie availability:** interactive searches, explicit search commands and manual grabs bypass minimum
  availability. Wanted/research searches, RSS and pushed releases keep the automatic availability gate.

Tests mirror the arr fixtures for proper settings/dates, seeder thresholds, title windows/naming/config and
grab/import matching, with database coverage for history/version isolation and manual-versus-automatic routing.

Without `episodeTitleRequired` or completed-download handling in the arr snapshot, these settings use the arr
defaults. Movie search callers mark their context with `decide.WithSearch` (acquisition's interactive, command and
wanted searches, and `theatarr decide -search`).

## Tests

- `internal/decide`: table tests for building, deciding, upgrades, ranking, mapping and flags.
  - **Build:** the policy profile, the `from_arr` profile from a trimmed fixture snapshot (11 formats, the profile,
    the definitions), and errors with their paths.
  - **Decide:** quality not wanted, minimum score, size limits, unknown movie and wrong year, mapping by IMDb id,
    unparseable titles, the version's indexer list, MULTi with and without the indexer's multi languages,
    indexer languages, freeleech from the volume factor, and the `from_arr` profile with its original-language
    rule.
  - **Upgrades:** better quality, a lower quality refused, better score, the same release, and a repack.
  - **Rank;** mapping by alias, roman numeral, secondary year, and an id naming another title (`WrongMovie`); the
    torznab flag mapping.
- `internal/decide/adapt`: the indexer cases of both arrs' `AggregateLanguagesFixture`, plus release languages and
  unmapped titles.
- `internal/decide/shadow`: rebuilding history from a small database. The parity runs are opt-in
  (`THEATARR_CORPUS`, `THEATARR_DECIDE_DB`).
- `cmd/theatarr`:
  - `theatarr decide` on a temporary database, as text and JSON;
  - `-search` refuses off the VPN without dialling anything but the local echo.

## Related policy and package pieces

- **`internal/policy`:**
  - `profiles[x].from_arr` and `profiles[x].propers`;
  - `indexers[x].multi_languages`;
  - validation with paths: a `from_arr` profile with other fields, an unknown instance, an instance of the wrong
    flavour for the version, a bad propers value, unknown or repeated languages. `Original` is allowed, as in the
    arrs.
- **`internal/decide/adapt`:** `Release`, `RadarrRelease` and `SonarrRelease`, and the indexer branch of
  `AggregateLanguages`. `Radarr` and `Sonarr` are wrappers.
- **`internal/decide/profile`:** `FromItems`, which exposes the recyclarr organiser for policy profiles.

## Names for mapping

Migration `00009` adds `title.secondary_year` and `title_alias`:

- `theatarr import-arr` stores Radarr's `alternateTitles` and `secondaryYear` from the movie resource;
- `--enrich` stores TMDb alternative titles and translated titles ([import-arr.md](import-arr.md));
- `LoadMovies` and `LoadTitle` fill `Title.Aliases` (every source, distinct) and `Title.SecondaryYear`. The
  mapping code is Radarr's `TryGetMovieBySearchCriteria` (clean title, original title, alternative titles,
  translations, roman numerals, secondary year).
- `theatarr decide shadow -aliases` adds the corpus names to the stored ones.
- Titles known only by a translation (an Italian or German release name) map only once `--enrich` has stored the
  TMDb translations.

## Languages and SeaDex flags

- **Languages from the indexer:** `indexer.Release.Languages` holds the Torznab `language` attributes (else
  `<language>` elements). `decide.Release` has no language field of its own; the indexer's are used, resolved as
  `IsoLanguages.FindByName`.
- **SeaDex condition** (`format.SeaDex`, implementation `SeaDexSpecification`, Theatarr's own, both flavours):
  - How standing and `prefer_seadex` order releases: [SeaDex standing](#seadex-standing).
  - `fields.value`: `best` matches a release SeaDex marks best; `alt` one it lists but not as best; `any` either.
    Negate as usual (a negated `any` matches releases SeaDex does not list). Anything else is a build error.
  - The release's standing (`format.Input.SeaDex`) comes from its flags: `seadex-best` is best, `seadex` alone an
    alternative (`decide.SeaDexRank`). The seadex indexer sets them; `indexer.MarkSeaDex` copies them by info hash
    to the same torrent found through another indexer (`theatarr decide -search` calls it on the merged results).
  - The version's file has a standing too: `ExistingFile.SeaDex` when the caller knows it, else the release's when
    the file comes from the same torrent (`ExistingFile.InfoHash`, from the registry content). So the SeaDex pick
    on disk is no upgrade over itself.
  - `examples/policy/formats/seadex.json` builds, and the example policy (with its anime profiles) builds with no
    warnings.

## Profile language

Radarr's quality profile has a language (`LanguageSpecification`), and so do policy profiles.

- **Policy:** `profiles[x].language`: `any` (the default), `original` or a Radarr language name (`English`,
  `French`, …; any of Radarr's, case-insensitive, as the arr's profile takes every language). A profile used by a
  series version may not set it (Sonarr v4 profiles have no language), and a `from_arr` profile may not either (the
  arr's comes with it).
- **Engine:** the builder sets the profile's language; `LanguageSpecification` is ported (Radarr
  `DecisionEngine/Specifications/LanguageSpecification.cs`), and its fixture cases are in the arr-fixture harness
  (`internal/decide/profile/testdata/arr_radarr_LanguageSpecificationFixture.json`). Decide cases:
  original wanted and met, original wanted and not met, a release without languages taking the original
  (`AggregateLanguages`), a named language wanted and not met, and met.
- **The movie's original language** is TMDb's `original_language` from the cached TMDb answer, mapped as Radarr's
  `SkyHookProxy` maps it: `IsoLanguages.Find(code)`, else English (`xx` is English). Without a cached answer it is
  the arr's name, which `theatarr import-arr` stores for movies too (Radarr's `originalLanguage`, in
  `title.original_language`). With neither, it is Unknown, where Radarr's would be English (the `MovieMetadata`
  default): an `original` profile then rejects every release with a known language rather than take an English
  one for a film that may not be English. Titles added through the arr view get the TMDb answer only from
  `theatarr import-arr --enrich`; until then they have neither.
## SeaDex standing

SeaDex (releases.moe) curates anime releases per AniList entry. Theatarr keeps each entry's answer as a standing
record and uses it when a version sets `prefer_seadex: true` (Admin › Settings › Libraries, **Prefer SeaDex**). Off,
the default, keeps the quality-first order. How the indexer layer stores and locks the records:
[indexers.md](indexers.md#seadex-standing-storage-and-concurrent-searches).

- **Records** (`indexer.SeaDexSnapshot`, record version 2, in `metadata_cache`). One per AniList entry, kept only for
  entries a catalog title maps or a held file references. Each torrent is listed (rank 1, or 2 for upstream best),
  pending retirement, a retired tombstone (rank 0), or legacy (stored before upstream facts were kept: read as a
  tombstone until the entry's next observation rewrites it).
- **Observations** (`indexer.ApplySeaDexObservation`), the one transition, for stored rows and for answers laid over
  them in memory (`theatarr decide -search`, router dry-run). A failed or malformed answer is no observation, and
  time alone never retires a torrent: a torrent SeaDex stops listing becomes pending, a second complete omission at least
  12 hours later (`SeaDexRetirementDelay`) retires it, and reappearance clears the pending mark.
- **Ranks** (`indexer.ConfigureSeaDex`), under the scoring of the version's seadex indexer (`indexers.<key>.seadex`:
  `best` weight, default 100, `dual_audio`, `size_weight`, `tags`, `exclude_tags`): 3 for the configured winner, 2
  for upstream best, 1 for other listed torrents. Pending torrents are ranked within the comparison set of their
  first omission, apart from the listed ones, so neither takes or loses rank through the other.
- **A release's standing** comes from its flags (`seadex-best`, `seadex`) or, with an info hash, from the record.
  Same-hash sources share it. Two torrents are the same only when both hashes are known and match
  (`decide.SameTorrent`); a release title with SeaDex's record suffix may match a record for standing, but never
  assigns a hash. Generic tracker flags alone are not proof.
- **Order with `prefer_seadex`.** Curator preference comes before quality order: configured winner, SeaDex best,
  other listed, the rest; then the comparer's quality, revision and score. Native SeaDex results carry the feature
  filenames' actual quality, with mixed or unidentified packs marked Unknown. A known-quality SeaDex release may
  replace a higher-resolution file within the profile's allowed qualities and keeps its actual quality; a
  non-curated release cannot replace a curated file just for a higher resolution. Size, language, format minimum,
  cache, episode coverage and upgrade controls still apply. Unknown native quality waits even when a profile
  allows Unknown, and an unknown movie runtime holds size-limited releases.
- **Held files** (`decide.ResolveSeaDexFiles`). The scope is the file's own entry, else the entries mapped to the
  title; matches outside it are ignored without retiring anything. A file with verified episodes (and every series
  file at import) takes standing only from entries covering all its episodes. Across several entries in scope, the
  held rank moves as follows:

  | Memberships | Held rank | Next |
  |---|---|---|
  | agree on r | ≤ r | r |
  | agree on r | > r | r when every mapped membership is known, else kept |
  | disagree, highest h | < h | kept: disagreement never promotes |
  | disagree, highest h | ≥ h | h when every mapped membership is known, else kept |

  Alternatives (curated upgrades a file could be searched for) come from the matched or own entry; only currently
  listed torrents are alternatives.
- **One read per operation.** An operation reads records and mappings once, in one read transaction, when a
  prefer-on version first needs them, and every version gets one immutable view of it. A prefer-off version gets an
  empty view and causes no read.
- **Prefer off.** Versions without `prefer_seadex` decide without curator preference: Radarr's 110-minute runtime
  assumption, Unknown rejected or allowed by the profile, and held files keep the standing they were stored with.
- **Where it is used:** decisions and ranking, router freshness, the queue, approval and import checks
  ([router.md](router.md), [library.md](library.md#seadex-standing-at-import)) and RSS
  ([rss.md](rss.md#seadex-standing)).
