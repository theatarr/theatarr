# Parallel versions: Theatarr active beside the arrs

A **parallel version** (`versions.K.follows: F`) runs `mode: active` beside the arr
that serves version F, on a root of its own. The arr keeps grabbing and importing into its roots as before;
Theatarr builds a second library from the same titles under its own root (for example `/media/library/...`), and
Plex shows it in sections only the admin sees. Nothing on the arr side changes, so there is nothing to hand over and
nothing to undo there. The client and Plex handover in [golive.md](golive.md) comes after a successful trial.

## What a parallel version gets

- **Titles and monitoring from the followed version.** Every `arr.import` of an instance feeding F also writes each
  follower K (`internal/importarr/follow.go`): the title, a version row for K (created `wanted`), the movie's
  monitored flag, each episode's monitored flag, the series' season monitoring and `monitorNewItems`
  (`series_monitor`). A title the arr drops is unmonitored in K, never deleted. Monitoring is mirrored on every
  import, so an edit made in Theatarr's UI for K is reset at the next import: change it in the arr.
- **Source settings and blocklists from the arr.** Decisions and naming use F's owning arr's series type, and
  movies inherit its minimum availability (an explicit choice for K's view still wins). Every import reads the
  complete paged arr blocklist with GET only and mirrors its hashes and hashless source titles into K's router
  blocklist, with reason `arr blocklist (<instance>)`; removed entries clear only that instance's contribution,
  and local blocks remain. The arr's recorded list is mirrored regardless of why it blocked a release.
  Episodes absent from a new snapshot are unmonitored in K, including retained metadata rows; links and file states
  remain, and ordinary matching excludes removed episodes. Migration 00051 stores the source relationship and
  each instance's blocklist contribution; existing followers acquire them on their next arr import.
- **None of the arr's files.** `version_file`, `version_source` and `arr_title` stay F's only. K owns no arr records,
  so the rest of Theatarr treats it as a theatarr version: tvrefresh adds new episodes from the monitoring intent, the wanted
  loop searches what K misses only while `router.wanted.missing` is on (otherwise RSS or a manual search fills
  K's gaps), the router adds through debrid (`providers` of K) and the library links under K's root. F stays
  `shadow`: Theatarr never searches, adds or links for it.
- **Its own symlinks, started by copy.** `theatarr library adopt` (below) links the arr's current files into K's root
  under Theatarr's naming, pointing at the same mount targets, so K starts complete instead of downloading everything
  again. While K does not grab, each arr import also adopts newly imported files and refreshes existing adopted
  links after arr upgrades. After K starts grabbing, an upgrade in K replaces only Theatarr's link; the arr's file is untouched.
- **The arr's monitored collections.** Each Radarr import carries the collections the arr monitors over to
  movie intake, targeting K when K is active (never F: a followed version's roots are its arr's; docs/design/arrview.md,
  Collections). Movies intake adds to K
  that the arr does not have are K's own: the import never unmonitors them, as it only unmonitors titles the arr owned.
- **Not in default adds.** UI adds and requests that name no versions leave parallel versions out
  (`catalog.DefaultVersions`); K follows F's titles through the import instead. The default `/radarr` and `/sonarr`
  views are unchanged. K has its own view at `/v/K/radarr` (or `/sonarr`) for a test client.

## Policy

```yaml
# versions.yaml
versions:
  movie-remux:                 # followed: stays shadow, fed by radarr_4k_debrid
    mode: shadow
    ...
  movie-remux-theatarr:
    follows: movie-remux
    mode: active
    kind: movie
    anime: false
    profile: remux-2160p       # the cutover profile; never from_arr
    library: /media/library/movies-remux
    storage: debrid
    indexers: [prowlarr-public]
    providers: [rd, ad, tb]
    client: decypharr
    naming: {from_version: movie-remux}

# library.yaml
library:
  enabled: true
  versions:
    movie-remux-theatarr: {roots: [/media/library/movies-remux], plex_section: 5}
```

Validation (`internal/policy/follow.go`) refuses: following itself, an unknown version or another parallel version;
a different kind or anime flag; an `import.arr` instance feeding K; no `library.versions.K`; roots overlapping any
other version's roots or library (F may have several roots; K has one); naming that is neither explicit nor
`from_version`; F in `mode: active` (its roots are its arr's). `naming.from_version: F` takes F's naming (explicit or `from_arr`) and nothing else may be
set beside it. As a second line, the write gate treats a followed version as not writable whatever its mode says.

Validation and the write gate resolve every version's roots and the trash through symlinks; overlapping physical
paths and roots with the same device and inode (including visible bind aliases) are refused. With the mount table
(`/proc/self/mountinfo`) each directory is also placed in its filesystem (device and path inside it), together with
every mount below it, so a bind mount of one root inside another root is refused too
(`TestNestedBindAlias`). The gate rechecks before writes, and the parallel go-live check reports the resolved paths
and fails on aliases.

A simple layout names K after F with a suffix (`movie-remux-theatarr`) and gives each follower one root under a
common folder (`/media/library/movies-remux`, `/media/library/tv`, ...) and one Plex section. An arr whose files are
not on the debrid mount (a usenet mount, local disk) gains little from a follower: adopt skips links that point off
the mount.

## Adopt (`theatarr library adopt`)

```
theatarr library adopt -version K [-dry-run] [-limit N] [-json]   link F's arr files into K's root
theatarr library adopt -version K -queue                          run it as the library.adopt job in theatarr serve
theatarr library adopt -version K -refresh [-dry-run] [-json]     move adopted links to the arr's current files (below)
theatarr library adopt -version K -refresh -queue                 the same as a library.adopt job in theatarr serve
```

For each arr file of F (`version_file` owned through `version_source`): read the arr's symlink, keep its target if
it is on the debrid mount, stat the target (a mount that is down aborts the run),
and link it under K's root: the folder name from the arr's title folder, the file name from K's naming with the
arr's quality, release group, media info and scene name (multi-episode, specials and anime absolute numbers as in a
normal import). The link row records the arr's file as the existing file (upgrades compare against it), the
episodes, and the version and episode states; every link is journaled in `library_action` ("adopted from ...") and
Plex gets a partial scan per folder.

Skipped, with the reason in the report: `arr_file_missing`, `not_a_symlink`, `target_off_mount`,
`dangling`, `path_taken` (K's path holds something else, including a link K imported itself, whatever became of
its symlink: a row with a download is never taken over, `TestAdoptLeavesOwnImportRow`), `no_episodes`, `naming`.
A file K already links counts as
`existing`. Re-running is safe: it links only what is missing and never overwrites; an arr upgrade of a file K
adopted is what `-refresh` (below) handles. `-dry-run` writes nothing and
runs with the library gate closed; a real run needs the gate open for K (`library.enabled`, `mode: active`, roots).
A root that does not answer (K's or F's) stops either, dry run or not, before anything under it is read
(`library.ErrRootDown`; a queued job retries).

Any overlap with K's own episodes still preserves its files and skips the whole source pack. For skipped packs,
the result/JSON reports `missing_episodes` and `partial_packs` counts, plus a capped `has_file` problem list with
uncovered episode IDs; only active links whose mount targets exist count as coverage.

## Refresh (`theatarr library adopt -refresh`, and after every arr.import)

Adopt never replaces a link K already has, so when the arr upgrades a file, K's adopted link keeps pointing at the
old file, and dangles once the arr deletes it. Refresh
closes that gap (`internal/library/refresh.go`):

- **Which links.** Only links adopt made: active links of K that carry no download and whose `link.adopted_from`
  holds "<instance> <arr file>", set by adopt and by refresh (migration 00077 filled it from the "adopted from ..."
  journal, so retention can prune old `library_action` rows without losing it). A link Theatarr's own import made, or
  took over at the same path, has it cleared and is never touched (`link.adopted` is 0 for every row and is not used).
- **Which versions.** Only while K follows its arr: K is skipped when it grabs on its own, which is the router's own
  gate (`router.Gate`, `library.FollowerGrabs`): `router.enabled`, `mode: active` and `grab: true`. A parallel version
  is active to have links at all; refresh runs until both `router.enabled` and the version's own `grab` are on, so
  the other followers keep refreshing while one follower grabs.
- **The arr's current file.** For a movie, the title's file of F; for a series, the one arr file with exactly the
  link's episodes, so each episode link follows its own file and a multi-episode link follows a multi-episode file.
  The arr's symlink is read as adopt reads it. When its target is the link's target, nothing happens.
- **The swap.** The new link is made under K's naming in the title's folder (in place when the name is the same),
  the old link is copied to the trash (`library.trash`, `link_trash` reason `refresh: arr upgrade`) and removed, the
  old row turns `unlinked`, and the new row is recorded as adopt records it ("adopted from <instance> <arr file>
  (refresh: replaces ...)"), with the file's metadata and K's state recomputed. A `replace` action names the trash
  copy. Plex gets a partial scan of the folders. The arr's files and links are only read.
- **Left as they are, and reported** (the report's `skipped` and `problems`, and a log line each):
  `arr_file_removed` (the arr has no file for the title or any of the episodes: the link stays, even if it
  dangles), `arr_episodes_split` (the arr holds the episodes in another split), `target_missing` (the arr's new
  target is not on the mount, for example during a mount outage: never swap to a missing file), plus adopt's `arr_file_missing`,
  `not_a_symlink`, `target_off_mount`, `path_taken` and `naming`. A mount that is down aborts the run, and so does a
  release-root listing that is empty or implausibly small (docs/design/library.md, "An implausible listing").
- **Dead adopted links are refreshed too.** An adopted link a sweep marked dead (the arr upgraded and its old
  release left the mount before a refresh moved the link) is moved to the arr's current file like an active one.
  Each swap holds the link leases of its old and new path through its
  commit (docs/design/library.md, "One link lease per path"), so a reconcile or relink of either path waits for it.
- **One failed link does not stop the rest.** A swap whose write is refused or fails (`swap_failed`), or whose link
  changed under it (`link_changed`: a rename or relink got there first; each swap is a compare-and-swap on the
  link's symlink and row) is recorded in `problems` and `failed`, the link is left, and the other links still run
  (`TestRefreshContinuesPastFailedLink`, `TestRefreshLosesToConcurrentRename`).
- **Idempotent.** A second run finds every link on the arr's current target (`current`) and changes nothing.
  `-dry-run` plans the swaps and writes nothing, gate or not; a real run needs K's gate open, as adopt does.

**Automatically after arr.import.** For each instance whose version has followers, the import notes each title's arr
files (file ids, paths, episodes) before and after the write; titles whose files changed are handed to
`library.RefreshFollowers`, which refreshes existing adopted links, then adopts missing files for every follower
that does not grab and whose gate is open, on those titles only. This includes new titles and newly imported
episodes. The adopt step uses the same source, mount, naming, ownership and path guards as manual adoption; it
does not overwrite a follower's own files. Each skip, swap and problem is logged (`library: follower refresh ...`
and `library: follower adoption ...`). A failure is logged and does not fail the arr import.

**Catch-up.** The per-import trigger fires only on the import that sees the arr's files change, so a new file or an
upgrade skipped while its mount target was unreadable, a gate was closed or a link failed would otherwise never
be retried. `arr.import` therefore also runs `library.RefreshFollowersAll`, the same refresh and adoption over every
title, for each followed version: at its first import after `theatarr serve` starts, then once an hour, and at the next
import after any follower refresh of that version failed (including a link that failed), until one succeeds. While
the catch-up is due it replaces the per-import refresh. It has the same gates (followers that do not grab, gate
open). Refresh is idempotent and links already on the arr's current file cost one `readlink` each, so the hourly
run is cheap; nothing is persisted (a restart makes it due), so no migration. Persisting pending titles was not
needed: the full run finds every stale link from the arr's current files, which is the source of truth either way
(`shadow.TestImportFollowerCatchUp`). `theatarr library adopt -refresh` still does the same by hand.

## Before grabbing: adopt and refresh each follower

Adopt is manual and runs once; refresh follows the arr's upgrades only while K does not grab. So once grabbing is on
for K (`router.enabled`, `mode: active` and `grab: true`), K loses refresh, and a file the arr imports for F after K's last adopt is
never adopted. Add-time search intents made while the router is off are dropped (`acquire/intent.go`), and with
`router.wanted.missing: false` nothing searches K's missing items by itself. Right before the flip, per follower:
`theatarr library adopt -version K`, then `theatarr library adopt -version K -refresh`. Anything still missing after that is left
to RSS or a manual search; Theatarr runs no catch-up search for it. `theatarr golive check -version K` shows whether it is due ("adopt and refresh", below).

## Plex

Create one Plex library per follower root and share it with no one yet: your users keep watching the arrs'
sections. Scan each section once after its first adopt.

## Going live check

`theatarr golive check -version K` replaces the shadow section with `parallel`: the followed version and the instances
feeding it, the arr grabbing as INFO (duplicate grabs are expected and fine), K's resolved roots, and how many of F's
arr files are not fully covered by active K links with existing mount targets (WARN until coverage is complete),
and "adopt and refresh": a WARN while F's newest arr import that Theatarr has read is newer than K's last applied adopt or
refresh (a PASS still says to run both right before grabbing).
Every episode in a pack must be covered; missing episode and partial-pack counts include a capped sample list.
Targets are checked through the configured mount path; a mount outage reports coverage as unknown. The arr
handover check passes: the arr keeps running. `theatarr health` has no arr-grabbing check, so it stays quiet.
The Homepage cutover reminder still warns while K grabs until `versions.K.golive_done: [homepage]` acknowledges
the tile's move to K's view; when checking F, the fix also points at K's view. The `release push`
check is INFO.

`theatarr parallel compare [-version K] [-since 7d] [-json]` reads each arr feeding F and compares its recorded grabs with K's actual routes; it does not change import ownership. It reports hash matches, differing releases with quality and score, unmatched grabs and routes that were already covered by an arr file. `theatarr golive check` shows the same follower agreement measurement in its parallel section (95% bar; too few grabs warns).

Episode comparisons resolve Sonarr IDs through the followed instance's episode aliases. Unmapped episodes remain
arr-only. `-since 7d` means 168 hours; Go durations such as `36h` are accepted.

## Cutover for followed versions

For a followed shadow version F, cutover means switching clients and Plex libraries/sections to active follower K's
roots. F's arr roots stay read-only throughout; after clients and Plex move, retire F's arr and roots. Theatarr never takes
over F's folders in place. Use the [follower runbook](golive.md) for trial settings, checks, handover and rollback.

## How to stop

With DB settings, edit mode/follows/root and view bindings in **Admin › Settings › Libraries**. Shadowing
stops new work; removing follows or removing a version with links requires the specific confirmation shown there.
Removal only stops management: operational link rows, filesystem links, mount content and trash remain intact.

Set K to `mode: shadow` (Theatarr stops searching, adding and linking for it; its links stay and Plex keeps showing them)
or remove K from `versions` and `library.versions` (its links stay on disk until removed by hand; the Plex sections
can be deleted). Nothing to undo on the arr side.

### Changing a root with links

The version editor refuses to change a root with active links. Keep the old root and version, create a parallel
version with a separate existing root inside the allowed library area, configure its naming in the naming editor,
then use the adopt/coverage checks above before switching Plex and clients. Retire the old version only after
reviewing its removal confirmation; Theatarr never migrates or deletes the old links as part of a settings edit.

## States and the cutoff

The two versions' states use different cutoffs and cannot be compared one to one:
- **The followed version F** takes its state from the arr's `qualityCutoffNotMet` (`arr.import`). That flag checks
  the quality cutoff only (Radarr `MovieFileResource`, Sonarr `EpisodeFileResource`).
- **K's adopted links** are judged as a Theatarr import is (`importedState`, `decide.Engine.FileCutoffNotMet`): the
  quality cutoff and the profile's `cutoff_score`. Formats are recalculated from the arr file's scene name, file
  name, group, languages, size and flags exactly as for the arr file itself.
- **What that means:** with a profile whose `cutoff_score` no arr file reaches, K holds every adopted file as
  `filled` while F shows most as `cutoff_met`. Both are right by their own definition.
- **What the wanted loop searches** follows the arrs: with `router.wanted.cutoff_unmet: quality` (the default) it
  searches only files below the quality cutoff, as the arrs' Cutoff Unmet search does (docs/design/router.md "The
  wanted loop"). Files at the cutoff quality but below the score are upgraded only when a feed or another search
  brings a better-scored release, as the arrs' RSS sync does. Adopted links need no repair.

## Open points

- The UI does not mark K as mirrored, so monitoring edits there are silently reset by the next import.
- Adopted links keep the arr's score but not its custom format list. That changes no decision: the cutoff and
  every upgrade comparison recalculate the formats from the file's inputs.
