# Now playing

**Now playing** in the web UI (`/now-playing`, `GET /api/theatarr/v1/nowplaying`) shows what Plex plays now: who watches
on which player, how Plex streams it, and whether it plays smoothly. When it does not, it names the likeliest cause.
For managers and admins it also shows where the file comes from: Theatarr's link, the versions, the release, the
providers holding the content (marked **single copy** when only one does) and the zurg account serving it. It also
shows the mount's fetch speed and the **next-episode pre-warm** (a head check, then the whole file read into the
mount's cache). The playbacks are kept in memory only; the one thing kept in the database is a file that broke while
someone played it (table `playing_break`, below).

Code: `internal/nowplaying` (tracker, mapping, verdict, pre-warm, rclone rc client, `rescue.go` and `incidents.go` for a
file that stops working), `internal/plex/sessions.go`
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
| `library.plex.stop_with_message` | true | "Stop a broken stream with a message" (Settings › Plex & playback): a playback whose file Theatarr found broken is stopped with a message its player shows (below, "Telling the viewer"). Off: nothing is ever sent to Plex; the check, the repair, the list below, History and the notifications go on as before. Needs `now_playing`. Read at each message from the settings in force, so a save needs no restart. |
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
- **zurg `/stats`** (with `zurg_api`): `active_streams` and `blocked_reads`, the files zurg refuses reads of. A blocked
  read's `failure` object (zurg 48c6d3c on: `scope`, `provider`, `provider_code`, `next_retry`) goes into the words only
  ("zurg refuses reads of the file: cooling down (Real-Debrid, account, code …, next try 14:05 UTC)"); nothing is decided
  by it.
  Blocked-read status accepts numeric or textual values from zurg; Theatarr reports it as a string.
- **zurg's log** (with `zurg_log`): the library's log follower passes each line on. Lines about a playing file are
  kept for 10 minutes: `Large transfer` (which account served it), `Cut a read` (a starved read from a slow delivery
  node), `Source state changed ... [ok_file → broken_file]` and `is broken; read retry window elapsed`, `Download link
  broken ... re-unrestricting` (with or without the error zurg 2ddd439 adds after the file, or after
  "re-unrestricting": the file ends at its first ": "), `Provider rate limited while unrestricting` (the file ends at
  ";" or ": "), and `Account ... is cooling down`, which
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
`library.versions[k].roots`) holds Plex's file is the one playing; when that
does not decide it, the source lists every version sharing the target. The link gives the title, its versions, release
and quality, its episodes and, through `content_file`, the content with its hash and the providers whose
account holds it in the record (`provider_torrent`; one whose listing no longer has it is neither listed nor counted
for single copy). Without a link the release folder is looked up as
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

## A file that stops working while it plays

When the file someone plays breaks on the debrid side, Theatarr checks it at once and replaces it, instead of
leaving it to the hourly sweep's two misses and zurg's repair grace (`internal/nowplaying/rescue.go`; the library's part:
`docs/design/library.md` "A file that broke while someone played it"; the search: `docs/design/router.md` "A file that
broke while playing is searched at once").

- **Which file.** Only a file that is certain: Plex's own path is an active, tracked Theatarr symlink into the mount,
  and its library is decided (the link's only library, or the one whose roots hold the path). A regular file, an arr's
  or any untracked symlink (even one in a library root pointing at a Theatarr link's file) or a link whose libraries
  the path does not decide is never checked, nor is a session that lists several media or parts without saying which
  one plays (it still shows in Now playing). The hash it is copied and its
  siblings found by is the one the link has stored (an adopted link's source included), else the one release the
  registry records in its folder; one only guessed from a folder name gives no copy and no sibling check.
- **The signs.** Plex says buffering; the playback ends below 90 % of its duration (seen where a poll drops the
  session; a failed poll is not a stop); with zurg, `/stats` lists a blocked read of the file, or its log calls the
  file broken or its download link broken. Not slow playback, starved reads, rate limits, cooldowns or pre-warm
  failures. Pause on or a library that writes nothing: no check. One check runs at a time, in one worker beside the
  poll (the poll, the websocket and the sweep never wait for it); a live playback's check goes before a stopped one's;
  a file that read fine (or gave no verdict) is not checked again for 2 minutes. Finishing an incident (its search
  ended, the old copy read again) is the library's own maintenance (`docs/design/library.md`), never this worker's,
  so it goes on with now playing off or Plex not answering, and no check or message waits behind it.
- **The reads** (`Library.ReadPiece`): 256 KiB through the mount, never from a provider, at the play position plus
  64 MiB but never into the second half of what is left (players read the tail on open, so it is cached), with a 15 s
  limit. A read error, zero bytes where the file has them, or no answer in time is a failure; a time-out kills only
  that read's helper and counts neither toward the mount's breaker nor toward the helpers' back-off, and no read starts
  while a helper so killed lingers (without helpers: none after two abandoned reads). An open breaker, every helper busy
  or held back, a cancelled read: no verdict, the check ends quietly. Read 1 fails; 20 s; read 2 fails; the mount still
  lists (`MountListed`, with zurg's `/ready`); the control read (below); read 3 of the same range fails. Any read that
  passes ends the check: nothing is marked, stopped or recorded. That is about a minute from the sign. After read 1
  the range comes from the mounted file's size when it can be read (Plex's may be an older file's at the same path); a
  read 1 that started at or past the file's end is no failure, and the count starts again at the corrected range. A
  file gone from the mount keeps its range, and its reads fail as any unreadable file's; a size lookup failing for
  another reason gives no verdict.
- **Mount, account or release** (the control read, at mid-file of another release). The serving account is the account
  zurg last served the file from (its log's "Large transfer" line for the file's whole path under the release root, not
  another release's file of the same name), else
  the release's only complete holder in the record (state downloaded at 100 %). A release only it holds reads: only
  this release is broken. It fails: one only another account holds is read; that reads: the account is out; it fails,
  or there is none: the mount, and nothing is done. zurg saying the account cools down, in a line whose own time (the
  RFC 3339 stamp it starts with) is at or after the time read 1 started, also means it is out (an older line, or one without
  a time, means nothing; the control read decides). With no
  release held by the serving account alone, any other release must read, and the break is this release on that
  account; if it fails, there is no verdict (an outage is concluded only from a failed release the serving account
  alone holds). With the account unknown any other release must read too; the release then counts as broken on each
  holder. Another release's hash is the one its link stored, else the one the registry records for its folder;
  with no hash on either side, a release in another folder on the mount is another release (only for the
  account-free control).
- **The repair.** A file the round would not search (`Library.PlayingSearchable`: its title or version not monitored
  or removed from the arr view, its version not the link's, a film not available yet by the library's minimum
  availability, or none of its episodes monitored there and aired) gets no fast path at all, before anything is
  said or counted: no incident, no mark, no message; the hourly sweep treats it as it always has. The same holds for
  each of the release's other files. The bound first: one incident per title and library a day; a second one that
  day gets no fast path (the hourly path as today). Then the incident is recorded, and: **step 1**, the same release on another account
  of the library, in its provider order, as the automatic "Add to other providers" adds it (one account at a time; it
  counts only when the release was added there now: an account that held it already adds nothing and is passed over
  with no wait and no miss), the rclone listing of its folder forgotten where
  configured, and the same range read every 10 s for up to a minute: it reads, the incident settles `copy` (link
  unchanged, outcome "readable again"); it does not, that account gets the release's miss too. **Step 2**, the file is
  marked dead at once and one `playing` search is queued for it, held to its episodes and off the accounts it failed
  on (an account found out by an outage gets no miss; the incident keeps the repair off it for a day). Every `playing`
  search of the incident leaves the incident's own release out on every account for the episodes it broke for, for
  that search only (no miss).
  Only the copy that was
  read is marked: a link a same-path import or a sweep changed meanwhile is left, and nothing is said. Not on a
  client-routed (decypharr) version, which has no step 1: the miss on the client's key makes its client
  take a different release. The account out and no other in the library (or a client version): nothing is replaced,
  the incident settles `none`, outcome unavailable. **The release's other files**, only when the release is proven
  broken on the account serving it (a release only that account holds read; never after an outage or a control read
  of a release on another account, which repair the watched file only), are then read the same way, at
  mid-file: one whose read and an immediate retry both fail joins the incident and is marked dead; their search is
  queued together once the watched file's search has ended and the pass is done (`Library.HoldPlayingSiblings` holds
  the incident from before the watched file's mark until then, so a search ending meanwhile queues nothing for them
  yet). The outcome, one per incident about the watched file, is
  sent through `Library.OnPlayingOutcome`: `same_release`, `replaced` (an import fixed it), `unavailable` (nothing
  could be done, or its search ended with nothing imported and nothing in flight).
- **Telling the viewer** (`tell`; `plex.Client.Terminate`: `GET /status/sessions/terminate?sessionId=…&reason=…`, the
  token in the header, through the gateway; `Session.ID` is the nested `Session.id`). Once the break is confirmed: after
  the dead mark, or, when step 1 has a candidate, after 30 s of it with the link still on the copy read (a file that
  reads again gets no message, and a message still owed is dropped), the same playback
  (session id, viewer, item and part), read from Plex right before, is stopped with "This file stopped working.
  Looking for another copy. Try again in a few minutes.", or "This file stopped working. Try again later." when nothing
  will be done (the day's bound reached, a replacement at the same path breaking the same day included, the account
  out with no other). Pause, or a library that stopped writing, checked again before each change and each message,
  ends the check with nothing done or said, and nothing kept (no incident to use up the day's bound or show in the
  list); the file is checked again at its next sign. Only when it is buffering or its position has
  not moved since the sign; one still advancing gets it at the next fresh Plex sample of it that is buffering, or
  whose position has stayed the same over playing samples for 15 s (Plex reports it about every 10 s; a rewind is
  movement too, and a pause starts the count again) (never on a remembered verdict or an old zurg line), and only
  when one read of the file at the playback's path now (the tracked symlink there), at the player's position (from
  its start when the file is gone from a mount that still lists; not listing: nothing is judged), still fails (it reads: the message is
  dropped, "the file read again"); a paused one never; no session left: nothing
  is sent. Never when the mount is the problem, never after a read that passed. `Options.StopWithMessage` switches it
  (on when unset; serve passes `library.plex.stop_with_message` of the settings in force); off, nothing is ever sent
  to Plex. A refusal by Plex is logged once and kept on the incident. When nothing is sent, why is kept there instead
  ("the player was not stopped: " and stopping a broken stream is off in the settings, the playback had already
  ended, the playback was paused, or the file read again), also when a message still owed is dropped because the
  playback ended or the file read again; it never replaces a stop or a refusal already kept. Users see these fixed
  sentences for their own incidents, not Plex's refusal.
- **Incidents.** `Tracker.Incidents` lists the last 24 hours' incidents (title, episodes, how many files, viewer,
  player, the old and new release, what the player was told) as "Looking for another copy", "Replaced with another
  release", "Readable again" or "No other copy yet" (with ": the debrid account did not answer" when its account was
  found out; no account named, as History says it), or "Removed from the library" when the watched copy was removed
  before its search ended (no outcome is sent for it). `GET /nowplaying` carries them as
  `incidents` (absent when none; see "Who sees what"), and the page lists them under "Files that stopped working".
- **Where the outcome goes** (`cmd/theatarr/serveplaying.go`). Serve sets one `OnPlayingOutcome` hook on the startup
  library (its fallback copy for imports keeps it through `WithPolicy`) and on each settings generation's library: it writes the title's
  History row (event `playing`, `pb:<incident>:<kind>`, the broken copy's release; "Replaced after it stopped
  working", "Readable again after it stopped working", "Stopped working, no other copy yet")
  and hands the outcome to `Notifier.PlayingOutcome` (sent at once; [devices.md](devices.md)). The
  library reports each incident once, so each reaches a target once.

## Who sees what

`GET /nowplaying` needs the browse permission. Managers and admins (see files) get everything: every playback, its
source, fetch and pre-warm, the cause detail, zurg's stream count and the rclone cache. Users get only their own
playbacks, matched on the Plex account linked to their Theatarr account (plex.tv id or name), with the verdict and cause
but no detail, and of the source only the title (for the link to its page). Plex's error text is replaced by "Plex
does not answer". Managers and admins get a sentence that says what to fix in `error` ("Plex does not answer at
<address>: check the address under Settings › Plex & playback", or "Plex refused the token: sign in to Plex again"
for a 401/403) and Plex's own error in `error_detail`. Of the files that stopped working while playing, managers and admins get every incident with its
releases and what Plex answered; users get only the incidents of their own playbacks (the viewer matched on their
linked Plex name), with what came of each, their player and the message their player showed, but no release names and
not why Plex refused. While the tracker is off the answer is `{"enabled": false, "sessions": []}` and the page says what
turns it on: admins get the link to Plex & playback (styled as a link), everyone else is told to ask an administrator
(the settings page is admin-only). Admins edit the rclone remote control in the one line at the top ("rclone remote
control at <host> · Edit"); there is no second link to its settings.

Admins also get a **Now playing** card at the top of Home (`web/src/components/now-playing-card.tsx`): one line per
playback (what, who, player, position, health badge), each linked to the page. It shows nothing while the feature is
off or nothing plays. Its reads count as a look at the page, so the tracker polls every 10 s while Home is open.

## Not in it

There is no play history (and no Tautulli), no movie or details-page warming, no provider adds or restores beyond the
repair's "Add to other providers" of a broken release, no changes to zurg or the mount, nothing sent to Plex but the
stop of a broken stream, and no mid-stream failover.

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
progress). `internal/nowplaying/rescue_test.go` covers the file that stops working on a temporary folder as the mount with the
real library: replaced within two minutes of the sign on a zurg mount; the same release from another account; a mount
with no zurg API and no log on a client-routed version; a file that reads fine; a restart-length outage; the mount
down; one account out (with and without another); a pack's other files searched together after the watched one's
search; the day's bound; a hung read that blocks neither poll nor page; an early stop, and an arr's symlink never
checked; the come-back after two sweeps; and the message (once confirmed, after step 1's 30 s, never with the setting
off, without a session or while paused, at the next sign while advancing, "try again later", a refusal kept).
`internal/library/readpiece_test.go` covers the piece read's verdicts (a cached head and an unreadable piece, helper
errors give none, a hang in process and in a helper counted nowhere, a sweep unblocked beside a lingering helper).
`internal/web/nowplaying_test.go`
covers the redaction for users, `web/src/lib/now-playing.test.ts` the page's labels and
`web/src/components/now-playing-card.test.tsx` the Home card.

## Connect rclone

Admins see **Connect rclone** on Now playing when no rc connection is configured, or a compact host and Edit
link when one exists. It adds fetch speed and cache state of the playing file. Enter the rc URL, username and
password, then **Test connection** or **Save**. Credentials are never pre-filled; blank fields keep stored values.
The test tells an rclone that does not answer ("rclone does not answer at <url>: is rclone running with --rc, and is
the URL right?") from one that refuses the credentials (HTTP 401/403: "rclone refused the username or password")
and from another HTTP error, so an outage does not send the admin to re-enter credentials (`TestRcloneTestUnreachable`).
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

## Watch in Plex

`GET /titles/{id}/plex?version=<library>` (browse) looks the title up in Plex when the title page asks, and stores
nothing and polls nothing. With the library's `plex_section` and the Plex token set, it makes one `/identity` call (the
server's machine identifier) and one listing of the section's items by file (`plex.Client.SectionFiles`, the listing
the playlists use: movies for a film, episodes for a series, an episode's show from its `grandparentRatingKey`). The
library's active links of the title are matched on their path. The answer is `{found: true, web_url, app_url}`:
`web_url` is `https://app.plex.tv/desktop/#!/server/<machine>/details?key=%2Flibrary%2Fmetadata%2F<ratingKey>` (Plex
Web; Android can hand it to the app), and `app_url` is `plex://preplay/?metadataKey=…&server=<machine>`, the form the
Plex iOS app opens. The web app picks one by platform. Otherwise it answers `{found: false, message}`: "Not found in
Plex yet" (no section, no file, or Plex has not listed it), or the sentence for a Plex that does not answer or refuses
the token (`uipages.Fix`). A title in two libraries is two Plex items and gets two lookups, one per library with a
file (`web.TestPlexLink`; a section of 5000 episodes from the fake takes about 20 ms to list and match).
