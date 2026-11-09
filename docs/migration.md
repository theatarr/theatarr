# Migrating from an arr stack

Coming from the arrs? See the [arr-to-Theatarr page map](arr-map.md) for routes and menu paths.

`theatarr migrate inspect | plan | apply | verify` moves the setup of an existing
Sonarr/Radarr fleet (with Prowlarr, Seerr and Recyclarr) into Theatarr's settings. The same operations are on the API
under `/api/theatarr/v1/migrate`. The migration never touches the old services, and it never turns grabbing on: everything it
adds lands disabled or in shadow, and the cutover per version stays a manual, confirmed step.

`theatarr migrate up | status` are the database migrations, a separate tool.

A first migration may create the install's first quality profile. Start `theatarr serve` once before applying, so
the database's quality settings are initialized; a database with no profiles yet is ready for that step.

## Workflow

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
4. **verify** answers, per version, whether it is ready to hand over: every automatic step done, the version's
   settings in place, `theatarr golive check` for the version, and an adopt and refresh dry run when the version
   follows another one. It prints the manual cutover and rollback steps.
5. **cutover** is manual, one library at a time ([golive.md](golive.md)): trial the active follower while its
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
when a version is not ready; `seerr-ids` exits 1 when a stored Seerr id names a different title.

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
run `seerr-ids` again and expect no wrong title ([arrview.md](design/arrview.md#seerrs-stored-ids) has what Seerr does
with each id).

## In the UI

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
according to their progress; their URL includes the plan ID, so reloading keeps the plan. Verify shows each version's
checks, fixes and pending work, with links to **Libraries** and **Sonarr/Radarr migration › Comparison**. Configure missing
credentials under **Settings › Download clients** or **Settings › Indexers**, compare in shadow first, and follow [golive.md](golive.md) for the later handover;
the wizard never enables the router or library switches.

The inspect API accepts optional `options` to return a `plan` preview with gaps and frozen quality profiles while
the raw arr bodies are still available on the server. With `save_plan: true` (requires `options`) it also saves that
plan for apply/resume. Without `options` its inventory response is unchanged; raw bodies and source key values are
never returned. When a step needs confirmation, apply returns the plan with that step in `needs_confirmation`, its effects and a
one-use token. The wizard retries one confirmed step at a time with `confirm` and `confirmation_token`; a token made
stale by another settings change is refused and the step can be reviewed again.

## What is migrated

| Step | Lands as | Notes |
|---|---|---|
| `budget/<key>` | a request budget | for Prowlarr's per-indexer limits |
| `indexer/<key>` | an indexer, **disabled** | a Prowlarr indexer as a Torznab feed through Prowlarr (`prowlarr-<slug>`), or native with `-native`; an existing feed with the same address is reused. A private tracker (Prowlarr's verdict) is added and listed in the versions like any other: privacy is a label. Arr history names indexers by the arr's name; Theatarr matches it to a configured indexer by name (`Policy.IndexerByName`); a name that does not normalize to the key (an arr-side name such as "DMM RD (proxy)") goes in the indexer's `arr_names` alias list |
| `provider/<key>` | a debrid provider, **disabled** | an existing provider of the kind is reused; a new one needs `-provider kind=KEYNAME` |
| `quality/<profile>` | a quality profile and its format set | from the arr's profile, custom formats and naming; Recyclarr's origin is recorded. Needs Theatarr's settings to manage quality (start `theatarr serve` once) |
| `version/<key>` | a version in **shadow, grab off** | library under the mapped arr root, which must exist and sit inside `library.dangling.roots`. No download client is needed: a debrid version adds to its providers directly (an existing `decypharr` client is named when there is one) |
| `view/<view>/<key>` | the version added to an arr view | so Seerr can be pointed at Theatarr at cutover |
| `import/<arr>`, `follow/<key>`, `seerr/<flavour>/<id>` | **manual** | the `import.arr` entry, the optional parallel follower ([parallel.md](parallel.md)), and each Seerr server's switch. Apply never runs them; Seerr is never written |

The `import/<arr>` step is done in the settings database, not the policy files (the database owns the `import`
section; a YAML edit is ignored once it is seeded): in Admin › Advanced › Sonarr/Radarr migration › Instances add
`arr.<name>` with its flavour, url, `api_key_env` and version, save, then enter the credential `arr.<name>.api_key`
(write-only; without it Theatarr reads the `api_key_env` variable). The section applies without a restart. Over the
API it is one `PUT /api/theatarr/v1/settings/sections/import` with `yaml` and `secrets: {"arr.<name>.api_key": ...}`.
`verify` passes the import check once the entry feeds the version.

Apply is idempotent: a step whose record is already there as planned is marked done without a write; one whose
record exists with other settings is a `conflict` and is left alone. Steps run in order and a step waits for the
steps it depends on. Statuses: `pending`, `done`, `failed`, `blocked`, `conflict`, `needs_confirmation`, `manual`.

Apply never sets a version active or grab on, never touches `router.enabled` or the other global switches, and
refuses a step whose payload would (a hand-edited plan).

### Gaps

Each gap has a topic: `secret` (a key the plan cannot name), `usenet` (Theatarr has no usenet path yet),
`local_library` (an arr that imports to local disk), `profile` (a profile Theatarr cannot build),
`paths` (no mapping, or the library is outside the library area), `prerequisite`, `unsupported` (delay and release
profiles, a Seerr server pointing at an arr that was not inspected, and the like), `unread` (a service inspect could
not read).

## JSON

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

## API

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

## Agent recipe

1. Ask the user for the services' addresses and the names of the environment variables holding their keys. Never
   ask for, print or store a key value.
2. `theatarr migrate inspect ... -json`; read `arr[].error`, `prowlarr.error`, `seerr.error`: fix access first.
3. `theatarr migrate plan ... -json`. Read `gaps` to the user and settle each `paths` and `secret` gap with
   `-path-map` and `-provider` before going on (planning again makes a new plan; the old one stays).
4. Show the user the steps (`what`, `why`, `reversible`) and get their go-ahead, then `theatarr migrate apply -plan ID
   -json`. For each step in `needs_confirmation`, show `confirmation.effects` and once the user agrees, apply it with MCP's `migration_apply` and the
   step's `confirmation_token` (or the user applies it in the UI, Admin › Advanced › Sonarr/Radarr migration › Migrate). A `conflict` means Theatarr already has a different record with that key: leave it to
   the user.
5. `theatarr migrate verify -plan ID -json`. Report `ready` per version, each check that is not PASS with its `fix`,
   and the `cutover` and `rollback` lists. Do not perform the cutover: it turns grabbing on and is the user's call.

## Rollback

- **One step**: each step's `undo` says how (delete the record in Settings, or turn it off). Advanced › Settings history
  shows the generation each step saved (`generation` on the step).
- **The whole migration**: apply takes a checkpoint before its first change: a full `theatarr backup` set (database,
  policy files, manifest) in serve's `-backup-dir`, plus the settings head exported (credentials stripped) beside it
  as `<set>.settings.yaml`. Stop Theatarr and run `theatarr backup restore -from <set> -db ... -policy-out ...`
  ([restore.md](restore.md)). Each apply run with open steps adds a checkpoint; the newest is in
  `verify`'s `rollback`.
- **A version after cutover**: `verify` lists the steps (grab off, back to shadow, Seerr back to the arr, the arr's
  RSS on again).

Apply refuses to start when backups are off (`-backup-dir off`) or the checkpoint fails, and then nothing is
written. A step that fails leaves the checkpoint and the steps before it in place; fix the cause and run apply again.

## Notes

- A confirmation token is tied to the settings generation it was issued for. If anything changed settings since,
  apply stores the new confirmation and asks again.
- `verify` inside serve does not read serve's own `/metrics` for VPN egress; run `theatarr golive check -version K`
  for that line.
- Agents can use the same inspect/plan/apply/verify flow over [MCP](mcp.md), and confirm a step after the user
  agrees: `confirm`, `confirmation_token`, or the `confirmation_tokens` map by step ID (never with `confirm`) work
  for a session and a user's API key alike, each with its own tokens.
- Not supported: discovering services on the network.

The migration CLI sends its admin API key directly to the configured Theatarr service: it ignores environment
proxies, refuses redirects, and checks resolved addresses at connect time with the same LAN guard as the MCP bridge.
