-- "Edit file facts" (internal/library filefacts.go, docs/design/library.md "Correcting an installed file"): the
-- operator actions take action edit_file, a manager's correction of an installed file's quality, languages and
-- release group.
--
-- action is a CHECK constraint, which SQLite cannot alter, so the table is rebuilt as 00039 built it, keeping every
-- row and id. Nothing references journal_action, so the rebuild runs in goose's transaction.

-- +goose Up
CREATE TABLE journal_action_new (
    id             INTEGER PRIMARY KEY,
    action         TEXT    NOT NULL CHECK (action IN ('grab', 'remove_download', 'retry_download', 'unblock',
                                                      'remove_version', 'remove_title', 'unlink_file', 'edit_file')),
    title_id       INTEGER REFERENCES title (id) ON DELETE SET NULL,
    version_key    TEXT    NOT NULL DEFAULT '',
    info_hash      TEXT    NOT NULL DEFAULT '',
    release_title  TEXT    NOT NULL DEFAULT '',
    download_id    INTEGER,
    target         TEXT    NOT NULL DEFAULT '',
    detail         TEXT    NOT NULL DEFAULT '',
    by             TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL
);
INSERT INTO journal_action_new (id, action, title_id, version_key, info_hash, release_title, download_id, target,
    detail, by, created_at)
SELECT id, action, title_id, version_key, info_hash, release_title, download_id, target, detail, by, created_at
FROM journal_action;
DROP TABLE journal_action;
ALTER TABLE journal_action_new RENAME TO journal_action;
CREATE INDEX journal_action_at ON journal_action (created_at);
CREATE INDEX journal_action_title ON journal_action (title_id, version_key, created_at);

-- +goose Down
CREATE TABLE journal_action_old (
    id             INTEGER PRIMARY KEY,
    action         TEXT    NOT NULL CHECK (action IN ('grab', 'remove_download', 'retry_download', 'unblock',
                                                      'remove_version', 'remove_title', 'unlink_file')),
    title_id       INTEGER REFERENCES title (id) ON DELETE SET NULL,
    version_key    TEXT    NOT NULL DEFAULT '',
    info_hash      TEXT    NOT NULL DEFAULT '',
    release_title  TEXT    NOT NULL DEFAULT '',
    download_id    INTEGER,
    target         TEXT    NOT NULL DEFAULT '',
    detail         TEXT    NOT NULL DEFAULT '',
    by             TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL
);
INSERT INTO journal_action_old (id, action, title_id, version_key, info_hash, release_title, download_id, target,
    detail, by, created_at)
SELECT id, action, title_id, version_key, info_hash, release_title, download_id, target, detail, by, created_at
FROM journal_action WHERE action <> 'edit_file';
DROP TABLE journal_action;
ALTER TABLE journal_action_old RENAME TO journal_action;
CREATE INDEX journal_action_at ON journal_action (created_at);
CREATE INDEX journal_action_title ON journal_action (title_id, version_key, created_at);
