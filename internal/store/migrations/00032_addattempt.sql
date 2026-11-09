-- The add attempt a download is in (docs/design/router.md "Crash between add and record").
--
-- download.add_attempt is a token the router writes with state adding, right before it sends a provider add, and
-- every write that settles that add (recorded, refused, adopted from the account, resumed, failed) matches it. A
-- route that lost the row to another process (which settled the add or started another) changes nothing, and an
-- answer that comes back late cannot overwrite a newer result. NULL on rows from before this migration and on rows
-- never sent; it is cleared when a refused add returns the row to queued.

-- +goose Up
ALTER TABLE download ADD COLUMN add_attempt TEXT;

-- +goose Down
ALTER TABLE download DROP COLUMN add_attempt;
