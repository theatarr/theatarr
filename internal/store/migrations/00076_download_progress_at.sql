-- When a download's provider progress last rose: router.downloading_timeout fails a download in
-- downloading whose progress has not moved for that long. NULL until the poller first sees it downloading.
-- +goose Up
ALTER TABLE download ADD COLUMN progress_at INTEGER;

-- +goose Down
ALTER TABLE download DROP COLUMN progress_at;
