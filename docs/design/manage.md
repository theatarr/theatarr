# Season and episode management, and upgrade explanations

Code: `internal/web/{manage,upgrade}.go`, `web/src/api/manage.ts`,
`web/src/lib/{upgrade,media}.ts`, `web/src/components/title/{seasons,episode-drawer,manage-bits,journal-list,versions}.tsx`.

**Why.** A file below its profile's cutoff (for example Bluray-1080p, score 6,302, against a cutoff of Bluray-2160p
Remux and 10,000) is an upgrade candidate even when nothing better has been seen, so a library can show "Upgrade" on
most titles without saying what the upgrade would be. Managers need that explanation, and they need to open and
manage single seasons and episodes as in Sonarr and Radarr.

## Words

One vocabulary for managers on the grid, the title page, season headers and episodes (`web/src/lib/upgrade.ts`).
"Below cutoff" is not shown to anyone: it stays the API state `below_cutoff`. The quality word is said beside the
status word (Available, Partly available: docs/design/ui.md, Status words), never in its place.

| State | Wording (the line under Why?'s heading) | Quality word (badge, Why? heading, row) |
|---|---|---|
| preferred target reached (`cutoff_met`) | Preferred quality met | Preferred quality met |
| accepted replacement found (`upgrade_available`) | Upgrade found | Upgrade found |
| target unmet, no replacement found (`below_cutoff`) | Preferred quality not met; no better copy found | Preferred quality not met |

- The quality word (`upgradeBadge`, `qualityWord`) says what a person can act on. **Upgrade found** only when a
  better release was actually found. **Preferred quality not met**: the copy holds a file below its profile's cutoff
  and nothing better has been found (a movie with Remux-1080p whose profile prefers WEBDL-2160p, after a search that
  found nothing better); it is neutral, never a warning. **Preferred quality met** at the preferred quality.
- The quality word opens **Why?**, headed by that same word, in three plain lines (docs/design/ui.md, Status
  words): "You have: WEBDL-2160p, score 2,244.", "Upgrades stop at: Remux-2160p and a score of 2,000.", and the
  verdict from the server's `quality_met` ("Below the wanted quality." / "The quality is reached, the score is not.")
  with the better release found and what came of it, or "No better release seen yet (last search 2 d ago).".
- List rows carry `upgrade_state` for a title with a file; posters show "Upgrade found" on the year line, and so does
  the table's Status, under the status word (J2: "Preferred quality not met" and "Preferred quality met" are
  the title page's). The grid's quality filter is **Upgrade found** (`upgrade=found`); the status filter
  **Available** means a file is held whatever the cutoff (owner decision O1, 2026-11-13; "Preferred quality not met" is gone from the grid, and Wanted › Cutoff unmet is
  unchanged). The Home row **Upgrade found** (`discover` `upgrades`, and its "See all" page) lists only titles with
  an upgrade found: a monitored copy below its preferred quality for which a better release was seen (a series copy
  that still misses episodes counts by the files it has), captioned with the libraries; season headers count "N
  under preferred quality · M upgrades found", the count left out beside an Upgrade found badge.
- Users without "see files" get availability and resolution only.

## The upgrade-evidence rule (`upgrade.go`)

Per item below cutoff (a movie version's file, or one episode's file):

1. Only evidence newer than the item's current file counts: newer than the later of its link's created time and
   its download's (an import that replaces a file at the same path keeps the link's first created time, which the
   badge readers leave alone).
2. The newest search after the file that covers the item (a journal search of the title, its season or
   episode) stands for every earlier search.
3. RSS sightings (journal RSS reads) newer than both the file and that search add to it.
4. A release counts when it was accepted and marked an upgrade over the file (`upgrade` on the stored candidate),
   and maps to the item. A sighting of the torrent the item's file came from (the candidate's hash equals the
   hash the file's link stores, `router.LinkStoredHashSQL`: its download's, or an adopted file's) is not a better release of that item; other episodes of the same pack still count it. A
   pack that only fills episodes with no file (its stored reason starts "fills ") is not an upgrade of a held file,
   though it carries `upgrade` so it stays accepted for the missing episodes; a pack that upgrades and also fills
   ("…; fills …") counts. A mixed pack (upgrades some files, keeps others: "… keep their file") stores the episodes
   whose file it keeps (the candidate's `kept`, decide's `Decision.Kept`); it is no upgrade of those episodes, and
   the episodes it upgrades, their season and the counts still count it. A candidate stored before `kept` was
   recorded has none and counts for every episode it maps to, until a later recorded search that covers the episode
   and returns candidates replaces it. The episode's stored Releases list still shows the pack. On an episode it
   upgrades the badge reads "Better pack found" and Why? words it at pack level ("Better pack found for some
   episodes …").
5. A release with a download of the same title and library that failed at or after its sighting (the candidate's
   hash against `download.info_hash`; the failure's time is its History row's, else `updated_at`) was tried and
   failed: it is not found. A download never started (Pause was on) or ended by a removal is no try, as History and
   Why? say. The best of the others is chosen; when
   every counted release was tried, the best tried one is still the answer's `next` (Why?: "Better release found …
   Tried …: not cached … Your file is unchanged.") while the state stays `below_cutoff`. A newer covering search
   that lists it again counts it again. An RSS sighting does not: a release that failed since the current file stays
   tried whenever the feed lists it again, unless the listing carries evidence newer than the failure: its
   publication time (the candidate's `published`, not when it was read) is after the failure, or the failure was a
   cache miss ("miss on every provider") and the candidate is now listed cached (`cached_on`) on a provider the
   library uses. Display only: nothing is written or deleted, and what is searched or grabbed is unchanged.
6. The best counted release (score, then newest) is what Theatarr would take next, with the engine's reason.

Hydration reads decision timestamps and coverage first, then fetches and decodes candidate blobs only for the
selected searches and subsequent RSS evidence of the requested versions' files below cutoff. Search metadata is
retained for `last_search`, even when it predates the file. Empty newer searches invalidate older finds, equal-time
searches keep the first record, and there is no history limit. The stored episode releases panel visits newest
decisions first and skips candidate decoding for older or non-covering decisions.

The cutoff a file is measured against is the library's own profile.

## Monitoring and searches

Every copy's monitoring may be changed here. Searches keep the existing rule
(`catalog.Searchable`: mode active and `router.enabled`), and the episode releases panel answers from what is stored
unless the version is active.

## API

Request pending, approved/auto-approved, declined, failed and available events queue webhooks through the existing notify outbox and retries; series also emit one partial-availability alert per request using the request detail's episode counts within the requested seasons. Native policy targets with no `events` filter receive them (the `versions` filter still applies), and `notify.enabled: false` records them as dropped; there is no per-user opt-in.

Payloads retain `eventType: theatarr.request_*` and use Seerr's `notification_type` names (`MEDIA_PENDING`, `MEDIA_APPROVED`, `MEDIA_AUTO_APPROVED`, `MEDIA_DECLINED`, `MEDIA_AVAILABLE`, `MEDIA_FAILED`), `event`, `subject`, `message`, `image`, `media`, `request` and `extra`, including joined requesters, title ids, seasons and copy keys/labels. Partial availability uses `MEDIA_AVAILABLE` with “Series Partially Available”; successful auto-approval skips the transient pending alert.

Discord and ntfy targets (`format: discord` or `format: ntfy`, docs/design/devices.md) get only available and partially available, as "**Title (year)** is ready in Plex, requested by <name>" ("is partly ready" for a partial series; plain text on ntfy). On a Discord target a requester with a Discord user id is mentioned (`<@id>`), and `allowed_mentions.users` lists only those ids, so nobody else is pinged. The id never reaches the request SSE stream or the theatarr.* payloads.

| Route | What |
|---|---|
| `GET titles/{id}` | per version `upgrade` (state, quality, score, cutoff, below/seen counts, next, last search) and `control` (mode, can_edit, can_search with reasons, live_releases); a movie file's `current.media`: for the version's newest active link, Theatarr's own probe stored at import (`content_file.probe`, formatted as the arrs format theirs), else for an adopted link the media info its arr had recorded (`link.adopted_media`, 00117) |
| `GET titles`, `discover` | `upgrade_seen` per row and version; the Home row `upgrades` lists only titles with an upgrade found (seen), its notes say "upgrade found" |
| `GET titles/{id}/episodes` | per version `control` and `cutoff`; per season and version `below_cutoff`, `upgrade_seen`, `monitored`, `quality`, `size_bytes`, `score`; per cell `upgrade_seen` |
| `GET titles/{id}/episodes/{eid}` | the episode drawer: per version state, upgrade, control, file (release, quality, score, size, media, path; `media` of a link as for `current.media`), history, journal entries |
| `GET titles/{id}/episodes/{eid}/releases?version=&live=` | the releases panel for one episode |
| `PUT titles/{id}/seasons/{season}/monitor`, `PUT titles/{id}/episodes/monitor` | monitor a season or episodes, per version; the message names the libraries ("Monitored 3 episodes in 4K"), skipped ones too |
| `POST titles/{id}/seasons/{season}/search`, `POST titles/{id}/episodes/search` | search a season or episodes through the acquirer's selection |
| `POST titles/monitor` | the library's copy-targeted bulk monitor (docs/design/ui.md "Library views"): sets `monitored` on existing copies of up to 500 titles, `dry_run` previews, `expect` skips copies changed since the preview (stale); a title without the copy comes back `missing`; never adds a copy or searches. Manage only |
| `POST titles/removal` | the selection bar's Remove and Remove from a library (docs/design/ui.md "Library views"): `GET titles/{id}/removal` for up to 500 titles in one call (a few queries), in the request's order; each scope's `requests` counts the open requests (approved, processing, failed, available) its removal cancels, by the rule the cancellation uses (every library a request asks for is in the scope, sync partners included, or removed before), `versions` the one library (empty: each whole title; an unknown library is 400); a title with nothing to remove is skipped with the reason. The removal itself runs per title through `POST titles/{id}/remove` |

Callers without "see files" get availability only (`docs/design/users.md`): whether there is a file and its resolution,
no upgrade explanation, cutoff, history, scores, releases, next, paths, sizes or journal, and the write and releases
routes answer 403. The permission check is `canManage` in
`manage.go` (`auth.PermManage`).

## SPA

- **Libraries table row** (docs/design/ui.md Title page): the monitor switch, the status (for a held movie the
  upgrade badge with Why?: "Bluray-1080p · 6,302 / 10,000 · cutoff Bluray-2160p Remux", what falls short, what
  Theatarr would take next or "No better release seen yet", "Show releases"), the chosen file columns, Search now
  (off with the reason) and Interactive search for that library (a movie, managers, a current library that searches
  live; a series has it per season and episode). The row's Details holds the rest; history and the journal are on
  the History tab.
- **Seasons** (laid out phone first, docs/design/ui.md Title page / Episodes): the season monitor switch, a badge
  per library in view ("8 / 10"), with one library its facts (missing, upgrades found, downloads, size, upgrade
  status with Why?), and Search and Interactive search on the header, all acting on the libraries in view; All /
  In progress / Missing / Upgrade found filters; episode rows open the episode overview (`?episode=<id>`) and end in
  Search and Interactive search icons; checkboxes select episodes for bulk monitor, unmonitor and search in the
  libraries in view. A season's Interactive search (also the Wanted row's) requests every episode of that season,
  Specials included.
- **Episode overview** (a shared-Modal dialog): the episode's synopsis under its air date, then three tabs: **Details** (per version the state, monitor switch,
  search now, Interactive search for managers and a version that searches live, the download in hand, the upgrade
  block and the file's facts with Held on), **History** (the title History's rows that hold the episode, the same table and words; no journal lines; its count the rows shown) and **Last search** (the stored decision
  scoped to the episode, read-only).

## Checked

Go tests (`manage_test.go`): the rule's cases, the API on a seeded series and movie, shadow versions get no
searches (state compared before and after, zero acquirer calls), the active path, and
users get no scores.
