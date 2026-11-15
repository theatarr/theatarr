-- Retention deletes provider_cache rows a day after they expire (internal/retention); the rows already past that
-- go here once.
-- +goose Up
DELETE FROM provider_cache WHERE expires_at IS NOT NULL
    AND expires_at < (CAST(strftime('%s', 'now') AS INTEGER) - 86400) * 1000;

-- +goose Down
