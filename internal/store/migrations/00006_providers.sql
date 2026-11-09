-- Provider registry sync (docs/design/library.md): content states from the provider torrent lists, provider presence per
-- content, and one sync run row per provider. Conventions as in 00001. See docs/design/providers.md.
--
-- content.state gains 'absent' and 'unreferenced'. SQLite cannot change a CHECK in place, so the table is rebuilt
-- (sqlite.org/lang_altertable.html "Making Other Kinds Of Table Schema Changes"). That needs foreign keys off, which
-- only works outside a transaction: with them on, DROP TABLE content would cascade into content_provider,
-- content_file and content_version. Hence NO TRANSACTION, with an explicit BEGIN ... COMMIT around the rebuild.
-- The writer pool has exactly one connection, so the PRAGMAs and the transaction share it.

-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;

-- state:
--   unknown       seeded (from arr history) but not every configured provider has been listed yet;
--   present       on at least one provider and referenced (history, an arr file or a link);
--   unreferenced  on at least one provider, referenced by nothing: a candidate for the by-hand prune report;
--   absent        on no provider, and every configured provider has been listed completely;
--   gone          marked gone on purpose (trumped or nuked). Sync leaves it unless a provider has it.
CREATE TABLE content_new (
    id             INTEGER PRIMARY KEY,
    info_hash      TEXT    NOT NULL UNIQUE,
    name           TEXT    NOT NULL DEFAULT '',
    mount_folder   TEXT,
    size_bytes     INTEGER,
    state          TEXT    NOT NULL DEFAULT 'present'
                           CHECK (state IN ('present', 'absent', 'unreferenced', 'gone', 'unknown')),
    first_seen_at  INTEGER NOT NULL,
    last_seen_at   INTEGER NOT NULL
);
INSERT INTO content_new (id, info_hash, name, mount_folder, size_bytes, state, first_seen_at, last_seen_at)
    SELECT id, info_hash, name, mount_folder, size_bytes, state, first_seen_at, last_seen_at FROM content;
DROP TABLE content;
ALTER TABLE content_new RENAME TO content;
CREATE INDEX content_state ON content (state);

-- Presence per provider. torrent_id is the provider's id of the torrent kept for this hash (a provider may hold the
-- same hash more than once; duplicates counts the others). state 'absent' keeps the row after the torrent left the
-- provider's list, with absent_at. status is the provider's raw status, progress 0-100, links the number of
-- download links the provider reports (never the links themselves). seen_at is when this presence started.
ALTER TABLE content_provider ADD COLUMN state TEXT NOT NULL DEFAULT 'present' CHECK (state IN ('present', 'absent'));
ALTER TABLE content_provider ADD COLUMN status TEXT NOT NULL DEFAULT '';
ALTER TABLE content_provider ADD COLUMN progress REAL;
ALTER TABLE content_provider ADD COLUMN size_bytes INTEGER;
ALTER TABLE content_provider ADD COLUMN links INTEGER;
ALTER TABLE content_provider ADD COLUMN duplicates INTEGER NOT NULL DEFAULT 0;
ALTER TABLE content_provider ADD COLUMN added_at INTEGER;
ALTER TABLE content_provider ADD COLUMN absent_at INTEGER;
CREATE INDEX content_provider_provider ON content_provider (provider, state);

-- One row per provider per sync: a log, like import_run. ok is 1 when the provider's list was read completely; only
-- such runs count towards 'absent'. summary is the run's JSON summary; error is set when ok is 0.
CREATE TABLE provider_sync_run (
    id           INTEGER PRIMARY KEY,
    provider     TEXT    NOT NULL,
    started_at   INTEGER NOT NULL,
    finished_at  INTEGER NOT NULL,
    ok           INTEGER NOT NULL CHECK (ok IN (0, 1)),
    error        TEXT,
    summary      TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX provider_sync_run_provider ON provider_sync_run (provider, ok, finished_at);

COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;
DROP TABLE provider_sync_run;
DROP INDEX content_provider_provider;
ALTER TABLE content_provider DROP COLUMN absent_at;
ALTER TABLE content_provider DROP COLUMN added_at;
ALTER TABLE content_provider DROP COLUMN duplicates;
ALTER TABLE content_provider DROP COLUMN links;
ALTER TABLE content_provider DROP COLUMN size_bytes;
ALTER TABLE content_provider DROP COLUMN progress;
ALTER TABLE content_provider DROP COLUMN status;
ALTER TABLE content_provider DROP COLUMN state;
CREATE TABLE content_old (
    id             INTEGER PRIMARY KEY,
    info_hash      TEXT    NOT NULL UNIQUE,
    name           TEXT    NOT NULL DEFAULT '',
    mount_folder   TEXT,
    size_bytes     INTEGER,
    state          TEXT    NOT NULL DEFAULT 'present' CHECK (state IN ('present', 'gone', 'unknown')),
    first_seen_at  INTEGER NOT NULL,
    last_seen_at   INTEGER NOT NULL
);
INSERT INTO content_old (id, info_hash, name, mount_folder, size_bytes, state, first_seen_at, last_seen_at)
    SELECT id, info_hash, name, mount_folder, size_bytes,
           CASE state WHEN 'unreferenced' THEN 'present' WHEN 'absent' THEN 'unknown' ELSE state END,
           first_seen_at, last_seen_at FROM content;
DROP TABLE content;
ALTER TABLE content_old RENAME TO content;
COMMIT;
PRAGMA foreign_keys = ON;
