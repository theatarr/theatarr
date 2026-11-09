-- TVDB v4 client (docs/design/tvdb.md): the /updates watermark. Responses themselves go to metadata_cache (source tvdb).

-- +goose Up

-- One row: where the next GET /updates?since= starts (unix ms), and what the last run did.
CREATE TABLE tvdb_sync (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    since_at     INTEGER NOT NULL,
    run_at       INTEGER NOT NULL,
    records      INTEGER NOT NULL DEFAULT 0,
    invalidated  INTEGER NOT NULL DEFAULT 0,
    truncated    INTEGER NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE tvdb_sync;
