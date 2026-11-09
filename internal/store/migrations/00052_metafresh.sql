-- Metadata freshness and add-time monitoring (docs/design/tv-metadata.md).
-- +goose Up
ALTER TABLE series_monitor ADD COLUMN pending_monitor TEXT NOT NULL DEFAULT '';
CREATE TABLE scene_refresh (
    source TEXT PRIMARY KEY,
    refreshed_at INTEGER NOT NULL,
    data BLOB NOT NULL
);
-- Re-evaluate movies scheduled with the former three-day cadence.
UPDATE title_meta SET refresh_after = MIN(refresh_after, refreshed_at + 43200000)
WHERE title_id IN (SELECT id FROM title WHERE kind = 'movie') AND refreshed_at IS NOT NULL;

-- +goose Down
DROP TABLE scene_refresh;
ALTER TABLE series_monitor DROP COLUMN pending_monitor;
