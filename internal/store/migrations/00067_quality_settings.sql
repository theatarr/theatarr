-- Quality settings: profiles, formats, format sets and per-version naming.
-- +goose Up
CREATE TABLE setting_quality_profile (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT NOT NULL UNIQUE,
    config TEXT NOT NULL CHECK (json_valid(config)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE setting_custom_format (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT NOT NULL UNIQUE,
    config TEXT NOT NULL CHECK (json_valid(config)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE setting_format_set (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT NOT NULL UNIQUE,
    config TEXT NOT NULL CHECK (json_valid(config)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE setting_version_naming (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT NOT NULL UNIQUE,
    config TEXT NOT NULL CHECK (json_valid(config)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE settings_quality_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    initialized_at INTEGER NOT NULL,
    source TEXT NOT NULL,
    sections TEXT NOT NULL CHECK (json_valid(sections))
);

CREATE TABLE setting_quality_sync (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT NOT NULL UNIQUE,
    config TEXT NOT NULL CHECK (json_valid(config)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE settings_quality_review (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_key TEXT NOT NULL,
    base_generation INTEGER NOT NULL,
    commit_id TEXT NOT NULL,
    preview TEXT NOT NULL CHECK (json_valid(preview)),
    candidate TEXT NOT NULL CHECK (json_valid(candidate)),
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'applied', 'superseded')),
    created_at INTEGER NOT NULL,
    applied_at INTEGER
);
CREATE INDEX settings_quality_review_pending ON settings_quality_review (state, source_key);
CREATE TABLE settings_quality_sync_run (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_key TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    commit_id TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL,
    review_id INTEGER REFERENCES settings_quality_review(id),
    error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX settings_quality_sync_run_source ON settings_quality_sync_run (source_key, started_at);

-- +goose Down
DROP TABLE settings_quality_sync_run;
DROP TABLE settings_quality_review;
DROP TABLE setting_quality_sync;
DROP TABLE settings_quality_state;
DROP TABLE setting_version_naming;
DROP TABLE setting_format_set;
DROP TABLE setting_custom_format;
DROP TABLE setting_quality_profile;
