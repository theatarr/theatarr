-- Claims and resumable states for crash consistency. Conventions as in 00001.
--
-- acquire_run.claim: the job lease (job id/attempt) that owns a run in state searching. A retry of the run's job takes
-- the claim over and searches again; a run finishes (and enqueues its route) only under the claim it holds, in one
-- transaction, so a run left searching by a crash or a shutdown is resumed and never routed twice.
--
-- library_claim: one import at a time per download. The periodic importer and a manual import claim the download
-- before planning or writing anything, and stop when another holds it. until is when an abandoned claim (a crash)
-- may be taken over.
--
-- library_import.state gains 'partial': some links of the import were made and others failed. The download stays
-- importing and is retried (the links made are found in place and kept); it is imported once every link is made.

-- +goose Up
ALTER TABLE acquire_run ADD COLUMN claim TEXT;

CREATE TABLE library_claim (
    download_id INTEGER PRIMARY KEY REFERENCES download (id) ON DELETE CASCADE,
    owner       TEXT    NOT NULL,
    until       INTEGER NOT NULL
);

CREATE TABLE library_import_new (
    download_id    INTEGER PRIMARY KEY REFERENCES download (id) ON DELETE CASCADE,
    state          TEXT    NOT NULL CHECK (state IN ('waiting_mount', 'linked', 'partial', 'rejected', 'dry_run', 'failed')),
    mount_folder   TEXT,
    waiting_since  INTEGER,
    alerted_at     INTEGER,
    attempts       INTEGER NOT NULL DEFAULT 0,
    last_error     TEXT,
    plan           TEXT    NOT NULL DEFAULT '{}',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);
INSERT INTO library_import_new SELECT download_id, state, mount_folder, waiting_since, alerted_at, attempts, last_error,
    plan, created_at, updated_at FROM library_import;
DROP TABLE library_import;
ALTER TABLE library_import_new RENAME TO library_import;
CREATE INDEX library_import_state ON library_import (state);

-- +goose Down
CREATE TABLE library_import_old (
    download_id    INTEGER PRIMARY KEY REFERENCES download (id) ON DELETE CASCADE,
    state          TEXT    NOT NULL CHECK (state IN ('waiting_mount', 'linked', 'rejected', 'dry_run', 'failed')),
    mount_folder   TEXT,
    waiting_since  INTEGER,
    alerted_at     INTEGER,
    attempts       INTEGER NOT NULL DEFAULT 0,
    last_error     TEXT,
    plan           TEXT    NOT NULL DEFAULT '{}',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);
INSERT INTO library_import_old SELECT download_id, CASE state WHEN 'partial' THEN 'linked' ELSE state END,
    mount_folder, waiting_since, alerted_at, attempts, last_error, plan, created_at, updated_at FROM library_import;
DROP TABLE library_import;
ALTER TABLE library_import_old RENAME TO library_import;
CREATE INDEX library_import_state ON library_import (state);

DROP TABLE library_claim;
ALTER TABLE acquire_run DROP COLUMN claim;
