# The arr views (`internal/arrview`)

The arr views answer Radarr- and Sonarr-shaped requests, so Seerr, Homepage, decypharr, autobrr and the phone apps
talk to Theatarr as if it were their arr. Where they sit: [overview.md](overview.md). Callers and the fields they
read: [clients.md](clients.md). Code: `internal/arrview`, migration `00012_arrview.sql`, `auth.Service.CheckAPIKey`,
comments in `examples/policy/views.yaml`.

The views answer from Theatarr's database, with metadata lookups for new series, and write only to that database.
Versions in `mode: shadow` never acquire anything: nothing is searched or grabbed for them. Search commands for active
versions go to the acquirer and the router (Commands). Pointing clients at the views: see Cutover.

File hydration chooses the owning instance's row first, then the oldest row, before decoding languages or media
JSON. Discarded alternatives skip that decoding. A link from Theatarr's own import still wins over an arr file;
an adopted link only fills a version with no file row.

Radarr collection intake (`GET/PUT collection`) and the TMDb import-list surface (`GET/POST/PUT/DELETE importlist`,
`importlist/schema`, TMDb List by `fields.listId` only) keep their target profile/root as Theatarr versions. Existing catalog
memberships appear as unmonitored collections; GET includes missing TMDb parts after refresh, and PUT accepts a
single collection or `collectionIds` with shared settings. Refresh runs at startup and every `metadata.lists.interval`
(default `24h`, minimum `10m`), skips `/exclusions`, and adds through the shared catalog path. A movie the catalog
already holds gets only the list's or collection's target versions it has no row for
(`TestListIntakeAddsMissingVersionsOfHeldMovie`); a version it has in any state (unmonitored, removed) stays as it is.
Import-list reads, updates (including a body-only id) and deletes are scoped to the caller's view; another view's
id answers 404 and cannot be taken over. The same TMDb list stays globally unique.
Lists with `enabled` and `enableAuto` import movies; `monitor`/`shouldMonitor: none` adds them unmonitored,
`movieAndCollection` also monitors their collections. Minimum availability is stored for future automatic searches
and gates `searchOnAdd`; future movies still enter the catalog. Collection intake adds nothing while unmonitored.

### Collections

The same `movie_collection` rows back the native **Collections** page (Library › Collections, `/collections`, managers
and admins; `GET /collections`, `PUT /collections/{id}`): name and poster after the first refresh, movies in the
library out of the collection's total, the monitored switch, target libraries, search on add and minimum
availability. Monitoring a collection adds its missing movies at the next refresh. Search on add starts the usual
add-time search through the acquirer and router, so nothing grabs while `router.enabled` is off.

**Add collection** on the page adds a collection that has no movie in the library yet: `GET /collections/search?q=`
searches TMDb (`/search/collection`, first page, cached a week; each result says whether it is already known), and
`POST /collections` (`tmdb_id`, `versions`, `monitored` default true, `search_on_add`, `minimum_availability`) reads
the collection from TMDb once and stores the row with its name and parts; a collection that is already known is
updated as `PUT` does and keeps the view, profile and root an arr client gave it. A monitored collection then gets its
missing movies in the same request, through `MovieIntake.RefreshCollection`, the one-collection form of the scheduled
refresh (one TMDb request per missing movie). `POST /collections/{id}/refresh` is that refresh for a known collection
(**Add missing movies now** on a monitored row). Both answer the collection with `added` and `skipped`; a refresh
that fails after the row is stored still answers 200, with `refresh_error`, and the scheduled refresh retries. The
validation is `PUT`'s: collection targets only, and a monitored collection needs one. Without TMDb
(`metadata.sources.tmdb` and its key) the three endpoints answer 503 and `GET /collections` says `can_add: false`, so
the page disables the buttons. The dialog turns search on add on by default; discovered rows keep it off.

A collection's targets are always explicit, and only **collection targets** qualify (`policy.CollectionTarget`): movie
versions in `mode: active` that no version follows, the versions the library gate lets write. The page lists only
those; `PUT` refuses any other key, and refuses monitoring with no target (an empty list, or stored targets none of
which qualify). `GET` leaves stored targets that no longer qualify out. Intake revalidates the stored targets at every
refresh: it adds only to the ones that qualify, skips the rest with one log line per collection and target set, and
adds nothing for a monitored collection left with none. So a discovered collection's preselected versions (its
members' versions, shadow ones among them) or an old empty list never add to a shadow or followed library. The Radarr
view's `PUT collection` follows the same rule: when it leaves a collection monitored, the targets (from its profile
or root, else the stored ones) are cut to collection targets and stored explicitly; with none left, a per-version
view `/v/K` whose K qualifies uses K, and otherwise the request is a 400 validation error (`TestCollectionPutTargets`). A `movieAndCollection` list monitors the collection only in the
movie's versions that qualify.

Held counts the catalog's movies with a visible version (not removed from every view) whose TMDb id is among the
collection's TMDb parts, whether or not a membership row links them; total is the parts, known after the first
refresh. Membership (`title_external_id` source `tmdb_collection`, which discovery reads) comes from TMDb's
`belongs_to_collection`: arr imports get it from `--enrich`, intake adds from the movie they add, and every movie
gets it on its metadata refresh (`meta.art`), so natively added movies, and older ones on their next refresh, show
up as collections without a separate backfill.

Each Radarr `arr.import` (and `theatarr import-arr`) also reads `GET collection` and carries every collection the arr
monitors over (`internal/importarr/collections.go`): monitored, with the arr's search on add and minimum availability,
targeting the instance's version and its followers (`versions.K.follows`) that are collection targets, in practice
the active followers, or the version itself when it is active and nothing follows it. A collection Theatarr already
monitors keeps its settings; the import drops its stored targets that no longer qualify (an empty list among them),
adds the missing ones and turns search on add on when the arr has it. The import never unmonitors or deletes a
collection, so turning one off in Theatarr lasts only until the next import while the arr still monitors it: unmonitor
it in the arr too. An instance whose version and followers include no
collection target has nowhere to add, and its monitored collections are skipped with a warning in the import summary.
A Radarr without the endpoint imports without collections. Movies a collection adds to a follower are not the arr's titles: the
next import does not unmonitor them (`TestCollectionIntakeTitleSurvivesArrImport`).

Known limits, kept as they are:

- Search on add is OR-merged: with two Radarrs monitoring the same collection, either one's search on add turns it on.
- A collection has one minimum availability, whichever arr (or the page) set it first; later imports keep it.
- A follower target newly added to an already monitored collection takes that collection's minimum availability,
  not the old Radarr view's for the follower.
- A collection-fetch failure is a warning in the import summary (`theatarr import-arr` and the job log); the
  import-runs page does not surface it.
- A `movieAndCollection` list does not turn on a collection that discovery already created (unmonitored).
- Existing native movies get their membership only on their normal metadata refresh; there is no backfill job.
- The Radarr view's `GET collection` shows a held part that has no membership row as missing.
- A refresh never removes a stale membership row (a movie TMDb moved out of a collection).

### Lists

The same `movie_import_list` rows back the **Lists** tab of the Collections page (`/collections/lists`, managers and
admins): TMDb movie lists only; other providers and series lists are not supported. `GET /lists` shows every row, of
any view; `GET /lists/lookup?tmdb_id=` reads a list's name, description and movie count from TMDb so the form can
confirm it (the form takes the id or a pasted themoviedb.org list address); `POST /lists`, `PUT /lists/{id}` and
`DELETE /lists/{id}` manage them, and `POST /lists/{id}/sync` imports one list now, enabled or not, through
`MovieIntake.SyncList`, the one-list form of the scheduled sync (one TMDb request per new movie, in the request).

A list is read in full: TMDb pages its lists, and both the scheduled sync and Sync now fetch every page (one paced
request per page, by `total_pages`, or `item_count` where TMDb sends none) before adding anything. A page that fails
fails the read, so the lookup answers an error and the sync reports it, adds nothing and leaves `last_sync_at`
where it was; part of a list is never taken for all of it. TMDb answers the list id as a string, which the client
accepts.

- A list made on the page is a **native row**: `view = ''`, no profile, no root, and `enable_auto` equal to
  `enabled`. Its `versions` are chosen explicitly from the movie libraries (any movie version, as an add offers them;
  at least one), and intake picks the ones that fit each movie. No arr view lists it (`readImportLists` filters by
  view), and the views' duplicate check still refuses a second row for the same TMDb list, as `POST /lists` does with
  a 409 for a list any view already has.
- A list an arr client made keeps its `view`, `quality_profile_id` and `root_folder_path`: `PUT /lists/{id}` never
  writes them, so that view keeps listing it with its profile mapping (`TestListsKeepArrView`). It may change the
  name, `enabled`, monitor, search on add, minimum availability and the libraries, which must stay within the view's
  versions; `enable_auto` stays as the client set it. The page labels such a row "Created through the *view* view".
- Deleting a list keeps the movies it added. Without TMDb the lists are still shown and edited; lookup and sync
  answer 503 and `GET /lists` says `can_sync: false`.

## Views

| Path | Flavour | Versions | Profiles | Key |
|---|---|---|---|---|
| `/radarr` | Radarr | movie-remux, movie-web, anime-movie | Anime, Rules default, WEB only | `THEATARR_VIEW_RADARR_KEY` or an active admin API key |
| `/sonarr` | Sonarr | tv-2160p, anime-tv | Anime, Rules default | `THEATARR_VIEW_SONARR_KEY` or an active admin API key |
| `/v/<version>/radarr` or `/sonarr` | by the version's kind | that version | one, named after it | an active admin API key |

The configured views come from the policy's `views` section; every version gets its `/v/` view without
configuration. Each serves `<path>/api/v3/...`. Routing below the path is case-insensitive like ASP.NET's (Seerr asks
for `/qualityProfile`). `theatarr serve` mounts them on the main listener (`:7700`).

**Auth.** The arrs' order (`ApiKeyAuthenticationHandler.ParseApiKey`): the `apikey` query parameter, then the
`X-Api-Key` header, then `Authorization: Bearer`. Accepted: the view's own key (stored in the settings database and set in Admin › Settings › Connected apps, seeded once
from `api_key_env`; `api_key_env` in YAML mode; compared in constant time) or an active admin account’s API key from Settings (`internal/auth`; `CheckAPIKey` is the one addition there). User and manager keys cannot access arr views. The native API still refuses the query parameter (a test pins both). Failure is the arrs' bare `401` with no body. Every response
under `/api/v3` carries `X-Application-Version`: `6.4.4.10685` (Radarr) or `4.0.20.3014` (Sonarr), the releases Theatarr's
ports are based on. Seerr parses the major (Sonarr 4: no language profiles); decypharr tells the flavours apart by
`appName`. The request log never has the query string, so keys sent as `apikey` are not logged (`internal/server`).

## Ids

| Thing | Id |
|---|---|
| Movie, series | `1,000,000 + title id`, the same in every view |
| A replaced instance's movie or series id (< 1,000,000) | looked up in `arr_alias` (written by `theatarr import-arr`) for the view's `alias_from` instances, first match in that order |
| Episode | `episode.id` |
| Movie file | `version_file.id` |
| Tag, command | Theatarr's (`arrview_tag`, `arrview_command`), global (a tag's assignments to titles are per version, see "Tags per version") |
| Quality profile | `arrview_profile.id`, given the first time a view sees the profile name, then kept |
| Root folder | position of the library in the view's version list, from 1 |

The offset keeps Theatarr's ids and the arrs' apart: without it title 7 and an arr's movie 7 would collide. So
Seerr's stored `externalServiceId` keeps answering after cutover, and its scanner then rewrites them to Theatarr's
ids from `GET movie`. An id from an arr outside `alias_from` can
name another title until that scan: see [Seerr's stored ids](#seerrs-stored-ids).

## Endpoints

All under `/api/v3`. "Shape" names the arr resource; `omitempty` follows the arrs' `WhenWritingNull` (a C# nullable or
reference type that is null is left out; value types are always written; `id` 0 is left out).

| Endpoint | Shape (source @ a96bf7c / cab419a) | Notes |
|---|---|---|
| `GET system/status` | `SystemResource` (SystemController.GetStatus) | `urlBase` is the view path; `instanceName` `theatarr <view>` |
| `GET qualityprofile`, `qualityprofile/{id}` | `QualityProfileResource` | The view's stable selection IDs; the first selected version supplies the captured runtime's ladder, cutoff and scores |
| `GET customformat`, `customformat/{id}` | `CustomFormatResource` | The captured runtime's full specifications; format IDs are scoped to the view and agree with its profile scores. No format/profile writes |
| `GET rootfolder` | `RootFolderResource` | The libraries of the view's versions; `freeSpace` from statfs when the folder exists. Stat and statfs give up after 3 s per root (`internal/rootprobe`): a root on a share that stops answering is `accessible: false`, and is skipped without another call while a stat on its filesystem (mount) is stuck |
| `GET tag`, `tag/{id}`, `POST tag`, `PUT tag[/{id}]` | `TagResource` | `TagService.Add`: an existing label (any case) returns the existing tag, new labels are stored lower-case; label must match `^[a-z0-9-]+$`. POST 201, PUT 202 |
| `GET movie[?tmdbId=]`, `movie/{id}` | `MovieResource` with `movieFile` (`MovieFileResource`) and `statistics` | Only the view's versions count. `hasFile` = any of them has a file; `movieFile` = the file of the first version in the view's order that has one; `statistics.movieFileCount` counts distinct files. `status` uses refreshed canonical dates (SkyHookProxy), falling back to the imported arr status until refreshed; `minimumAvailability`, `isAvailable` and `releaseDate` follow "Minimum availability" below. 404 when the view does not have the title |
| `GET movie/lookup?term=`, `movie/lookup/tmdb?tmdbId=` | `MovieResource` | `tmdb:N`, `imdb:tt…` or text. Text: the catalog's titles by name, then TMDb's title search for movies the catalog lacks (below) |
| `GET moviefile/{id}` | `MovieFileResource` | Files of the view's versions |
| `POST movie`, `PUT movie[/{id}]`, `DELETE movie/{id}` | | See Writes. POST 201, PUT 202, DELETE 200 with no body |
| `GET series[?tvdbId=]`, `series/{id}`, `series/lookup?term=` | `SeriesResource` with `seasons[].statistics` and `statistics` | Counts follow `SeriesStatisticsRepository`: `episodeCount` = monitored and aired, or with a file; `totalEpisodeCount` = all; `episodeFileCount` = with a file. `seasonCount` leaves out specials |
| `POST series`, `PUT series[/{id}]`, `DELETE series/{id}` | | As for movies; `seasons[].monitored` sets the per-version episode monitoring |
| `GET episode?seriesId=[&seasonNumber=]`, `?episodeIds=`, `episode/{id}` | `EpisodeResource` | `monitored` and `hasFile` are the view's (below) |
| `PUT episode/monitor` | `EpisodeResource[]`, 202 | Sets `episode_version_state.monitored` for the view's versions |
| `GET queue` | `PagingResource<QueueResource>` | Theatarr's downloads still in hand (queued, adding, added, downloading, ready, importing; a held import stays until imported or removed) for the view's versions (`internal/arrview/queue.go`). Default `page` 1, `pageSize` 10, sort `timeleft` ascending (QueueController). `DELETE queue/{id}` and `queue/bulk` end Theatarr's record only; nothing is removed on debrid |
| `GET queue/status` | `QueueStatusResource` | Counts of the queue items; warnings and errors include held imports and failures awaiting manual resolution |
| `GET queue/details` | `QueueResource[]` | Every queue item, unpaged; `movieId`, or `seriesId` and `episodeIds`, filter it. Homepage calls it always |
| `GET wanted/missing` | `PagingResource<MovieResource>` / `<EpisodeResource>` | Radarr: monitored without a file, sort `movieMetadata.sortTitle`; Sonarr: monitored, aired, no file, series monitored, sort `episodes.airDateUtc`. `monitored=false` inverts |
| `GET history`, `history/since?date=`, `history/movie?movieId=`, `history/series?seriesId=` | `PagingResource<HistoryResource>` for history; `HistoryResource[]` for the other routes | Imported and local download history; SQL filters `eventType` (number or name, repeated or comma-separated), `downloadId`, `movieIds`/`seriesIds`, Sonarr `episodeId` and title history `seasonNumber`. Optional `includeMovie`, `includeSeries`, `includeEpisode` embed the matching resources. Default date descending; `/since` is inclusive and ascending |
| `GET health` | `HealthResource[]` | One `notice` while any of the view's versions is in shadow mode; one `warning` while `router.enabled` is off and any is active ([devices](devices.md)) |
| `GET diskspace` | `DiskSpaceResource[]` | One per existing root folder (statfs), bounded like `rootfolder`: a root that does not answer is left out |
| `GET config/downloadclient` | `DownloadClientConfigResource`, `id` 1 | Completed download handling on, auto-redownload on |
| `POST command`, `GET command`, `GET command/{id}` | `CommandResource` | See Commands |

History, wanted missing/cutoff, blocklist and queue select pages in SQL before loading resources; counts include every matching record. Defaults remain page 1 and size 10; nonpositive pages clamp to 1, size 0 returns an empty page, and malformed or non-Int32 values and negative sizes return 400. Offset multiplication uses checked 64-bit arithmetic; queue follows the arr's fallback to its last page when the requested page is empty. Unlike the reference repository's unsafe negative LIMIT and overflowing Int32 arithmetic, invalid sizes cannot become unlimited reads.

Title sorting uses the same Unicode fallback as the resources through a SQLite scalar function. Quality cutoff judgements and ETA sorting process compact candidates in batches of 128 and retain matching ids or one numeric key per download; only the selected page loads resources. The arr's plain-array history and queue/details routes intentionally remain complete lists.

Sonarr pack members have distinct, stable queue ids and share downloadId; deleting any member (including through queue/bulk) removes the whole download or pending approval release, as Sonarr's tracked-download and pending-release removal does. Bulk members are handled once per download, including blocklisting and redownload; provider content is retained. RefreshMonitoredDownloads shares one poll across views, with a 30-second cooldown even after completion/failure and across service restarts.

Movie status and `isAvailable` use canonical release dates from `meta.art`, shared with acquisition and the calendar;
a successful refresh supersedes imported dates, including corrections/removals. Migration 00041 adds the date-refresh
marker, makes existing movies due for refresh and clears the RSS seen cache.

### Minimum availability

Theatarr owns a movie's minimum availability, one rule per library (`availability.Rules`, by version). In order:

1. **Choice**: the newest `arrview_title` row with a value (by `updated_at`, then view key) whose view is the
   library's own (`v/<key>`) or a configured view that holds the library today.
2. **Imported**: the owning arr's value (`arr_title`, through `follows` for a parallel library).
3. **Default**: the library's `minimum_availability` setting (`announced`, `inCinemas` or `released`; movie libraries
   only, left out of the stored settings when unset), else `released`.

The rule also carries the library's `availability_delay` (days, negative for earlier, unset is 0), applied as Radarr's
`Movie.IsAvailable(delay)`: added to the date compared against, ignored for `announced`/`tba`, and a movie without a
date is never available. `availability.Movie`, the gate of every automatic search and grab, reads the same rule, so a
view cannot show one value while acquisition uses another. With neither setting set the rule is what it was before
the settings existed (`TestRulesMatchMain`).

What a Radarr view answers:

- `minimumAvailability` is the earliest value (tba, announced, inCinemas, released) among the libraries the view
  shows for the movie; a per-library view shows its library's. The list, `movie/{id}`, and the POST, PUT and editor
  answers all use it. A lookup result the view does not hold stays `released`.
- `isAvailable` is true when any shown library is available under its own minimum and delay
  (`availability.Available`, `TestViewIsAvailableWithDelays`); `releaseDate` ports `GetReleaseDate` for the shown
  minimum, without the delay, as Radarr.

The write convention (`TestMinimumAvailabilityEchoChangesNothing`, `TestMinimumAvailabilityPutWritesShownLibraries`):

- **`PUT movie`**: a value equal to what this view shows for the movie is the view's answer sent back and stores
  nothing: every library keeps its value and where it comes from, and the PUT answers what the GET did. One scalar
  cannot tell an echo from "set every library to the value shown", so that edit needs the editor (or Theatarr's own
  page). A different value is stored as the choice of each library the view shows for the movie, and no other: not a
  copy removed from the views, not a library outside the view. A copy the same PUT turns on by a profile change
  takes its library's own rule, not the body's value.
- **`PUT movie/editor`** is outside the echo guarantee. A supplied `minimumAvailability` is a deliberate edit, as
  Radarr applies it only when the field is present (`MovieEditorController.cs:64`): it becomes the choice of every
  library the view shows for each movie, even when it equals the value shown. Without the field nothing is stored
  (`TestMinimumAvailabilityEditor`).
- **`POST movie`** is decided per library the add turns on (new, restored or unmonitored before): a library with
  `minimum_availability` set uses it, and the client's value is not stored for it; a library without stores the
  client's value as its choice. Copies already on keep their rule, so an add that turns nothing on stores nothing
  (`TestMinimumAvailabilityClientAdd`). Seerr sends its Radarr server's setting with every add and the phone apps a
  per-add choice; the server cannot tell them apart, so a library default, once set, wins over both at add time. A
  PUT afterwards is still an edit.
- **Storage**: new choices go to the libraries' own `v/<key>` rows only (`availability.Choose`; a row made for it
  carries no profile, root folder or added time). Rows that combined views stored before stay readable with the
  precedence above; none is rewritten or cleaned up. `arrview_title.updated_at` orders the choices and moves only
  when a minimum availability is written: saving a profile, root folder or tags no longer promotes an old choice
  over a newer one.
- List and collection intake still stores the list's or collection's own minimum for the movies it adds, as a choice
  (a library default does not apply to them), and searches on add only the libraries that are ready under it with
  their own delay.

Left as before, on purpose:

- A PUT that succeeds in an anime check that failed earlier (above) can turn on the view's anime library for the
  title; the view then shows the earliest value including that library's, so this one unchanged PUT can answer
  another value than the GET did.
- `isAvailable` uses the canonical dates, while acquisition uses the owning arr's dates for a library an arr still
  feeds, until its cutover: the two can disagree for such a library when the arr's dates differ.

## Writes

- **`POST movie|series`** (Seerr's add): the title is found by TMDb (movies) or TVDB (series) id, or created with the
  body's title and year. Then the versions are turned on (a new version row starts `wanted`, monitored as the body
  says, default true; a version the views removed is restored):
  - the chosen profile's versions that fit the title (anime-only versions for anime titles, non-anime for the rest).
    When none of them fits and the profile is "Rules default" (let Theatarr's rules decide: the profile a view without
    profiles gets, `arrview.RulesDefault`; there is no other marker in the views config), the view's versions with
    `enable: default` that fit (so "Rules default" of non-anime versions puts an anime title in the view's anime
    versions). A profile picked on purpose is honoured: all its versions ("WEB only" on an anime movie gives
    movie-web; `TestAnimeFallbackOnlyForRulesDefault`). A view with one profile, like every `/v/` view, always uses
    it;
  - no known profile: the versions whose library is `rootFolderPath`; else the view's versions with
    `enable: default` that fit.
  - **Anime.** What the request says wins: the series type `anime` (Seerr sends it for TMDb's anime keyword), a root
    folder that is an anime version's library, or a profile of anime versions only. When it says none of these
    (a non-anime root or `standard` says nothing), Theatarr decides for a title it does not hold yet: first its anime
    mapping data (an `anime_entry` of a built franchise naming the TMDb movie; an `anime_mapping` into the TVDB
    series that is not rejected), then TMDb's Japanese-language animation (original language `ja` and the Animation
    genre, `tmdb.LooksAnime`, as the search box guesses) from the movie's details, or the series' record found by
    the request's `tmdbId` or the TVDB id; bounded to five seconds, outside the write. No data, no TMDb key or TMDb
    has no such title: not anime. Any other TMDb error (an outage): not anime for now, recorded `unknown`; the
    title's next add, PUT or `RefreshMovie`/`RefreshSeries` through a view asks TMDb again, and an anime answer flags
    the title anime and turns on the view's versions for it (chosen with the record's profile and root; the versions
    already on stay; a new series version takes the season monitoring the request gave the view's version already on,
`TestAnimeRecoveryKeepsRequestedSeasons`, `TestAnimeCheckRetriedAfterTMDbOutage`). A title already held otherwise keeps its
    flag. The source is stored on the view's record of the new title (`arrview_title.anime_source`: `request`,
    `mapping`, `tmdb`, `none`, `unknown`; migrations 00072, 00103) and logged ("anime decided"), so a wrong guess is
    visible; editing the title's versions fixes it (`TestAddDecidesAnime`).
  - A title the view already has gets the extra versions and a 201, not Radarr's "already added" 400.
  - Root folder, profile, season folder and monitor-new-items go to
    `arrview_title` (per view and title); tags to `arrview_title_tag`, on the view's versions of the title. A
    movie's minimum availability is decided per library ("Minimum availability" above).
  - Sonarr's `seriesType` is the title's one type (`docs/design/series.md` "Series type"), shown the same on every
    view. An add stores it only for a series that has none (`TestSeriesTypeAddKeepsStored`); the request's
    `anime` type is the anime hint below only for a series Theatarr does not hold or one with no stored type.
    `series/lookup` answers the title's type for a held series also in a view that does not show it
    (`TestSeriesTypeLookupOutsideView`).
  - `addOptions.searchForMovie` / `searchForMissingEpisodes` record a `MoviesSearch` / `MissingEpisodeSearch`
    command (below).
  - Sonarr's `addOptions.monitor` (Ruddarr, LunaSea; a name or MonitorTypes index) is applied after the body's seasons
    to the view's versions (`catalog.ApplyMonitor`, Sonarr's EpisodeMonitoredService): on a series whose episodes
    are known, each episode's per-version flag from the option and each season's choice from Sonarr's season rule.
    A new series has no episodes until its first metadata refresh, so the option becomes season choices only:
    `missing` is `all`, `existing` and `pilot` are `none`, `future` and `recent` monitor the last season of a series
    whose `status` is not ended (aired episodes of it included). `skip`, `unknown` and no option keep the body's
    seasons; Sonarr's legacy path for `unknown` (`ignoreEpisodesWithFiles`, which Seerr sends) is not ported.
- **`PUT movie|series`** (full object, as Seerr sends the lookup back): only the view's versions change. Only
  what differs from the view's own answer changes (`TestPutEchoKeepsOtherCopies`): a `monitored` that
  differs from the reported one (monitored when any copy is) applies to all of them, and a known `qualityProfileId`
  other than the reported one turns on that profile's versions (never turns any off), restoring removed ones; echoing
  the view's answer back leaves an unmonitored or removed copy alone; a movie's `minimumAvailability` follows
  "Minimum availability" above. Tags are
  replaced on the view's versions; Sonarr seasons set per-version episode monitoring. As in Sonarr's `SeriesService.UpdateSeries`, only a
  season whose `monitored` differs from what the view reports (monitored when one of its episodes is) changes its
  episodes: Seerr re-requests a season by PUTting the whole lookup back, and a partly monitored season it echoes keeps
  its unmonitored episodes. A season the view has no episodes for only records the choice; versions the PUT's profile
  turns on (new, restored or unmonitored before) take every season of the body (`TestPutProfileTurnsOnSeasonsOfExistingCopy`), and a version the new profile picks that was already on takes a season
  the body monitors when none of its own episodes of that season is (`TestSeasonRequestForMonitoredCopyWithSeasonOff`).
  `monitorNewItems` goes to the versions' intent (`series_monitor`), from
  PUT `series` and PUT `series/editor` alike, and the series resource reports that intent. Files are never moved
  (`moveFiles` is ignored).
  - **Series type.** A `seriesType` equal to the one the view showed stores nothing
    (`TestSeriesTypeEchoChangesNothing`); another value is an edit of the title's type, seen on every view and by
    every library (`TestSeriesTypeEdit`). The libraries a profile change, an editor profile change or an add turns
    on are chosen by what acquisition takes the series for once the request is stored: its anime flag, or its type
    then being `anime` (`acquire` `TestTargetsAnimeLibraryBySeriesType`). So a type sent back unchanged counts
    through the stored type, a changed one because it becomes the stored type, and an add on a held series, which
    never changes a stored type, by the stored one (`TestSeriesTypeChoosesSearchableLibraries`). An anime root
    folder or a profile of anime libraries only remains a request hint. Outside this guarantee: a title whose anime check is still
    `unknown` is asked about again after the PUT (above), and an anime answer sets the flag, so a series with no
    stored type then shows `anime`. `PUT series/editor` is outside it too: a `seriesType` it is given is an edit,
    also when it is the value shown; without the field nothing changes (`TestSeriesTypeEditor`).
- **`GET|HEAD <view>/ping`**: 200 `{"status":"OK"}` without a key, on every view, as the arrs' PingController
  (outside `api/v3`, so no `X-Application-Version`); any other method answers 405. It says nothing about the view.
- **`DELETE movie|series/{id}`**: unlinks only the view's versions. On a Radarr view `addImportExclusion=true` also
  puts the movie on the exclusion list (`movie_exclusion`, by TMDb id, one list for every view, the row the
  `exclusions` POST writes) in the transaction of the remove, so list and collection intake skip it afterwards;
  `false` or absent writes none. Sonarr's `addImportListExclusion` is ignored: there is no series exclusion list. With `deleteFiles=true`, Theatarr links in
  the versions' own library roots go through the library trash gate; every link is checked before anything changes,
  and a closed gate or refused path answers 409 with the title and links intact.
  Each is unmonitored and listed in
  `arrview_removed_version`, which every view hides; the version row, its files, history and arr records stay, and
  `deleteFiles=false` leaves links in place; imported arr files, mount content and provider data are never touched.
  The versions' downloads in hand end in Theatarr's records (failed, "deleted through <view> by <client>"), as the
  web UI's remove does (`router.EndRemoved`), in the same transaction as the hide, so none is imported back into the
  library and a failure leaves nothing removed; the provider keeps its torrents. With `deleteFiles=true` the links
  are moved only after that transaction commits (they are just checked before it;
  `TestDeleteUnlinksOnlyAfterTheHide`) and enumerated then: a link an import committed before the hide goes to the
  trash too, and an import still running finds its download ended at its commit and takes its links back (`TestDeleteUnlinksWhatAnImportCommittedMeanwhile`). A link the gate refuses at that point answers 409 "versions
  removed; some files were kept"; one whose move fails (a full disk) does not stop the others and answers 500
  "versions removed; moving some files to trash failed". Either way the versions are removed; the links left are the
  removed copies' leftover links on Theatarr's title page, where Remove with unlink moves them. A version an arr instance still owns (an import source in the policy) is refused with
  409 before anything changes, with the web UI's reason (`policy.ManagedReason`;
  `TestDeleteEndsDownloadsAndRefusesManaged`). The title stays for other views. A later add or PUT with a profile
  restores the versions.
- **Commands.** Accepted per flavour: Radarr `MoviesSearch`, `MissingMoviesSearch`, `CutOffUnmetMoviesSearch`,
  `RssSync`, `RefreshMovie`, `RescanMovie`, `RefreshMonitoredDownloads`, `DownloadedMoviesScan`, `ManualImport`,
  `RenameMovie`, `RenameFiles`; Sonarr `SeriesSearch`, `SeasonSearch`, `EpisodeSearch`, `MissingEpisodeSearch`,
  `CutoffUnmetEpisodeSearch`, `RssSync`, `RefreshSeries`, `RescanSeries`, `RefreshMonitoredDownloads`,
  `DownloadedEpisodesScan`, `ManualImport`, `RenameSeries`, `RenameFiles`. Others get a 400 (Radarr throws a 500).
  Each is stored in `arrview_command` and answered 201:
  - **All shadow.** A search whose versions (of the titles or episodes it names, or all the view's) are all in shadow
    mode is stored `completed` with `Shadow mode: nothing searched or grabbed (versions …)`. Nothing is searched.
  - **Active.** A search touching an active version is stored `queued` and handed to the acquirer
    (`internal/acquire`, docs/design/router.md "Triggers") for its **active versions only**:

    | Command | Searches |
    |---|---|
    | `MoviesSearch` | each movie, unmonitored too (Radarr skips unmonitored movies only for automatic triggers) |
    | `SeriesSearch` | each season with monitored episodes (no specials) |
    | `SeasonSearch` | the season |
    | `EpisodeSearch` | each episode, unmonitored too (Sonarr's `EpisodeSearch` has `MonitoredEpisodesOnly` false) |
    | `MissingMoviesSearch`, `MissingEpisodeSearch` | missing items, aired episodes only |
    | `CutOffUnmetMoviesSearch`, `CutoffUnmetEpisodeSearch` | items below the quality cutoff (the arrs' Cutoff Unmet list, format score aside, whatever `router.wanted.cutoff_unmet` says), aired episodes only |

    - A command that names titles or episodes (`movieIds`, `movieId`, `seriesId`, `seriesIds`, `episodeIds`)
      reaches only what they resolve to: when none do (`[]`, `0`, stale ids) nothing is searched, even for a
      Missing or Cutoff command, and a maintenance command fails (`TestSearchCommandUnresolvedIDsSearchNothing`).
    - A command over a whole view searches that view's active versions only; one naming titles also serves the
      other active versions of those titles where they are monitored, not removed and available; only the named
      version searches unmonitored or unavailable items (docs/design/router.md "The acquirer").
    - A command over a whole view starts at most 100 searches, the ones searched longest ago first.
    - A search that cannot be handed to the acquirer (shutdown, database error) is stored `failed`, so the same
      request sent again starts a new search instead of returning the stuck one (`TestSearchCommandSubmitFailure`).
    - The message says what started and names the shadow versions that were left out.
    - Each search with accepted releases becomes a `router.route` job.
  - **Command status** (`GET command`, `command/{id}`) is computed from the searches, the route jobs and the router's
    download (or dry-run plan) rows:
    - `queued`, then `started`;
    - `completed` with what happened ("added X on P", "no accepted release among N results", "a download for it is in
      flight already", "dry run (disabled): would add X on P");
    - `failed` when every search failed.

    The final status is stored on the command row once known. A command with the same name and body as one still
    `queued` or `started` answers that command; a repeat of a finished one is a new command (`TestFinishedSearchCommandDoesNotDedupeRepeat`).
  - `RssSync` on an active version completes at once: the manager reads RSS on its own schedule.
  - `RefreshMovie` / `RefreshSeries` queue the same per-title `meta.art` refresh as the native title refresh API;
    omitted ids mean every title in the view. Status follows all queued jobs, including metadata retries.
  - `RescanMovie` / `RescanSeries` and `DownloadedMoviesScan` / `DownloadedEpisodesScan` queue an import pass
    over ready/importing downloads of the selected titles and view versions. A supplied `path` restricts it to
    recorded mount folders at or below that directory. Theatarr imports its own downloads only; unmatched paths, missing
    downloads, waiting mounts, rejected files and library gate refusals fail with a message instead of claiming an import happened.
    The two Rescan commands first check the titles' links against the disk (`library.ReconcileLinks`: a deleted
    symlink stops counting and the title is wanted again; an unavailable or empty root is skipped; shadow versions
    only report), and succeed when there is nothing ready to import.
  - `RefreshMonitoredDownloads` queues one router download poll. Maintenance commands follow their jobs through
    `queued`, `started`, and terminal `completed` / `failed`; unavailable services fail explicitly.
  - `RenameMovie`, `RenameSeries` and `RenameFiles` apply the current version naming to theatarr-owned links. The command
    is terminal and successful when every selected title is applied; a closed library gate returns a terminal
    `failed` command with the gate's clear refusal and leaves links unchanged.
    The recorded Seerr contracts post download refresh before reading the queue; Homepage only reads data;
    LunaSea and Ruddarr issue manual commands. No automatic rename retry is recorded in those contracts, so the views keep the accepted HTTP response and
    terminal command shape.
  - The add option (`searchForMovie` / `searchForMissingEpisodes`) goes the same way as the command it records.
- **Rename preview.** `GET /api/v3/rename?movieId=` (Radarr) and `GET /api/v3/rename?seriesId=` (Sonarr, optionally
  `seasonNumber`) list only links whose computed `newPath` differs, with the arr file ids and existing paths. Inputs
  come from each link's saved import metadata; incomplete metadata is skipped. Applying a rename relinks the same
  target through the library gate, then removes the old link.
- **Per-version episode state.** Episode monitoring writes `episode_version_state` rows for the view's versions,
  creating `wanted` rows where none exist. An episode reads as monitored when such a row says so (and the version is
  monitored), else when the episode and the series are; it has a file when a view version's state is `filled` or
  `cutoff_met`, or an active link of a view version holds it.

Note: while a version is owned by `theatarr import-arr` (shadow mode), the 15-minute import sets its `monitored` from the
arr again. The removal marker stays, so the views keep hiding a removed version; other monitoring changes made through
a view last until the next import. At cutover the import stops owning the version.

## Tags per version

A tag on a title belongs to a version (`arrview_title_tag (version, title_id, tag_id)`, migration 00105), so a tag set
through one view does not show in a view of another version of the same title: a `debrid-upgrade` tag set
through `/v/movie-web/radarr` stays out of `/v/movie-remux/radarr`. A view reads a title's tags as the union of
its versions' tags, each tag once (movie and series resources, `tag/detail`, the `tags` filter, webhook payloads, the
tag delete's "in use" check). Writes (add, PUT, editor `add`/`remove`/`replace`) touch the view's versions the title
has and no other: `replace` replaces only within them. A version a view's DELETE removed is out of both: its tags
are not shown and writes leave them alone (they show again when the version is turned back on); a request that turns
versions on does so before its tags are written (`TestTagsSkipRemovedVersions`). Labels (`arrview_tag`) stay global, like an arr's tag list, and
deleting a label removes it from every version. Rows from before 00105 were copied to every version the title had
(`TestTagsPerVersion`, `TestVersionTagsMigration`).

## Tags from the arrs

Every `arr.import` copies the arr's tag labels of each title to the views' tags of the import's source version and,
when the instance owns the title, the followers the import gives it (`Source.Followers`), so each Radarr's behaviour
tags carry over to the versions that follow it. Seerr requester tags (`<user id>-<name>` or the older
`<user id> - <name>`) are stored in Seerr's current form, lower-case (`1 - alice` becomes `1-alice`); every other
label (behaviour tags such as `debrid-upgrade` or `skip-upgrade-check`, and hand-made labels) is copied as it is. An existing view tag with that label in any case keeps its id;
a tag id the arr does not list (`#<id>` in `arr_title.tags`) is skipped. The copy is edge-triggered and only adds:
`arr_title.tags_copied` holds the labels already copied, and a run copies only the arr's labels not in it, then sets
it to the arr's current labels. A version that gets the title in this run (a title new to the version, a follower
newly added to the policy) takes every current label once (`TestImportTagsToNewlyFollowingVersions`). So a tag a view took off (a client clearing `debrid-upgrade` after it upgraded)
is not put back while the arr keeps it, a label the arr adds later is copied once, and a run with the arr's tags
unchanged writes nothing. The first run after 00105 copies every label (`tags_copied` starts `[]`). The import never
removes a view tag (`TestImportCopiesTagsToVersions`, `TestImportTagsNotToFollowersOfOthersTitles`).

## Schema (00012)

| Table | Holds |
|---|---|
| `arrview_tag` | Tags: id, label (unique, case-insensitive) |
| `arrview_title_tag` | Tags on titles, per version (00105) |
| `arrview_profile` | Profile ids per view and name |
| `arrview_title` | Per view and title: profile, root folder, minimum availability (a library's choice on its `v/<key>` row; `updated_at` orders the choices), season folder, monitor new items, added (`series_type` is no longer read: the type is the title's); where the add's anime decision came from (`anime_source`, 00072, `unknown` 00103) |
| `arrview_removed_version` | Versions a view's DELETE removed |
| `arrview_command` | Commands: name, body as sent, status, result, message, user agent, times |

## Deviations from the arrs

- **Uncatalogued TVDB id lookups** fetch default-order seasons and series fields through the existing TVDB/TMDb
  metadata gateway clients, bounded to five seconds, cached five minutes (thirty seconds for failures; at most 64
  entries). Free text without a local match searches TVDB by name (`/search?query=…&type=series`), with the whole
  lookup bounded to five seconds; metadata failures keep the local answer (the TVDB id stub or an empty name search),
  never an HTTP error. Without a metadata source the local answer stays. Seerr's lookup → POST
  carries selected and unselected seasons, stored as durable monitoring intent that metadata refresh preserves. Other
  external-id lookups remain local.
- **Movie lookup by name** (`movie/lookup?term=` with free text, as Ruddarr, LunaSea and Seerr search): catalog titles
  whose name matches come first, then TMDb's title search (`/search/movie`, first page, cached a week, bounded to
  five seconds) for the movies they do not already answer, at most 20 results in all. A trailing year ("Akira 1988",
  "Akira (1988)", up to next year) narrows the search; when that finds nothing the whole term is searched ("Wonder
  Woman 1984"). A TMDb result whose id the catalog holds is answered as that title (with the view's id when the view has it);
  the others are un-added movies: no `id`, `tmdbId`, `title`, `originalTitle`, `year`, `overview`, `images`
  (`poster`, `fanart` with TMDb's original-size URLs as `url` and `remoteUrl`), `remotePoster`, `ratings.tmdb`,
  `popularity`, `folder` and `titleSlug` (the TMDb id). The app's add posts the `tmdbId` back (Writes). TMDb
  errors, or no `metadata.sources.tmdb` key, leave the catalog's answer, never an HTTP error
  (`TestMovieLookupByNameFromTMDb`).
- **Episodes the metadata source dropped** (`episode.removed_at`, kept for their files and history) are left out of
  `episode`, `episode/{id}`, calendar, wanted/missing, cutoff and the season statistics, as Sonarr deletes them on
  refresh. Queue, manual import and parse still name them, since a download or file already points at them.
- **Lookups set `hasFile` and `monitored` from the view** (Radarr leaves `hasFile` out of lookups), and set `id` only
  when the view has the title, so Seerr adds it otherwise and returns early when the view has a file.
- **Second add is a 201**, extra versions on.
- **DELETE keeps version rows and arr files**; `deleteFiles=true` moves only Theatarr's links to trash.
- **Ids are offset** by 1,000,000 (above).
- **Queue** and `queue/details` come from the router's downloads (docs/design/router.md). `timeleft` and
  `estimatedCompletionTime` use up to six provider progress samples from the last five minutes (migration 00036);
  flat, stale or reset progress leaves both omitted. Ready/importing downloads report zero time left and now.
- **Quality profiles are version sets**: every quality allowed, no formats, cutoff the best quality.
- **Metadata Theatarr does not hold** (overview, images, genres, ratings, runtime, certification) is written empty, not
  null, where the arr's type is a value or list.
- **History ids** are row ids for imported history, or `1,000,000,000 + download id × 10 + event code` for local events; event types are the arr's names. In the Sonarr view a local event has one record per episode of the download, as Sonarr writes them: the episode at position i of the download's episodes adds `i × 2^40` (the first keeps the plain id), so `episodeId` filters, paging and `history/failed/{id}` work for every episode of a pack.
- **Episode files**: `episodeFileId` is `1,000,000,000 + link id` when a Theatarr link holds the episode, else the
  arr's file id (`version_file`), else 0; `episodefile` is in [devices](devices.md).
- **Languages** map by name through Radarr's list for both flavours.
- **Movie files** use parsed quality to fill `movieFile.mediaInfo.resolution` when no probe value exists; a
  recorded value remains authoritative. See [the client compatibility matrix](clients.md) for the Seerr
  status-sync contract and the other clients' request tables.
- **Endpoints of the "Should" tier and the rest of "Must"** (queue removal, history/failed, release, release/push,
  parse, manualimport, files, editors, tag/detail, wanted/cutoff, calendar, notification, indexer, blocklist): see
  [devices](devices.md).

## Tests

`go test ./internal/arrview` (no network; requests go to the mux in memory):

- **Golden JSON** per endpoint in `testdata/golden/` (rewrite with `go test ./internal/arrview -update` and review the
  diff), and key-set checks against every property of the C# resources (`MovieResource`, `MovieFileResource`,
  `SeriesResource`, `EpisodeResource`, `SystemResource`, `QualityProfileResource`, `RootFolderResource`,
  `HistoryResource`, `CommandResource`) plus the fields each caller reads.
- **Auth**: header, query parameter, bearer, the view's own key (and not another view's), wrong and revoked keys, the
  bare 401, `X-Application-Version`, case-insensitive routes, Sonarr routes absent on Radarr views; and beside the
  web layer on one mux, where the native API still refuses `apikey`.
- **Alias table**: Theatarr's ids, the first `alias_from` instance's id winning over a later one with the same number, ids
  with no alias, per-version views without aliases.
- **Add and delete per view**: profile choice, a second add turning on the extra version, anime by root folder and
  profile, no-profile adds, per-version adds; DELETE from `/v/movie-web` leaving `/radarr`'s remux, DELETE by alias,
  lookups without an id afterwards, nothing deleted, re-add restoring the file; PUT; commands; Sonarr adds, episode
  monitoring and season PUTs.
- **Lookup, blocklist, anime**: `TestMovieLookupByNameFromTMDb` (fake TMDb on httptest: catalog first, TMDb's
  un-added movies, trailing year, TMDb down, lookup → add by `tmdbId`), `TestBlocklistList`, `TestBlocklistDelete`,
  `TestAddDecidesAnime` (mapping, TMDb, client wins, root wins, held titles, TMDb down, Sonarr).
- **Contract tests** replaying the callers' request sequences from their source: Seerr's settings test, user tag,
  `addMovie` (new, has file, exists unmonitored → PUT, exists monitored), download tracker, scanner, availability by
  stored id and by alias, `removeMovie` then re-request; Seerr's `addSeries` (new anime series with the anime
  settings; existing series → PUT, episode list, `episode/monitor`, `MissingEpisodeSearch`), scanner and remove;
  Homepage's Radarr and Sonarr widgets; decypharr's status, download client config, commands, movie by TMDb id,
  series by TVDB id and episodes.
- **Uncatalogued Seerr/Overseerr adds** with a fake TVDB client: lookup seasons build the exact POST shape; stored
  season choices survive two metadata refreshes and new episodes. Queue estimates cover measured, completed and
  unknown cases for both flavours; metadata cache, timeout and TMDb fallback tests live in `metadata/tvrefresh`.

## Cutover

Per area, after the version is switched to `mode: active` ([migration.md](../migration.md)). Before: make one key per
client in Settings › Connected apps (or set `THEATARR_VIEW_RADARR_KEY` and `THEATARR_VIEW_SONARR_KEY`), and back up
Seerr's settings.

- **Seerr** (Settings → Services): one Radarr server and one Sonarr server, both default, no 4K servers (one view
  each) and no anime override rules: Theatarr decides anime itself (Writes).
  - Radarr: Theatarr's host, port `7700`, URL base `/radarr`, the key. Test, then pick the quality profile
    "Rules default", root folder `/media/movies` and minimum availability; re-select tags (the views'
    tag ids are new). An anime movie (mapping data, else TMDb's Japanese animation) lands in `anime-movie`.
  - Sonarr: host and port the same, URL base `/sonarr`; profile "Rules default", root
    `/media/tv`, series type `standard`, anime series type `anime`; leave the anime profile and
    anime root empty. Seerr then sends `anime` for TMDb's anime keyword, which wins; otherwise Theatarr decides. Either way
    an anime series lands in `anime-tv`.
  - Edit the existing default server entries in place (that keeps their Seerr ids, which media items store), turn
    **Enable Scan** on, and remove the old anime override rules and any second (anime or 4K) server.
  - Then fix Seerr's stored ids (below): run **Radarr Scan** and **Sonarr Scan** (Settings › Jobs & Cache › Run now)
    right after repointing, and check with `theatarr migrate seerr-ids`.
  Alias resolution uses only the requesting view's `alias_from`, in order, so overlapping ids from different arr
  instances can name different titles in different views without a migration.
- **Homepage**: one radarr widget at `http://theatarr:7700/radarr` and one sonarr widget at
  `http://theatarr:7700/sonarr`, each with a key, in place of the per-instance widgets.
- **decypharr** logs in with the callback view's URL and key (`clients.decypharr.callback_view`).
- Rollback: restore Seerr's settings backup and the Homepage config.

### Seerr's stored ids

Seerr keeps, per media item, the server (`serviceId`, `serviceId4k`) and that arr's id of the title
(`externalServiceId`, `externalServiceId4k`; `server/entity/Media.ts`). After the switch those ids reach the views,
which answer an id below `IDBase` through `alias_from`. An id from an arr that is not in the list can name an unrelated title: one
Sonarr's series 35 may be another Sonarr's series 35, a different show, in `/sonarr`. What Seerr does with a stored id (source `seerr-team/seerr` at c167958):

| Where | Uses the stored id? | After the switch |
|---|---|---|
| Request (`MediaRequestSubscriber` → `addMovie`/`addSeries`) | No: looks the title up by TMDb/TVDB id, then stores the id the view returns | Safe |
| Delete (`routes/media.ts` DELETE `/:id/file` → `removeMovie`/`removeSeries`) | No: by TMDb/TVDB id; `serviceId` only picks the server | Safe while the server entry exists; one naming a removed entry answers 409 until a scan rewrites it |
| Radarr/Sonarr scan (`scanners/radarr`, `scanners/sonarr`, `baseScanner.processMovie`/`processShow`) | No: walks `GET movie`/`GET series` and matches by TMDb (movies) or TVDB (series), then **rewrites** `serviceId`, `externalServiceId` and the slug of every title the view lists | Fixes the ids, Theatarr's (`IDBase` + title) from then on. Exception: an unmonitored movie without a file whose request was processing keeps its old id when "decline requests on status reset" is on |
| Availability sync (`availabilitySync.mediaExistsInRadarr`/`InSonarr`) | Yes: `GET movie/{id}` / `series/{id}` on every server of the item's kind and 4K class | Seerr skips an answer whose TMDb id (movies) or TVDB id (series) differs, and a 404 means "not in the arr", so Plex decides. Only a series with no TVDB id in Seerr takes a wrong title's files as its own, which can only keep it marked available |
| Download tracker (`downloadtracker.ts`) | Filters the fetched queue by `movieId`/`seriesId` locally | An old id never matches Theatarr's: no progress shown until the scan |
| "Open in Radarr/Sonarr" (`Media.setServiceUrl`) | No: the slug | The view's slug after the scan |

So Seerr uses no wrong-title id for anything that changes state, and its own scan replaces the ids. The fix is the
cutover step above, not a change to `resolve`: run both scans right after repointing (by default they run at 04:00
and 04:30, before the 05:00 availability sync), then `theatarr migrate seerr-ids` to confirm. Titles the views do not
hold keep their old ids and stay unresolved (404), which is harmless.

`theatarr migrate seerr-ids -seerr URL,KEYNAME` ([migration.md](../migration.md#seerr-ids)) reads Seerr's servers and
media with GET only and asks the view of each id's flavour (`-radarr-view`, default `/radarr`; `-sonarr-view`,
default `/sonarr`) through the running service, exactly as the availability sync asks it: `GET movie/{id}` or
`series/{id}`. Both the plain and the 4K id are checked: availability sync sends an id to every server of its class
whatever `serviceId` says. Each id is **ok** (the view's title has the item's TMDb id, or TVDB id for a series),
**wrong title**, **unresolved** (404) or **unverifiable** (a series Seerr has no TVDB id for, answered without a TMDb
id); ids whose server entry is gone are counted. It exits 1 on a wrong title. Before the switch it lists what the
scans will fix; after them it should list no wrong title.

## Manual import and queue holds

`POST manualimport` recalculates the decision and custom formats using the chosen title/episodes, quality and full
revision, languages, release group and indexer flags. As in the arrs' ManualImportService, Unknown quality/language
and a blank group fall back to parsing; final `ManualImport` accepts explicit choices without rerunning upgrade
specifications, while Theatarr's sample guard and library gate still apply (the mislabel guard only notes a manual pick). Per-file choices survive retries in
`download_file.overrides` (00066), name the link and populate its saved metadata and subsequent arr file responses.
All import modes (`auto`, `move`, `copy`, `hardlink`, `link`) create symlinks under version roots and leave the mount
source untouched; each link action records the client's requested mode and the substitution.

Queue and queue/details use these closest Sonarr/Radarr tracked-download states; queue/status aggregates their
warning/error flags. Calendar windows select title/episode identities in SQL before loading their resources; queue
ETA samples are read once per batch rather than per download. Each waiting or held item has a status message with its reason. Finished ordinary failures
and imported downloads leave the queue; library failures and stalled providers follow the expiry rules below.
Pending upgrade approvals create no download and are resolved through the existing upgrade approval API. Their
queue IDs are positive hashed IDs (as Sonarr's per-episode IDs are), kept apart from download IDs, so
`DELETE queue/{id}` removes them (declining the hold) in both views.

Failed stalled downloads and failed library imports leave the queue when a later download of the same title
and version imports, or seven days after the failure. Dismissing a failure in Activity removes it from both views
while keeping the failure, blocklist and provider ownership evidence in history. Activity lists undismissed failed
downloads, including ones no longer in the arr queue.

| Theatarr condition | trackedDownloadStatus | trackedDownloadState | Message |
|---|---|---|---|
| Queued/adding/added, waiting for provider | `ok` | `downloading` | Waiting for the provider |
| Deferred provider attempt/account pacing | `warning` | `downloading` | Router's recorded reason/retry time |
| Added past `router.stalled_after` | `warning` | `downloading` | Provider stalled, awaiting readiness |
| Provider failed as stalled | `error` | `failedPending` | Stall reason |
| Ready, import not started | `ok` | `importPending` | Waiting for library import |
| Release missing on mount before `library.mount.wait_timeout` | `ok` | `importPending` | Release not on the mount yet |
| Release still missing after `wait_timeout` | `warning` | `importPending` | Same mount wait reason; no re-grab |
| Episode title hold | `warning` | `importBlocked` | `episode title hold: …` |
| Held for review/no video/selected files absent | `warning` | `importBlocked` | Library's review reason |
| zurg unconfirmed file | `warning` | `importBlocked` | Reason starting `zurg` |
| Library gate dry run | `warning` | `importBlocked` | Gate refusal |
| Upgrade awaiting approval | `warning` | `importBlocked` | Upgrade awaiting approval and its reason |
| Not an upgrade | `warning` | `importBlocked` | Not an upgrade for the existing files |
| Sample or unmatched episode rejection | `warning` | `importBlocked` | Sample guard or episode matching reason |
| Import in progress | `ok` | `importing` | Importing library links |
| Partial import waiting for retry | `warning` | `importPending` | Link failure and retry reason |
| Library link failure | `error` | `failedPending` | Library's failure reason |
