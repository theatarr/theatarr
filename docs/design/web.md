# Web: auth, native API and the UI shell

Authentication, the native API's base routes, setup and logs. The UI itself: [ui.md](ui.md); accounts and
permissions: [users.md](users.md). Code: `internal/auth`, `internal/web`, migration `00007_web.sql`, `cmd/theatarr/admin.go`.

## Auth model

Auth is on by default and has no switch to turn it off. Public routes include `/healthz`, `/readyz`,
`/metrics` (see below), the public SPA shell and assets, and JSON login. Protected API handlers require credentials.

**First start.** `theatarr serve` calls `auth.New`. When the database has no signing secret, it creates one (32 random
bytes in `web_setting`). When it has no user, it creates `admin` with a random 24-character password, stores only its
argon2id hash, and writes the password to **`<data dir>/initial-admin-password`** (the database's directory, mode
0600), in the same transaction: if the file cannot be written, no account is made. The password never goes to the
log (logs reach `docker logs` and `GET /logs`); the log says only where the file is:

```
{"level":"WARN","msg":"first start: created the admin account; its password is in the file (mode 0600), which is deleted at the first login; or set one with 'theatarr admin reset-password'","component":"auth","username":"admin","file":"/data/initial-admin-password"}
```

The file is deleted at the first successful login (any account) and by `theatarr admin reset-password` for `admin`. In a
container: `docker exec theatarr cat /data/initial-admin-password` right after the first start. Without a data directory
(`auth.Options.DataDir` empty, tests only) the password is written nowhere; use the reset. Test:
`TestFirstStartPasswordFileNotLog` (no log record holds the password), `TestResetPasswordRemovesInitialPasswordFile`.

**Reset.** `theatarr admin reset-password [--db /data/theatarr.db] [--username admin] [--password-stdin]` generates and prints a
new password (or reads one from stdin, at least 12 characters), creates the account if missing, and ends all its
sessions. For `admin` it also deletes `initial-admin-password` next to the database. It works on the database alone, so it runs with the service stopped, or in the container with
`docker exec -it theatarr theatarr admin reset-password`.

**Passwords.** argon2id (RFC 9106: 64 MiB, 3 passes, 4 lanes), PHC string in `web_user.password_hash`. An unknown
username still costs one hash, so timing does not tell which usernames exist.

**Sessions** (browser). `theatarr_session` cookie: 256-bit random token, `HttpOnly`, `SameSite=Lax`, `Path=/`, and `Secure`
when the request came over TLS or with `X-Forwarded-Proto: https` (`--cookie-secure auto|always|never`, env
`THEATARR_COOKIE_SECURE`). The database keeps the token's SHA-256 only. A session ends after 7 days idle or 30 days after
login, at logout, or on a password reset.

**API keys** (per client). `tha_` plus 256 random bits, shown once at creation, stored as SHA-256 with a 10-character
prefix for listing, revocable (the row stays with `revoked_at`). Sent as the `X-Api-Key` header only: the `apikey`
query parameter is refused on the native API and accepted only on the arr-compatible views. Keys are managed in
Settings or through `/api/theatarr/v1/keys`, with the caller's own-keys permission; keys for other accounts need the
users permission. `last_used_at`
is written at most once a minute per key.

**CSRF.** A session request that changes state (not GET/HEAD/OPTIONS) needs a token in the `csrf` form field or the
`X-CSRF-Token` header: HMAC-SHA256(secret, session hash), so it needs no storage and dies with the session. A foreign `Origin` is refused.
The JSON login takes `application/json` only (a cross-site form cannot send that without a preflight) and returns the new
session's CSRF token. API-key requests carry no ambient credential and need no token.

**Throttling.** Failed logins are counted per client address and per username separately. Five failures in 15
minutes are free; each further one locks the key for 2 s, doubling up to 15 minutes. A locked key gets 429 with
`Retry-After` before the password is checked; a success clears both keys. Each attempt is counted before its password
is checked, so a burst sent at once is counted as it arrives instead of all passing the check together. At most 2
password checks run at once (each argon2id check holds 64 MiB, so 128 MiB at most); a login beyond that gets the same
429 with `Retry-After: 1` without being counted. Unknown usernames take the same steps. A password change waits for
a slot instead. In memory: a restart forgets it. `X-Forwarded-For` is not trusted.

**Metrics.** `--metrics open|lan|key` (env `THEATARR_METRICS`), default `open` (Prometheus scrapes without a
key). `lan` serves loopback, RFC 1918, ULA and link-local peers only; `key` wants an API key like the API.

**Headers.** Every page and API response: `Content-Security-Policy: default-src 'self'` (and `frame-ancestors 'none'`,
`form-action 'self'`), `X-Frame-Options: DENY`, `nosniff`, `Referrer-Policy: same-origin`, `Cache-Control: no-store`.

## Schema (00007)

| Table | Holds |
|---|---|
| `web_setting` | `signing_secret` (hex) |
| `web_user` | username, argon2id hash, timestamps |
| `web_session` | SHA-256 of the token, user, created, last seen, absolute expiry, remote address, user agent |
| `api_key` | name (unique among active keys), prefix, SHA-256, creator, created, last used, revoked |

The database file holds the signing secret, so it should stay mode 0600 (it already holds everything else).

## API surface (`/api/theatarr/v1`)

Hand-written OpenAPI 3.1 at `GET /api/theatarr/v1/openapi.json` (`internal/web/openapi.json`); a test checks that it and
the router list the same operations. JSON errors are `{"error": "..."}`. The base routes:

| Route | Returns |
|---|---|
| `GET /status` | version, schema version, row counts |
| `GET /titles` | titles with version coverage. Filters: `kind`, `q` (name contains), `with=<key>` (has a file for that version), `without=<key>` (no file: no such version or it is wanted; `without=movie-web` is "no WEB version"), `state`, `limit` (≤500), `offset` |
| `GET /titles/{id}` | title, external ids, release dates, versions (each with arr files, current file marked, links, arr records), registry entries |
| `GET /versions`, `/versions/{id}` | versions; filters `key`, `state`, `has_file` |
| `GET /registry` | contents, newest first; `q` = hash prefix or name, `state` |
| `GET /registry/{hash}` | a content: versions and titles, arr files, library links, providers, files, arr history for the hash |
| `GET /imports`, `/imports/{id}` | import runs with their JSON summary |
| `GET /jobs` | counts by state and jobs (running, queued by run time, latest finished); `state`, `kind`, `limit`. Payloads are left out |
| `GET /gateway` | egress paths (VPN ok, exit IP, age, stale), budgets (rate, caps, this hour's and today's use, users, back-off), all back-offs |
| `GET /policy`, `/policy/validate` | the running policy summarised (env var names, never values); a fresh validation of the policy directory when one is configured, which never reloads |
| `GET /events` | Server-Sent Events: `jobs` and `gateway` (below) |
| `POST /auth/login`, `POST /auth/logout`, `GET /auth/me` | session login (JSON), logout, who am I (with the CSRF token for a session) |
| `GET/POST /keys`, `DELETE /keys/{id}` | API key list, create (key in the response only), revoke. Open to API keys with the owner's permissions |

**Events.** `jobs` carries `{counts, changed: [Job]}`, `gateway` a full gateway state. Each is sent once on connect
as a snapshot, then when it changes. Neither `jobs` nor `gateway` publishes changes, so a poller (every 2 s, only
while a client listens) compares job `updated_at` and counts, and the gateway state without the check ages. A
`: ping` every 25 s. At each ping the stream re-authenticates its request; a revoked key or session ends it, and
changed permissions apply from then on. Every write has a 30 s deadline, so a stuck client cannot hold a handler.
Streams end on shutdown.

The stream and Jobs page share a count-only reader, including the same unresolved-failure attention count. Event
polls do not fetch or sort a job-list row. The gateway comparison caches its previous snapshot without check ages;
events still contain the full current snapshot, including ages.

**Budgets, back-offs and egress** come from `Gateway.State()`, the gateway's in-memory snapshot (its source of truth
while running); the web layer does not read the 00002 tables. Egress is `Gateway.EgressSummary()`, as in `/readyz`.
Without a gateway (tests only) the budgets and back-offs are empty.

## Screens

React is the only browser UI. Browser login uses the JSON auth API with the existing session and CSRF protections. See
[the UI navigation](ui.md#navigation) for screens and routes. A binary built without the SPA shows
build instructions and the API link. Unknown API paths return JSON 404.
Other application paths retain SPA fallback.

## Wiring

- `internal/server/server.go`: `Config.MetricsGuard func(http.Handler) http.Handler` wraps `/metrics` when set.
- `cmd/theatarr/serve.go`: flags `--metrics` and `--cookie-secure`; `auth.New` and `web.New` after the gateway; `ui.Run` in
  the wait group; `ui.Mount(srv.Mux())`.
- `go.mod`: `golang.org/x/crypto` (argon2).

Routes register directly on the server mux, so the request log and `theatarr_http_requests_total` get real route patterns.
Routes served by the runtime mux (`serveruntimehttp.go`) report their pattern through `server.SetRoute`, so they are
labelled the same way instead of by the catch-all pattern.

## Setup and preflight

Code: `internal/web/setup.go`, `internal/preflight`, `cmd/theatarr/preflight.go`,
`web/src/pages/admin/setup.tsx`. User guide: [docs/quickstart.md](../quickstart.md).

- `GET /setup` (admin): `first_run` (no version yet), `settings_source`, `admin_password_generated` (the caller is
  `admin` and still has the first-start password), one `missing` line per feature that is off (no versions, indexers,
  providers, TMDb source or debrid mount), counts of versions, versions with a root, providers, indexers,
  profiles and active API keys, and `tmdb`: the `tmdb` source has its address and a key that resolves, stored in
  settings or in the environment variable it names (the predicate search and add use). `arr_instances` counts the
  `import.arr` instances: 0 is an install no arr feeds. `ready_providers` and `ready_indexers` name the sources the
  two counts count, and `trash` says the trash folder is set.
- `POST /setup/start` (admin, CSRF; `internal/web/settingsapi_start.go`): the one-step start. Body: `generation`,
  `key`, `roots` and `config` as `POST /settings/versions` takes them, an optional `preset` (`flavour`, `preset`,
  `modifiers`) and `confirmation_token`. One settings save creates the version with `mode: active` and `grab: true`
  whatever the body says, creates the preset's profile and format set under the library's key (from the checkout
  or the bundled TRaSH defaults) and sets the global switches `router` and `library` on; `notify` and every other
  record are untouched. It is one generation and one confirmation: the 422 lists the activation, the grab switch and
  each global switch with one token. It answers 409 while `import.arr` holds an instance (also when the settings
  are not in the database), and the usual stale-generation 409 and validation 422 (an unwritable root is named; a missing trash folder is
  `library.trash.dir: required`). It also answers 422 when none of the library's indexers, or none of its
  providers, is enabled and ready.
  The answer is `generation`, `revision` and `forced_off`: the switches among `router` and `library` that
  `THEATARR_FORCE_OFF` keeps off although they are now saved as on.
- `POST /preflight {sources?}` (admin, CSRF): `preflight.Run` over the current generation's policy. Each item is
  `ok`, `warn` or `fail` with `why` and `next`. The mount check is the library's guarded mount check; roots and trash
  are stat and access(2), plus one filesystem when a version imports with hardlinks; providers and indexers are the
  settings tests (bounded and audited, through the gateway and its VPN check), with each indexer's query types; the
  arr views are asked on the service's own handler without a key (200 or 401 is ok); Plex is asked for its identity;
  egress and indexer back-offs come from the health checks as they stand. `{"sources": false}` skips every outbound
  call. Nothing is written: no settings, no budgets beyond what the settings tests already count.
- `theatarr preflight [-json] [-sources=false]` runs the same checks offline. A missing database or an empty policy
  directory is a fresh install, not an error; the database is never migrated. The gateway has no store, so the live
  budget counters are untouched. It exits 1 when a check fails.
- Health: `SetupCheck` (first in the list) is OK when versions, indexers, providers, TMDb and the mount are set, else a
  notice naming what is missing. It is a notice, so it does not join Needs attention.
- UI: Admin › Setup (first admin entry while unfinished). `/admin` leads an admin there once per browser session while the
  install has no version and settings come from the database. Steps: the admin password (`POST /me/password`),
  providers and indexers (links to their editors under Settings, `/admin/settings/downloads` and
  `/admin/settings/indexers`, with Prowlarr feeds and the native catalog side by side; status is beside the editors),
  the TMDb key (one field while `tmdb` is false: it adds the sources `tmdb` and `tmdb-images` with TMDb's public
  addresses where they have none and stores the key, in one `PUT /settings/sections/metadata` with its confirmation;
  `web/src/components/settings/tmdb-key.tsx`), a
  first version (key, kind, root with the validate-root check, a TRaSH preset through `POST /settings/presets/create`
  or an existing profile, indexers, providers; Review, then `POST /settings/versions` with `mode: shadow`, `grab: false`,
  answering a 422 confirmation with the token; a retry after a failed version save reuses the profile it already made
  only for the same key and preset, and asks for another key when the preset changed), and the preflight table.
  With `arr_instances` 0 the form has two buttons: **Create library and start downloading** sends `POST /setup/start`
  (the preset in the same request, no separate profile save) and shows the 422's effects in the settings
  confirmation dialog before sending the token; it is disabled, with the reason, unless one of the picked providers
  and one of the picked indexers is ready and the mount and the trash folder are set. **Create in shadow (decide later)** is the flow above with the same request. A
  non-empty `forced_off` is shown after the library is created. With `arr_instances` above 0 the form is the shadow
  flow only.
  `GET /setup` also carries `last_preflight`, the summary of the latest `POST /preflight` (time, settings generation,
  whether the source tests ran, counts), kept in memory until a restart. Readiness counts the latest run in the
  browser session, else that summary when it is a full run without failures on the current generation.

## Logs

Admin › Advanced › Logs polls `GET /logs` by sequence (`after`, matching count `seen`) and ring lifetime (`boot_id`), independently of
wall-clock time. Pages are oldest first; `more` drains another page at the returned cursor. The browser keeps the
latest 2,000 lines and clears old-process lines when the boot ID changes, even if the new ring is empty. If the
ring overwrites unread entries, `skipped` reports matching log lines lost under the same level/component filters,
using the previous page's `seen` count. A filter change starts a fresh cursor. The page shows a “log lines skipped”
marker and a separate restart notice when `boot_id` changes. `/logs/files` lists persistent log downloads for older lines.
