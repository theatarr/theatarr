-- A cardigann indexer's site session (cookie jar, FlareSolverr user agent, login state), so a restart does not sign
-- in again. The value holds credentials: it is kept like setting_secret (plain text in the 0600 database) and never
-- returned by an API.
-- +goose Up
CREATE TABLE cardigann_session (
    indexer    TEXT PRIMARY KEY,
    value      TEXT NOT NULL CHECK (json_valid(value)),
    updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE cardigann_session;
