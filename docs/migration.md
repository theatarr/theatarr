# Moving from Sonarr and Radarr

Coming from the arrs? See the [arr-to-Theatarr page map](arr-map.md) for routes and menu paths.

This is the one path from an existing Sonarr/Radarr setup (with Prowlarr, Seerr and Recyclarr) to Theatarr, in
stages. The old services are never changed, nothing here turns grabbing on by itself, and every stage can be undone.
Go one library at a time: every step is a setting you change and confirm.

## Who this is for

Anyone with a running arr whose libraries should move to Theatarr. A fresh library with no arr behind it does not
need this guide: it only needs the check and the switches ([quickstart.md](quickstart.md#9-go-live-deliberately)), or
starts live from Setup: on an install with no `import.arr` instance, **Create library and start downloading**
creates the library Active with its downloads on and turns on **Allow new downloads** and **Write library links** in
one confirmed save ([quickstart.md](quickstart.md#6-a-first-library)). An install that has an arr instance is never
offered that button, and the server refuses the request.

| Stage | What you do | Where | How to undo |
|---|---|---|---|
| [1. Inspect and plan](#1-inspect-and-plan) | Read the old services, save a plan and apply its automatic steps; everything lands disabled or in shadow | **Admin › Advanced › Sonarr/Radarr migration › Migrate**, or `theatarr migrate inspect`, `plan`, `apply`, `verify` | Each step's `undo`, or restore the checkpoint apply took ([rolling back the migration](#rolling-back-the-migration)) |
| [2. Shadow](#2-shadow-compare-with-the-arr) | Let the migrated library decide without grabbing, and compare its decisions with the arr's grabs | **Advanced › Sonarr/Radarr migration › Comparison**, **Activity › Decision journal**, `theatarr parallel compare` | Nothing to undo: a shadow library adds and links nothing |
| [3. Follower](#3-a-follower-library-beside-the-arr) | Add a follower library with a root and a Plex library of its own, adopt the arr's files, run a trial | **Settings › Libraries**, **Settings › Plex & playback**, `theatarr library adopt` | Turn the follower's downloads off or set it to shadow; the arr never stopped ([stage 6](#6-stop-switches-and-rollback)) |
| [4. Go-live check](#4-the-go-live-check) | Run the check before the trial, during it and before the handover | `theatarr golive check` | It only reads |
| [5. Hand over](#5-hand-over-clients-and-plex-retire-the-arr) | Move Seerr, the apps and Plex to the follower, then retire the arr | **Settings › Connected apps**, **Users & access**, each client | Restore the client settings and Plex sharing you saved; keep the arr's database and roots through the rollback window |
| [6. Stop and roll back](#6-stop-switches-and-rollback) | Stop one library or everything, without double downloads | **Settings › Libraries**, **Settings › Download clients**, `THEATARR_FORCE_OFF` | Turn the switches back on deliberately, one side at a time, never both as a catch-up |

[Reference](#7-reference) at the end has states and the cutoff, known limits, the JSON documents, the API and a
recipe for agents.

## 1. Inspect and plan

`theatarr migrate inspect | plan | apply | verify` moves the setup of an existing
Sonarr/Radarr fleet (with Prowlarr, Seerr and Recyclarr) into Theatarr's settings. The same operations are on the API
under `/api/theatarr/v1/migrate`. The migration never touches the old services, and it never turns grabbing on: everything it
adds lands disabled or in shadow, and the cutover per library stays a manual, confirmed step.

`theatarr migrate up | status` are the database migrations, a separate tool.

A first migration may create the install's first quality profile. Start `theatarr serve` once before applying, so
the database's quality settings are initialized; a database with no profiles yet is ready for that step.

### Workflow

1. **inspect** reads the old services with GET only and prints what they hold. Nothing is saved. Per arr this
   includes its Connect targets (`connect NAME (IMPLEMENTATION, on TRIGGERS) HOST`) and import lists (`list NAME
   (IMPLEMENTATION, enabled, auto add true) HOST`), so the ones that must survive the cutover can be listed by hand;
   plan does not migrate them. Their address is printed as scheme, host and port only (a webhook's path or query can
   hold a token; userinfo in a host such as `user:pass@host` is dropped; Email's plain server prints as host:port), and
   no field whose name looks secret is read. Sonarr lists have no enabled switch: one syncs only with automatic add
   on, so that is what `enabled` shows.
2. **plan** maps the inventory to Theatarr settings and saves the plan in Theatarr's database. Each step says what it
   adds, why, whether it can be undone and how. The plan also lists everything that will not be migrated (gaps).
   Plans are kept, so a later `show`, `apply` or `verify` resumes where the last one stopped.
3. **apply** takes a rollback checkpoint, then runs the plan's automatic steps through the settings API, one saved
   settings generation per step. A step that needs a confirmation stops with the confirmation's effects and a token for the caller. The CLI
   and MCP use an API key, which confirms like a signed-in user: MCP's `migration_apply` with `confirmation_token`, or the API's
   `confirm`/`confirmation_tokens` (the CLI has no confirm flag). A token is bound to the key or
   session it was issued to, so a step applied first in the UI is confirmed in the UI (Admin › Advanced › Sonarr/Radarr migration › Migrate).
4. **verify** answers, per library, whether it is ready to hand over: every automatic step done, the library's
   settings in place, `theatarr golive check` for the library, and an adopt and refresh dry run when the library
   follows another one. It prints the manual cutover and rollback steps.
5. **cutover** is manual, one library at a time ([stage 3](#3-a-follower-library-beside-the-arr) to [stage 5](#5-hand-over-clients-and-plex-retire-the-arr)): trial the active follower while its
   parent stays shadow, move clients and Plex to the follower’s roots, then retire the parent’s arr. The parent’s
   roots stay read-only to Theatarr. Enabling the follower’s Grab is a confirmed change in Settings › Libraries.

```
export THEATARR_API_KEY=...                  # an admin's API key (Users & access › API keys)
theatarr migrate inspect -env-file arr.env \
    -arr radarr_uhd=radarr,http://radarr:7878,RADARR_UHD_KEY \
    -arr sonarr_anime=sonarr,http://sonarr:8989,SONARR_ANIME_KEY \
    -prowlarr http://prowlarr:9696,PROWLARR_KEY -seerr http://seerr:5055,SEERR_KEY -recyclarr recyclarr.yml
theatarr migrate plan -env-file arr.env [the same source flags] -name fleet \
    -provider realdebrid=RD_KEY -path-map /movies=/media/movies-uhd -prowlarr-url http://prowlarr:9696
theatarr migrate apply -plan 1 -env-file arr.env
theatarr migrate verify -plan 1
```

The source flags can also come from a file, `-sources sources.yaml`:

```yaml
arr:
  - {name: radarr_uhd, flavour: radarr, url: "http://radarr:7878", key: RADARR_UHD_KEY}
  - {name: sonarr_anime, flavour: sonarr, url: "http://sonarr:8989", key: SONARR_ANIME_KEY}
prowlarr: {url: "http://prowlarr:9696", key: PROWLARR_KEY}
seerr: {url: "http://seerr:5055", key: SEERR_KEY}
recyclarr: /config/recyclarr.yml
```

| Command | Flags |
|---|---|
| `inspect` | source flags, `-json`, `-raw` (with `-json`: keep the arrs' profile, format and naming bodies, which `plan -inventory` needs), `-o FILE` (mode 0600) |
| `plan` | source flags or `-inventory FILE`; `-name`, `-native NAME,...\|all` (Prowlarr indexers to run with Theatarr's own Cardigann definitions), `-skip NAME,...`, `-provider kind[:key]=KEYNAME` (a debrid account to add, disabled), `-path-map FROM=TO` (an arr path as Theatarr sees it), `-prowlarr-url` (Prowlarr as Theatarr reaches it) |
| `list`, `show -plan ID` | |
| `apply -plan ID` | `-step ID` (repeat; default every open automatic step), `-env-file` |
| `verify -plan ID` | |
| `seerr-ids` | `-seerr URL,KEYNAME`, `-radarr-view PATH` (default `/radarr`), `-sonarr-view PATH` (default `/sonarr`), `-env-file` ([below](#seerr-ids)) |

Every command takes `-json`. Commands that talk to Theatarr take `-service URL` (default `http://127.0.0.1:7700`) and
`-api-key-env NAME` (default `THEATARR_API_KEY`); the CLI sends the key to a loopback or LAN address only. `apply` exits 1 when an automatic step is not done; `verify` exits 1
when a library is not ready; `seerr-ids` exits 1 when a stored Seerr id names a different title.

### Keys

Keys are named, never given: a source names an environment variable (`RADARR_UHD_KEY`), and the CLI reads its value
from the environment or `-env-file` (variables already set win). Inspect sends a key only to its own service, in the
`X-Api-Key` header. Keys never appear in the inventory, the plan, the output, Theatarr's database or its log; the
inventory is refused if a key value turns up in it (a service echoing it back). `plan` inspects on the CLI's host and
sends Theatarr the inventory only, so arr keys never reach Theatarr. `apply` sends Theatarr only the keys the plan's
records take (a debrid account's `Payload.secret`), for that request only; Theatarr stores them as secrets like
Settings › Download clients does. A key that is not set is not an error: the record is added without it, and the step's
note says to enter it in Settings.

### Seerr ids

Seerr stores each title's id in the arr it was sent to. `theatarr migrate seerr-ids` checks, before and after Seerr is
pointed at Theatarr, which title each stored id names in the view it reaches:

```
export THEATARR_API_KEY=...
theatarr migrate seerr-ids -env-file arr.env -seerr http://seerr:5055,SEERR_KEY [-json]
```

It reads Seerr's servers and media (`GET /api/v1/media`, paged) with GET only and writes nothing anywhere. The views
are asked through the running service with the admin key, as Seerr asks them (`GET movie/{id}`, `series/{id}`). Each
id is ok, wrong title, unresolved or unverifiable; `-json` prints every id with the view's answer. Seerr's own Radarr
and Sonarr scans rewrite the ids of every title the views list, so the cutover step is: repoint Seerr, run both scans,
run `seerr-ids` again and expect no wrong title ([arrview.md (internals)](design/arrview.md#seerrs-stored-ids) has what Seerr does
with each id).

### In the UI

Open **Admin › Setup › Coming from Sonarr/Radarr? › Migrate from arrs**, or **Admin › Advanced › Sonarr/Radarr migration › Migrate**.
Add named Sonarr/Radarr instances, optional Prowlarr and Seerr/Overseerr, and the path to a Recyclarr config on the
Theatarr server. Source API keys are write-only, kept only in page memory and sent only to inspect; saving a plan or
leaving the page clears them. Inspect shows counts, libraries and grouped gaps with their reasons. Pick a plan name
and native/Prowlarr/skip for each Prowlarr indexer, then create the plan (this reads the sources again with those
choices). Review what each step changes, why, whether it is reversible and how to undo it; select automatic steps
to apply. Dependencies are selected together; manual and blocked steps need attention outside the wizard.

**Start another migration** resets the wizard to empty Sources, including immediately after saving a new plan;
the saved plan remains available through its Resume link.

Apply takes a backup checkpoint and shows its backup path, settings export and generation. If a step needs
confirmation, review its effects in the shared dialog before retrying. Saved plans reopen at review, apply or verify
according to their progress; their URL includes the plan ID, so reloading keeps the plan. Verify shows each library's
checks, fixes and pending work, with links to **Libraries** and **Sonarr/Radarr migration › Comparison**. Configure missing
credentials under **Settings › Download clients** or **Settings › Indexers**, compare in shadow first, and follow [stages 2 to 5](#2-shadow-compare-with-the-arr) for the later handover;
the wizard never enables the router or library switches.

The inspect API accepts optional `options` to return a `plan` preview with gaps and frozen quality profiles while
the raw arr bodies are still available on the server. With `save_plan: true` (requires `options`) it also saves that
plan for apply/resume. Without `options` its inventory response is unchanged; raw bodies and source key values are
never returned. When a step needs confirmation, apply returns the plan with that step in `needs_confirmation`, its effects and a
one-use token. The wizard retries one confirmed step at a time with `confirm` and `confirmation_token`; a token made
stale by another settings change is refused and the step can be reviewed again.

### What is migrated

| Step | Lands as | Notes |
|---|---|---|
| `budget/<key>` | a request budget | for Prowlarr's per-indexer limits |
| `indexer/<key>` | an indexer, **disabled** | a Prowlarr indexer as a Torznab feed through Prowlarr (`prowlarr-<slug>`), or native with `-native`; an existing feed with the same address is reused. A private tracker (Prowlarr's verdict) is added and listed in the libraries like any other: privacy is a label. Arr history names indexers by the arr's name; Theatarr matches it to a configured indexer by name (`Policy.IndexerByName`); a name that does not normalize to the key (an arr-side name such as "DMM RD (proxy)") goes in the indexer's `arr_names` alias list |
| `provider/<key>` | a debrid provider, **disabled** | an existing provider of the kind is reused; a new one needs `-provider kind=KEYNAME` |
| `quality/<profile>` | a quality profile and its format set | from the arr's profile, custom formats and naming; Recyclarr's origin is recorded. Needs Theatarr's settings to manage quality (start `theatarr serve` once) |
| `version/<key>` | a library in **shadow, grab off** | library under the mapped arr root, which must exist and sit inside `library.dangling.roots`. No download client is needed: a debrid library adds to its providers directly (an existing `decypharr` client is named when there is one) |
| `view/<view>/<key>` | the library added to an arr view | so Seerr can be pointed at Theatarr at cutover |
| `import/<arr>`, `follow/<key>`, `seerr/<flavour>/<id>` | **manual** | the `import.arr` entry, the optional parallel follower ([stage 3](#3-a-follower-library-beside-the-arr)), and each Seerr server's switch. Apply never runs them; Seerr is never written |

The `import/<arr>` step is done in the settings database, not the policy files (the database owns the `import`
section; a YAML edit is ignored once it is seeded): in Admin › Advanced › Sonarr/Radarr migration › Instances add
`arr.<name>` with its flavour, url, `api_key_env` and library (`version`), save, then enter the credential `arr.<name>.api_key`
(write-only; without it Theatarr reads the `api_key_env` variable). The section applies without a restart. Over the
API it is one `PUT /api/theatarr/v1/settings/sections/import` with `yaml` and `secrets: {"arr.<name>.api_key": ...}`.
`verify` passes the import check once the entry feeds the library.

Apply is idempotent: a step whose record is already there as planned is marked done without a write; one whose
record exists with other settings is a `conflict` and is left alone. Steps run in order and a step waits for the
steps it depends on. Statuses: `pending`, `done`, `failed`, `blocked`, `conflict`, `needs_confirmation`, `manual`.

Apply never sets a library active or grab on, never touches `router.enabled` or the other global switches, and
refuses a step whose payload would (a hand-edited plan).

### Gaps

Each gap has a topic: `secret` (a key the plan cannot name), `usenet` (Theatarr has no usenet path yet),
`local_library` (an arr that imports to local disk), `profile` (a profile Theatarr cannot build),
`paths` (no mapping, or the library is outside the library area), `prerequisite`, `unsupported` (delay and release
profiles, a Seerr server pointing at an arr that was not inspected, and the like), `unread` (a service inspect could
not read).

## 2. Shadow: compare with the arr

A migrated library starts in shadow with its downloads off, and its arr keeps running. The shadow runner decides on
the arr's grabs and on the RSS feeds and records what Theatarr would have done; nothing is added or linked.

Let the library decide in shadow for a while and compare its decisions with the arr's grabs in **Advanced ›
Sonarr/Radarr migration › Comparison** and **Activity › Decision journal** ([journal.md](journal.md)). Explain each
difference before you go on. The go-live check's default bar is 95% agreement over 14 days, with at least 20 arr
grabs ([stage 4](#4-the-go-live-check)).

Once a follower grabs ([stage 3](#3-a-follower-library-beside-the-arr)), compare its routes with the arr's grabs:

```sh
docker compose exec theatarr theatarr parallel compare -library movie-remux-theatarr -since 7d
docker compose exec theatarr theatarr parallel compare -library movie-remux-theatarr -since 7d -json
```

This reads each arr feeding F and compares its recorded grabs with K's routes, without changing who owns the
imports. It reports same hashes, different releases with quality and score, unmatched choices and routes already
covered by an arr file. `golive check` shows the same measurement in its parallel section (95% bar; too few grabs warn). Keep F's arr running during the trial: after it is
retired there are no new arr grabs to compare. The arr and K can pick the same release while they run side by side;
that is not K adding content twice.

Episode comparisons resolve Sonarr IDs through the followed instance's episode aliases. Unmapped episodes remain
arr-only. `-since 7d` means 168 hours; Go durations such as `36h` are accepted.

## 3. A follower library beside the arr

A library migrated from an arr, **F**, stays in shadow and its arr keeps running. You add a **follower** library
**K** (`follows: F`) with its own roots and its own Plex section. K takes F's titles and monitoring from the arr,
starts with links to the arr's current files, and then grabs for itself. F's roots stay read-only to Theatarr
throughout. Going live means proving K, moving clients and Plex to K, then retiring F's arr. Theatarr never takes
over F's folders in place. A follower is also called a **parallel library**: it runs `mode: active`
beside the arr that serves F, and Plex shows it in sections only the admin sees until the handover.

Below, `movie-remux` is F and `movie-remux-theatarr` is K. Use your own keys.

### What a follower gets

- **Titles and monitoring from the followed library.** Every `arr.import` of an instance feeding F also writes each
  follower K (`internal/importarr/follow.go`): the title, a library row for K (created `wanted`), the movie's
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
  so the rest of Theatarr treats it as a library Theatarr manages: tvrefresh adds new episodes from the monitoring intent, the wanted
  loop searches what K misses only while `router.wanted.missing` is on (otherwise RSS or a manual search fills
  K's gaps), the router adds through debrid (`providers` of K) and the library links under K's root. F stays
  `shadow`: Theatarr never searches, adds or links for it.
- **Its own symlinks, started by copy.** `theatarr library adopt` ([below](#adopt-and-refresh)) links the arr's current files into K's root
  under Theatarr's naming, pointing at the same mount targets, so K starts complete instead of downloading everything
  again. While K does not grab, each arr import also adopts newly imported files and refreshes existing adopted
  links after arr upgrades. After K starts grabbing, an upgrade in K replaces only Theatarr's link; the arr's file is untouched.
- **The arr's monitored collections.** Each Radarr import carries the collections the arr monitors over to
  movie intake, targeting K when K is active (never F: a followed library's roots are its arr's; docs/design/arrview.md,
  Collections). Movies intake adds to K
  that the arr does not have are K's own: the import never unmonitors them, as it only unmonitors titles the arr owned.
- **Not in default adds.** UI adds and requests that name no libraries leave parallel libraries out
  (`catalog.DefaultVersions`); K follows F's titles through the import instead. The default `/radarr` and `/sonarr`
  views are unchanged. K has its own view at `/v/K/radarr` (or `/sonarr`) for a test client.

### Policy

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

Validation (`internal/policy/follow.go`) refuses: following itself, an unknown library or another parallel library;
a different kind or anime flag; an `import.arr` instance feeding K; no `library.versions.K`; roots overlapping any
other library's roots or library folder (F may have several roots; K has one); naming that is neither explicit nor
`from_version`; F in `mode: active` (its roots are its arr's). `naming.from_version: F` takes F's naming (explicit or `from_arr`) and nothing else may be
set beside it. As a second line, the write gate treats a followed library as not writable whatever its mode says.

Validation and the write gate resolve every library's roots and the trash through symlinks; overlapping physical
paths and roots with the same device and inode (including visible bind aliases) are refused. With the mount table
(`/proc/self/mountinfo`) each directory is also placed in its filesystem (device and path inside it), together with
every mount below it, so a bind mount of one root inside another root is refused too
(`TestNestedBindAlias`). The gate rechecks before writes, and the parallel go-live check reports the resolved paths
and fails on aliases.

A simple layout names K after F with a suffix (`movie-remux-theatarr`) and gives each follower one root under a
common folder (`/media/library/movies-remux`, `/media/library/tv`, ...) and one Plex section. An arr whose files are
not on the debrid mount (a usenet mount, local disk) gains little from a follower: adopt skips links that point off
the mount.

### Adopt and refresh

```
theatarr library adopt -library K [-dry-run] [-limit N] [-json]   link F's arr files into K's root
theatarr library adopt -library K -queue                          run it as the library.adopt job in theatarr serve
theatarr library adopt -library K -refresh [-dry-run] [-json]     move adopted links to the arr's current files
theatarr library adopt -library K -refresh -queue                 the same as a library.adopt job in theatarr serve
```

**Adopt** links the arr's current files of F into K's root under K's naming, pointing at the same mount targets, so
K starts complete instead of downloading everything again. It links only what is missing and never overwrites, so
running it again is safe. `-dry-run` writes nothing; a real run needs the gate open for K (`library.enabled`,
`mode: active`, roots). A file it cannot link is skipped with the reason in the report (`arr_file_missing`,
`not_a_symlink`, `target_off_mount`, `dangling`, `path_taken`, `no_episodes`, `naming`). A pack that overlaps K's own
episodes is skipped whole and counted under `missing_episodes` and `partial_packs`. A mount that is down, or a root
that does not answer, stops the run before anything is read.

**Refresh** moves the links adopt made to the arr's current files after the arr upgrades one: the old link goes to
the trash and Plex scans the folder. It never touches a link of Theatarr's own imports. It leaves alone, and reports,
`arr_file_removed`, `arr_episodes_split` and `target_missing`.

While K does not grab, `theatarr serve` does both by itself after every arr import, for the titles whose arr files
changed, and catches up over every title at the first import after a start and then once an hour.
[design/library.md (internals)](design/library.md#adopt-and-refresh-for-parallel-libraries) has the rules in full.

Adopt is manual and runs once; refresh follows the arr's upgrades only while K does not grab. So once grabbing is on
for K (`router.enabled`, `mode: active` and `grab: true`), K loses refresh, and a file the arr imports for F after K's last adopt is
never adopted. Add-time search intents made while the router is off are dropped (`acquire/intent.go`), and with
`router.wanted.missing: false` nothing searches K's missing items by itself. Right before the flip, per follower:
`theatarr library adopt -library K`, then `theatarr library adopt -library K -refresh`. Anything still missing after that is left
to RSS or a manual search; Theatarr runs no catch-up search for it. `theatarr golive check -library K` shows whether it is due ("adopt and refresh", [stage 4](#4-the-go-live-check)).

Scan each of K's Plex sections once after its first adopt.

### Before the trial

1. **Read the shadow evidence** ([stage 2](#2-shadow-compare-with-the-arr)). Explain each difference between F's
   decisions and the arr's grabs before you go on.
2. **Create K.** In **Settings › Libraries**, add a library that follows F, with a root of its own (for example
   `/media/library/movies-remux`), the profile and naming you want, its indexers and providers. Keep every other
   follower's **Allow downloads for this library** off. Check the mount, the trash and the roots.
3. **Give K a Plex library.** Create one Plex library on K's root, shared with no one yet, and set its section for K
   under **Settings › Plex & playback**. Your users keep watching F's sections.
4. **Review legacy queued work.** `theatarr router reconcile` lists queued work left from before. Review it, then
   `theatarr router reconcile -apply` settles the automatic rows; manual rows need a new selection. Work in flight
   is left alone ([router.md (internals)](design/router.md)).
5. **Adopt and refresh K**, with K active, downloads off and **Write library links** on, right before the trial:

   ```sh
   docker compose exec theatarr theatarr library adopt -library movie-remux-theatarr
   docker compose exec theatarr theatarr library adopt -library movie-remux-theatarr -refresh
   docker compose exec theatarr theatarr golive check -library movie-remux-theatarr
   docker compose exec theatarr theatarr backup now
   ```

   Resolve every FAIL and explain the coverage, dangling-link and agreement WARNs. Adoption skips missing or
   off-mount source files; it cannot repair them. Once K grabs, adoption and refresh stop for K, and new gaps are
   left to RSS or a manual search. Keep the backup set.

### The trial

Write down the settings and the success criteria before you turn anything on.

1. **Settings › Libraries:** K is Active and is the only follower whose **Allow downloads for this library** will be
   on. F stays in shadow. **Write library links** stays on for K's imports.
2. **Settings › Download clients › Behaviour:** start conservatively. Keep cached-only adds on, keep the wanted loop's
   missing and cutoff-unmet searches off, and use a low searches-per-hour limit. Set the providers' pacing to what
   their API limits allow. Don't cap Real-Debrid or AllDebrid with an uncached-adds-per-hour limit: on those
   providers the cache check is itself an add. Keep **Send notifications** off, and review the request budgets under
   **Advanced › Request limits**.
3. Turn K's **Allow downloads for this library** on, then **Allow new downloads**. Review each confirmation's
   effects. Check `theatarr router status` and run the go-live check again.
4. **Test the stop switch in the first minutes:** turn K's **Allow downloads for this library** off and check that
   no new provider adds start. A request already sent may finish, and ready downloads can still import. Turn it back
   on once the behaviour is clear.
5. Run the trial for about a week. Try at least three different cached releases (a new episode, a season pack, an
   upgrade, or their movie equivalents) and play each in K's Plex library.

**Success criteria**

- Every download imports within the stall timeout plus one poll, or ends with an explained failure; none stays stuck
  adding, added or ready, and the unsettled-adds health check stays PASS.
- No uncached add remains on the provider. Automatic cleanup deletes only Theatarr's own adds, never reused or
  foreign torrents.
- Links land only in K's root, replaced links go to the trash, and Plex shows new files within a minute. Every trial
  release plays.
- No repeat add of the same hash for the same title and library; a release imported before is recognised.
- K's dangling-link count stays at zero, no unexplained errors appear, and the stop-switch test passes.
- After a week, compare K's routes with the arr's grabs ([stage 2](#2-shadow-compare-with-the-arr)). Explain the differences and check that nothing was
  added twice. A good overall percentage does not excuse an unexplained choice.

### What to watch

**First hour:** the container logs, **Activity › Queue**, **System › Status**, **System › Errors** and **Activity ›
Decision journal**. Follow each download from add to poll, import, link and Plex; watch provider refusals and their
reasons, mount and VPN health, and playback.

**First day and week:** cached-only behaviour in **Admin › Torrents**, imports and upgrades confined to K's root and
trash, dangling links, repeated hashes, comparison differences and new errors. Check **System › Backup** for a
complete nightly set. Test requests through K's own view before you switch your users' clients.

## 4. The go-live check

```sh
docker compose exec theatarr theatarr golive check
docker compose exec theatarr theatarr golive check -library movie-remux-theatarr
docker compose exec theatarr theatarr golive check -library movie-remux-theatarr -days 7 -json
```

The check reports PASS, INFO, WARN or FAIL with a fix for each, and exits 1 on a failure. It only reads; it never
activates a library. Without `-library` it checks every library; a followed library reports that it is cut over
through its follower.

It checks the profile build, the mode and the download and global switches, provider order and credentials, indexer
budgets, the mount, writable roots and trash, the Plex section, metadata, health and the backlog. A followed
library's roots are read-only by design: the check makes sure they exist and are readable, not that Theatarr can
write there. A configured VPN check must pass for its required traffic; without one the check warns. Credentials
are named by variable or settings path, never shown.

For a follower, `theatarr golive check -library K` replaces the shadow section with `parallel`: the followed library and the instances
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

The follower sample counts the arr's grabs: same-hash matches, different-hash matches and arr-only rows. A PASS does
not replace the playback and stop-switch evidence.

A library with no arr to compare with (a fresh install, no Sonarr/Radarr migration: no `import.arr` instance feeds
it and no arr grabs are recorded for it) gets INFO on `window` and `agreement`: "no arr feeds K: no shadow window
needed" and "no arr feeds K: nothing to compare, skipped". Neither line counts against the exit code. There is no
evidence that its decisions match anything, so read its decisions in the journal before going live.

A new library that an arr feeds can get a FAIL on `window`: "the shadow runner has not run". The shadow runner records
a run each time it reads the RSS feeds (every 15 minutes, starting a few minutes after `serve` starts) or imports an
arr's grabs, and no run is recorded yet. The RSS job runs only while a shadow library has an indexer with a feed (see
[quickstart.md](quickstart.md#6-a-first-library)), so check that first. Leave `serve` running and run the check again
later: the FAIL turns into a WARN that counts the days the runner has run, until the window (`-days`) is covered.

The check names settings by their YAML keys. Where each one is in the UI:

| Key | Settings page |
|---|---|
| `versions.K.*` (`grab`, `library`, `providers`, ...) | Settings › Libraries, the library K |
| `library.versions.K` roots | Settings › Libraries, the library's write roots |
| `library.versions.K.plex_section`, `library.plex` | Settings › Plex & playback (the Plex library for each Theatarr library) |
| `library.mount.path`, `library.trash.dir` | Settings › Libraries › File management |
| `metadata.sources.tmdb`, `metadata.sources.tvdb` | Settings › Metadata & artwork |
| `router.enabled` (**Allow new downloads**) | Settings › Download clients, the switch above the tabs |
| `router.cached_only` | Settings › Download clients › Behaviour (What to grab) |
| `router.wanted.enabled` | Settings › Download clients › Behaviour (Automatic searches) |
| `library.enabled` (**Write library links**) | Settings › Libraries |
| `import.arr` | Admin › Advanced › Sonarr/Radarr migration (the Sonarr and Radarr instances) |
| `egress` | Settings › Indexers › VPN |
| `views` (the views section) | Settings › Connected apps |

`-provider-expiry key=YYYY-MM-DD` warns when a provider plan ends within 30 days, and `-public-url` (or
`THEATARR_PUBLIC_URL`) puts Theatarr's address into the client steps it prints.

## 5. Hand over clients and Plex, retire the arr

For a followed shadow library F, the handover means switching clients and Plex libraries to the active follower K's
roots. F's arr roots stay read-only throughout; after clients and Plex move, retire F's arr and roots. Theatarr
never takes over F's folders in place.

### Before retiring the arr

List what still depends on the arr and decide what replaces each piece:

- every client of the arr: Seerr or Overseerr servers (default, 4K, anime), phone apps, Homepage, autobrr and any
  scripts or webhooks;
- the arr's import lists and Connect targets (`theatarr migrate inspect` lists them);
- indexers that only Prowlarr reaches, if you plan to retire Prowlarr too;
- usenet downloads, which Theatarr does not handle.

Keep Prowlarr while any library still uses one of its feeds.

### Hand over clients and Plex, then retire F's arr

1. Back up each client's settings and pick a quiet time for requests. Prepare K's views under **Settings ›
   Connected apps** and client API keys under **Users & access**. K's own test view is
   `/v/movie-remux-theatarr/radarr` (`/sonarr` for series).
2. Repoint **Seerr or Overseerr** server by server: pick the view, profile and K's root again. Check the stored ids
   first with `theatarr migrate seerr-ids` ([Seerr ids](#seerr-ids)), then test an existing request
   and a new one against the intended titles and libraries.
3. Repoint the phone apps, autobrr and Homepage to K's view and a client key. Test browsing, requests, release
   pushes and widgets. A pushed release must name an indexer K lists. After checking Homepage, add `homepage` to
   K's `golive_done` list (an acknowledgement for the go-live check, not an automatic test).
4. Switch your users' Plex access to K's library after checking playback and sharing. Keep F's Plex libraries and
   roots available for a rollback; don't point K at F's folders.
5. When every client has moved, stop new arr searches, drain its queue, disable its download clients and stop the
   arr. Keep its database, configuration and roots through the rollback window.
6. End K's mirroring deliberately: set K's naming explicitly before you remove the inherited naming, review the
   confirmed removal of `follows` in **Settings › Libraries**, and remove the arr import entry under **Advanced ›
   Sonarr/Radarr migration**. Check that K's monitoring and requests now stand on their own. Run the go-live check
   and a request and playback test again.

## 6. Stop switches and rollback

**During the trial (the arr never stopped):** turn K's **Allow downloads for this library** off in **Settings ›
Libraries**, then **Allow new downloads** off if no other library should grab. This stops new adds; a provider request
already sent can finish. Ready downloads can still import while **Write library links** is on. Review them in
**Activity** and remove unwanted queued or ready work there (ending Theatarr's record does not delete the provider
torrent).

With K still active and following F, turning its downloads off resumes adoption and refresh at later arr imports;
refresh never replaces K's own imported links. The arr keeps doing its job, so there is no arr catch-up search to
cause duplicates. To stop all work for K, set it to shadow as well: that stops new links and adoption too. Existing
links stay available in Plex.

**After the handover:** stop K's downloads first, account for every download in flight, and restore the client
settings and Plex sharing you saved. If the arr was stopped, keep its searches off while you compare its catalogue
and files with what K acquired. Add new requests deliberately and reconcile missing content before you allow arr
searches; the arr does not adopt K's roots. Never turn both sides back on at once as a catch-up. Keep the original
roots; don't move K's links into F's root.

**Emergency stop:** turn **Allow new downloads**, **Write library links** and **Send notifications** off on their
Settings pages; they affect every library. `THEATARR_FORCE_OFF=router,library,notify` forces them off at startup
whatever the saved settings say; it needs a restart and can never turn anything on. A database or image rollback
uses a backup set ([restore.md](restore.md)); it cannot undo provider adds or filesystem changes.

### Stopping or removing a follower

With DB settings, edit mode/follows/root and view bindings in **Admin › Settings › Libraries**. Shadowing
stops new work; removing follows or removing a library with links requires the specific confirmation shown there.
Removal only stops management: operational link rows, filesystem links, mount content and trash remain intact.

Set K to `mode: shadow` (Theatarr stops searching, adding and linking for it; its links stay and Plex keeps showing them)
or remove K from `versions` and `library.versions` (its links stay on disk until removed by hand; the Plex sections
can be deleted). Nothing to undo on the arr side.

### Changing a root with links

The library editor refuses to change a root with active links. Keep the old root and library, create a parallel
library with a separate existing root inside the allowed library area, configure its naming in the naming editor,
then use the adopt and coverage checks of stages 3 and 4 before switching Plex and clients. Retire the old library only after
reviewing its removal confirmation; Theatarr never migrates or deletes the old links as part of a settings edit.

### Rolling back the migration

- **One step**: each step's `undo` says how (delete the record in Settings, or turn it off). Advanced › Settings history
  shows the generation each step saved (`generation` on the step).
- **The whole migration**: apply takes a checkpoint before its first change: a full `theatarr backup` set (database,
  policy files, manifest) in serve's `-backup-dir`, plus the settings head exported (credentials stripped) beside it
  as `<set>.settings.yaml`. Stop Theatarr and run `theatarr backup restore -from <set> -db ... -policy-out ...`
  ([restore.md](restore.md)). Each apply run with open steps adds a checkpoint; the newest is in
  `verify`'s `rollback`.
- **A library after cutover**: `verify` lists the steps (grab off, back to shadow, Seerr back to the arr, the arr's
  RSS on again).

Apply refuses to start when backups are off (`-backup-dir off`) or the checkpoint fails, and then nothing is
written. A step that fails leaves the checkpoint and the steps before it in place; fix the cause and run apply again.

## 7. Reference

### States and the cutoff

The two libraries' states use different cutoffs and cannot be compared one to one:
- **The followed library F** takes its state from the arr's `qualityCutoffNotMet` (`arr.import`). That flag checks
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

### Known limits

- The UI does not mark K as mirrored, so monitoring edits there are silently reset by the next import.
- Adopted links keep the arr's score but not its custom format list. That changes no decision: the cutoff and
  every upgrade comparison recalculate the formats from the file's inputs.

### JSON

Every document carries `schema_version` (1). New fields may be added within a version; a removed or changed field
bumps it.

```jsonc
// inspect -json (an Inventory)
{"schema_version": 1, "inspected_at": "...",
 "arr": [{"name": "radarr_uhd", "flavour": "radarr", "url": "...", "key": "RADARR_UHD_KEY", "error": "",
          "app_version": "5.x", "titles": {"total": 3, "monitored": 3, "with_files": 2, "anime": 0},
          "root_folders": [{"path": "/movies", "titles": 3}], "quality_profiles": [{"id": 7, "name": "UHD", "titles": 3}],
          "custom_formats": ["..."], "tags": ["..."],
          "indexers": [{"name": "...", "implementation": "Torznab", "protocol": "torrent", "rss": true, "search": true,
                        "priority": 25, "prowlarr_id": 4}],
          "download_clients": [{"name": "...", "implementation": "QBittorrent", "protocol": "torrent", "enabled": true,
                                "priority": 1, "host": "...", "category": "...", "bridge": "decypharr"}],
          "notifications": [{"name": "Plex", "implementation": "PlexServer", "triggers": ["Download", "Upgrade"],
                             "target": "http://plex:32400"}],
          "import_lists": [{"name": "...", "implementation": "RadarrImport", "enabled": true, "auto_add": true,
                            "source": "http://radarr:7878"}],
          "delay_profiles": 1, "release_profiles": 0, "hardlinks": true, "raw": {"qualityprofile": [...]}}],
 "prowlarr": {"url": "...", "key": "PROWLARR_KEY", "indexers": [...], "applications": [...]},
 "seerr": {"url": "...", "key": "SEERR_KEY", "radarr": [...], "sonarr": [...]},
 "recyclarr": {"path": "...", "instances": [...]}}

// plan, show -json: {"plan": Plan, "inventory": Inventory}
{"schema_version": 1, "id": 1, "name": "fleet", "created_at": "...", "updated_at": "...", "options": {...},
 "libraries": [{"name": "movies", "kind": "movie", "versions": [{"key": "movie-uhd", "instance": "radarr_uhd",
     "library": "/media/movies-uhd", "arr_root": "/movies", "profile": "uhd", "arr_profile": "UHD",
     "indexers": ["..."], "providers": ["realdebrid"], "client": "...", "view": "radarr"}]}],
 "indexers": [{"key": "prowlarr-foo", "prowlarr_id": 4, "name": "Foo", "choice": "prowlarr", "privacy": "public"}],
 "steps": [{"id": "version/movie-uhd", "kind": "version", "what": "...", "why": "...", "reversible": true,
            "undo": "...", "automatic": true, "depends_on": ["quality/uhd"], "status": "pending", "error": "",
            "confirmation": null, "generation": 0, "notes": []}],
 "gaps": [{"topic": "unsupported", "subject": "radarr_uhd", "what": "...", "why": "..."}],
 "checkpoints": [{"at": "...", "backup": "/data/backups/theatarr-...", "settings": "...settings.yaml", "generation": 12}]}

// list -json
[{"id": 1, "name": "fleet", "created_at": "...", "updated_at": "...", "steps": {"done": 9, "manual": 6}}]

// apply -json: the Plan after the run

// verify -json
{"schema_version": 1, "plan_id": 1, "at": "...", "pending": ["..."], "rollback": Checkpoint,
 "versions": [{"key": "movie-uhd", "instance": "radarr_uhd", "ready": false,
               "checks": [{"name": "plan", "status": "FAIL", "message": "...", "fix": "..."}],
               "golive": {...}, "cutover": ["..."], "rollback": ["..."]}]}
```

### API

All routes need an admin, signed in or with an API key in `X-Api-Key` (no CSRF token then). Shapes are in
`internal/web/openapi.json`.

| Route | Body | Answer |
|---|---|---|
| `POST /api/theatarr/v1/migrate/inspect` | `{"sources": Sources, "keys": {"NAME": "value"}}` | the Inventory without `raw` |
| `GET /api/theatarr/v1/migrate/plans` | | the plan list |
| `POST /api/theatarr/v1/migrate/plans` | `{"sources", "keys"}` (Theatarr inspects) or `{"inventory"}` (with `raw`), plus `"options": PlanOptions` | 201, `{"plan", "inventory"}` |
| `GET /api/theatarr/v1/migrate/plans/{id}` | | `{"plan", "inventory"}` |
| `POST /api/theatarr/v1/migrate/plans/{id}/apply` | `{"steps": [...], "confirm": [...], "keys": {...}}` | the Plan; 400 for an unknown or manual step, 500 when the checkpoint fails ("nothing was applied") |
| `POST /api/theatarr/v1/migrate/plans/{id}/verify` | | the VerifyReport |

`keys` are used for the one request and never stored, logged or answered; error texts mask them. A request is
capped at 32 MiB. Without the settings database the routes answer 503.

### Agent recipe

1. Ask the user for the services' addresses and the names of the environment variables holding their keys. Never
   ask for, print or store a key value.
2. `theatarr migrate inspect ... -json`; read `arr[].error`, `prowlarr.error`, `seerr.error`: fix access first.
3. `theatarr migrate plan ... -json`. Read `gaps` to the user and settle each `paths` and `secret` gap with
   `-path-map` and `-provider` before going on (planning again makes a new plan; the old one stays).
4. Show the user the steps (`what`, `why`, `reversible`) and get their go-ahead, then `theatarr migrate apply -plan ID
   -json`. For each step in `needs_confirmation`, show `confirmation.effects` and once the user agrees, apply it with MCP's `migration_apply` and the
   step's `confirmation_token` (or the user applies it in the UI, Admin › Advanced › Sonarr/Radarr migration › Migrate). A `conflict` means Theatarr already has a different record with that key: leave it to
   the user.
5. `theatarr migrate verify -plan ID -json`. Report `ready` per library, each check that is not PASS with its `fix`,
   and the `cutover` and `rollback` lists. Do not perform the cutover: it turns grabbing on and is the user's call.

### Notes

- A confirmation token is tied to the settings generation it was issued for. If anything changed settings since,
  apply stores the new confirmation and asks again.
- `verify` inside serve does not read serve's own `/metrics` for VPN egress; run `theatarr golive check -library K`
  for that line.
- Agents can use the same inspect/plan/apply/verify flow over [MCP](mcp.md), and confirm a step after the user
  agrees: `confirm`, `confirmation_token`, or the `confirmation_tokens` map by step ID (never with `confirm`) work
  for a session and a user's API key alike, each with its own tokens.
- Not supported: discovering services on the network.

The migration CLI sends its admin API key directly to the configured Theatarr service: it ignores environment
proxies, refuses redirects, and checks resolved addresses at connect time with the same LAN guard as the MCP bridge.
