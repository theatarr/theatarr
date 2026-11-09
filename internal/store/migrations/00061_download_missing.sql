-- A provider that does not find a download's torrent once is not proof that it is gone: the poller blocklists the
-- release only after consecutive not-found reads spanning a few minutes. missing_polls counts them, missing_since is
-- the first of the current run; a read that finds the torrent clears both.
-- +goose Up
ALTER TABLE download ADD COLUMN missing_polls INTEGER NOT NULL DEFAULT 0;
ALTER TABLE download ADD COLUMN missing_since INTEGER;

-- +goose Down
ALTER TABLE download DROP COLUMN missing_since;
ALTER TABLE download DROP COLUMN missing_polls;
