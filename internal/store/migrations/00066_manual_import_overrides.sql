-- Client choices survive manual import retries independently for each selected file.
-- +goose Up
ALTER TABLE download_file ADD COLUMN overrides TEXT NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE download_file DROP COLUMN overrides;
