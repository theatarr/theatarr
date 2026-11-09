-- The error inbox (docs/design/errors.md): unexpected failures while Theatarr runs, one row per fingerprint (source,
-- component, normalized message and, for a panic, the top in-repo stack frame). count and the first and last
-- sighting grow in place; a resolved row that recurs goes back to new (regressions counts how often). sample,
-- context and stack hold the latest sighting, redacted and size-capped. Retention prunes resolved and ignored rows
-- after the runs window; the writer caps the table at 5000 rows. error_inbox_state holds per-key state overwritten in
-- place: the dropped-event count and the latest health snapshot for theatarr debug bundle.
-- +goose Up
CREATE TABLE error_event (
    id            INTEGER PRIMARY KEY,
    fingerprint   TEXT NOT NULL UNIQUE,
    source        TEXT NOT NULL CHECK (source IN ('log', 'panic', 'http', 'job', 'health')),
    component     TEXT NOT NULL DEFAULT '',
    level         TEXT NOT NULL DEFAULT 'error',
    message       TEXT NOT NULL,
    sample        TEXT NOT NULL DEFAULT '',
    context       TEXT NOT NULL DEFAULT '{}',
    stack         TEXT NOT NULL DEFAULT '',
    count         INTEGER NOT NULL DEFAULT 1,
    first_seen    INTEGER NOT NULL,
    last_seen     INTEGER NOT NULL,
    status        TEXT NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'seen', 'resolved', 'ignored')),
    resolved_at   INTEGER,
    regressions   INTEGER NOT NULL DEFAULT 0,
    first_version TEXT NOT NULL DEFAULT '',
    last_version  TEXT NOT NULL DEFAULT '',
    note          TEXT NOT NULL DEFAULT '',
    updated_at    INTEGER NOT NULL
);
CREATE INDEX error_event_status_last ON error_event (status, last_seen);
CREATE INDEX error_event_last ON error_event (last_seen);

CREATE TABLE error_inbox_state (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE error_inbox_state;
DROP TABLE error_event;
