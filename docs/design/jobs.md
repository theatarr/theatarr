# Failed jobs: resolve, retry, dismiss

A failed job (for example `provider.sync` failing with AllDebrid `AUTH_BLOCKED` until the location is approved)
would otherwise keep Health at "1 failed job" and stay in "Needs attention" for good, even after a later sync worked.
The rules below say when a failed job stops needing attention, and how a person retries or dismisses it. Code: `internal/jobs/manage.go`, `internal/web/jobs.go`, `cmd/theatarr/jobs.go`,
`web/src/{api,lib}/jobs.ts`, Admin › System › Tasks and Status (`web/src/pages/admin/jobs.tsx`, `overview.tsx`). Migration `00025_jobclear.sql`.

## Rules

A failed job **needs attention** until one of these happens. No job is deleted by any of them.

- **Resolved** (derived, nothing stored): a later job (higher id) of the same kind finished `done` with the same
  payload, or with the empty payload `{}`, which kinds use for a run over all their subjects (`provider.sync` of every
  provider, the `meta.art` batch, `db.nightly`). So `meta.art {"title_id":7}` failing is resolved by a later done run
  for title 7 or by a batch, not by title 8. `resolved_by` is the first such job.
- **Retried**: `retry` queues a fresh job with the same kind, payload, priority, max attempts and dedupe key, and
  `retry_of` pointing at the old one. The new job carries the attention from then on: if it fails, it needs
  attention; if it finishes done, it also resolves the old one. Retrying while a retry is queued or running returns
  that one; if the dedupe key already has a queued or running job, that job is returned and linked.
- **Dismissed**: `dismissed_at` and `dismissed_by` (user, `key:<name>`, or `cli:<user>`). Only failed jobs.

Job pruning (`Queue.Prune`, older finished jobs) is separate from these rules.

## Migration

`00025_jobclear.sql` adds `job.retry_of`, `job.dismissed_at`, `job.dismissed_by` and two indexes (`job_resolve` on
`(kind, state, id)` for the resolve lookup, `job_retry_of`). Resolution needs no column. Retry links and dismissals
are columns rather than payload fields, so a retry carries exactly the payload the handler reads and the same-payload
rule holds.

## API (`/api/theatarr/v1`)

These routes require admin permission. Writes take a session with the CSRF token or an API key. Retrying
`torrents.restore`, whose payload carries the person’s confirmed re-adds, works with an admin's API key as with a
session (keys confirm like sessions, [design/settings.md](settings.md) "Who confirms").

| Route | What |
|---|---|
| `GET jobs?state=&kind=&attention=&dismissed=&limit=` | `counts` per state plus `attention`; each job has `retry_of`, `retried_as`, `resolved_by`, `dismissed_at`, `dismissed_by`, `attention` |
| `POST jobs/{id}/retry` | `201 {job, retry_of, created: true}`; `200` with `created: false` when a retry is already queued or running; `409` unless failed |
| `POST jobs/{id}/dismiss` | the job; `409` unless failed; dismissing twice keeps the first |
| `POST jobs/dismiss-resolved` | `{dismissed: n}` |

The event stream's `jobs` counts carry `attention` too, so the top-bar dot and Health count only failed jobs that
need attention.

## UI

- Health: "failed jobs to look at" is the attention count; Needs attention lists those jobs with Retry and Dismiss.
- Jobs: a "need attention" stat; the Failed section lists failed jobs not dismissed, with "Dismiss all resolved"
  when any is resolved. Every table marks rows with muted badges (`resolved by #N`, `retried as #N`, `retry of #N`,
  `dismissed`); failed rows get Retry (unless resolved or retried) and Dismiss (unless dismissed).

## CLI

```
theatarr jobs list [-state failed] [-kind K] [-attention] [-dismissed=false] [-format json]
theatarr jobs retry ID...
theatarr jobs dismiss ID...
theatarr jobs dismiss -resolved
```

They work on the database file (`-db`, env `THEATARR_DB`), also while the service runs; its workers pick up a retry on
their next poll.

## Pages that could not load

Admin › Jobs shows the failed-jobs request's error (or a loading state) in "Needs attention", never "No failed job
needs attention" when that request failed. Status lists each source it could not load (failed jobs, health checks,
the gateway, downloads) as a "Could not load …" item, and says "Nothing needs attention" only once every source
loaded without an error.

## Scheduled tasks

Recurring work is not a job: each recurring task is one row in `task` (migration `00133_task.sql`, code
`internal/tasks`), as Sonarr's System › Tasks. Its loop keeps its own timing (start-up delay, Pause, the settings in
force, wall-clock times) and registers the task (name, schedule) while it runs; a run updates the row (last start
and finish, error, when the last good run ended, next run) and writes no job row. A run with nothing to do (nothing
due, the mount backing off, a manual backup running) records nothing. Two runs of one task never overlap (a run
while one goes on does nothing); a failed run is tried again after the job queue's back-off (10 s, doubling) up to
the task's tries; a run lost to a restart is not resumed, the next time covers it (the nightly backup, whose next
run the row keeps, runs at start when it came due while stopped; the upgrade carries a queued nightly's due time).
Whether a task runs is known in the process only. A task whose newest run failed needs attention (Tasks and Status,
the attention count) until a run works; tasks that replaced a job report a failed run to the error inbox as the job
did. A failed one-subject job of a task's kind (one title's art or series refresh, one provider's retry) is resolved
when that task works after it (`resolved_by_task`), as a later `{}` sweep job resolved it before.

| Task | When | Tries |
|---|---|---|
| `acquire.wanted` search round | every 15 min, first 5 min after start (at once when Pause goes off); paused while Pause is on or no library is Live | 1 |
| `shadow.rss` RSS read | `shadow.rss_interval`, first 3 min after start; on demand only (the arr `RssSync` command, Run now) while RSS is off | 2 |
| `provider.sync` all providers | `provider_sync.interval` after the oldest provider's last sync (soon after a restore) | 5 |
| `meta.art` batch | `metadata.art.interval`, and when a title has no details yet | 5 |
| `meta.series` sweep, `meta.scene` scene names | `metadata.series.interval`; every 3 h | 1; 3 |
| `tvdb.updates`, `lists.movies`, `lists.arr`, `anime.playlists` | hourly; `metadata.lists.interval`; 15 min; `anime.playlists.interval` | 1 |
| `torrents.backup` scheduled torrent-list backup | `torrents.backup.every` (checked every 10 min); a manual backup (a job) and it never overlap | 1 |
| `db.nightly` backup and tidy-up, then `db.retention` | daily at `--backup-hour`; a night missed while stopped runs at start | 3; 3 |
| `notify.digest` | daily at `notify.digest_at`, once a day (its last start), listed while notifications are on | 3 |
| `library.relink`, `library.dangling`, `library.trash` | `library.relink_interval` (and after zurg's log rotates); `library.dangling_every` and at start; daily | 1 |
| `quality.sync`, `db.optimize`, `egress.check` | every minute (recorded when a source was due); daily from start; `egress.interval` from start | 1 |
| `cardigann.refresh` indexer definitions | `cardigann.definitions.refresh` after the last check; a failed one again after an hour | 1 |
| `anime.discover` anime franchise discovery and refresh | every minute, first 2 min after start (recorded when it queued a build job) | 1 |

Not tasks: the download poll, the library's import pass, now playing, the offer sweep, and the health hand-off to the
error inbox. The per-subject runs of the same kinds stay jobs: one provider's retry, one title's art or series
refresh, a manual torrent backup.

`GET tasks` lists the registered tasks (key, name, `every_seconds` or `at`, `paused`, `running`, last start/finish,
`last_error`, `last_ok`, `next_run`); `POST tasks/{key}/run` runs one now in the background (`202`; `409`
while it runs or is paused; `404` for a task not scheduled). The arr `RssSync` command is the RSS task's Run now.
System › Tasks lists them under "Scheduled" with Run now.

## Retention (`db.retention`)

Once the nightly task is done (its last try included), it starts the `db.retention` task right away (before
0.4.5 retention was a lowest-priority job that waited for idle workers), which deletes in
batches of 500 rows, at most 100 batches per table a run, so a backlog drains over a few nights without holding the
writer. Code: `internal/retention`. The windows:

| Setting | Default | Tables |
|---|---|---|
| `--retention-days` (`THEATARR_RETENTION_DAYS`) | 180 | failed `download` rows (their attempts, files and progress go with them), finished `acquire_run`, `router_plan`, `library_action`, `settings_audit` |
| `--retention-run-days` (`THEATARR_RETENTION_RUN_DAYS`) | 30 | `provider_sync_run`, `settings_quality_sync_run`, sent, failed and dropped `notify_delivery`. `provider_cache` rows go a day after they expire, whatever this setting and `--retention-days`, 0 included (cache evidence from every feed search would otherwise grow without bound; index `provider_cache_expires`, migration 00086; migration 00132 deleted the ones already past that) |

0 keeps that kind for good. Kept whatever their age: the latest successful and the latest failed sync run of each
provider; the latest quality sync run of each TRaSH source and outcome (the sync reads the latest run and the latest
that read TRaSH), and runs still running; pending deliveries; every download that is not failed, and a failed one that a link, a live library claim,
an import still waiting for the mount or partial, a router blocklist entry, a router offer or a
queued or running job refers to (a finished import or an expired claim does not keep it); applied
`link`/`replace` library actions whose download is kept or whose path is still an active or dead link (the offer
sources and the superseded check read them; adoption provenance lives on `link.adopted_from`); acquire
runs that are not finished, the latest run of each version and target (the search schedule's "last searched") and runs a
queued or running job refers to, and a run whose children are kept; the current settings generation's audit rows.

Not pruned, by design: `router_offer` (one row per version, title and release; deleting one re-offers that release
at the next sweep), `notify_event` (the once-only memory of scanned events), and imported downloads (offer sources, and the journal keeps routed decisions by
them). `shadow_run`, `journal_decision` and finished jobs keep their own pruning in the nightly job. A
journal decision older than 180 days whose only download was a pruned failed one is deleted instead of trimmed.
`TestRetentionCoversEveryTable` checks that every table the migrations create is pruned by a rule, goes
with a pruned row (`ON DELETE CASCADE`), or is listed in `keptByDesign` with its reason (configuration, catalog and
library state, per-key state, pruned by its own package, a memory kept by design); a new table fails it until it gets a rule or a listed
reason.
