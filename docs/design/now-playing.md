# Now playing

**Now playing** in the web UI (`/now-playing`, `GET /api/theatarr/v1/nowplaying`) shows what Plex plays now: who watches
on which player, how Plex streams it, and whether it plays smoothly. When it does not, it names the likeliest cause.
For managers and admins it also shows where the file comes from: Theatarr's link, the versions, the release, the
providers holding the content (marked **single copy** when only one does) and the zurg account serving it. It also
shows the mount's fetch speed and the **next-episode pre-warm** (a head check, then the whole file read into the
mount's cache). All state is kept in memory: there is no history
table and no migration.

Code: `internal/nowplaying` (tracker, mapping, verdict, pre-warm, rclone rc client), `internal/plex/sessions.go`
(sessions and the notification websocket), `internal/library` (`ZurgAPI.Stats`, the log followers' `OnLine`,
`Library.Prewarm`), `cmd/theatarr/servenowplaying.go` (the `nowplaying` live-settings worker), `internal/web/nowplaying.go`
(the API), `web/src/pages/now-playing.tsx` (the page).

## Settings

| Setting | Default | What it does |
|---|---|---|
| `library.plex.now_playing` | true | Follow Plex's sessions. Needs `library.plex` with its token. Now playing only reads, so it runs even while `library.enabled` is off. |
| `library.plex.prewarm` | true | The next-episode pre-warm (below). Needs `now_playing`. |
| `library.plex.prewarm_rate` | `25MB` | The pre-warm's whole-file read is capped at this many bytes per second (`25MB` is 25 MB/s), so it never starves a live stream. A size with a unit (`10MiB`, `25MB`) or a number; at least `1MB`. |
| `library.plex.prewarm_max_size` | `20GB` | A next episode larger than this is not read whole; its head check still runs. |
| `library.mount.rclone_rc.url` | unset | The mount's rclone remote control (`rclone rc`, e.g. `http://rclone:5572`). Optional: without it there is no fetch speed and no cache state. It is an outbound host with its own gateway target class `rclonerc` (`rclonerc:rc`). |
| `library.mount.rclone_rc.username_env`, `password_env` | unset | The rc basic-auth credentials (`--rc-user`, `--rc-pass`), by environment variable name. In DB settings mode they are write-only secrets (`mount.rclone_rc.username`, `mount.rclone_rc.password`). They go in the Authorization header only and are never logged or returned. |
| `library.mount.rclone_rc.budget` | unset | A budget for rc calls. |
| `library.mount.rclone_rc.forget_release_root` | false | Lets an import clear rclone's cached listing of the release root (`vfs/forget`), the one rc call that changes state, and only rclone's cache: the cached folders under the root and the parent's listing are listed again when next read ([design/library.md](library.md), "Import of a ready download"). |
| `library.mount.zurg_api` | unset | Adds zurg's `/stats` (open streams, blocked reads) and, for a file Theatarr has no link for, its library entry. |
| `library.mount.zurg_log` | unset | Adds zurg's log lines about the playing files (below). |

The tracker is a live-settings worker (`nowplaying`, `cmd/theatarr/serveliveworkers.go`). Its key is the Plex URL and
token, `zurg_api`, the mount paths, `prewarm`, `prewarm_rate`, `prewarm_max_size` and the rc URL and credentials (secrets hashed). A save that changes any
of them restarts it, and a save that leaves them alone leaves it running. Requests go through the gateway of the
generation in force.

## What it reads

- **Plex sessions**: `GET /status/sessions` every 10 s while someone has looked at the page (or the API) in the last
  minute, every 30 s otherwise. The first look after a quiet spell reads at once. A playback's selected media and
  part give the file, bitrate, resolution and stream decision, and `TranscodeSession` gives the transcoder's speed.
  Plex may leave a video's `Part.file` out of the session; then `GET /library/metadata/<ratingKey>` gives it, the
  part matched by the session's selected media and part ids.
- **Plex notifications**: the websocket `/:/websockets/notifications`, read only (the only frames sent are pongs to the
  server's pings), through the gateway with a one-hour request lifetime. The connection is closed as soon as the
  tracker stops (a settings restart). It is reopened after 5 s, doubling up to
  5 min while it keeps failing. Its `playing` notifications are the only source of **buffering** (sessions rarely show
  it). A state change sets the playback's verdict at once and triggers a sessions read, which finds its cause, as does
  a notification for a session the tracker does not know.
- **zurg `/stats`** (with `zurg_api`): `active_streams` and `blocked_reads`, the files zurg refuses reads of.
  Blocked-read status accepts numeric or textual values from zurg; Theatarr reports it as a string.
- **zurg's log** (with `zurg_log`): the library's log follower passes each line on. Lines about a playing file are
  kept for 10 minutes: `Large transfer` (which account served it), `Cut a read` (a starved read from a slow delivery
  node), `Source state changed ... [ok_file → broken_file]` and `is broken; read retry window elapsed`, `Download link
  broken ... re-unrestricting`, `Provider rate limited while unrestricting`, and `Account ... is cooling down`, which
  names no file and counts when the account is the one serving the file.
- **rclone rc** (with `rclone_rc`): `core/stats`, whose `transferring[]` is what the VFS cache is downloading now, with
  its speed. The playing file's speed over its bitrate is the **fetch ratio**. `vfs/stats` is read every minute for the
  disk cache (full, errored files). rclone's rc takes POST even for reads; the tracker calls nothing else (an import's
  `vfs/forget` with `forget_release_root` is the library's, not the tracker's).

## From Plex's file to Theatarr

Plex's `Part.file` is read as a symlink (only outside the mount: a readlink inside it would be a FUSE call), relative
targets resolved. The target's path under the release root (`<all_dir>/<folder>/<file>`) is joined to
`link.target`, written as Theatarr writes it (`link_path/<all_dir>/...`), not to the link path. That way a Plex
library that still points at the arr roots finds the same link. If that fails, the link whose path is Plex's file is
used. When several versions link the same target, the one whose root (`versions[k].library`,
`library.versions[k].roots`), or the root of the version it follows, holds Plex's file is the one playing; when that
does not decide it, the source lists every version sharing the target. The link gives the title, its versions, release
and quality, its episodes and, through `content_file`, the content with its hash and the providers in
`content_provider` whose state is `present` (an `absent` provider is neither listed nor counted for single copy). Without a link the release folder is looked up as
`content.mount_folder`, and then zurg's `GET /api/library/entry` names the torrent hash. Only the hash is used: the
signed streaming path is neither read nor logged. `source.matched` says which way the file was found (`link`, `content`,
`zurg`, or empty). Sources without a Theatarr link are resolved again at most every two minutes (`resolveEvery`), not on
every poll, so a link adopted during the current playback supplies its version and next-episode pre-warm within that
time without restarting playback.

## Health verdict

A playback is **ok**, **paused**, **buffering** (Plex says so now, or a buffering notification came in the last
2 minutes) or **slow**. Slow means that between two sessions reads at least 5 s apart while playing, the position moved
less than 0.8 of the wall time. A jump of more than 3x is a seek and is not measured, and a pause ends the sample. For
a buffering or slow playback the cause is the first that holds:

1. **transcode**: Plex transcodes at a speed under 1x and is not throttled (throttled means the transcoder is ahead).
2. **provider**: zurg blocks reads of the file (`/stats`); or its log names the file (starved read, broken copy,
   broken link, rate limit) or a cooldown of the account serving it.
3. **mount**: the library's mount check fails (`Library.MountHealthy`, which includes zurg's `/ready`), or the rclone
   cache has errored files. A full cache alone is normal: rclone evicts old files as needed.
4. **provider**: the mount fetches the file at under 1x its bitrate (after the mount, whose problems also slow the
   fetch).
5. **network**: a relayed client (Plex's relay caps bandwidth).
6. **unknown**: no diagnostic evidence identifies the cause. The detail suggests checking the remote connection or
   upload, or the local player or home network, without attributing the failure to them from topology alone.

The buffering count shown is the changes to buffering in the last 10 minutes.

## Next-episode pre-warm

The tracker warms the next episode of an episode Theatarr linked in two steps, each once per Plex session and link:
a head check about a minute in, which gives the early alert for a dead copy, and a whole-file read once the episode
is past half, which puts the next episode in rclone's VFS cache so it plays from local disk.

**Head check.** About a minute into the episode (`viewOffset` ≥ 60 s), one at a time, bounded to 2 minutes:

1. The next episode in the **version's order**: by absolute number when the series type is anime (the title's,
   `docs/design/series.md` "Series type") and all regular episodes have one, skipping specials (a special numbered between
   two regular episodes is not the next one), otherwise by season and number so unnumbered episodes are not dropped.
   While a special plays, specials stay among themselves. Episodes removed from the metadata source are skipped.
   It must have aired and be monitored for the
   version, and the series version itself must be monitored, otherwise the pre-warm is skipped.
2. Its active link in the **same version**. With no link the next episode is missing: an error-inbox item. A
   non-symlink link (a local file) is skipped.
3. The **registry**: the content is found as the source mapping finds it (the link's `content_file`, else the release
   folder as `content.mount_folder`, else zurg's entry hash). Content marked `gone` or `absent` is an error-inbox
   item. With `zurg_api`, zurg's library entry for the file is checked too: no entry is an item.
4. **One read** of the file's head through the mount (`Library.Prewarm`): one sequential read from offset 0 of at most
   4 MiB, the file closed at once, under the mount guard and in a helper process when the mount has them. It is not
   the import's header probe, whose reads seek. zurg resolves the file's link and rclone caches its head before Plex
   asks for it, so a dead copy fails now and not when the episode starts. A target off the mount or a read error is an
   error-inbox item. A zero-byte head read fails as an empty file; it never counts as warmed.

Error-inbox items have source `job`, component `nowplaying.prewarm` and the key `<version id>:<episode id>` (the
inbox's `Event.Key`), so each episode gets one item, raised again only as a repeat of it. The pre-warm never calls a
provider: no add, no cache check, no restore.

**Caching read.** Once the episode is past half (`viewOffset` over half its `duration`) and its next episode passed the
head check, the tracker reads that file whole through the zurg mount, in order from its start to its end, and drops
the bytes (`Library.CacheFile`): rclone's full-mode VFS cache keeps it. The read goes in pieces of 32 MiB, each a call
under the mount guard (in a helper process when the mount has them), capped at `prewarm_rate` bytes per second. Only
one runs at a time, server-wide. A file over `prewarm_max_size` is not read (its state says so). Once started it runs
to the end, whatever the sessions do: stopping playback or switching to something else does not stop it (only
Theatarr stopping does). This is simpler than stopping when nobody plays the show, and costs at most one unneeded
file of up to `prewarm_max_size`, which rclone evicts. A read error mid-file is an
error-inbox item (the same key as the head check's) and stops the read. Start, finish (or stop), bytes and duration
are logged. Nothing is deleted from the cache: rclone evicts on its own (LRU, its size cap).

New next-episode reads (head checks and caching reads) wait while any playback's health verdict is **buffering**
with cause **provider** or **mount**. Buffering attributed to the client, network or transcoder, paused sessions,
and a full rclone cache do not block them. Deferred reads are eligible on the next poll after the problem clears.
An ongoing read still runs to completion even if buffering starts. The rate cap paces Theatarr's reader; rclone's
read-ahead can fetch upstream data ahead of that reader.

The page shows the pre-warm's state: `running` (head check), `ok` (head check passed; the detail says when the file is
not read whole), `caching` with its progress (`caching 42%`), `cached`, `failed` and `skipped` with the reason.
The `ok` detail is shown when only a head check was done because the file exceeds `prewarm_max_size`.
An ongoing cache read's progress is shared with viewers of the same series version whose next episode is the file
being cached, including when the viewer who started it leaves or switches shows. Viewers of other episodes, titles
and versions do not receive that progress.

## Who sees what

`GET /nowplaying` needs the browse permission. Managers and admins (see files) get everything: every playback, its
source, fetch and pre-warm, the cause detail, zurg's stream count and the rclone cache. Users get only their own
playbacks, matched on the Plex account linked to their Theatarr account (plex.tv id or name), with the verdict and cause
but no detail, and of the source only the title (for the link to its page). Plex's error text is replaced by "Plex
does not answer". While the tracker is off the answer is `{"enabled": false, "sessions": []}` and the page says what
turns it on: admins get the link to Plex & playback, everyone else is told to ask an administrator (the settings
page is admin-only).

Admins also get a **Now playing** card at the top of Home (`web/src/components/now-playing-card.tsx`): one line per
playback (what, who, player, position, health badge), each linked to the page. It shows nothing while the feature is
off or nothing plays. Its reads count as a look at the page, so the tracker polls every 10 s while Home is open.

## Not in it

There is no play history (and no Tautulli), no movie or details-page warming, no provider adds or restores, no
changes to zurg, Plex or the mount, and no mid-stream failover.

## Tests

`internal/nowplaying/nowplaying_test.go` covers log patterns and attribution, next-episode order (absolute order,
specials, anime specials skipped, late-added episodes), mapping by symlink and by path with the registry, present
providers and single copy, shared targets by version root, the pre-warm (one read, skipped when not aired, inbox items
for a missing link, gone or absent content found by folder or hash, a missing zurg entry, a target off the mount and
an unreadable file), the verdicts (set at once by a notification) and their causes in order, seeks and pauses, the rc
client with basic auth, and the polling speed-up. `internal/plex/sessions_test.go` and `sessions_ws_test.go` cover
session parsing (the file from the item's metadata when the session has none) and the websocket (upgrade check,
fragments, pings answered with pongs, close, size limit, closed on cancel through the real gateway).
The caching read's tests cover the start past half, once per session and link, progress, the size cap, a read error
and that it runs to the end after its session ends or moves on. `internal/library/nowplaying_test.go` covers the head read (at most
4 MiB from offset 0, closed at once, in process and in a helper) and the whole-file read (in order, rate cap,
progress). `internal/web/nowplaying_test.go`
covers the redaction for users, `web/src/lib/now-playing.test.ts` the page's labels and
`web/src/components/now-playing-card.test.tsx` the Home card.

## Connect rclone

Admins see **Connect rclone** on Now playing when no rc connection is configured, or a compact host and Edit
link when one exists. It adds fetch speed and cache state of the playing file. Enter the rc URL, username and
password, then **Test connection** or **Save**. Credentials are never pre-filled; blank fields keep stored values.
Saving patches `library.mount.rclone_rc.url` while keeping its budget and other library settings. Credentials
travel as section secrets in the same PUT as the parent object; no `_env` fallback names are required.
Changing the address requires re-entering each stored credential or selecting **Remove saved credentials**
before either Save or Test. Removal clears stored values and environment fallbacks. In YAML mode an unset
connection shows no card; a configured connection has only the read-only host line.

`POST /api/theatarr/v1/nowplaying/rclone/test` requires admin and accepts `{url,
username?, password?, use_stored_username?, use_stored_password?, use_stored?}`. It calls rclone's read-only
`core/version` with POST and basic auth, through a temporary gateway sharing the runtime's budgets and
egress state, with a five-second deadline. It returns `{ok, version?, error?}` without credentials or
upstream error bodies. Each `use_stored_*` flag resolves its blank field independently from the settings
snapshot, so a new password can be tested with the stored username. The `use_stored` flag requests
both. Stored credentials are refused at a changed URL; tests at a new address use only explicitly entered
credentials. The test never saves settings. Saving follows the usual stale-settings reload and effects
confirmation flow.
