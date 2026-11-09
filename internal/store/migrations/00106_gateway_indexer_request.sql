-- Indexer requests by purpose (docs/design/gateway.md "Indexer requests by purpose"): the gateway counts every request it
-- sends to an indexer per indexer key, purpose (interactive, wanted, upgrade, rss, shadow, test, other) and UTC day.
-- Accounting only; the gateway keeps 30 days and prunes older rows.

-- +goose Up
CREATE TABLE gateway_indexer_request (
    indexer       TEXT    NOT NULL,
    purpose       TEXT    NOT NULL,
    day_start_at  INTEGER NOT NULL,
    count         INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (indexer, purpose, day_start_at)
);

-- +goose Down
DROP TABLE gateway_indexer_request;
