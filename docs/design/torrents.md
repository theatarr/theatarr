# The torrent manager (`internal/torrents`)

A library view of every debrid account's torrents, like Debrid Media Manager's: what
each torrent is used by, backups, restores and deleting the torrents a person picks. Migration
`00078_torrent_journal.sql` (and `00093_torrent_restore_pending.sql`). Commands `theatarr torrents …`; API `/api/theatarr/v1/torrents/…` (admin); UI
Admin › Torrents (`/admin/torrents`); backups and restore live at System › Backup (`/admin/system/backup`).

Nothing here deletes on its own: there is no automatic cleanup. None of it needs `router.enabled`: a restore is not a
grab. Reports and backups only read (GET through the gateway, like the registry sync).

## Usage

Each torrent on Real-Debrid, AllDebrid and TorBox is one of:

| Usage | When |
|---|---|
| **used** | a file of it is referenced: a library link of Theatarr (active or dead; by the content's or the download's hash, the hash in the link's file metadata, or the mount folder its target names), a current arr file (arr import: the content tied to it, else the arr's download id), a download that is queued or in flight (`queued`, `adding`, `added`, `downloading`, `ready`, `importing`), or a symlink into the debrid mount under one of `torrents.scan_roots`. The report lists which references (up to 20) and counts them all |
| **unknown** | Theatarr lacks the data: the provider was never listed completely by the registry sync, the hash is not in the registry yet (added since the last sync), a scan root could not be read, `torrents.scan_roots` is set without `library.mount`, library links could not be tied to a torrent, or the torrent has no v1 hash. The reasons are listed. Never offered as unused |
| **unused** | every file is unreferenced and none of the above applies |

Only the registry's own states are used (no second registry). Accounts are shared: another app or person on the same
account may use a torrent Theatarr calls unused. The delete confirmation says so every time.

### `torrents.scan_roots`

Libraries other tools manage (another Plex library, a second Theatarr): absolute directories walked for symlinks whose
target lies in the debrid mount; the folder each target names marks that torrent used. Only the links are read
(`readlink`), never their targets. A root that is, or leads into, the debrid mount (or one of its aliases) is refused
without touching it (`policy.ResolvePath`), and the torrents it might reference become unknown. Every read of a scan
root is bounded like the library's own scans: its resolution goes through the root prober
(`policy.RootResolver`, `policy.RootResolveTimeout`) and the walk through `library.WalkLinks` (each listing chunk,
batch of readlinks or lstat charged to its filesystem and bounded). A root on a share that stops answering ends its
scan with an error after one bounded wait (and at once while that read is stuck), so the report, a delete preview
or a delete never hang on it; any directory or link that cannot
be read makes the scan incomplete. An incomplete scan is a reason: the torrents it might reference are unknown and
never offered as unused (`TestScanRootBoundsHungResolution`, `TestWalkLinksStrictAndBounded`).

```yaml
torrents:
  scan_roots: [/mnt/media/other-library]
  backup:
    every: 24h   # scheduled backups; off when unset (the default)
    keep: 30     # newest kept; every backup from the last 7 days is kept too (default 30, max 1000)
```

## Report

`GET /torrents/report`, `theatarr torrents report [-json] [-group G]`. Per provider: every torrent with its id, hash,
name, size, added date, status, selected file count (`-1` when the list gives none and the registry has no files),
linked file count (library and scanned links), usage with references or reasons, and whether Theatarr's router added
it (a Theatarr download with this provider id that did not reuse an existing torrent). Each reference of a library
link, arr file or download names its title (`title_id`, `title`); `titles` lists the distinct titles of all the
references, also those past the listed 20, read with the references themselves (no extra query). Groups:

- **unused**;
- **duplicate hash**: the same hash twice on one account or on several accounts;
- **duplicate title**: the same parsed title and episode (Sonarr's parser) or title and year (Radarr's) across
  different hashes, with each one's quality. A film whose year the series parser reads as its episode
  ("The.Dark.Knight.Rises.2012" as S20E12, "Gladiator.2000" as absolute episode 2000) is parsed as the film: when the
  series result is nothing but the movie parser's year (1900 to next year) under the same title, and the name has no
  explicit series marker (`S01`, `S01E02`, `1x02`, "season", a daily date) and does not start with a `[Group]` tag;
- **failed** (the provider's error states), **stalled** (queued or downloading for over a day), **largest** (25).

An account that cannot be listed is reported with its error; its torrents are not guessed.

### Account limit

An AllDebrid account holds a limited number of magnets. `providers.<key>.magnet_limit` (AllDebrid only, default 5000,
1 to 1,000,000) is that number: the report gives each AllDebrid account `limit` and `near_limit` (count at or above
90% of it), Admin › Torrents shows "count of limit" in the provider strip and flags it near the limit, and the
`TorrentLimitCheck` health check warns from 90%, counting from the provider's latest complete registry sync (no
provider call). It is only a count: nothing is deleted, refused or rerouted because of it. zurg's notes give a
lower figure than the default, "AllDebrid takes at most 1000 magnets per account"
([local libraries](https://notes.debridmediamanager.com/zurg/guides/local-libraries/)), so set `magnet_limit: 1000` if
the account refuses adds there. Real-Debrid and TorBox have no limit here (`limit` absent).

## Backups

The web controls live at **System › Backup**, below Theatarr backups. These are saved lists of debrid torrents;
restoring re-adds them to a provider and does not change Theatarr settings.

`POST /torrents/backups` (queues job `torrents.backup`), `theatarr torrents backup`. A backup lists every configured
provider: per torrent the provider id, hash, name, size, added date, status and the selected files (id, path, size).
TorBox lists files with the torrent; Real-Debrid and AllDebrid lists do not, so a backup reads up to 50 torrents'
details per provider and reuses what the newest earlier backup holds for the same id and hash: a few backups fill in
every torrent. A torrent whose files the backup never read is restored with all its files.

- **Selection known or not** (format version 2). A torrent Real-Debrid still holds in
  `waiting_files_selection` (or `magnet_conversion`) has files but no selection yet: the backup marks it
  `selection_unknown` instead of recording an empty selection, and later backups do not reuse it (only settled
  selections are cached). A version 1 backup's empty known selection is read as unknown for the same reason. The
  torrents a delete removes are read again for its pre-delete backup whatever an earlier backup holds, and that
  backup must know each one's selection (the files, or that it awaits one)
  (`TestBackupRefreshesSelectionBeforeDelete`, `TestBackupV1EmptySelectionIsUnknown`).
- **Listing** reads a lightweight summary of each file (no file lists), cached per process by path, size and
  modification time (backups are written once, atomically). A restore, a download and the file cache read the
  whole backup and validate it: format, a version this build reads, the id it was written as, a provider on every
  list and a hash on every torrent.

- Stored in `torrent-backups/` next to the database: the directory `0700`, each file `0600`, written atomically. No
  keys are in them.
- Retention: the newest `torrents.backup.keep` (default 30) plus every backup from the last 7 days.
- Scheduled backups: off by default; `torrents.backup.every` (at least 1h) turns them on. `theatarr serve` checks every
  10 minutes and queues one when the newest scheduled backup is older.
- Listed in the UI with date, reason (`manual`, `scheduled`, `pre-delete`, `import`), counts and size; downloadable
  (`GET /torrents/backups/{id}`) as Theatarr's format or, with `?format=dmm`, Debrid Media Manager's (an array of
  `{filename, hash}`).
- Import (`POST /torrents/backups/import?provider=KEY`, `theatarr torrents import -file F -provider KEY`): a Theatarr
  backup, or a DMM backup with the account to restore it to. Stored as a backup of reason `import`; nothing is sent.
- A full backup is taken automatically before every delete (below).

## Restore

`POST /torrents/restore/preview`, then `POST /torrents/restore` with the preview's `confirmation_token` (queues job
`torrents.restore`; a session or an admin's API key, like the delete); `theatarr torrents restore -id B` previews, `-confirm TOKEN` runs. From a stored, downloaded or
DMM backup; optionally one provider's list (`provider`), another target account (`target`) or some hashes.

The preview says per torrent: already present (`present`), will be re-added (`readd`), or skipped: `skip_invalid`
(not a v1 hash), `skip_target` (the target account cannot be listed). The token binds the
set of re-adds (digest `r1-<count>-<digest>`) to the caller (`r2-…`, as for the delete, step 5 below); the job plans
again and adds only hashes in the confirmed set that are still missing.

- **Every entry is re-added** (privacy is a label). A backup holds hashes with no release; `adder.Restore`
  makes their bare magnet from the hash alone. There is no privacy scan and no proof: the backup is the user's own
  account list.
- **Pacing.** Each add takes a slot from the provider's pacing row (`router.ReserveAdd`, the same rows the router
  uses: `adds_per_minute`, `uncached_adds_per_hour`, back-offs and cool-downs) and records its outcome
  (`router.RecordAddOutcome`), on top of the gateway's budgets. The slot is taken under the router's add lock, just
  before the add is journaled and sent; while the provider is paced the lock is let go, and the slot (with any hold or
  back-off a route started meanwhile) is checked again once it is held, so a route's add while the restore waited
  still gets its `min_gap` (`TestRestorePacingReservedUnderAddLock`). A wait over 15 minutes, or an answer for the account
  (traffic, auth, too many active, rate limit, account hold), ends that provider's part of the run. A definite
  refusal counts against just that release only for a recognised content code. Bare HTTP 4xx and unrecognised
  provider codes back the provider off without caching the hash as a miss, as in the router. A definite refusal
  means the add did not land; it does not establish that the release is bad. Uncertain failures (no answer, a 5xx)
  also back it off (`TestRestoreUnknownRefusalBacksProviderOff`).
- **Files**. Each preview item says its `selection`: `files` (the backup's selected files, matched by
  path to the new torrent's files on Real-Debrid), `all` (the backup never read the files, as with a Debrid Media
  Manager import or a torrent past the detail budget: every playable file is selected, the router's rule,
  `router.Playable`, never the nfo, text, images or subtitles; see router.md "File selection on Real-Debrid"), or
  `unknown` (the backup knows the files but no selection: Real-Debrid awaited one when it was backed up, or the list
  is empty). An unknown selection, one none of whose paths is in the new torrent, or an `all` torrent with no
  playable file is never taken as every file: the torrent is added and left waiting for the person's selection,
  outcome `incomplete` (selecting the nfo and images alone could make Real-Debrid pack it into a RAR). AllDebrid and
  TorBox take every file (`TestRestoreSelections`, `TestRestoreAllSelectsPlayableOnly`).
- **An uncached known selection**. Real-Debrid caches per selected file set. A `files`
  selection is tried exactly first: it was cached when the backup was taken. A known strict subset of the playable
  files stays exactly as recorded, even uncached: a deliberately selected episode must not expand to its whole pack
  (`TestRestorePartialSelectionKept`). A selection whose playable part is such a subset but that holds other files
  too (episodes with their `.srt`, a movie with its nfo: files Real-Debrid packs into a RAR) is retried as below
  with that playable part alone, still `files`: the pack's part the backup held, never the whole pack (`TestRestoreMixedPartialSelectionKeepsItsPart`). For other selections, when the torrent is still not downloaded one
  `router.cached_grace` after the selection, the restore deletes its own add (`DeleteOwnUncachedAdd`:
  Real-Debrid makes a new torrent per add, the run's plan found the hash absent, and no registry row or download
  knows the id) and adds the torrent once more with every playable file, the set DMM, decypharr and zurg select and
  so the one most likely cached. The first add is counted uncached in the shared pacing and its journal row
  completed as `uncached` with that detail; the second goes through the same pacing slot wait and has its own row;
  the item then shows selection `all` (`files` for a playable part) and the second add's outcome. One retry at most. None when every playable file
  is the set already selected, when the provider is paced past `restoreWait` (the torrent would be deleted and not
  added again), when the delete fails (the first add stays, `uncached`, with the reason), or on AllDebrid and TorBox
  (`TestRestoreKnownSelectionFallsBackToPlayable`, `TestRestoreKnownSelectionRetryLimits`).
- **Pending selections** (migration `00093_torrent_restore_pending.sql`). On Real-Debrid the add
  answers with the new torrent's id, so that torrent is provably the restore's own: it is recorded in
  `torrent_restore_pending` (with the selection to apply) before its files are read and selected, and the row goes
  once the selection is made. A failed detail read or selection (or the process ending) leaves the row and the
  outcome `incomplete` ("restore it again to resume the selection"). The next restore of that hash, instead of
  skipping it as present, plans it as `resume` (in the confirmation token, counted in `resume`) and selects the
  files on exactly that torrent; one selected meanwhile on the site counts as done, and rows of torrents the
  provider no longer lists are forgotten (`TestRestoreResumesPendingSelection`).
- **The job** fails when any re-add failed or is `incomplete`, saying how many still wait for their file selection;
  `theatarr torrents restore -confirm` exits with an error then.
- Each re-add is journaled with its outcome: `added` (cached), `uncached` (added and downloading, or refused as not
  cached), `incomplete` (added, still waiting for its file selection), `failed`, `skipped`. The row of an add that is sent is written first with outcome `sending` (it keeps that
  outcome if the process dies before the answer), and the add goes out under the router's add lock
  (`Router.AddLock`, held by every route and poll; the CLI has none). On AllDebrid an upload of a hash already on the
  account returns that magnet, so a restore landing between a route's look at the account and its upload looked
  like the route's own new add, and with `router.alldebrid_delete_uncached` the route deleted it. The lock keeps them
  apart in `theatarr serve`, and the router treats an entry whose hash a restore sent since its look as not its own
  (`router.RestoredSince`, from any process; `TestRestoreAddIsAnnouncedAndLocked`).

## Delete selected

`POST /torrents/delete/preview`, then `POST /torrents/delete` with the preview's `confirmation_token` (a session
or an admin's API key, which confirms like a session, as with settings; the token is bound to the set and the caller);
`theatarr torrents delete -torrent PROVIDER:ID:HASH …` is a dry run until `-confirm TOKEN`. Up to 200 torrents.

1. **Only the chosen ids.** Each selection names the provider, id and hash the report showed.
2. **A fresh list:** the id must still be there with the same hash, or it is refused.
3. **A fresh usage check:** used and unknown torrents are refused unless the selection sets `override` with the
   item's `expected_override_token`. That token is a digest of the torrent's references (or unknown reasons) as the
   preview listed them: the second confirmation names what is being overridden, and a new reference since the
   preview invalidates it.
4. **Recent or busy:** a torrent added less than 24 hours ago (or with no date), or still queued or downloading, is
   refused unless `allow_recent`.
5. **The confirmation** is bound to the exact set and the caller. The plan digest (`d1-<count>-<bytes>-<digest>`)
   covers count, total size and a digest of each deleted torrent's provider, id, hash, size, usage and flags. Any
   change answers 422 with the new preview. The token a preview returns (`d2-…`, restore `r2-…`) is bound to the caller: an HMAC, under a random key made once per process and never stored, of the caller's confirmer (`user:<id>` for a session, `key:<id>` for an API key) and the plan digest. Restore and delete plan again and compare against the HMAC for the current caller: no preview, no token; another key, another user or a session gets 422 (`ErrConfirmation`) from the same token; a revoked key cannot authenticate. After a restart or a changed plan, preview again. The CLI (no confirmer, previewing and confirming in two processes as the local operator) gets the plain digest instead.
   Override tokens (step 3) are not bound to the caller.
6. **A full backup** of every provider (reason `pre-delete`) is taken first and must hold every torrent being deleted
   with its files. No backup, no delete (503, nothing deleted).
7. **A last usage check per torrent**, right before its own delete (after the 250 ms pacing wait), under the
   router's add lock (`Router.AddLock`), which each delete holds from that check until the provider answered: a torrent a route reused
   or re-added while the backup ran (on AllDebrid an add of a hash on the account hands back the same magnet), or
   that a link or arr file references by now (also one written while the earlier deletes of the batch ran), no longer
   matches the confirmed usage and is skipped ("preview again"); no route adds between a torrent's check and its delete (`TestDeleteRechecksUsageAfterTheBackup`, `TestDeleteRechecksEachItemBeforeItsDelete`). The scan roots are
   walked once after the backup; the references Theatarr writes are read again for every torrent, only the rows that
   torrent's usage can see (its hash, its release folders, and the links with no hash), in the same order as the
   full read, so its usage and override token are the same (`TestScopedRefsMatchFullIndex`).
   **The reference fence** (`internal/torrents/reffence`): the writers of those references hold it
   around their write transaction: library link creation and retargeting (an import, a manual import, adopt, a
   relink). A delete holds it from its last usage check until the
   provider answered (that call is bounded to 4 minutes), so no reference lands in between. In one process it is a
   read-write lock: writers wait for the one delete in flight and a delete waits for the writes in progress. Across
   processes (the CLI and the service) the delete also holds the lease `torrents:delete` in `library_lease` (5
   minutes, written before the check); a writer's transaction refuses while another process holds it (a link write
   as a link change: the import retries, a client gets 409), and a
   second process's delete skips its torrents. No SQLite transaction stays open across a provider call
   (`TestDeleteFencesReferenceWriters`, `TestDeleteFenceLeaseAcrossProcesses`).
   A library link is a reference on disk before its row records it, so the writers that make or repoint a symlink
   (an import, adopt, a relink) take the fence before the filesystem change and hold it
   through their commit (`reffence.Begin`). Across processes such a writer also writes a lease of its
   own (`torrents:write:<owner>/<n>`, 5 minutes, refused while another process's delete holds its lease, checked
   again at its commit); a delete waits up to 15 s for the link writes in progress, this process's (the in-process
   lock) and other processes' (their leases), then skips its torrent ("a library link write is still in progress";
   also when its own ctx ends). It takes the fence before the router's add lock, so a link write stuck on a share
   that stopped answering holds up neither routing nor the writers that come after it, and a writer waiting for a
   delete gives up when its own ctx ends (`TestHoldBoundedByLocalWriter`,
   `TestDeleteDoesNotHoldAddLockWhileWaitingForWriter`). A writer that waited for a delete of this
   process checks again that its target is still on the mount (an import plans again), so no link points at a
   torrent the delete took (`TestAdoptNoSymlinkDuringForeignDelete`, `TestAdoptWaitsForDeleteAndRechecksTarget`,
   `TestImportNoSymlinkDuringForeignDelete`, `TestRelinkNoRepointDuringForeignDelete`,
   `TestDeleteWaitsForForeignLinkWrite`).
8. **Deletes** run one by one through `adder.UserDeleter.DeleteUserChosen` (Real-Debrid `DELETE /torrents/delete/{id}`,
   AllDebrid `POST /v4/magnet/delete`, TorBox `POST /api/torrents/controltorrent` `{"torrent_id", "operation":
   "delete"}`), 250 ms apart per provider within the gateway's budgets; an answer for the account or the transport
   stops that provider's part. Restores and deletes in one process never run at the same time.
   TorBox answers a delete of an id that is already gone with HTTP 500 `DATABASE_ERROR` (and its single-torrent read
   with 500 too; [notes: torrent lifecycle](https://notes.debridmediamanager.com/zurg/internals/torrent-lifecycle/)),
   so after a 5xx on a TorBox delete the account's list is read again: a list without the id or hash counts as
   deleted (the item's `note` and the journal say how it was settled); a failed read or a listed torrent stays a
   failure and stops the provider's part (`TestTorBoxDeleteServerErrorSettledByList`).
9. **Journaled:** every delete in `torrent_journal` with who, when, provider, id, hash, name, size, the backup id, the
   outcome and any override. `GET /torrents/journal`, `theatarr torrents journal`.

### The third delete path

`internal/provider/adder/delete.go` holds every provider delete. Besides the router's two (Theatarr's own just-made
uncached add, its own stalled RD add) there is `DeleteUserChosen`, which takes a `UserChoice` only
`adder.ChooseForDelete` makes, from an id, a v1 hash, a backup id and a confirmation digest. Fences:

- `TestNoDeletes`: the method sets, paths and HTTP methods per provider (three deletes per kind's set), and
  `Adder` does not include `UserDeleter`.
- `TestOnlyRouterImportsAdder`: only `internal/router` and `internal/torrents` import the adder package.
- `TestOnlyTorrentsDeletesChosen`: only `internal/torrents` names `ChooseForDelete`, `UserChoice`, `UserDeleter`,
  `DeleteUserChosen` (as any selector) and `Restore`; the router cannot reach this path.
- `TestDeleteUserChosenRefused`: a zero choice or one missing its backup or confirmation sends no request.

## UI

Admin › Torrents: a strip per provider (torrents, of the limit on AllDebrid, size, unused), provider tabs, group filters (unused, duplicate
hash, duplicate title, failed, stalled, largest), a name or hash filter (every word in the name or parsed title, in
any order, or the text as a hash prefix; `?q=` in the address fills it, so other pages link to one torrent: a title's
**Torrent** link sends the hash's first 12), sort by size, age or name, under "All providers" a **Provider** column
with the account's tag (RD, AD, TB, as **Cached on** shows it; the account key as tooltip), a usage column whose
references show on hover and expand on click (the expanded references link their titles to the title page), one
table at every width, multi-select and **Delete selected**: the preview opens a
confirmation with the count, size, the backup it takes first and the shared-account warning; refused torrents are
listed with why, and used or unknown ones with a checkbox naming their references to override. **Saved inventory** (`/admin/torrents/inventory`) shows known hashes and provider holdings.
The torrent journal remains below the provider list. **System › Backup** holds torrent-list backups: **Back up
now**, import (Theatarr or DMM, to a chosen provider), the list with download (Theatarr or DMM format) and **Restore…**
with its preview. Code: `web/src/pages/admin/torrents.tsx`, `web/src/components/backups/torrent-backups.tsx`, `web/src/lib/torrents.ts`.

## Out of scope

Automatic cleanup; changes to the router's two delete paths; anything in the router.

## Scheduled backups control

System › Backup has a **Schedule** panel beside the torrent-list backups. The shared settings form offers
backup interval presets or a custom duration (at least one hour), and **Backups to keep** (1–1000, default 30).
Off removes `backup.every`. Saving reads fresh settings and replaces only the `backup` field, preserving
other torrent settings and unknown fields in the draft. The summary refreshes after a successful save, with
its Saved notice retained. Conflicts require reloading; changes needing confirmation
use the same confirmation dialog. Retention also keeps backups from the last week.
