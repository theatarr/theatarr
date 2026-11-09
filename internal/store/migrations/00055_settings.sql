-- Source settings (indexers, providers, budgets). Secret values are plain text; only the settings credential
-- adapter reads them.
-- +goose Up
CREATE TABLE settings_generation (
    generation INTEGER PRIMARY KEY,
    revision   TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE setting_secret (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    value      TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE setting_indexer (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    key        TEXT NOT NULL UNIQUE,
    kind       TEXT NOT NULL,
    enabled    INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    priority   INTEGER NOT NULL CHECK (priority >= 0),
    secret_id  INTEGER UNIQUE REFERENCES setting_secret (id),
    config     TEXT NOT NULL CHECK (json_valid(config)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE setting_provider (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    key        TEXT NOT NULL UNIQUE,
    kind       TEXT NOT NULL,
    enabled    INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    order_rank INTEGER NOT NULL CHECK (order_rank > 0),
    pacing     TEXT NOT NULL CHECK (json_valid(pacing)),
    secret_id  INTEGER UNIQUE REFERENCES setting_secret (id),
    config     TEXT NOT NULL CHECK (json_valid(config)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE setting_budget (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    key        TEXT NOT NULL UNIQUE,
    config     TEXT NOT NULL CHECK (json_valid(config)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE settings_audit (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    actor      TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    generation INTEGER NOT NULL REFERENCES settings_generation (generation),
    entity     TEXT NOT NULL,
    key        TEXT NOT NULL,
    operation  TEXT NOT NULL,
    before     TEXT NOT NULL CHECK (json_valid(before)),
    after      TEXT NOT NULL CHECK (json_valid(after))
);
CREATE INDEX settings_audit_generation ON settings_audit (generation, id);

-- +goose Down
DROP TABLE settings_audit;
DROP TABLE setting_budget;
DROP TABLE setting_provider;
DROP TABLE setting_indexer;
DROP TABLE setting_secret;
DROP TABLE settings_generation;
