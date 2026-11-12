-- Import lists from three sources: a TMDb movie list (as movie_import_list held), or the titles of a Radarr or Sonarr
-- instance (docs/design/arrview.md, Lists). The instance's API key is kept on the row like setting_secret (plain,
-- in the 0600 database) and never answered by an API.
-- +goose Up
CREATE TABLE import_list (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    name                 TEXT NOT NULL,
    list_type            TEXT NOT NULL DEFAULT 'tmdb' CHECK (list_type IN ('tmdb', 'radarr', 'sonarr')),
    tmdb_id              INTEGER NOT NULL DEFAULT 0,
    url                  TEXT NOT NULL DEFAULT '',
    api_key              TEXT NOT NULL DEFAULT '',
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
    CHECK ((list_type = 'tmdb') = (tmdb_id > 0)),
    CHECK ((list_type = 'tmdb') = (url = ''))
);
CREATE UNIQUE INDEX import_list_tmdb ON import_list (tmdb_id) WHERE list_type = 'tmdb';
CREATE UNIQUE INDEX import_list_url ON import_list (list_type, url) WHERE list_type <> 'tmdb';

INSERT INTO import_list (id, name, list_type, tmdb_id, enabled, enable_auto, monitor, search_on_add, versions, view,
    quality_profile_id, root_folder_path, minimum_availability, last_sync_at, created_at, updated_at)
SELECT id, name, 'tmdb', tmdb_id, enabled, enable_auto, monitor, search_on_add, versions, view,
    quality_profile_id, root_folder_path, minimum_availability, last_sync_at, created_at, updated_at
FROM movie_import_list;
DROP TABLE movie_import_list;

-- +goose Down
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
INSERT INTO movie_import_list (id, name, list_type, tmdb_id, enabled, enable_auto, monitor, search_on_add, versions,
    view, quality_profile_id, root_folder_path, minimum_availability, last_sync_at, created_at, updated_at)
SELECT id, name, list_type, tmdb_id, enabled, enable_auto, monitor, search_on_add, versions, view,
    quality_profile_id, root_folder_path, minimum_availability, last_sync_at, created_at, updated_at
FROM import_list WHERE list_type = 'tmdb';
DROP TABLE import_list;
