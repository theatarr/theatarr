-- The publish date of the feed head a shadow_rss_cursor records (NULL when the feed gave none). RSS catch-up stops at
-- a page holding an item older than it, as the arrs do with the last release's publish date, so a head the indexer
-- removed does not send catch-up to the page cap.
-- +goose Up
ALTER TABLE shadow_rss_cursor ADD COLUMN published_at INTEGER;

-- +goose Down
ALTER TABLE shadow_rss_cursor DROP COLUMN published_at;
