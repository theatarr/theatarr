# Series metadata refresh (`internal/metadata/tvrefresh`)

Theatarr keeps its own persistent TV metadata: episodes are read from TVDB v4 (TMDb as a fallback), so a series does
not depend on a Sonarr import for its episode list. Migration `00030_tvmeta.sql`.

## What it does

`meta.series` jobs (one per title, dedupe `meta.series:title:<id>`) read the series from TVDB v4
(`tvdb.Client.SeriesEpisodes`: default order, English names when the series is not English, absolute numbers from the
absolute order) and store it in one transaction:

- **Identity:** TVDB rows match by (season, episode), as Sonarr's RefreshEpisodeService.cs:56–79 does. If TVDB
  recreates an episode with a new id at the same slot, its Theatarr row, files, links and monitoring survive and take the
  new TVDB id. TMDb fallback rows still match by source id first, then empty-id slots.
- **Fields:** season, number, absolute number (`episode_number`, scheme `absolute_tvdb`, source `tvdb`), name ("TBA"
  when empty), air date, `air_date_utc` from airs time and the network country's time zone (TVDB v4 has no zone: a
  country to IANA zone map, US = Eastern, unknown = UTC), runtime. Sonarr ports: duplicate slots dropped, multi-episode
  air times spread (up to 3, not specials), direct-to-DVD air dates, anime absolute mapping.
- **Renumbering:** TVDB identity follows the slot, including when TVDB moves an id between slots. A TMDb episode
  that moves keeps its row; moves use a temporary negative number to preserve the unique slot index.
- **Removals:** a row TVDB no longer lists is deleted only when nothing refers to it. A row with an arr alias, a file,
  a link, a download (its episodes, or a file mapping of it: `download_file.episodes`, as a pending manual import
  keeps them, `TestManualFileMappingKeepsEpisode`) or a non-wanted state is kept with `removed_at` set (and parked at number `-id` when its slot is
  needed). Removed rows are never searched (acquire filters them unless asked for by id). The calendar and the arr/native
  episode APIs list them.
- **Series row:** status, air time, zone, episode count and errors in `series_meta`; name, sort name, year,
  runtime and IMDb id refresh on each successful fetch. A fed arr remains authoritative until cutover, checked in
  the write transaction (`TestFillTitleRechecksOwnership`). User settings, including explicit standard/daily/anime
  series type, stay. Arr API adds (for a series with no type) and edits persist that type on the catalog row every
  reader uses (`docs/design/series.md` "Series type").
- **Scene names:** TVDB names and translations become all-season aliases (`tvrefresh:tvdb`). A separate
  `meta.scene` job refreshes Sonarr scene aliases, XEM season-specific aliases and XEM episode/absolute numbers
  every three hours; numbers go into the existing `episode_number` scene schemes read by decide/search. See below.
- **Monitoring intent** is durable in `series_monitor` per version: `monitor_new` (`all`/`none`, Sonarr's
  MonitorNewItems) and per-season choices. New seasons are monitored iff `all`, season 0 never. New episodes of a
  version get `episode_version_state` rows from it (Sonarr's GetMonitoredStatus); episodes TVDB re-adds that aired
  over a day ago are unmonitored on a series that already had episodes (UnmonitorReaddedEpisodes). Set by the add
  (`monitor_new` on `POST /api/theatarr/v1/titles`, `monitorNewItems`, seasons and `addOptions.monitor` on the arr add),
  season monitor changes in the UI and arr views (PUT `series` applies only changed seasons), `monitorNewItems` from
  PUT `series` and `series/editor`, and the arr import (the arr's `monitorNewItems` and season flags, by the instance
  that owns the version). Add-time modes (all, future, recent, missing, existing, pilot, first/last season, none
  and specials) wait in `series_monitor.pending_monitor` when episodes do not exist, then apply once after the first
  refresh supplies episodes, before held searches run. Later refreshes preserve the user’s episode choices.

**TMDb fallback:** a non-anime series TVDB lacks (no id, or 404) is read from TMDb (`/tv/{id}` and each season; id
from the title or `find` by TVDB id). Anime takes TVDB only.

## When

- **On add:** the catalog marks `series_meta.added_at`; a poll (every minute) queues those. Searches for the series
  (add with search, arr `SeriesSearch`/`SeasonSearch` without episode ids) wait in `series_search_wait` and go to the
  acquirer when the first refresh finishes, or when no source has the series (Sonarr's SeriesScannedHandler). A held
  search counts as one queued item; a released search that finds nothing completes its arr command with a message.
- **On demand:** the title refresh job (`POST .../refresh`, UI) and arr `RefreshSeries` queue it forced (cache
  invalidated); `theatarr tvdb refresh TITLE-ID [-force]` runs it inline.
- **Periodically:** a sweep every `metadata.series.interval` (default 1h, at most `per_run` 50 series) after Sonarr's
  ShouldRefreshSeries: never refreshed, or over 30 days, or a TBA aired episode after 6 h, otherwise not within 12 h,
  ended series only if the last episode aired in the last 30 days. Failures wait 6 h; three in a row stop the sweep.
  The hourly `/updates` run also queues series TVDB says changed.

Policy: `metadata.series {enabled, interval, per_run}` (enabled by default when a `tvdb` or `tmdb` source exists).

## Shadow precedence (Sonarr import still runs)

The arr instances in `import.arr` are **fed**. Rule:

1. On rows that came from a fed instance (`arr_alias` kind episode), the refresh matches by slot and only fills empty
   fields and an empty TVDB id; the import stays authoritative and the refresh never moves, renames or removes them.
   Fill-only means fill-only: a nonempty id on such a row is never replaced or cleared, even when TVDB now gives that
   id to another episode. That episode then goes without an id (inserted, or left as it is) until cutover; its row
   is matched by slot meanwhile (`TestShadowPrecedence`).
2. Versions fed by an instance keep the import's monitoring: the refresh writes no states for them. Series identity,
   scene names and scene numbering also remain the owning arr's; followed versions share those catalog mappings.
3. The import does not delete rows that carry a TVDB or TMDb episode id when Sonarr drops them.
4. At cutover, removing the instance from `import.arr` hands the series to the refresh. Successful scene-name reads
   retire imported aliases; XEM replaces imported scene numbers. Failed reads preserve the previous mappings.

During shadow, episodes TVDB has and Sonarr lacks appear in the episode table and the shadow decisions see them.

## Anime

The anime pipeline stays authoritative for franchise mappings and orders; the refresh supplies the episode list,
TVDB's absolute numbers (`absolute_tvdb`), scene aliases and ongoing XEM numbering. XEM extrapolation follows
Sonarr, including gaps, season shifts, specials at a scene season's tail and the Fate/Zero alias exception.
Titles added with the anime flag but no explicit series type are loaded as anime for decisions and absolute-numbered
searches; an explicit series type still takes precedence.

## Scene sources and freshness

Ported from Sonarr `DataAugmentation/Scene/SceneMappingService.cs:135–182,282–308` and
`DataAugmentation/Xem/XemService.cs:36–235` / `XemProxy.cs:21–124` @ cab419a. The three-hour job is independent of
the series sweep. Successful raw responses persist in `scene_refresh` (migration `00052_metafresh.sql`) and are
replayed for newly refreshed episodes and cutover titles. Empty name lists retain prior aliases; successful empty
number mappings clear obsolete numbering. Each provider fails independently; failed requests do not advance freshness.

The existing `metadata.sources.thexem` entry (`https://thexem.info`, budget `thexem`) supplies
`/map/allNames?seasonNumbers=true&origin=tvdb`, `/map/havemap?origin=tvdb` and `/map/all?id=TVDB_ID&origin=tvdb`.
For Sonarr's other scene source, configure `metadata.sources.sonarr` with URL `https://services.sonarr.tv/v1`
and a named budget, for example `sonarr` with 60 requests per minute. Unconfigured sources are skipped. Requests
use gateway metadata targets, allowlists, budgets and back-offs, without the TVDB authorization wrapper.

The existing catalog/readers store the display alias and season limits; distinct scene `searchTitle`,
`filterRegex` and `searchMode` remain unsupported. Sort names follow Theatarr's lowercase convention, since the arrs'
sort titles come from their metadata servers.

## Movie freshness and identity

Movie metadata follows Radarr `Movies/ShouldRefreshMovie.cs:30–57` @ a96bf7c: refresh every 12 hours while
announced/in cinemas or physically released within 30 days, otherwise every 180 days. Physical release falls back
to cinema + 90 days, then an unknown future date (`MovieMetadata.cs:77–80`); digital dates do not set this cadence.
TMDb refreshes and arr imports update nonempty title, sort name and year so decision year checks and text searches
read current identity; user monitoring stays. A followed movie version takes canonical release dates from its
owning arr while that feed is active, including later corrections; after cutover it uses Theatarr's successful TMDb dates.

## Tests

Recorded TVDB answers for Severance, Slow Horses and Frieren (`testdata/`, trimmed, no keys or URLs), edited in memory
to simulate new seasons, recreated ids, removals and fed rows. Local HTTP scene fixtures cover source paths,
three-hour caching, failures, aliases, extrapolation and cutover. Monitor tests cover each add mode after episodes
arrive and one-shot consumption. Movie tests cover cadence boundaries, identity and followed date ownership. No live calls.

## Limits

- Removed rows show in the calendar and episode APIs (flag only).
- New episodes that aired recently are searched by the wanted loop, not at refresh time.
- Specials' airsBefore fields come from the listing only (no extended fetch per special, unlike `Show`).
