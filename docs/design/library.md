# The library (`internal/library`, `internal/library/naming`, `internal/library/probe`, `internal/plex`)

The library imports ready downloads: it finds the release on the debrid mount, probes and names each file, and makes
symlinks under the version's roots for Plex. Where it sits in the pipeline: [overview.md](overview.md). Migration
`00015_library.sql`. Commands `theatarr library`. The tests run in temp directories.

Series imports wait while a required episode title is TBA or missing inside Sonarr's 48-hour window, then retry
automatically; the arr queue reports the pending import.

## The gate

A filesystem write or a Plex call happens only when **all** of these hold:

1. `library.enabled: true`. Off by default and when the section is missing.
2. The version is `mode: active`.
3. The version is listed under `library.versions`, and the path is under one of its `roots` (a link moved aside goes
   under `library.trash.dir` instead).

How it is enforced:
- `fs.go` is the only code in the package that changes the filesystem (`TestWritesOnlyInFS` greps for every `os`
  write call elsewhere). Each of its calls asks the `Gate` with the exact path first.
- Paths must be absolute and clean, never inside the mount, and resolved through the filesystem: a symlinked directory
  inside a root that points elsewhere is refused (`TestGatePaths`), and so is a root or directory whose symlinks lead
  into the mount, its link path or a bind mount of either (`TestGatePhysicalRoots`). The resolution stops at the
  mount and never makes a call in it (`policy.ResolvePath`), so a hung mount cannot block the gate.
- Before every write the gate resolves every version's roots and the trash (`policy.CheckRootSeparation`), each
  through the root prober (`internal/rootprobe`, one call per filesystem at a time, `policy.RootResolveTimeout`,
  3 s). A root on a share that stops answering refuses the write (reason `unreachable`, "not answering") after one
  bounded wait, and at once while that call is stuck: fail closed, without holding the caller's link locks and
  worker slot on a filesystem call nothing can interrupt (`TestGateHungRootRefusesWithoutHanging`). The write itself
  then touches only the version's own root. Everything else a write check resolves goes through the same prober,
  once per check (one `policy.RootResolver` per `CheckPath`/`CheckTrash`): the debrid mount's parent (its physical
  alias, `policy.MountPathsWith`) and the written path's directory, each charged to the filesystem it is on. A root
  that answers while a share mounted below it does not, or a hung share above the mount, refuses the write the same
  way (`TestGateHungDestinationRefusesWithoutHanging`, `TestGateHungMountParentRefusesWithoutHanging`).
  `policy.MountPaths` (settings validation, the usage scan) resolves the mount's parent leniently: one that does not
  answer is left out of the aliases after one bounded wait, so a save is never held (`TestMountPathsHungParent`).
- **A root that does not answer is a pause, not a dry run** (`ErrRootDown`). The gate's `unreachable`
  refusal matches `ErrRootDown`, and `library.Unavailable` (`ErrMountDown` or `ErrRootDown`) is the one test for
  "stop, read nothing there, retry later". An import refused that way plans nothing and records nothing (planning
  reads the root's links, unbounded); the download stays `ready`, and the periodic pass logs it and goes on with the
  others. Reconcile, relink and verify skip such a root whole (the scan's `skipped`: "unreachable: ..."), and adopt
  and refresh stop (a `library.adopt` job retries rather than failing for good). Versions the gate refuses before
  their roots (shadow, library off), dry runs and adopt/refresh dry runs check the roots they read the same way
  (`Gate.Reachable`), including every share mounted below a root, which a walk would cross into
  (`TestImportUnreachableRootPlansNothing`, `TestImportShadowUnreachableRootPlansNothing`,
  `TestScansSkipUnreachableRoot`, `TestScanSkipsRootWithHungNestedMount`, `TestAdoptDryRunUnreachableRoot`).
  `Gate.Reachable` only shows the root answered a moment ago (a share can answer a cached stat, then hang on a
  listing), so the scans' read phase is bounded too (`rootread.go`): the walk of a root (relink sweep,
  verify, dangling summary), reconcile's root check and lstats, adopt's and refresh's reads of the arr's links and of
  their own target paths, and the trash purge's walk go through the same root prober in short calls (a chunk of up
  to 512 entries of a directory listing, or up to 32 readlinks or lstats of one directory), each charged to the
  filesystem of the directory it reads and bounded by `policy.RootResolveTimeout`, counted from when the call holds
  that filesystem's slot (a read waiting its turn, or a large directory on a slow share that keeps
  answering, is not taken for a hung one; a read waiting behind one that gave up is refused at once). A call that does not answer skips the root
  ("unreachable: ...", nothing recorded, counts of the cut-short walk dropped), and while it stays stuck later reads
  of that filesystem are refused at once, so a hung share holds one blocked thread. Calls stay short so a scan never
  holds a filesystem's slot long enough to make a write check beside it time out (`TestScanHungListingAfterPreflight`,
  `TestReconcileHungLstatAfterPreflight`, `TestWalkSlowListingByChunks`, `TestAdoptHungArrLink`,
  `TestPurgeTrashHungListing`). The write phase (a repoint's or leftover's readlink right before its
  write) still reads directly, right after the gate resolved that directory through the prober.
- The writer only creates directories and symlinks, and removes symlinks it has copied to the trash. It refuses to
  replace or remove a regular file. Nothing ever deletes or writes debrid content; the mount is read-only.
- When the gate refuses (other than `unreachable`, above), the import runs as a **dry run**: it plans everything and
  records it (`library_import` state `dry_run`, `library_action` mode `dry_run`), and the download stays `ready`.
- `theatarr serve` logs the gate at start, at WARN: `LIBRARY DISABLED: library.enabled is false: no filesystem writes and no
  Plex calls; shadow dry runs only`, or `LIBRARY ENABLED: library writes enabled for: <versions and roots>; dry run
  only: <versions>`.

**Proof (`go test ./internal/library`).**
- `TestGateBlocksWrites`: disabled, then enabled with the version in shadow. An import with an upgrade to make, a
  relink sweep and a trash purge 400 days later leave the temp tree byte-for-byte identical; the download stays
  `ready`; the planned link and the planned move-aside are recorded as `dry_run`; Plex calls are refused.
- `TestGatePaths`: the root itself, another version's root, a sibling directory, `..` traversal, a symlinked escape
  directory, the mount, a relative path and an unlisted version are all refused.
- `TestPlexScanJob`: a queued scan job does not call Plex while disabled.

## Policy

`examples/policy/library.yaml` (disabled), validated in `internal/policy/library.go`:

```yaml
library:
  enabled: false
  mount: {path: /mnt/debrid, link_path: /mnt/debrid, all_dir: __all__, wait_timeout: 6m}
  probe: {max_bytes: 16777216}
  sample: {min_runtime_ratio: 0.5}   # resolution: true, mislabel: true by default
  trash: {dir: /media/.trash, keep_days: 30}
  plex: {url: http://plex:32400, token_env: THEATARR_PLEX_TOKEN, budget: plex}
  versions:
    movie-remux: {roots: [/media/movies], plex_section: 1}
```

- Validation: mount path and trash dir required once enabled; roots and trash absolute, never overlapping the mount,
  the trash never inside a root (Plex would scan it); `versions[k].library` must be under one of its roots; a Plex
  section needs `library.plex`.
- `versions[k].naming` gained `specials`, `daily`, `anime`, `colon`, `multi_episode` and **`from_arr: <instance>`**,
  which takes the whole naming config and the quality definition titles from that `import.arr` instance (GET
  `config/naming` and `qualitydefinition`, stored in `library_naming` by `theatarr library naming-sync` and daily in
  `theatarr serve`). `from_arr` keeps Theatarr's names equal to the arr's, which a move from an arr needs.
- `library.plex.url` is an outbound host; the gateway has a new target class `plex` (`plex:server`), with the optional
  budget. The token goes in the `X-Plex-Token` header, never the URL.
- `library.mount.zurg_log` is zurg's log, read-only: an absolute file path, or an `http(s)://` URL of zurg's `/logs`
  when the file is not readable from Theatarr's container (for example `http://zurg:9999/logs`). A URL is an outbound host with its own gateway target class `zurglog` (`zurglog:log`), direct
  (LAN, no VPN), with the optional budget `library.mount.zurg_log_budget`; it is fetched every
  `library.mount.zurg_log_interval` (default 15 s, 5 s to 10 m). See "Following zurg's log" below.
- `library.mount.zurg_api` is zurg's base URL (for example `http://zurg:9999`), read-only and off when unset. It is an
  outbound host with its own gateway target class `zurgapi` (`zurgapi:api`), direct (LAN), with the optional budget
  `library.mount.zurg_api_budget`. `zurg_api_interval` (default 1 m, 10 s to 1 h) is how long the torrent listing and
  the repair state are reused; `zurg_api_repair_grace` (default 2 h, 10 m to 24 h) bounds the repair grace. The three
  settings beside it need it set. With it set, zurg's `/ready` also gates the mount checks. See "zurg's API" below.
- `library.plex.now_playing` and `library.plex.prewarm` (both default true), `library.plex.prewarm_rate` (default
  25MB/s) and `prewarm_max_size` (default 20GB) for the pre-warm's whole-file read, and `library.mount.rclone_rc` (url,
  username_env, password_env, budget; optional, gateway class `rclonerc`) drive the now playing page and the
  next-episode pre-warm, which read Plex's sessions, zurg's `/stats` and log, and rclone's rc
  ([design/now-playing.md](now-playing.md)).

## Naming (`internal/library/naming`)

A port of Radarr's and Sonarr's `FileNameBuilder` (movie file and folder; series folder, season folder and episode
file with multi-episode styles, absolute numbers, daily, the two-pass episode title truncation and path length
limits) and `MediaInfoFormatter`. Every token common arr naming configs use works, including `{imdb-{ImdbId}}` and
`{edition-{Edition Tags}}` (Radarr tags), Sonarr's literal `{imdb-…}` braces, `{Custom Formats}`, `{Quality Full}`,
the `MediaInfo` tokens with audio language filters, colon replacement and reserved device names. Deviations are in
the package doc.

Token handler keys are normalized with a direct Unicode pass, so regex timeouts under load cannot silently drop
recognized tokens (for example, turning `Volume {season:00}` into `Volume`).

A regex match timeout (netregex's 250 ms, reachable on a starved CPU) fails the naming with an error: the movie and
episode file names and the three folders return `naming: match timeout ...`. regexp2's own `ReplaceFunc` returns ""
and no error when a match after the first times out (that gave a reassigned link the path `Season 01/.mkv`), so the
package runs its own replace loop. The file name entry points also refuse an empty base name (`naming: the format
gives an empty file name`, e.g. a format of only `{Release Hash}` on a file without one). An empty final component is
refused too: Radarr drops it and names the file after the folder before it (`{Movie Title}/{Edition Tags}` without an
edition links `Movie.mkv`); Theatarr refuses. Change episodes and import
then fail with the error and nothing moves; a rename preview skips the link (counted in `skipped`). Tests:
`TestNamingMatchTimeoutFails`, `TestNamingTimeoutAfterFirstMatch`, `TestFolderMatchTimeoutFails`,
`TestEmptyFileNameRefused`, `TestEmptyFileNameRefusedLinkKept`.

**Golden test against real paths** (`TestGoldenDB`, opt-in: `THEATARR_LIBRARY_DB=<import dbs> THEATARR_CORPUS=<corpus>`).
Every file of the import databases' instances is named from the arr's own data (`version_file`, `title`, `episode`,
the instance's naming config and quality definitions) and compared with the arr's path.

- Files the arr kept under their release name (imported without renaming, or under an older format) are not the
  naming's output and are left out.
- File names that differ are episode titles TVDB changed after the arr renamed the file; the arr would rename them
  the same way now.
- Folder differences are folders the arrs did not build from the format: titles changed since, or series added with
  an explicit path that keeps `&`, `'` or `(US)`. So **an import reuses the folder the version already has for the
  title** (an active link's, else the arr's `arr_title.path`), and computes one only for a new title.
- Custom format names are not stored per file, so the fixture reads them back from the bracket before
  `[Quality Full]`; the import gets them from the decision engine (the release's matched formats with
  `includeCustomFormatWhenRenaming`, in profile order).
- The arr API formats the video codec with the file's scene name only, where naming used the original file name, so
  `h264`/`h265` are matched again against `original_file_path` (that is how `[AVC]`/`[HEVC]` names came about).
- 400 matching cases (favouring custom formats, editions, propers and multi-episode files) are committed in
  `testdata/golden` and run in every `go test`; Sonarr's `MultiEpisodeFixture` cases are ported too.

## Probe (`internal/library/probe`)

Theatarr's own Matroska (EBML) and MP4 header readers, no dependency, with one byte budget per file (16 MiB default; a test
probes a sparse 50 GB file and a 20 GB MP4 with `moov` at the end reading under 1 MiB).

- Why not go-mediainfo: it reads by path with no byte cap, which over the rclone mount is uncapped ranged reads
  against the debrid provider; and it is GPL-2.0+. go-mp4 is not
  needed for the few boxes read. No ffprobe binary.
- It reports what the arrs' ffprobe gives (`naming.Model`): codecs, size, bit depth (hvcC, avcC + SPS, av1C),
  colour from the container, Dolby Vision compatibility id (BlockAdditionMapping / dvcC), and from the first frames:
  TrueHD Atmos (`thd+`), E-AC-3 JOC (`ec+3`, full bsi parse), the DTS profile (core, ES, 96/24, HD HRA, HD MA, DTS:X),
  and for PQ video HDR10+ and mastering SEI, as Radarr reads frames only for PQ.
- Limits: colour from the container only (not the VUI); the Atmos and DTS:X bit checks follow the specs and were
  checked on synthetic streams only. `theatarr library verify` compares Theatarr's names with the arrs' on
  real files.

## Import of a ready download

`Library.Run` (every `mount.poll_interval` in `theatarr serve`) takes downloads in `ready` (router, migration 00014):

1. **Mount health:** a `stat` of `<mount>/__all__` with a timeout. A hang, `ENOTCONN`/`EIO` or a missing root stops
   the pass (`ErrMountDown`): nothing is read as missing. Every mount call goes through the mount guard ("Mount
   guard" below). One download whose file fails a read (EIO) while the mount itself stays healthy does not pause
   every import: that download waits in `waiting_mount` with "file unreadable on the mount: ..." as
   its error (Activity: "File unreadable on the mount, retrying"), is retried after 10 min, and the pass goes on.
2. **Find the release:** the folder a path or manual import named (`download.source_path`, below), else
   `__all__/<name>` for the download's `name`, the registry's provider folder names, the content
   name and the release title, each with `.mkv`/`.mp4` trimmed and with zurg's ` {hash6}` suffix (`ReleaseFolderNames`:
   zurg's exact rule first, `TrimSuffix(".mp4")` then `TrimSuffix(".mkv")`, case sensitive, so `X.mkv.mp4` is `X` and
   `X.MKV` keeps its extension, [notes: naming](https://notes.debridmediamanager.com/zurg/internals/naming/); then an
   any-case `.mkv`-then-`.mp4` trim and the untrimmed name;
   `TestReleaseFolderNamesZurgExactFirst`). With zurg's API on, an automatic import puts zurg's name for the info
   hash (from the cached `/debug/torrents` listing) first: zurg names the folder after its own name for the torrent,
   which the providers may report otherwise (Real-Debrid's lower-cased video file name, TorBox's cut or other title,
   AllDebrid's bare hash). A listing that fails or does not hold the hash leaves the list as it was; one INFO line
   names a folder only zurg's name found (`TestImportZurgNameForHash`). Automatic imports
   try folders whose suffix matches the download's info hash before bare names, even without zurg's API. Not there yet:
   `library_import` `waiting_mount`; after `wait_timeout` one `alert` action and a WARN log. It waits until the
   router's ready deadline (`router.ready_timeout`, default 6h from `ready_at`); a held import is excluded from that
   expiry: one held for review, for its episode title, or recorded as a dry run (the gate refuses the version, or
   every episode waits for its anime mapping review). Other overdue downloads fail and are blocklisted and re-searched, ending the mount wait without deleting a provider torrent. A live import claim defers
   expiry; retries do not extend the deadline (`docs/design/router.md`, "Ready deadline").
3. **No video file** with a dangerous or executable extension fails the download and blocklists it. Other empty or
   unmatched imports are held in the queue with `importBlocked` and a warning reason; they are not blocklisted or
   re-searched, and the user resolves them with manual import or remove. As in Sonarr v5 (`CheckEmptyResultForIssue` with
   `RejectedFileExtensionService`, PRs #8775, #9093), every file of the folder is then checked: a potentially
   dangerous file (`.arj .lnk .lzh .ps1 .scr .vbs .zipx`, checked first) or an executable (`.bat .cmd .exe .sh`)
   becomes the reason, e.g. `Caution: Found executable file with extension: .exe`; otherwise `no video files in
   <folder>`. With video files present v5 doesn't check at import, and neither does Theatarr: the router refuses those
   torrents before they get here (`docs/design/router.md`, Polling). `TestImportRefusesDangerousFiles`.
4. **Probe** every video file (largest first) and apply the **guard**: the arrs' `DetectSample` runtime rule (by the
   title's or episodes' runtime; specials, `.strm`, disc images skipped), plus under `min_runtime_ratio` of the known
   runtime, plus a resolution two steps under the release's (a 2160p release holding a 720p file; scope films count
   by width). An unreadable file is indeterminate and passes, as in the arrs. Then the **mislabel guard** ("Mislabel
   guard" below) on a file that passed: a probe that plainly contradicts the file's Remux or 2160p label.

   **Mislabel guard** (`internal/library/mislabel.go`, `sample.mislabel`, default true). Remux ranks over Bluray, so a
   re-encode named "BDRemux" (an x265 fan cut) would replace a real 2160p TrueHD Bluray. The label is the file name's
   quality (without its extension), else the release title's: a `1080p.WEB-DL` file in a `2160p REMUX` pack is judged
   as a WEB-DL. A file name with a resolution but no source keeps the release's Remux claim (its resolution counts):
   `Amelie.2001.2160p.mkv` in a `2160p.BDRemux` release is judged as a 2160p remux. A file name without a resolution
   keeps the release's (the parser's inferred one is no claim): `Amelie.2001.BluRay.REMUX.mkv` in a 2160p release is
   judged as a 2160p remux, not 1080p. A file name that names another source and no resolution claims no resolution:
   `Show.S01E05.HDTV.mkv` in a 2160p remux pack is not refused for its 1080 lines. Refused, only on a clear signal
   (a false positive throws a good file away):
   - **2160p on 1080-line video**: a 2160p label whose video is at most 1080 lines tall and under 3200 wide
     (`mislabelled 2160p: the video is 1920x1080`); scope films (3840x1600) pass.
   - **CRF encode named Remux**: an x264/x265 user-data SEI (MediaInfo's "Writing library" and "Encoding settings")
     whose options show CRF rate control (`rc=crf`, `crf=`; x265's `crf-max=`/`crf-min=` are not CRF). Disc video is
     bitrate/VBV targeted, never CRF (`mislabelled remux: x265 encode with crf=18.0 (disc video is never CRF)`).

   Noted only, never refused: an x264/x265 SEI without CRF (real disc authoring uses x264 `--bluray-compat` and x265,
   `remux: x264 encoder SEI, rate control 2pass`), a 1080p Remux in HEVC or AV1 (UHD discs may carry 1080p HEVC), and
   a low bitrate: file size over probed runtime, audio included, under 6 Mb/s for a 1080p Remux or 15 Mb/s for a 2160p
   one (`remux: 4.0 Mb/s overall, under the 6 Mb/s floor of a 1080p disc`). Most honest remuxes sit far above (1080p
   rarely under ~15 Mb/s, UHD under ~30) and x264/x265 encodes live under the floors, but disc authoring allows 5 Mb/s
   video, so a lean disc can sit under a floor too. A file exactly at the floor is not noted; Remux labels at other
   resolutions have no floor. The notes join with `; `.
   A refused file is a guard rejection like a sample: no link, the existing file stays, and a download with no file
   left fails with the signal as its reason (`library_import.last_error`, `download.error`, the version's
   `router_blocklist` row, source `library`), which the poll turns into a re-search (`docs/design/router.md`,
   "Re-search").
   **A partly accepted pack** (some files link, others are refused) is imported: a refused file gets no link, no
   blocklist row and no re-search. The blocklist is per release (version and hash), and the pack's other episodes
   came from it, so there is no way to bar the pack for only the refused episodes without a new mechanism, and none
   is built. The refused file's episodes are on the plan file (`episodes`, e.g. `S01E02`) and they stay wanted. The
   journal's import entry for the title names them (`rejected: mislabelled 2160p: the video is 1920x1080 (S01E02)`)
   and its `library_guard` check names every refused file's reason and episodes and says they stay wanted without a
   blocklist or re-search, so "why is this episode always missing" has an answer. The check says only what the import
   did: "the rest of the release linked" when it made links (link rows, not the plan's), "blocklisted and searched
   again" when the failed import has the library's `router_blocklist` row (a hold for review has none), and for a
   dry run that nothing was linked, blocklisted or searched. A file skipped as not an upgrade is no guard refusal.
   The limit: the pack's hash is not blocklisted, so the wanted loop can pick the same pack again for those
   episodes, and the same file is refused again each time.
   The note is the plan file's `note`; the journal shows it on the file and as a `library_guard`
   info check. A client's explicit files (manual import) are only noted (`not refused (manual import): ...`), and
   specials are skipped (a pack's special is often an SD disc extra). Gated versions (shadow, library off) record it
   as a dry-run reject without a blocklist, and adoption of the arr's files does not run the guard. The SEI comes
   from the first frame of an H.264 or HEVC Matroska stream (at most 64 KiB of it unless PQ) or from hvcC; MP4
   samples are not read. Tests: `TestMislabelCheck`, `TestMislabelFileClaim`, `TestImportRefusesMislabelledRemux`,
   `TestImportMislabelGuardPassesHonestRemux`, `TestImportMislabelSeriesAndManual`, `TestImportMislabelPartPack`,
   `journal.TestImportPartPackRefusal`, `probe.TestEncoderSEI`,
   `TestMatroskaEncoderSEI`, `TestHvcCEncoderSEI`.
5. **What each file is** (`decide.AggregateSeriesFile`, `AggregateMovieFile`, `library/facts.go`): the
   arrs' import aggregation, per file, with the version's Sonarr parser dialect. Quality (`AggregateQuality`: the
   file's name, the release folder, the download client item, the probe's frame size and container title, the grabbed
   release title; a source or resolution is replaced only by a more confident one, media info above a name tag,
   Radarr's modifier rule included), revision, release group (Sonarr: the client's and folder's unless a full season,
   then the file's; Radarr: client, folder, file), release hash (`{Release Hash}`: the file's, never a full
   season's), edition (movies), languages (file, folder, client, then the probe's audio languages; none found keeps
   the decision's) and scene name (`SceneNameCalculator`: the client title for a lone non-season file, else a scene
   file name, else a scene folder name). "Other video files" counts non-samples, as `nonSampleVideoFileCount`. Theatarr's
   client title and grabbed title are both the release title, the folder the release folder without zurg's `
   {hash6}`. This one result names the file, is stored on its link (`link.quality`, `file_metadata` quality, revision,
   group, hash, languages, scene name), and is what the upgrade check (step 6) and the cutoff state judge. The
   release-level provenance stays the download's: info hash, indexer flags, SeaDex, release type, and the
   decision's formats and score. A `2160p REMUX` pack holding `Show.S01E01.1080p.WEB-DL-FILE.mkv` (1920x1080)
   links it as `[WEBDL-1080p]-FILE` and leaves the episode below a remux cutoff (`TestPackFileIsWhatItIs`,
   `TestUpgradeCheckJudgesTheFile`, `TestReleaseHashToken`, `TestMovieFileIsWhatItIs`,
   `decide.TestAggregateSeriesFile`, `TestAggregateMovieFile`). A link's stored scene name is empty for a pack
   file whose name is not a scene title, so decide scores its formats on its own file name (older links keep the
   release title).

   **Name** with the version's naming: that quality, revision, group, hash, edition and scene name, media info from
   the probe, custom formats from the router's decision stored on the download (`download.formats`, 00016), else
   from deciding the release title again. Movies take the largest passing file; series map each file to episodes as
   below ("Which file is which episode"). Files matching no episode are listed, not linked.
6. **Upgrade check** (Sonarr's `EpisodeImport` and Radarr's `MovieImport` `UpgradeSpecification`, the same in Sonarr
   v4 and v5; `decide.Engine.ImportUpgrade`, `internal/library/upgrade.go`): a file whose episodes (or
   movie) already have a file is linked only when it upgrades **every** current file it would replace. The current
   files are the ones the decision compares with: per episode the active link, else the arr's file
   (`decide.LoadEpisodes`); for a movie `decide.LoadExisting`. Both sides are scored the way decide scores an existing
   file (`seriesFile`, `existingFile`: the stored link inputs, or the arr row; the new file from the download's
   `file_metadata` inputs with its own path and size), with the version's profile and format set. As the arrs: a lower
   quality (profile order, groups equal: `Not an upgrade for existing file(s). Existing quality: X. New Quality Y.`), a
   lower revision of the same quality unless `propers: do_not_prefer` (`Not a quality revision upgrade ...`), and a
   lower custom format score at the same quality (`Not a Custom Format upgrade ... New: [..] (n) do not improve on
   Existing: [..] (c)`) refuse it. **Deviation:** the arrs import a file that is merely no worse; Theatarr replaces only on a
   gain (better quality, a better revision under `prefer_and_upgrade`, or a better score). An equal file, or a proper
   under `do_not_upgrade`/`do_not_prefer` with the same score, is `... the same quality (X) and custom format score
   (n)`. A refused file is not linked and does not take its episodes: it goes to the plan's `rejected` with
   `not_upgrade: true` and the reason (the journal, ManualImport previews and dry-run `reject` actions show it like a
   guard rejection). Files for missing episodes and true upgrades are linked; a download with at least one link is
   imported. When nothing qualifies, the download ends as the arrs' "not an upgrade" import: `rejected` in
   `library_import`, held ready with `not an upgrade for the existing files`, without a blocklist or re-search.
   Not checked: a link already pointing at the same mount file (a retry
   or repeat of the import), a client's explicit files (manual import: the arrs' `ManualImport` executes its choices
   without the import specifications), an existing file with no known quality (replaced), and a library
   without a decision engine. Shadow and dry-run plans run the same check against the arr's files. Why: `packs:
   any_upgradable` takes a pack when any episode is missing or upgradable; without this check the import would replace
   every episode (a WEB-DL batch taken for a missing E12 would move eleven BD remuxes to the trash).
   Tests: `TestImportUpgradeCheck` (mixed set: better remux and higher-scored file kept, missing filled, worse
   upgraded, retry), `TestImportUpgradeCheckShadow`, `TestImportEqualIsNotAnUpgrade`, `TestImportUpgradePropers`,
   `TestImportUpgradeMultiEpisode`, `TestImportUpgradeMovie`, `TestManualImportSkipsUpgradeCheck`.
   Series replacements are planned across the accepted batch: every episode in an old file must be covered by
   accepted incoming files or another active link that stays. Otherwise the incoming partial file is skipped,
   as Sonarr's `SameEpisodesImportSpecification` does, with `Episode file on disk contains more episodes than this
   file contains` and the old path in its per-file reason; the old multi-episode link stays. Separate accepted
   files may jointly replace one multi-episode link, moved aside once, after all its episodes are successfully
   linked; a failed link preserves the old file for the partial-import retry (`TestImportUpgradeSplitMultiEpisode`,
   `TestImportUpgradeSplitMultiEpisodeRetry`).
7. **Link**: atomic symlink (temp link, rename) to the mount path as Plex sees it (`link_path`). An **upgrade**
   recreates each of the version's current files for that movie or those episodes (Theatarr's active links and the arr's
   `version_file` paths) under the trash, then removes the old link. `link_trash` keeps path, trash path and target;
   `PurgeTrash` removes trash copies after `keep_days`. A trash copy with no `link_trash` row (a run interrupted
   between the copy and its commit) is purged too once the date folder it was filed under
   (`<trash>/<version>/<date>/`) is wholly older than `keep_days`; symlinks only, through the gate, and recorded
   as a `purge` action "no trash row" (`TestPurgeTrashOrphans`).
8. **Record**: `content` (created if the hash is new; `mount_folder`), `content_file` with the probe JSON, `link`,
   `link_version`, `link_episode`, `library_action`; the download goes `importing` → `imported` (`imported_at`).
   Each link also freezes its file's inputs for decide (`file_metadata`, 00038): the file's own aggregated quality,
   revision, group, hash, languages and scene name (step 5) over the release's flags, formats, score and hash, and
   the file size; the router retains source inputs in `download.release_data`.
   When some links are made and others fail, the import is **partial** (`library_import` state `partial`, each
   link's `done` or `error` in the stored plan) and the download stays `importing`. `Run` retries it after a
   back-off (10 min, doubling to 6 h); a manual import retries at once. The links already made are found in place,
   so the retry finishes the rest; once every link is made the download is `imported` (`TestPartialImportRetried`).
   No link made at all still fails the download only for dangerous content; otherwise it remains held for review.
   A dangerous-file rejection sets the download `failed` with the reason and blocklists the hash for the version
   (`router_blocklist`, 00016), so the router never routes it again for that version; a held import leaves the
   download `ready` and carries its reason in `library_import.last_error`; the periodic importer leaves it alone until
   a manual import or queue removal resolves it.
9. **Plex**: a `plex.scan` job per touched folder, debounced (`plex.debounce`, 30 s) and deduplicated per folder.
   Refresh, adopt, rename, relink (repoint and revive) and an unlink (an arr client's delete, the admin unlink, a
   copy removed with its links) queue the same scan of each folder they change; a refused (dry-run) unlink queues
   none (`TestUnlinkQueuesPlexScan`).

Idempotent: a second import of the same download changes nothing on disk (`TestImportMovie`).

### Client downloads (`add_via: client`)

A download a client finished (`download.client` set; `docs/design/router.md`, "Client-routed versions") imports like a
provider download, from the mount. Two differences:

- With the client's `import_from: mount` (the default) the row carries the client's torrent name, and the release
  folder is found on the mount by name as above.
- With `import_from: download_folder` the row's `source_path` is the client's folder (mapped to `local_path`). The
  mount's `Locate` accepts a folder of symlinks: it follows each link (up to three hops), and every link must land in
  the same release folder under the mount; files that are not links (an `.nfo`) are passed over. Absolute link
  targets are mapped by the client's `remote_path` → `local_path` first (decypharr may see the mount under its own
  path). The import then
  links from that mount folder, never from the client's folder. Links into two releases, links off the mount, or no
  links at all are refused as "not on the debrid mount". A client folder that does not exist (also one mapped to a path under
  the mount root, checked under the mount guard) imports from the mount instead, found by hash and names as with `import_from: mount`: decypharr's default multi-season handling makes one
  folder per season and none for the torrent (the router marks a client download ready only on a completed state,
  after decypharr made its links). A provider download's missing `source_path` is still an error. `internal/library/clientimport_test.go`.

## Version and episode state

An applied import updates state in the same transaction as its links, judging each linked file as the arrs'
`UpgradableSpecification.CutoffNotMet` does: `filled` while the quality is below the profile cutoff **or** the file's
custom-format score is below the cutoff score (the minimum score when upgrades are off), else `cutoff_met`. The
formats are calculated from the link's frozen inputs (`file_metadata`) exactly as decide scores an existing file
(`decide.Engine.FileCutoffNotMet`), so a version is pursued for a format upgrade exactly when a release could upgrade
it (the quality alone would stop RSS and the cutoff sweep at every import at the cutoff quality). Radarr's and Sonarr's Wanted → Cutoff Unmet list is quality-only; Theatarr's `filled` also covers files below the
format cutoff, as their RSS upgrade check does. Series files update each linked episode and the version summary;
shadow versions keep their arr-owned state. Partial imports update only linked files and never lower an existing
state. Moving or unlinking a file returns its version or episodes to `wanted` only when no active link or arr file
remains for them.

**Recompute.** The first import pass of each process (`Run`), and the first after each settings change (a new policy
revision, e.g. a raised profile cutoff), recomputes the states of active versions' files under the current profile
(`RecomputeLinkStates`): Theatarr's links and imported arr files alike, a movie by its current file, an episode by its file;
only `filled` and `cutoff_met` move, and only between each other. An arr file imported as `cutoff_met` under the
arr's profile thus reaches the cutoff sweep once Theatarr's profile asks for more (`TestRecomputeJudgesArrFilesAfterProfileChange`). A series version is judged whatever its own state: one aired
episode without a file makes it `wanted` while its other episodes' states still follow the profile. A version an arr
still manages (`import.arr`) keeps the arr's verdict on the arr's files; Theatarr's links in it are judged. After a settings
change only the versions whose judging settings changed (the version, its profile, the formats and format sets, the
managing arrs) are recomputed (`TestRecomputeAfterSaveOnlyChangedVersions`). It is idempotent and
touches nothing of a shadow version.

**The arr's file on an upgrade.** When an import moves an arr file aside (its `version_file` path), the row and its
file-id aliases are deleted in the same transaction, as the arr deletes the file it upgrades
(`UpgradeMediaFileService`, `DeleteMediaFileReason.Upgrade`). The arr API and decide stop showing it, and a later
removal of the new link leaves the version wanted. Episodes the old file held that the new links do not are wanted
again.

**Removed from the queue during an import.** A client can end the download (`DELETE queue`, `router.Dismiss`: state
`failed`, maybe blocklisted) while its links are being made. Each fence also checks the download is still
`importing`, and the recording transaction checks it again. The removal wins (`ErrRemoved`): every link this run made
goes to the trash (one replaced in place points at its old target again), every file it moved aside is linked back,
every trash copy gets a `link_trash` row, the import is recorded `failed`, and the download keeps the removal's
state. Links an earlier, interrupted run left in place stay (`TestRemovedDuringImport`).

**Links whose symlink is gone.** `ReconcileLinks` checks a version's active links against the disk: a link whose
path lstat says does not exist (the symlink or its folder was deleted) turns `unlinked` (action `dead`, "vanished"),
and its version or episodes are wanted again when no other file holds them, as the arrs' rescan drops files missing
from disk (`DiskScanService`, `MediaFileTableCleanupService`). Any other error leaves the link alone; a root that is
missing, unreadable or empty is skipped whole (the arrs' failsafe), so an unmounted library marks nothing missing.
A share mounted below a root that is not mounted now (an empty mountpoint) would still make every link in it look
deleted, so a second failsafe: when more than a quarter of a root's links (and more than five) are
missing in one pass, none is marked missing; the scan says why (`skipped`: "failsafe: ..."), the log warns, and an
`alert` action (mode `refused`, "vanish failsafe: ...") is recorded, which the `VanishFailsafeCheck` health check
reports as a warning for a day after the latest one (and so the health webhook). Links someone did delete in bulk
are reconciled once the share is back, or unlinked by hand (`TestReconcileVanishFailsafe`). A Rescan of some titles
is measured against the root's whole link set too, so deleting a season of one series and rescanning it is taken
as a full sweep would take it (`TestReconcileTitleScanFailsafeCountsTheRoot`).
Links of a download being imported are left to that import. A version the gate refuses only reports
(`LinkScan.Vanished`), writing nothing. The hourly relink sweep runs it for every root, and
`RescanMovie`/`RescanSeries` for the selected titles.

**Crashes mid-import.** Each link replaced and each old file moved aside is fenced first: the import must still hold
its claim (not expired) and, inside a job, its lease (`jobs.Fence`); otherwise it stops before the step and records
nothing. Filesystem steps come before the one database transaction, so a crash between them leaves links the database
does not know. The retry plans them as already in place, still moves aside the files they replace, unlinks the rows of old links it finds gone, and records everything. The trash copy made by the
interrupted run stays on disk without a `link_trash` row. Tests: `TestImportFencedBeforeLinkReplacement`,
`TestImportCrashAfterLinkMade`, `TestImportCrashAfterOldMovedAside`. A relink inside a job fences each link it
repoints too.

### Mount guard

A call on a dead FUSE mount can block in the kernel for as long as the mount is dead, and nothing in Go cancels it.
So every mount call (stat, directory listing, file sizes, symlink reads, header probes) goes through a guard
(`breaker.go`), and in `theatarr serve` it runs in a helper process, not in Theatarr (`mountpool.go`, `mounthelper.go`).

**Helper processes.** `theatarr serve` starts up to 4 helpers, `theatarr mount-helper -root <mount path>` (the same binary, a
hidden command; no image change), as they are needed. Each answers one call at a time over its stdin and stdout
(a length-prefixed JSON request, then the answer; paths, names and link targets travel as bytes, base64 in the JSON,
so a file name that is not UTF-8 reaches the helper and comes back unchanged). The caller waits with the call's
deadline on the pipes (`stat_timeout`, 2 min for a probe), in its own goroutine and with no goroutine of its own:

- a call that misses its deadline is `ErrMountDown`, and its helper gets SIGKILL. A helper stuck in the kernel (D
  state) may stay until the call returns; it holds none of Theatarr's threads, and Theatarr collects it later without waiting
  (`Lingering` while it is there). A fresh helper starts for the next call: at once after the first kill, then
  250 ms doubling to 30 s for kills in a row, back to at once after a call answers;
- lingering helpers count toward a hard cap of 8 processes (`MountHelper.Max`, twice the pool): with no room left a
  call waits for a running helper to come back, and with none running fails at once with `ErrMountDown`, starting
  nothing, until the stuck ones go (`TestMountHelperLingeringCap`). A mount whose calls keep hanging can't pile up
  processes, even when answers in between keep resetting the back-off and the breaker;
- a hung call holds one helper; the others go on answering. A pool of single-call processes is simpler than
  pipelining in one helper, and killing one never fails another caller's call;
- a helper that ends mid-call (or answers out of turn) is `ErrMountDown` too, and is replaced. A helper that cannot
  start is the mount down: a call never falls back to running in Theatarr;
- a call whose context ends (shutdown, a client gone) returns at once; its helper is killed without counting it;
- idle helpers stop after 5 minutes; all of them stop when Theatarr does: an idle one at EOF on its stdin, and one blocked
  in a call by the kernel's parent-death signal (SIGKILL, Linux) or, failing that, its own watch of its parent
  process, checked every second in a goroutine of its own (`TestMountHelperEndsWithParent`, `TestWatchParent`).

The helper only reads: stat, lstat, readlink, directory listing, symlink resolution, the header probe
(`probe.File`'s reads, at most `probe_max_bytes`) and the head read (one sequential read from offset 0, at most the
bytes asked, the file closed at once: the now playing pre-warm, `Mount.ReadHead`) and the range read (32 MiB in
order from an offset at a byte rate cap: a piece of the pre-warm's whole-file read, `Mount.ReadAll`). A path under the mount root must be clean and absolute, and on
Linux every such path is opened with `openat2(RESOLVE_BENEATH)`, so a symlink leading out of the mount is refused
(`ErrNotOnMount`) by the kernel without extra calls on the mount. Elsewhere (or a kernel without `openat2`) the
helper goes through `os.Root`, which walks the path from a descriptor of the root and refuses the same escapes. Either
way what is checked is what is used: the call works on the descriptor the check opened, and symlink resolution
(`eval`) answers with the path of that descriptor (`/proc/self/fd` on Linux; `os.Root` lstat/readlink one component
at a time elsewhere), never the path resolved again by name, which a symlink swapped in between could lead out of
the mount (`TestMountHelperContainmentRace`). Go's `os.Root` does not promise this on every system (Windows, plan9),
which Theatarr does not run on. Any other operation is refused. Errors come back with the same message and
the same `errors.Is` answers (`fs.ErrNotExist`, `ENOTCONN`, `EIO`, `ErrNotOnMount`, the probe's sentinels).

Paths a client names outside the mount root (a manual import preview of a local folder of symlinks into the mount)
run in a helper too: whether such a path reaches the mount is only known by following it, and a stat that follows a
symlink into a dead mount would pin a goroutine and a thread of Theatarr for good. Outside the root the helper answers
stat, lstat, readlink and listings only, as plain calls (they read where Theatarr would have, with no root to stay under);
header probes, head and range reads and symlink resolution there are refused (`ErrNotOnMount`), and so is a relative path. This is
simpler than a list of allowed local roots and keeps Theatarr's own goroutines off every path the guard sees
(`TestMountHelperRunsPathsOutsideRoot`). `theatarr serve --mount-helper=false` (env `THEATARR_MOUNT_HELPER=false`) runs every
call in Theatarr under that guard, for debugging. The CLI commands (`theatarr library ...`, `theatarr golive`, `theatarr health`) run
their calls in-process too: they end when they are done.

**The guard** (both ways):

- in-process, at most 8 calls outstanding, counting those still blocked past their timeout; a call that finds no
  free slot within its timeout counts as a timeout. With helpers, a call that finds no helper free in time because of
  the back-off after kills, or killed helpers still stuck in the kernel, counts as one. One that finds every helper
  running a call (slow probes, a busy pool) fails with `ErrMountDown` but does not count: that is load, not the mount;
- 3 timeouts in a row open the circuit: every mount call fails at once with `ErrMountDown`, starting no goroutine and
  no helper;
- after 1 min open, one call goes through (half open): an answer in time closes the circuit, another timeout opens it
  for another minute. Errors that come back (`ENOTCONN`, `EIO`) don't trip it: they leave nothing blocked.
- probes run under the guard with a 2 min timeout; a probe that hangs pauses the import (`ErrMountDown`) instead of
  judging the file.

While the circuit is open, `theatarr serve` skips the import, relink and dangling passes (one WARN when it opens).
`Library.MountBreaker()` returns the state (`closed`, `open`, `half_open`, outstanding calls, timeouts, retry time, and
`helper`: size, max, running, busy, starts, kills, crashes, kills in the last 15 min, lingering) for health checks.
With helpers, the serve `MountCheck` reads through the guard (`Library.MountAnswers`), and `MountHelperCheck` warns
from 3 kills in 15 minutes (`docs/design/readiness.md`). Metrics: `theatarr_library_mount_breaker_open`,
`theatarr_library_mount_calls_outstanding`, `theatarr_library_mount_timeouts_total`, `theatarr_library_mount_breaker_opens_total`,
`theatarr_library_mount_helper_starts_total`, `theatarr_library_mount_helper_kills_total`,
`theatarr_library_mount_helper_crashes_total`, `theatarr_library_mount_helpers_running`, `theatarr_library_mount_helpers_lingering`.
They are registered once (`library.RegisterMountMetrics`) and read the active settings generation's library at each
scrape, as the preflight does: after a save that moves the mount they report its fresh guard, not the startup one
(`TestMountMetricsFollowCurrentLibrary`, `TestServeLibraryCurrentFollowsSaves`). The counters can therefore drop to
the new guard's values after such a save.
The limits are constants (`Mount.MaxCalls`, `BreakAfter`, `Cooldown`, `MountHelper.Size`, `MountHelper.Max`).

Tests use a fake mount whose calls block: `TestMountBreakerOpensAndRecovers`, `TestMountBreakerHalfOpenProbeFails`,
`TestMountCallsBounded`, `TestLibraryBacksOffWhileMountBreakerOpen`, `TestHungProbePausesImport`. The helper tests run
the test binary as the helper, with a path that blocks forever or ends the process: `TestMountHelperKillsHungCall`,
`TestMountHelperStuckSyscallsPinNoThreads` (probes of a FIFO block in `open(2)`; Theatarr's threads and goroutines stay
where they were), `TestMountHelperBackoffAndBreaker`, `TestMountHelperCrashMidCall`, `TestMountHelperCannotStart`,
`TestMountHelperCancelledCall`, `TestMountHelperRefusesPathsOutsideRoot`, `TestMountHelperRunsPathsOutsideRoot`,
`TestMountHelperMatchesInProcess` (every call answers as in Theatarr) and `TestMountHelperNonUTF8Names`. `make check` also
runs the whole library package with its mount calls in helpers (`THEATARR_TEST_MOUNT_HELPER=1`).

What a guarded call reads counts as the mount's answer, and so do its parts: a directory listing fails as a whole
when a file's info (`DirEntry.Info`, an lstat on the mount) fails, so `EIO`/`ENOTCONN` there pause the import
(`ErrMountDown`) instead of turning into "no video files" (a rejection and a blocklisted hash)
(`TestDirEntryInfoErrorPausesImport`). Paths a client names go through the guard too: `Locate` reads a symlink
(`lstat`, `readlink`) under it, with the request's context, since a local directory on the way may itself lead into a
dead mount. Only a path that does not exist or is not a symlink into the mount is `ErrNotOnMount`; a call that hung
(a timeout, a killed or busy helper) is `ErrMountDown` and counts against the guard like any other, and another error
(permission denied) is returned as it is, so the manual-import view says "the debrid mount does not answer" rather
than "not on the debrid mount" (`TestLocateHungPathIsMountDown`); the arr views' `manualimport` folder preview and reprocess list and stat through `Library.ReadDir` and
`Library.Stat`, and a mount that does not answer is a 500, not an empty list (`TestLocateGoesThroughTheGuard`,
arrview `TestManualImportPreviewOnADeadMount`).

**Containment.** A path on the mount is placed by its name, and whether it is mount content is checked where the
release is read: the release folder found must resolve (every symlink followed, under the guard) inside the mount
root resolved, and a symlink inside a release folder is not followed. Either stops the import with `ErrNotOnMount`:
nothing is linked, the download is not failed or blocklisted, and the error is logged each pass
(`TestSymlinkOutOfTheMountIsNotImported`). zurg's mount holds no symlinks.

**One import at a time.** The periodic pass and a manual import (arr view `ManualImport`, `POST /api/theatarr/v1/import`)
claim the download (`library_claim`, migration 00027) before planning or writing anything, and stop with
`ErrClaimed` when another import holds it. Every outcome an import records checks the claim again inside its own write
transaction (imported, partial, failed, rejected with the blocklist, waiting for the mount, a gate dry run, a hold
for review): a stale run whose claim expired and was taken over records nothing, so it cannot fail or blocklist a
download the newer run imported. A claim lasts 30 minutes, so one left by a crash is taken over after that
(`TestConcurrentImportsClaimed`, `TestStaleImportRecordsNoOutcome`). The claim is renewed while the release's files
are probed (once a third of it has passed), so a large pack whose planning takes longer than 30 minutes still
imports; an import that lost its claim anyway stops with a WARN log (`ErrClaimLost`; `TestSlowPlanNeverImports`).

**One path, one title.** The link row at a path keeps its title: an import (or adopt) never takes over another
title's active link, nor another version's active link to another file, at the path it names. Planning finds the
collision (two titles whose names come out the same under the version's naming, e.g. same clean title and year with
no id in the template): the file is not linked, the collision is logged at WARN, and an import left with no link is
held for review (`naming collision: <path>: the path holds link N of another title`), never failed or blocklisted.
The commit checks it again, and its upsert changes no `title_id`. A dead or unlinked row of another title (or shared
with other versions) at the path is dropped for the new one; the version's own row is replaced in place, and a new
file there starts with no noted miss (`missing_since` cleared, so it gets its own two sweeps;
`TestPathCollisionStealsOtherTitlesLink`, `TestInPlaceReplaceKeepsMissingSince`).

**Dry runs make no download.** `POST /api/theatarr/v1/import` with `dry_run` (or while the gate refuses) plans a
`library.NewImport` with `Library.PlanNew`: the same plan, from the row it would insert, with no download row at all.
No `ready` row exists for the import pass to pick up while it plans, and a cancelled request or a crash leaves nothing
behind (arrview `TestDryRunImportPublishesNoDownload`).

## Which file is which episode

Decision and import share one resolver, as Sonarr's import aggregation (`AggregateEpisodes`) calls its
`ParsingService`. Each video file of a series release takes its episodes from, in order (`library/resolve.go`):

1. **The client's choice** (`download_file` source `manual`, migration 00029): a manual import's files, each with the
   episodes chosen for it. Only those files import (a chosen file not in the release is listed as unmatched), and
   the anime hold does not apply (the choice is the review). The sample guard and the gate still do. The choice is
   authoritative: one that cannot be applied in full (an episode another chosen file took first, files going largest
   first, or one the title no longer has) leaves the file unmatched; it never falls back to steps 2 to 4
   (`TestManualChoiceIsAuthoritative`). A chosen file with no episodes goes through steps 2 to 4, except one that
   Change episodes left with none by replacing it (`how` `replaced`, below): it holds none and stays unmatched
   (`unresolved` `replaced: ...`). A new manual import of the download writes its rows afresh.
2. **What an earlier import linked** (`download_file` source `import`, written with each link and its `how`), so a
   partial import's retry maps files the same way.

Files with a choice made by hand (step 1, and a step 2 row with `how` `manual`, which only Change episodes below
writes) take their episodes before the release's other files: another file's recorded or automatic mapping never
takes a corrected episode first, and the other files still import as usual (`TestReassignReplaceKeptByReimport`).
3. **`decide.ResolveFile`**: `GetBestEpisodeInfo` (the file's path; for the only video file whose name is not a
   scene title, the release title's or folder's parse unless it is a full season or absolute where the file's is
   not; else a special by episode title), then the decision's own mapping with the series given: scene numbering,
   daily air dates, absolute numbers, specials. For anime with franchise mappings the seam runs first: an alternate
   cut the file name (else the release title) names, a franchise entry the file's series title names (entry-local
   `01` of "2nd Season" is canonical S02E01, absolute 13), else the decision's mapping stored on the download
   (`download.episode_map`: the router writes the decision's franchise entry or alternate cut). Entry/cut file
   numbers must be absolute: explicit SxxEyy, season 0, specials and OVAs keep their ordinary episode meaning.
   An alternate-cut table missing a file number returns an explicit refusal (`FileEpisodes.Refused`), including
   gaps in a stored cut mapping; the file stays unmatched instead of using canonical numbers. The episodes must
   all be the download's (every episode for a pack the router did not narrow down). An answer from the resolver is
   authoritative even when it cannot be used: the arr's mapping error (several episodes match: `ambiguous`),
   episodes outside the download's (`out of scope`: a scene or franchise mapping that lands elsewhere) and episodes
   another file of the release took (`conflict`) leave the file unmatched, with the reason in the plan's
   `unresolved`; it is not read by its canonical numbers. Example: with XEM swapping canonical E01 and E02, a
   download for E01 does not link the file named S01E01 (canonical E02) as E01 (`TestResolverAnswerIsNotOverridden`). A
   manual choice (step 1) still decides such a file.
   In particular, an automatic multi-episode file overlapping an episode an earlier accepted file claimed is left
   wholly unmatched; its free episodes remain wanted unless another file fills them. This matches Sonarr's
   `ImportApprovedEpisodes` overlap check. It does not link the overlapping file for just its free episodes.
4. Else, only when the resolver finds nothing at all (no parse, no mapping), the canonical season and episode numbers
   (and TVDB absolute numbers for anime).

**Change episodes** (admins; `library/reassign.go`, `POST /api/theatarr/v1/links/{id}/episodes
{episode_ids, replace}`, the episode overview's File section) corrects a file that is already linked to the wrong
episodes (absolute numbering, a pack, a special) without a re-import. It works on one of Theatarr's active symlinks
in one library (a series, an active version, the link not shared with another library, made by an import of a
download); everything else is refused (409) and nothing changes. The chosen episodes must all be the title's. A
chosen episode that already has another file in that library refuses the change unless the admin chooses
**Replace**: then that other link is unlinked as `Unlink` does (trash, never the source on the mount), with every
episode it held, and the chosen episodes leave its file's recorded choice (an `import` row left with none is removed;
a client's `manual` row left with none stays, marked `how` `replaced`, so it holds none rather than being read by
its name; `TestReassignReplaceManualKeptByReimport`; a `manual` row with no episodes, its name having decided them,
first records the episodes the file held, so the ones it keeps are its choice and `replaced` is only for none left;
`TestReassignReplaceManualByNameKeepsRest`), so a re-import does not give them back to it. The replace and the move are one unit: every check runs before anything is
unlinked, leases are taken on all the paths, the other link's symlink goes to the trash first, and its row turns
unlinked in the move's transaction. When the move fails, it is linked back from its trash copy (the copy goes; its
row, episodes and states never changed) and a `replace` action says it was put back
(`TestReassignReplaceFailureRestores`). Whether the move committed is read, and the put-back done, with a bounded
context (30 s) that ignores the request's cancel, so a cancelled or timed-out request still puts it back
(`TestReassignReplaceCancelledRestores`). When that read fails, nothing is put back over a link that may have moved:
the copy stays in the trash, and an error log and a failed `replace` action name it. An episode an arr file of that library holds always refuses. The link moves to the
name the naming templates give for the new episodes through the rename machinery (new symlink first, gate on old
and new path, compare-and-swap, old symlink removed), and in the same transaction its `link_episode` rows, the
episode states (the chosen ones take the file's state; the ones it no longer holds become wanted when no other
file holds them) and the download's `download_file` row are written. The row is the recorded choice of step 2
(source `import`, `how` `manual`; an existing `manual` row keeps its source and only its episodes change), so
later imports and retries of that download, relinks and verifies keep the mapping
(`TestReassignKeptByLaterScans`). Other versions' links to the same file are untouched. A failed move leaves the
links, states and recorded choice as they were (`TestReassignRenameFailureRollsBack`); a destination name that
exists and is not the replaced link, or another active link's row there, is refused. An unlinked or dead row left
at the new name by an earlier unlink makes way, as for an import (`TestReassignReplaceStaleRowAtDestination`). Plex scans of the old and new folders are queued as for a rename.
The action is recorded in `library_action` (kind `link`, detail `episodes changed by hand: S01E01 -> S01E03`)
and the log; it has no journal entry (the journal's action names are fixed by 00039). Tests:
`internal/library/reassign_test.go`.

Each link carries `how` in the plan (`manual`, `file`, `release`, `folder`, `special`, with `+franchise:<entry>` or
`+alt_cut:<name>`; `number`, `absolute`). Lifecycle tests: `TestImportFranchiseNumbering`,
`TestImportMixedPackSpecialsDailyScene`, `TestImportManualFiles`; the resolver alone: `decide.TestResolveFile`.

**Paths.** A path import or manual import names a path on the mount (`download.source_path`): as Theatarr reads it, as
links carry it (`link_path`), under `__all__` or another zurg directory (same release folders), or a symlink into the
mount (decypharr's download folders; read with `readlink` under the mount guard). The import takes that release
folder and, without manual files, only the files under the path. A path anywhere else is refused
(`ErrNotOnMount`): Theatarr links mount content; it never copies or moves files.

## Relink and verify

- `Relink` (every `relink_interval`, 1 h): walks each listed root, groups the links into the mount by release folder,
  lists `__all__` once and each release directory once (never a `stat` per file). A link's hash comes from its
  content file, download, or frozen link metadata (with a provider torrent-id match as a fallback), else from the
  registry by folder name (or the ` {hash6}` suffix). For a missing target, matching hash-suffixed folders win over
  bare names; bare replacement candidates require an unambiguous registry match, preferring the recorded mount
  folder over old torrent/provider names. If a matching suffixed or recorded
  mount folder lacks the file, the sweep reports it instead of falling back to an old bare name another torrent
  may own (`TestRelinkRecordedIdentity`, `TestRelinkKeepsNewerTorrent`).
  An existing target is left healthy unless an explicit rename hint or a matching hash-suffixed folder proves it
  moved; ambiguous or stale registry names never make it dangling. A successful known-hash move also records the
  new `content.mount_folder` atomically with the link update. The registry lookup per folder name is an index
  search on `content.mount_folder`, `content.name` and `content_provider.folder_name` (00103), not a registry scan
  per link (`TestIdentityLookupUsesIndexes`). The link is repointed atomically (`TestRelink`). What
  can't be fixed is reported by cause (`gone`: absent on
  every provider; `missing`; `unknown`), and its link row turns `dead` on the **second** miss:
  the first sweep that misses the file notes it (`link.missing_since`, 00070), and a later sweep at least 30 min
  (`deadConfirm`) after it, with no sweep finding the file in between, marks the link dead and its title or episodes
  wanted, and queues one search for them in the same transaction (reason `dead_link`, `docs/design/router.md` "Re-search
  after a failure": run only while grabbing is on, skipped once they are filled again; `TestRelinkDeadQueuesOneSearch`,
  `TestRelinkDeadSeriesQueuesItsEpisodes`; only for versions the gate lets write,
  `TestRelinkDeadSharedLinkQueuesActiveVersionOnly`). Within zurg's repair grace the link is not marked (cause
  `repairing`, below), so the search waits through it too; after the grace it does not wait for a repair zurg still
  reports. So one folder that lists empty or answers ENOENT for a sweep (zurg half-loaded, a folder being re-filed)
  never kills a link; the hourly sweep marks a link dead an hour after its first miss. Before it notes or confirms a
  miss, the sweep checks the mount again (`/ready`, the stat and the `__all__` listing rules below): a walk can
  outlast a zurg restart, and a mount down by then stops the sweep and forgets the noted misses
  (`TestRelinkRechecksMountBeforeConfirmingMiss`). A run of `theatarr library relink`
  is a sweep like any other. Only Theatarr's active row with the target the walk saw is marked; an untracked symlink (an
  arr's file in the root) has no row and gets no `dead` action. A **dead link whose file is on the mount
  again** turns `active` (action `relink`, "revived"), and its versions are no longer wanted: a movie version and
  the link's episodes go from `wanted` to `filled`, then `cutoff_met` where the file meets the cutoff (the
  reverse of the dead mark; `TestRelinkRevivesDeadLink`, `TestRelinkRevivesDeadSeriesLink`,
  `TestRelinkDeadNeedsTwoSweeps`, `TestRelinkUntrackedNoDeadAction`). The state of a link found present is read
  again in the sweep's write transaction, so one another sweep marked dead meanwhile is revived, not left dead
  (`TestRelinkRevivesLinkMarkedDeadDuringSweep`). A repoint of a dead link revives it the same
  way, and a revived link's folder gets a Plex partial scan, as a repoint's does (`TestRevivedLinkScansPlex`). A
  dead link whose file is back while another file now holds its title (a movie) or every one of its episodes, in
  each of its versions (an active link or an arr file), is not revived but moved aside: copied to the trash, removed,
  its row `unlinked` (action `replace` "dead link moved aside", the scan's `retired`); revived it would be a second
  file next to its replacement (`TestDeadRevivedNextToReplacement`). An import moves the version's
  dead links of the title (or of the episodes it links) aside like the active files it replaces. **A gated version** (library disabled, version not `mode: active`, followed) is swept read-only: what it
  would mark dead, revive or repoint is recorded as `dry_run` actions, and no link or title state changes
  (`TestRelinkDeadWhileGated`).
- **An implausible listing is a mount that is down** (`Mount.checkListing`). `Healthy` only stats
  `__all__`, and a zurg that restarted can serve it present but empty or half-loaded. So relink, the dangling report,
  `Verify` and the follower refresh refuse with `ErrMountDown` (nothing marked dead or missing, no title flipped to
  wanted) when the `__all__` listing is empty, has fewer than half the entries of the largest listing accepted in
  this process, or has fewer than half as many entries as there are distinct release folders that active links
  point into. A genuine shrink below half (never expected: the debrid account is shared) keeps them refusing until
  Theatarr restarts and the linked-folder rule allows it; those links are then for a human (`TestRelinkRefusesEmptyMount`,
  `TestRelinkRefusesShrunkMount`, `TestRefreshRefusesEmptyMount`). A refused sweep (not a dry run) also clears the
  `missing_since` noted on active links, so a miss before the outage and one after it are not taken as two sweeps in
  a row. One release folder that fails to list with another error (EIO) while `Healthy` still passes
  is skipped for that sweep (logged; nothing marked missing) instead of aborting the whole sweep, in relink, the
  dangling report and `Verify`.
- **One link lease per path** (`linklock.go`). Every link mutation holds a lease on each path it touches
  (`library_lease` `link:<path>`, 00070) from before it reads what it replaces until its database commit: an import
  (every path it links or moves aside), an unlink, relink's repoint, revive and dead mark, reconcile, the leftover
  cleanup, the follower refresh (old and new path), adopt and rename. The lease is a database row, so it holds
  across processes (the CLI) and across the runtime generations a settings reload creates; each commit renews it in
  its own transaction and fails when it was taken over (a run that stalled past the 10 min). A mutation that finds a
  path held does not wait: it leaves that link (`ErrLinkChanged`): an import stays `ready` and is retried by the
  next pass, a link that changed between its plan and its lease makes the import `partial` (retried after the
  back-off, never failed), a sweep or refresh leaves it to its next run, and an arr client's delete gets a 409
  "try again" (`TestUnlinkKeepsConcurrentReplacement`, `TestConcurrentImportsSamePath`,
  `TestReconcileDuringRefreshLosesBothLinks`).
- **Link mutations are compare-and-swaps** (`cas.go`), the last guard under the lease. Rename,
  relink's repoint and dead mark, unlink and the follower refresh each check, right before they replace or remove
  anything, that the symlink still points at the target they read and that the row still has that path and target;
  their row updates require the same; the symlink is read before the write transaction, never in it, so a hung
  share cannot hold the database writer (`TestRenameReadsLinkOutsideWriteTx`). The one that loses leaves the link
  alone and removes only what it made itself
  (`ErrLinkChanged`; relink counts it as cause `changed`)
  (`TestRenameLosesToConcurrentRelink`, `TestRelinkLosesToConcurrentRefresh`, `TestRelinkDeadMarkSkipsRepointedLink`,
  `TestRefreshLosesToConcurrentRename`). The other mutations:
  - **Unlink** (an arr client's delete) checks the row is still at the path and the symlink still points at the
    row's target before moving anything, removes only the symlink it copied to the trash, and unlinks the row only
    `WHERE id AND path AND state IN (active, dead)` with the target moved aside; a row a rename or relink changed is
    left (`ErrLinkChanged`), and a row already unlinked at that path (a link shared by versions) is done
    (`TestUnlinkCompareAndSwap`, `TestUnlinkKeepsConcurrentReplacement`).
  - **Relink's repoint** never revives an `unlinked` row (`state IN (active, dead)`, `TestRelinkKeepsUnlinkedRow`).
    `ReconcileLinks` unlinks a vanished link only at the path and target it checked.
  - **An upgrade racing a rename**: the plan keeps each replaced link's id (`PlannedLink.ReplaceIDs`); apply finds
    the link by id and moves aside the path it has now, and unlinks the row by id. A rename that commits after the
    old symlink was moved aside leaves its new symlink, which the upgrade then moves aside too
    (`TestUpgradeRacingRename`).
  - **A rename that crashed** between making the new symlink and its commit is retried: a destination that already
    points at the target, with no link row of its own, is taken (`TestRenameRetryAfterCrash`).
  - **A crash after the commit**: a rename commits the new path before it removes the old symlink, and an import
    commits an upgrade before it moves aside a link renamed meanwhile (the stray). The relink sweep removes such a
    leftover (`leftover.go`; copied to the trash first, recorded as a `replace` action "leftover symlink moved aside
    …") when the database proves Theatarr retired it: no row at the path and an active row of the same version with the
    same target elsewhere (the rename), or the row at the path unlinked with the same target (the stray), in both
    cases a row that changed after the symlink was made, so a rename still in flight (its new symlink is newer) is
    left. A symlink put back from the trash by hand (its recorded trash copy, not purged, is gone; `mv` keeps the
    copy's older mtime) is kept (`TestRestoredFromTrashMovedAsideAgain`); its row stays `unlinked`. Only versions Theatarr may write to are checked, never a followed arr root; only symlinks into the mount, never a
    regular file or the mount (`TestRelinkMovesAsideCrashLeftovers`).
  - **Follower refreshes** of one version are exclusive across processes (service and CLI): a lease row
    (`library_lease` `refresh:<version>`, 00070) held while the refresh writes, renewed as it goes and taken over
    10 min after a crash. A second refresh fails with `ErrBusy` (the automatic one after an arr import skips the
    version and leaves it to the scheduled catch-up); a dry run takes no lease (`TestRefreshLease`). Without it two
    refreshes would each see the old link, and the loser of the row's compare-and-swap would remove the winner's new link.
    A refresh that stalled past its lease is fenced: it renews (rechecks) the lease after its trash copy and before
    replacing the link, before removing an old link, and in the transaction that records the swap; a lost lease stops
    it with `ErrBusy` before it overwrites a newer refresh's link (`TestRefreshStalledPastLeaseKeepsNewerLink`). When
    the record does not land (the row moved on, or the lease was lost), the path gets back what it held right before
    the swap: the previous symlink, the same path included, or no link at a new path; a link someone else rewrote
    meanwhile is left alone (`TestRefreshSamePathRestoredWhenCommitLoses`). What stays: a stall inside a single
    filesystem call cannot be fenced.
- Following zurg's log (`internal/library/zurglog*.go`): when `library.mount.zurg_log` is set, `theatarr serve` relinks
  as soon as zurg logs a folder rename (its two rename lines), batching a burst for 10 s and folding chains
  `a→b→c`, through `RelinkRenamed` and the gate (nothing is written unless `library.enabled` and the version is
  active). Both sources start at the current end; older renames are the sweep's and the dangling report's.
  Explicit renames take precedence over an existing old target (a newer torrent may now own the bare name), but
  move only when the same relative file exists at the reported destination; a missing file is reported without
  changing the link or trying another folder. A hint with a suffix contradicting the link's recorded hash is
  ignored; normal journal, gate and Plex-scan handling still applies (`TestRelinkRenameCollision`, `TestRelinkCollisionGate`).
  - A **file** is tailed every 2 s; a new inode or a shorter file is read from its start (`TestZurgLogFollower`).
  - A **URL** (`ZurgLogHTTPFollower`): zurg serves the whole current log as `text/plain` (~6 MB, rotated at ~10 MB),
    ignores Range and sends no ETag or Last-Modified, so every poll is a full GET through the gateway. The follower
    keeps the processed length (up to the last whole line; a partial line waits for the next poll) and a SHA-256 of
    the first 4 KiB. A body shorter than either, or with a different head, is a new file (rotation, truncation, or an
    empty body when the file is missing) and is read from its start. If the server ever sends ETag or Last-Modified,
    the next poll is conditional and a 304 skips the body. A failed fetch keeps the position and backs off (interval
    doubled per failure, at most 5 min or until the gateway's own back-off ends); lines appended meanwhile are read
    on recovery, once. **Loss on rotation:** lines written to the old file between the last poll and the rotation
    are served nowhere (`/logs` shows only the current file), so on each rotation `theatarr serve` runs the relink sweep
    at once instead of waiting for the hourly one. zurg redacts the body it serves; that only matters if its
    redaction changes mid-file (the head then differs and the file is reread: harmless, relinks are idempotent).
    Tests: `TestZurgLogHTTPOffsets`, `TestZurgLogHTTPShortHead`, `TestZurgLogHTTPConditional`,
    `TestZurgLogHTTPRunBatchesAndBacksOff`, `TestZurgLogHTTPThroughGateway`.
- `Verify` (`theatarr library verify`) is read-only: dirs, symlinks, links into the mount, regular files, dangling links with
  the relink candidate, and the arr's files whose names differ from Theatarr's.

### zurg's API

With `library.mount.zurg_api` set, `internal/library/zurgapi.go` reads four endpoints through the gateway, GET only
(response shapes as zurg serves them, `/ready` from zurg's public notes; zurg's source is not public):

| Endpoint | Answer | Used for |
|---|---|---|
| `/debug/torrents` | array of `{name, hash, size}`; `size` is the torrent's full size as the debrid service reports it; 0 for a torrent with nothing usable. It is not what zurg can still serve: a torrent whose video is lost can report many GB while its folder holds only `.nfo`/`.txt` | Lost-video classification; the import's folder lookup (zurg's name for the hash) |
| `/api/library/entry?path=/dav/<all_dir>/<folder>[/<file>]` | `{hash, name, imdb_id, size, media_info}` (size for files; `media_info` null until zurg probed); 404 when there is no such path. It also has `signed_path`, which is never decoded, stored or logged | Per-file confirmation |
| `/torrents/repair/status` | `{"running": bool}`, no torrents named | Repair grace |
| `/ready` | 503 while zurg is still loading its library, 200 once loaded; outside basic auth ([zurg docker notes](https://notes.debridmediamanager.com/zurg/setup/docker/): "If /ready fails with error 503 the library is still loading") | Mount readiness |

Every use falls back to the mount alone when the API is off, a request fails (the API is then left alone for one
interval), or there is no entry. The listing backs off on its own clock: a failed or missing `/debug/torrents` is not
asked again for one interval but does not mark the API down, so the per-file confirmation still asks for entries
(`TestImportZurgNameForHash`). Nothing is deleted, relinked, failed or blocklisted because of an API answer beyond
what the dangling path already did.

- **Lost video.** A listed torrent maps to its folders by `ReleaseFolderNames` (name without `.mkv`/`.mp4`, and with
  ` {hash6}`). A dangling link's torrent is found by its recorded hash (`linkHash`), else by its folder when every
  torrent zurg may show there has size 0. A link is also `no_video` when its release folder is listed on the mount but holds no video file (the mount index's listing, cached per folder in a pass); this catches most cases. The dangling report stores cause `no_video` on the row and, per report, the
  count of such links, of zurg's torrents without video, whether zurg was repairing and why the API was not read
  (migration 00063); `theatarr library dangling` prints a CAUSE column (not in the `-tsv` output) and the health
  check `DanglingLinksCheck` (a notice while any link dangles) says "N of them: torrent has no video upstream". Verify
  and the relink sweep report cause `no_video` with that reason; the sweep still marks the link dead as usual
  (`TestDanglingReportNoVideo`, `TestRelinkZurgNoVideoAndRepair`).
- **Per-file confirmation.** An import that finds its release folder by name asks zurg whose the folder is: a bare
  name held by another torrent of the same name is passed over for the next name (this download's ` {hash6}` folder),
  and the import waits when none is its own. Each video file's entry must then carry the download's hash and the size
  the mount shows (`PlannedFile.zurg`); a file that does not is not linked, and an import left with such files is held
  for review (`importBlocked`), never failed or blocklisted. Path and manual imports are the client's choice and are
  not checked. The relink sweep skips a candidate folder whose file zurg says is another torrent's (only when the
  link's full hash is known). The probe and every mount check still run (`TestImportZurgConfirmsRelease`,
  `TestRelinkZurgConfirmsCandidate`).
- **Repair grace.** The status names no torrents, so it is one global signal. While zurg repairs, the sweep reports
  every dangling link it cannot fix as cause `repairing` (reason keeps `no_video` when it applies) and does not mark
  it dead or flip its title to wanted; Verify says the same. The grace ends `zurg_api_repair_grace` after this process
  first saw the repair running, so a repair that never ends cannot hold links for ever; an unreadable status gives no
  grace (`TestZurgAPIClient`, `TestRelinkZurgUnreachableKeepsBehaviour`).
- **Mount readiness.** Every library mount check (`mountHealthy`: `MountHealthy`, `MountListed` and so the router's
  ready deadline, relink, the dangling-link sweep, verify, refresh, adopt, imports) asks `/ready` first: a 503 is
  `ErrMountDown` ("zurg is still loading its library"), so nothing is marked dead, repointed or refreshed against a
  half-loaded `__all__`, verify reports the mount as down, and the trash purge waits for its next run too. A 503 is
  reused for 15 s, a 200 for `zurg_api_interval`. Any other answer (404 from a zurg without the endpoint, another
  status, a refused connection, a timeout) is no answer: the stat and listing checks alone decide, as without the
  API, and it neither warns nor puts the other endpoints in backoff (`TestZurgReadyHoldsSweeps`,
  `TestZurgReadyNoAnswerFallsBack`). No health check reads file bytes through the mount: every such read is a fetch
  from the debrid service (the helper's only plain byte reads are the now playing pre-warm's), so the health check
  stays the stat, the listing and `/ready`.

## Commands

Manual rename applies only to theatarr-owned links: the new symlink is written under its version root, the link row is
updated, and the old symlink and any empty theatarr-made folders are removed through the library write gate.
Arr and native admin previews rebuild each link's path from its saved `file_metadata`, probe and episode rows; links
with incomplete metadata are counted and skipped. Applying a preview writes the new link first, updates its row,
then removes the old link, while the gate refuses the whole operation when library writes are closed. Each move is a compare-and-swap (above): a link a relink or
refresh changed meanwhile is left, the new link removed, and the rename answers `ErrLinkChanged`. Every completed
move queues debounced Plex partial scans of the old and the new folder, also when a later link of the batch fails
(`TestRenameQueuesPlexScans`). The web UI's title page reaches it through `serveOps` (`GET`/`POST
/api/theatarr/v1/titles/{id}/rename`; a gate refusal or a changed link is 409, `TestServeOpsRename`); the page says
"Names match" only after a successful preview found nothing, and shows a failed preview or apply as an error.

```
theatarr library dry-run -download ID [-json]   plan one import: files, probes, guard, link paths, what it moves aside
theatarr library verify [-version KEY] [-json]  read-only scan of a version's roots
theatarr library relink [-version KEY] [-dry-run] [-json]
theatarr library naming-sync                    read config/naming and qualitydefinition from the from_arr instances
theatarr library adopt -version KEY [-dry-run] [-limit N] [-queue] [-json]
                                          link a parallel version's followed arr files into its own root
```

All take `-db` and `-env-file` (settings come from the database). `dry-run` and `verify` write nothing at all, to disk or database. `adopt`
(start by copy for a parallel version) is described in [docs/parallel.md](../parallel.md).

## Migration 00015

`library_import` (the library's progress per download), `library_action` (every action: applied, dry run, refused,
failed), `link_trash`, `library_naming`. The `download` table is the router's (00014); the library sets only
`importing`, `imported`, `failed`, `error` and `imported_at`.

Migration 00038 adds `link.file_metadata` and `download.release_data`. Existing links are backfilled from their
download, content file and matching release rows; missing historical parse fields are reconstructed from the saved
release title on read (languages and flags absent from both the name and stored rows cannot be recovered).

## Known limits

- Change episodes with Replace unlinks the whole other link: its other episodes become wanted too.
- Theatarr assumes it is the only writer under its own version roots (and the mount is read-only). Its checks and the
  writes after them are separate steps: a check that a path is a symlink, or a missing destination, and the rename
  or removal right after it. Another local writer that replaced the path (or a parent directory with a symlink) in
  between could have that file overwritten or removed, or a write redirected. Not guarded against; nothing else
  should write in Theatarr's version roots. Followed arr roots are only read.

## Deviations and open points

| Design | Here | Why |
|---|---|---|
| Import upgrade check as the arrs' `UpgradeSpecification` | An equal file (same quality, revision and score) is not an upgrade; a proper replaces only under `prefer_and_upgrade` | The arrs import a file that is no worse; replacing an equal file only churns the library and Plex |
| Rejected content is blocklisted | The download fails with the guard's reason; the hash goes on the router's blocklist for the version | Nothing is deleted on the provider |
| rclone `vfs/refresh` while waiting | Polling only | Remote control is not set on the mounts |
| abema/go-mp4, go-mediainfo | Own readers | Byte budget; see Probe |
| Rename adopted links | Never renamed; an import reuses the title's existing folder | A renamed folder is a new item to Plex |

**Turning the library on.** The container needs `/mnt/debrid` read-only and the library roots plus the trash read-write
at the same paths Plex uses; then `library.enabled: true`, `trash.dir`, `naming.from_arr` per version, and
`theatarr library verify` before switching a version to active.

## SeaDex standing at import

Imports for versions with `prefer_seadex` pin one standing snapshot for planning and applying. For those versions
the actual allowed quality is checked on first imports and non-overlapping episodes as well as replacements; explicit
manual imports keep their override. Versions with `prefer_seadex` off check replacements only. The link-state
recompute pins standing once per run, and only a prefer-on version reads it. Record-id matches may identify held
standing but do not prove torrent identity. How standing is kept: [decide.md](decide.md#seadex-standing).
