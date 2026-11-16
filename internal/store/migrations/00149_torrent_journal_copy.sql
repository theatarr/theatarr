-- "Add to other providers" (internal/torrents copy.go, docs/design/torrents.md): the torrent journal takes action
-- copy, one row per provider a copy tried. outcome for a copy: sending (while its add is out), added, present,
-- not_cached, refused, skipped or failed.
--
-- action is a CHECK constraint, which SQLite cannot alter, so the table is rebuilt as 00078 built it, keeping every
-- row and id. Nothing references torrent_journal, so the rebuild runs in goose's transaction.

-- +goose Up
CREATE TABLE torrent_journal_new (
    id          INTEGER PRIMARY KEY,
    action      TEXT    NOT NULL CHECK (action IN ('delete', 'restore', 'backup', 'copy')),
    provider    TEXT    NOT NULL DEFAULT '',
    torrent_id  TEXT    NOT NULL DEFAULT '',
    info_hash   TEXT    NOT NULL DEFAULT '',
    name        TEXT    NOT NULL DEFAULT '',
    size_bytes  INTEGER,
    backup_id   TEXT    NOT NULL DEFAULT '',
    outcome     TEXT    NOT NULL,
    detail      TEXT    NOT NULL DEFAULT '',
    by          TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);
INSERT INTO torrent_journal_new (id, action, provider, torrent_id, info_hash, name, size_bytes, backup_id, outcome,
    detail, by, created_at)
SELECT id, action, provider, torrent_id, info_hash, name, size_bytes, backup_id, outcome, detail, by, created_at
FROM torrent_journal;
DROP TABLE torrent_journal;
ALTER TABLE torrent_journal_new RENAME TO torrent_journal;
CREATE INDEX torrent_journal_at ON torrent_journal (created_at);

-- +goose Down
CREATE TABLE torrent_journal_old (
    id          INTEGER PRIMARY KEY,
    action      TEXT    NOT NULL CHECK (action IN ('delete', 'restore', 'backup')),
    provider    TEXT    NOT NULL DEFAULT '',
    torrent_id  TEXT    NOT NULL DEFAULT '',
    info_hash   TEXT    NOT NULL DEFAULT '',
    name        TEXT    NOT NULL DEFAULT '',
    size_bytes  INTEGER,
    backup_id   TEXT    NOT NULL DEFAULT '',
    outcome     TEXT    NOT NULL,
    detail      TEXT    NOT NULL DEFAULT '',
    by          TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);
INSERT INTO torrent_journal_old (id, action, provider, torrent_id, info_hash, name, size_bytes, backup_id, outcome,
    detail, by, created_at)
SELECT id, action, provider, torrent_id, info_hash, name, size_bytes, backup_id, outcome, detail, by, created_at
FROM torrent_journal WHERE action <> 'copy';
DROP TABLE torrent_journal;
ALTER TABLE torrent_journal_old RENAME TO torrent_journal;
CREATE INDEX torrent_journal_at ON torrent_journal (created_at);
