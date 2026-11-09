-- Durable rename events: the common library writer records the successful path change in its transaction.
-- +goose Up
ALTER TABLE version ADD COLUMN add_method TEXT NOT NULL DEFAULT 'manual';
CREATE TABLE notify_rename (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    title_id INTEGER NOT NULL,
    version_key TEXT NOT NULL,
    link_id INTEGER NOT NULL,
    previous_path TEXT NOT NULL,
    path TEXT NOT NULL,
    quality TEXT NOT NULL,
    quality_version INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE notify_rename;
ALTER TABLE version DROP COLUMN add_method;
