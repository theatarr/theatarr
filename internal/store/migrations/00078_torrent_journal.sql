-- The provider torrent manager (internal/torrents, docs/design/torrents.md): one row per torrent a person deleted,
-- restored or tried to, and per backup taken. Conventions as in 00001.
--
-- by is the user name, key:<name> for an API key, or cli. backup_id names the backup file taken before a delete (in
-- <data dir>/torrent-backups). outcome is deleted, failed or not_attempted for a delete; added, present, skipped or
-- failed for a restore; ok or failed for a backup. detail is the reason in words (the references a used torrent was
-- deleted over, the provider's refusal). Kept for good: the audit trail of the only deletes of torrents Theatarr did not add.
-- +goose Up
CREATE TABLE torrent_journal (
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
CREATE INDEX torrent_journal_at ON torrent_journal (created_at);

-- +goose Down
DROP TABLE torrent_journal;
