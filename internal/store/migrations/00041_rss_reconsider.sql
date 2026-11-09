-- Old RSS reads remembered temporary rejections as well as picks. Let the next sync decide them again.
-- Track canonical movie date refreshes separately from artwork-only metadata, and schedule their backfill.
-- +goose Up
DELETE FROM shadow_rss_seen;
ALTER TABLE title_meta ADD COLUMN movie_dates_at INTEGER;
UPDATE title_meta SET refresh_after = 0 WHERE title_id IN (SELECT id FROM title WHERE kind = 'movie');

-- +goose Down
ALTER TABLE title_meta DROP COLUMN movie_dates_at;
-- Seen feed items are an expendable cache; the old contents cannot be restored.
