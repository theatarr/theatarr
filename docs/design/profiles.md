# Profiles: the quality half of the decision

Package `internal/decide/profile`. It builds on
[`formats.md`](formats.md): a `profile.Profile` embeds the `format.Profile` (format items and the three format
scores) and adds the quality part.

## What it contains

- **Qualities.** Both arrs' quality lists come from the parser ports (`radarr.AllQualities`,
  `sonarr.AllQualities`), turned into one flavour-neutral `Quality` (source and modifier in each arr's numbering, as
  in `format.Input`). `Revision` and `QualityModel` follow the arrs: revisions compare `Real`, then `Version`.
- **Quality definitions.** Both `DefaultQualityDefinitions` tables (weight, group name, min/preferred/max MB per
  minute). Weights always come from the defaults, as `QualityDefinitionService.WithWeight` does.
- **Profiles.**
  - Items lowest first, as the API lists them. Groups have ids from 1000.
  - Also: cutoff (a quality or group id), upgrade allowed, the format part, and the language (Radarr).
  - Ported: `GetIndex` (unlisted qualities get index 0, as in the arr), `QualityIndex.CompareTo`,
    `QualityModelComparer`, `FirstAllowedQuality` and `GetDefaultProfile`.
- **Import and export.**
  - Arr API: `ParseArrProfiles` and `ParseArrDefinitions` read `GET qualityprofile` and `qualitydefinition`.
    `Profile.JSON` and `Definitions.JSON` write the same shape, so `CheckSnapshot` can verify an import by a round
    trip.
  - TRaSH:
    - `FromTrash` builds a quality profile from the TRaSH JSON the way recyclarr creates it in an empty arr. The
      items are organized from the arr's default profile, new groups get ids above 1000, and the order is reversed
      into API order. The cutoff is found by name among the allowed items, falling back to the first. The
      language is matched by name.
    - `LoadTrashSizes` and `TrashSizes.Definitions` apply a quality-size file. Max and preferred are capped at the
      limits, and a value at or above the limit becomes unlimited, as in recyclarr's `QualitySizeCalculator`.
- **Decision specifications.** `Engine.Evaluate` ports `GetDecisionForReport`:
  - specifications run grouped by priority;
  - every specification in a group runs;
  - the first group with a rejection ends the evaluation.

  Each specification returns accept, or the arr's `DownloadRejectionReason` name with the arr's message text.

  | Specification | Radarr | Sonarr | Notes |
  |---|:-:|:-:|---|
  | `QualityAllowedByProfile` | yes | yes | |
  | `CustomFormatAllowedbyProfile` | yes | yes | minimum format score |
  | `AcceptableSize` | yes | yes | Radarr: movie runtime, 110 min when unknown (written back to the movie, as in the arr). Sonarr: sum of the episode runtimes, else the series runtime, 45 min within a day of the premiere; specials skipped; "2x 60min" messages |
  | `MaximumSize` | yes | yes | the global indexer setting |
  | `NotSample` | yes | yes | |
  | `RawDisk` | yes | yes | |
  | `Repack` | yes | yes | |
  | `Language` | yes | n/a | the profile language, Any or Original |
  | `AnimeVersionUpgrade` | n/a | yes | |
  | `UpgradeAllowed`, `UpgradeDisk` | yes | yes | over `UpgradableSpecification`: `IsUpgradable`, `QualityCutoffNotMet`, `CutoffNotMet`, `IsRevisionUpgrade`, `IsUpgradeAllowed` |

- **Ranking.** `Engine.Compare` and `Engine.Prioritize` port `DownloadDecisionComparer`.
  - Radarr order: quality and revision, format score, preferred protocol, indexer priority, indexer flags (when
    preferred), seeders and peers, usenet age, size closest to preferred.
  - Sonarr order: quality, format score, protocol, then season pack and episode count (anime prefers more
    episodes, other series fewer), lowest episode number, indexer priority, peers, age, size.
  - The two arrs differ in details. Sonarr has no flag step. It scores every usenet age over a week the same,
    where Radarr uses -log10(days). Its `Round` floors negative size differences, where Radarr's truncates them.

## Tests

The arr fixtures are extracted to `testdata/arr_{radarr,sonarr}_<Fixture>.json`. One case is one `[Test]` or one
`[TestCase]` row, with the fixture's setup applied. The schema is in the header of `arrtests_test.go`, and
`TestArrFixtures` runs the cases.

| Fixture | Radarr | Sonarr |
|---|---:|---:|
| AcceptableSizeSpecification | 11/11 | 39/39 |
| AnimeVersionUpgradeSpecification | n/a | 8/8 |
| CustomFormatAllowedByProfileSpecification | 6/6 | 6/6 |
| LanguageSpecification | 6/6 | n/a |
| MaximumSizeSpecification | 5/5 | 5/5 |
| QualityAllowedByProfileSpecification | 6/6 | 6/6 |
| RawDiskSpecification | 17/17 | 17/17 |
| RepackSpecification | 10/10 | 10/10 |
| UpgradeAllowedSpecification | 12/12 | 12/12 |
| UpgradeDiskSpecification | 17/17 | 22/22 |
| UpgradeSpecification (UpgradableSpecification) | 23/23 | 14/14 |
| PrioritizeDownloadDecision (the comparer) | 25/25 | 29/29 |
| QualityModelComparer | 6/6 | 6/6 |
| QualityIndexCompareTo | 10/10 | 10/10 |
| RevisionComparable | 12/12 | 12/12 |
| **Total** | **166/166** | **196/196** |

**362 of 362 cases pass, with no known failures.**

- **The cases can fail.** Removing the `UpgradeAllowed` check fails 6 cases, the revision rule in the quality
  cutoff fails 4, Radarr's protocol step fails 1, and changing the 110-minute default fails 1.
- **Unit tests** cover what the fixtures leave out:
  - Radarr's log-scale usenet age, and Sonarr's floor rounding;
  - `SizeSuffix` formatting and the premiere grace boundary (`Before` is `<=`);
  - the exact rejection messages;
  - the order of several rejections from one evaluation.
- **TRaSH:** all 62 TRaSH quality profiles (both flavours) build into valid profiles with the guide's cutoff.
  - One profile is checked item by item: Remux + WEB 2160p, whose WEB 2160p group keeps the arr's id 1003.
  - Every quality-size file applies.
- **Corpus (opt-in, `THEATARR_CORPUS`, a directory of arr API snapshots):**
  - every snapshot round-trips (`CheckSnapshot`);
  - Radarr default profiles in the snapshots match `GetDefaultProfile` item for item.
- **Omitted from the extraction:**
  - `RevisionComparableFixture.operating_on_nulls`: there is no null revision in Go.
  - `QualityFixture`: id and quality conversions only.
  - The profile service and repository fixtures: storage and delete guards.
  - Radarr has no NotSample fixture.

The setup the extraction had to work out is noted per case. It covers:
- the NBuilder defaults;
- Prioritize's mocked definitions with no preferred size;
- the episode air dates NBuilder assigns.

## Deviations

These are also in the package doc.

- **Specification order.** Specifications are listed by class name. The arr uses assembly-scan order, which only
  orders the rejection messages.
- **Exceptions.** An exception in the arr becomes a `DecisionError` rejection here too, through a recovered panic.
  A file id with no loaded file cannot happen here, so the arr's NullReference paths read as "no file".
- **Delay profile.** The preferred protocol is passed in (`Config.PreferredProtocol`), not resolved from tags.
- **Ranking scope.** `Prioritize` ranks one title's releases. Grouping by title is the caller's.
- **TRaSH import:**
  - it assumes the increased size limits of current arr versions;
  - it uses the guide alone, with no recyclarr config overrides, the top quality sort and no preferred ratio;
  - it does not do recyclarr's per-profile sizes, which current arr versions lack.

## Arr specifications outside `Engine.Specs`

These arr specifications are not ported into this package. Each would plug into `Engine.Specs` as another `Spec`,
with its facts on `Subject` or on the `Engine`.

| Arr specification | Needs | Plugs in as |
|---|---|---|
| Queue | Downloads in flight with their quality and formats | `UpgradableSpecification` against each queued item, from the Download table |
| Blocklist, BlockedIndexer | Blocklist rows; indexer failure status | lookups on the Engine (store, gateway budgets) |
| AlreadyImported, RssSync/History, RssSync/DeletedEpisodeFile (Sonarr) | Grab and import history; file deletion events | registry and history tables (import-arr) |
| FreeSpace | Free disk space | storage policy; mostly moot for debrid symlinks |
| TorrentSeeding, RequiredIndexerFlags (Radarr), Retention, MinimumAge | Indexer settings (seed criteria, required flags); usenet retention and age | indexer config from policy and gateway; the usenet ones apply only to usenet indexers |
| Protocol, RssSync/Delay, Pending | Delay profiles; the pending-release store | policy delay profiles; a pending table |
| ReleaseRestrictions | Release profiles (required and ignored terms) | policy, reusing netregex |
| HardcodeSubs (Radarr) | Parsed hardcoded subs and the allow setting | parser adapter field plus a policy flag |
| RssSync/Proper, RssSync/IndexerTag, RssSync/Monitored, RssSync/Availability (Radarr) | File dates, tags, monitored and availability state | version state ([overview.md](overview.md)) |
| Search/* (Movie, Series, Season and episode matching), FullSeason, MultiSeason, SeasonPackOnly, SplitEpisode, SameEpisodes(Grab), SceneMapping | Episode mapping and search criteria | the metadata engine's EpisodeNumber resolution ([tv-metadata.md](tv-metadata.md)) |

The comparer's protocol step needs delay profiles too; the caller sets `Config.PreferredProtocol` instead.
