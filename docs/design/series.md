# Series (Sonarr import, series decisions, series RSS)

Series follow the movie path end to end: the one-time import reads a Sonarr, `internal/decide` decides series
releases as Sonarr does, and RSS reads series feeds for libraries. Where this sits:
[overview.md](overview.md); TV metadata: [tv-metadata.md](tv-metadata.md). Migration `00011_series.sql`.

## Parts

### Series from Sonarr

The one-time import (Admin › Import from Sonarr/Radarr, `internal/arrimport`) reads a Sonarr's series, episodes and
episode files (GET only) and adds them to a library through the normal add and link paths
([migration.md](../migration.md)). The periodic import that kept arr rows in step
with a Sonarr is gone: migration 00125 dropped its tables, and 00134 turned the file rows it left into adopted links.

### Series type

A series has one type (standard, daily or anime), the same for every library and every view: `title.series_type`.

- **Resolver.** `store.SeriesTypeSQL` (and `store.SeriesType` for one title) is the only place the rule lives:
  the title's value; when it is empty, `anime` if the title's anime flag is set, else `standard`. The arr's own type
  is not read (migration 00121 copied it onto titles without one).
- **Readers.** All of them read the resolver: decisions and search style (`decide.LoadTitle`, `LoadSeries`),
  import naming (`library` `completeDownload`), the one-time import's adoption, rename preview, reassign,
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
`TestSeriesTypeChoosesSearchableLibraries`, `TestSeriesTypeLookupOutsideView`, `TestSeriesTypeEditor` (arrview), `TestNextEpisodeUsesTitleSeriesType`
(nowplaying), `TestLoadSeriesVersion`.

### Series decisions (`internal/decide`)

Every version of a series decides and names with the title's series type (above); removed episodes are excluded
from ordinary matching loads.

- `Engine.Decide` for a series version runs Sonarr's `DownloadDecisionMaker`; `Engine.DecideSearch` passes the search
  criteria (`SeriesSearch`: kind, season, episode, requested episodes). Movies keep the movie path.
- **Mapping** (`series.go`) ports `ParsingService.Map` with search criteria: scene mapping lookup
  (`FindSceneMapping` with its season filters and Levenshtein tie-break), series by search clean title, indexer TVDB
  or IMDb id (IMDb ids compared normalised, so ids differing only in zero padding are one title), title, all titles, title and year; episodes by season and number, scene numbering (search episodes
  first), absolute numbers for anime (`GetAnimeEpisodes` with the scene season rules), air date for daily series,
  whole seasons for packs (scene season first), specials by episode title (`ParseSpecialEpisodeTitle`), and
  `ValidateForSeriesType`. `UnknownSeries` and `UnknownEpisode` as the arr names them.
  One fix of ours: a series name, scene name or special title that cleans or normalises to nothing (punctuation only) matches
  nothing, where the arr finds it in, or at the start of, every release name (`TestEmptyNamesMatchNothing`).
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
- **Not aired yet** (ours, `NotAired` beside `FullSeason`, a temporary rejection `NotAiredYet`): a TV recording
  (a television source the name states: HDTV, SDTV, Raw-HD; not the parser's HDTV fallback for a name without a
  source) that is not a season pack is passed over while its episode airs more than
  36 hours after now, by the stored `air_date_utc_at`: it cannot exist before the broadcast, so it is a fake. The
  episode that decides is the earliest of a release of one or two episodes and the latest with an air date of a
  longer range (a fake `E01-E10` does not ride on E01; none dated: not delayed). Never delayed: WEB, disc and unknown sources, an episode without an air date,
  season 0, an upgrade (every episode it maps to has a file), a file already on disk (Manual import, the import by path,
  reuse for another library: `decide.WithOnDisk`), and packs (they keep `FullSeason`). Automatic search, RSS and the route's second
  decision apply it alike; RSS does not remember it, so a later sighting is decided again. When a queued route resumes
  and this hold alone refuses the release its download holds (the listing moved later while it waited for a
  provider), that unstarted download is removed quietly, with no failure, History or blocklist row (one a provider
  or client already accepted once, requeued after a stall, is cancelled the usual way and keeps its attempts); the
  route goes on with its other releases. A grab by
  hand from Interactive search still works: the release is listed with the reason and Grab overrides it (the route
  never decides a grab by hand again). Words: "Not aired yet: S02E05 airs Thu 26 Nov, 9 pm UTC; a TV recording cannot
  exist before then.", in the server's zone with its short name; the day alone when the deciding episode's instant is the refresh's placeholder
  (exactly midnight of its air date in the series' zone, `series_meta.time_zone`, UTC without one), as Calendar
  reads it. No History or blocklist row.
- **`theatarr decide -tvdb N -version KEY [-season N [-episode N]] 'Title'`** decides a series release; `-search` with
  `-season` searches that season.
- **Parser and season packs** (every series library, no setting): release names parse as v4plus, the Sonarr v4
  port with the Sonarr v5 parser changes chosen one by one and a few fixes of our own (the table in
  `internal/decide/parser/sonarr/parser_v4plus.go`): multi-episode anime ranges with a subgroup, more language tags,
  `WEB` with `EAC3` or a separated `DDP.5.1` and `ATVP WEB` as WEB-DL, and a bit depth written `10 bits` / `8 bits` /
  `10-bits` (not an episode number).
  Subtitle tags are kept out of the audio languages (`TestV4PlusSubtitleLanguages`). This covers everything after
  `Multiple Subtitle`, a language right before the whole word `SUB(S)` or `SUBTITLE(S)` (`[English Subs]`), and `SUB`
  joined before a language (`SUBENGLISH`). A word that only starts with `Sub` is no marker: `ENG.Submarine` and
  `EN.Submarine` read English. A list before `SUBS` keeps its languages on purpose: `ITA.ENG.SUBS`, `GER.DUB.ENG.SUBS`
  and `(JA) ENG Subs` read both.
  A trailing id tag (`{imdb-tt…}`, `{tmdb-…}`, `{tvdb-…}`, `[tt…]`, `-tt…`) is no release group
  (`TestV4PlusTrailingIDTag`). It is removed before the group is read, so a real group before it wins; with none the
  group is empty. Sonarr reads the tag as the group: a torrent folder named `… .English.HONE {imdb-tt15469618}` gave
  the group `tt15469618`. Files already named keep their names.
  Packs named by a season title, original titles and partial seasons are not read (v5's mapping is not ported).
  **Multi-season and complete-series packs** (standard and daily series; departs from Sonarr, which rejects every
  multi-season release): a season pack's name is read for its span (`indexer.PackSeasons`): an ascending range
  (`S01-S05`, `Season 1-4`, `S01 to S03`, which the parser reads as a pack of season 1) or a list in steps of one
  (`S01.S02.S03`), from the parsed season to at most the series' last season; a multi-season release with anything
  else (`S01.S03`, `S01 S04`) keeps `MultiSeason`. A name with "Complete Series", "All Seasons" or "The Complete
  Collection" that does not parse, parses to absolute numbers only, or (with no `S01`, `S01E02`, `Season 1` or `1x02`
  marker) has a year directly before or after the phrase (`Show.2010.Complete.Series`, read by the parser as S20E10)
  is parsed again with the phrase read as `S01` (`indexer.ParseSeriesName` behind `decide.ParseSeriesRelease`, also
  RSS's title lookup): a season pack from that is a complete-series pack, every season above 0 (bare "Complete" stays
  unparsed). Whatever asks of a standard or daily release "is this a pack of several seasons" by its name (the
  decision's `multi_season`, the merged route's attempt bucket, the filter of the query without a season) reads
  `indexer.HoldsSeasons`, which answers no for a name that parses as an explicit episode
  (`Show.S01E05.A.Man.for.All.Seasons`, `Show.S01E01-E02.All.Seasons`, a dated episode): the phrase is its episode
  title. Sonarr reads no such phrase at all (a deliberate difference). The bare `Show S01 - 02` is read by the parser as
  seasons 1 to 2 and stays a pack of two seasons; with a group tag (`[Grp] Show S01 - 02 [1080p]`) it reads as episode
  2 and is no pack. The span maps to every
  episode of each season, each through its own scene mapping (specials never); the decision's season is the first,
  its episodes all of them, its release type `seasonPack` as before. A season search accepts a span that holds the
  searched season; a single-episode or daily episode search rejects it as a season pack (`FullSeason`). The other
  specifications apply over the whole span: every episode aired, every episode monitored on RSS and automatic
  searches, at least one missing or upgradable episode, size by the summed runtime; a single-season pack still ranks
  first at equal quality and score. In a merged search (below, "Seasons searched together") a pack of several seasons is
  for the searched seasons only: its download leaves out every episode of a season nobody searched (one that fills
  or upgrades no episode of the searched seasons is rejected; its upgrade reason, the Why's, names the searched
  seasons' files and missing episodes, while the specifications still judge the whole pack), and packs rank before other releases as a step of their own; between packs a known-cached one ranks first, then the
  one that fills more searched episodes. Anime series keep the rule: no span, no complete-series reading. A season pack, or any multi-episode release, is taken when it fills or
  upgrades at least one episode (the actuallyevan fork's `ACCEPT_RELEASE_ANY_UPGRADABLE`, taken further than Sonarr
  and the fork). Two kinds of episode keep their file. An episode that already has a better file (`UpgradeDisk`)
  stays in the download's episodes, and the import upgrade check (library.md) leaves its file in place. An episode
  whose file fails `UpgradeAllowed`, `Repack`, `Proper` or `AnimeVersionUpgrade` (a better quality with upgrades
  off, a repack or anime v2 of another or an unknown group, a proper for a file older than 7 days) no longer refuses
  the whole release, as long as another episode of the release is usable: it has no file, or a file the release
  upgrades that passes all four checks. With no usable episode each check rejects as before. An episode kept this
  way is counted in `Decision.Kept`, is never quoted as upgraded, and is left out of the download's episodes
  (`Decision.DownloadEpisodes`), so the import leaves its file in place as out of scope. A release that is one file
  for two episodes (`S01E01E02`) with one episode kept this way and the other usable is accepted and then held for
  review on import ("no file matched the download's episodes"). The decision's upgrade reason ends
  `; N episode(s) keep their file`.

### RSS for series

RSS ([rss.md](rss.md)) reads category 5000 for the feeds Live series libraries list; TV items map by TVDB id or by
the parsed series title against titles and scene mappings, and are decided per series library (RSS path). RSS
considers monitored series libraries in `wanted`, `filled` and `cutoff_met`; existing per-episode upgrade
specifications still reject non-upgrades. Each full round of the search schedule marks a series wanted when a monitored missing
non-removed episode reaches its stored UTC air time, without fetching metadata.

## Tests

- `internal/decide`: standard (single, pack over a file, pack at cutoff, not aired, unknown series and episode, alias,
  TVDB id, file with more episodes, search mismatch, special by title, unmonitored), anime (absolute, batch, scene
  mapping with a scene season, alias, pack ranking), daily, the database loading, Levenshtein.
- `internal/decide/profile`: the Sonarr fixtures above.
- `internal/decide`: `TestSeasonPackRule` and `TestDefaultSeriesLibraryRules` (the pack rule and the v4plus range on
  a library with no settings); `internal/decide/parser/sonarr`: `TestV4Plus` (each v4plus change against the v4 base),
  `TestV4PlusBitDepth` and `TestExportedParserIsV4Plus`.
- `internal/acquire/rss`: an RSS TV item routed by scene mapping name (`TestRSSSeriesBySceneName`), series batch
  coverage and partly in-flight series.

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
- `MonitoredEpisode` runs only for RSS items, against today's monitored flags.
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
| standard | one `tvsearch` per scene episode mapping: scene season/episode (`season=00` for specials), by id first, then the mapping's names with `q` (or `title`) only when the ids found nothing about the title ([indexers.md](indexers.md#id-searches-and-padding)); a season-specific scene name (mapping with a scene season and another name) searches by that name only, for its season only | episodes grouped by scene season: one `season=N` search per group (one episode: an episode search); season 0 as specials |
| daily | `season=YYYY&ep=MM/DD` by air date (no air date: by its numbers, where Sonarr fails) | by air year: `season=YYYY` for several episodes of a year, else the episode by date |
| anime | `tvsearch` ids `q=NN` (scene absolute, else absolute), ids `season/ep` (scene numbers), `t=search q="<name> NN"` and `tvsearch q=<name>&season&ep` per scene name (AnimeStandardFormatSearch on, as Prowlarr syncs it) | v5: one `season=N` search per scene season (a season-title name as `t=search q=<name>`); the aired episodes one by one only while the season still airs |
| specials (season 0, anime without absolute numbers) | `t=search q="<clean name> <clean episode title>"` per name (TV and anime categories), then the episode by its numbers | |

Names: `GetSceneNames` (mappings of the episodes' TVDB or scene seasons, plus all-season non-`tvdb` ones, Latin-1
only) plus the series title; episode mappings follow `GetSceneEpisodeMappings` (a `tvdb`-origin mapping searches TVDB
numbers). Clean titles are `GetCleanSceneTitle`; a feed whose caps say `searchEngine="raw"` gets raw and clean.

Other sources keep the plain query in TVDB numbering: DMM and Torrentio key by IMDb id (Cinemeta/TMDb numbering),
Zilean filters by IMDb id and title, SeaDex by AniList id per TVDB season. A search for a title with no episodes
loaded keeps the plain query everywhere.

**Seasons searched together** (Theatarr's own; every series type, anime since phase AC). When one series version is about to search
two or more seasons (a newly added show, Search series, the wanted loop's due seasons), those season searches run
together as one merged search. `acquire.mergeGroups` takes the season targets above season 0 of one version with the
same reason and why: seasons are searched together per library version and reason, so missing seasons and seasons
below cutoff that come due together are two merged searches; an episode target and a dead link's search are never members, nor an anime series' season searched again after any failure (its seasons are then searched one by one). "Anime" is the series type, not the title's anime flag. The merge happens
where the runs start: `Submit` before its `MaxBulk` cut, `Round` after busy and running items left and before the
per-hour loop. Each member keeps its own run row (one root, the others its children), schedule row and stored search.
The merged search costs one search per member of `router.wanted.per_hour` and `MaxBulk`, and starts while one is
left (it may overdraw; a series is never split). A group with one member left when it runs (the others in flight)
is that member's ordinary search, recorded under the group's route source like any member.

Each member asks exactly its own queries, plus one query without a season (criteria kind `series`): Torznab
`tvsearch` by id, by title only where the id search falls back; Cardigann the series name, by id first; DMM and
Zilean by IMDb id alone (without one their own checks refuse the query); Torrentio and SeaDex need a season and are not asked. An anime series' query without a season also asks by text, for the series name and the scene names its seasons' queries use, with the anime categories (a Torznab feed without a text tv-search gets `t=search&q=`). From that query's answer only releases
that can be a pack of several seasons are kept: whose name reads as two or more seasons, or for an anime series a name that states a span or an absolute batch that fills whole seasons, read as the decision reads it (a batch a franchise entry places is left out); how many were left out ("outside the whole-series search") is a note on every
member. The filter that keeps only rows about the title (for answers found by id) reads them the same way, in that query and in each member's, so "S1+S2" is not dropped before it is decided. Cardigann asks an anime series' query without a season by text as well as by id. Every result is decided with the criteria of the queries that returned it: a `series` criterion takes only a
release of two or more seasons that maps an episode of a searched season ("Wrong season" or "Holds one season"
otherwise). The results are ranked together and routed once, on the root's source, as a flagged request (router.md
"Merged searches"), whatever the members' own queries did. The bounded cache checks (TorBox asks ten) ask the best
accepted pack of several seasons first, then each member's best release of one season, then the other packs, then
the rest; every member with a release for one of its episodes
carries that route job. A member is partial or unanswered only when its own queries failed (recorded beside its
routing outcome); a failed query without a season is a note. Its stored search holds the decisions that would
download one of its episodes (a rejected one: that map one), under the route's source. Why, Last search, the
episode window and History find a member's downloads by that source and its episodes: a member's Why shows its own
stored search or, with none (its season found nothing, or was skipped), only its counts; a download's Why the member
whose list holds the grabbed release and whose season holds its episodes, else the member of its season; pruning keeps the stored
searches while a download of the route lives. A search of one season or one episode, RSS and films are
unchanged.

**Anime packs of several seasons** (phase AC; code in `decide/animepack.go` and `library/animepack.go`). Only a
decision made for a search of several seasons (a merged search, or the whole-series window) reads an anime release
as a pack of several seasons; a one-season search, a one-episode search and RSS parse and decide as
before ("Multi-season releases are not supported" stays there). Two shapes read as a span:
- a name that states its seasons, read by an anime reader of its own (the standard series' reader and the parser are
  untouched): both sides marked ("S01-S03", "S1 - S3", "S01 to S03", "S01~S03", "Season 1 - Season 3", "S1 & S2"),
  the plural ("Seasons 1-3", "Seasons 1 to 3", "Seasons 1+2"), "Season" with "+", "&", "to", "~" or a tight "-"
  ("Season 1-3", "(Season 1+2)", "(Season 1+2+OVA+Specials)" is seasons 1 to 2), a tight "S01-03", and lists
  ("S01.S02.S03", "S1+S2+S3"): ascending, steps of one. Not a span: "Season 2", "2nd Season", "Part 2", "Cour 2",
  "S2 - 01-12", "S1 - 02", "Season 2 - 05" (a spaced dash before a bare number is an episode), "S01+S03", "S03-S01",
  "S01E12-S02E01", bare "Complete" or "Batch", and "Complete Series" (unread for anime). The phrase is read as
  "S<first>" for the parser, so "Seasons 1-3" is not absolute 1 to 3. The library must have every season of the span,
  none numbered differently by the scene (a scene season or number, or a scene mapping's offset), no regular episode
  of another season numbered by the scene into the span, and none in a franchise entry marked as conflicting; then
  it is mapped by TVDB season (as Sonarr maps a multi-season pack) and claims every regular episode of those seasons.
  A span name that fails any of this, or whose episodes after the anime mapping are not exactly those, keeps no
  episodes and is refused ("Unable to identify correct episode(s)"): it is never read again as its first season's
  pack, and not even a grab by override routes it without its claim.
- an absolute batch (two or more numbers, not a special, no season marker) placed by the plain numbering (not by a
  franchise entry, an alternative cut or a SeaDex mapping; not a SeaDex feed result) whose numbers each map to one
  regular episode, those being every episode of two or more whole seasons. "001-030", "13-30" and "- 12-13" across a
  break are not spans.

The decision stores what the pack claims with the download (whether its name states seasons or it is an absolute
batch, its seasons, the episodes it claims and, for a batch, each number's episode); the claim travels with the
route and is the whole pack's, also when the download is narrowed. For anime, whatever asks "is this a pack of
several seasons" (the route's attempts for packs, the order of the cache checks, the release rows' "Seasons 1 to
3", the journal's checks) reads the claim, not the name; a batch across a season break counts against its first
season's attempts. A pack of several seasons counts as a season pack in the merged order and in the rule that a
season pack waits until every episode has aired. It carries no SeaDex standing at all, on every library: its SeaDex
marks and SeaDex ids are cleared on the release wherever it is decided, ranked, stored, routed or imported. When
SeaDex lists the torrent for a season, the SeaDex feed returns it as that season's own row, which carries the
standing, so a season's own SeaDex pick keeps winning that season; the pack is only taken where no SeaDex row won.
The standing an import stores for each proved file, and held files, are untouched. Known cost: where the SeaDex feed
gives no row (the whole-series window, a failed SeaDex query) a SeaDex-listed pack shows no SeaDex mark. An anime absolute batch across seasons gets the same claim
(no span) in every search and RSS: its decision is today's, its import is proved.

At import (`library/animepack.go`) a download with a claim, and no files chosen by hand, is proved before any link.
Extras are set aside and never linked (the marker rule below), as is a file the parser reads as a special or on
season 0. Every other video file must read as
numbers in the claim's convention: bare absolute numbers under a batch's claim (number n is the episode stored for
n; a season folder is allowed only when it is that episode's season, so "Season 2/Show - 13" passes and numbers
restarting per folder fail); under a named claim a season from the file's own name ("S02E05", "S2 - 05", "Show
2nd Season - 05", "Show (Season 2) - 05"; under a batch's claim such a file is rejected) or one season folder ("Season 2/Show - 05.mkv", "Show 2nd Season/"; a folder and a name that
disagree fail; a folder that names a range, "Show S01-S02", names no season). A file's name is read with the library's
own title dropped first (never a scene name, which can carry a season), so a number in the title is never an
episode ("Brave 10 - 11" is episode 11); a file holds several episodes only when its name joins them ("S01E01-E02",
"S01E01-03", "1x01-1x03", "2x9-2x10", "E01-E02", "01-02", "01-02-03", "01+02"), never across a title separator
("Brave 10 - 13"), and a plus sign joins bare numbers only ("S01E01+E02" and "1x01+1x02" join nothing: the pack
fails and its seasons are searched one by one): the parse must read
the episodes, and a joining token confirms them: it stands on its own outside brackets, after resolution and bit-depth
tags between delimiters ("1080p", "-10bit", "_10bit_") are blanked, no letter or digit touching it (an episode marker
`E`, `EP`, `SxxE` or `Nx` is part of the token), and its first and last numbers are the parse's, so a tag such as "x264-10bit",
"H265-10bit" or "DDP2.0-10bit" joins nothing. A token the parser reads as one episode ("EP01-02" and "Ep01+02" read as
episode 2, "S02E03 - EP03-04" as S02E03, "EP01-5.1ch" as 1) holds that episode only, as on 0.4.5.10. A folder's name is read whole by the same marker
rule before a season is read from it ("Season 1 [OVA]", "Disc.OVA", "OVAs" are folders of extras), except a
wrapper that names a season range ("Show (Season 1+2+OVA)"), whose own name is not read. Extras follow one rule that can only fail closed: a marker word (OVA, OVAs, NCOP, creditless, recap,
preview, opening, ending, movie, film, OP or ED with a number in any case, bare OP or ED in capitals, a decimal
"12.5" after " - ", and the like; not "5.1" audio) anywhere in the name after the series' name sets the file
aside, and nothing un-marks it. An episode whose title holds such a word ("S01E05 - Special Training") is set aside
too; its episode then has no file and the pack is rejected. No episode may be claimed by two files, and the files'
episodes must be exactly the claimed ones. The proof's table is the placement (no franchise seam, no canonical
fallback); a file of a season the download was not given is out of scope. A failed proof, or a proved pack none of
whose files is for an episode the download was given, runs the import rejection: download failed, nothing linked,
the release blocklisted for the library, never held. Anything after placement ends as for any other download (not
an upgrade, a naming collision, the SeaDex veto). After a rejection each season of the download is searched again, each by itself (an anime series' seasons are not
searched together after a failure), and the blocklist keeps the release from coming back. An unconfirmed zurg listing waits first, as today. The later passes that re-read files after a mapping
change leave a claimed download's links alone, and it is not linked into other libraries at import (they get it by their own search).

**Judging the answers.** The target's search (`SearchTarget.Series`, built by
`decide.SearchFor`) is in TVDB numbering: the episodes wanted by their own season and number. Each query carries its
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
a tie (`DeDupeDecisions`). A release imported for another library is judged the same way (router.md "Other libraries at import"). The query key and the one-query-per-key
sharing across versions stay as they are: criteria are per target, so two versions sharing a request each judge its
answer with their own.

Both `SearchFor` kinds give a daily season search `daily-season` criteria (Sonarr's
`DailySeasonSearchCriteria`, no season check): a daily release parses as season 0, so `SeasonMatch` would reject it as
the wrong season.

Deviations: v4 anime season searches also search every episode (dozens of requests per feed); Theatarr follows v5. Non-Latin
names are never search terms (Theatarr's TVDB aliases include translations that Sonarr's mapping list does not). No
monitored filter: the target's episodes are already the wanted ones. An anime episode without any absolute number is
not searched as `00`. Aggregate id searches (`tvdbid` and `imdbid` in one request) are not used.
