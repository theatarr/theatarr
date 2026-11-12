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

## Retention (`db.retention`)

Once its backup is written and finished jobs are pruned, the nightly job enqueues `db.retention` (one per night,
priority -10, below every other job; a failed nightly run enqueues none) and the job deletes in
batches of 500 rows, at most 100 batches per table a run, so a backlog drains over a few nights without holding the
writer. Code: `internal/retention`. The windows:

| Setting | Default | Tables |
|---|---|---|
| `--retention-days` (`THEATARR_RETENTION_DAYS`) | 180 | failed `download` rows (their attempts, files and progress go with them), finished `acquire_run`, `router_plan`, `library_action`, `settings_audit` |
| `--retention-run-days` (`THEATARR_RETENTION_RUN_DAYS`) | 30 | `provider_sync_run`, `settings_quality_sync_run`, sent, failed and dropped `notify_delivery`, `provider_cache` rows expired that long ago (cache evidence from every feed search would otherwise grow without bound; index `provider_cache_expires`, migration 00086) |

0 keeps that kind for good. Kept whatever their age: the latest successful and the latest failed sync run of each
provider; the latest quality sync run of each TRaSH source and outcome (the sync reads the latest run and the latest
that read TRaSH), and runs still running; pending deliveries; every download that is not failed, and a failed one that a link, a live library claim,
an import still waiting for the mount or partial, a router blocklist entry, a router offer or a
queued or running job refers to (a finished import or an expired claim does not keep it); applied
`link`/`replace` library actions whose download is kept or whose path is still an active or dead link (the offer
sources and the superseded check read them; adoption provenance lives on `link.adopted_from`); acquire
runs that are not finished, the latest run of each version and target (the wanted loop's "last searched") and runs a
queued or running job refers to, and a run whose children are kept; router plans a kept shadow grab or RSS pick
refers to; the current settings generation's audit rows; pending shadow grabs and open RSS picks.

Not pruned, by design: `router_offer` (one row per version, title and release; deleting one re-offers that release
at the next sweep), `notify_event` (the once-only memory of scanned events), `arr_history` (a mirror of the arrs'
own history that decisions read), and imported downloads (offer sources, and the journal keeps routed decisions by
them). `import_run`, `shadow_run`, `journal_decision` and finished jobs keep their own pruning in the nightly job. A
journal decision older than 180 days whose only download was a pruned failed one is deleted instead of trimmed.
`TestRetentionCoversEveryTable` checks that every table the migrations create is pruned by a rule, goes
with a pruned row (`ON DELETE CASCADE`), or is listed in `keptByDesign` with its reason (configuration, catalog and
library state, per-key state, pruned by its own package, a memory kept by design); a new table fails it until it gets a rule or a listed
reason.
