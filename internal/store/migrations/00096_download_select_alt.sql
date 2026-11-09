-- The Real-Debrid torrent of a cached-only route's second add (altRetry) whose selection is the movie's
-- largest video alone. The intent lives with the torrent, not only in the route, so the poller and a restart select
-- that set when the route's own selection failed, never the set Real-Debrid just answered uncached for. A new add
-- has a new torrent id, so the mark ends with the add it names (docs/design/router.md "One second selection").
-- +goose Up
ALTER TABLE download ADD COLUMN select_alt_torrent TEXT;

-- +goose Down
ALTER TABLE download DROP COLUMN select_alt_torrent;
