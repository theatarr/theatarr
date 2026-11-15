-- The journal kept slim (phase K, K8; docs/journal.md "Flags"). A History row stores its journal entry's flags,
-- worked out once by the journal after the row is written (internal/journal flag.go): flags is the JSON list
-- [{code, severity, reason}] ('[]' when none fired; NULL until the journal has checked the row) and severity the
-- highest one ('' when none). The flagged view and its count read history_flagged. The checks are Go (release-name
-- parsers, title similarity, size medians, the import plan), so existing rows are left NULL here and the journal's
-- start pass flags them once.
--
-- Gone: verdicts (journal_verdict, Ok/Wrong), the error inbox (error_event, error_inbox_state; errors are in Logs),
-- the provider copy events (provider_copy_event; their History rows stay in history as copy:<id>), and the error
-- alerts' state (notify_state errors_since, notify_event error:/error_held: claims).

-- +goose Up
ALTER TABLE history ADD COLUMN flags TEXT;
ALTER TABLE history ADD COLUMN severity TEXT NOT NULL DEFAULT '';
CREATE INDEX history_flagged ON history (at, ref) WHERE severity IN ('warn', 'error');
CREATE INDEX history_unchecked ON history (id) WHERE flags IS NULL;

DROP TABLE journal_verdict;
DROP TABLE error_inbox_state;
DROP TABLE error_event;
DROP TABLE provider_copy_event;
DELETE FROM notify_state WHERE key = 'errors_since';
DELETE FROM notify_event WHERE kind LIKE 'error:%' OR kind LIKE 'error_held:%';

-- +goose Down
CREATE TABLE provider_copy_event (
    id         INTEGER PRIMARY KEY,
    at         INTEGER NOT NULL,
    info_hash  TEXT    NOT NULL,
    name       TEXT    NOT NULL DEFAULT '',
    title_id   INTEGER REFERENCES title (id) ON DELETE SET NULL,
    provider   TEXT    NOT NULL,
    state      TEXT    NOT NULL CHECK (state IN ('added', 'already', 'not_cached')),
    message    TEXT    NOT NULL DEFAULT '',
    quality    TEXT,
    score      INTEGER
);
CREATE INDEX provider_copy_event_at ON provider_copy_event (at);
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
CREATE TABLE journal_verdict (
    id            INTEGER PRIMARY KEY,
    entry_id      TEXT    NOT NULL,
    verdict       TEXT    NOT NULL CHECK (verdict IN ('ok', 'wrong')),
    reason        TEXT    NOT NULL,
    acked         TEXT    NOT NULL DEFAULT '[]',
    title_id      INTEGER REFERENCES title (id) ON DELETE SET NULL,
    version_key   TEXT    NOT NULL DEFAULT '',
    info_hash     TEXT    NOT NULL DEFAULT '',
    action        TEXT    NOT NULL CHECK (action IN ('recorded', 'blocklisted')),
    action_note   TEXT    NOT NULL DEFAULT '',
    research_job  INTEGER,
    by            TEXT    NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL
);
CREATE INDEX journal_verdict_entry ON journal_verdict (entry_id, created_at);
CREATE INDEX journal_verdict_at ON journal_verdict (created_at);
DROP INDEX history_unchecked;
DROP INDEX history_flagged;
ALTER TABLE history DROP COLUMN severity;
ALTER TABLE history DROP COLUMN flags;
