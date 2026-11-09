# Operational readiness: health checks, backups, policy revisions, job priorities

The checks and records that make an install safe to run and recover: health checks, nightly backups, the policy
revision each decision was made under, and job slots that keep acquisitions ahead of background work. Restore steps
and the drill: [`../restore.md`](../restore.md). Migration `00031_policyrev.sql`.

## Health checks

`internal/health` runs a fixed set of cheap checks, after the arrs' `HealthCheckService`. Results are cached for a
minute, each check runs under a 10 s timeout, and a panicking check reports an error instead of taking the process
down. Every check reports, ok ones too; the arr views list only the others, as the arrs do.

| Source | Reads | Warning | Error |
|---|---|---|---|
| `MountCheck` | stat and first name of `<library.mount.path>/<all_dir>` (only with `library.enabled`) | | not readable, empty (an unmounted mount point is an empty directory), no answer within `stat_timeout`; while an earlier probe is still blocked no new one starts (a hung FUSE call can't be cancelled), so probes never pile up |
| `MountHelperCheck` | `theatarr serve` with mount helpers only (`docs/design/library.md` "Mount guard"): helpers killed or crashed in the last 15 minutes. With helpers, `MountCheck` itself reads through the library's mount guard in a helper | 3 or more (the mount keeps hanging), with the last call that did not answer and any killed helper still stuck in the kernel | |
| `MetadataRefreshCheck` | `title_meta`, `tvdb_sync` | TMDb refresh off while titles exist; titles without a row an hour after they were added; half or more of the last day's refreshes failed (3 or more); no good fetch for 2 × `metadata.art.interval` + 1 h while rows are overdue; TVDB `/updates` not run for 3 h (when configured). Rows a day overdue are a notice | |
| `JobBacklogCheck` | `jobs.Queue.Backlog`: per kind, ready jobs and the oldest one's wait past `run_at` | an acquisition/import kind waiting 15 min; another kind 6 h | an acquisition/import kind waiting 1 h |
| `FailedJobsCheck` | failed jobs that need attention (`docs/design/jobs.md`) | any | |
| `BackupCheck` | the newest `theatarr-*.db` in the backup dir, and its policy archive and manifest | backups off; newest 36 h old; no archive or manifest beside it | none a day after start; newest 72 h old |
| `ProviderSyncCheck` | `provider_sync_run` per provider with a key | a provider's last sync failed, or no complete sync for 3 intervals | every provider so |
| `ProviderAccountCheck` | each enabled provider's account (`Account`: premium and expiry) through the running clients, so through the gateway and the provider's budget; a read is reused for 6 h (a failed one for 30 min), keyed on the provider and a hash of its key: a settings save keeps an unchanged account's read and reads a changed key at once; the expiry is judged at every run | premium ends within 7 days; a provider does not answer (naming it) | not premium, premium ended, or the key refused (`ErrAuth`, AllDebrid's blocked location too) |
| `UnsettledAddsCheck` | downloads in `adding` for over 10 min (`docs/design/router.md` "Crash between add and record") | any: a provider add whose outcome the account has not shown yet; never failed by time | |
| `CachedOnlyCheck` | downloads whose cached-only check is unfinished (`download.cached_check`) more than 30 min after the add (`docs/design/router.md` "Unfinished checks") | any: no verdict yet, or an uncached add of ours whose delete waits for the gate or a working delete | |
| `TorrentLimitCheck` | the latest complete `provider_sync_run` count of each account with a torrent limit (AllDebrid `magnet_limit`, default 5000; `docs/design/torrents.md` "Account limit") | 90 % of the limit or more; nothing is deleted | |
| `IndexerStatusCheck` | gateway back-offs (`indexer:<key>`) and spent hourly/daily budgets of indexer targets | some indexers unavailable | all of them |
| `VpnEgressCheck` | the gateway's egress summary (only with a policy `egress`) | | VPN egress not confirmed |
| `DiskSpaceCheck` | free space of the database's directory | under 2 GiB or 5 % | under 256 MiB |

Where they show:

- **Native API:** `GET /api/theatarr/v1/health` (admin): `{status, checks: [{source, name, type, message, detail,
  checked_at, took_ms}]}`; `?refresh=1` runs them now. `GET /status` adds `health` (the worst type) and
  `policy_revision`.
- **Admin › System › Status**: a "health checks" stat and a Checks list; failing checks join Needs attention (not the
  VPN and failed-job checks, which that list already names).
- **Arr views:** every view's `GET /api/v3/health` appends the failing checks after its own shadow-mode and
  router notices, in `HealthResource` shape, so Homepage, LunaSea and Ruddarr show them.
- **Webhooks:** the notifier's scan (every `notify.poll`) sends one `Health` event per check that turns bad or changes
  type, and `HealthRestored` per check that clears, carrying its previous issue; message-only changes send nothing.
  The state is `notify_state.health_checks`; installed targets respect both On* flags and `includeHealthWarnings`.
- **CLI:** `theatarr health -db …` runs the checks that need no running service (not the indexer, VPN and account ones)
  and exits 1 on an error.

## Backups

Backups are on by default: `--backup-dir` defaults to `backups/` next to the database (`off` disables), `--backup-keep`
to 14 (env `THEATARR_BACKUP_KEEP`). Each night writes a set: the database snapshot, a tar of the policy directory, and a
manifest with hashes, the policy revision and the names of the secret env vars (never values). Retention removes a
set's three files together. `theatarr backup now|list|restore|fingerprint`; details in [`../restore.md`](../restore.md).
Each nightly schedules the next night even when backup or job pruning fails, including after all three attempts
are exhausted; retries retain the failure and share the next night's dedupe key (`TestNightlyFailureStillSchedulesNext`).

## Policy revisions

`policy.Policy.Revision`: the first 12 hex digits of SHA-256 over each policy file's name, length and bytes, in name
order (the `.yaml`/`.yml` files read; dotfiles and subdirectories are not part of it). Logged at start.

- `journal_decision.policy_rev`: the policy a search decided under (acquire searches and RSS picks).
- `acquire_run.policy_rev`: the policy in force when the run was queued.
- `download.policy_rev`: the policy the router opened the download under.
- Journal entries carry `policy_revision` (the search's, else the download's, else the queued run's) and
  `queued_policy_revision` when a run was queued under a different one; the text form adds a `policy` line, the
  entry page shows it marked "(current)" or "(earlier policy)" against `/status`.

Rows from before 00031 have `''`.

## Job priorities

The worker leases jobs by priority then age, with two refinements (`jobs.WorkerOptions.Urgent`, `Reserved`):

- The acquisition and import kinds (`acquire.search`, `acquire.wanted`, `acquire.research`, `router.route`,
  `router.offer`, `plex.scan`) are leased before any other ready job, ahead of priority and age.
- One extra slot (`--workers-reserved`, env `THEATARR_WORKERS_RESERVED`, default 1) runs those kinds only, on top of the
  general slots (`--workers`, env `THEATARR_WORKERS`, default 2). A backlog of `meta.art`, `arrview.maintenance`,
  `provider.sync` or `arr.import` can hold both general slots and a grab still runs at once.
- Queue age per kind: `jobs.Queue.Backlog`, the metrics `theatarr_jobs_ready{kind}` and
  `theatarr_jobs_oldest_ready_seconds{kind}`, and `JobBacklogCheck`.

The `priority` column orders jobs within a class.

## Tests

`internal/health` (every check, cache, timeout, panic, hung mount), `internal/jobs` (urgent-first leasing, the
reserved slot running while background work holds the general slots, backlog ages), `internal/journal` (revisions on
run and download entries), `internal/notify` (one event per change of type, not of message), `internal/arrview`
(service checks in GET health, one webhook per changed check), `internal/web` (`/health`, `/status`), `cmd/theatarr` (nightly
set and retention, backup → restore round trip with equal fingerprints, damaged sets refused, tar escapes refused,
secrets by name only, `theatarr health`), `web/src/lib/health.ts`.

## Defaults in the container

- Backups are on: the image sets `THEATARR_BACKUP_DIR=/data/backups`, and 14 sets are kept.
- Three worker slots: 2 general and 1 reserved.
- With `notify.enabled`, a check that is already failing sends one `Health` webhook at the first scan.
