-- Indexer statistics (phase N, N4; docs/design/gateway.md "Indexer requests by purpose"): besides the count, the
-- gateway now adds up per indexer, purpose and UTC day how many requests it measured (an answer or a transport error,
-- with the time it took), how many of them failed (a transport error, HTTP 400 or more, or an error the indexer
-- reported inside an HTTP 200 answer) and their total time. Rows from before this have a count and no measurement.
-- The gateway keeps 90 days now instead of 30.

-- +goose Up
ALTER TABLE gateway_indexer_request ADD COLUMN measured INTEGER NOT NULL DEFAULT 0;
ALTER TABLE gateway_indexer_request ADD COLUMN failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE gateway_indexer_request ADD COLUMN took_ms INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE gateway_indexer_request DROP COLUMN took_ms;
ALTER TABLE gateway_indexer_request DROP COLUMN failures;
ALTER TABLE gateway_indexer_request DROP COLUMN measured;
