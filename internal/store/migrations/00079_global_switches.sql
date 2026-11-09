-- +goose Up
CREATE TABLE setting_global_switches (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT NOT NULL UNIQUE CHECK (key = 'global'),
    config TEXT NOT NULL CHECK (json_valid(config)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE settings_switches_bootstrap (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    generation INTEGER NOT NULL REFERENCES settings_generation (generation)
);

-- +goose Down
DROP TABLE settings_switches_bootstrap;
DROP TABLE setting_global_switches;
