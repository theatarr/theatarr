-- +goose Up
ALTER TABLE download ADD COLUMN dismissed_at INTEGER;

-- +goose Down
ALTER TABLE download DROP COLUMN dismissed_at;
