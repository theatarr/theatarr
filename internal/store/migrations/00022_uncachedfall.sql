-- Cached-only fallback, stall timeout and re-search (docs/design/router.md "Cached-only on Real-Debrid and AllDebrid",
-- "Stalled downloads", "Re-search after a failure").
--
-- download.own_add is 1 when the router proved, at the add, that the entry is a new one of ours (the hash was not on
-- the account and the id is new), 0 when it could not, NULL when it did not look (reused, TorBox, older rows). Only
-- an own_add download on Real-Debrid may have its torrent deleted when it stalls.
-- router_blocklist.researched_at is when the router asked the acquirer to search the title again after the failure
-- behind the row; existing rows count as done. acquire_run.research is the failure a re-search run follows (NULL
-- for every other run); such runs have source wanted and count against router.wanted.per_hour.

-- +goose Up
ALTER TABLE download ADD COLUMN own_add INTEGER CHECK (own_add IN (0, 1));
ALTER TABLE router_blocklist ADD COLUMN researched_at INTEGER;
UPDATE router_blocklist SET researched_at = created_at;
ALTER TABLE acquire_run ADD COLUMN research TEXT;

-- +goose Down
ALTER TABLE acquire_run DROP COLUMN research;
ALTER TABLE router_blocklist DROP COLUMN researched_at;
ALTER TABLE download DROP COLUMN own_add;
