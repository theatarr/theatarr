-- Grabbing fixes.
--
-- acquire_run.requested: 1 when the caller named the run's version, 0 when a title search widened to it (another
-- active version of the same title; docs/design/router.md "The acquirer"). The router keeps its freshness check for a
-- first-pass route of a run with requested = 0, as it does for automatic searches.
--
-- router_refusal_451: the hashes Real-Debrid refused with 451 in the last holdWindow (docs/design/router.md "Pacing").
-- A 451 blocks its hash on that provider; only 451s on different hashes within the window hold the account.
--
-- download.poll_tried_at, poll_errors, poll_error_since: the poller's last status read attempt (answered or not), the
-- failed reads in a row and when they began. The pass reads least recently tried first and backs failing reads off;
-- polled_at stays the last successful observation (docs/design/router.md "Polling").
--
-- acquire_run.infra: 1 for a search that failed on infrastructure (the VPN guard refused it, or no indexer answered).
-- It is not counted as searched: again_after, add-time intents and the re-search limit ignore it.
-- +goose Up
ALTER TABLE acquire_run ADD COLUMN requested INTEGER NOT NULL DEFAULT 1;
ALTER TABLE acquire_run ADD COLUMN infra INTEGER NOT NULL DEFAULT 0;
CREATE TABLE router_refusal_451 (
    provider    TEXT    NOT NULL,
    info_hash   TEXT    NOT NULL,
    refused_at  INTEGER NOT NULL,
    PRIMARY KEY (provider, info_hash)
);
ALTER TABLE download ADD COLUMN poll_tried_at INTEGER;
ALTER TABLE download ADD COLUMN poll_errors INTEGER NOT NULL DEFAULT 0;
ALTER TABLE download ADD COLUMN poll_error_since INTEGER;

-- +goose Down
ALTER TABLE download DROP COLUMN poll_error_since;
ALTER TABLE download DROP COLUMN poll_errors;
ALTER TABLE download DROP COLUMN poll_tried_at;
DROP TABLE router_refusal_451;
ALTER TABLE acquire_run DROP COLUMN infra;
ALTER TABLE acquire_run DROP COLUMN requested;
