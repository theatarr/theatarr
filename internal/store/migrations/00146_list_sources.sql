-- More list sources and a series exclusion list (phase N3; docs/design/arrview.md, Lists). An import list gains a
-- media (movie or series): a TMDb list row imports that media's items, so a mixed TMDb list is added once per media;
-- a Radarr row is movie and a Sonarr row series. Two new kinds: tmdb_discover (a TMDb discover rule, kept as JSON in
-- rule; it has no URL and no TMDb id) and json (a public URL answering a JSON array of TMDb or IMDb ids).
-- last_error is the list's own last sync result, set and cleared by each sync.
--
-- list_type is a CHECK constraint, which SQLite cannot alter, so the table is rebuilt as 00116 built it, keeping
-- ids, views and keys. Nothing references import_list, so the rebuild runs in goose's transaction.
--
-- Exclusions: movie_exclusion gains the title and year it was excluded under (shown on the Import lists page; NULL
-- for older rows), and series_exclusion is the series list, by TVDB id, read by Sonarr lists, TMDb series lists and
-- the one-time Sonarr import.

-- +goose Up
CREATE TABLE import_list_new (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    name                 TEXT NOT NULL,
    list_type            TEXT NOT NULL DEFAULT 'tmdb'
        CHECK (list_type IN ('tmdb', 'tmdb_discover', 'json', 'radarr', 'sonarr')),
    media                TEXT NOT NULL DEFAULT 'movie' CHECK (media IN ('movie', 'series')),
    tmdb_id              INTEGER NOT NULL DEFAULT 0,
    url                  TEXT NOT NULL DEFAULT '',
    api_key              TEXT NOT NULL DEFAULT '',
    rule                 TEXT,
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
    last_error           TEXT,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    CHECK ((list_type = 'tmdb') = (tmdb_id > 0)),
    CHECK ((list_type IN ('tmdb', 'tmdb_discover')) = (url = '')),
    CHECK ((list_type = 'tmdb_discover') = (rule IS NOT NULL)),
    CHECK (list_type <> 'radarr' OR media = 'movie'),
    CHECK (list_type <> 'sonarr' OR media = 'series')
);

INSERT INTO import_list_new (id, name, list_type, media, tmdb_id, url, api_key, enabled, enable_auto, monitor,
    search_on_add, versions, view, quality_profile_id, root_folder_path, minimum_availability, last_sync_at,
    created_at, updated_at)
SELECT id, name, list_type, CASE list_type WHEN 'sonarr' THEN 'series' ELSE 'movie' END, tmdb_id, url, api_key,
    enabled, enable_auto, monitor, search_on_add, versions, view, quality_profile_id, root_folder_path,
    minimum_availability, last_sync_at, created_at, updated_at
FROM import_list;

-- The AUTOINCREMENT counter is carried over, so the id of a deleted list is not reused.
INSERT INTO sqlite_sequence (name, seq) SELECT 'import_list_new', 0
WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'import_list_new')
    AND EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'import_list');
UPDATE sqlite_sequence SET seq = MAX(seq, (SELECT seq FROM sqlite_sequence WHERE name = 'import_list'))
WHERE name = 'import_list_new' AND EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'import_list');

DROP TABLE import_list;
ALTER TABLE import_list_new RENAME TO import_list;
CREATE UNIQUE INDEX import_list_tmdb ON import_list (tmdb_id, media) WHERE list_type = 'tmdb';
CREATE UNIQUE INDEX import_list_url ON import_list (list_type, url, media) WHERE list_type IN ('radarr', 'sonarr', 'json');

ALTER TABLE movie_exclusion ADD COLUMN title TEXT;
ALTER TABLE movie_exclusion ADD COLUMN year INTEGER;

CREATE TABLE series_exclusion (
    tvdb_id    INTEGER PRIMARY KEY,
    title      TEXT,
    year       INTEGER,
    created_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE series_exclusion;
ALTER TABLE movie_exclusion DROP COLUMN year;
ALTER TABLE movie_exclusion DROP COLUMN title;

-- The rows 00116 can hold go back (TMDb movie lists, Radarr and Sonarr lists); TMDb series lists, discover rules
-- and JSON lists are dropped.
CREATE TABLE import_list_old (
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
INSERT INTO import_list_old (id, name, list_type, tmdb_id, url, api_key, enabled, enable_auto, monitor, search_on_add,
    versions, view, quality_profile_id, root_folder_path, minimum_availability, last_sync_at, created_at, updated_at)
SELECT id, name, list_type, tmdb_id, url, api_key, enabled, enable_auto, monitor, search_on_add, versions, view,
    quality_profile_id, root_folder_path, minimum_availability, last_sync_at, created_at, updated_at
FROM import_list WHERE list_type IN ('radarr', 'sonarr') OR (list_type = 'tmdb' AND media = 'movie');
INSERT INTO sqlite_sequence (name, seq) SELECT 'import_list_old', 0
WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'import_list_old')
    AND EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'import_list');
UPDATE sqlite_sequence SET seq = MAX(seq, (SELECT seq FROM sqlite_sequence WHERE name = 'import_list'))
WHERE name = 'import_list_old' AND EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'import_list');
DROP TABLE import_list;
ALTER TABLE import_list_old RENAME TO import_list;
CREATE UNIQUE INDEX import_list_tmdb ON import_list (tmdb_id) WHERE list_type = 'tmdb';
CREATE UNIQUE INDEX import_list_url ON import_list (list_type, url) WHERE list_type <> 'tmdb';
