-- The scheduled tasks (docs/design/jobs.md "Scheduled tasks"; internal/tasks): one row per recurring task, as
-- Sonarr's System › Tasks. A run updates its row; it inserts no job row. every_s is the interval in seconds (0 for a
-- daily task at a time of day), at that time ('03:00') when there is one. Times are ms; last_error is '' after a run
-- that worked. next_run is when the task's loop runs it next, NULL while it is not scheduled.
-- The job rows the recurring runs left behind go, queued ones too (no handler leases them any more): the whole kinds
-- that now run only as tasks, and the sweep runs ({} payload, or a scheduled backup) of kinds that also run for one
-- subject.
-- +goose Up
CREATE TABLE task (
    key              TEXT    PRIMARY KEY,
    name             TEXT    NOT NULL,
    every_s          INTEGER NOT NULL DEFAULT 0,
    at               TEXT    NOT NULL DEFAULT '',
    last_started     INTEGER,
    last_finished    INTEGER,
    last_duration_ms INTEGER,
    last_error       TEXT    NOT NULL DEFAULT '',
    next_run         INTEGER,
    updated_at       INTEGER NOT NULL
) STRICT;

DELETE FROM job WHERE (
    kind IN ('acquire.wanted', 'shadow.rss', 'db.nightly', 'db.retention', 'notify.digest', 'meta.scene')
    OR (kind IN ('provider.sync', 'meta.art', 'meta.series') AND payload = '{}')
    OR (kind = 'torrents.backup' AND json_extract(payload, '$.reason') = 'scheduled'));

-- +goose Down
DROP TABLE task;
