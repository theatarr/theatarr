# From the arrs to Theatarr

Use the menu paths below on desktop or in phone **More**. Settings search accepts these page names and keeps each named alias under its own label.
Theatarr libraries replace separate arr instances; you choose each library's feeds and providers and when it may
download. Title pages call them **Libraries**. **Allow new downloads** is in **Settings › Download clients**; each
library's mode and **Allow downloads for this library** are in **Settings › Libraries**. Download notices link to the closed gate's page.
“Not needed” means no equivalent page in Theatarr's current workflow, not that the upstream feature is worthless.

The arr navigation comes from the Sonarr v5 (develop branch), Radarr v6.4.4 and Prowlarr sources: each
`frontend/src/App/AppRoutes.tsx` and `frontend/src/Components/Page/Sidebar/PageSidebar.*`. This is a UI map, not a
claim of Sonarr v5 API parity. Recyclarr has no web pages; its table maps the CLI commands from
`src/Recyclarr.Cli/Console/Commands/`. Prowlarr has History but no Wanted or Activity queue tabs.

## Sonarr v5

| Arr page or tab | Theatarr page (route and menu path) | Difference |
|---|---|---|
| Series | Library › Series — `/series` | One title can have multiple libraries. |
| Series › Add New | Search — `/search` | Choose libraries when adding. |
| Series › Library Import | Admin › Advanced › Sonarr/Radarr migration › Migrate — `/admin/advanced/arr/migrate` | Import the arr inventory into shadow mode; Manual import handles existing files. |
| Calendar | Calendar — `/calendar` | Movies and episodes together. |
| Statistics | **not needed** | No separate library-statistics dashboard; counts and filters are in the library and System Status. |
| Settings | Admin › Settings — `/admin/settings` | Search by task or an arr page name. |
| Settings › Media Management | Admin › Settings › Libraries — `/admin/settings/libraries` | Libraries have independent roots, profiles, indexers and providers; Naming and File management are tabs. |
| Settings › Profiles | Admin › Settings › Profiles — `/admin/settings/profiles` | Quality, language preferences and scores are configured per profile. |
| Settings › Quality | Admin › Settings › Profiles › Edit raw — `/admin/settings/profiles?raw=1` | Quality sizes are edited in the raw profile editor; Quality sync can manage TRaSH size definitions. |
| Settings › Custom Formats | Admin › Settings › Custom formats — `/admin/settings/formats` | Shared definitions; scores are edited on profiles. |
| Settings › Indexers | Admin › Settings › Indexers — `/admin/settings/indexers` | Built-in sources, native catalog and Torznab feeds coexist; each library chooses its feeds. |
| Settings › Download Clients | Admin › Settings › Download clients — `/admin/settings/downloads` | Debrid providers are direct; seedbox clients use Other clients. Allow new downloads remains explicit. |
| Settings › Import Lists | **no equivalent** | Series import lists are not supported; list intake is limited to TMDb movie lists through the Radarr-compatible API. |
| Settings › Connect | Admin › Settings › Notifications — `/admin/settings/notifications` | Webhook targets and Send notifications; Plex lives under Plex & playback. |
| Settings › Metadata | **not needed** | No NFO/sidecar consumer editor in this debrid/Plex workflow; metadata services live in Settings › Metadata & artwork. |
| Settings › Tags | **not needed** | No tag-based routing page; library indexer lists and per-indexer provider allowlists make routing explicit. |
| Settings › General | No General page: provider account sync is on Download clients (`/admin/settings/downloads#provider-sync`), the artwork cache on Metadata & artwork (`/admin/settings/metadata#artwork-cache`), other libraries' folders on Torrents (`/admin/torrents#scan-roots`) | Accounts/API keys are under Users & access, deployment flags remain outside the UI. `/admin/settings/general` redirects. |
| Settings › UI | **not needed** | No separate UI settings page; theme is in the account menu, display choices are beside their views. |
| Settings › Metadata Source | Admin › Settings › Metadata & artwork — `/admin/settings/metadata` | TMDb/TVDB and anime metadata services. |
| System › Status | Admin › System › Status — `/admin` | Health and readiness across the single service. |
| System › Tasks | Admin › System › Tasks — `/admin/system/tasks` | Jobs and failures, rather than one scheduler per arr instance. |
| System › Backups | Admin › System › Backup — `/admin/system/backup` | Full Theatarr backups and restore plus separate torrent-list backups. |
| System › Updates | **not needed** | Deploy a new Theatarr binary/container; there is no in-app updater. |
| System › Events | Admin › System › Errors — `/admin/system/errors` | Persistent error inbox; ordinary runtime messages are in Logs. |
| System › Log Files | Admin › System › Logs — `/admin/system/logs` | Recent in-memory logs; use container/service logging for older files. |
| System › Update Log Files | **not needed** | No in-app updater; inspect deployment/container logs. |
| Activity › Queue | Activity › Queue — `/activity` | Downloads and imports; each action follows the account's permissions. |
| Activity › History | Activity › History — `/activity/history` | Grabs, imports, failures and search decisions; admin decision links explain outcomes. |
| Activity › Blocklist | Activity › Blocklist — `/activity/blocklist` | Requires See files; provider deletes remain separate. |
| Wanted › Missing | Wanted › Missing — `/wanted` | Admin-only read-only automatic queue with waiting reasons; no Search all button. |
| Wanted › Cutoff Unmet | Wanted › Cutoff unmet — `/wanted/cutoff` | Read-only upgrade queue; library and global gates remain visible. |

## Radarr

| Arr page or tab | Theatarr page (route and menu path) | Difference |
|---|---|---|
| Movies | Library › Movies — `/movies` | Multiple libraries per movie. |
| Movies › Add New | Search — `/search` | Choose libraries when adding. |
| Movies › Import Library | Admin › Advanced › Sonarr/Radarr migration › Migrate — `/admin/advanced/arr/migrate` | Migrate arr inventory into shadow mode; Manual import handles files. |
| Movies › Collections | Library › Collections — `/collections` | Managers and admins. Targets are libraries; the arrs' monitored collections are carried over at each Radarr import. |
| Movies › Discover | Home — `/` | Discovery rows and search share one library for every user. |
| Calendar | Calendar — `/calendar` | Movies and episodes together. |
| Settings | Admin › Settings — `/admin/settings` | Search by task or an arr page name. |
| Settings › Media Management | Admin › Settings › Libraries — `/admin/settings/libraries` | Libraries have independent roots, profiles, indexers and providers; Naming and File management are tabs. |
| Settings › Profiles | Admin › Settings › Profiles — `/admin/settings/profiles` | Quality, language preferences and scores are configured per profile. |
| Settings › Quality | Admin › Settings › Profiles › Edit raw — `/admin/settings/profiles?raw=1` | Quality sizes are edited in the raw profile editor; Quality sync can manage TRaSH size definitions. |
| Settings › Custom Formats | Admin › Settings › Custom formats — `/admin/settings/formats` | Shared definitions; scores are edited on profiles. |
| Settings › Indexers | Admin › Settings › Indexers — `/admin/settings/indexers` | Built-in sources, native catalog and Torznab feeds coexist; each library chooses its feeds. |
| Settings › Download Clients | Admin › Settings › Download clients — `/admin/settings/downloads` | Debrid providers are direct; seedbox clients use Other clients. Allow new downloads remains explicit. |
| Settings › Import Lists | **no separate page** | The Radarr-compatible API supports TMDb movie lists. Settings › Metadata & artwork › Refresh schedules (`/admin/settings/metadata#refresh`) controls timing only. |
| Settings › Connect | Admin › Settings › Notifications — `/admin/settings/notifications` | Webhook targets and Send notifications; Plex lives under Plex & playback. |
| Settings › Metadata | **not needed** | No NFO/sidecar consumer editor in this debrid/Plex workflow; metadata services live in Settings › Metadata & artwork. |
| Settings › Tags | **not needed** | No tag-based routing page; library indexer lists and per-indexer provider allowlists make routing explicit. |
| Settings › General | No General page: provider account sync is on Download clients (`/admin/settings/downloads#provider-sync`), the artwork cache on Metadata & artwork (`/admin/settings/metadata#artwork-cache`), other libraries' folders on Torrents (`/admin/torrents#scan-roots`) | Accounts/API keys are under Users & access, deployment flags remain outside the UI. `/admin/settings/general` redirects. |
| Settings › UI | **not needed** | No separate UI settings page; theme is in the account menu, display choices are beside their views. |
| System › Status | Admin › System › Status — `/admin` | Health and readiness across the single service. |
| System › Tasks | Admin › System › Tasks — `/admin/system/tasks` | Jobs and failures, rather than one scheduler per arr instance. |
| System › Backup | Admin › System › Backup — `/admin/system/backup` | Full Theatarr backups and restore plus separate torrent-list backups. |
| System › Updates | **not needed** | Deploy a new Theatarr binary/container; there is no in-app updater. |
| System › Events | Admin › System › Errors — `/admin/system/errors` | Persistent error inbox; ordinary runtime messages are in Logs. |
| System › Log Files | Admin › System › Logs — `/admin/system/logs` | Recent in-memory logs; use container/service logging for older files. |
| Activity › Queue | Activity › Queue — `/activity` | Downloads and imports; each action follows the account's permissions. |
| Activity › History | Activity › History — `/activity/history` | Grabs, imports, failures and search decisions; admin decision links explain outcomes. |
| Activity › Blocklist | Activity › Blocklist — `/activity/blocklist` | Requires See files; provider deletes remain separate. |
| Wanted › Missing | Wanted › Missing — `/wanted` | Admin-only read-only automatic queue with waiting reasons; no Search all button. |
| Wanted › Cutoff Unmet | Wanted › Cutoff unmet — `/wanted/cutoff` | Read-only upgrade queue; library and global gates remain visible. |

## Prowlarr

| Arr page or tab | Theatarr page (route and menu path) | Difference |
|---|---|---|
| Indexers | Admin › Settings › Indexers — `/admin/settings/indexers` | One indexer manager shared by libraries. |
| Indexers › Stats | **not needed** | No separate indexer-statistics dashboard; per-source status and tests are beside Settings › Indexers (`/admin/settings/indexers`). |
| Search | Search — `/search` | Find a title, then use its Releases action; no unrestricted all-indexer raw search page. |
| History | Admin › Advanced › Decision journal — `/admin/advanced/journal` | Search and download decisions, not a full Prowlarr request log. |
| Settings | Admin › Settings — `/admin/settings` | Search by task or an arr page name. |
| Settings › Indexers | Admin › Settings › Indexers › VPN — `/admin/settings/indexers/vpn` | Optional VPN guard and catalog settings replace proxy management; native definition reviews are under Reviews. |
| Settings › Apps | Admin › Settings › Connected apps — `/admin/settings/apps` | Theatarr is its own indexer manager, so there is no indexer-to-arr sync; Connected apps exposes Radarr- and Sonarr-compatible APIs to Seerr and other clients. |
| Settings › Download Clients | Admin › Settings › Download clients — `/admin/settings/downloads` | Debrid providers are direct; seedbox clients use Other clients. Allow new downloads remains explicit. |
| Settings › Connect | Admin › Settings › Notifications — `/admin/settings/notifications` | Webhook targets and Send notifications; Plex lives under Plex & playback. |
| Settings › Tags | **not needed** | No tag-based routing page; library indexer lists and per-indexer provider allowlists make routing explicit. |
| Settings › General | No General page: provider account sync is on Download clients (`/admin/settings/downloads#provider-sync`), the artwork cache on Metadata & artwork (`/admin/settings/metadata#artwork-cache`), other libraries' folders on Torrents (`/admin/torrents#scan-roots`) | Accounts/API keys are under Users & access, deployment flags remain outside the UI. `/admin/settings/general` redirects. |
| Settings › UI | **not needed** | No separate UI settings page; theme is in the account menu, display choices are beside their views. |
| Settings › Development | **not needed** | No developer-settings page; diagnostics use System and deployment configuration. |
| System › Status | Admin › System › Status — `/admin` | Health and readiness across the single service. |
| System › Tasks | Admin › System › Tasks — `/admin/system/tasks` | Jobs and failures, rather than one scheduler per arr instance. |
| System › Backup | Admin › System › Backup — `/admin/system/backup` | Full Theatarr backups and restore plus separate torrent-list backups. |
| System › Updates | **not needed** | Deploy a new Theatarr binary/container; there is no in-app updater. |
| System › Events | Admin › System › Errors — `/admin/system/errors` | Persistent error inbox; ordinary runtime messages are in Logs. |
| System › Log Files | Admin › System › Logs — `/admin/system/logs` | Recent in-memory logs; use container/service logging for older files. |

## Recyclarr (CLI)

| Arr page or tab | Theatarr page (route and menu path) | Difference |
|---|---|---|
| Sync | Admin › Settings › Quality sync — `/admin/settings/quality-sync` | Preview then run; held changes are approved under Reviews › Quality updates. Local edits survive managed-field sync. |
| Config create / list templates | Admin › Settings › Profiles — `/admin/settings/profiles` | Create a profile from a TRaSH preset; Recyclarr YAML can also be used as a sync source. |
| Config list local | Admin › Settings › Quality sync — `/admin/settings/quality-sync` | Configured sync sources replace a directory of target-instance configs. |
| List custom formats / custom format groups | Admin › Settings › Custom formats — `/admin/settings/formats` | Definitions are shared and profile scores are separate. |
| List quality profiles / qualities / score sets | Admin › Settings › Profiles — `/admin/settings/profiles` | Profiles expose ordering, sizes, cutoff and format scores. |
| List media naming | Admin › Settings › Libraries › Naming — `/admin/settings/libraries/naming` | Edit naming explicitly; Recyclarr sync reports unsupported media_naming rather than applying it. |
| Delete custom formats | Admin › Settings › Custom formats — `/admin/settings/formats` | Use the definition editor; no automatic removal of local definitions. |
| State repair / migrate | **not needed** | Theatarr stores sync state and manages its own schema; Recyclarr CLI state maintenance does not apply. |

