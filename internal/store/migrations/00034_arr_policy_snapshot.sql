-- Keep the complete decision inputs for an offline policy export. Older partial profile rows cannot prove parity.
-- +goose Up
CREATE TABLE arr_policy_snapshot (
    instance    TEXT PRIMARY KEY REFERENCES arr_instance (name) ON DELETE CASCADE,
    body        TEXT NOT NULL,
    fetched_at  INTEGER NOT NULL
);
-- +goose Down
DROP TABLE arr_policy_snapshot;
