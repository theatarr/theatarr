# Database-backed settings

## Settings contract

The database is the only runtime settings source. There are two editing surfaces: the inline Settings panels and
one **Edit as YAML** editor per section. The inline panels use the section schema and JSON PUT API; the YAML editor
uses the same validation, generation conflict and confirmation
protocol. Section switches and library roots remain in their dedicated editors.

`serve` never reads root policy YAML or seeds settings. No generation means a fresh install: Setup leads, all
switches start off, and the first settings save creates a generation. Offline commands that need settings refuse
an empty database with `no settings yet: run Setup or theatarr settings import`. Preflight can still describe a fresh
install. On an initialized but invalid database it retains validation FAIL items and runs local checks using
force-off switches and enabled sources; it skips outbound tests until validation succeeds.

To import policy files deliberately:

```sh
theatarr policy validate /config/policy
theatarr settings import -db /data/theatarr.db -policy /config/policy          # preview, no settings write
theatarr settings import -db /data/theatarr.db -policy /config/policy -confirm # reviewed import
```

The command is for the first seed of a database and for recovering a database without complete settings, with the
service stopped. It refuses a database whose current generation already has all 13 settings sections with
`this database already has settings: import from Advanced › YAML settings › Import & export in the running service`:
that page imports through the running service's continuity checks and publication. Generation 0 and a partial
(older) generation still import from the command line.

Every import, on the command line, on the Import & export page and in the recovery of an older, partial generation, builds one
candidate the same way:

- the records (indexers, providers, budgets, versions, views and the quality settings) come from the files;
- the 13 settings sections are kept as stored; only sections the database lacks are filled from the files;
- switches are kept as stored (a new database: all off), as are source enabled flags and priorities;
- the candidate is validated after that merge, with the stored settings as its base.

The preview (CLI and the page's JSON, `sections_note` and `sections_from_files`) says this in one line:
`Settings sections are kept as stored; filled from the files: provider_sync.` (or `none is filled from the files`).
An import over an existing generation needs all five record groups (indexers, providers, budgets, versions and
views; an empty map reads as absent), so an omitted group can never delete stored records. On a new database every
version root is checked against the mount, the trash and the other libraries' roots (symlinks resolved); a root may
sit anywhere else. Database backup and restore are unchanged;
exports without secrets are review copies, not backups.

The policy directory (serve's `-policy`, `THEATARR_POLICY`, default `/config/policy`) is not a settings
source. It is where relative data paths in the stored settings resolve (`metadata.overrides_dir`, with franchise and
anime override files) and what backups archive. Imports store `overrides_dir` as written, so a relative path keeps
resolving under whatever directory serve is given. Commands, which take no `-policy`, use `THEATARR_POLICY` or the
default. Custom-format JSON is materialized in database records, so those files are import inputs. TRaSH preset/sync
files and other configured external data paths remain external data.

Unset or `db` settings-source options are accepted for compatibility. `yaml` refuses startup with
`the YAML settings source is no longer read; run theatarr settings import`.
Other settings-dependent commands take their settings from `-db`; `preflight -policy` is removed. Directory
inspection/import tools retain directory inputs.

`THEATARR_FORCE_OFF` forces Pause on (`pause`, `router` and `library` all do) and notifications off (`notify`). Saves
cannot override it. Turning Pause off, making a library Live, removing a library, an address change that keeps a
stored key, and an arr import into a Live library require confirmation; other changes save at once ("Confirmations"
below).


Agents can read sections and views and save them through [MCP](../mcp.md), with the same validation and confirmation
tokens. MCP writes are audited as "via MCP"; a change that needs confirmation is confirmed with the key's own token
("Who confirms" below).

## Settings pages

Settings follows the arr layout: Libraries, Profiles, Custom formats, Quality sync, Indexers, Download clients,
Notifications, Plex & playback, Metadata & artwork and Connected apps (`/admin/settings/general` redirects to the
pages below, anchor by anchor). The navigation and routes are listed in
[UI §2](ui.md#2-information-architecture). YAML settings, settings history, request limits and
Sonarr/Radarr instances are under Advanced. Older URLs redirect, keeping their query strings and anchors.

A library is **Watch only** or **Live**; there is one **Pause** in the admin header; **Send notifications** is an
ordinary field of the Notifications page (`notify.enabled`). Connected apps is the one editor for the compatible endpoints: each app's libraries and profiles (named
checkboxes), path, aliases and API key, with each library's state in the list. Libraries links there. The editor uses
`PUT /settings/view-bindings/{key}`.

**Show advanced settings** is one app-wide flag, off by default, stored as `theatarr.settings.advanced` in localStorage
(with unavailable storage tolerated). It controls both advanced panels and fields. **Find a setting** on Settings
home searches pages, tabs, panels and schema fields (including dotted technical paths), plus arr aliases such as
Recyclarr, Prowlarr, root folder, Seerr and rclone. Named records saved in a section (a metadata source such as
`tmdb`, a client, a notification target, a library's Plex mapping) are indexed by name with their field labels, so
"tmdb key" opens Metadata & artwork › Metadata services with the tmdb record unfolded (`?open=metadata.sources.tmdb`). Results are limited to 12; Enter
opens the first. Anchors scroll and highlight the panel. An arr page that has a Theatarr page (Prowlarr's Apps:
Connected apps) points there, not at a "no separate page" entry.

`settings-map.test.ts` compares this ownership against the real schema fixture: each top-level field appears exactly
once. `section-panel.test.tsx` exercises partial writes, credentials, 422, 409, YAML, defaults and advanced visibility.

| Page / tab | Panel (anchor) | Data | Fields | Advanced only |
|---|---|---|---|---|
| Libraries › File management | Mount and folders (`#mount`) | `library` | `mount` |  |
| Libraries › File management | Broken link checks (`#links`) | `library` | `relink_interval`, `dangling` |  |
| Libraries › File management | Trash (`#trash`) | `library` | `trash` |  |
| Libraries › File management | Media inspection (`#inspection`) | `library` | `probe`, `sample` | Yes |
| Indexers › VPN | VPN guard (`#vpn`) | `egress` | `check_url`, `vpn_ips`, `vpn_cidrs`, `interval`, `max_age`, `timeout`, `require_vpn_for` |  |
| Indexers › Catalog | Indexer catalog and Prowlarr (`#catalog`) | `cardigann` | `definitions`, `flaresolverr_url`, `prowlarr` |  |
| Indexers › RSS | RSS feed checks (`#rss`) | `shadow` | `rss`, `rss_interval` |  |
| Download clients › Other clients | Other download clients (`#clients`) | `clients` | `"" (root keyed object)` |  |
| Download clients › Behaviour | What to grab (`#grabbing`) | `router` | `cached_only`, `alldebrid_delete_uncached` |  |
| Download clients › Behaviour | Automatic searches (`#searches`) | `router` | `wanted` |  |
| Download clients › Behaviour | Cache checks (`#cache-checks`) | `router` | `cache_evidence` |  |
| Download clients › Behaviour | Retries and timing (`#timing`) | `router` | `cached_grace`, `stalled_after`, `downloading_timeout`, `ready_timeout`, `miss_ttl`, `poll_interval`, `max_candidates`, `trackers` | Yes |
| Notifications | Destinations (`#targets`) | `notify` | `targets`, `digest_at` |  |
| Notifications | Delivery (`#delivery`) | `notify` | `allow_hosts`, `budget`, `retries`, `poll` | Yes |
| Plex & playback | Plex server (`#plex`) | `library` | `plex` |  |
| Plex & playback | Plex library for each Theatarr library (`#plex-libraries`) | `library` | `versions` |  |
| Plex & playback | Playlists (`#playlists`) | `playlists` | `enabled`, `orders`, `routes`, `skip_optional`, `name`, `franchises`, `interval` |  |
| Metadata | Metadata services (`#sources`) | `metadata` | `sources` |  |
| Metadata | Refresh schedules (`#refresh`) | `metadata` | `art`, `series`, `lists` |  |
| Metadata | Overrides folder (`#overrides`) | `metadata` | `overrides_dir` | Yes |
| Download clients › Debrid providers | Provider account sync (`#provider-sync`) | `provider_sync` | `interval` |  |
| Metadata & artwork | Artwork cache (`#artwork-cache`) | `ui` | `images` |  |
| System › Backup | Torrent-list backups (`#torrent-backups`) | `torrents` | `backup` |  |
| Torrents (Admin › Torrents) | Other libraries’ folders (`#scan-roots`) | `torrents` | `scan_roots` |  |
| Advanced › Sonarr/Radarr instances | Sonarr and Radarr instances (`#arr`) | `import` | `arr` |  |

### Form details

- **Indexer form:** a new indexer shows Service, Enabled, API key, Name and its required fields (the URL) before
  Show advanced; only tuning is under it. Providers keep Name and URL under Show advanced (the service fills them).
- **Indexer provider allowlist:** "Any provider chosen by the library" (saves an empty list) or "Only these
  providers" with a checkbox per provider key; it saves the same `providers` list the comma field did. A saved key no
  provider has stays listed, ticked and marked "(not found)".
- **Size limits:** the profile editor has min / preferred / max per allowed quality (groups give one row per
  member; qualities that already have a limit are listed too), in MB per minute. It edits `config.sizes` and shows
  what the engine applies (`internal/decide/build.go`): a quality with no entry uses the arr default definition, shown
  as the placeholder ("95 default"); an entry replaces all three values, max 0 being no limit and preferred 0 no
  preferred size unless `preferred_zero` keeps the arr's preferred size of 0. A blank field is the default, `none` is
  no limit (Max) or no preferred size, and a typed preferred 0 sets `preferred_zero`. Editing one field writes the
  row's other effective values with it (Sonarr WEBRip-1080p with only Max 200 saves `{min: 4, preferred: 95, max:
  200}`); a row back at every default loses its entry. The defaults come from
  `web/src/pages/admin/settings/profiles/size-defaults.json`, which `TestWebSizeDefaultsMatchEngine` keeps equal to
  the engine's table. The defaults are those of the flavour the engine builds the profile in, one per kind of library
  using it (series: Sonarr, others: Radarr; a new profile from a preset: the preset's). An unused profile asks
  "Defaults for: Movies / Series" first (not saved; no fields until picked). A profile used by both movie and series
  libraries shows its sizes read-only, as one map cannot hold both flavours' defaults, and links to the raw editor. A
  typed value that is not a size (a decimal comma is read as a point) stays shown with an error and blocks saving.
  The raw "Size limits" JSON field stays in the raw profile editor (`?raw=1`).
- **Custom formats:** one list of definitions. Standalone formats have Edit definition and Export arr JSON in their
  row; Standalone formats below keeps Add format and lists only a stored definition the merged list leaves out (two
  sharing a TRaSH ID and flavour merge into one row carrying the last one's key), so every definition keeps Edit,
  Export and Delete; the page filter applies to those rows too. Each score chip links to its profile (`/admin/settings/profiles?edit=`), and a profile's score
  rows link to `/admin/settings/formats?filter=<key>`; the filter matches names and profiles as text and a TRaSH ID or
  format key exactly, so scores keyed by TRaSH ID find their format.
- **Settings history** opens at the newest change: `GET /settings/audit?order=desc&before=<id>` (Older follows
  `next`, Newer goes back). Without `order` the endpoint still pages oldest first with `after`. The other order's
  cursor (`before` without `order=desc`, `after` with it) and a malformed cursor are a 400.
- **Moved panels** sit before content that loads later (Provider account sync above provider status, Other
  libraries' folders above the torrent list), so `#provider-sync` and `#scan-roots` links stay on target.
- **Field rows:** labels wrap and Reset is one word, named "Reset <field>" for screen readers, so Cache checks fits at
  400 px.

## Global switches

The stored record is one flag, `{"paused": bool}` (`setting_global_switches`). Both runtime switches are derived from it
when settings load: `router.enabled` and `library.enabled` are each `!paused`, so every gate reader stays as it was.
Pause stops searches, new downloads and library changes (links, renames, deletes, Plex updates) for every library.
Downloads already at a provider finish there and are imported when Pause is turned off. A fresh install starts paused.
Notifications are not part of it: `notify.enabled` is a field of the `notify` section like any other.

`PUT /settings/switches` takes `{generation, paused}`. The removed fields `router_enabled`, `library_enabled` and
`notify_enabled` are refused with 400 ("use paused"). Pause on needs no confirmation. Pause off answers with the 422
confirmation flow, or `POST /settings/switches/confirm` previews it: "Turn Pause off: Live libraries search, download
and link again: A, B. Waiting downloads are imported." The token binds the entire candidate and next generation.
Turning Pause off under an environment override still needs confirmation. Whole-policy imports and clients omitting
the record preserve it; YAML imports never turn Pause off.

`THEATARR_FORCE_OFF` takes a comma list: `pause`, `router` and `library` each force Pause (both runtime switches
closed; reported as `pause` in `forced_off` and `pause_forced`), `notify` forces notifications off. Other names are
warned about at startup and in preflight and change nothing.

Migration 00122 rewrote existing installs: the record became `{"paused": true}` unless both old switches were on; a
Live library whose `grab` was not true (a missing `grab` was false: the field was omitted when false and had no
default) became Watch only, and every library lost `grab`; `notify_enabled` true set `$.enabled` on the `notify`
section (inserting the row if missing), false removed it.

Each save publishes a runtime generation without a restart. New routes and library operations use the new gates; the router's final-write check also refuses further adds from retired generations. A provider request already sent is not canceled, and existing downloads continue to be polled as with the router off. Library operations already in flight keep their captured generation; subsequent operations do the same dry run as startup with the switch off. Notification recording, tests and delivery sending read the runtime switch; queued deliveries are dropped while off. Health and arr/native views use the active generation. Pause is not part of history: startup fallback takes it from the stored head (paused when there is none), so recovery cannot turn Pause off. Versions get the same treatment: a recovered version stays active only while the head's version is active, and a version the head no longer has is recovered as shadow. Anything else the fallback would allow that the head does not (the confirmation effects of going from the head to it: a Live library that follows an arr library where the head's does not, an arr import into a grabbing library the head does not map, or a library the head has, with links or a follows relationship, that the fallback lacks) refuses the fallback, and the start fails with both reasons instead of running looser settings than the saved ones. An address the fallback gives a provider or indexer with its kept key is not checked: that generation was published with it (`TestFallbackEffectsIgnoreOwnAddress`). While a fallback runs, the health check `SettingsFallbackCheck` is an error naming the running and the stored generation (Admin › System › Status lists it under Needs attention, linking to Settings, whose banner offers the reload); it is OK again once the head is loaded (`TestSettingsFallbackHealth`). It compares the running generation with the stored head (one row), so it can also flag, for a moment, a save committed but not yet published.


## Sections and views

**Section editing.** The inline Settings panels are the forms. The section list opens the raw YAML editor directly with **Edit as YAML**. The inline panels use the shared schema and section API.

Forms keep the loaded config intact: an untouched default is not inserted, leaf Reset removes only that key,
and unknown keys survive edits. Objects and named-record collections have no Reset. Default-on switches show
the effective default and a click writes an explicit false; the wanted-loop default follows active versions.
Duration presets compare parsed values (`6h0m0s` selects 6h); Off removes the key for off-by-default
settings, including the Torrents schedule control. The router’s stalled and no-progress timeouts retain
explicit `0s` for Off: omitting them restores their nonzero runtime defaults. Weekly is `168h` because
policy durations do not accept days. Byte sizes initially use the largest exact GB/MB/KB unit (bytes when
none fits). Configured nullable sections offer **Remove this section**, which submits `config: null` through
the same confirmation flow. Credentials are never pre-filled: Set/Replace and Clear send write-only secret
updates. Renaming a record with stored credentials requires re-entering them; a masked notification
destination also needs its full URL. Rename moves draft replacements and clears to the new path. Empty or
duplicate names are refused inline. Server field errors appear beside the field or indexed list row, tied to the
control with `aria-describedby` like its help. Edited fields are marked Changed with Undo back to the saved
value; Reset goes to the default. A sticky bar counts the unsaved changes with Discard and Save; record sheets
and the Libraries editor count them in the footer, and Discard clears stale validation. Saves
use the same policy validation, 422 confirmation token and explicit reload after a 409.

Notifications › Destinations: each saved destination has **Send test**, which posts a test message to the saved
target at once (`POST /settings/notify/targets/{name}/test`, admin; `{ok: true}` or `{ok: false, error}`, and an
error while `notify.enabled` is off). Unsaved edits are not used, so save first. A destination's **Message format**
(`format`) is empty for Theatarr's JSON, `discord` for Discord messages or `ntfy` for ntfy messages (the URL is the
ntfy server address followed by the topic; an access token goes in **Authorization header**), and **Daily digest
time** (`digest_at`, local `HH:MM`, default 09:00) sets when the Discord and ntfy digest goes out
(docs/design/devices.md).

`GET /settings/schema` includes `sections` and `section_nullable`, built by reflection over policy types.
Help comes from Go field comments: `go generate ./internal/web` parses policy source and writes the embedded
`sectionfields_gen.json`. The generator rejects undocumented reachable fields. `TestSectionFieldDocsCurrent`
catches stale output; `TestSectionSchemaCoverage` independently checks every policy field, owned-field
exclusions and credential paths. Generated help replaces Go field identifiers and expands “environment
variable”; `TestSectionHelpHasNoGoFieldNames` checks every reachable field name without exceptions.
`TestSectionSchemaFixture` checks the real schema fixture used to render all 13 forms in web tests
(regenerate with `go test ./internal/web -run TestSectionSchemaFixture -update-section-fixture`).
`TestSectionDefaultTrue` checks runtime defaults against annotations. Limits and enums live in the
annotation table in `internal/web/sectionschema.go`, beside the reflection code. Durations use seconds for
schema bounds; byte limits use bytes. `zero_allowed` distinguishes Off from a positive minimum. The schema
uses full secret paths with `*` for a record name; the existing section PUT API still takes section-relative
paths (for example `mount.rclone_rc.password` in the library section).

| Section | Applies |
|---|---|
| `metadata` (TMDb, TVDB, image hosts, art, series, lists) | live: each generation builds its metadata services (TVDB and TMDb clients, series refresh, art, movie intake, search and add, Plex sign-in, the arr views' lookups), reusing the previous generation's where their inputs (settings and key hashes) did not change; the art, series, TVDB updates and list loops restart only when theirs did |
| `library` (mount, link_path, all_dir, trash, dangling roots, zurg_log/zurg_api, rclone_rc, Plex with now_playing/prewarm/prewarm_rate/prewarm_max_size, probe, sample) | live: imports, scans and Plex calls started after the save use the new values; the library loop (zurg log watch, import pass, dangling report, relink sweep, naming sync) restarts when its schedule or zurg log changes; the now playing tracker restarts when Plex, zurg_api, the mount paths, `plex.prewarm`, `plex.prewarm_rate`, `plex.prewarm_max_size` or `mount.rclone_rc` change ([nowplaying.md](now-playing.md)) |
| `notify` (targets, events, formats, allow-hosts, retry, digest time) | live: deliveries, tests and the poll read the generation in force |
| `clients` (seedbox download clients) | live: the gateway is rebuilt per generation |
| `egress` (VPN guard: check URL, VPN IPs/CIDRs, require_vpn_for) | live: the gateway is rebuilt per generation |
| `torrents` (scan roots, scheduled backups) | live: each scan, backup and the 10-minute backup check read the generation in force |
| `cardigann` (catalog, flaresolverr_url, Prowlarr reader) | live: the catalog is on exactly when `definitions` is set (Indexers › Catalog says which; **Turn the catalog on** fills in `prowlarr/indexers` for the panel's Save, and the add-indexer sheet's **Turn on the catalog** saves it directly); a new install's first settings save turns it on (Prowlarr/Indexers); the catalog is made when a save first sets `definitions`; source, FlareSolverr and the Prowlarr reader are read at use; the refresh loop restarts (and reads a changed source at once) when `definitions` changes; indexers already built keep their engine and sessions |
| `shadow` | live: RSS follows each generation (no restart) |
| `playlists`, `ui`, `provider_sync`, `router` (options) | live: rebuilt per generation; the playlist, provider sync, download poll and wanted loops restart when their interval changes |
| views (path, flavour, versions, profiles, API key) | live: the HTTP routes are rebuilt per generation |

**Removed settings.** `release_push`, `indexers.<key>.private_delivery` and `notify.private_delivery` are not
settings: privacy is a label only. `indexers.<key>.arr_names` stays as a plain alias list: other
names for the indexer, as arr history or a release push gives them, matched without regard to case by
`Policy.IndexerByName` (offers, `/release/push`); each name is listed once and is no other indexer's key. Configs that
still have them load: a stored `release_push` section row is ignored (`settings.removedSections`), and policy YAML has
the removed keys stripped before the strict decode, each named in `Policy.Ignored` and logged at serve start ("policy setting
ignored"). `indexers.<key>.providers` is an optional allowlist of the providers the indexer's releases may go to
(empty: all; each must be a configured provider, no duplicates).

**Live sections.** Every section applies on save; `PUT /settings/sections/{key}` answers
`restart_required: false` and the editor shows "live". Short-lived uses (requests, jobs, imports, searches) capture
the generation they start on. The long-running loops run as workers (`cmd/theatarr/serveliveworkers.go`): each has a key
computed from the settings it schedules with, and when a published generation changes that key the worker is stopped,
waited for and started again on the new generation; a save that leaves its key alone (any unrelated section) restarts
nothing. The periodic VPN egress check is one of them (key: `egress.interval` while an `egress` section exists), so
an `egress` section added after startup gets its periodic checks, a new interval takes effect at once, and removing
the section stops them; each pass checks the gateway in force. One goroutine supervises each worker, so two runs never overlap and quick saves collapse into one restart on
the newest generation. A worker that fails to start on new settings, or does not stop within two minutes, shows in
health as `LiveSettingsCheck` and the log; a failed one is retried at the next save. A reload keeps the safety
rules: the switches stay where they are (no worker turns grabbing, the library or notifications on), confirmation
tokens still apply, library writes stay inside version roots, and indexer traffic goes through the gateway of the
generation in force, whose VPN guard is active before the generation is published.

**Concurrent editors.** The section and view sheets keep the section or view and the generation they opened with;
a refetch (window focus, another save) does not move them. A save or delete after someone else's change therefore
answers 409, shown in the sheet with "Reload, discard my changes" (the draft becomes the current value) and "Reload,
keep my changes" (the draft is saved over the current generation). A view deleted elsewhere stays an edit of that view
and says so on reload instead of turning into a new view (`sections.test.tsx`, `views.test.tsx`).

Not moved: `library.enabled` and `router.enabled` follow Pause ("Global switches" above; `notify.enabled` is an
ordinary field of the notify section), and
`library.versions.*.roots` stays with the version editor (a section save naming them is refused with a pointer to the
owner). What stays on the command line or in the environment is what is needed before the database opens or
outside the policy: the database path, the policy directory (data files and backups), the listen address,
`THEATARR_FORCE_OFF`, the TRaSH checkout (`THEATARR_TRASH`), log and backup directories, and credentials supplied as
environment variables, which remain the fallback while no credential is stored.

**Editing.** `GET /settings/sections` lists each section as `config` and `yaml`, `live`, and `secrets` (credential
path, `set`, and the `*_env` fallback name). `PUT /settings/sections/{key}` takes `generation`, the whole section as
`yaml` or `config`, `secrets` (path → value) and `clear_secrets`, and answers `restart_required`. Validation is the
policy files' (strict field names, the same `Validate`). Views: `POST /settings/view-bindings` creates one (key,
flavour, path, versions, profiles, `api_key`), `PUT /settings/view-bindings/{key}` updates the fields it
names (versions and profiles together; `api_key` or `clear_api_key`), `DELETE /settings/view-bindings/{key}?generation=`
removes it and its key. Settings › Connected apps is the editor for all of it.

**Credentials** are write-only: the `*_env` fields of a section (`plex.token`, `sources.tmdb.api_key`, a client's
`password`, ...) and a view's API key are stored in `setting_secret` and shown only as set or not. The runtime
policy names them by an internal `THEATARR_SETTINGS_SECRET_*` name that only that generation's own policy answers
(`Policy.Getenv`; each generation carries its values, nothing is installed process-wide, so preparing a candidate
or falling back never changes what the generation in force resolves); it is never read from the process environment
and never exported. A changed section or record whose `*_env` field names a `THEATARR_SETTINGS_SECRET_*` name is
refused on save and preview: it could only point at another record's stored secret (a webhook header carrying the
Plex token). A stored credential wins
over the environment variable the section names; clearing it falls back to that variable. Values never appear in
responses, audit rows, confirmations, logs or exports; the export lists `sections/<key> <path> credential: set`.
Notification target URLs (`notify.targets.<name>.url`) carry their webhook token in the path or query, so they are
write-only too: `GET /settings/sections` (and MCP `settings_sections`), the audit and the export show only
`scheme://host/[redacted]`. Every other URL in a section is shown by the same masker (`settings.MaskSectionConfig`)
with its credential-like parts as `********`: user information, a credential query parameter (`X-Plex-Token`,
`apiKey`, `rss_key`, ... compared without case or separators) and a token-like path segment. A save
that sends a mask back unchanged at the same place keeps the stored URL; a mask at another place (another target
name) or with another scheme or host is refused, so a stored URL never moves to another target or host. Migration
00083 replaced the URLs in older audit rows with `[redacted]`. Policy validation refuses credential query
parameters in configured URLs by the same name rule (`secrets.CredentialQueryName`): put them in the `*_env` fields.

**Confirmations.** A save answers 422 with effects and a token (the shared dialog) only when it makes a library Live
(or creates one Live), turns Pause off, removes a library, changes a provider's or indexer's address
while it keeps its stored key, or maps an arr import into a Live library. Every other change, other addresses, folders, the VPN guard, `router.cached_only`, profiles, formats,
naming, indexers, providers, views, notifications and credential names included, saves at once after validation.
Validation itself is unchanged (`TestEverydayLiveEditsSaveDirectly`, `TestKeptConfirmations`).

Settings editors attach the confirmation token centrally on every retry of a refused save. The retry keeps the reviewed request body and generation and sends the token only in
`X-Confirmation-Token`. Canceling leaves the change unsaved.
The dialog shows the server's effects unchanged, grouped by the library each names (effects without one first, under
"This change"), with the libraries affected and, in the Libraries editor, the other libraries a change also
affects; Cancel is focused ([UI](ui.md#settings)).
When the service is running an older generation after a startup fallback, shared confirmation dialogs explain
that saving can also activate stored settings that are not running yet.


**Who confirms.** A signed-in user (a browser session) and a user's API key, MCP on either included, confirm the
same way: the 422 carries the effects and a token, the confirm endpoints issue one, and the same request with the
token (`confirmation_token` or `X-Confirmation-Token`) applies it. A session's tokens are bound to its user
(`settings.WithConfirmer`, "user:<id>"); a key's to that key (`settings.WithKeyConfirmer`, "key:<id>"), so a key's
token is refused for another key, for a session and after the key is revoked, and a session's for a key. This covers
every flow: records (an address with a kept key), sections (an arr import), versions (activation and removal), Pause, `orphan_active_downloads`, migration apply steps and torrent delete and restore. Torrent delete and restore store no
token: theirs is an HMAC, under a random per-process key, of the confirmer (`settings.Confirmer`) and the plan digest,
so only a preview gives it and only the same caller can use it; a restart means preview again
([torrents.md](torrents.md)). Permissions still decide what a key
reaches: it carries its owner's, so a manager's key gets 403 on admin routes. A save confirmed by a key is audited as
the key ("admin (api key NAME)", plus " via MCP") with ", confirmed by api key NAME". Arr view keys reach only the
arr views and confirm nothing. Any other caller (the web guard's fallback, `settings.WithoutConfirmation`)
gets the effects with `confirm_in_ui: true` and no token.

## Source of truth

The database is the live source of truth. YAML remains a strict import/export format for setup, backup and review. Serve never loads it at start: a database without settings starts in Setup, or is seeded with `theatarr settings import` while the service is stopped (above). After that the policy directory holds only data files and backups until an administrator explicitly imports again. Secrets are entered in the UI, stored in plain text like the arrs and Seerr ("Secrets and sync policy" below), write-only in APIs and represented only as set/unset; YAML exports omit values. Import accepts the existing `*_env` fields and resolves them from the process environment. Policy files with environment references therefore still import, and the stored settings keep no environment indirection.

`policy.Read` parses the root YAML files strictly (merges top-level sections, rejects duplicate keys and unknown fields) and `policy.Load` also validates the whole `Policy`. Neither is on serve's start path: `theatarr serve` loads the stored generation over its `-policy` directory with `databasePolicyBase` (relative data paths such as `metadata.overrides_dir` resolve there, root YAML is not read) and never imports. The files are read only by an explicit import (`theatarr settings import`, Advanced › YAML settings › Import & export), which parses them, fills the sections the database does not keep, and validates the one merged candidate.

## Inventory and runtime consumers

The policy directory is an open set of root YAML files. The example files enumerate the sections: versions,
profiles, formats and format sets, indexers, providers, clients, views, budgets, metadata, import, egress, provider
sync, shadow, library, router, playlists, notify and UI (`examples/policy/*.yaml`; the section fields are in
`internal/policy/policy.go`). Per-title overrides are a separate format under `metadata.overrides_dir`, not root
policy. These files are import input, not live settings: serve and the CLI commands that need settings (`theatarr
search`, `router`, `shadow` and the others using `loadCommandPolicy`) read the stored generation from the
database, over the policy directory for relative data paths, and refuse with "no settings yet" before the first
generation. Only data files such as the overrides are read from the directory at run time.

| Sections | Runtime consumers, rebuilt together per generation |
|---|---|
| `indexers`, `budgets`, `egress` | Gateway allowlist, budget buckets, proxy and egress guard, indexer clients and the search planner; the gateway is the shared outbound path (`cmd/theatarr/serve.go`, `cmd/theatarr/serveindexer.go`, `internal/gateway/doc.go`) |
| `providers`, `provider_sync`, `router`, `clients` | Provider sync registry and clients, router provider adapters, pacing, fallback and adders, as one generation (`cmd/theatarr/serve.go`, `cmd/theatarr/router.go`) |
| `versions`, `profiles`, `formats`, `format_sets`, `shadow` | Decide engines, the RSS sync, acquisition and search, offers, all from the same snapshot (`cmd/theatarr/serve.go`, `cmd/theatarr/decide.go`) |
| `library`, version roots and naming, `playlists` | Library import, relink and Plex gates, root mapping, naming and playlist sync (`cmd/theatarr/servelibrary.go`) |
| `metadata`, `import` | TMDb, TVDB and other metadata sources, arr imports, series refresh, indexer helper lookups and Plex sign-in, with their schedules |
| `views` | Arr-view routes, version exposure and release push; a route identity or path change replaces the route registration (`internal/policy/policy.go` `View`) |
| `notify` | Webhook targets and sender; the outbox and retry state are kept |
| `ui`, metadata `art` and `series` | Art proxy and cache, metadata refresh schedules and clients (`cmd/theatarr/serveart.go`, `cmd/theatarr/servetvmeta.go`) |

## Storage and snapshot contract

`internal/settings`, over `internal/store`, owns persistence, validation, import and export, audit and reload
coordination. `policy.Policy` is the one runtime shape: the loader reads a transactionally consistent database
generation, assembles one complete `*policy.Policy`, attaches its generation and revision and calls `Validate`.
Every mutation validates the whole candidate policy before commit, and a refusal carries the structured
`policy.Errors` paths. Consumers do not interpret rows or keep their own policy copy.

The records are normalized: `setting_indexer` (kind, config, priority, enabled, secret reference),
`setting_provider` (kind, config, order, enabled, pacing, secret reference), `setting_budget` and the shared
`setting_secret`. Typed non-secret config is a JSON payload validated per kind; ownership, enabled state, priority and
secret references are queryable columns. Quality profiles, custom formats, format sets, naming and sync sources,
and versions, roots and view bindings, follow the same pattern (below). Every mutable record has a stable id and
timestamps. `settings_audit` stores the actor, UTC time, generation, entity and key, operation and a redacted
before/after diff, never secret bytes or hashes.

A save writes the generation row and its audit rows in one transaction. The exported policy document is rebuilt
from the typed rows through the one loader. Revision consumers (`Policy.Revision`, in status and the journal) get a
stable content revision per generation, so earlier decisions stay attributable.

## Live changes and safety

Validated snapshots are published through an atomic pointer. The runtime coordinator serializes saves, commits the database changes, builds the replacement services, then publishes a `Runtime` holding the policy and the component handles: gateway with budgets and egress, indexers, provider sync and registry, router, decide engine and acquirer, library and roots, metadata clients, arr views, notify, art, health and the UI summary. A failed preparation leaves both the stored generation and the active runtime unchanged: preparation finishes before the commit.

Each request, search, RSS read, route attempt, import, poll pass and offer decision captures one runtime pointer at its start and uses it through completion. Queued jobs do not carry a generation; when a job starts it captures whichever runtime is current then. Retired runtimes stay alive only while an operation that captured them is running, and are released when that operation finishes. An operation that requires VPN and whose retired egress path has no fresh check fails closed; its job retries under the current runtime. A save cannot mix an old engine with new indexers midway through one search. Rate-limit counters, provider/download state, library links, outbox, job leases, auth sessions and in-flight network calls are operational state and are not recreated by settings swaps.

Indexer metadata and cache-evidence callbacks, acquisition (including source-ceiling evidence), native web handlers and arr views are prepared for each generation. Startup services that only read unchanged sections keep their configuration while their outbound gateway resolves the captured runtime, or the current runtime for work without a capture.

Polling leaves downloads unchanged when their provider has no client, counts them as `unpolled`, and warns once per provider with its key and count. Only the settings commit fails downloads for provider removal or disabling: inside the save transaction it re-reads `adding`, `added`, `downloading` and `ready` rows, refuses with “would orphan N active download(s)” unless `orphan_active_downloads` is confirmed, and then fails exactly those rows with “provider removed by settings”, without blocklisting.

A provider kind, URL or credential change requires verifying every active torrent through the candidate gateway and client, with matching normalized info hashes. Preparation refuses missing torrent IDs or more than 100 active rows per provider; probes use at most four workers under one overall 60-second deadline. Not-found and hash mismatches mean the new account cannot see the torrents; transient failures and timeouts instead report “could not verify N active download(s) on the new account; retry”. The commit re-reads active rows and refuses unverified or changed rows with “downloads changed during save; retry”. The two-minute not-found grace starts at publication and subsequent runtimes retain unexpired deadlines; during grace a missing torrent fails without blocklisting.

For classes listed in `egress.require_vpn_for` (default `[indexers]`), the VPN guard checks again after waiting for budget, immediately before sending and at every redirect hop; an expired or failed check refuses the request. No `egress` section turns the guard off; `require_vpn_for: []` keeps checks and their status without refusing requests. A redirect into the host of a VPN-required target is followed only by a request already guarded on one of that target's egress paths (or staying on its own host); any other cross-target redirect there is refused. Echo requests retain their own unguarded path but may only follow redirects on the check_url endpoint. Egress results are ordered on the shared per-path state across periodic, immediate and per-generation checks: an observation that started earlier never replaces a newer result. Each generation's gateway masks that generation's DB-backed credentials as well as environment-backed ones, and transport errors are returned and logged only as a safe category, never with server-supplied text such as a redirect Location. Immediate checks after publication use a service-lifetime context, run at most once per gateway concurrently, and are canceled and joined at shutdown. Publication markers use a bounded context independent of request cancellation, and reloading the active generation repairs a lagging marker. Every save/import and startup fallback prunes snapshots to the last two published generations and the generation being committed or recovered. The first preparation at startup tolerates and logs individual profile build failures; reloads with a previous runtime remain strict.

Changes requiring restart: listener address/TLS/cookie security, DB path, worker count/queue topology, backup location/retention/schedule, embedded UI build, and data-directory paths, because these are CLI/process resources established before or outside policy construction (`cmd/theatarr/serve.go`). Changing root paths or mount probes is reloadable only after validating mount accessibility and rebuilding the library gate; the host mount itself is external and cannot be changed by settings. Live imports use the same prepare, commit and publish safety path as saves; provider removals wait for in-progress adds, and retired web event loops are joined before a generation is closed.

Pause and per-version `mode` are explicit. Startup logs `ROUTER DISABLED` and `LIBRARY ENABLED` when those states apply (`internal/router/router.go`, `cmd/theatarr/servelibrary.go`). Turning Pause off requires a separate, explicit confirmation bound to the candidate generation; an ordinary save, import or sync never turns it off. Turning notifications on saves at once: it is an ordinary setting that downloads, writes and deletes nothing. Adding a provider or version never makes it active. There is no per-library downloads switch: a Live library downloads whenever Pause is off.

## API and UI

The authenticated admin endpoints live under `/api/theatarr/v1/settings` ("Settings API" below): records with
schema, test, test-all, enable and priority for indexers and debrid providers, budgets, YAML import preview, apply
and export, and the paginated audit. They use stable ids, generation checks and typed validation errors. Secret
fields take writes, and responses carry only `secret_set`. Tests return connectivity, auth, latency and status
without echoing response bodies or credentials, and a test never enables a source.

`internal/arrview` stays the compatibility surface: its `GET indexer` and related resources report the configured
runtime view, not the admin records. Arr-view writes follow their own supported-operation rules, and arr ids are not
settings ids.

## Fresh install and import

Serve never imports at start. A database without a settings generation starts in Setup with nothing configured; it gets its settings from Setup and the settings editors, or from an explicit import (`theatarr settings import` with the service stopped, or the import page), which resolves each `*_env` reference once, puts secret values into the secret table, validates the merged candidate and saves it atomically. YAML export strips secrets: it marks each credential as set or unset and lists the required secret entries separately.

**Fresh install.** A missing or empty policy directory is not an error: `theatarr serve` starts on a database with
no settings generation and no versions, indexers, providers or budgets, and the admin adds them through the settings
API (Admin › Setup leads through it, docs/quickstart.md). The first write (a Setup save, any section or record save,
or a first import) creates generation 1 with all 13 settings sections: the sections it does not set are stored with
their defaults in the same transaction, so every generation >= 1 is complete. A head written without some
sections (by an older Setup) serves on their defaults; a section save over it starts from the default, and
serve's next write stores every missing section with its default (`TestSectionSaveCompletesAnOldHeadWithoutSections`).
A first import into an empty database may omit or empty a group (`views`, `clients`, ...);
the completeness check applies only over an existing generation. `policy.Read`/`Load` and an explicit import refuse
an empty directory. An empty policy validates, and a debrid version needs no `client` (the router adds to the providers itself; a seedbox version still needs one). Tests:
`TestServeStartsWithNothingConfigured`, `TestFirstSaveSeedsEverySection`, `TestFirstSettingsImportWithoutViews`,
`TestReadOrEmpty`, `TestDebridVersionWithoutClient`.

## Persistence and runtime

### Records, credentials and audit

Migration **00055** adds normalized indexer/provider/budget records, separate plain-text credentials, generations
and redacted per-record audits. `settings.New` restricts the database and existing WAL/SHM files to 0600;
`Load`, `Save`, `Bootstrap`, `PreviewImport`, `Import`, `Snapshot.Export` and paginated `Audit` are the package's
entry points. Saves replace the three sections, validate the whole candidate through `policy.Validate`, and
check `ExpectedGeneration` inside the same writer transaction as records, credentials, generation and audit.
IDs and creation times survive updates/imports; deleted IDs are never reused. The quality, naming, version
and view records use the same row adapter and transaction and audit path. A snapshot's revision
combines its persisted generation revision with the complete assembled policy content, without hashing credentials.

The caller supplies a YAML `Base` for every load/save/explicit import so validation includes
all unchanged sections, directory-relative paths and file origins (`Policy.Clone` preserves them). An omitted
record group cannot silently delete its records ("Settings contract").
Bootstrap checks for an existing generation before opening the import directory or resolving credentials.
Secret resolution is injected as a named lookup; bootstrap and import copy the referenced values into the secret table,
and `Snapshot.WithSecret` passes credentials only to runtime constructors. Stored credentials win; a record whose DB secret was
never set can use the retained `api_key_env` name through an explicitly supplied lookup (the environment fallback). New
DB-only providers use an adapter-local env-shaped validation reference, never an environment lookup. Secret writes
omitted from a save/import preserve the stored value; an explicit empty write clears it, and a cleared credential
is no credential: the environment fallback is not consulted again (see "Reloads, accounts and redaction" below). Rendering secrets always
uses set/unset markers, and driver/constructor errors are opaque to avoid leaking credentials.

Disabled records stay in the validated policy so references remain checked; the runtime leaves disabled indexers and
providers out of its services and orders indexers by record `Priority` (the policy types have no such fields). Export/preview
produce review YAML with every `*_env` field removed, credential set/unset comments and a separate list of
YAML-owned credential requirements. This is deliberately not a directly importable credential backup: supply secret
references/credentials again before strict import; enabled/priority metadata stays in the DB, and an import keeps
each existing record's enabled state and priority. Preview does not mutate
state, and apply rechecks the generation.

Tests cover migration up/down/constraints, fixture YAML → DB → Policy equality and relative-path preservation,
plain-text carryover and fallback precedence, credential rotation/clear, formatting/JSON/YAML/log/error/audit/export
redaction, stale concurrent writers on independent connections, invalid/partial imports, read-only previews, stable
record identity/deletion and rollback after a final audit failure (including all earlier writes and audit rows).

### Runtime generations

`settings.RuntimeCoordinator` prepares a complete immutable policy/gateway/indexer/provider-sync/decision-engine/router
bundle before committing and publishing it through an atomic pointer. Failed preparation leaves both the stored and
active generations unchanged. Queued jobs do not retain a settings generation; each job captures the runtime current
when it starts. Gateway back-offs, window counts and token counters carry forward. Route jobs use the router current
when they start, while router polling follows the current runtime and shares its route/poll lock across swaps.
HTTP and arr-view requests pin a runtime at entry; searches, direct grabs, RSS reads and imports use that runtime
through completion. Migration 00056 stores complete settings-owned snapshots and the last published generation for
startup fallback. The reload function is passed through `web.Config`. Global switches and version modes have DB records and confirmed editors ("Global switches").
Saves and imports use the coordinator's immutable base, never a caller-supplied one. That base lacks the DB-owned
sections, so an import validates it only merged with the imported records (a whole-policy base is also checked on its
own).

### Secrets and sync policy

- Secrets are stored in plain text in the settings tables, as Sonarr, Radarr, Prowlarr and Seerr (`config/settings.json`)
  do; no encryption key to manage. They stay write-only in the API/UI (`secret_set` only), never appear in YAML exports
  or audit diffs, and the DB file keeps restrictive permissions. `setting_secret` therefore has no key id; encrypt backups that leave
  the host.
- An environment variable named by a `*_env` field is the fallback while no DB secret was ever set (an explicit
  clear does not fall back).
- Provider/indexer test calls are admin-only, audited as outbound actions, and count against the source's budget.
- Scheduled TRaSH sync: each enabled sync source applies what changed upstream on its schedule, as Recyclarr does,
  and writes an update log entry; there is no review step. A value set by hand stays, and nothing is deleted
  ("Quality and naming records" below). A run never changes a version's mode.
- Restart-only settings are listed under "Live changes and safety".

### Settings API

| Method and path | Does |
|---|---|
| `GET /settings` | Overview: source, stored and active generation, provider epoch, effective Pause, all records |
| `GET /settings/schema` | Form schema for the three record types and the write rules |
| `GET /settings/export` | Review YAML of the stored head (`Snapshot.Export`: credentials and `*_env` stripped, webhook URL paths and URL credentials redacted) |
| `GET /settings/audit?after=&limit=` | Redacted audit, oldest first, pages of up to 500 with `next`; `order=desc&before=` pages newest first (`next` is then the following `before`). Settings history uses newest first with Older/Newer |
| `POST /settings/import/preview` | Dry-run diff of importing the configured policy directory; changes nothing. A record whose resolved credential the import replaces is an `update` with `credential_changed: true`; each update names its changed `fields` (`kind`, `enabled`, `priority`, `order`, `config`) |
| `POST /settings/import` | `ImportAndApply` of that directory; needs `generation` and `orphan_active_downloads` |
| `POST /settings/reload` | Prepare, commit and publish the stored head again (after a startup fallback) as a provider transition; `generation` must be the head; optional `orphan_active_downloads` (default false) |
| `GET`, `POST /settings/{indexers,providers,budgets}` | List; create (new sources default to disabled) |
| `GET`, `PUT`, `DELETE /settings/{indexers,providers,budgets}/{id}` | One record; update (omitted config fields keep their values; a sent one replaces the stored field whole, nested objects included, and `null` resets it to its default); delete (`?generation=`) |
| `PUT /settings/indexers/priority` | Indexer priorities, `[{id, priority}]` |
| `PUT /settings/providers/order` | Provider fallback order, every id once |
| `POST /settings/{indexers,providers}/{id}/test` | Test a saved source (enabled or not; a test never enables it) |
| `POST /settings/{indexers,providers}/test` | Test an unsaved candidate (new key and config, or changes to an `id`); validated through `Candidate`, nothing stored. Changes to an `id` at a new scheme, host or port need `secret` again (422 otherwise) |
| `POST /settings/{indexers,providers}/testall` | Test every enabled source (`include_disabled` adds the rest) |

Tests (`cmd/theatarr/servesettingstest.go`) run through one gateway operation, `Gateway.Probe`
(`internal/gateway/probe.go`), built on the active runtime's gateway for the tested snapshot and pinned to the test's
context. It holds the test's authorization, charging and health accounting:
- **Destinations.** A candidate never widens its own authorization. A test sends the stored credential only to the
  saved source's address (`settings.SourceAddress`: kind, the url's scheme, host and port, an indexer's proxy
  scheme/host/port, and a cardigann indexer's definition and `hosts`): a candidate for an existing `id` that
  changes it without a new `secret`
  answers 422 "send the credential again to test a new address" (the tester refuses it too). Saving such a change
  is confirmed instead (below). A probe may reach the
  live allowlist plus the tested source's own host:port; a private, loopback, link-local (or CGNAT and other
  special-purpose) address only when that host:port is already in the live allowlist or is a live proxy. This is
  checked on the resolved addresses at dial time, for every redirect hop (a hop off the allowlist is refused as
  before, the VPN guard and the redirect rules of "Live changes and safety" still apply), and the checked addresses are the ones dialed.
  Through a proxy, which resolves names itself, the probe resolves and checks the host before each hop. A
  candidate's proxy must be one of the live egress paths. A public host that is not live can be tested (adding an
  indexer), and it does not join the live allowlist.
- **Budgets.** A test of an existing source is charged to its saved budget whatever the candidate says; a new
  candidate, or a saved source without a budget, to the shared probe budget `settings-probe` (4 requests a minute,
  burst 4, shared by every probe and generation). A test is never unbudgeted.
- **Health.** Budget buckets and windows, back-offs and egress checks are the live ones: a test waits for or is
  refused by the budget, is refused while the source or its budget backs off (candidates included) or the VPN check
  is missing or stale. Only a test of a saved source as saved records its outcome in live health. A candidate's
  failures, rate limits and successes go to a separate in-memory record shared by all probes (it backs off further
  candidates for that key, expires an hour after it settles) and never touch a live failure streak or back-off. A
  candidate probe also keeps its caller-chosen keys and hosts out of the live metrics.

Indexers run the first probe they support (recent releases, then an IMDb movie query, then text); a DMM indexer runs
only an IMDb movie query (tt0133093, ten results) on its configured path, since its RSS reads only the path's rd/ad
providers. Providers read the account. Results carry categories only (ok, auth, credential, http, connectivity,
timeout, unsupported, `dmm_provider_key` for DMM's error 102, no key linked at DMM for a provider in the path, or the
gateway's refusal reason), HTTP status, latency, the release count or premium/expiry, never upstream text.
`web.SettingsTestLimits`, shared by every generation's web layer, bounds them: 20 s per source (and at most 20 s
waiting for a slot), at most 4 running across all requests, and a 30 s cooldown (429 with `Retry-After`; test-all
reports the source as `cooldown` without calling it). The cooldown of a saved source is keyed on its record; a
candidate's on what the server derives from it: entity, scheme, host, port and the account (an HMAC of the resolved
credential under a random per-process key, kept in memory only), never the key the caller chose. Candidate tests
also share a rate limit of 10 a minute (429, category `candidate_rate`), and cooldown entries expire once their
cooldown is over. Each test is audited as `test` or `test-candidate` at the head generation with its status only.
The last result per saved source is kept in memory and shown as `last_test`, next to the gateway's back-off row and
each budget's use.

Tests: `internal/web/settingsapi_test.go` (auth and CSRF, YAML-mode refusal, stale 409, secret scan of every response,
log and audit row, keep-secret-on-update, test timeout/concurrency/shared slots/cooldown, no save on test, kill switches,
CRUD/schema/audit paging), `internal/web/settingsapi_errors_test.go` (fixed config type errors and scrubbing, preview
`credential_changed`, candidate cooldown identity, rate limit and expiry, trailing JSON),
`cmd/theatarr/servesettingstest_test.go` (continuity refusals through the real commit hook; the tester against the live
gateway's VPN, budget and back-off state), `cmd/theatarr/servesettingscandidate_test.go` (candidate destinations, saved and
probe budgets, isolated candidate health over the HTTP API and the real tester) and `internal/gateway/probe_test.go`
(resolved-address dial checks, DNS rebinding on a redirect hop, live LAN hosts, proxies, charging, health isolation
and expiry, address classes).

Limits: provider order changes that contradict a version's YAML provider list are refused by
validation; tests, last results, cooldowns and candidate health records are not persisted across restarts. A dial-time refusal of a name that resolves privately happens after the probe took its
budget token (an IP literal is refused before). Through a proxy the probe checks the host with its own resolver,
which may differ from the proxy's.

### Reloads, accounts and redaction

- **Reload is a provider transition.** `RuntimeCoordinator.Reload` goes through the same apply path as save and
  import: prepare against the active runtime, then a commit transaction that rechecks the head generation (409 if it
  moved), runs the provider continuity hook (orphan refusal unless `orphan_active_downloads`, failing orphaned
  downloads, clearing the changed accounts' `content_provider` rows) and advances the provider epoch when an account
  is removed or changed, then publishes. Reloading the head after a startup fallback therefore cannot swap a
  provider account silently. A reload writes no new generation and no audit row.
- **Torrent ids are per account.** Migration 00062 adds `download.provider_epoch`; the router records the
  epoch on every add and only reuses an existing download's torrent id (`heldBy`) from the current epoch. Rows from
  before the migration (NULL) are never reused. Because the epoch is
  global, any account change stops download-row reuse for every provider's older rows (the provider registry still
  covers those torrents). Provider sync is fenced the same way: a listing taken under an older epoch is discarded
  in its write transaction (summary `discarded: true`, no retry) instead of writing the old account's torrents into
  the registry.
- **A cleared credential stays cleared.** Every explicit secret write, including a clear, stores a `setting_secret`
  row; only a record that never had one may resolve its `*_env` fallback. `secret_set` reports the effective presence.
  Stored generation snapshots written before clears were stored cannot tell a clear from never-set and treat an empty stored
  value as a clear.
- **Export redaction.** Export and the import preview's YAML replace each `notify.targets.*.url` with
  `scheme://host/[redacted]` (webhook paths often are the token) and, for every other URL, drop userinfo and replace
  query values with `redacted`. Token-like URL path segments use the same mask as section reads. Plex preflight
  results and Now playing startup logs use that masker too, including configured URLs repeated in preflight errors.
- **Import keeps source state.** The policy YAML has no enabled or priority fields (the strict loader refuses them),
  so an import cannot express either: an import and its preview keep each existing indexer's enabled state and
  priority and each existing provider's enabled state; new records take the import defaults. Provider `order` is
  required in YAML and so always comes from the file. Preview `changes[].fields` names what each update changes.
- **What follows a reload.** Budgets: every gateway handle, including those built at startup for metadata and
  notify, delegates to the active generation's gateway, so budget edits apply on publish. Indexers and providers:
  via the published runtime. Notify: targets and budget name are a DB section read per delivery from the generation in force; enabled state is a DB switch.
  The switch follows the active generation (`notify.Config.PolicyFor`). Webhooks always carry the release name
  and indexer.
  Webhook payloads and the Health webhook read the arr views of the current runtime, and the
  grab journal's verdicts follow the current policy generation (`journal.Config.PolicyFor`;
  `journal.TestJournalFollowsCurrentPolicy`).

Tests: `internal/settings/reload_export_test.go`, `internal/router/provider_epoch_test.go`, `internal/gateway/budget_reload_test.go`,
`internal/web/settingsapi_reload_test.go` and
`cmd/theatarr/servesettingsreload_test.go`.

### Settings UI

Settings (`/admin/settings`) opens the searchable task pages. Indexers and Download clients contain their own
source editors and status; request budgets live under Advanced › Request limits. Policy files, section editors
and Import & export live under Advanced › YAML settings; Audit is Advanced › Settings history. Components are in
`web/src/components/settings/`, the API hooks in `web/src/api/settings.ts` and every decision the views make
(error explanations, write bodies, test and import state) in the pure module `web/src/lib/settings.ts`.

Backend parts: `GET /settings` carries the read-only `switches` (and the OpenAPI schema types the
audit page); `settings.Store.SetClock` makes timestamps injectable; `theatarr demo seed` seeds settings (bootstrap
from the demo policy with invented `demo-only-*` credentials, two edits and two test rows), so the demo
`theatarr serve` starts with every credential it needs.

Tests: `web/src/lib/settings.test.ts` (error explanations, write bodies, secret patches, order conflicts, test and
import reducers, the 409 flow over the API client with Retry-After, the editor session) and `web/src/components/settings/settings.test.ts`
(rendered states: YAML read-only, secrets never rendered, 409 explanation and reload with or without the draft, the
editor across tabs, orphan confirmation unticked,
test-all running and per-item results, import preview then apply, the audit diff and its redaction, editor focus,
the tests' live region), `web/src/lib/audit-diff.test.ts` (the field diff), `internal/web/settingsapi_test.go`
(`TestSettingsAPIOverviewSwitches`) and `internal/demo/demo_test.go` (`TestDemoSettings`). The web tests run in
node without a DOM: they check reducers and the markup of each state, not simulated clicks.

### Quality and naming records

Migration **00067** adds quality profiles, custom formats, format sets, naming keyed by the version key,
and quality sync sources. The common settings transaction, audit and generation path owns these records.
Formats retain their complete arr/TRaSH JSON (including `trash_id` and every specification), rather than a `file:`
path or a dependency on the TRaSH checkout. The one policy loader overlays naming onto the versions (version records). Each generation has a persisted revision for decision attribution;
`Policy.Revision` is that generation revision (identical for serve and the CLI).

Settings has Profiles, Custom formats, Libraries › Naming and Quality sync editors. The raw format-set editor is linked from Profiles. Profile quality
order includes disallowed items and groups; format editors expose the specification implementations and the
arr flavour's enum fields, and import/export single arr JSON objects. Saves require the editor's original
settings generation (or `If-Match: "settings-N"`), validate the whole policy, and rebuild the decide/runtime
consumers together before publication. In-flight operations keep their original runtime. Naming previews use
the naming port; naming changes affect new imports only. Existing links move only through explicit rename
preview/apply. Arr qualityprofile/customformat reads expose the captured runtime, with no new writes. A view's
stable profile IDs still select version sets; the first selected version supplies its displayed ladder and scores.
Settings-dependent CLI commands read the stored settings from `-db` as serve does, without migrating, importing or
publishing settings, and leave out indexers and providers disabled in the settings, as serve's services do. Before
any DB generation exists they refuse with `no settings yet: run Setup or theatarr settings import`. Limit: CLI commands that call an
indexer or provider still read its credential from the environment (`*_env`), not the settings database, and load
the stored head rather than a startup fallback generation.

TRaSH sources read the local `THEATARR_TRASH` directory, recording its Git commit (or a content digest for an exported
tree), or a tar.gz Git archive URL pinned to a full commit (use the `codeload.github.com/<owner>/<repo>/tar.gz/<sha>`
form: quality sync fetches, the archive and the tracked source's commit lookup, follow no redirect at all, so a hop
can never reach a LAN host of the live allowlist; codeload and the commits API answer directly;
`TestQualityFetchFollowsNoRedirect`). The pax global header that `git archive` writes is
skipped. Archives go through the gateway's separate
`settings-quality-sync` budget (2 requests/minute, burst 2, 24/day), destination checks and size limits.
Recyclarr YAML uses the existing converter; instance credentials and URLs are discarded, and a Recyclarr source
changes only the profile values its config sets ("Recyclarr sources: managed fields" below). Sources select a
flavour, optional TRaSH IDs to add, and local profile → TRaSH score-set bindings. Bindings update existing scored
formats and explicitly selected additions; they do not score every global format implicitly. Previews show
added/spec-changed/retired formats, per-profile score changes and the effect on existing files (score, cutoff and upgrade
eligibility, with examples). Removed or renamed upstream IDs are retained and marked retired. A preview
(`POST /settings/sync/preview`, "Preview" and the editor's "Preview draft import") is a dry run: it stores and applies
nothing, and the next run applies what it shows. A draft preview (a config that is not the saved source's) applies
a bound score set's scores as a whole and remembers nothing; a saved source's preview or run follows the score rules
below.

**Runs apply by themselves.** There are no sync modes and no pending reviews. Each enabled source runs on its
schedule (daily, `24h`, by default; weekly for a tracked source) and applies what changed upstream (specification
changes, formats added through its `trash_ids`, bound scores, a Recyclarr source's profile values and new profiles)
in one audited generation through the same coordinator save as an administrator, actor `sync:<source key>`,
operation `quality-import`. The apply goes through no confirmation: a sync run never makes a library Live, turns
downloads on or touches a switch, even when the change widens what a Live library grabs
(`TestQualitySyncAppliesToLiveLibrary`). Nothing is ever deleted: a format TRaSH retires or renames, and a profile a
Recyclarr config no longer lists, stay as they are (`TestQualitySyncNeverDeletes`); a run whose only finding is a
retirement is `unchanged`. A value set by hand stays when the source changes it, and a source's first run takes the
source's values ("Following TRaSH's scores" below). Unchanged runs only record their run, without a settings
generation. Sources are disabled until explicitly enabled. No test fetches upstream.

Each applied run writes one **update log** entry: a `settings_quality_review` row with `state = 'applied'`, the run's
preview (formats added, specifications changed, retired, score changes, profile value changes and the values kept as
yours, `held` / `held_fields`) and no candidate (`candidate = 'null'`). `GET /settings/sync/changes` (admin,
`?limit=`, default 50, at most 100) lists them newest first with `applied_at`; held-score decisions of the former
review (`kind: "scores"`) are left out (`TestQualitySyncChangesAPI`). The run row points at its entry
(`review_id`). A run is applied only at the generation it previewed: a settings save in between makes the run an
error (`ErrConflict`, the last state is kept) and the schedule tries again (`TestQualitySyncStalePreviewNotApplied`).
Nightly retention removes an applied or superseded row only after the history window (180 days by default), outside
the newest 100 such rows **per source**, and when no retained sync run references it. Applied settings remain in
settings generations and their normal audit history; retention never changes current settings. A zero history
window disables this pruning.

Migration **00119** (`00119_quality_sync_auto.sql`) moves existing installs over: pending reviews of any kind become
`superseded`, the stored `mode` is removed from every source, and every run's config digest is cleared, so each
source reads its upstream again on its next run and applies the result (`TestQualitySyncAutoMigration`). Changes a
former `review` or `auto_definitions` source was holding apply on that run. Old applied reviews appear in the update
log; old `review` outcomes stay in Recent runs as they were.

The sync implementation stays in package `settings`: `qualitysync.go` holds configuration and shared types,
`qualitysync_preview.go` computes candidates and loads sources, `qualitysync_apply.go` applies a run and reads the
update log, and `qualitysync_runs.go` schedules and records runs. The apply and its log entry are written in the same
coordinator transaction.

**Tracked sources** (`origin: github`). A source with `origin: github` tracks `repo` (default
`TRaSH-Guides/Guides`) at `branch` (default `master`) instead of the local tree or a pinned archive (`archive_url`
and `commit` must stay empty). Each run asks GitHub's commits API for the branch's newest commit
(`https://api.github.com/repos/<repo>/commits/<branch>` with the `application/vnd.github.sha` media type: the answer
is the bare 40-hex id, at most 4 KiB read) through the gateway, under the same `settings-quality-sync` budget and
destination checks as the archives. When that commit and the source's config are those of the source's last run that
did not fail (migration **00073** records a config digest per run), the run is recorded `unchanged` and nothing is
fetched; otherwise Theatarr fetches `https://codeload.github.com/<repo>/tar.gz/<commit>` and builds the usual preview. A
weekly run therefore costs one small request, and two when the branch moved. The default schedule for a tracked
source is `168h` (weekly, counted from the last run, not a weekday).

Every source behaves like a common Recyclarr setup: only listed formats are managed (`delete_old_custom_formats:
false`), and unbound profiles keep their local scores. An unbound profile never gets a score change from a TRaSH
source. Each run is recorded with its commit and outcome (`unchanged`, `applied`, `error`). A failed lookup, fetch
or apply changes nothing but its own run record ("the update could not be applied: …; the last state is kept"; the
settings stay as they were, `TestQualitySyncFailedApplyKeepsState`), health (Quality sync) turns to a warning naming
the source, its error and its last good commit, and a tracked source tries again after 6h instead of waiting for the
next weekly slot. Otherwise health says "Quality sync is up to date". A manual "Run sync now" skips the schedule but
not the commit check; "Preview" always fetches. The editor shows origin, repository, branch and schedule; the sync
page shows each source's kind, origin, last good commit and a failed last run. Tests use a fake GitHub only
(`internal/settings/qualitytracked_test.go`, `internal/gateway/quality_test.go`).

**Linking formats to TRaSH.** A sync source compares only formats that carry a TRaSH id.
The arr import keeps the arrs' formats inline in format sets by name, without an id (the arrs do not store one,
whether Notifiarr's TRaSH sync made the format as `<TRaSH name> - Notifiarr` or Recyclarr under the plain name),
so after an arr import a tracked source follows only the standalone formats and none of the set formats. Settings → Quality sync → "Link formats to TRaSH" (per source; `POST /settings/sync_sources/{id}/link/preview` and
`.../link/apply`) links them. It is an explicit administrator action and nothing else links: not startup, not a
settings import, not a sync run (`TestTrashLinkNoStartupOrSyncSideEffects`).

The preview fetches the source's TRaSH tree as a run would and lists every format of the source's flavour
(standalone formats with that flavour, and the inline formats of that flavour's sets; a set entry that references a
standalone format is that format) with its state. Matching is by specification: per condition the implementation,
negate, required and field values (the arr's field list and TRaSH's field object read alike, and a zero, false or
missing value is the arr's default); condition names, the renaming flag, ids and TRaSH's own keys do not count. The
name (exact, or without ` - Notifiarr`) only picks between TRaSH formats with one specification, or flags a
near-match:

| State | Meaning | Preselected in the UI | Linkable |
|---|---|---|---|
| `linked` | already carries a TRaSH id | - | - |
| `matched` | TRaSH has its specification under its name | yes | yes |
| `diverged` | the TRaSH format of its name has another specification, or its specification has another name | no | yes, by explicit choice |
| `ambiguous` | several TRaSH formats have its specification and the name picks none | no | yes, by explicit choice among those |
| `unmatched` | nothing in TRaSH resembles it (a local format, such as `Tier Fallback`) | no | no |

The apply (`{generation, commit, links: [{set, key, trash_id}]}`) recomputes the match at the previewed settings
generation and TRaSH commit (the preview's tree is kept 30 minutes, so a tracked source is not fetched again; a
different generation or commit is a 409) and refuses any link the preview did not offer (422). It writes one
audited generation (operation `trash-link`) and clears the source's run digests, so its next run reads upstream
again even at the same commit. The link is the format's own `trash_id` (and `flavour`), on a
standalone format or on the set's inline entry: no new structure. The display name is the format's key and never
changes (`3D - Notifiarr` stays `3D - Notifiarr` in profiles, decisions, naming and the arr views), nor do scores or
set membership. A format whose specification matches stores TRaSH's JSON from then on (its name inside the JSON
included, which is how a later upstream rename is recognised); a diverged one keeps its specification, and the
source's next run applies TRaSH's as a specification change. When TRaSH's
`includeCustomFormatWhenRenaming` differs from the arr's, the arr's value is pinned on the link
(`include_when_renaming`), so neither the link nor a later definition update changes file naming. A linked format
named `<TRaSH name> - Notifiarr` whose JSON still carries that name is not taken for an upstream rename, and its
updates keep the name (`TestQualitySyncUpdatesLinkedSetFormatSpec`, `TestTrashLinkApplyThenSyncUpdatesSetFormat`,
`TestTrashLinkPreviewClassifies`).

**Binding a profile to a TRaSH score set.** A source's `profiles` maps a local profile key to a TRaSH score set
(`default`, `anime-sonarr`, `anime-radarr`, ...; a format without a score in that set falls back to `default`).
A binding touches only formats the profile already scores that carry a TRaSH id, standalone or in the profile's
format set, so link the set's formats first. Recyclarr sources follow the same rules for every value their config
manages ("Recyclarr sources: managed fields" below).

**Following TRaSH's scores.**
A saved source remembers, per bound profile and score, TRaSH's last score, in the source's `score_state` (inside the
sync source record, so every change of it is part of an audited settings generation; no migration). Reads return it;
an edit of the source never sets or drops it. Per score, on each run:

1. The local score equals TRaSH's current score: it follows (remembered).
2. TRaSH has not changed its score since the last run: nothing happens.
3. The local score still equals TRaSH's last score and TRaSH changed it: TRaSH's new score is taken.
4. Not known yet (the first run after binding, after rebinding the profile to another score set, or for a format
   just linked or added through the source's `trash_ids`): TRaSH's score is taken, except that a format the profile
   does not score, which TRaSH scores 0, stays unscored.
5. Otherwise (the local score differs from TRaSH's last score, so it was set by hand, and TRaSH changed it): the
   local score is **kept**, TRaSH's new score is remembered, and the run lists it under `held` ("Kept yours (the
   source changed them)" in the preview and the update log entry: profile, format, ours, TRaSH's last and new
   score). The next run with the same TRaSH score leaves it alone.

Nothing waits: there is no take/keep choice. To follow TRaSH again, set the score to TRaSH's value; it follows from
the next run. A retired or renamed format keeps its score and its memory. Remembering scores is itself a change of
the source record: a run whose only change is that applies too (outcome `applied`). The profile pages show each
bound score's state: follows, **yours** (differs from TRaSH's last score; TRaSH changes to it are not applied, and
the update log notes them) or **differs** (bound, never synced: the next run takes TRaSH's score). Tests:
`TestQualitySyncKeepsYourScore`, `TestQualitySyncFirstRunTakesTrash`, `TestQualitySyncAppliesWithoutReview`,
`TestQualitySyncTrackedKeepsScoresAppliesAddsSkipsRetirements`.

To bind anime profiles, for example a Sonarr profile `sonarr-anime` and a Radarr profile `radarr-anime` (in
Settings; nothing does it by itself):

1. Settings → Quality sync → `trash-sonarr` → "Link formats to TRaSH". Check the `sonarr-anime` rows, keep
   the matched ones selected, add any diverged one that is really the TRaSH format, and apply. Same for
   `trash-radarr` and `radarr-anime`.
2. Edit the sync source `trash-sonarr` and set "Local profile key to TRaSH score set" (config `profiles`) to
   `{"sonarr-anime": "anime-sonarr"}`; edit `trash-radarr` and set it to `{"radarr-anime": "anime-radarr"}`.
   Save.
3. Run sync now on the source (or wait for its weekly run). The run takes TRaSH's anime scores for those profiles
   (rule 4: the first run after binding), in one generation, actor `sync:<source>`, and lists them in the update
   log. From then on a score you change by hand stays yours. "Preview" on the saved source shows the same first,
   without applying anything.

**Recyclarr sources: managed fields.** A Recyclarr source (local, pinned or tracked TRaSH tree alike) touches only
the values its config sets, so a run never wipes values the config does not mention (a SeaDex score, a size
limit). The managed values, by policy name: `qualities`, `cutoff` (`upgrade.until_quality`), `upgrades` (`upgrade.allowed`), `min_score`,
`cutoff_score` (`upgrade.until_score`), `min_upgrade_step` (`min_upgrade_format_score`), `formats/<format>` (a
score `assign_scores_to` sets, or one `reset_unmatched_scores` resets, except its `except`/`except_patterns`) and
`sizes/<quality>` (from `quality_definition`). Everything else of the profile stays local. A profile the settings do
not hold yet is created whole from the config (it then needs `qualities`); a profile the config only assigns scores
to is fine when the settings hold it. A profile the config no longer lists stays as it is. Taking new qualities keeps
a `quality_order` that still ranks them and drops one that does not, and moves a cutoff that is no longer allowed (to
the config's, else the first allowed item); each field is compared with the profile as it was before the run, so a
cutoff moved by new qualities is not taken for a hand edit.

The source remembers per profile and managed field the value it last asked for (`field_state` in the source record,
like `score_state`; reads return it, an edit never sets or drops it, no migration). Per field, on each run, the
rules of bound scores apply: a local value equal to the source's last value follows the source; a value the source
did not change is left alone; the first run that sees a field (a new source, a new profile) takes the source's value;
a local value that differs from the source's last value is **kept** when the source changes it, the source's new
value is remembered and the run lists it under `held_fields` (profile, field, ours, last, upstream as JSON in the
records' spelling, null for "not set"). A score `reset_unmatched_scores` reset earlier stays remembered while the
reset covers it, so restoring it by hand makes it yours (`TestReconcileResetScoreRestoredStays`). Profile value
changes, a Recyclarr source's scores and new profiles apply on the run like everything else
(`TestRecyclarrProfileValuesApply`). Previews and log entries list each changed profile value (`fields`, sizes per
quality) and the config keys the sync skips (`notes`). A draft preview (the editor's unsaved config) applies every
managed value of the config as a whole and remembers nothing, as it does for bound scores; values the config does
not manage stay local there too.

`quality_definition` (instance-wide, and per profile, which wins for that profile; Theatarr keeps sizes per profile,
so a per-profile definition applies whatever the flavour) maps TRaSH's quality-size file of its `type` (`movie`,
`series`, `anime`, `sqp-streaming`, ...) to the profile's `sizes`, as recyclarr's QualitySizePlanComponent and
QualitySizeCalculator do: per-quality `min`/`max`/`preferred` overrides (a number or `unlimited`), `preferred_ratio`
(clamped to 0..1, preferred = min + (max capped at the preferred limit - min) * ratio), max and preferred capped at
the service limits (Radarr 2000/1999, Sonarr 1000/995 MB/min) and unlimited at or above them. Only the qualities the
file lists are managed; a quality Theatarr does not know is skipped with a note, an unknown type or override quality
or out-of-order sizes fail the run. A source's `quality_sizes` switch (editor: "Apply quality sizes from
quality_definition", default on) turns it off; the definition is then listed as skipped. `media_naming`,
`media_management` and `quality_sort` are listed as skipped too. TRaSH-kind sources have no sizes: they manage only
bound scores, which already leave everything else alone. Tests: `TestQualitySyncRecyclarrKeepsUnmanagedValues`,
`TestQualitySyncRecyclarrFollowsAndKeepsManagedFields`, `TestQualitySyncRecyclarrResetUnmatched`,
`TestQualitySyncRecyclarrQualityDefinition`, `TestConvertQualityDefinitionSizes`.

The secret-free YAML export writes `file:` references; `/settings/export/files` supplies that YAML plus the
corresponding JSON file contents, which must be written under the exported relative paths for YAML reuse.

### Versions and view bindings

Migration **00068** adds `setting_version`, `setting_view_binding` and a bootstrap marker. Versions persist
all version policy except naming, plus the `library.versions` write-root lists. Profiles and format sets keep
existing keys (a format set is selected through the profile); naming has its own records (above), assembled by key.
View records hold ordered version sets and named profile bindings, and also the identity (path,
flavour, aliases) and the view's stored API key ("Sections and views"). The existing settings loader assembles and validates the one runtime policy.
A version created in the editor gets its naming record at once, empty: it starts with the arr default names, edited
in the naming editor (Settings › Libraries › Naming). A version's naming record cannot be deleted on its own; deleting
the version removes it. Naming records stored with `FromVersion` (following, removed in 00121) were given the
followed library's naming by that migration.

A version has an optional `name`, a display name for people (at most 40 characters, no control characters; two
versions may share one). When set it replaces the label guessed from the key and profile wherever Theatarr names a
library (title pages, Wanted, request notifications); colliding names get the key appended like colliding guessed
labels. It names nothing an arr client sees: keys, root folders and the arr views' profile names do not come from it,
and renaming an active library asks for no confirmation. The field is left out of the stored record and of the API's
record when it is empty, so a library without a name is stored as before; a build from before the field refuses to
load settings that hold a name (the policy decoder is strict), until the name is cleared.

The Libraries settings page is `/admin/settings/libraries`; it links to Connected apps for view bindings and to the naming editor
(`/admin/settings/libraries/naming`). Admin APIs under `/api/theatarr/v1/settings` provide version CRUD, `versions/validate-root`,
`versions/confirm`, view-binding list/update and `import/confirm`. Session writes require CSRF; writes require the
base generation and honor `If-Match`. Validation errors include field paths. Forms retain the generation they
opened with and offer explicit reload/discard or reload/keep after a conflict. The live root field reports failed
checks as `root check: ...` (a relative path first: "must be an absolute path"); this check alone does not attempt or
refuse a settings save. A refused save shows each field's message beside Root and Write roots; a relative root is
reported as that alone, not also as a library folder "under none of the roots". The view editor
(Connected apps) picks libraries and profile members as checkboxes, so there is no typed list to normalize.

Creating a library Live, Watch only to Live, turning downloads on for a Live library and removing a library
require review of effects naming
roots, providers and current global gates (the full list is under "Confirmation on every path"). Every other version
change, a Live library's root, providers, profile, indexers, upgrades, naming and storage included, saves at once
after validation; so do view-binding changes. Removing a library removes its rows (see below). Tokens expire after ten
minutes, are one-use and bind the next generation, candidate revision and effects, and the signed-in user or API key
they were issued to (another user's attempt neither uses nor uses up the token; a commit that fails restores it for
the same user only); saves recheck managed links in the transaction. Imports use the same flow; ordinary save, direct
store import and reload cannot silently authorize these effects. A fallback runtime is checked alongside the stored
head. `router.enabled`, `library.enabled` and `notify.enabled` are managed in Settings → Download clients, Libraries
and Notifications; version saves never change them, including when confirming an active version while grabbing is
off.

**Confirmation on every path.** The effects come from the whole candidate, computed against the active runtime and
the stored head, on record, quality and version saves, secret saves, import apply and reload. Only these ask:

- a library created Live, or going from Watch only to Live ("Library X becomes Live: it searches, downloads to P and
  writes links under R.", with "Pause is on, so nothing starts until you turn it off." while paused);
- turning Pause off ("Global switches" above);
- removing a library, always ("Library K is removed with its entries on every title (monitoring, wanted state,
  history). N linked files stay on disk and are no longer tracked. M downloads in progress stay at the provider or
  client and are no longer tracked. Q queued downloads that were not sent yet are dropped.", the last three sentences
  only when not zero). The save that removes it deletes its rows in
  the same transaction ([library.md](library.md) "Removing a library");
- a provider or indexer, enabled or not and whatever versions use it, whose address (`settings.SourceAddress`:
  kind, url scheme, host and port, proxy scheme/host/port, a cardigann `definition` or `hosts`) changes while it
  keeps its credential
  ("Provider rd changes its address from realdebrid https://api.real-debrid.com:443 to … and keeps its stored
  credential …"). The credential does not have to be entered again: a browser session or a user's API key
  confirms it like the other effects. A new credential sent with the new address needs no
  confirmation. Until the change commits the stored key goes nowhere new: the source tests refuse it (above), and
  preparing the save does not probe the provider's active downloads at the new address (the same key is the same
  account: its downloads stay, the provider epoch advances and the registry sync after publication reads the new
  address). The store enforces it for every caller. A startup fallback is not refused for going back to its own
  earlier address: that generation was published with it.

Nothing else asks: profile, format and specification edits, indexers and providers becoming usable, a view serving
a Live library, the VPN guard, `router.cached_only`, `router.alldebrid_delete_uncached`, `torrents.scan_roots`
(torrent deletes keep their own confirmed preview), folders, naming and credential names save at once. A quality-sync run applies without
confirmation too ("Runs apply by themselves" above).

`Store` saves recheck the same effects in their transaction, so a direct store save cannot turn on downloads for a
Live library either. A refused write answers 422 with `effects` and a `confirmation` (`token`, `generation`,
`effects`); sending the same request again with that token in the body (`confirmation_token`) or the
`X-Confirmation-Token` header commits it. Reload takes the token too, so a startup fallback does not block it. A
token is used up only when the commit lands: a refused or failed store commit leaves it valid (a failed prepare still
uses it). Version delete takes its token only in the header; `?confirmation_token=` is refused with 400 so tokens
stay out of URLs and access logs (the UI sends the header). The web editors (records, sections, libraries, switches,
reload) open one shared dialog listing the effects (`web/src/components/settings/confirm-write.tsx`). Tests:
`internal/settings/confirm_widen_test.go` (`TestEverydayLiveEditsSaveDirectly`, `TestKeptConfirmations`),
`internal/settings/sourceaddress_test.go`, `internal/web/settingsconfirm_test.go`.

New or changed roots must be clean absolute existing directories. A root may be anywhere outside the debrid mount,
the trash and the other libraries' roots: there is no library area, and `library.dangling.roots` only adds folders
for the broken link check. The filesystem root is never a library root, directly or through a symlink. Checks
resolve symlinks and device/inode aliases, reject a root at, in or above the mount and overlap with other versions or
the trash, and require active roots to be writable without creating probe files (`TestLibraryFolderAnywhere`); a Watch-only
root need only be readable. A root edit with active links is refused, and so is a deleted version key created again
while active links of the key sit outside its new roots; see the procedure in
[migration.md](../migration.md#changing-a-root-with-links). Removal does not delete any operational
link row, link, mount file or trash entry.

### Settings UX

Contrast, editors and the indexer pages. Profiles and presets are in the next section.

- **Contrast.** Theme tokens in `web/src/styles/globals.css` meet WCAG AA: text 4.5:1, control boundaries 3:1. The
  `--th-control` token draws the border of outline and secondary buttons, inputs and selects (dark #5a6488,
  3.29:1 on a card; light #8590aa, 3.20:1 on white), outline buttons get a raised fill (`--th-raised`, dark
  #141d36), filled buttons use #4a52e6 with white text (5.77:1), the dark destructive fill is #dc2626 (4.83:1), and
  the faint texts, borders, light focus ring (#5b64ff), light success fill and warning text were darkened or
  lightened to pass. `web/src/lib/contrast.test.ts` reads the CSS and asserts the pairs.
- **Editors.** Every record editor opens in one side sheet (`web/src/components/ui/sheet.tsx`, a Modal variant:
  640px from the right, the whole screen on a phone): indexers, providers, budgets, versions, and the quality pages
  (profiles, formats, format sets, naming, sync sources). The sheet focuses its heading, traps focus, closes on
  Escape and gives focus back to the opener (after a delete, the next row's Edit or the Add button); Save, Cancel and
  Delete sit in its footer. The row being edited stays marked (`data-selected`). Delete and the active-version
  confirmation open as dialogs on top of the sheet; Escape closes only the dialog. The page keeps the open record in
  `?edit=<key>` (`new` while adding), so a reload reopens the sheet. Small inline edits (priority arrows, enable
  switches) stay inline. The version editor has no separate format set picker: the format set comes with
  the profile, and the editor says which.
- **Indexers page.** One table: indexer (badges and an active back-off in the row), via (adapter and host, or the
  catalog definition), privacy, priority with arrows, last test, and labelled Test and Edit buttons. A filter box
  narrows it by key, kind, definition, URL or privacy (the arrows are off while it filters). Providers keep their
  cards, under a fallback line such as "1 RD › 2 AD › 3 TB". Definition updates held for review
  ([cardigann](cardigann.md#the-definition-catalog-sync-source)) are listed above the table with Approve, which asks first.
- **Adding providers and built-in indexers.** Choose the service, review Enabled and set its write-only API key
  when the service uses one. Enabled starts off for a provider and on for an indexer; a new indexer also has the
  **Libraries** list (below). Test these values tests the unsaved form; while the form still
  holds exactly the values of a passed test and Enabled is off, the result line offers **Enable and save**, which
  turns Enabled on and saves in one write. A retest clears the last result. Save alone stores the record as the
  form has it. **Show advanced** exposes the record key, endpoint and tuning fields. Providers get
  editable service defaults; self-hosted indexers need their address entered.
- **Adding an indexer.** Add indexer opens a sheet with four tabs, on Catalog. Every way of adding saves the indexer
  **on** (the API's own default stays `enabled: false` when the field is omitted; the UI always sends it), with a
  **Libraries** checklist in the same form: the ticked libraries search it, sent as `versions` on the create
  (`POST /settings/indexers`), which appends the indexer to each named library's indexer list in the same save
  (unknown library: 400; `versions` on an update: 400). Every library that suits it starts ticked: a catalog site by
  its Newznab categories (2000s: movie libraries; 5000s but 5070: series libraries; 5070 anime: series libraries not
  marked non-anime and movie libraries marked anime), SeaDex for libraries not marked non-anime, every other source
  for all libraries; Watch-only libraries are ticked like the rest. The add waits until the library list has loaded
  (its button is disabled; a failed read shows Try again). Tests are offered, never required.
  - *Catalog*: the cardigann definitions (`GET /cardigann/definitions`), searchable, filtered by privacy, content
    (Movies, TV, Anime from the Newznab categories), language and "runs here" (runs or needs an account; Cloudflare
    and unsupported ones only under All). With the catalog off the tab says what it is and offers **Turn on the
    catalog**, which saves `cardigann.definitions = {repo: Prowlarr/Indexers}` (the section PUT) and lists the
    definitions once the first fetch is done ("Fetching the catalog…" meanwhile, polled every 3 s; a failed first
    fetch shows its error and **Try again**, `POST /cardigann/refresh`). Set up opens a form built from the
    definition's settings: the site address is one of its links, privacy is shown read-only as a label,
    secret settings and the account name go in the credential as a JSON object, other settings are sent only when
    they differ from the default, info notes are shown as text, extra hosts are listed. The indexer gets a budget of
    its own under its key (10 per 1m by default). Test search runs the candidate test (`POST /settings/indexers/test`)
    and, because a candidate needs its budget to exist, writes that budget first; Add reuses it. **Add** writes the
    budget (if new) and then the indexer with the form's **Enabled** (on) and Libraries. Credentials, Enabled and
    Libraries appear first; **Show advanced** exposes the other settings.
  - *Torznab & built-in sources*: torznab/newznab, dmm, torrentio, zilean and seadex open the record editor in the
    same sheet with that kind chosen. The DMM card carries a **Paid key** badge: DMM gives its API key only to paying
    GitHub sponsors.
  - *Link Prowlarr*: Prowlarr's address and API key are entered once (`cardigann.prowlarr`, the key stored write-only
    like other section credentials). A new address needs the key typed again: a `cardigann` section save that changes
    `prowlarr.url` while a key is in use (stored, or the environment fallback) and sends no new one (clearing the
    stored key does not count) answers 422 "Enter the API key again for the new
    address", as Radarr and Sonarr lists do; the form says so before sending.
    `GET /settings/prowlarr/feeds` reads Prowlarr's indexer list (no catalog needed) and marks each `linkable` (with
    the key it gets, `prowlarr-<name>`), `linked` (an indexer already has its feed URL), `disabled` (off in Prowlarr)
    or `usenet`; only `linkable` ones can be ticked, all are by default. **Link {n} feeds** posts the ids and the
    ticked libraries to `POST /settings/prowlarr/feeds`, which reads Prowlarr again and adds each as an enabled
    Torznab feed (Prowlarr's key copied into its credential, the shared `prowlarr` budget of 60 a minute made when
    absent) in one generation. The trackers stay in Prowlarr; the tab says to keep DMM and SeaDex added directly and
    to run Prowlarr behind the VPN, since Theatarr's VPN check does not cover Prowlarr's requests.
  - *Move from Prowlarr (once)*: needs the catalog (the tab offers **Turn on the catalog** when it is off) and a
    connected Prowlarr (the Link tab's form otherwise). Read Prowlarr's indexers asks `GET /cardigann/prowlarr`; each
    `proposed` one that is not already an indexer can be ticked (all are by default), and only the ticked ones are
    written, each as its own budget plus an indexer at the end of the priority list, on and in the libraries its own
    definition's categories fit (every library when the catalog lacks the definition), shown on its row. While the
    catalog's first fetch runs (or failed) the tab shows that state, as the Catalog tab does, and offers no Read. A proposal that names secret settings (a login)
    is added off: "added off: enter its login, then turn it on". No-definition ("link it on Link Prowlarr instead"),
    unsupported and usenet proposals are shown and skipped.

  Adding an indexer changes no global switch and no library besides appending it to the ticked ones.
- **Schema.** `GET /settings/schema` offers the `cardigann` kind with `definition` (string), `settings` (the new
  `string_map` field type: `name=value` pairs, comma or newline separated) and `hosts` (list), all for that kind
  only (`internal/web/settingsschema_test.go` checks every indexer field is a policy field).

Tests: `web/src/components/settings/sheet.test.tsx` (heading focus, Escape and focus return, row marking,
`?edit=`, the delete dialog stacked over the sheet, the provider orphan choice), `add-indexer.test.tsx` (catalog
filters, set-up writes with libraries, turning the catalog on, Built-in sources, the move from Prowlarr, holds),
`prowlarr-link.test.tsx` (connect, list, link), `web/src/lib/cardigann.test.ts` (including `matchingLibraries`).

### Profiles, presets and formats

The Libraries page, the quality profile pages, presets and the impact preview. Nothing here grabs, searches or touches the router, the library or
notifications; `router.enabled`, `library.enabled` and `notify.enabled` have their own switch cards on Download clients, Libraries and Notifications (turning the first two on asks for confirmation).

#### TRaSH defaults

The binary carries a TRaSH-Guides snapshot at one pinned commit (`internal/trashbundle`: custom formats, groups and
quality profiles of both flavours; MIT, licence beside it). The profile pages read the checkout in `THEATARR_TRASH`
when it loads for at least one flavour, and that checkout alone; otherwise the snapshot. `GET /settings/presets`
says which (`source`: `checkout` or `bundled`, with `commit`), and the quality overview likewise (`trash_source`,
`trash_commit`). `THEATARR_TRASH` set to a directory without the guides uses the snapshot and keeps a `trash_note`.

- Settings › Profiles lists every preset of both flavours under **TRaSH defaults**, with the stored copies of each.
  **Use** (**Create a copy** when one exists) opens the profile editor on a copy under a free key.
- The library editor's profile picker has two groups: the stored profiles and the TRaSH defaults of the library's
  kind (movies: Radarr's, series: Sonarr's). Saving with a default picked first creates its profile, named after the
  library (or the next free key), then saves the library with it: two saves, and the profile stays if the second
  is refused. A default stays listed after it is used.
- Nothing is stored until a default is used, so an upgrade writes no settings. A profile made from a default is an
  ordinary profile with its own format set holding every format's full specification: the policy build, the decision
  engine and "Check a release" never read a tree for it, and it decides releases exactly as the same preset made
  from a checkout of the bundled commit. With no TRaSH sync source it is unbound; with one it is bound as before.
- The fallback is for these pages only. Quality sync from a local tree, the recyclarr import, the settings import
  of `trash_id` formats and the policy build read the configured checkout and behave as before without one. So do
  the format summaries of the Custom Formats page: without a checkout a format is grouped and labelled as before,
  whatever the bundled tree knows about its name.
- The listed defaults stay at the bundled commit until the next version, or follow the checkout when one is
  mounted. Quality sync updates the stored profiles bound to a source, not the list.
- `scripts/update-trash-bundle.sh` refreshes the snapshot before a release. New presets then appear in the lists;
  existing copies change only through quality sync.

### Library state

Runtime `grab_state` carries `paused` and `pause_forced` (Pause and whether `THEATARR_FORCE_OFF` forces it),
`library_writes` (the library's own write gate, `library.Gate.Active`: Pause off, mode active, listed under
`library.versions` with roots) and `will_grab` (Live and Pause off). The library state ([UI](ui.md), "Library
state") reads Watch only, Live, or Live, paused.

## Readiness and phone navigation

Setup is a six-step checklist; [quickstart](../quickstart.md#readiness-led-setup) defines its shared progress count.
Its activation review lists library keys (also their current display labels), roots, the library state (mode and verdict), indexers and
providers, plus Pause (and whether the environment forces it).
Each links to its existing editor; reviewing never writes settings. Home and Settings remind admins while setup is
unfinished. [The arr map](../arr-map.md) maps upstream pages and explains missing equivalents.

At phone widths the source/generation banner is one line with Details, including YAML read-only and startup
fallback states. The full explanation and fallback reload remain available. Route tabs become a Section select
below 640 px; desktop retains the strip. More groups the same admin destinations in vertical lists.
