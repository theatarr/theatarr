-- Version settings: version policy and arr-view version bindings. View credentials and identity remain YAML-owned.
-- +goose Up
CREATE TABLE setting_version (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    key        TEXT NOT NULL UNIQUE,
    config     TEXT NOT NULL CHECK (json_valid(config)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE setting_view_binding (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    key        TEXT NOT NULL UNIQUE,
    config     TEXT NOT NULL CHECK (json_valid(config)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE settings_e3_bootstrap (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    generation  INTEGER NOT NULL REFERENCES settings_generation (generation)
);

-- +goose Down
DROP TABLE settings_e3_bootstrap;
DROP TABLE setting_view_binding;
DROP TABLE setting_version;
