# Series (Sonarr import, series decisions, series shadow)

Series follow the movie path end to end: `theatarr import-arr` reads Sonarr, `internal/decide` decides series
releases as Sonarr does, and the shadow (history replay and the live runner) covers Sonarr grabs. Where this sits:
[overview.md](overview.md); TV metadata: [tv-metadata.md](tv-metadata.md). Migration `00011_series.sql`.

## Parts

### `theatarr import-arr` for Sonarr (`internal/importarr`)

- **Reads** (GET only): system/status, rootfolder, tag, qualityprofile, `series`, and per series `episode?seriesId=`
  and `episodefile?seriesId=`, then history as for Radarr (newest first, stops at the first page all known).
- **Writes:**

  | Table | Holds |
  |---|---|
  | `title` | One per TVDB id. New columns: `series_type`, `use_scene_numbering`, `runtime_minutes`, `original_language` |
  | `arr_title.series_type` | The type as each instance had it, kept as a record. It does not decide anything: the title's type does (Series type, below) |
  | `title_scene_mapping` | Sonarr's scene mappings as its API exposes them (`alternateTitles`): name, season, scene season, origin, comment; source = the instance |
  | `episode` | One per (title, season, number) in TVDB numbering, shared by the title's versions; name, air date (and UTC), runtime |
  | `episode_number` | `absolute_tvdb`, `scene` (season and number) and `scene_absolute`, source = the instance |
  | `episode_version_state` | Per version and episode: monitored, state (`wanted` no file, `filled` below cutoff, `cutoff_met`), the file's score |
  | `series_monitor` | Per version (owner only): the arr's `monitorNewItems` and its seasons' `monitored`, the intent the metadata refresh follows for seasons that appear later (`docs/design/tv-metadata.md`) |
  | `version_file` (+ `release_type`) and `version_file_episode` | One row per episode file, with the episodes it holds |
  | `arr_alias` | `series`, `episode` and `episodefile` ids for the arr views |

- **Version state:** `wanted` while a monitored episode that has aired has no file (or no episode has one), `filled`
  while a file is below the cutoff, else `cutoff_met`.
- **Ownership** as for movies: the first instance owns the version and writes its episode states. The owner's
  `monitored` wins over what arrview wrote (`episode_version_state` rows from `PUT episode/monitor` are upserted, never
  inserted twice); that is the intended behaviour while the arr owns the version.
- **Idempotent and incremental:** a second run changes no row; new history adds only its rows. An episode no instance
  lists any more (and no link holds) is deleted; an episode file the arr no longer has goes with its episode rows.
- **History:** `seriesMatchType`, `releaseHash` and `tvRageId` join the allowlist. Registry seeding is shared with
  Radarr (one content per hash, one `content_version` per hash and version).
- **`import_run` pruning**: `importarr.PruneRuns` keeps the last 7 days plus each instance's latest run.
  It runs in `theatarr serve`'s nightly job (`db.nightly`), before the backup.

### Series type

A series has one type (standard, daily or anime), the same for every library and every view: `title.series_type`.

- **Resolver.** `store.SeriesTypeSQL` (and `store.SeriesType` for one title) is the only place the rule lives:
  the title's value; when it is empty, `anime` if the title's anime flag is set, else `standard`. Between the two,
  a title with no value takes the first non-empty `arr_title.series_type` by instance: the import copies an arr's
  type onto a title that has none on every run, so no code path leaves a title in that state; the fallback only
  covers a database that somehow has one.
- **Readers.** All of them read the resolver: decisions and search style (`decide.LoadTitle`, `LoadSeries`),
  import naming (`library` `completeDownload`), `checkNames`, follower adoption, rename preview, reassign,
  now-playing's next episode, the anime-library filter of acquisition (the flag or the type), the TV metadata
  refresh (anime series take TVDB only) and the Sonarr views (resource and webhooks). The per-instance imported
  type no longer overrides the title's value for a version: an install whose imported types differ per instance,
  or from the title's value, gets the title's value everywhere.
- **Writers.** An arr import fills an empty value. An add (`catalog.AddSeriesType`: a Sonarr view's POST, the native
  add) stores its type only for a series that has none; a later add never changes a stored one. An edit
  (`catalog.SetSeriesType`: a view's PUT with a type other than the one shown, the editor) replaces it. No writer
  touches the anime flag.
- **The anime flag** keeps its own role: which libraries take the title (`versions.<k>.anime`, a view's version
  choice) and anime indexer queries (flag or type). A `standard` series can be flagged anime and the reverse.

Tests: `TestSeriesTypeEchoChangesNothing`, `TestSeriesTypeEdit`, `TestSeriesTypeAddKeepsStored`,
`TestSeriesTypeEchoIsNoAnimeHint`, `TestSeriesTypeEditor` (arrview), `TestNextEpisodeUsesTitleSeriesType`
(nowplaying), `TestLoadSeriesVersion`, `TestFollowerNamingSeriesType`.

### Series decisions (`internal/decide`)

Every version of a series, parallel followers included, decides and names with the title's series type (above);
removed episodes are excluded from ordinary matching loads. Each arr import also unmonitors follower episodes
absent from its snapshot, retaining their links and file states.

- `Engine.Decide` for a series version runs Sonarr's `DownloadDecisionMaker`; `Engine.DecideSearch` passes the search
  criteria (`SeriesSearch`: kind, season, episode, requested episodes). Movies keep the movie path.
- **Mapping** (`series.go`) ports `ParsingService.Map` with search criteria: scene mapping lookup
  (`FindSceneMapping` with its season filters and Levenshtein tie-break), series by search clean title, indexer TVDB
  or IMDb id, title, all titles, title and year; episodes by season and number, scene numbering (search episodes
  first), absolute numbers for anime (`GetAnimeEpisodes` with the scene season rules), air date for daily series,
  whole seasons for packs (scene season first), specials by episode title (`ParseSpecialEpisodeTitle`), and
  `ValidateForSeriesType`. `UnknownSeries` and `UnknownEpisode` as the arr names them.
- **Decision** (`episodes.go`): languages through `adapt.SonarrRelease` with the episode titles, formats and score,
  then the Sonarr specifications with every mapped episode's file. A pack is judged against every episode's file
  (one `UpgradeDisk` failure rejects it), as Sonarr does. Each file's formats follow
  `ParseCustomFormat(EpisodeFile, Series)`: scene name, else original path, else relative path; the file's release
  type. The decision lists the episodes (`S01E02`), their ids, how many have no file, the release type and season.
- **Loading** (`dbload.go`): `LoadSeries`, `LoadSeriesVersion` (scene mappings, episodes with
  numbers and the version's files with their episode sets) and `LoadEpisodes`.
- **Specifications added** (`internal/decide/profile/series.go`, additive): `FullSeason`, `MultiSeason`,
  `SplitEpisode`, `SameEpisodesGrab`, `SceneMapping`, `SeasonPackOnly`, `RssSync/MonitoredEpisode`,
  `Search/EpisodeRequested`, `Search/SeasonMatch`, `Search/SingleEpisodeSearchMatch`. Their arr fixtures are Go tables
  (`series_test.go`: FullSeason, SameEpisodes, SingleEpisodeAge, MonitoredEpisode, SingleEpisodeSearchMatch).
- **`theatarr decide -tvdb N -version KEY [-season N [-episode N]] 'Title'`** decides a series release; `-search` with
  `-season` searches that season.
- **Sonarr version** (`versions.<k>.sonarr: v4 | v5`, default `v4`, series only): a `v5` version parses and maps
  as Sonarr v5 (`develop` `a6171f0ca`): multi-season packs, anime season packs named by a season title, seven new
  languages, `ENG SUBS` as subtitles, diacritics removed for matching, the series' original title (from the cached
  TVDB record) and aliases with a year. `v4` parses as Sonarr v4. Items, fixtures (v5 ParserTests 1,688/1,688) and the v4
  against v5 measurement: `docs/design/sonarr-v5.md`.

### Shadow for series

- **History replay** (`internal/decide/shadow`): `LoadCorpusSeries` (corpus: series and episodes from the parse
  answers, scene mappings from a saved `GET series` with `-series-aliases`) and `LoadSeriesDB` (database: files
  rebuilt per episode from import and delete events, with their episode sets). Sonarr writes one grabbed event per
  episode; the rows of one release (same series, download id and title, within two minutes) are one grab. The search
  criteria are the grab's episodes (season search for several or a pack), none for an RSS grab. The report adds
  "same episodes", and full agreement for series needs them too.
- **Live runner** (`internal/shadow`): series versions in shadow mode are shadowed. A Sonarr grab becomes one
  `shadow.grab` (first history row; `history_ids` and `episodes` stored). The arr's release is decided with the
  arr's criteria against each episode's file at grab time (rebuilt from history, else the `version_file` the import
  stored when the arr added it before the grab). Theatarr's search asks the grab's seasons through the planner (torznab
  `tvsearch` by TVDB id), and every result is decided for the grab's episodes. RSS reads category 5000 for feeds
  series versions list; TV items map by TVDB id or by the parsed series title against titles and scene mappings,
  and are decided per series version (RSS path). `shadow_diff.kind` holds movie or series.
- RSS considers monitored series versions in `wanted`, `filled` and `cutoff_met`; existing per-episode upgrade
  specifications still reject non-upgrades. Each enabled wanted round marks a series wanted when a monitored missing
  non-removed episode reaches its stored UTC air time, without fetching metadata.
- **Agreement per kind:** `Summary.Kinds` gives grabs, agreement and `meets_bar` (95%) for movies and series
  separately; the overall line stays.

## History replay

`theatarr decide shadow` replays an instance's recorded Sonarr grabs: each grab is decided with the instance's own
profiles (`from_arr`, from the corpus snapshots; policy `internal/decide/shadow/testdata/policy`) and compared with
what the arr grabbed. It reports same quality, same episodes, the score against the arr's score now and at grab time
(`data.customFormatScore`, database mode only), accepted, and full agreement.

- **Corpus mode** reads grabs and today's scores from the corpus; scene mappings come from a saved `GET series`
  (`-series-aliases`). Without them, grabs named by an alias are `UnknownSeries`.
- **Database mode** reads an imported database, with files rebuilt per episode from history.
- The usual disagreements are not engine defects: the profile's scores changed since the grab (Theatarr's score
  equals the arr's score today), `Disk*` against a file rebuilt from history (a mass re-grab of equal releases, or an
  upgrade from a file history no longer shows), hand grabs of releases the profile rejects (below the minimum size,
  a repack of another group, a title the arr's own parse does not map), and `RepackReleaseGroupDoesNotMatch` against
  the file at the time. Quality, episodes and today's score agree on every grab the arr's own parse maps.
- Under `sonarr: v5` ([sonarr-v5.md](sonarr-v5.md)) the agreement is the same apart from language-only changes
  (`ITA ENG SUBS`: v5 drops Italian with the subtitles) and packs v5 maps that v4 does not.

`tvrefresh` stores TVDB's original name, aliases, translated names and translation aliases in `title_scene_mapping`
with source `tvrefresh:tvdb` and origin `tvdb`, on every successful series refresh (including due-series sweeps). These
are all-season names: Sonarr's existing `CleanSeriesTitle` deduplicates them against the primary title, each other and
imported mappings, whose season limits and other fields stay authoritative. Only refresh-owned rows are replaced; a
failed fetch keeps the last successful names.

### Season pack rule (`versions.<k>.packs`)

`any_upgradable` ports the actuallyevan Sonarr fork's rule (`ACCEPT_RELEASE_ANY_UPGRADABLE=true`). The fork's only
change is in `UpgradeDiskSpecification` (fork `v4.0.20.3014-1`, `9a75b78`): every episode of the release is looked at, an episode
with no file (or a file record that is gone) counts as upgradable, and the release is accepted when **any** episode is
upgradable; otherwise the first episode's rejection stands. `UpgradeAllowed`, the queue and history specifications are
not changed by the fork, so a pack whose file the profile does not allow upgrading is still refused.

- **Policy:** `versions.<k>.packs`: `all` (the default: Sonarr's rule, every episode's file must be upgraded),
  `any_upgradable` (the fork's) or `threshold` with `packs_threshold: <percent>` (Sonarr v5, below). Series versions
  only; validated (`packs_threshold` 1 to 100, required with `threshold` and refused otherwise). `examples/policy` sets `any_upgradable` on `tv-2160p`
  and `anime-tv`, and so does the replay policy (`internal/decide/shadow/testdata/policy`).
- **Import:** the pack is taken whole, but each file is linked only when it upgrades every current file it would
  replace (the arrs' import `UpgradeSpecification`, `docs/design/library.md` "Import of a ready download" step 6): the
  episodes the pack fills and the files it betters are linked, the better files stay.
- **Engine:** `profile.Config.AcceptReleaseAnyUpgradable`; a profile is built once and shared by versions, so a version
  with the fork's rule decides with a copy of the engine (`withPackRule`). The decision's upgrade reason names the first
  upgraded file, the episodes the pack fills, and how many keep their file.
- **`threshold`** ports Sonarr v5's `SeasonPackUpgrade = Threshold` (PR #7975, `cf6b21aef`;
  `UpgradeDiskSpecification` on `develop` `a6171f0ca`), `profile.Config.SeasonPackUpgradeThreshold`:
  - Scope as in v5: **full-season packs only**. A multi-episode or other release keeps the all-files rule and its
    reasons.
  - Episodes with no file count as upgradable, and so does each file the pack upgrades (cutoff not met and an
    upgrade). With no files at all the pack is accepted; otherwise it is accepted when the upgradable share is at least
    the threshold.
  - Rejected with v5's reason `DiskNotUpgrade` and message `Season pack does not meet the upgrade criteria.
    Upgradable: 2/3 (66.67%), Mode: Threshold, Threshold: 70%`.
  - `packs_threshold: 100` accepts the same packs as `all`, but with v5's reason.
  - v5's own `All` and `Any` are not used: `all` stays v4's rule and `any_upgradable` the fork's (they differ at the
    edges: v5's `Any` is for full seasons only and has its own reason; [sonarr-v5.md](sonarr-v5.md)).
- **Tests:** v5's three `SeasonPackUpgrade` fixture cases (`arr_sonarr_v5_UpgradeDiskSpecificationFixture.json`;
  v5's `All` case runs as `all`, its `Any` case as `any_upgradable`), the threshold cases in `TestSeasonPackRule`
  (60% and 70% on a 3-episode season, every file at its best, nothing on disk, a multi-episode release, the message),
  and the policy checks. Also: the fork's fixture cases (`arr_sonarr_fork_UpgradeDiskSpecificationFixture.json`), the S01 pack cases
  under both rules (`TestSeasonPackRule`), and five recorded pack grabs (`internal/decide/testdata/packs`: Lioness S03,
  Vikings S04 Volume 2, Only Murders S04, SPY x FAMILY S01, Re:Zero S04, with the profiles trimmed to their
  scored formats and the files at grab time): rejected `DiskCustomFormatScore` under `all`, accepted under
  `any_upgradable` (`TestPackRuleReplayGrabs`).

Reproduce. `decide shadow` reads its settings from the `-db` database, so the replay policy is imported into a
scratch database first (into the run.db copy for the second command, unless that copy already has settings, which the
replay then uses). The import needs all five record groups and version roots that exist inside a library area, so the
copy of the test policy gets a `budgets` section (`public: {requests: 1, per: 1s}`), a `library.dangling.roots` area
and version `library` paths under it that exist. The `*_env` credentials must be set; any value works offline.
```
cp -r internal/decide/shadow/testdata/policy <scratch>/replay-policy   # then add budgets, library and roots as above
THEATARR_RD_KEY=x THEATARR_VIEW_RADARR_KEY=x THEATARR_VIEW_SONARR_KEY=x \
  bin/theatarr settings import -policy <scratch>/replay-policy -db <scratch>/replay.db -confirm
bin/theatarr decide shadow -db <scratch>/replay.db -arr-snapshot <corpus> -corpus <corpus> \
  -instance <tv instance> -version tv-2160p -series-aliases <saved GET series JSON> -dump 4k.tsv
bin/theatarr decide shadow -db <copy of run.db> -arr-snapshot <corpus> \
  -instance <anime instance> -version anime-tv -dump anime.tsv
THEATARR_CORPUS=<corpus> THEATARR_SERIES_ALIASES=<dir with <instance>.series.json> go test -run CorpusParity -v ./internal/decide/shadow/
```

## Tests

- `internal/importarr`: a fake Sonarr (two series, a two-episode pack file, an anime series with scene numbering and
  mappings, a manual import), the database contents, idempotence, an upstream change (files replaced, an episode gone,
  a mapping gone), a second instance on another version of the same series, rows arrview wrote, and `PruneRuns`.
- `internal/decide`: standard (single, pack over a file, pack at cutoff, not aired, unknown series and episode, alias,
  TVDB id, file with more episodes, search mismatch, special by title, unmonitored), anime (absolute, batch, scene
  mapping with a scene season, alias, pack ranking), daily, the database loading, Levenshtein.
- `internal/decide/profile`: the Sonarr fixtures above.
- `internal/decide/shadow`: the series database loader (grouping, rebuilt files). Corpus parity is opt-in. So is
  `TestSonarrV5Diff` (v4 against v5 over the corpus and the replays).
- `internal/decide`: `TestSeriesDecisionsV5` and `TestSeriesV5Languages` (each case under v4 and v5), the original
  title in `TestLoadSeriesVersion`; `internal/decide/profile`: v5 season matching.
- `internal/shadow`: the runner on a fake Sonarr: a pack grab of two history rows becomes one grab, the season
  search, the diff of kind series, the per-kind summary, the grab by its second row, an RSS TV pick by scene mapping
  name.

`internal/metadata/tvrefresh` also tests alias/translation persistence, cleaned-name deduplication, stale-name
removal, repeated refreshes, import preservation, failed-fetch retention and transaction rollback. Its decide integration test
loads a newly added anime and its refresh-written mappings from the store: romaji/fansub releases without indexer
TVDB ids match `Alias` and S01E01; removing those mappings produces `UnknownSeries`.

## Deviations and known gaps

- **The library is the one series under decision.** A release naming another series is `UnknownSeries`, where the
  arr would map it there and reject it `WrongSeries` or `MatchesAnotherSeries`; same verdict.
- **Scene mappings:** imported season mappings plus TVDB's all-season aliases/translations from the refresh.
  No filter regex and no mapping type: mappings with a filter regex
  (comment "Limited matching") are left out, and the XEM season-1 rule (mapping type `XemService`) cannot apply.
  Sonarr's scene mapping table also holds mappings of series not in the library; Theatarr's holds the library's only.
  TheXEM scene numbers come through imported episodes and stop updating once the arr is no longer imported; new series get TVDB absolute
  numbers only. Season-specific aliases and ongoing XEM numbering remain open: the anime HTTP client already
  fetches `map/all` through the metadata gateway, but a safe series-refresh integration also needs `map/names`,
  ownership/precedence for scene numbers (the episode-number unique index spans sources), and mapping-type support
  in decide for Sonarr's XEM season-1 rule.
- **Anime:** mapping is Sonarr's (absolute, scene absolute, scene season). The AniDB/AniList mapping engine, the
  review queue and alternate orders are in [anime.md](anime.md). Searches follow Sonarr's criteria on Prowlarr feeds ("Search criteria"
  below).
- **The arr's SingleOrDefault exceptions** (two episodes with one absolute number or air date) become its
  "Unexpected error processing release" (reason `Error`).
- **Episode files** have no `originalFilePath` in Sonarr's API; formats use the scene name, else the relative path.
- **Search criteria in replay** are reconstructed from the grab's episodes (`ArrSearchFor`, scene numbering where the
  series uses it); history does not keep the arr's.
  `MonitoredEpisode` runs only for RSS grabs, against today's monitored flags.
- **RSS picks** link to a later arr grab of the same title and version whatever its episodes.
- **Deferred specifications** as for movies (queue, blocklist, history, delay, release profiles); for series also
  `DeletedEpisodeFile`, `Search/SeriesSpecification` (one series by construction) and the indexer's
  `SeasonSearchMaximumSingleEpisodeAge` (0, off: the policy has no such setting).

## Search criteria

A series search asks Prowlarr's feeds (kind `torznab`) what Sonarr's `ReleaseSearchService` and
`NewznabRequestGenerator` would ask: `internal/router/criteria.go` builds the criteria from the target's episodes and
the title's scene mappings, `internal/indexer/criteria.go` turns them into requests. One search per title is still
shared across versions: the criteria are part of the query key.

| Series | Episode search | Season search (several episodes) |
|---|---|---|
| standard | one `tvsearch` per scene episode mapping: scene season/episode (`season=00` for specials), by id first, then the mapping's names with `q` (or `title`) only when the ids found nothing; a season-specific scene name (mapping with a scene season and another name) searches by that name only, for its season only | episodes grouped by scene season: one `season=N` search per group (one episode: an episode search); season 0 as specials |
| daily | `season=YYYY&ep=MM/DD` by air date (no air date: by its numbers, where Sonarr fails) | by air year: `season=YYYY` for several episodes of a year, else the episode by date |
| anime | `tvsearch` ids `q=NN` (scene absolute, else absolute), ids `season/ep` (scene numbers), `t=search q="<name> NN"` and `tvsearch q=<name>&season&ep` per scene name (AnimeStandardFormatSearch on, as Prowlarr syncs it) | v5: one `season=N` search per scene season (a season-title name as `t=search q=<name>`); the aired episodes one by one only while the season still airs |
| specials (season 0, anime without absolute numbers) | `t=search q="<clean name> <clean episode title>"` per name (TV and anime categories), then the episode by its numbers | |

Names: `GetSceneNames` (mappings of the episodes' TVDB or scene seasons, plus all-season non-`tvdb` ones, Latin-1
only) plus the series title; episode mappings follow `GetSceneEpisodeMappings` (a `tvdb`-origin mapping searches TVDB
numbers). Clean titles are `GetCleanSceneTitle`; a feed whose caps say `searchEngine="raw"` gets raw and clean.

Other sources keep the plain query in TVDB numbering: DMM and Torrentio key by IMDb id (Cinemeta/TMDb numbering),
Zilean filters by IMDb id and title, SeaDex by AniList id per TVDB season. A whole-series search, or one for a title
with no episodes loaded, keeps the plain query everywhere. Search ceiling coverage stays per TVDB season; only a
season search by id whose scene season is the TVDB season can prove absence.

**Judging the answers.** The target's search (`SearchTarget.Series`, built by
`shadow.SearchFor`) is in TVDB numbering: the episodes wanted by their own season and number. Each query carries its
own decision criteria, as each of Sonarr's `SearchCriteriaBase` is dispatched and its reports decided against it
(`ReleaseSearchService.Dispatch`, `GetSearchDecision`):

| Query | Criteria kind | Season / episode the specifications read | Requested episodes |
|---|---|---|---|
| episode mapping (`SearchSingle`) | `episode` | the mapping's scene season and episode | the episode |
| scene season group (`SeasonSearch`) | `season` (one episode: `episode`) | the group's scene season | the group's episodes |
| anime episode | `anime-episode`; within a season search `anime-season` (`IsSeasonSearch`) | scene numbers (not read) | the episode |
| anime season (v5) | `anime-season` | the scene season (not read) | every episode of the search |
| daily / daily year | `daily` / `daily-season` | none | the episode / the year's episodes |
| special title search | `special` | none | the special episodes |

So canonical S01E03 searched as scene S02E05 accepts `Show.S02E05…` (`SingleEpisodeSearchMatch` against 2/5), and a TVDB
season spanning scene seasons 1 and 2 accepts each scene season's pack against its own season (`SeasonMatch`). The
plain queries (DMM, Torrentio, Zilean, SeaDex, or a feed when there are no criteria) are judged with the target's
TVDB criteria and, for a scene-numbered series, with each `episode`/`season` criteria of another number
(`plainCriteria`): their release names use either numbering. A release found by several of a target's queries (same
indexer and hash) is judged with each of their criteria and keeps the decision with the fewest rejections, the first on
a tie (`DeDupeDecisions`). Offers judge the offered release the same way. The query key and the one-query-per-key
sharing across versions stay as they are: criteria are per target, so two versions sharing a request each judge its
answer with their own.

History replays and the live shadow judge the arr's grab with `shadow.ArrSearchFor`: the first episode's scene season
and episode. Both `SearchFor` kinds give a daily season search `daily-season` criteria (Sonarr's
`DailySeasonSearchCriteria`, no season check): a daily release parses as season 0, so `SeasonMatch` would reject it as
the wrong season.

Deviations: v4 anime season searches also search every episode (dozens of requests per feed); Theatarr follows v5. Non-Latin
names are never search terms (Theatarr's TVDB aliases include translations that Sonarr's mapping list does not). No
monitored filter: the target's episodes are already the wanted ones. An anime episode without any absolute number is
not searched as `00`. Aggregate id searches (`tvdbid` and `imdbid` in one request) are not used.
