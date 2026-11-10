# Client compatibility

An audit of what each client calls, read from the clients' source at these revisions: Seerr `c57dca9`, Homepage
`20bfd1c`, Ruddarr `d07829d`, LunaSea `6ee0bf9`, autobrr `58bad4f`, and decypharr `917cfa2`. The usenet-to-debrid
helper rows come from the calls pinned in `contract_devices_test.go`. Status means `ok` (wire contract matches),
`fixed` (arrview adapted to the client), or `not needed` (the client does not make that
call, with the reason stated).

## Seerr

Source paths: `server/api/servarr/base.ts`, `radarr.ts`, `sonarr.ts`, and `server/lib/downloadtracker.ts`.

Every request uses the Radarr or Sonarr base URL ending in `/api/v3` and sends `apikey` as a query parameter.
Types below are JSON types. `?` marks a property the arr may omit when its nullable value is unknown; Seerr's
TypeScript declarations sometimes incorrectly make those fields required.

| Call | Request sent | Response fields Seerr reads | Status |
|---|---|---|---|
| `GET /system/status` | No query/body. | `version:string`, `urlBase:string`; Sonarr parses the major version. | ok |
| `GET /qualityProfile` | No query/body. | `{id:number,name:string}[]`. | ok |
| `GET /rootfolder` | No query/body. | `id:number`, `path:string`, `freeSpace:number`; the UI defaults missing free space to 0. `totalSpace:number` is declared but not used. | ok |
| `GET /tag` | No query/body; repeated on request when looking for a requester tag. | `{id:number,label:string}[]`. | ok |
| `POST /tag` | `{label:string}`. | `{id:number,label:string}`. | ok |
| `GET /movie/lookup?term=tmdb:N` | Add or remove request lookup. | First `RadarrMovie`: `id?:number` when not added, `hasFile:boolean`, `monitored:boolean`, `tags:number[]`; the result is spread into PUT when reactivating an unmonitored movie. `movieFile?` and `mediaInfo?` may be omitted; `resolution?` is a string when available. | ok |
| `POST /movie` | `{title:string,qualityProfileId:number,profileId:number,titleSlug:string,minimumAvailability:string,tmdbId:number,year:number,rootFolderPath:string,monitored:boolean,tags:number[],addOptions:{searchForMovie:boolean}}`. | Created movie `{id:number}`; Seerr stores it as the external service id. | ok |
| `PUT /movie` | Full lookup object plus `{title:string,qualityProfileId:number,profileId:number,titleSlug:string,minimumAvailability:string,tmdbId:number,year:number,tags:number[],rootFolderPath:string,monitored:boolean,addOptions:{searchForMovie:boolean}}`. Tags are the union of existing and requested ids. | `id:number`, `monitored:boolean`, `isAvailable:boolean`; `id` is used for an optional search. | ok |
| `POST /command` (Radarr) | `{name:"MoviesSearch",movieIds:number[]}`; download sync sends `{name:"RefreshMonitoredDownloads"}`. | Response ignored. | ok |
| `GET /movie` | Scanner list, no query; cleanup also sends `?tmdbId=N`. | Scanner reads `id:number,tmdbId:number,titleSlug:string,title:string,hasFile:boolean,monitored:boolean`; cleanup checks `tmdbId`. | ok |
| `GET /movie/{id}` | Stored service id or a legacy id. | `tmdbId:number`, `hasFile:boolean`; for a file, `movieFile?` and `mediaInfo?` may be omitted, with `resolution?:string` (used to split 4K from non-4K on a shared host). | fixed |
| `POST /command` (Sonarr) | `{name:"MissingEpisodeSearch",seriesId:number}`; download sync sends `{name:"RefreshMonitoredDownloads"}`. | Response ignored. | ok |
| `GET /series/lookup?term=tvdb:N` | Add/remove request lookup. Existing series are PUT; otherwise lookup seasons seed the POST body. | First `SonarrSeries`: `id?:number`, `monitored:boolean`, `tags:number[]`, `seasons:{seasonNumber:number,monitored:boolean,statistics?:{episodeFileCount:number,totalEpisodeCount:number}}[]`. Uncatalogued series read TVDB/TMDb metadata. | fixed |
| `POST /series` | `{tvdbId:number,title:string,qualityProfileId:number,languageProfileId?:number,seasons:{seasonNumber:number,monitored:boolean}[],tags:number[],seasonFolder:boolean,monitored:boolean,monitorNewItems:"all"|"none",rootFolderPath:string,seriesType:"standard"|"daily"|"anime",addOptions:{ignoreEpisodesWithFiles:true,searchForMissingEpisodes:boolean}}`. | Created series `{id:number}`. | ok |
| `PUT /series` | Full lookup series; changes `monitored:boolean`, unions tags and marks requested `seasons[].monitored`. | `{id:number}`; then Seerr lists episodes to repair monitoring. | ok |
| `GET /episode?seriesId=N` | Existing-series request follow-up. | `{id:number,seasonNumber:number,monitored:boolean}[]`. | ok |
| `PUT /episode/monitor` | `{episodeIds:number[],monitored:true}`. | Response ignored. | ok |
| `GET /series` | Scanner list, no query; cleanup also sends `?tvdbId=N`. | Scanner reads `id:number,tvdbId:number,titleSlug:string,title:string,seasons[].{seasonNumber:number,monitored:boolean,statistics.{episodeFileCount:number,totalEpisodeCount:number}}`; cleanup checks `tvdbId`. | ok |
| `GET /series/{id}` | Stored service id or legacy id. | `tvdbId:number`, `statistics.episodeFileCount:number`, and `seasons[]` with per-season `statistics.episodeFileCount:number`. | ok |
| `GET /series/lookup?term=<TV title>` | Seerr's service lookup route searches by a TMDb title. | Series list; only the result list is returned to the caller. | ok |
| `GET /queue?includeEpisode=true` | No page parameters, so Arr defaults apply (`page=1,pageSize=10`). | Reads `records[]`: `movieId?:number` or `seriesId?:number`, `episode?` with episode fields, `size:number`, `sizeleft:number`, `status:string`, `title:string`, `downloadId:string`, `timeleft:string` and `estimatedCompletionTime:string` in Seerr's declaration. Recent progress supplies ETA; completed downloads report zero/now. Unknown estimates remain omitted. | fixed |
| `DELETE /movie/{id}` | `?deleteFiles=true&addImportExclusion=false`; response ignored, 404 is treated as already removed. | No response fields. | ok |
| `DELETE /series/{id}` | `?deleteFiles=true&addImportExclusion=false`; response ignored, 404 is treated as already removed. | No response fields. | ok |
| Sonarr `GET /languageprofile` | Only called if `system/status.version` is major 3 or lower. Theatarr reports Sonarr 4. | `[{id:number,name:string}]`. | not needed: reported major is 4, so Seerr skips this endpoint. |

The movie-file resolution row is fixed in arrview: movie-file resources expose the parsed quality's standard
resolution when no probe resolution exists, while any recorded probe value remains authoritative. Required numeric
`mediaInfo` fields are also emitted with arr zero values so Ruddarr's strict file decoder accepts the object.
Uncatalogued series lookups fetch the default-order seasons and series fields from the configured TVDB client
(TMDb on TVDB 404 or without TVDB), through the metadata gateway, within five seconds. Results are cached for five
minutes, failures for thirty seconds. A failed lookup is a 503 (Sonarr's SkyHookException), so Seerr's request fails
and can be retried instead of POSTing a stub with no seasons; an unknown id is an empty list. Both Seerr and
Overseerr's lookup → POST season selection is stored as durable intent and survives metadata refreshes. Seerr's
existing-series PUT changes only the seasons whose flag differs from what Theatarr reports, as Sonarr does.

Seerr declares queue `timeleft` and `estimatedCompletionTime` as non-null strings, but the Radarr/Sonarr queue
resources make both nullable. Theatarr omits unknown values just as the arr serializer does. Seerr turns
`estimatedCompletionTime` into a `Date` without a null guard, so an unknown ETA may render poorly. The router
keeps six progress samples over at most five minutes (migration 00036) for measured estimates; flat, reset or stale
progress has no estimate. Downloads ready for linking or importing report `00:00:00` and the current time.

## decypharr

Source paths: `pkg/arr/client.go`, `queue.go`, `history.go`, `release.go`, `import.go`, `media.go`, and `library.go`.

The client sends `X-Api-Key`. It does not call `rootfolder` or `tag`.

| Call | Request sent | Response fields decypharr reads | Status |
|---|---|---|---|
| `GET /system/status` | No query/body. | `appName:string` to identify Radarr vs Sonarr. | ok |
| `GET /config/downloadclient` | No query/body. | `{id:int,enableCompletedDownloadHandling:bool,autoRedownloadFailed:bool,autoRedownloadFailedFromInteractiveSearch:bool}`. | ok |
| `GET /queue?page=N&pageSize=200` | Pages until `records` is empty or `totalRecords` is reached. | `{page:int,pageSize:int,sortKey:string,sortDirection:string,totalRecords:int,records:[]}`; each item reads `id:int,title/status/trackedDownloadStatus/trackedDownloadState/downloadId/protocol:string,statusMessages:{title:string,messages:string[]}[]`. | ok |
| `DELETE /queue/bulk` | Query `removeFromClient=true`, `blocklist=true`, `skipRedownload=<bool>`, `changeCategory=false`; body `{ids:int[]}`. | 2xx status only. Theatarr ends only its router row; the request never deletes a shared provider torrent. | ok |
| `GET /history` | `page,pageSize=100,sortKey=date,sortDirection=descending,downloadId`. | Paged `{totalRecords:int,records:[]}`; fields read include `id:int,downloadId:string,eventType:string,movieId?:int,data?:map[string]string` (`data.indexer:string`). | ok |
| `POST /history/failed/{id}` | No body. | 2xx status only. | ok |
| `GET /movie?tmdbId=N`, `GET /movie/{id}` | Radarr library mapping. | `id:int,tmdbId:int,path:string,movieFile?:{id:int,movieId:int,path:string,size:int64}`. | ok |
| `GET /series?tvdbId=N`, `GET /episodefile?seriesId=N`, `GET /episode?seriesId=N` | Sonarr library mapping. | Series `id:int`; file `id/seriesId/seasonNumber:int,path:string,size:int64`; episode `id/episodeFileId:int`. | ok |
| `GET /moviefile/{id}`, `GET /episodefile?seriesId=N` | Repair reads a file or lists files. | Radarr file `id/movieId:int,path:string,size:int64`; Sonarr file `id/seriesId/seasonNumber:int,path:string,size:int64`. | ok |
| `DELETE /moviefile/{id}`, `DELETE /episodefile/{id}`, bulk file delete | Repair cleanup; bulk body is `{movieFileIds:number[]}` or `{episodeFileIds:number[]}`. | 2xx status only; 409 while the library gate is closed. | ok |
| `GET /release?movieId=N`, `?episodeId=N`, or `?seriesId=N&seasonNumber=N` | Search results. | `guid/title/indexer:string,indexerId:int,approved/rejected/temporarilyRejected/downloadAllowed:bool,rejections:string[]`; the full returned release is retained as raw JSON. | ok |
| `POST /release` | The release resource verbatim (including `{guid,indexerId}` and any unknown response fields). | 2xx status only. | ok |
| `GET /manualimport?downloadId=HASH` | Lists candidate files. | `path/folderName:string,quality:JSON,languages:{id:int,name:string}[],customFormats:[] ,customFormatScore/indexerFlags:int,releaseGroup/releaseType:string,rejections:{reason:string,type:string}[],movie.id?` or `series.id?,episodes[].id,seasonNumber`. | ok |
| `POST /command` (`ManualImport`) | `{name:"ManualImport",importMode:"copy",files:[{downloadId:string,path:string,movieId?:int,seriesId?:int,episodeIds:int[],quality,languages,releaseGroup,customFormats,customFormatScore,indexerFlags,releaseType,rejections}]}`. | `id:int,name/commandName/status:string,queued:string` (timestamp), `body:{name:string,movieIds?:int[],episodeIds?:int[],seriesId?:int,seasonNumber?:int}`; body carries submitted ids/name. | ok |
| `POST /command` (search/refresh), `GET /command`, `GET /command/{id}` | Search names include `MoviesSearch`, `EpisodeSearch`, `SeasonSearch`; refresh uses `RefreshMonitoredDownloads`. | Command `id:int,name/commandName/status:string,queued:string` (timestamp), `body:{name:string,movieIds?:int[],episodeIds?:int[],seriesId?:int,seasonNumber?:int}`. | ok |
| `GET /rootfolder`, `GET /tag` | No calls in `pkg/arr` or its consumers. | None. | not needed: no request is made. |

### Theatarr as decypharr's client (`add_via: client`)

The other direction: a version with `add_via: client` adds its grabs to decypharr through decypharr's qBittorrent
API (`internal/qbit`; `docs/design/router.md`, "Client-routed versions"). Calls: `POST /api/v2/auth/login`, then
`POST /api/v2/torrents/add` (`urls=magnet:?xt=urn:btih:<hash>`, `category`) and `GET /api/v2/torrents/info`
(`hashes=<one hash>` per request, no category: decypharr 2.7.1 does not split `h1|h2`), with the `SID` cookie; a
403 logs in again once.

- **Login.** decypharr takes the qBittorrent username as the arr URL it calls back and the password as that arr's
  API key. So `username_env` holds the callback view's URL (e.g. `http://theatarr:8080/radarr`) and `password_env` a
  Theatarr key for that view. Both are read from the environment; the password is a gateway secret.
- **Category.** `category` is sent with the login and every add; it is required for a client an `add_via: client`
  version uses. Reads are by hash under any category, so a hash an arr added before the handover is adopted.
- **Import.** `import_from: mount` (default): Theatarr finds the finished torrent on its own debrid mount by name.
  `import_from: download_folder`: Theatarr reads decypharr's folder of symlinks (`content_path`), with
  `remote_path` (decypharr's prefix) replaced by `local_path` (the same folder as Theatarr sees it), for the folder
  and for absolute link targets; the links must point into Theatarr's debrid mount. A completed torrent with no
  folder of its own (decypharr's multi-season fan-out) is found on the mount by name instead.
- **Errors.** decypharr's JSON error `code` decides: `torrent_not_cached` moves on to the next release, a permanent
  error blocklists the release, anything else retries later. Theatarr never deletes from decypharr.

## autobrr

Source paths: `pkg/arr/radarr/{client.go,radarr.go,types.go}` and `pkg/arr/sonarr/{client.go,sonarr.go,types.go}`.

Radarr and Sonarr clients send `X-Api-Key`. The response decoder requires a non-empty array from a
successful push. It treats `rejected || temporarilyRejected` as a rejection; otherwise the push is success. `approved`
is part of the response type but is not used by autobrr's decision code.

| Call | Request sent | Response fields autobrr reads | Status |
|---|---|---|---|
| `GET /system/status` | No query/body. | `version:string`; HTTP 200 succeeds, 401 is bad credentials. | ok |
| `GET /movie`, `GET /series` | No query, for list processing. | Radarr fields read: `id:int64,monitored:bool,tags:int[],title/originalTitle:string,alternateTitles:{title:string}[]`; Sonarr fields read: `id:int64,monitored:bool,tags:int[],title:string,alternateTitles:{title:string}[]`. Empty tag and alternate-title lists are accepted. | ok |
| `GET /tag` | Only when an autobrr list has include/exclude tag filters. | `{id:int,label:string}[]`. | ok |
| `POST /release/push` | Radarr sends `{title:string,infoUrl?:string,downloadUrl?:string,magnetUrl?:string,size:uint64,indexer:string,downloadProtocol:string,protocol:string,publishDate:string,downloadClientId?:int,downloadClient?:string,indexerFlags?:int,tmdbId?:int,imdbId?:int}`. Sonarr sends the same common fields with `tvdbId?:int,imdbId?:string`. | Success: `{approved:bool,rejected:bool,temporarilyRejected:bool,rejections:string[]}[]`. A 400 validation array is decoded from `{severity,errorCode,errorMessage,propertyName,attemptedValue:string}`. | ok |

Pushes return HTTP 200 with the arr-shaped list. A rejected release has `rejected:true` and string
rejections; a temporarily rejected release has `temporarilyRejected:true` and `rejected:false`, which autobrr still
reports as a rejection. A `downloadUrl` alone can be evaluated but is not routed without an info hash.

## Homepage

Homepage puts `apikey` in the query string. It reads raw JSON fields in widget mappings.

| Call | Request sent | Response fields Homepage reads | Status |
|---|---|---|---|
| Radarr `GET /movie` | No query. | `monitored,hasFile,isAvailable,title,id`. | ok |
| Radarr `GET /queue/status` | No query. | Requires `totalCount`. | ok |
| Radarr `GET /queue/details` | No query. | `trackedDownloadState,trackedDownloadStatus,timeleft,size,sizeleft,movieId|id,status`. | ok |
| Sonarr `GET /series` | No query. | `title,id`. | ok |
| Sonarr `GET /queue`, `GET /wanted/missing` | No query. | Requires `totalRecords`. | ok |
| Sonarr `GET /queue/details` | No query. | `trackedDownloadState,trackedDownloadStatus,timeleft,size,sizeleft,seriesId,episode?.title|title,episodeId|id,status`. | ok |
| Radarr/Sonarr `GET /calendar` | `start,end,unmonitored`; Sonarr can additionally send `includeSeries,includeEpisodeFile,includeEpisodeImages`. | Widget passes the JSON through. | ok |

## Ruddarr

Ruddarr sends `X-Api-Key`; it decodes required Swift properties and fails when a required field is absent or null.

| Calls | Request/response contract | Status |
|---|---|---|
| Radarr `GET /system/status`, `/rootfolder`, `/qualityprofile`, `/diskspace`, `/tag`, `/movie`, `/movie/{id}` | Reads `appName,instanceName,version`; root `id,accessible`; movie list requires `tmdbId,title,sortTitle,year,runtime,alternateTitles,genres,status,isAvailable,minimumAvailability,monitored,qualityProfileId,added,tags,images`. File `mediaInfo?` requires numeric `audioBitrate,audioChannels,audioStreamCount,videoBitDepth,videoBitrate,videoFps` when present. | ok |
| Radarr `GET /history/movie?movieId=`, `/moviefile?movieId=`, `/extrafile?movieId=`, `/release?movieId=`, `/calendar` | History/file/release models require their IDs, dates, quality and status fields; extra files may be `[]`. Calendar sends `unmonitored=true,start,end`. | ok |
| Radarr `GET /queue` | `includeMovie=true,includeSeries=true,includeEpisode=true,pageSize=250`; reads paging object and records including `id,protocol,size,sizeleft,quality,movie`. | ok |
| Radarr `POST /release` | `{guid,indexerId}`; forced grab adds `movieId`. | ok |
| `GET <view>/ping` (outside `/api/v3`, no key) | Picks a reachable address; wants 2xx and JSON `status` equal to `OK`. | ok |
| Radarr `DELETE /queue/{id}`, `DELETE /movie/{id}`, `DELETE /moviefile/{id}` | Queue query includes `removeFromClient,blocklist,skipRedownload`; movie query includes `deleteFiles,addImportExclusion`. File unlink returns 409 while gated. | ok |
| Radarr `PUT /movie/editor`, `POST /command`, `GET /manualimport`, notification CRUD | Editor body includes `movieIds,monitored,qualityProfileId,minimumAvailability,rootFolderPath,tags,applyTags,moveFiles?`; commands include refresh/search/manual import. Notification install uses `/notification`. | ok |
| Sonarr `GET /series`, `/series/{id}`, `/episode?seriesId=&includeEpisodeFile=true`, `/release?episodeId=` or `?seriesId=&seasonNumber=`, `/history?episodeId=`, `/calendar` | Reads series and episode fields including `seasons`, `episodeFile`, `episodeFileId`, `hasFile`, `monitored`; calendar sends `unmonitored,start,end,includeSeries`. | ok |
| Sonarr `POST /series`, `PUT /series/{id}`, `PUT /series/editor`, `PUT /episode/monitor` | Add/update full series resources and season monitoring; editor body includes `seriesIds,monitored,monitorNewItems,seriesType,seasonFolder,tags,applyTags,moveFiles?`; episode monitor body `{episodeIds,monitored}`. | ok |
| Sonarr `POST /release`, `POST /command`, `DELETE /queue/{id}`, `DELETE /series/{id}`, `DELETE /episodefile/{id}` and `/episodefile/bulk` | Release body is `{guid,indexerId,seriesId?,seasonNumber?}`; commands include refresh/search; delete files are refused with 409 while gated. | ok |

## LunaSea

LunaSea sends the key as the `apikey` query parameter. The contract includes its old `blacklist` queue parameter.

| Calls | Request/response contract | Status |
|---|---|---|
| Radarr/Sonarr `GET /calendar`, `GET /queue`, `GET /queue/status`, `GET /queue/details`, `GET /history`, `GET /history/movie` or `/history/series`, `GET /release`, `GET /moviefile` or `/episodefile` | Calendar accepts date-only `start,end`; queue uses `page,pageSize,sortDirection,sortKey` and include flags; history uses paging/sorting. Queue/details embeds episode when requested. | ok |
| Radarr/Sonarr `DELETE /queue/{id}` | Radarr client sends `removeFromClient,blacklist`; Sonarr supports the standard queue flags. | ok |
| Radarr/Sonarr `GET /manualimport?folder=`, `POST /manualimport` reprocess, `POST /command` ManualImport | Reprocess sends selected file items; command uses `{name:"ManualImport",importMode:"copy",files:[...]}`. Files outside the debrid mount fail with a terminal command message. | ok |
| Sonarr `GET /wanted/missing`, `/episode`, `/languageprofile`, `/importlistexclusion`; Radarr `GET /extraFile`, `/credit`, `/exclusions`, `/qualitydefinition`, `/language`, `/filesystem`, `/health`, `/diskspace`, `/importlist/movie` | Screens read the expected array/static resources; filesystem browsing is empty. | ok |

## Usenet-to-debrid helpers

A helper that hands usenet releases to a debrid service through the arr API parses titles, pushes releases, tags
movies, runs manual imports and polls history for failures. It points each of its instances at a per-version view
(`/v/<version>/radarr|sonarr`). These calls and response checks are pinned by `contract_devices_test.go`.

| Calls | Request/response contract | Status |
|---|---|---|
| Radarr `GET /parse?title=`, `/qualityprofile`, `/moviefile?movieFileIds=`, `/moviefile?movieId=`, `/movie`, `/movie/{id}`, `/release?movieId=`, `/health`, `/indexer`, `/tag`, `/tag/detail`, `/history/movie`, `/history`, `/queue` | Reads parse score/movie id, profile quality items, file ids and quality, movie metadata, `cachedOn`, tags and their movie ids, history download IDs/data, queue page records, health issues, indexer name/id/protocol. | ok |
| Radarr `POST /tag`, `PUT /movie/editor`, `POST /release/push`, `POST /release`, `POST /command` ManualImport and `GET /command/{id}` | Tag body `{label}`; editor body `{movieIds,tags,applyTags,monitored?}`; push body includes title, magnet/hash, protocol, indexer id/name, size, external ids and publish date. ManualImport `move` is deliberately reported failed because Theatarr links and never moves files. | ok |
| Sonarr `GET /series`, `/series/{id}`, `/episodefile?seriesId=`, `/episode?seriesId=&seasonNumber=`, `/wanted/cutoff` | Reads title/series id, episode files, episode season data and cutoff paging. | ok |
| Sonarr `POST /release/push`, `GET /history/series?seriesId=&eventType=4`, `GET /history?episodeId=&eventType=4` | Pushes hash/TVDB id; history poll reads failed download id and `data.message`. | ok |

## Decisions

- **Season selection resolved:** metadata-backed TVDB id lookups let Seerr carry the real season choices into the
  add, including unmonitored seasons; refresh preserves them. When the metadata lookup fails the lookup is a 503 and
  the request fails (retryable); only without a metadata source does it return the old stub.
- **Queue estimates resolved where measurable:** provider progress supplies ETA, completed downloads report zero/now,
  and unknown ETA remains omitted as in the arrs. Seerr's missing null guard remains a client limitation for unknown
  estimates; Theatarr never supplies a constant to conceal it.
