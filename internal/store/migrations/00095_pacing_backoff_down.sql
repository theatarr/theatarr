-- Whether the provider's running back-off means it is down (unreachable, 5xx, its maintenance, downloads
-- that stalled while it was down) rather than an add refused or rate-limited. Only a down back-off keeps a route from
-- reusing a torrent the provider already holds (docs/design/router.md "Pacing").
-- +goose Up
ALTER TABLE router_pacing ADD COLUMN backoff_down INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE router_pacing DROP COLUMN backoff_down;
