-- Radarr collection and import-list intake (collections and TMDb lists).
-- +goose Up
CREATE TABLE movie_collection (
    tmdb_id              INTEGER PRIMARY KEY,
    name                 TEXT NOT NULL DEFAULT '',
    movies               TEXT NOT NULL DEFAULT '[]',
    monitored            INTEGER NOT NULL DEFAULT 0 CHECK (monitored IN (0, 1)),
    versions             TEXT NOT NULL DEFAULT '[]',
    view                 TEXT NOT NULL DEFAULT '',
    quality_profile_id   INTEGER NOT NULL DEFAULT 0,
    root_folder_path     TEXT NOT NULL DEFAULT '',
    minimum_availability TEXT NOT NULL DEFAULT 'released',
    search_on_add        INTEGER NOT NULL DEFAULT 0 CHECK (search_on_add IN (0, 1)),
    refreshed_at         INTEGER,
    updated_at           INTEGER NOT NULL
);

CREATE TABLE movie_import_list (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    name                 TEXT NOT NULL,
    list_type            TEXT NOT NULL DEFAULT 'tmdb',
    tmdb_id              INTEGER NOT NULL,
    enabled              INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    enable_auto          INTEGER NOT NULL DEFAULT 1 CHECK (enable_auto IN (0, 1)),
    monitor              TEXT NOT NULL DEFAULT 'movieOnly',
    search_on_add        INTEGER NOT NULL DEFAULT 0 CHECK (search_on_add IN (0, 1)),
    versions             TEXT NOT NULL DEFAULT '[]',
    view                 TEXT NOT NULL DEFAULT '',
    quality_profile_id   INTEGER NOT NULL DEFAULT 0,
    root_folder_path     TEXT NOT NULL DEFAULT '',
    minimum_availability TEXT NOT NULL DEFAULT 'released',
    last_sync_at         INTEGER,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    UNIQUE (list_type, tmdb_id)
);

CREATE TABLE movie_exclusion (
    tmdb_id    INTEGER PRIMARY KEY,
    created_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE movie_exclusion;
DROP TABLE movie_import_list;
DROP TABLE movie_collection;
