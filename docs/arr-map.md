# From the arrs to Theatarr

Use the menu paths below on desktop or in phone **More**. Settings search accepts these page names and keeps each named alias under its own label.
Theatarr libraries replace separate arr instances; you choose each library's feeds and providers and when it may
download. Title pages call them **Libraries**. Each library's **Watch only** / **Live** control is in **Settings ›
Libraries**; **Pause** in the admin header stops searches, new downloads and library changes for every library. Download notices link to the closed gate's page.
“Not needed” means no equivalent page in Theatarr's current workflow, not that the upstream feature is worthless.

The arr navigation comes from the Sonarr v5 (develop branch), Radarr v6.4.4 and Prowlarr sources: each
`frontend/src/App/AppRoutes.tsx` and `frontend/src/Components/Page/Sidebar/PageSidebar.*`. This is a UI map, not a
claim of Sonarr v5 API parity. Recyclarr has no web pages; its table maps the CLI commands from
`src/Recyclarr.Cli/Console/Commands/`. Prowlarr has History but no Wanted or Activity queue tabs.

## Sonarr v5

| Arr page or tab | Theatarr page (route and menu path) | Difference |
|---|---|---|
| Series | Library › Series — `/series` | One title can have multiple libraries. **Add series** in the header opens the search. In the table view each row ends with **Search now** and **Choose season or episode** (managers and admins). The arrs' "Search Selected": **Select**, tick the titles, pick one library in the bar and choose **Search now** (managers and admins). It searches what is missing in that library, as the Wanted page's Search now does, under its hourly limit. |
| Series › Add New | Search — `/search` | Choose libraries when adding. A series new to the library gets **Monitor** (the seasons you pick, or All, Future or Missing episodes, First or Latest season, Pilot only, None) and **Monitor seasons that appear later**. **Add** adds without searching; **Add and search** also searches now. A new series also gets **Series type** (Standard, Daily or Anime); it is one value per series, shown on every Sonarr view, and is changed later on the title page. |
| Series › Library Import | Admin › Import from Sonarr/Radarr — `/admin/arr-import` | Brings a Sonarr instance's series, their monitoring and the files on the debrid mount into one library, once; Wanted › Manual import (`/wanted/manual-import`) handles other existing files. |
| Calendar | Calendar — `/calendar` | Movies and episodes together, coloured by status (downloaded, downloading, missing, upcoming, not monitored) with a legend. An episode opens its series with that episode open. |
| Statistics | **not needed** | No separate library-statistics dashboard; counts and filters are in the library and System Status. |
| Settings | Admin › Settings — `/admin/settings` | Search by task or an arr page name. |
| Settings › Media Management | Admin › Settings › Libraries — `/admin/settings/libraries` | Libraries have independent roots, profiles, indexers and providers; Naming and File management are tabs. |
| Settings › Profiles | Admin › Settings › Profiles — `/admin/settings/profiles` | Quality, language preferences and scores are configured per profile. |
| Settings › Quality | Admin › Settings › Profiles — `/admin/settings/profiles` | Quality sizes are set per profile in the profile editor's Size limits form (the raw profile editor still works); Quality sync can manage TRaSH size definitions. |
| Settings › Custom Formats | Admin › Settings › Custom formats — `/admin/settings/formats` | Shared definitions; scores are edited on profiles. |
| Settings › Indexers | Admin › Settings › Indexers — `/admin/settings/indexers` | Torznab feeds (Prowlarr, Jackett: **Add indexer › Torznab & built-in sources**), built-in sources and the native catalog coexist; each library chooses its feeds. |
| Settings › Download Clients | Admin › Settings › Download clients — `/admin/settings/downloads` | Debrid providers are direct; a decypharr-style debrid client goes under Other clients (seedbox clients are not supported yet). Pause remains explicit. |
| Settings › Import Lists | Library › Collections › Lists — `/collections/lists` | A Sonarr instance can be a list (its series are added to the chosen series libraries every 15 minutes, read only; with Search on add they are searched on the hourly schedule, not at once); TMDb movie lists and Radarr instances live there too. Other series list providers are not supported. |
| Settings › Connect | Admin › Settings › Notifications — `/admin/settings/notifications` | Webhook, Discord and ntfy targets and Send notifications; Plex lives under Plex & playback. |
| Settings › Metadata | **not needed** | No NFO/sidecar consumer editor in this debrid/Plex workflow; metadata services live in Settings › Metadata & artwork. |
| Settings › Tags | **not needed** | No tag-based routing page; library indexer lists and per-indexer provider allowlists make routing explicit. |
| Settings › General | No General page: provider account sync is on Download clients (`/admin/settings/downloads#provider-sync`), the artwork cache on Metadata & artwork (`/admin/settings/metadata#artwork-cache`), other libraries' folders on Torrents (`/admin/torrents#scan-roots`) | Accounts/API keys are under Users & access, deployment flags remain outside the UI. `/admin/settings/general` redirects. |
| Settings › UI | **not needed** | No separate UI settings page; theme is in the account menu, display choices are beside their views. |
| Settings › Metadata Source | Admin › Settings › Metadata & artwork — `/admin/settings/metadata` | TMDb/TVDB and anime metadata services. |
| System › Status | Admin › System › Status — `/admin` | Health and readiness across the single service. Admins open the broken library links from the dangling-links check (**Show the N broken links**). |
| System › Tasks | Admin › System › Tasks — `/admin/system/tasks` | Jobs and failures, rather than one scheduler per arr instance. |
| System › Backups | Admin › System › Backup — `/admin/system/backup` | Full Theatarr backups and restore plus separate torrent-list backups. |
| System › Updates | **not needed** | Deploy a new Theatarr binary/container; there is no in-app updater. |
| System › Events | Admin › System › Errors — `/admin/system/errors` | Persistent error inbox; ordinary runtime messages are in Logs. |
| System › Log Files | Admin › System › Logs — `/admin/system/logs` | Recent lines from memory, plus the rotated log files to download. |
| System › Update Log Files | **not needed** | No in-app updater; inspect deployment/container logs. |
| Activity › Queue | Activity › Queue — `/activity` | Downloads and imports as a table with the columns you choose (accounts that see files); remove several at once; each action follows the account's permissions. |
| Activity › History | Activity › History — `/activity/history` | Grabs, imports, failures and search decisions as a table with the columns you choose (accounts that see files); admin decision links explain outcomes. |
| Activity › Blocklist | Activity › Blocklist — `/activity/blocklist` | Requires See files; filter by title and library, 50 a page, unblock several at once; provider deletes remain separate. |
| Wanted › Missing | Wanted › Missing — `/wanted` | Managers and admins: each item's next automatic search and why. **Search now** for a row, the selection or the whole tab makes them due; searches start as the hourly limit allows. |
| Wanted › Cutoff Unmet | Wanted › Cutoff unmet — `/wanted/cutoff` | Upgrade queue with the same **Search now** actions; library and global gates remain visible. |

## Radarr

| Arr page or tab | Theatarr page (route and menu path) | Difference |
|---|---|---|
| Movies | Library › Movies — `/movies` | Multiple libraries per movie. **Add movie** in the header opens the search. In the table view each row ends with **Search now** and **Interactive search** (managers and admins). A movie one library holds and another monitored library lacks reads **Partly available** (accounts that see files). The arrs' "Search Selected": **Select**, tick the titles, pick one library in the bar and choose **Search now** (managers and admins). It searches what is missing in that library, as the Wanted page's Search now does, under its hourly limit. |
| Movies › Add New | Search — `/search` | Choose libraries when adding. **Add** adds without searching; **Add and search** also searches now. **Minimum availability** is **Library default** unless you pick Announced, In cinemas or Released; a pick is kept per library and is changed later in the movie's **Libraries** table on its title page. |
| Movies › Import Library | Admin › Import from Sonarr/Radarr — `/admin/arr-import` | Brings a Radarr instance's movies, their monitoring and the files on the debrid mount into one library, once; Wanted › Manual import (`/wanted/manual-import`) handles other files. |
| Movies › Collections | Library › Collections — `/collections` | Managers and admins. **Add collection** searches TMDb; targets are libraries. |
| Movies › Discover | Home — `/` | Discovery rows and search share one library for every user. |
| Calendar | Calendar — `/calendar` | Movies and episodes together, coloured by status (downloaded, downloading, missing, upcoming, not monitored) with a legend. An episode opens its series with that episode open. |
| Settings | Admin › Settings — `/admin/settings` | Search by task or an arr page name. |
| Settings › Media Management | Admin › Settings › Libraries — `/admin/settings/libraries` | Libraries have independent roots, profiles, indexers and providers; Naming and File management are tabs. Radarr's Availability Delay (Settings › Indexers) is per movie library here: **Minimum availability** (the library's default) and **Availability delay** in days (negative searches earlier), both under **Which releases**. A library default wins over the value an arr client sends with an add. |
| Settings › Profiles | Admin › Settings › Profiles — `/admin/settings/profiles` | Quality, language preferences and scores are configured per profile. |
| Settings › Quality | Admin › Settings › Profiles — `/admin/settings/profiles` | Quality sizes are set per profile in the profile editor's Size limits form (the raw profile editor still works); Quality sync can manage TRaSH size definitions. |
| Settings › Custom Formats | Admin › Settings › Custom formats — `/admin/settings/formats` | Shared definitions; scores are edited on profiles. |
| Settings › Indexers | Admin › Settings › Indexers — `/admin/settings/indexers` | Torznab feeds (Prowlarr, Jackett: **Add indexer › Torznab & built-in sources**), built-in sources and the native catalog coexist; each library chooses its feeds. |
| Settings › Download Clients | Admin › Settings › Download clients — `/admin/settings/downloads` | Debrid providers are direct; a decypharr-style debrid client goes under Other clients (seedbox clients are not supported yet). Pause remains explicit. |
| Settings › Import Lists | Library › Collections › Lists — `/collections/lists` | Managers and admins. TMDb movie lists and Radarr or Sonarr instances: add, edit, check, preview, sync now and delete; lists made through the Radarr-compatible API show here too. With the list's Search on add ticked, titles it adds are searched on the hourly schedule, not at once (monitored titles that are already released); with it off they are only added. Settings › Metadata & artwork › Refresh schedules (`/admin/settings/metadata#refresh`) controls timing. |
| Settings › Connect | Admin › Settings › Notifications — `/admin/settings/notifications` | Webhook, Discord and ntfy targets and Send notifications; Plex lives under Plex & playback. |
| Settings › Metadata | **not needed** | No NFO/sidecar consumer editor in this debrid/Plex workflow; metadata services live in Settings › Metadata & artwork. |
| Settings › Tags | **not needed** | No tag-based routing page; library indexer lists and per-indexer provider allowlists make routing explicit. |
| Settings › General | No General page: provider account sync is on Download clients (`/admin/settings/downloads#provider-sync`), the artwork cache on Metadata & artwork (`/admin/settings/metadata#artwork-cache`), other libraries' folders on Torrents (`/admin/torrents#scan-roots`) | Accounts/API keys are under Users & access, deployment flags remain outside the UI. `/admin/settings/general` redirects. |
| Settings › UI | **not needed** | No separate UI settings page; theme is in the account menu, display choices are beside their views. |
| System › Status | Admin › System › Status — `/admin` | Health and readiness across the single service. Admins open the broken library links from the dangling-links check (**Show the N broken links**). |
| System › Tasks | Admin › System › Tasks — `/admin/system/tasks` | Jobs and failures, rather than one scheduler per arr instance. |
| System › Backup | Admin › System › Backup — `/admin/system/backup` | Full Theatarr backups and restore plus separate torrent-list backups. |
| System › Updates | **not needed** | Deploy a new Theatarr binary/container; there is no in-app updater. |
| System › Events | Admin › System › Errors — `/admin/system/errors` | Persistent error inbox; ordinary runtime messages are in Logs. |
| System › Log Files | Admin › System › Logs — `/admin/system/logs` | Recent lines from memory, plus the rotated log files to download. |
| Activity › Queue | Activity › Queue — `/activity` | Downloads and imports as a table with the columns you choose (accounts that see files); remove several at once; each action follows the account's permissions. |
| Activity › History | Activity › History — `/activity/history` | Grabs, imports, failures and search decisions as a table with the columns you choose (accounts that see files); admin decision links explain outcomes. |
| Activity › Blocklist | Activity › Blocklist — `/activity/blocklist` | Requires See files; filter by title and library, 50 a page, unblock several at once; provider deletes remain separate. |
| Wanted › Missing | Wanted › Missing — `/wanted` | Managers and admins: each item's next automatic search and why. **Search now** for a row, the selection or the whole tab makes them due; searches start as the hourly limit allows. |
| Wanted › Cutoff Unmet | Wanted › Cutoff unmet — `/wanted/cutoff` | Upgrade queue with the same **Search now** actions; library and global gates remain visible. |

## Prowlarr

| Arr page or tab | Theatarr page (route and menu path) | Difference |
|---|---|---|
| Indexers | Admin › Settings › Indexers — `/admin/settings/indexers` | One indexer manager shared by libraries. |
| Indexers › Stats | **not needed** | No separate indexer-statistics dashboard; per-source status and tests are beside Settings › Indexers (`/admin/settings/indexers`). |
| Search | Search — `/search` | Find a title, then use its Releases action; no unrestricted all-indexer raw search page. |
| History | Activity › Decision journal — `/activity/journal` | Search and download decisions, not a full Prowlarr request log. |
| Settings | Admin › Settings — `/admin/settings` | Search by task or an arr page name. |
| Settings › Indexers | Admin › Settings › Indexers › VPN — `/admin/settings/indexers/vpn` | Optional VPN guard and catalog settings replace proxy management; native definition reviews are under Reviews. |
| Settings › Apps | Admin › Settings › Connected apps — `/admin/settings/apps` | Theatarr is its own indexer manager, so there is no indexer-to-arr sync; Connected apps exposes Radarr- and Sonarr-compatible APIs to Seerr and other clients. |
| Settings › Download Clients | Admin › Settings › Download clients — `/admin/settings/downloads` | Debrid providers are direct; a decypharr-style debrid client goes under Other clients (seedbox clients are not supported yet). Pause remains explicit. |
| Settings › Connect | Admin › Settings › Notifications — `/admin/settings/notifications` | Webhook, Discord and ntfy targets and Send notifications; Plex lives under Plex & playback. |
| Settings › Tags | **not needed** | No tag-based routing page; library indexer lists and per-indexer provider allowlists make routing explicit. |
| Settings › General | No General page: provider account sync is on Download clients (`/admin/settings/downloads#provider-sync`), the artwork cache on Metadata & artwork (`/admin/settings/metadata#artwork-cache`), other libraries' folders on Torrents (`/admin/torrents#scan-roots`) | Accounts/API keys are under Users & access, deployment flags remain outside the UI. `/admin/settings/general` redirects. |
| Settings › UI | **not needed** | No separate UI settings page; theme is in the account menu, display choices are beside their views. |
| Settings › Development | **not needed** | No developer-settings page; diagnostics use System and deployment configuration. |
| System › Status | Admin › System › Status — `/admin` | Health and readiness across the single service. Admins open the broken library links from the dangling-links check (**Show the N broken links**). |
| System › Tasks | Admin › System › Tasks — `/admin/system/tasks` | Jobs and failures, rather than one scheduler per arr instance. |
| System › Backup | Admin › System › Backup — `/admin/system/backup` | Full Theatarr backups and restore plus separate torrent-list backups. |
| System › Updates | **not needed** | Deploy a new Theatarr binary/container; there is no in-app updater. |
| System › Events | Admin › System › Errors — `/admin/system/errors` | Persistent error inbox; ordinary runtime messages are in Logs. |
| System › Log Files | Admin › System › Logs — `/admin/system/logs` | Recent lines from memory, plus the rotated log files to download. |

## Recyclarr (CLI)

| Arr page or tab | Theatarr page (route and menu path) | Difference |
|---|---|---|
| Sync | Admin › Settings › Quality sync — `/admin/settings/quality-sync` | Runs on schedule and applies upstream changes; an update log lists them. Local edits stay yours. |
| Config create / list templates | Admin › Settings › Profiles — `/admin/settings/profiles` | Create a profile from a TRaSH preset; Recyclarr YAML can also be used as a sync source. |
| Config list local | Admin › Settings › Quality sync — `/admin/settings/quality-sync` | Configured sync sources replace a directory of target-instance configs. |
| List custom formats / custom format groups | Admin › Settings › Custom formats — `/admin/settings/formats` | Definitions are shared and profile scores are separate. |
| List quality profiles / qualities / score sets | Admin › Settings › Profiles — `/admin/settings/profiles` | Profiles expose ordering, sizes, cutoff and format scores. |
| List media naming | Admin › Settings › Libraries › Naming — `/admin/settings/libraries/naming` | Edit naming explicitly; Recyclarr sync reports unsupported media_naming rather than applying it. |
| Delete custom formats | Admin › Settings › Custom formats — `/admin/settings/formats` | Use the definition editor; no automatic removal of local definitions. |
| State repair / migrate | **not needed** | Theatarr stores sync state and manages its own schema; Recyclarr CLI state maintenance does not apply. |

