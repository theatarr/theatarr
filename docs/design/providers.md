# Debrid providers, read side (`internal/provider`, `theatarr providers`)

The provider clients read Real-Debrid, AllDebrid and TorBox accounts and keep the content registry in step with what
each account holds. Writes (add, select, delete) live in a separate package, `internal/provider/adder`, driven by
the router ([router.md](router.md)). Where this sits: [overview.md](overview.md).

## What it does

- **Clients** for Real-Debrid, AllDebrid and TorBox behind one `provider.Provider` interface:
  - `Account`: plan, premium, expiry, points, active slots, cooldown. `theatarr serve`'s health check
    `ProviderAccountCheck` reads it every 6 h per enabled account: a warning 7 days before premium ends or when a
    provider does not answer, an error when the account is not premium or the key is refused
    ([readiness.md](readiness.md) "Health checks");
  - `Torrents`: the whole list, paged;
  - `Torrent(id)`: one torrent with its files, and its original name (`OriginalName`, the name the torrent declared,
    which zurg names the folder after): Real-Debrid's `original_filename`; for TorBox and AllDebrid, read from the
    files as zurg does (the folder every path shares, else the single file's name, else the torrent's name; AllDebrid
    lists files only for a ready magnet, so earlier reads leave it empty). List answers leave it empty; TorBox's
    single read that falls back to the list on a 5xx fills it from the listed item's files (`TestOriginalNameFromFiles`,
    `TestTorBoxGoneIdOn5xx`);
  - `Cache` and `CheckCached`: the instant-availability check, where one still exists.
- **Read-only by construction.** There is no add, select, delete, restart or unrestrict method anywhere.
  - The clients reach a provider only through an unexported `getter`, which builds `GET` requests and nothing else.
  - Tests fail if the package names any other HTTP method, or if the interface or a client gains a write-like
    method.
  - Writes are a separate package (`internal/provider/adder`).
- **Every request goes through the gateway** as `Target{Class: provider, Key: <policy key>}`. That brings the host
  allowlist, the provider's budget, failure back-off up to `pacing.backoff_max`, 429 and Retry-After handling,
  and the VPN guard when `egress.require_vpn_for` lists `providers`.
- **Secrets and links:**
  - The key goes in the `Authorization: Bearer` header (all three accept it), never in a URL.
  - Download links in answers are decoded only as a count, so they never reach a struct that is logged or stored.
  - Account e-mails and usernames are not kept.
- **Registry sync** (`provider.Syncer`; job kind `provider.sync`): see below.
- **Commands:**
  - `theatarr providers sync [-provider rd|ad|tb|<key>]... [-dry-run] [-json] [-v]`
  - `theatarr providers status [-account] [-json] [-v]`
  - Both take `-db` and `-env-file` (settings come from the database).
  - `status` reads only the database. `-account` adds one live `GET` per account (two on RD).

## Per-provider API notes

Sources: each provider's public API docs, and decypharr's clients
(`ref/sirrobot01_decypharr` @ 917cfa2, MIT; the response types and status mapping are ported with the notice in
`internal/provider/LICENSE.decypharr`). Paging, date and error behaviour were also checked against zurg's handling.
The fixtures in `internal/provider/testdata` are hand-written from these sources: no keys, no real links, fake
hashes.

### Real-Debrid (`https://api.real-debrid.com/rest/1.0`)

| Call | Use |
|---|---|
| `GET /user` | `type`, `premium` (seconds left), `expiration`, `points` |
| `GET /torrents/activeCount` | `nb` and `limit`: active torrents and the slot limit |
| `GET /torrents?limit=5000&offset=N` | The list, newest first. `X-Total-Count` header; 204 for an empty page |
| `GET /torrents/info/{id}` | One torrent, with `files[]` (`id`, `path`, `bytes`, `selected`) |

- **Page size is 5000, RD's maximum.** Above that, RD silently returns its default page of 100.
- **The list shifts while it is paged:**
  - an add between two pages repeats an entry, which is deduped by id;
  - a delete at a page boundary drops one.
  - So a list with fewer distinct torrents than `X-Total-Count` is read again, up to 3 times, and then fails with
    `ErrIncomplete`. A partial list is never applied, because it would mark contents absent.
  - RD keeps counting a deleted torrent for a few minutes, so a sync just after a delete can fail this way. The job
    retries.
- **Dates end in `Z` but are Paris wall-clock time.** They are read as `Europe/Paris` (tzdata is embedded) and
  stored as UTC.
- **One hash can be listed several times:** every `addMagnet` creates a new torrent, even for the same hash. The
  sync keeps one torrent per hash and counts the others (`duplicates`).
- **Errors** are `{"error", "error_code"}`:
  - 8, 9, 14 and 22 (bad token, permission denied, account locked, IP not allowed) and HTTP 401/403 give `ErrAuth`;
  - 7 (`unknown_ressource`) and HTTP 404 give `ErrNotFound`;
  - 34 (too many requests) comes as HTTP 429, and the gateway backs off;
  - 35 (`infringing_file`, HTTP 451) only happens on adds.
- **Statuses:**

  | Normalised | RD status |
  |---|---|
  | queued | `magnet_conversion`, `waiting_files_selection`, `queued` |
  | downloading | `downloading`, `compressing`, `uploading` |
  | downloaded | `downloaded` |
  | error | `magnet_error`, `error`, `virus`, `dead` |
  | unknown | anything else |
- **Rate limit:** 250 requests per minute per account, shared with decypharr and zurg. The example budget `rd-api` is
  100/min. A sync of an account under 5000 torrents costs one list request.

### AllDebrid (`https://api.alldebrid.com/v4`, used as v4.1)

| Call | Use |
|---|---|
| `GET /v4.1/user` | `isPremium`, `isTrial`, `premiumUntil` (unix), `fidelityPoints`, `limitedHostersQuotas` |
| `GET /v4.1/magnet/status` | Every magnet in one answer, with no paging. v4.1 dropped files and links from the list |
| `GET /v4.1/magnet/status?id=` | One magnet. `data.magnets` is then an object, not an array |
| `GET /v4.1/magnet/files?id[]=` | The file tree: `{n, s, l}` leaves, `{n, e}` folders. `l` is a link and is only counted. Read for a ready magnet (`statusCode` 4) only, as decypharr does: a queued, downloading or failed magnet is its status alone, so a failing files call cannot fail its read |

- **Base URL:** the policy URL may end in `/v4` or `/v4.1`; the client always uses v4.1.
- **GET:** AllDebrid accepts GET with query parameters, which is how decypharr calls it. Other clients send the same
  calls as form POSTs. If an account refuses GET, that is the first thing to look at.
- **Answers** are `{"status": "success"|"error", "data"|"error": {code, message}}`, and errors can come with HTTP 200.
  - `AUTH_*` and `MUST_BE_PREMIUM` give `ErrAuth`; `MAGNET_INVALID_ID` gives `ErrNotFound`; `MAINTENANCE` and
    `GENERIC` give `ErrUnavailable` (AllDebrid down or failing on its side: no answer about a magnet).
  - `AUTH_BLOCKED` (a login from a new location; its message names the account's e-mail address) gives
    `ErrLocationBlocked`, also an `ErrAuth`: "AllDebrid blocked this location; approve it from the email AllDebrid
    sent". The provider's message is dropped.
- **Ids** are numbers in `magnet/status` and strings in `magnet/files`.
- **`statusCode`:**

  | Code | Meaning | Normalised |
  |---|---|---|
  | 0 | in queue | queued |
  | 1-3 | downloading, compressing, uploading | downloading |
  | 4 | ready | downloaded |
  | 5-15 | upload failed, unpack error, no peer within 20 min, too big, internal error, over 72 h, deleted on the hoster, processing failed, tracker error, no peer | error |
- **Rate limit:** 12 requests per second and 600 per minute per key; 429 and 503 come with Retry-After. The example
  budget `ad-api` is 300/min. A sync costs one request.
- **Live mode** (`session` and `counter` for diffs) is not used. A full list per sync is one request.

### TorBox (`https://api.torbox.app/v1`)

| Call | Use |
|---|---|
| `GET /api/user/me?settings=false` | `plan` (0 free, 1 essential, 2 pro, 3 standard), `premium_expires_at`, `cooldown_until` |
| `GET /api/torrents/mylist?bypass_cache=true&limit=1000&offset=N` | The list with files, id descending. No total; a short page ends it |
| `GET /api/torrents/mylist?id=&bypass_cache=true` | One torrent. `data` is an object, sometimes a one-item array; an unknown id gives 404 `ITEM_NOT_FOUND` |
| `GET /api/torrents/checkcached?hash=a,b&format=object&list_files=false` | The cache check (below) |

- **`bypass_cache=true` is required.** Without it TorBox serves a list that lags 5 to 10 minutes.
- **Paging:** 1000 per page is the maximum. Each page re-reads the last 50 entries of the previous one. A page that
  shares none of them means the list moved, and the listing starts over, up to 3 times, then `ErrIncomplete`.
- **Plans and active slots:** free 1, essential 3, standard 5, pro 10.
- **TorBox lists carry no download links.** Those come from `requestdl`, which this package never calls. So `links` is
  NULL for TorBox, as it is for AllDebrid's v4.1 list, which has no link count either.
- **Answers** are `{"success", "error", "detail", "data"}`, and `success: false` can come with HTTP 200.
  - `AUTH_ERROR`, `BAD_TOKEN`, `NO_AUTH`, `PLAN_RESTRICTED_FEATURE` and HTTP 401/403 give `ErrAuth`.
  - `ITEM_NOT_FOUND` gives `ErrNotFound`; `DATABASE_ERROR`, `SERVER_ERROR` and `UNKNOWN_ERROR` give `ErrUnavailable`.
  - The single read (`mylist?id=`) of an unknown or deleted id answers HTTP 500, so a 5xx there is checked against
    the whole list: absent from it gives `ErrNotFound` (the poller's missing-torrent grace then applies), listed gives
    the listed torrent, a failed list keeps the 5xx.
- **States:** finished with the files present means downloaded. Finished without files is a brief finalising step,
  unless the state is empty, which means the files are gone (error). Otherwise:

  | Normalised | `download_state` (detail in parentheses dropped) |
  |---|---|
  | queued | queued, paused, stalled (`stalled (no seeds)`), allocating, metaDL, checking; checkingDL while the size is unknown (≤ 0) |
  | downloading | downloading, checkingDL with a size, processing, moving, uploading |
  | downloaded | completed, cached, seeding, the `*UP` states |
  | error | error, failed, missingFiles, expired, reported missing |

  metaDL and checking are TorBox resolving the magnet's metadata, not downloading, and TorBox never gives up there on
  a dead hash ("checking is where TorBox waits for metadata and not where it verifies a hash"; a random hash was still
  `checking` at 910 s: [notes: torrent lifecycle](https://notes.debridmediamanager.com/zurg/internals/torrent-lifecycle/)).
  As queued they fall under the router's `stalled_after`, not only the 24 h `downloading_timeout`
  (`TestTorBoxMetadataStatesQueued`).
- **Rate limit:**
  - 300 requests per minute per token across all endpoints. The example budget `torbox-api` is 250/min.
  - A 429 comes with `Retry-After: 300` and locks the whole account; the gateway honours it.
  - 60 uncached adds per hour.

## Error text

Provider errors (`*APIError`) carry the provider's code and a short message, never the request URL or the body. For
all three providers the message is sanitised before it reaches an error, a log or `theatarr providers`: HTML entities
decoded, tags dropped, anything shaped like an e-mail address replaced with `[email]`, whitespace collapsed, at most
120 characters. The code is cut to one token of letters, digits, `_`, `.` and `-` (40 at most). First, before
anything is cut, the provider's key is masked (`***`) in both: as is and URL-escaped in any letter case, and in
base64 at any alignment (`provider.Redactor`). Both fields are decoded and normalized, then redacted again
before token selection or length limits; raw-value redaction also catches keys changed by normalization. The text is decoded after the gateway's own redaction, so this is
what keeps a provider that echoes the key ("invalid token ...") out of errors, logs and download-attempt messages.
The adder's errors (`adder.Error`) use the same value-only `Redactor.Sanitize` method. Each client builds its
immutable, deduplicated credential matchers once per runtime generation; no global credential cache is kept.

## Cache checks

| Provider | Endpoint | Status | What to use instead |
|---|---|---|---|
| Real-Debrid | `torrents/instantAvailability` | Answers `disabled_endpoint` since November 2024. Not called | The DMM and Torrentio feeds (they mark RD-cached results), DMM's availability (`POST /api/sponsor/availability`, DMM's key; docs/design/router.md "Cache evidence"), and earlier refusals (`torrent_not_cached` from decypharr). The only first-hand check is add, select, poll ~1-2 s for `downloaded`, delete if not: a write (router.md "Cached-only on Real-Debrid and AllDebrid"), never on shared torrents |
| AllDebrid | `magnet/instant` | Removed in 2024 | `magnet/upload` answers `ready: true` for a cached magnet, but that adds it: a write. Before an add: DMM's `ad` feed and availability (docs/design/router.md "Cache evidence") |
| TorBox | `GET /api/torrents/checkcached` | Works. `provider.CheckCached`, batches of 100 hashes, `format=object` | Also `createtorrent` with `add_only_if_cached=true` (a write; a miss still uses one of the 60/hour adds) |

**What is checked.** `CheckCached` takes a `CacheQuery{Hashes}` and refuses anything that is not a v1 hash before a
request (`TestCacheCheckRefusesNonHashes`). Which hashes are sent is the caller's choice: the search-side check sends
only results whose indexer a debrid version being searched lists with TorBox among its providers and allowed by the
indexer's `providers` allowlist (`docs/design/router.md`).

## Registry sync

`provider.Syncer.Run` lists each chosen provider in turn, in policy order. It applies each list in its own
transaction, and then recomputes every content's state. Before acquiring the writer, it loads the listing's
content rows by hash in one reader query. The write still checks the provider epoch and atomically applies the
complete list, absence marking and state changes. Concurrently inserted content is reused, and metadata fills
recheck empty fields in SQL so a stale preload never replaces another writer's name or size.

**Tables** (migration `00006_providers.sql`):

| Table | Change |
|---|---|
| `content` | `state` gains `absent` and `unreferenced`. SQLite can't change a CHECK in place, so the table is rebuilt with foreign keys off (the migration is `NO TRANSACTION` with its own `BEGIN ... COMMIT`; with keys on, dropping `content` would cascade). A test seeds rows at version 3, migrates, and checks that `content_version`, `content_provider` and `content_file` survive, plus down and up again |
| `content_provider` | New columns `state` (`present`/`absent`), `status` (raw), `progress`, `size_bytes`, `links` (count), `duplicates`, `added_at`, `absent_at`. `torrent_id` and `folder_name` (the torrent name) are filled. `seen_at` is when the current presence started |
| `provider_sync_run` | One row per provider per run: `ok`, `error`, and the JSON summary |

The same transaction writes the record of the account (migration `00129_provider_torrent.sql`, torrents.md "The
record"): `provider_torrent` gets the complete list, one row per torrent (same-hash duplicates included), and
`provider_torrent_read` the time. A failed listing leaves both alone. `Syncer.Refresh` (the Torrents page's Refresh)
runs `Run` for every provider with a key, back-off or not, and schedules the failed ones' retry jobs as `Handle` does.

**Content states:**

| State | Rule |
|---|---|
| `present` | On at least one configured provider, and referenced: a `content_version` (an import), a `version_file`, or a live `link` |
| `unreferenced` | On at least one provider, referenced by nothing. The "unreferenced content" report to prune by hand |
| `absent` | On no provider, and **every configured provider has had a complete listing**. A provider with no key set therefore keeps contents `unknown` |
| `unknown` | Not every configured provider has been listed yet |
| `gone` | Set on purpose later (trumps, nukes). Sync leaves it unless a provider has the hash |

**Rules:**
- **A provider torrent with no registry row** gets a content row: name, size, first seen at its added date.
- **An existing content** only gets an empty name or size filled.
- **Duplicates:** when a provider holds a hash twice, the row keeps a downloaded torrent first, then the oldest.
- **A torrent that leaves a provider's list** turns its row `absent` with `absent_at`. Nothing is deleted. If it comes
  back, the row turns `present` again.
- **Idempotent:** every write compares first. A re-run with nothing new changes no row besides its
  `provider_sync_run` row (tested by dumping both tables).
- **A failed list** (an HTTP error, a refusal, or a list that keeps shifting) writes a failed run row and changes
  nothing else. The other providers still run. `theatarr providers sync` returns the error; the job treats it as a
  partial success (below).
- **`-dry-run`** computes everything inside a transaction and rolls it back, run rows included. With several
  providers, each is computed against the committed state.

**Job:** kind `provider.sync`, payload `{"providers": [...], "retry": bool}` (no providers means all with a key).
Enqueue it with dedupe key `provider.sync`. `Syncer.RegisterJobs(worker)` registers the handler.

**Partial failure and per-provider back-off** (`internal/provider/retry.go`). One provider failing (for example
AllDebrid's `AUTH_BLOCKED` location block) does not make the others list again:
- **Back-off per provider**, read from its `provider_sync_run` rows (no new table): the failed runs since its last
  complete one, and when the last ended. The next attempt is 2 min × 2^(failures-1) after it, capped at 1 h
  (2, 4, 8, 16, 32 min, then hourly).
- **A scheduled sync** (no providers in the payload) skips providers that are backing off, with a log line.
- **A failed provider gets its own retry job:** payload `{"providers": ["ad"], "retry": true}`, `run_at` its next
  attempt, dedupe key `provider.sync/<key>/<failures>` (so a failing retry can enqueue the next one while it runs),
  one attempt. A retry job never fails: it schedules the next retry instead. It does nothing when a sync since has
  listed the provider completely.
- **The job succeeds** when at least one provider synced, or none was due. When every provider it listed failed, it
  fails and the queue retries it; by then they are all backing off, so the retry only finishes it.
- **`theatarr providers status`** has a `retry` column (`2 failed, next 14:32 (in 3m0s)`) and a line per failing provider
  with its last error; `-json` adds `failures` and `retry_at`.
- `theatarr providers sync` lists what it is asked to, back-off or not, and a complete listing ends the
  back-off.

**Schedule in `theatarr serve`** (policy section `provider_sync`, top level because `providers` is a map of provider keys):

```yaml
provider_sync:
  interval: 6h   # default 6h, minimum 15m
```

- Only providers whose `api_key_env` is set are synced. The others are named in one log line at start
  (`provider sync skips providers with no key set`).
- The first sync is due one interval after the oldest provider's latest run (`provider_sync_run.started_at`), or one
  minute after start if a provider has never run or the sync is overdue. The minute lets the egress check run first.
  Then every interval.
- Each tick enqueues `provider.sync` with dedupe key `provider.sync`. While one is queued, retrying or running, the
  tick enqueues nothing and logs `provider sync still queued or running`, so a slow sync never stacks.

## Checking a deployment

Inside the `theatarr` container (settings in its database, keys in its environment):

1. `theatarr providers status -account`
   - One line per account: plan, expiry and, for RD, active slots.
   - Check that the cache line says TorBox `checkcached` and none for RD and AD.
2. `theatarr providers sync -dry-run -provider rd`
   - The torrent count should match RD's web UI. `bad hash` should be 0.
   - `content state changes` shows what `unknown` contents would become.
   - `-v` logs each request.
3. The same for `-provider ad` and `-provider tb`. AD is a single request; if it fails with an HTTP error, see the GET
   note above.
4. `theatarr providers sync` (all three), then run it again. The second run must show only
   `unchanged` and `content state changes: none`.
5. `theatarr providers status`: `contents by state`, with `absent` meaning gone from every account. Spot-check a few
   `absent` hashes against RD before trusting the number. The `unreferenced` count is the prune-candidate list (by
   hand only).

## Design notes

| Choice | Why |
|---|---|
| Registry states `absent` and `unreferenced` beside present and gone; `unknown` until every provider is listed | Absent everywhere differs from not yet known, and unreferenced content is the prune report |
| "Unreferenced" = no `content_version`, `version_file` or live `link` | Those are the references: imports, files kept from a former arr instance and Theatarr's links |
| Only TorBox has a provider cache check; RD and AD answer from feeds and refusals | Both providers removed their endpoints |
| AllDebrid: a full list per sync, not live mode | One request either way, and no session state to keep |

Registry sync treats a successful empty inventory as a complete listing, so an account whose owner removed its
last torrent can become absent. A listing error (including a partial listing) keeps presence unchanged.

Provider adds require affirmative cache evidence: TorBox's known cached-success answer, never a substring such
as “not cached” or “uncached”. TorBox BAD_REQUEST/INVALID_OPTION describe request failures and do not blocklist
release content. AllDebrid upload success must match the requested info hash before an add or ownership proof is
accepted. Add responses honor Retry-After delta seconds and HTTP dates, including temporary server failures.
