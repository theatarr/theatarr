-- The settings provider epoch (settings_runtime_state.provider_epoch, 00057) a download's provider torrent id was
-- recorded under. A DB-managed router reuses a torrent id from our own downloads only under the current epoch: after
-- an account change the old account's ids are not reused. NULL: recorded by a YAML router or before this column, so
-- a DB-managed router never reuses it (the registry, which provider sync fences the same way, still can).
-- +goose Up
ALTER TABLE download ADD COLUMN provider_epoch INTEGER;

-- +goose Down
ALTER TABLE download DROP COLUMN provider_epoch;
