-- Runtime publication state and recoverable settings-owned snapshots.
-- +goose Up
ALTER TABLE settings_generation ADD COLUMN snapshot TEXT NOT NULL DEFAULT '{}';

CREATE TABLE settings_runtime_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    last_published_generation INTEGER NOT NULL,
    previous_published_generation INTEGER
);

-- +goose Down
DROP TABLE settings_runtime_state;
ALTER TABLE settings_generation DROP COLUMN snapshot;
