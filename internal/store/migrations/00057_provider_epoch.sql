-- Provider account changes fence in-flight routes independently of settings generations.
-- +goose Up
ALTER TABLE settings_runtime_state ADD COLUMN provider_epoch INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE settings_runtime_state DROP COLUMN provider_epoch;
