# Error inbox

Theatarr keeps its own unexpected failures in the database so that an admin, the CLI or an agent can see what broke,
how often and since which build, without reading logs. Expected outcomes (a provider refusing an add, an indexer
that is down, a 4xx answer, a context cancelled at shutdown) are not errors and stay out. Code:
`internal/errinbox`; migration 00101.

## What is captured

| Source | From | Component |
|---|---|---|
| `log` | every error-level log record of `theatarr serve`, through the log ring's `OnError` hook (`logring.Ring.OnError`); the log output is unchanged | the logger's component |
| `panic` | a recovered panic with its stack: HTTP handlers (`internal/server`), job handlers (`internal/jobs` worker), health checks, the live-settings workers, and the serve loops wrapped in `errinbox.Go` | `http`, `jobs`, `health`, `live-settings`, or the loop's name |
| `http` | a 5xx answer of Theatarr's own handlers (method, route pattern, status); 502, 503 and 504 are left out (a dependency down or switched off, which health reports) | the route pattern, `GET /api/theatarr/v1/...` |
| `job` | a job that failed for good (its last attempt); retries that will run again and a job that panicked (already a `panic` row) are not. Also the now playing pre-warm's missing or unreadable next episode, one item per version and episode (`Event.Key`; [nowplaying.md](now-playing.md)) | the job kind; `nowplaying.prewarm` |
| `health` | a health check that turned `error` (from ok, notice or warning, or at the first look); when it is no longer error the row resolves itself with the note `recovered <time>: ...`. Warnings and notices stay on the health screen | the check |

A log record can opt out without changing its level: `log.ErrorContext(errinbox.Quiet(ctx), ...)`. The inbox
also skips records whose `err` (or `error`) is `context canceled`.

Panic handling:

- HTTP handlers: the panic is recovered, recorded, the client gets a 500 (or the connection is aborted when the
  answer had started) and the server keeps serving (net/http alone would close the connection without an
  answer). `http.ErrAbortHandler` panics on to net/http.
- Job handlers and health checks recover panics and record them with their stack. The worker goes on to the next
  job.
- Live-settings workers recover panics (the worker shows the error and restarts on its next settings) and record
  them.
- The top-level serve loops (quality sync, torrents schedule, error inbox health observer, ceiling, notify, offers,
  jobs, http) crash the process on a panic (the container restarts it), on purpose: a loop that died
  silently would stop grabbing or importing with no sign. `errinbox.Go` writes the panic synchronously (at most 2
  s) before it panics on.

Expected noise taken out of the inbox (the log lines are unchanged, tagged `Quiet`):

- the request log line of a 5xx (the `http` row or the `panic` row covers it);
- `handler panicked`, `health check panicked`, `worker failed on saved settings` after a panic (the `panic` row);
- `backup failed` per attempt (the nightly job's final failure is the `job` row);
- `egress changed` when the VPN path goes down (an outage: the egress health check reports it).

A 500 from a handler that also logs an error (`api`, the arr views' `request failed`) makes two rows, a `log` row
with the cause and an `http` row with the route. They are kept apart on purpose: the route row counts how often
users met the failure.

## Grouping

Each event gets a fingerprint: sha256 of source, component, the normalized message and, for panics, the top
in-repo frame (the first `github.com/theatarr/theatarr` function below `panic(`). Normalization replaces URLs, quoted
text, UUIDs, paths, hex strings of six or more characters with a digit and a letter, and numbers, then collapses
whitespace, so `route failed for title 12: open /mnt/x/a.mkv` and the same failure for title 13 are one row.

A row keeps the latest sample (the message as it happened), its context (attributes) and stack, the count, first
and last seen, the build version at the first and the latest sighting, and a status: `new`, `seen`, `resolved` or
`ignored`. A resolved row that happens again is `new` again with `regressions` counted; an ignored one keeps
counting and stays ignored. A note (a fix, a cause) is kept with the row.

## Redaction and limits

Every sample, context value and stack is redacted before it is stored: the known credentials of the running
settings generation (the gateway's redactor), URLs through the credential URL masker, credential-named key/value
pairs (`apikey=`, `"token": ...`), Authorization values, and long generated-looking tokens (32+ letters and digits,
not plain hex: info hashes stay). Attributes with credential names (`logring.SecretKey`) are stored as `[redacted]`.
Caps: message 1 KB, context 8 KB, stack 16 KB.

## Writing

Events go through a bounded queue (1024) to one writer that batches (100 events or 1 s) and upserts per
fingerprint. Reporting never blocks: when the queue is full the event is dropped and counted (`drops`, shown in
the list). A write failure is counted and logged as a quiet warning at most once a minute; it never feeds back
into the inbox. At shutdown the writer drains what is queued for at most 5 s.

## Retention

- Resolved and ignored rows whose last sighting and resolution are older than the run-history window
  (`--retention-run-days`, 30 days by default) are pruned by the nightly retention job.
- At most 5000 rows: beyond that the writer removes resolved and ignored rows first, then the oldest by last
  sighting.
- `error_inbox_state` (drop count, health snapshot) is one row per key and kept.

## Surfaces (admins only)

API (`/api/theatarr/v1`, see openapi.json):

- `GET /errors?status=new|seen|resolved|ignored|open&source=&since=24h&limit=&offset=` — rows newest sighting
  first without stacks, `total`, `counts` per status, `drops`;
- `GET /errors/{id}` — one row with context and stack;
- `POST /errors/{id}` `{"status": "resolved", "note": "..."}` — status, note or both;
- `POST /errors/bulk` `{"ids": [...]}` or `{"where": {"status": "open", "source": "http"}}` with `"status"` — an
  empty filter is refused.

MCP: `errors_list`, `errors_get`, `errors_resolve` (admin; [mcp.md](../mcp.md)).

CLI (on the database directly, safe while serve runs: `docker exec theatarr theatarr errors list`):

```
theatarr errors list [-status new|seen|resolved|ignored|open|all] [-source S] [-since 24h] [-limit N] [-json]
theatarr errors show ID [-json]
theatarr errors resolve|seen|ignore|new ID... [-note TEXT] [-json]
```

UI: Admin › Errors: counts, status and source filters, the grouped list, a row's detail with context, stack and
note, Seen, Resolve, Ignore and Reopen, and Mark all seen / Resolve all for the filter. The Errors entry in the
admin nav shows the count of new rows (polled every minute).

## Debug bundle

`theatarr debug bundle -out FILE` writes a tar.gz (mode 0600) for a bug report:

| File | Holds |
|---|---|
| `build.json` | version and VCS revision, Go, OS/arch, database and log paths, schema version and target |
| `settings.yaml` | the settings review export (credentials, env names and webhook paths omitted); a note in YAML mode |
| `logs.jsonl` | the latest log lines (`-lines`, 2000) from `theatarr.log` and its rotated files |
| `errors.json` | the open (new and seen) inbox rows with stacks, and the drop count |
| `health.json` | the last health snapshot serve recorded (every change, at least every 10 minutes) |

Log lines lose credential-named attributes and go through the same text redaction; then every file is scrubbed of
the known secrets: the values of every env variable the policy names, the indexer and provider credentials of the
settings generation, and every `THEATARR_*` variable ending in `_KEY`, `_TOKEN`, `_SECRET`, `_PASSWORD`,
`_PASS` or `_PIN`. A part that cannot be read becomes a note in its file.
