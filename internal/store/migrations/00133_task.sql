-- The scheduled tasks (docs/design/jobs.md "Scheduled tasks"; internal/tasks): what a recurring task keeps across a
-- restart, as Sonarr's System › Tasks. A run updates its row; it inserts no job row. Times are ms. last_error is the
-- newest run's error ('' when it worked); last_ok is when the newest run that worked ended; next_run is when the
-- task's loop planned its next run. A task's name and schedule live in the process that registers it.
-- The job rows the recurring runs left behind go, queued ones too (no handler leases them any more): the whole kinds
-- that now run only as tasks, and the sweep runs ({} payload, or a scheduled backup) of kinds that also run for one
-- subject. Before they go, the newest done run of each such kind becomes its task's last good run (so a failed
-- one-title job a later sweep resolved stays resolved), and a queued nightly backup's due time becomes the nightly
-- task's next run (so an overdue one still runs at the first start).
-- job_finished serves the job list's finished rows, newest first.
-- +goose Up
CREATE TABLE task (
    key           TEXT    PRIMARY KEY,
    last_started  INTEGER,
    last_finished INTEGER,
    last_error    TEXT    NOT NULL DEFAULT '',
    last_ok       INTEGER,
    next_run      INTEGER
) STRICT;

CREATE TEMP VIEW recurring_job AS SELECT * FROM job WHERE
    kind IN ('acquire.wanted', 'shadow.rss', 'db.nightly', 'db.retention', 'notify.digest', 'meta.scene')
    OR (kind IN ('provider.sync', 'meta.art', 'meta.series') AND payload = '{}')
    OR (kind = 'torrents.backup' AND json_extract(payload, '$.reason') = 'scheduled');

INSERT INTO task (key, last_finished, last_ok)
SELECT kind, MAX(finished_at), MAX(finished_at) FROM recurring_job WHERE state = 'done' AND finished_at IS NOT NULL
GROUP BY kind;

INSERT INTO task (key, next_run)
SELECT 'db.nightly', MIN(run_at) FROM job WHERE kind = 'db.nightly' AND state IN ('queued', 'running') HAVING COUNT(*) > 0
ON CONFLICT (key) DO UPDATE SET next_run = excluded.next_run;

DELETE FROM job WHERE id IN (SELECT id FROM recurring_job);

DROP VIEW recurring_job;

CREATE INDEX job_finished ON job (finished_at, id) WHERE state IN ('done', 'failed');

-- +goose Down
DROP INDEX job_finished;
DROP TABLE task;
