-- Saved inventory merged into Torrents (K9): what the accounts hold is the record alone (provider_torrent, 00130),
-- which every reader of the old per-hash presence table reads since 00142. content_provider and the per-hash usage
-- (provider_hash_usage, read only by the removed Saved inventory pages) go; content and content_file stay as the
-- record of the hashes Theatarr itself uses. provider_sync_run stays: its complete runs are the marker "Unused" needs.
-- Down recreates the tables empty (the next sync fills nothing into them: the code no longer writes them).
-- +goose Up
DROP INDEX IF EXISTS content_provider_folder;
DROP INDEX IF EXISTS content_provider_provider;
DROP TABLE content_provider;
DROP TABLE provider_hash_usage;

-- +goose Down
CREATE TABLE provider_hash_usage (
    info_hash    TEXT    PRIMARY KEY,
    usage        TEXT    NOT NULL,
    linked_files INTEGER NOT NULL DEFAULT 0,
    detail       TEXT    NOT NULL DEFAULT '{}'
);
CREATE TABLE content_provider (
    content_id   INTEGER NOT NULL REFERENCES content (id) ON DELETE CASCADE,
    provider     TEXT    NOT NULL,
    torrent_id   TEXT,
    folder_name  TEXT,
    seen_at      INTEGER NOT NULL,
    state        TEXT    NOT NULL DEFAULT 'present' CHECK (state IN ('present', 'absent')),
    status       TEXT    NOT NULL DEFAULT '',
    progress     REAL,
    size_bytes   INTEGER,
    links        INTEGER,
    duplicates   INTEGER NOT NULL DEFAULT 0,
    added_at     INTEGER,
    absent_at    INTEGER,
    PRIMARY KEY (content_id, provider)
);
CREATE INDEX content_provider_provider ON content_provider (provider, state);
CREATE INDEX content_provider_folder ON content_provider (folder_name);
