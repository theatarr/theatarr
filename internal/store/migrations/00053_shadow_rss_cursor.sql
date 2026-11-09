-- +goose Up
CREATE TABLE shadow_rss_cursor (
    indexer TEXT PRIMARY KEY,
    item_key TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE shadow_rss_cursor;
