-- An unfinished cached-only check (docs/design/router.md "Cached-only on Real-Debrid and AllDebrid"). With
-- router.cached_only, the router reads an add on Real-Debrid or AllDebrid back to tell cached from uncached; when that
-- read gave no verdict, or the verdict was uncached but the delete of our own add was blocked (the stop switch, the
-- job lease) or failed, download.cached_check keeps the check open for the poller: 'pending' (no verdict yet) or
-- 'uncached' (verdict uncached, the delete still owed). NULL: done or not applicable. It counts only while the
-- download is in added or downloading.
-- download.selected_at is when the router's file selection on Real-Debrid was recorded (files_selected became 1):
-- the poller's verdict waits router.cached_grace after the selection (Real-Debrid) or the add.

-- +goose Up
ALTER TABLE download ADD COLUMN cached_check TEXT CHECK (cached_check IN ('pending', 'uncached'));
ALTER TABLE download ADD COLUMN selected_at INTEGER;

-- +goose Down
ALTER TABLE download DROP COLUMN selected_at;
ALTER TABLE download DROP COLUMN cached_check;
