# Season and episode management, and upgrade explanations

Code: `internal/web/{manage,upgrade}.go`, `web/src/api/manage.ts`,
`web/src/lib/{upgrade,media}.ts`, `web/src/components/title/{seasons,episode-drawer,manage-bits,journal-list,versions}.tsx`.

**Why.** A file below its profile's cutoff (for example Bluray-1080p, score 6,302, against a cutoff of Bluray-2160p
Remux and 10,000) is an upgrade candidate even when nothing better has been seen, so a library can show "Upgrade" on
most titles without saying what the upgrade would be. Managers need that explanation, and they need to open and
manage single seasons and episodes as in Sonarr and Radarr.

## Words

One vocabulary for managers on the grid, the title page, season headers and episodes (`web/src/lib/upgrade.ts`
`UPGRADE_WORDS`). "Below cutoff" is not shown to anyone: it stays the API state
`below_cutoff` and the availability key `upgrade` (saved grid URLs keep working).

| State | Wording (Why?) | Short label (poster, badge, row) |
|---|---|---|
| preferred target reached (`cutoff_met`) | Preferred quality met | Available |
| known or manual source ceiling reached (`best_available`) | Best available | Best quality known |
| provisional ceiling reached (`best_source_seen`) | Best source seen so far | Best quality known |
| higher source known, no accepted replacement (`higher_source_exists`) | No suitable release found; a better source is known to exist | Best quality known |
| accepted replacement found (`upgrade_available`) | Upgrade found | Upgrade found |
| target unmet, no replacement found (`below_cutoff`) | Preferred quality not met; no better copy found | Best quality known |

- The short label (`upgradeBadge`, `POSTER_STATUS_LABEL`) says what a person can act on. **Upgrade found** only when
  a better release was actually found. **Best quality known**: the copy holds a file and nothing better has been
  found for its profile (a movie with Remux-1080p whose profile prefers WEBDL-2160p, after a search that found
  nothing better); it is neutral or positive, never a warning. **Available** at the preferred quality. The longer
  wording stays in Why?.

- The status opens **Why?** (docs/design/ceilings.md): the cutoff line ("WEBDL-2160p · profile score 2,244 / 2,000 ·
  cutoff Remux-2160p"), what falls short, what Theatarr would take next, and the source-ceiling evidence.
- Poster badges word the row's `upgrade_state`; the grid filter is "Preferred quality not met"; the Home row
  **Upgrade found** (`discover` `upgrades`, and its "See all" page) lists only titles with an upgrade found: a
  monitored copy below its preferred quality for which a better release was seen (a series copy that still misses
  episodes counts by the files it has);
  season headers count "N under preferred quality · M upgrades found".
- Users without "see files" get availability and resolution only.

## The upgrade-evidence rule (`upgrade.go`)

Per item below cutoff (a movie version's file, or one episode's file):

1. Only evidence newer than the item's current file counts.
2. The newest search after the file that covers the item (a journal search of the title, its season or episode;
   a shadow runner search for an arr grab of those episodes) stands for every earlier search.
3. RSS sightings (journal RSS reads, shadow RSS picks) newer than both the file and that search add to it.
4. A release counts when it was accepted and marked an upgrade over the file (`upgrade` on the stored candidate),
   and maps to the item.
5. The best counted release (score, then newest) is what Theatarr would take next, with the engine's reason.

Hydration reads decision timestamps and coverage first, then fetches and decodes candidate blobs only for the
selected searches and subsequent RSS evidence of the requested versions' files below cutoff. Search metadata is
retained for `last_search`, even when it predates the file. Empty newer searches invalidate older finds, equal-time
searches keep the first record, and there is no history limit. The stored episode releases panel visits newest
decisions first and skips candidate decoding for older or non-covering decisions.

The cutoff a file is measured against is the owning arr instance's profile while an arr imports the version (that
profile decided the state), else the policy profile.

## Shadow ownership

`arr.import` rewrites a version's monitored flags and files from its owning instance every 15 minutes
(`docs/design/series.md`, Ownership). So a version with a `version_source` whose instance is still under `import.arr`
is **managed by that arr** while the instance stays under `import.arr`: its monitor switches show the arr's state read-only with that note, and
every monitor write refuses it (409, `PUT titles/{id}/versions/{key}` too). Searches keep the existing rule
(`catalog.Searchable`: mode active and `router.enabled`), and the episode releases panel answers from what is stored
unless the version is active. A shadow version Theatarr owns (no arr source) may be monitored here.

## API

Request pending, approved/auto-approved, declined, failed and available events queue webhooks through the existing notify outbox and retries; series also emit one partial-availability alert per request using the request detail's episode counts within the requested seasons. Native policy targets with no `events` filter receive them (the `versions` filter still applies), and `notify.enabled: false` records them as dropped; there is no per-user opt-in.

Payloads retain `eventType: theatarr.request_*` and use Seerr's `notification_type` names (`MEDIA_PENDING`, `MEDIA_APPROVED`, `MEDIA_AUTO_APPROVED`, `MEDIA_DECLINED`, `MEDIA_AVAILABLE`, `MEDIA_FAILED`), `event`, `subject`, `message`, `image`, `media`, `request` and `extra`, including joined requesters, title ids, seasons and copy keys/labels. Partial availability uses `MEDIA_AVAILABLE` with “Series Partially Available”; successful auto-approval skips the transient pending alert.

Discord and ntfy targets (`format: discord` or `format: ntfy`, docs/design/devices.md) get only available and partially available, as "**Title (year)** is ready in Plex, requested by <name>" ("is partly ready" for a partial series; plain text on ntfy). On a Discord target a requester with a Discord user id is mentioned (`<@id>`), and `allowed_mentions.users` lists only those ids, so nobody else is pinged. The id never reaches the request SSE stream or the theatarr.* payloads.

| Route | What |
|---|---|
| `GET titles/{id}` | per version `upgrade` (state, quality, score, cutoff, below/seen counts, next, last search) and `control` (mode, managed_by, can_edit, can_search with reasons, live_releases); a movie file's `current.media`: for the version's newest active link, Theatarr's own probe stored at import (`content_file.probe`, formatted as the arrs format theirs), else for an adopted link the media info its arr had recorded (`link.adopted_media`, 00117); the arr file's `media_info` only when the version has no active link |
| `GET titles`, `discover` | `upgrade_seen` per row and version; the Home row `upgrades` lists only titles with an upgrade found (seen), its notes say "upgrade found" |
| `GET titles/{id}/episodes` | per version `control` and `cutoff`; per season and version `below_cutoff`, `upgrade_seen`, `monitored`, `quality`, `size_bytes`, `score`; per cell `upgrade_seen` |
| `GET titles/{id}/episodes/{eid}` | the episode drawer: per version state, upgrade, control, file (release, quality, score, size, media, path; `media` of a link as for `current.media`), history, journal entries |
| `GET titles/{id}/episodes/{eid}/releases?version=&live=` | the releases panel for one episode |
| `PUT titles/{id}/seasons/{season}/monitor`, `PUT titles/{id}/episodes/monitor` | monitor a season or episodes, per version; arr-managed versions refused with the reason |
| `POST titles/{id}/seasons/{season}/search`, `POST titles/{id}/episodes/search` | search a season or episodes through the acquirer's selection |
| `POST titles/monitor` | the library's copy-targeted bulk monitor (docs/design/ui.md "Library views"): sets `monitored` on existing copies of up to 500 titles, `dry_run` previews, `expect` skips copies changed since the preview (stale); arr-managed copies come back `read_only`, a title without the copy `missing`; never adds a copy or searches. Manage only |

Callers without "see files" get availability only (`docs/design/users.md`): whether there is a file and its resolution,
no upgrade explanation, cutoff, history, scores, releases, next, paths, sizes or journal, and the write and releases
routes answer 403. The permission check is `canManage` in
`manage.go` (`auth.PermManage`).

## SPA

- **Version card:** the upgrade block (badge, "Bluray-1080p · 6,302 / 10,000 · cutoff Bluray-2160p Remux", what
  falls short, what Theatarr would take next or "No better release seen yet", "Show releases"), media, the monitor switch
  and search now (off with the reason), Interactive search for that copy (a movie, managers, a current copy that
  searches live; a series has it per season and episode), history, and the version's journal.
- **Seasons** (laid out phone first, docs/design/ui.md Title page / Episodes): a completion summary
  per season; behind its Details per version a line (have, missing, below cutoff, upgrades seen, size, upgrade
  status with Why?), a season monitor switch and "Search season"; All / Missing / Upgrade found filters; episode
  rows open the episode overview (`?episode=<id>`); checkboxes select episodes for bulk monitor, unmonitor and
  search. Managers get Interactive search on each season header and an icon button per episode row.
- **Episode overview** (a shared-Modal dialog): per version the state, monitor switch, search now, Interactive search
  (managers, a version that searches live), upgrade block, then File, History (with the journal) and Last search (the
  stored decision scoped to the episode, read-only) as closed disclosures.

## Checked

Go tests (`manage_test.go`): the rule's cases, the API on a seeded series and movie, shadow and arr-managed
versions get no writes and no searches (state compared before and after, zero acquirer calls), the active path, and
users get no scores.
