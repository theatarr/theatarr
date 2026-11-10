# Devices and integrations (`internal/arrview`, `internal/notify`, native decide and import)

Everything that talks to Radarr or Sonarr works against Theatarr's arr views. Seerr and Homepage are covered in
[arrview.md](arrview.md); this doc covers the phone apps (Ruddarr, LunaSea), autobrr, decypharr's repair flows, push
clients, and outbound webhooks. Callers and the fields they read: [clients.md](clients.md).

Code:
- `internal/arrview` (endpoints);
- `internal/arrview/nativeapi` (request types shared with web);
- `internal/notify`;
- `internal/web/devices.go`;
- `internal/policy/notify.go`;
- migration `00020_devices.sql`;
- `cmd/theatarr/serve.go` wiring.

Hooks in other packages:

| Package | Addition |
|---|---|
| `internal/router` | `Dismiss` (end a download in Theatarr's database, optional blocklist); `Searched.Releases` (every decision's release) |
| `internal/acquire` | `Interactive` and `Decide` (search or decide one target for any version, recording nothing); the acquirer keeps the engine |
| `internal/library` | `Unlink` (move one link to the trash through the writer and the gate) |
| `internal/gateway` | Class `webhook`: one target per `notify.targets` key plus `installed` |

`go test` uses fakes, and `TestNoOtherOutboundClients` passes: webhooks go through the gateway.

## Gates

| Write | Path | Gate |
|---|---|---|
| Grab (`POST release`), push (`release/push`) | `router.Enqueue` per version | Router: `router.enabled` + `mode: active`. A shadow version gets a dry-run plan in `router_plan`, never an add (`TestReleaseSearchAndGrab`, `TestReleasePush`) |
| Queue removal, mark failed | `router.Dismiss`: download `failed` in Theatarr's database, optional `router_blocklist` row | DB only. Nothing is removed on a provider, whatever `removeFromClient` says |
| File delete (`DELETE moviefile/episodefile`, bulk) | `library.Unlink`: the symlink is copied to the trash, then removed | Library gate (`library.enabled` + active + roots). While it refuses, the answer is **409** with the reason and nothing changes, so decypharr doesn't continue as if the file were gone |
| `ManualImport` command, `POST /api/theatarr/v1/import` | `library.Import` | Library gate. When gated, the import API plans on a temporary download row and removes it again (dry run) |
| Notifications, editors, tags | arrview tables | DB only |
| Webhooks | `notify` → gateway | `notify.enabled` (default false) plus the allowlist (`notify.allow_hosts`, target hosts) |

## Endpoints (all under `<view>/api/v3`)

| Endpoint | What it answers |
|---|---|
| `DELETE queue/{id}`, `DELETE queue/bulk {ids}` | Ends Theatarr's download (`failed`, "removed from the queue by <client>"). `blocklist=true` (or LunaSea's old `blacklist`) blocklists the hash for the version; without `skipRedownload` it then starts a search (active versions only). 200 (bulk `{}`) |
| `POST history/failed/{id}` | For one of Theatarr's grabs: ends the download and blocklists the hash. For an imported arr record: blocklists its hash for its version. Then it searches again (`autoRedownloadFailed` is on). 200 `{}` |
| `GET history`, `history/movie`, `history/series` | Imported arr history **plus Theatarr's downloads**: `grabbed` (provider took it), `downloadFolderImported`, `downloadFailed` (failed after the grab, **or before any provider took it**: see below). Ids are `1,000,000,000 + download×10 + event`. `grabbed`'s `data` (strings only) has `indexer`, `size`, `guid` (`theatarr:<indexer>:<hash>`), `releaseSource`, `downloadClient`, `torrentInfoHash`. New filter: `episodeId` |
| `GET queue`, `queue/details` | `includeMovie` / `includeSeries` embed the title |
| `GET release?movieId=` / `?episodeId=` / `?seriesId=&seasonNumber=` | Interactive search of each of the view's versions of the title (each with its own indexers), decided. One row per release: `approved` when any version accepts it; `rejections` are strings, prefixed with the version key when the view has several. `cachedOn` (Theatarr extension, below). `seriesId` alone, or no id, returns `[]` (the arrs' RSS cache) |
| `POST release {guid, indexerId}` | Grabs from the last 30 minutes' searches of this view (cache key view, title and `indexerId_guid`; the arrs key `indexerId_guid` only). A body that names its title (`movieId`/`seriesId`, or `mappedMovieId`/`mappedSeriesId` in an echoed resource) grabs that title's entry; without one, the title this view searched last. Routed to the versions that accepted it, else to all decided versions (the arrs allow a forced grab; the router still refuses hash-less releases). Unknown guid: 404 "Couldn't find requested release in cache" |
| `POST release/push` | Validated as the arrs (title; downloadUrl, magnetUrl or infoHash; protocol; publishDate): 400 array, `attemptedValue` as a string. Mapped by tmdbId/tvdbId/imdbId (number or `tt` string), else by parsed name among the view's titles. Decided for each version; accepted versions are routed. **Indexer:** the API key is the gate; the body's `indexer` is matched to a configured indexer by name (`Policy.IndexerByName`, not the positional `indexerId`), and one that matches none or that the version does not list is rejected (`IndexerNotInVersion`, `TestReleasePushIndexerMatchedByName`). Returns `[ReleaseResource]` with `mappedMovieId` / `mappedSeriesId` |
| `GET parse?title=` (Sonarr also `path=`) | `parsedMovieInfo` / `parsedEpisodeInfo` from Theatarr's parser ports. When the name maps to a view title: `movie` or `series` + `episodes`, `languages`, and `customFormats` / `customFormatScore` from Theatarr's decision for the title's first view version. Blank: 204 |
| `GET manualimport` | `downloadId`: the library's dry-run plan of Theatarr's download (files, guard rejections, unmatched files). `folder`: video files under it (read-only, 2 levels), each mapped by file or folder name and decided. Rejections are `{reason, type}` |
| `POST manualimport` | Reprocess: decides each item for the given `movieId` / `seriesId` + `episodeIds` |
| `POST command {ManualImport}` | Imports files whose `downloadId` is one of Theatarr's downloads, through the library. Others are reported, not imported (see deviations) |
| `GET moviefile?movieId=&movieFileIds=`, `moviefile/{id}`; `GET episodefile?seriesId=&episodeFileIds=`, `episodefile/{id}` | The arr's files (`version_file`, ids as imported) and Theatarr's links (`1,000,000,000 + link id`) |
| `DELETE moviefile/{id}`, `episodefile/{id}`, `…/bulk` | Unlink, see Gates. Applied: Theatarr's link row `unlinked`, the arr file row dropped, the version (or the file's episodes) `wanted` again, a FileDelete webhook. An id that does not parse (`0`, `-1`, `undefined`) or names no file is a 404 ("File not found"); bulk with no ids is a 400 and with ids that name no file a 404: nothing is unlinked (`TestDeleteFileBadIDUnlinksNothing`). Every selected file, for every version of the view that holds it (a link shared by versions is a row per version), passes the gate check before the first unlink: one refused row is a 409 and nothing is unlinked (`TestBulkDeleteFilesPreflightsEveryRow`, `TestDeleteSharedLinkChecksEveryVersion`) |
| `PUT movie/editor`, `series/editor` | `monitored`, `qualityProfileId` (turns its versions on), `minimumAvailability`, `rootFolderPath`, `seriesType`, `seasonFolder`, `monitorNewItems`, `tags` with `applyTags` add/remove/replace. Files never move. 202 with the titles. Ids that resolve to no title of the view (`[]`, `0`, stale arr ids) change nothing: 202 `[]` (`TestEditorUnresolvedIDsEditNothing`) |
| `GET tag/detail`, `tag/detail/{id}` | Per tag, the view's `movieIds` / `seriesIds` and `notificationIds`; other lists empty |
| `DELETE tag/{id}` | `TagService.Delete`: 409 "Tag with ID N 'label' cannot be deleted since it's still in use" while a title any view shows, or any view's notification, carries it (tags are global); else 200, no body. An assignment on a title no view shows (versions removed) goes with the tag. Unknown: 404 |
| `GET delayprofile[/1]`, `GET releaseprofile[/{id}]` | Not supported (Theatarr has no delay or release profiles). One delay profile describing Theatarr (torrent only, no delay, no tags); no release profiles (`[]`, `{id}` 404). POST, PUT, DELETE on either: 405, `Allow: GET`, with the reason |
| `GET blocklist`, `blocklist/movie?movieId=` (Radarr), `DELETE blocklist/{id}`, `DELETE blocklist/bulk {ids}` | `router_blocklist` of the view's versions only, as `PagingResource<BlocklistResource>` (default `page` 1, `pageSize` 10, `date` descending; `sortKey` also `indexer`, `sourceTitle`, `movieMetadata.sortTitle` / `series.sortTitle`; filters `movieIds` / `seriesIds`, `protocols`). `id` is the row's SQLite rowid, as the journal names entries (`bl:<rowid>`). Radarr: `movieId`, `movie`; Sonarr: `seriesId`, `episodeIds`, `series`. Quality, indexer, formats and episodes come from Theatarr's download when the entry has one, else quality and languages are parsed from the name; `message` is the reason; `protocol` `torrent`. Release names are shown as in history and queue (the views take admin keys only). Removal is the web UI's unblock (`router.Unblock` and an `unblock` journal action by `key:<API key name>`): Theatarr's database only, the download stays as it was, no provider is called. A single id the view does not hold is 404; bulk skips such ids and answers `{}` (`TestBlocklistList`, `TestBlocklistDelete`) |
| `GET wanted/cutoff` | Titles (Radarr) or episodes (Sonarr) whose file in a view version is below the **quality** cutoff, as the arrs list them (Radarr `MovieCutoffService`, Sonarr `EpisodeCutoffService`): `filled` and of a quality the profile ranks below the cutoff. A file at the cutoff quality but below `cutoff_score` is not listed. A prefer-SeaDex version's `cutoff_met` file that a better curated alternative makes wanted is listed too, as the cutoff search selects it. `TestWantedCutoffQualityOnly`, `TestCutoffPageListsCuratorWanted` |
| `GET calendar` | Radarr: movies with a cinema, digital or physical date in `[start, end]`. Sonarr: episodes by `airDateUtc`, with `includeSeries`, `includeEpisodeFile`, `unmonitored`, `tags` |
| `GET notification[/{id}]`, `POST`, `PUT {id}`, `DELETE {id}`, `GET schema`, `POST test` | Webhook only (see Webhooks) |
| `GET health` | `ShadowModeCheck` (notice) while a view version is in shadow mode; `RouterDisabledCheck` (warning) while `router.enabled` is off (or the policy has no `router`) and a view version is active; then the service's failing health checks, worst first (`MountCheck`, `BackupCheck`, ...: [readiness.md](readiness.md)). `wikiUrl` is always written, `""` (see below) |
| `GET indexer[/{id}]` | The policy indexers the view's versions search, read-only. Ids are the sorted keys' positions (1-based); no URL or key is written |
| `GET episode?includeEpisodeFile=true` | Embeds `episodeFile` |
| `GET extrafile`, `credit`, `exclusions`, `importlistexclusion`, `importlist/movie`, `filesystem`, `language`, `qualitydefinition`, `languageprofile` (Sonarr) | Empty or static answers in the arrs' shape, so the phone apps' screens load |

Not served: `customformat` / `qualityprofile` writes and `indexer` CRUD (used by recyclarr and Prowlarr sync),
SignalR, Sonarr v5, `downloadclient`.

## View extensions

- **Failed routes in history.** A download the router fails before any provider takes it (no `added_at`) is a
  `downloadFailed` record, as one that failed after the grab is. No `grabbed` record is written for it.
  - Reasons are the router's `download.error`: "no provider of the version has an API key set", "miss on every
    provider", or a queue removal's "removed from the queue by <client>".
  - Found by `history/movie?movieId=&eventType=4`, `history/series?seriesId=&eventType=4`,
    `history?episodeId=&eventType=4`, `history?downloadId=<hash>`; `eventType` also takes `downloadFailed`.
  - Shape, after the arrs' `HistoryService.Handle(DownloadFailedEvent)` (Radarr @ a96bf7c L301-322, Sonarr @ cab419a
    L233-257); the date is the failure's (`updated_at`):

    ```json
    {"id": 1000000034, "movieId": 1000001, "sourceTitle": "Alpha.2020.2160p.WEB-DL.H265-GRP",
     "languages": [], "quality": {…}, "customFormats": [], "customFormatScore": 50, "qualityCutoffNotMet": false,
     "date": "2026-10-26T12:00:00Z", "downloadId": "3333…", "eventType": "downloadFailed",
     "data": {"downloadClient": "theatarr router", "downloadClientName": "theatarr router",
              "message": "no provider of the version has an API key set", "releaseGroup": "GRP",
              "size": "5368709120", "indexer": "prowlarr-public"}}
    ```

    Sonarr's record has `seriesId` and `episodeId` (the download's first episode) instead of `movieId`, and no
    `indexer` in `data` (Sonarr's handler writes none).
  - A client polling after `release/push`: `eventType=1` with the hash means a provider took it; `eventType=4` with
    the hash means it failed, `data.message` says why.
- **Router disabled in health.** `GET health` adds
  `{"source": "RouterDisabledCheck", "type": "warning", "message": "Router disabled: grabs are dry runs: movie-web", "wikiUrl": ""}`
  while `router.enabled` is false and some of the view's versions are `mode: active` (they are listed in the view's
  order). `ShadowModeCheck` comes first. The Health webhook reports the same items over the configured views'
  versions. `wikiUrl` is always written (`""`), as the arrs write it (a string from `HttpUri.FullUri`).
- **`cachedOn` on releases (Theatarr extension).** `ReleaseResource` has `cachedOn: ["rd", "ad", "tb"]`: the providers
  cache evidence says hold the release cached, in that order. It is every piece of evidence
  (docs/design/router.md "Cache evidence"), for **every** release of the search: the release's own `cached_on` (DMM's
  feed: `rd,ad` for `/torznab/cached`, one of them for `/torznab/<rd|ad|tb>/cached`, each item's own `cachedon` on a
  path naming several); the same info hash in a DMM feed of the same search (a Torrentio or Prowlarr release DMM lists
  on `/ad/cached` is `["ad"]`); stored evidence from earlier DMM results and TorBox checks (`provider_cache`, live for
  `router.cache_evidence.ttl`); and TorBox's own `checkcached` for the best accepted releases. Left out when there is
  no evidence, which never means uncached (`release/push` answers carry only the pushed release's own). Not in the
  arrs. Callers that decode releases skip unknown keys: decypharr (sonic, default
  config; it sends the element back verbatim and `POST release` ignores the field), autobrr (encoding/json,
  push responses only), Ruddarr (Swift `Decodable`) and LunaSea (dynamic JSON).

## Native API (`/api/theatarr/v1`, OpenAPI updated)

- **`POST /decide`**.
  - Body: `{title_id, version, release, indexer?, info_hash?, size?, seeders?, languages?, flags?}`.
  - Returns the engine's `Decision`: accepted, rejections, quality, its `profile_rank` of `profile_ranks`, formats,
    score, episodes, the existing file (with its rank) and upgrade. Nothing is recorded.
  - An empty indexer, size or seeders means unknown.
  - Settings › Profiles › **Check a release** is its admin screen ([decide.md](decide.md#release-check)).
- **`POST /import`**.
  - Body: `{title_id, version, info_hash? | path?, release?, provider?, episodes?, dry_run?}`.
  - Opens a `ready` download (source `api:import`) and runs the library import on it, exactly as for a routed
    download: find on the mount by hash and names, probe, guard, name, link.
  - Gated or `dry_run`: the plan comes back with `applied: false` and `refused`, and the temporary row is removed.
- Both take a session or an API key; 503 when not wired.

## Webhooks (`internal/notify`, migration 00020)

- **Installed notifications** (`arrview_notification`).
  - `POST notification` stores the resource per view. Only `Webhook` is accepted.
  - Credentials are written back as `********` in list, detail and write answers: the password, and (beyond the
    arrs, which return these as stored) the URL's user information, token-like path segments, query values under a
    credential-like key or that look like a token, and header values under a credential-like name (Authorization,
    Cookie, X-Api-Key, ...) or that look like a token. A masked value sent back keeps the stored one, so a client
    can GET, change a flag and PUT. The stored values are put back only while the URL keeps its scheme and host;
    a URL pointed elsewhere keeps the masks as sent.
  - `supportsOn*` says what Theatarr sends. Radarr: Grab, Download, Upgrade, Rename, MovieAdded, MovieDelete,
    MovieFileDelete, HealthIssue, HealthRestored and ManualInteractionRequired. Sonarr: the series equivalents
    (`SeriesAdd`, `SeriesDelete`, `EpisodeFileDelete`). The matching `on*` switches round-trip on saved definitions.
  - A resource that would not be sent carries `message` (warning), with the reason.
  - As in the arrs, POST and PUT test first, unless `forceSave`. Here that happens only when it would be sent at all,
    so a policy with webhooks off never blocks Ruddarr's install.
- **Events.** Each event becomes one arr payload per view that holds the version.

  | Event | Source |
  |---|---|
  | Grab | A provider took one of Theatarr's downloads (`added_at`) |
  | Download (`isUpgrade`) | The library imported it; an upgrade when it moved an older file aside |
  | MovieDelete / SeriesDelete | View `DELETE movie|series` |
  | MovieFileDelete / EpisodeFileDelete | File unlink |
  | Health / HealthRestored | One changed check, with `level`, `message`, `type`, `wikiUrl`; recovery carries the previous issue, even while other checks fail |
  | SeriesAdd / MovieAdded | New title/version from arr POST, native add, list/collection intake or follower import; `series` or `movie` (`addMethod`: manual/list/collection for movies), once per title/version |
  | Rename | The common library writer applies a move (preview alone sends nothing); `series`/`movie` and `renamedEpisodeFiles`/`renamedMovieFiles` |
  | ManualInteractionRequired | Import held for review (`importBlocked`), unconfirmed zurg files or unreviewed episode mapping; title/episodes, `downloadInfo` (quality, release title, indexer, size), `release` (releaseTitle, indexer, size), `downloadStatus` and safe `downloadStatusMessages`, once per download/reason |
  | Test | The `test` endpoint |

  - Pipeline events are found by polling the database every `notify.poll` (15 s); applied renames have a durable
    record (migration 00065). Downloads and title adds from before the notifier's first run are never announced.
  - Health changes compare each check's source and level, ignoring moving counts/ages in messages. Installed
    targets require `onHealthIssue` or `onHealthRestored`; warnings also require `includeHealthWarnings`, including
    restoration of a warning. Notices do not go to installed health notifications, as in the arrs.
  - Each event is emitted once (`notify_event`). The claim and the event's deliveries are written together, so an
    event whose preparation fails (a payload or title read) is tried again at the next scan, not lost.
- **Targets.**
  - Installed notifications of the payload's view with the matching `on*` flag.
  - Policy event names: `grab`, `download`, `upgrade`, `rename`, `delete` (also file deletes), `health`,
    `health_restored`, `series_add`, `movie_added`, `manual_interaction_required`, `test`; empty means all events.
  - `notify.targets[k]`: the views it lists, filtered by `events`. With `native: true` it also gets Theatarr's own
    `theatarr.<kind>` payload (version, title, download, episodes, file, reason, health).
- **Sending.**
  - `Emit` only inserts `notify_delivery` rows. `Run` posts the due ones through the gateway: `webhook:installed`,
    or `webhook:<key>`.
  - Installed notifications: basic auth from username and password, their headers, POST or PUT.
  - Policy targets: `Authorization` from `auth_header_env`.
  - Failures retry after 30 s, doubling, at most 1 h, `notify.retries` times (default 5). A gateway `RetryAt` wins.
    Then the delivery is `failed`.
  - `dropped`: webhooks disabled, the host not allowed, or the target gone. It
    never blocks the pipeline.
  - Webhook URLs can carry a token in the path or query, so a target is logged and stored
    (`notify_delivery.last_error`, gateway and notify logs) as scheme and host only (`gateway.TargetURL`;
    `notify.TestWebhookPathTokenInLogURL`).
  - Payloads and the Health webhook read the views through the current settings runtime, so a version saved after
    startup shows up without a restart (`TestNotifyViewsFollowSettingsRuntime`).
  - Health webhook text is the check's `message`, which names no indexer keys and no provider error text (that can
    hold torrent ids): `IndexerStatusCheck` says "2 of 3 indexer(s) unavailable …" and `UnsettledAddsCheck` gives counts
    and age; which indexers and the add's error stay in the check's `detail` for the API and Health page
    (`health.TestHealthWebhookTextNamesNoKeysOrTorrents`).
- **Release details.** Webhook events always carry the release name and indexer. Rename arrays keep file IDs and
  quality without paths, and manual hold messages use safe categories rather than raw errors.
  - Tests: `arrview.TestWebhookReleaseDetails`, `notify.TestOldPrivateDeliverySent` (a delivery queued by an older
    release is sent).
- **Policy** (`examples/policy/notify.yaml`): `notify.enabled` (default false), `allow_hosts`, `budget`, `retries`,
  `poll`, `digest_at`, `targets{url, format, views, native, events, versions, auth_header_env}`. `installed` is a reserved key. Hosts join
  the gateway allowlist.

- **Discord** (`format: discord`). A target with `format: discord` gets Discord webhook messages
  (`internal/notify/discord.go`) instead of the theatarr.* JSON, through the same outbox, retries and 429 handling; it takes
  no `views`. The URL is a write-only credential like any target URL.
  - Messages: a health check going to error ("Health check failing: <check>") and leaving it, once: back to OK
    ("Health check OK again") or eased to a warning ("Health check no longer failing, now warning: <check>", amber,
    with the new and the old message; the warning clearing after that sends nothing). Also new error-inbox groups,
    the daily digest, request ready and the test. Other warnings and notices only appear in the digest.
  - Limits: content 2000 characters, 10 embeds, 6000 characters of embed text (a description 4096); lines that do not
    fit become "and N more". `allowed_mentions.parse` is always empty, and `users` names only the requesters a
    request-ready message mentions.
  - Errors (`scanErrors`): an alert when the inbox has a new fingerprint, or one that happened again after it was
    resolved (claimed in `notify_event` as `error:<regressions>`). Groups that existed when this first ran are the
    baseline. At most 3 error alerts per hour; the rest are held (`error_held:<regressions>`) and counted in the digest.
  - Daily digest: job `notify.digest`, queued once `notify.digest_at` (local `HH:MM`, default 09:00) has passed. It
    covers the 24 hours before: grabs and imports (counts and up to 10 titles), failed downloads, blocklisted releases,
    dead links (found: the relink sweep's applied `dead` actions in the window, so one relinked or replaced since
    still counts; "N dead links found, M still dead"), open error groups, health checks not OK, debrid accounts expiring (`ProviderAccountCheck`) and
    "grabbing is off". A quiet day is one line. The claim (`notify_event` digest/<yyyymmdd>) commits with the
    deliveries, so a day is sent once even across a restart.
  - Request ready: "**Title (year)** is ready in Plex, requested by Ann (@Ann)" (partly ready for a partial series),
    mentioning each requester with a Discord user id (`web_user.discord_id`, docs/design/users.md).
  - Policy event names for filters: `health`, `health_restored`, `error`, `digest`, `request_ready`, `test`.
  - theatarr.* targets get no error or digest events (Discord and ntfy targets do).
  - Send test: `POST /api/theatarr/v1/settings/notify/targets/{name}/test` (admin) sends a test to the saved target now, through
    the gateway, and answers `{ok}` or `{ok:false, error}`; it needs `notify.enabled`.
  - Tests: `notify.TestDiscord*`, `notify.TestDigest*`, `web.TestRequestReadyDiscord`, `web.TestNotifyTargetTest`.

- **ntfy** (`format: ntfy`). A target with `format: ntfy` gets the same messages as a Discord target, for the same
  events (health check failing and leaving error, new errors, the daily digest, request ready, the test) and with the
  same `events` and `versions` filters, as plain text (`internal/notify/ntfy.go`; the wording is shared in
  `message.go`). It takes no `views`, and uses the same outbox, retries, gateway class and allowlist as every target.
  - URL: the ntfy server address followed by the topic, `https://<server>/<topic>` (a server under a sub-path:
    `https://<server>/<path>/<topic>`). Validation refuses an ntfy target whose URL has no topic, or a topic that is
    not 1 to 64 letters, digits, `-` or `_`. Like any target URL
    it is write-only and shown as scheme and host.
  - Request: `POST` to the URL without its last path segment, with a JSON body `{"topic", "title", "message",
    "priority", "tags"}`. The topic is split from the URL when a delivery is sent, so the stored delivery
    (`notify_delivery.payload`, event `ntfy.<kind>`) holds no topic, and a changed URL applies to queued deliveries.
    A delivery queued under another format (its event is not `ntfy.*`) is dropped when the target is now ntfy, and
    an `ntfy.*` delivery is dropped when the target no longer is.
  - Access token: the target's Authorization secret (`auth_header`, or `auth_header_env`). A value with a scheme
    (`Bearer tk_...`, `Basic ...`) is sent as it is; a bare token is sent as `Bearer <token>`. No secret: no header.
  - Priority and tags: priority 4 (high) and tag `warning` for a failing health check and an error; priority 3
    (default) otherwise, with tag `warning` for a check eased to a warning or a digest with problems, and
    `white_check_mark` for a check OK again and request ready.
  - Text: no markdown and no Discord mention; request ready names the requesters ("Alpha (2020) is ready in Plex,
    requested by Ann and bob"). The title is kept to 250 bytes and the message to 3800 bytes; lines that do not fit
    become "and N more".
  - **Not tested against an ntfy server.** The request follows ntfy's JSON publishing as remembered from its
    documentation; nothing was checked against the documentation or a running server. Assumed: a JSON `POST` to the
    server root with a `topic` field publishes to that topic; `title`, `message`, `priority` (1 to 5, 3 the default)
    and `tags` (emoji short codes) are the field names; a message up to 4096 bytes is accepted as text; an access
    token is accepted as `Authorization: Bearer <token>`. Use **Send test** after saving to check a target.
  - Tests: `notify.TestNtfy*`, `policy.TestNotifyFormats`.

## Callers

| Caller | Endpoints | Covered | Tested (contract test, source cited) | Deviations |
|---|---|---|---|---|
| Ruddarr (Radarr) | status, rootfolder, qualityprofile, diskspace, tag, movie (list, id, lookup, POST, DELETE), movie/editor, moviefile (list, DELETE), extrafile, release (GET, POST), history/movie, history, calendar, queue (+includes), DELETE queue/{id}, manualimport, command (RefreshMovie, MoviesSearch, RefreshMonitoredDownloads, ManualImport), notification CRUD | all | `TestRuddarrRadarrContract`, `TestNotificationsRuddarr` (API+Live.swift L11-341, InstanceWebhook.swift) | File delete is 409 while the library gate is closed; extrafile is empty |
| Ruddarr (Sonarr) | series (list, id, PUT, POST, DELETE), series/editor, episode (+includeEpisodeFile), episode/monitor, release (episode, season), history?episodeId, episodefile DELETE and bulk, calendar (+includeSeries), commands | all | `TestRuddarrSonarrContract` | as above |
| Ruddarr webhook | GET/POST/PUT/DELETE notification | yes; payloads to `notify.ruddarr.com` once allowed and enabled | `TestNotificationsRuddarr`, `notify.TestTestNotification` | Relay payload fields: Grab/Download/Delete/Health shapes as the arrs; ApplicationUpdate never sent |
| LunaSea | apikey query, calendar (plain dates), queue (sortKey progress), queue/status, DELETE queue `blacklist`, history, history/movie, history/series, release, moviefile, episodefile, extraFile, credit, exclusions, qualitydefinition, language, filesystem, languageprofile, importlistexclusion, manualimport (folder, reprocess), ManualImport, queue/details, wanted/missing | all | `TestLunaSeaContract` | filesystem browsing returns nothing; LunaSea's manual import therefore starts from a typed folder |
| autobrr | system/status, release/push (Radarr imdbId int, Sonarr string), movie/series, tag | all | `TestAutobrrContract`, `TestReleasePush` (radarr.go L75-170, types.go L104-138, run_radarr.go L18-87) | A push needs an API key, and its indexer must be a configured one the version lists; `downloadUrl`-only pushes are rejected (no hash) |
| decypharr | status, config/downloadclient, queue paging, DELETE queue/bulk, history by downloadId, history/failed, moviefile/episodefile GET and DELETE, release GET and verbatim POST, manualimport?downloadId, ManualImport (copy), commands, series/movie/episode/episodefile mapping | all | `TestDecypharrContract` (arrview), `TestDecypharrRepairContract` (queue.go, history.go, release.go, import.go, media.go, library.go) | File delete 409 while gated; `removeFromClient` never deletes on a provider |
| Seerr, Homepage | (arrview) | yes | existing contract tests | — |

## Deviations

- **Decisions are per version.** In a view with several versions, a release is approved when any accepts it, and
  rejections name the version. Per-version views (`/v/<version>/…`) give one version's answer; push clients should
  use them.
- **Interactive search, parse and decide also serve shadow versions.** They only read indexers through the gateway;
  any grab still goes to the router, which writes only for active versions.
- **Release guids are Theatarr's** (`theatarr:<indexer>:<hash>`): an indexer's own guid can be a URL with a passkey.
  `downloadUrl` is never written; `magnetUrl` is the bare magnet of the hash.
- **Custom format ids** in release, parse and manualimport resources are stable numbers derived from the name (Theatarr's
  formats have no arr ids).
- **Indexer ids** are positions of the sorted policy keys, the same in every view; they change if the policy gains
  an indexer that sorts before.
- **Queue removal** ends Theatarr's record (`failed`). A download that was only waiting for a pacing slot (`queued`) can
  be routed again by its deferred route job, unless `blocklist=true` was set.
- **History** for a Sonarr pack names its first episode only (the arrs write one record per episode).
- **Failed routes and queue removals are `downloadFailed`.** A route that fails before any provider takes it has no
  direct arr equivalent (an arr hands a grab to its download client at once); Theatarr records it as `downloadFailed` so a
  caller polling history sees it. A queue removal without `blocklist` is also
  `downloadFailed` here (the arrs write no history for it).
- **`cachedOn`** on `ReleaseResource` is Theatarr's, not the arrs'.
- **File deletes** answer 409 instead of 200 when the library gate refuses.
- **ManualImport** imports each chosen file at its path with the client's movie or series and `episodeIds`
  (Sonarr's `ManualImportService.Execute`): files of one of Theatarr's downloads still in hand (`downloadId`, ready or
  importing) import into it; other files on the mount into a new download (source `api:manualimport`) of the
  title's first version in the view, one per release folder. Only the chosen files link, with the chosen episodes
  (`docs/design/library.md`, "Which file is which episode"); the sample guard and the gate still apply (the mislabel guard only notes a manual pick). `importMode`
  `auto` and `copy` link (the source stays, as a copy leaves it); `move` fails the command, as does a file that is
  not on the debrid mount (nor a symlink into it): Theatarr never copies, moves or deletes files. A series file without
  episodes, or with another series' episodes, is refused. `POST /api/theatarr/v1/import` takes a hash or a mount path
  (a release folder, or a folder or file in it, kept as given) and optional `files` with their episodes.
- **Webhook payloads**: no images, overview, genres or media info (Theatarr does not hold them); `indexerFlags` empty; the
  release name and indexer always (see Webhooks, Release details);
  `deletedFiles` on an upgrade is an empty list; `downloadClientType` is
  `theatarr`. Only Webhook notifications are served.
- **Unlinks are logged** as `library_action` kind `replace` with an "unlinked" detail (the kind's CHECK has no
  `unlink`).
- **Anime movies through Seerr's default Radarr server.** Seerr picks the server per request (one server per arr). Theatarr's `/radarr` view serves the default versions and has no anime-movie override of its own, so an anime
  movie requested through the default server becomes a non-anime version unless a Seerr override rule sends anime
  movies to the anime view.

## Integrating a push client

A client that pushes releases and imports its own downloads (for example a Usenet-to-debrid bridge):

- Point each instance at a per-version view, e.g. `http://theatarr:7700/v/movie-web/radarr`, with a Theatarr API
  key. Its calls then answer for that version alone.
- **Push by hash**:
  - Send the indexer's **name** as `indexer` (a policy key, or a name that matches one: "1337x (Prowlarr)" matches
    `prowlarr-1337x`). The positional `indexerId` is not used. An indexer that matches none, or one the version does
    not list, is rejected (`IndexerNotInVersion`).
  - Provider choice is the router's (`versions[*].providers` order), not the indexer's, so per-provider indexer
    copies are not needed.
  - The push is routed only when the version is active and `router.enabled` is on. Before that the answer is still
    the decision, and the router records a dry-run plan.
- **Import**: instead of `manualimport?folder` + `ManualImport`, use
  `POST /api/theatarr/v1/import {title_id, version, info_hash, release}` once the torrent is on the debrid provider.
  - The library finds it on the mount by hash, guards and links it with the version's naming.
  - It returns the plan, or a dry run with the reason while the library gate is closed.
- **Scoring**: `parse` and `release` work as in the arrs. `POST /api/theatarr/v1/decide` gives the full decision
  (rejections with reasons, formats with scores, upgrade verdict) for any name and version.
- **Ids**: movie ids are `1,000,000 + title id` (replaced instances' ids still answer through `alias_from` on the
  configured views only, not on `/v/` views). Stored rows with old ids need a one-time mapping (`GET movie?tmdbId=`).
- **Usenet search** stays on Prowlarr (`/api/v1/search`): Theatarr does not serve it.
- **Health**: `GET health` lists the shadow-mode notice and the router-disabled warning (`RouterDisabledCheck`).
  Indexer back-offs are in `GET /api/theatarr/v1/gateway`, not in "Indexers unavailable due to failures".
- **Push outcome**: after `release/push`, poll `history/movie?movieId=&eventType=4` (or without `eventType`) as well
  as `eventType=1`: a failed route is `downloadFailed` with `data.message`.
- **Cached on**: `GET release` carries `cachedOn` for every release with cache evidence (DMM's feeds across the
  whole search, stored evidence, TorBox's check).
