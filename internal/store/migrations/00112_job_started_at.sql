-- When a job's current (or last) run was claimed by a worker: the Tasks page shows it as a running task's start.
-- updated_at cannot say it, since every lease renewal moves it. NULL for a job never claimed, or claimed before
-- this column existed.

-- +goose Up
ALTER TABLE job ADD COLUMN started_at INTEGER;

-- +goose Down
ALTER TABLE job DROP COLUMN started_at;
