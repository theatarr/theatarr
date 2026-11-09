-- A tracked (GitHub branch) quality sync source skips a run when the branch's newest commit and the source's config
-- are those of its last successful run; the digest records the config each run used.
-- +goose Up
ALTER TABLE settings_quality_sync_run ADD COLUMN config_digest TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE settings_quality_sync_run DROP COLUMN config_digest;
