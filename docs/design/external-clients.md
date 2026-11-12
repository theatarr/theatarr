# External download clients (qBittorrent API)

A library can send its grabs to an external qBittorrent-API client, the way Radarr and Sonarr do, so an install that
uses decypharr can keep it. Theatarr searches and decides; the client talks to the
debrid provider, checks the cache, waits for the files on its mount, symlinks them into its download folder and
reports the torrent complete. Theatarr then imports it.

Without `add_via: client` the router adds to the providers itself (docs/design/router.md "Deviations from the
design": direct adds). The `qbittorrent` client kind (storage `seedbox`) is accepted by validation, but nothing sends
to it.

## Flow

1. **Decide as for a provider add.** Search, decide, blocklist, the upgrade rule and the in-flight checks
   run unchanged (`router.candidate` up to `openDownload`). Three provider-only steps branch for a client-routed
   version:
   - `route()`'s `not_debrid` stop (no providers) does not apply when `add_via: client`;
   - `knownMiss` and `providerOrder` are skipped; the client branch checks its own miss row (key `client:<name>`)
     instead;
   - a row the in-flight check finds in `adding` with `client` set is settled from the client (step 4's read: the
     hash held, under any category, means added, absent means queued and resumed), never by `reconcileAdding`, which reads
     a provider account.
2. **Add to the client instead of the providers.** For a version with `add_via: client` the provider loop is
   replaced by one request: `POST /api/v2/torrents/add`, form `urls=<bare magnet>` (the same bare magnet the
   provider adds use, no trackers) and `category=<client category>`. The gates are the same as for a provider add
   (`mayWrite`: router on, Grab on, active version).
3. **The client does the debrid part.** decypharr (v2.7.1, `pkg/server/qbit`) submits to its debrid account at once
   and refuses an uncached release when its `download_uncached` is off: HTTP 4xx/5xx with JSON
   `{error, code, retryable, permanent}`, code `torrent_not_cached`. Other codes: `torrent_blocked`,
   `invalid_magnet`, `traffic_exceeded`, `too_many_active_downloads`, `hoster_unavailable`.
4. **Poll the client.** Each poll reads `GET /api/v2/torrents/info?hashes=<hash>` once per open download, with no
   category filter: decypharr 2.7.1 reads each `hashes` value as one hash (`pkg/server/qbit/context.go`
   `hashesContext`), so qBittorrent's `h1|h2` form finds nothing there. The same per-hash read serves the pre-add
   adoption check and the settling of an `adding` row; a hash held under another category (an arr's, from before
   the handover) is adopted as it is (`reused = 1`). States:
   - `metaDL`, `queuedDL`, `downloading`, `stalledDL`, `pausedDL`, `checkingDL`, `forcedDL`: **downloading**,
     with the client's progress;
   - `pausedUP`, `stoppedUP`, `uploading`, `stalledUP`, `queuedUP`, `forcedUP`, `checkingUP`: **ready**
     (decypharr's `MarkAsCompleted` sets `pausedUP` after `processSymlink` has made its links). Progress 1 in a
     downloading state is still downloading: decypharr reports it before its links exist, and its interim
     `content_path` (the category folder) is not kept;
   - `error`, `missingFiles`: **failed**;
   - missing from the answer for `missingGrace` over `missingPolls` polls (the provider rule): **failed**. So the
     client must keep completed torrents listed: decypharr's download action `symlink` (default) or `download`;
     action `none` drops the entry as it completes and is not supported.

   A client that does not answer (refused connection, timeout, 5xx, login failure) fails no download: its rows wait,
   polls back off as for a provider outage, and the outage is logged as one error (errors inbox) until it answers.
5. **Import, from the mount or from the client's folder** (per client, `import_from`):
   - `mount` (default): the existing import. The release folder is found on Theatarr's debrid mount by hash and
     names (the client's torrent name is added to the names), confirmed by zurg where configured, probed and linked
     into the library root. decypharr's download folder is not read.
   - `download_folder`: as the arrs do. At ready, the client's `content_path` (mapped by `remote_path` →
     `local_path` when set) becomes the download's `source_path`, and the existing path import follows it: a symlink
     into the mount is read with `readlink` under the mount guard (`Mount.Locate`). `Locate` also resolves a directory of
     symlinks (decypharr's release folder) to the mount release folder its video links point at; links
     that point at different release folders, or off the mount, are refused (`ErrNotOnMount`). Absolute link
     targets are mapped by `remote_path` → `local_path` too before that check. A completed download whose content
     path does not exist imports from the mount as in `mount` mode (by hash and names): decypharr's default
     multi-season handling makes one folder per season and none for the torrent itself.

   Either way Theatarr links mount content only, so Theatarr's mount must show the client's debrid account (for
   decypharr, the same zurg or rclone mount). A release that never shows waits and expires like a direct add.
6. **Failures as for provider adds.** A failed client download blocklists the hash for the version and searches again (the
   provider-failure path). Add refusals:
   - `torrent_not_cached`: a cache miss for that hash on this client (cache row under `client:<name>`, `miss_ttl`),
     the route moves to the next candidate, nothing blocklisted;
   - `permanent: true` or `torrent_blocked`/`invalid_magnet`: refused, blocklisted for the version, next candidate;
   - `retryable: true` (or no JSON), 5xx, a transport error: the route is deferred and retried, as when a provider
     is paced.

## Configuration

```yaml
clients:
  # decypharr keys its arr identity and per-arr settings (download_uncached) on the category, and probes the login
  # username to tell Radarr from Sonarr. So one entry per view flavour, each with its own category.
  decypharr-movies:
    kind: decypharr
    url: http://decypharr:8282
    category: theatarr-movies
    username_env: THEATARR_DECYPHARR_MOVIES_USER   # Theatarr's Radarr view URL: decypharr calls it back like an arr
    password_env: THEATARR_DECYPHARR_MOVIES_PASS   # a Theatarr API key for that view
    callback_view: radarr
  decypharr-tv:
    kind: decypharr
    url: http://decypharr:8282
    category: theatarr-tv
    username_env: THEATARR_DECYPHARR_TV_USER
    password_env: THEATARR_DECYPHARR_TV_PASS
    callback_view: sonarr
versions:
  movie-web-theatarr:
    add_via: client            # default: providers (Theatarr adds to the providers itself)
    client: decypharr-movies
```

- `versions.K.add_via`: `providers` (default; Theatarr adds itself) or `client`. With `client`, `client` must name a
  client of a debrid kind (`decypharr`), storage stays `debrid`, import `symlink`, and `providers` may be empty (the
  client chooses the account). Policies without these keys stay valid.
- Login is qBittorrent's: `POST /api/v2/auth/login` with the env credentials, keep the `SID` cookie, log in again
  once on 401/403. decypharr without `use_auth` accepts any credentials; it probes them as an arr (username = arr
  URL, password = key) and calls that arr back, which Theatarr's views serve (docs/design/clients.md).
- `callback_view` is required for decypharr and checked: for a client-routed version the
  view must serve that version and its flavour must match the version's kind (radarr for movies, sonarr for
  series), so decypharr's callbacks reach a view that knows the download.
- `import_from: mount | download_folder` (default `mount`); `remote_path` and `local_path` (optional, both or
  neither, absolute) map the client's paths to Theatarr's, like an arr's remote path mapping. `download_folder` with
  no mapping needs the client's folder at the same path in Theatarr's container.
- One category per client (`category`); required when an `add_via: client` version uses the client
  (decypharr refuses an add without one). Provider-routed configurations stay valid without it. The UI's library editor has a "Send grabs to" choice: debrid
  providers (Theatarr adds) or a download client.

## What stays and what the client owns

Kept for client-routed versions: the router and Grab gates, blocklist and re-search, the import's mount, root and probe checks.

Owned by the client, stated in help: cached-only and
uncached downloads, provider order and fallback, pacing and budgets, Real-Debrid file selection, stall handling.
Theatarr's `cached_only`, provider pacing, 451 holds, own-add proof and stall delete do not apply to these adds.

Theatarr never deletes from the client: ending a client download in Activity ends Theatarr's record only. Removing a
torrent from decypharr can remove it from the debrid account, which may be shared; that stays the client's or the
user's choice. decypharr's own list and download folder are cleaned in decypharr.

## Data

Migration 00108: `download.client TEXT` (the client key), NULL for provider downloads. A client download keeps
`provider` NULL, so the provider sweeps, reconcile, stall timeout, torrent manager and delete paths, which all
select on the provider, never touch it. A row left in `adding` by a crash is settled from the client: the hash in
`torrents/info` (by hash, under any category) means added, absent means queued again. Before an add the same read
adopts a hash the client already holds, in any category, instead of adding it twice.

## Not covered

- Real-file clients (a seedbox qBittorrent, hardlink or copy imports), SABnzbd/Usenet.
- decypharr's per-add `debrid` field (pinning an account per add); the client picks.
- Removing torrents from the client after import.
- `.torrent` uploads: bare magnets only, as for provider adds.
- zurg's own qBittorrent endpoint has not been checked; any client that speaks this API subset works.
