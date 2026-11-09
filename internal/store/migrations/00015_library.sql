-- Library (docs/design/library.md): imports of ready downloads into the version roots, relinks, the trash of replaced
-- links, and the naming configs read from the arrs. The router owns download (00014); the library reads it and sets
-- imported or failed, and keeps its own progress here. Conventions as in 00001.
-- +goose Up

-- The library's side of one download's import: waiting for the mount, done, rejected by the sample guard, or only
-- planned (dry run while the library is disabled or the version is in shadow mode).
CREATE TABLE library_import (
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
CREATE INDEX library_import_state ON library_import (state);

-- Every library action, applied or not. mode: applied (written), dry_run (what would be written), refused (the gate
-- said no), failed. kind: link (a new link), replace (an old link moved aside by an upgrade), relink (a dangling link
-- repointed), dead (a dangling link that could not be fixed), reject (sample guard), alert (mount wait timeout),
-- scan (a Plex partial scan), purge (a trashed link removed after keep_days).
CREATE TABLE library_action (
    id           INTEGER PRIMARY KEY,
    at           INTEGER NOT NULL,
    kind         TEXT    NOT NULL CHECK (kind IN ('link', 'replace', 'relink', 'dead', 'reject', 'alert', 'scan', 'purge')),
    mode         TEXT    NOT NULL CHECK (mode IN ('applied', 'dry_run', 'refused', 'failed')),
    version_key  TEXT,
    download_id  INTEGER,
    link_id      INTEGER,
    path         TEXT,
    target       TEXT,
    detail       TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX library_action_download ON library_action (download_id);
CREATE INDEX library_action_at ON library_action (at);

-- Links moved aside: an upgrade's old link, or a link an import replaced. The symlink is recreated under the trash
-- directory before the original goes; the target (debrid content) is never touched. purged_at is set when the trash
-- copy is removed after keep_days.
CREATE TABLE link_trash (
    id          INTEGER PRIMARY KEY,
    link_id     INTEGER REFERENCES link (id) ON DELETE SET NULL,
    version_key TEXT    NOT NULL,
    path        TEXT    NOT NULL,
    trash_path  TEXT    NOT NULL UNIQUE,
    target      TEXT    NOT NULL,
    reason      TEXT    NOT NULL,
    moved_at    INTEGER NOT NULL,
    purged_at   INTEGER
);
CREATE INDEX link_trash_moved ON link_trash (moved_at) WHERE purged_at IS NULL;

-- Naming read from an arr instance (versions[].naming.from_arr): the GET config/naming body and the quality
-- definition titles, so names keep working while the arr is down.
CREATE TABLE library_naming (
    instance    TEXT    PRIMARY KEY,
    naming      TEXT    NOT NULL,
    qualities   TEXT    NOT NULL DEFAULT '{}',
    fetched_at  INTEGER NOT NULL
);

-- +goose Down
DROP TABLE library_naming;
DROP TABLE link_trash;
DROP TABLE library_action;
DROP TABLE library_import;
