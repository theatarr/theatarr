# Development

How to build and test Theatarr, how the code is laid out, and the rules for changing it. How to send a change is in
[CONTRIBUTING.md](../CONTRIBUTING.md); the architecture is in [design/overview.md](design/overview.md).

## Toolchain

- **Go** at the version in `go.mod` (1.27). SQLite is modernc (pure Go), so the binary builds with `CGO_ENABLED=0`;
  only the race detector needs cgo.
- **Node 20** for the web UI in `web/` (`npm ci` installs it from `web/package-lock.json`). `make check`, `make build`
  and `go test` do not need Node.
- **Docker** for `make image`, which builds the UI in a Node stage and the binary in a Go stage.

## Make targets

| Target | What |
|---|---|
| `make check` | `go vet`, the Go tests, the mount-helper tests of `internal/library`, and gofmt |
| `make lint` | staticcheck |
| `make ui-check` | The web UI: API types generated from `internal/web/openapi.json`, typecheck, lint and unit tests |
| `make e2e` | The end-to-end pipeline tests (`internal/e2e`) |
| `make race` | The Go tests under the race detector (needs cgo) |
| `make ui` | Builds the UI into `internal/webui/dist`, which `make build` embeds |
| `make build` | `bin/theatarr`. Without `make ui` first, the binary serves a "UI not built" page |
| `make image` | A local Docker image, `theatarr:<git describe>`; never pushed |
| `make test`, `make vet`, `make fmt` | The single steps |

Before a pull request, run `make check`, `make lint` and `make ui-check`; run `make e2e` and `make race` too when you
touch acquisition, import or concurrency code. `.github/workflows/ci.yml` runs the same checks, the example policy's
validation and an image build for linux/amd64 and linux/arm64 (not pushed; the arm64 binary is started once under
emulation), on every push to `main`, on pull requests, and by hand from the Actions tab.

**Releases.** Pushing a tag `vX.Y.Z` (three numbers, no suffix) runs `.github/workflows/release.yml`: the checks of
`ci.yml`, then an image build for linux/amd64 and linux/arm64 pushed to the GitHub Container Registry as `ghcr.io/theatarr/theatarr:X.Y.Z`,
`:X.Y` and `:latest`, with the tag as the version the binary reports. Tags with a suffix (`v1.2.3-rc1`) and other
tags publish nothing. The registry package is public; its visibility and the repository's Actions permission
to write packages were set once on GitHub, by hand. Before tagging, refresh the bundled TRaSH defaults with
`scripts/update-trash-bundle.sh <checkout>` and update the commit in `NOTICE`. The install docs name the newest version
(`README.md`, `docs/quickstart.md`, `examples/compose.image.yaml`): bump that pin to `X.Y.Z` in the commit that
gets tagged. [`examples/compose.image.yaml`](../examples/compose.image.yaml)
is the compose file for a released image.

`theatarr policy validate examples/policy` checks the example policy. `go run ./cmd/theatarr help` lists the
subcommands; each one takes `-h`.

## Layout

| Path | Holds |
|---|---|
| `cmd/theatarr` | The one binary. Each subcommand is its own file and registers itself in `init` (see `main.go`), so new commands never edit `main.go` |
| `internal/buildinfo` | Version string |
| `internal/netregex` | regexp2 with .NET semantics and a match timeout. **Every** arr or TRaSH pattern compiles through it |
| `internal/store` | SQLite: one writer connection plus a reader pool, goose migrations |
| `internal/jobs` | Persistent job queue in SQLite, with leases and retries; urgent kinds and reserved slots, backlog per kind ([design/readiness.md](design/readiness.md)) |
| `internal/health` | Health checks after the arrs' HealthCheckService: mount, metadata, job backlog, failed jobs, backups, providers, indexers, VPN egress, disk; cached ([design/readiness.md](design/readiness.md)) |
| `internal/preflight` | `theatarr preflight` and Setup's readiness check: what a fresh install still needs, and what is set up but broken |
| `internal/policy` | YAML policy loader and validation |
| `internal/settings` | The settings database: validated sections, credentials, generations, audit, and runtime generation swaps ([design/settings.md](design/settings.md)) |
| `internal/secrets` | Shared rules for showing and restoring credentials (masking, webhook URLs) |
| `internal/migration` | The arr profile and naming converter (`freeze.go`); kept for phase D's `from_arr` decision |
| `internal/trashbundle` | The TRaSH-Guides snapshot the binary embeds for the profile presets (one pinned commit, MIT). `scripts/update-trash-bundle.sh <checkout>` refreshes it before a release; update the commit in `NOTICE` with it |
| `internal/recyclarr` | Recyclarr configurations and arr custom formats into Theatarr formats and profiles |
| `internal/server` | HTTP server, health, metrics, logging setup |
| `internal/decide/parser/radarr` | Port of Radarr's parser: movie titles, quality, language, release group |
| `internal/decide/parser/sonarr` | Port of Sonarr's parser: episode titles, quality, language, release group |
| `internal/decide/format` | Custom formats: specifications, arr matching rules, scores, TRaSH and arr JSON import |
| `internal/decide`, `internal/decide/{adapt,profile}` | Decision engine, adapters, profiles and specs ([design/decide.md](design/decide.md)) |
| `internal/availability` | Movie acquisition gated on the stored release dates |
| `internal/ceiling` | Local source evidence and display-only quality ceilings; never searches or grabs |
| `internal/arr` | Read-only client for the arr v3 API |
| `internal/gateway` | The one outbound HTTP path: allowlist, budgets, back-offs and the VPN egress guard ([design/gateway.md](design/gateway.md)) |
| `internal/indexer` | Search adapters (Torznab, DMM, Torrentio, Zilean, SeaDex), planner, single-flight |
| `internal/indexer/cardigann` | Native Cardigann engine (Mediarium port), definition catalog and sync source, Prowlarr migration reader ([design/cardigann.md](design/cardigann.md)) |
| `internal/provider` | Debrid clients, **read-only** (Real-Debrid, AllDebrid, TorBox), registry sync |
| `internal/provider/adder` | Debrid **write** capability: add a magnet, select files. The only deletes are in `delete.go`: Theatarr's own just-made uncached add (on AllDebrid only with `router.alldebrid_delete_uncached`, default off), Theatarr's own stalled Real-Debrid add, each behind a proof, and torrents a person chose and confirmed after a backup (`DeleteUserChosen`). Only `internal/router` and `internal/torrents` may import it: `cmd/theatarr` builds adders with `router.BuildAdders`, `TestOnlyRouterImportsAdder` enforces it, and `TestOnlyTorrentsDeletesChosen` keeps the user-chosen delete and `adder.Restore` in `internal/torrents` |
| `internal/qbit` | The qBittorrent Web API subset a library's grabs go through when it adds via an external client ([design/external-clients.md](design/external-clients.md)) |
| `internal/torrents` | The torrent manager ([design/torrents.md](design/torrents.md)): what uses each provider torrent, reports, backups (DMM export and import), paced restore through `adder.Restore`, and deleting chosen torrents after a fresh check and a full backup; journaled in `torrent_journal` |
| `internal/router` | Download router: fallback, refusals, pacing, poller; gated by `mode: active` and `router.enabled` ([design/router.md](design/router.md)) |
| `internal/acquire` | Searches for active libraries: arr-view commands and the wanted loop, into the router |
| `internal/pipeline` | Shared acquisition and series-refresh wiring for the service and the end-to-end tests |
| `internal/e2e` | Active pipeline tests over local HTTP fixtures and temporary mount and library roots |
| `internal/router/routertest` | Test helper: a real router over logging fake adders |
| `internal/acquire/rss` | The RSS sync for Live libraries ([design/rss.md](design/rss.md)) |
| `internal/shadow` | The periodic arr import (`arr.import`) |
| `internal/catalog` | The one add path for titles and libraries: find or create the title, turn the chosen libraries on, season monitoring |
| `internal/importarr`, `internal/metadata/tmdb` | Read-only arr import, TMDb client |
| `internal/metadata/art` | `meta.art` job (TMDb details into `title_meta`) and the `/img` proxy with its disk cache ([design/artwork.md](design/artwork.md)) |
| `internal/metadata/tvrefresh` | `meta.series` jobs: TVDB (TMDb fallback) episodes, series metadata, monitoring intent, held searches ([design/tv-metadata.md](design/tv-metadata.md)) |
| `internal/metadata/anime` | Anime metadata pipeline: crawl, joins, mappings, arbitration, orders, overrides, review queue ([design/anime.md](design/anime.md)) |
| `internal/auth`, `internal/web` | Login, users, roles and permissions, API keys, the native API, requests, MCP ([design/users.md](design/users.md), [design/web.md](design/web.md)) |
| `internal/plexauth`, `internal/plexauth/plexauthtest` | Plex sign-in: plex.tv PINs and the server-access check through the gateway; a fake plex.tv for tests |
| `internal/webui`, `web/` | The SPA (React, Vite, Tailwind; [design/ui.md](design/ui.md)). `web/` is the source; `make ui` builds it into `internal/webui/dist`, which is embedded (only `dist/.keep` is committed) |
| `internal/uipages` | UI destinations named in Go readiness and recovery messages; kept in step with `web/src/components/shell/nav.ts` and `web/src/lib/settings-map.ts` |
| `internal/logring` | The last 2,000 log records in memory, secrets redacted, for `GET /logs` |
| `internal/errinbox` | The error inbox ([design/errors.md](design/errors.md)): unexpected failures, deduplicated, for the API, `theatarr errors` and MCP |
| `internal/activity` | The shared reader behind the arr views' queue and the native activity, calendar and Continue row |
| `internal/arrview`, `internal/arrview/nativeapi` | Radarr- and Sonarr-shaped views for Seerr, Homepage, decypharr, autobrr and the phone apps; request types shared with `web`. `ops.go`: the web UI's grab, retry and unlink ([design/arrview.md](design/arrview.md)) |
| `internal/journal`, `internal/journal/snapshot` | The grab journal: entries over the decision, router, library and arr tables, suspect checks, verdicts, web UI actions ([journal.md](journal.md)) |
| `internal/notify` | Outbound webhooks (arr-shaped per view, native `theatarr.*`): outbox, sender, retries; through the gateway |
| `internal/library`, `internal/library/{naming,probe}`, `internal/plex` | Arr naming, gated import (probe, sample guard, symlinks), relink and verify, Plex scans ([design/library.md](design/library.md)) |
| `internal/rootprobe` | Bounded filesystem calls on library roots, so a hung network filesystem cannot block the service |
| `internal/retention` | The nightly `db.retention` job: bounds the tables that grow with every run, poll and delivery |
| `internal/nowplaying` | The now-playing tracker: Plex sessions, file-to-link mapping, health verdict, rclone rc client, next-episode pre-warm ([design/now-playing.md](design/now-playing.md)) |
| `internal/demo` | `theatarr demo seed`: an invented dataset for trying the UI ([demo.md](demo.md)) |
| `examples/` | The example compose file, an example policy directory and the demo overlay |
| `scripts/` | A demo server and a restore drill on an internal-only Docker network ([demo.md](demo.md), [restore.md](restore.md#restore-drill)) |
| `docs/` | User guides; design and reference docs in `docs/design/` |

Code notes:

- SQL `IN` lists use `store.Placeholders(n)`; callers handle empty lists before querying.
- The native settings API stays in package `web`: `settingsapi.go` owns routing, import and reload,
  `settings_records.go` reads and projects records, `settings_write.go` handles CRUD and shared save and errors, and
  `settings_decode.go` strictly decodes candidates.
- Webhook URL masking and restoration share `internal/secrets.URLMasker`: settings keep only scheme and host, while
  arr notifications mask credential-like parts and restore them only for the same target.
- Helpers used only by their own package's tests live in `_test.go` files. `auth.SetFastHashing` and
  `parsertest.MustLoad` are deliberate test APIs used by other packages' tests.
- `internal/web/openapi.json` is maintained by hand. Change it, then run `cd web && npm run gen:api`.

## Porting arr code

- **Port faithfully.** Same regex text, same order of attempts, same options, same fallbacks. A "better" pattern is a
  deviation.
- **Head every ported file** with its source, e.g. `// Port of Radarr src/NzbDrone.Core/Parser/QualityParser.cs @ a96bf7c.`
  The commits the ports follow are in [NOTICE](../NOTICE): Radarr `a96bf7c` (v6.4.4.10685) and Sonarr `cab419a`
  (v4.0.20.3014).
- **Keep the arr names** for enums, ids and JSON fields, so the output marshals like the arr's `GET /api/v3/parse` and
  can be compared field by field. `theatarr sonarr-parse` prints the Sonarr port's parse of a release title.
- **List every deviation** in the package doc comment, with the reason. Known differences between .NET and regexp2
  belong there too.
- **Licence.** The arrs are GPL-3.0; the Cardigann engine is ported from Mediarium, which is AGPL-3.0. Theatarr is
  AGPL-3.0. `NOTICE` lists every project code was ported or adapted from; a new port adds its line there.

## Migrations

Numbered `internal/store/migrations/000NN_<name>.sql`, applied in order by goose. The newest is **00110**
(`anime_discovery`); a new migration takes the next number above the newest.

**Every unused number below the newest migration is retired.** Never fill a gap: goose refuses out-of-order lower
migrations on databases already past them. Retired numbers are 00004, 00005, 00008, 00040, 00042–00050, 00071, 00085,
00088, 00090, 00091, 00094, 00098 and 00099. Never change the SQL statements of a migration once it is on `main`
(goose keeps no checksums, so an edit would make fresh installs differ from existing ones); comments may change.

Opening the store and migrating refuse a database whose applied version is newer than the newest embedded migration,
before anything can start workers or write. The error names both versions and points to [restore.md](restore.md).
Backup restore checks the manifest's schema and the staged database through the same guard. The read-only
`backup fingerprint` diagnostic works without a writer or migrations; there is no option to bypass the guard.

A test that runs migrations up and down pins its version with `UpTo`/`DownTo`, since later migrations will exist.

## Tests

- Table tests. Arr test cases extracted from `ParserTests` live in `testdata/` as JSON (fixture, method, inputs,
  expected fields).
- A known failure is listed in the testdata with a reason, so the pass rate is honest and the suite stays green.
- Name test files and functions after the behaviour they cover.
- `go test` never touches the network or a live service. Checks against live services are `theatarr` commands.
- Tests that read a local corpus of real release data are opt-in: they skip unless `THEATARR_CORPUS` points at it.
  Test-only variables use the `THEATARR_` prefix too.
- Fixtures use invented data: compose service names for URLs (`http://radarr:7878`), documentation addresses
  (`192.0.2.x`), `/mnt/debrid` and `/media/...` paths, and obviously fake keys. Never commit data copied from a real
  install.
- Settings save and error tests can render `RecordEditor` directly and paste complete values, to avoid repeated page
  renders and per-character input; sheet navigation and control names have separate coverage.

### End-to-end tests

`make e2e` runs `internal/e2e`: active movie, multi-library reuse, series, absolute-numbered anime, upgrade, provider
failure, mount waiting and write-gate scenarios over the real services, local HTTP fixtures and temporary SQLite,
mount and library directories. Jobs and injected clocks advance the pipeline without background timers; the
acquirer and metadata wiring use the same constructor as `theatarr serve` (`internal/pipeline`). A `BUG:` skip marks a
known blocker in a protected package.

## Demo server and restore drill

`scripts/devserver.sh up|reseed|down [IMAGE]` runs an image (`make image`; default the tag it gives this checkout,
`theatarr:<git describe>`) on the demo dataset ([demo.md](demo.md)) on an internal-only Docker network, so nothing
it tries leaves the machine. Other containers reach it by joining that network; sign in as `admin` with the demo
password. `reseed` wipes its data volume and seeds again anchored at now; `down` keeps the volume. With
`TRASH=<TRaSH-Guides checkout>` the seeder imports the format specifications from the checkout; without it, it uses
bundled example specifications. The credentials in the demo are invented.

`scripts/restore-drill.sh [IMAGE]` backs up the running demo server, restores the set into a fresh volume, starts the
copy beside it on the same network and checks it is ready with the same titles, libraries, links and users
([restore.md](restore.md#restore-drill)). It leaves the demo server running and unchanged apart from a backup set.

## Safety rules for development

- **Live services are read-only during development.** Point commands at test instances or fixtures. The arr
  importers send `GET` requests only.
- **Secrets** come from the environment or the settings database. Never print, log or commit them; errors and logs
  mask API keys.
- **Data from live services stays out of git.** Small hand-picked samples may go in `testdata/` when they contain no
  URLs, keys, passkeys or GUIDs.
