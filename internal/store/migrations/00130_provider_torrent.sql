-- The record of what each debrid account holds (docs/design/torrents.md "The record"): one row per torrent of an
-- account, as the provider sync's last successful complete listing of that account gave it. The sync replaces a
-- provider's rows in one transaction; a failed listing leaves them and their time alone. Two torrents of one hash on
-- one account are two rows (content_provider keeps one per hash). A delete from the Torrents page removes its row;
-- a changed or removed account loses its rows and read time.
-- info_hash is the provider's hash as listed (lower case, may be empty or not v1); original_name the torrent's own
-- name when the listing gives it (TorBox's files), else ''; files the selected file count, -1 when the listing has
-- none; links the provider's download links, -1 when it does not say; added_at in ms, NULL when unknown.
-- The usage the screens show is worked out once after each sync (and Refresh) and stored with the rows: usage
-- (used, unused, unknown; '' not worked out yet), linked_files (links into it), detail (JSON: the shown file count,
-- reference counts, the first references, titles, reasons, added by Theatarr, parsed title, quality, title key),
-- usage_at. provider_hash_usage is the same answer per hash (references united across the hash's torrents).
-- provider_torrent_read holds when that listing ended (read_at, ms) and the reasons every unreferenced torrent's
-- usage was unknown then (unknown, JSON): a provider with no row was never read, which is not an empty account.
-- Nothing else is changed: content_provider and content.state stay as they are.
-- +goose Up
CREATE TABLE provider_torrent (
    provider      TEXT    NOT NULL,
    torrent_id    TEXT    NOT NULL,
    info_hash     TEXT    NOT NULL DEFAULT '',
    name          TEXT    NOT NULL DEFAULT '',
    original_name TEXT    NOT NULL DEFAULT '',
    size_bytes    INTEGER NOT NULL DEFAULT 0,
    status        TEXT    NOT NULL DEFAULT '',
    state         TEXT    NOT NULL DEFAULT '',
    progress      REAL    NOT NULL DEFAULT 0,
    added_at      INTEGER,
    files         INTEGER NOT NULL DEFAULT -1,
    links         INTEGER NOT NULL DEFAULT -1,
    usage         TEXT    NOT NULL DEFAULT '',
    linked_files  INTEGER NOT NULL DEFAULT 0,
    detail        TEXT    NOT NULL DEFAULT '{}',
    usage_at      INTEGER,
    PRIMARY KEY (provider, torrent_id)
);
CREATE INDEX provider_torrent_hash ON provider_torrent (info_hash);

CREATE TABLE provider_torrent_read (
    provider TEXT    PRIMARY KEY,
    read_at  INTEGER NOT NULL,
    unknown  TEXT    NOT NULL DEFAULT '[]'
);

CREATE TABLE provider_hash_usage (
    info_hash    TEXT    PRIMARY KEY,
    usage        TEXT    NOT NULL,
    linked_files INTEGER NOT NULL DEFAULT 0,
    detail       TEXT    NOT NULL DEFAULT '{}'
);

-- +goose Down
DROP TABLE provider_hash_usage;
DROP TABLE provider_torrent_read;
DROP TABLE provider_torrent;
