-- arr import (docs/design/library.md, docs/migration.md): what `theatarr import-arr` reads from each arr instance, the files the arrs hold per
-- version, the registry seeded from arr history, and the TMDb response cache. Conventions as in 00001.

-- +goose Up

-- An arr instance imported from, and the version its titles become (policy import.arr). root_folders is a JSON
-- array of the instance's root folder paths as last read.
CREATE TABLE arr_instance (
    name          TEXT    PRIMARY KEY,
    flavour       TEXT    NOT NULL CHECK (flavour IN ('radarr', 'sonarr')),
    policy_key    TEXT    NOT NULL,
    app_version   TEXT    NOT NULL DEFAULT '',
    root_folders  TEXT    NOT NULL DEFAULT '[]',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

-- One import run per instance: a log, so re-runs leave every other table unchanged when nothing changed upstream.
-- summary is the run's JSON summary.
CREATE TABLE import_run (
    id           INTEGER PRIMARY KEY,
    instance     TEXT    NOT NULL,
    started_at   INTEGER NOT NULL,
    finished_at  INTEGER NOT NULL,
    summary      TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX import_run_instance ON import_run (instance, started_at);

CREATE TABLE arr_tag (
    instance  TEXT    NOT NULL REFERENCES arr_instance (name) ON DELETE CASCADE,
    arr_id    INTEGER NOT NULL,
    label     TEXT    NOT NULL,
    PRIMARY KEY (instance, arr_id)
);

CREATE TABLE arr_quality_profile (
    instance             TEXT    NOT NULL REFERENCES arr_instance (name) ON DELETE CASCADE,
    arr_id               INTEGER NOT NULL,
    name                 TEXT    NOT NULL,
    cutoff               TEXT    NOT NULL DEFAULT '',
    upgrade_allowed      INTEGER NOT NULL DEFAULT 1 CHECK (upgrade_allowed IN (0, 1)),
    min_format_score     INTEGER NOT NULL DEFAULT 0,
    cutoff_format_score  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (instance, arr_id)
);

-- A movie (later a series) as one arr instance has it, tied to our title and version. tags is a JSON array of tag
-- labels. arr_file_id is the arr's current file id, 0 when it has none.
CREATE TABLE arr_title (
    instance              TEXT    NOT NULL REFERENCES arr_instance (name) ON DELETE CASCADE,
    arr_id                INTEGER NOT NULL,
    kind                  TEXT    NOT NULL CHECK (kind IN ('movie', 'series')),
    title_id              INTEGER NOT NULL REFERENCES title (id),
    version_id            INTEGER NOT NULL REFERENCES version (id),
    monitored             INTEGER NOT NULL CHECK (monitored IN (0, 1)),
    quality_profile_id    INTEGER NOT NULL DEFAULT 0,
    minimum_availability  TEXT    NOT NULL DEFAULT '',
    status                TEXT    NOT NULL DEFAULT '',
    path                  TEXT    NOT NULL DEFAULT '',
    root_folder_path      TEXT    NOT NULL DEFAULT '',
    tags                  TEXT    NOT NULL DEFAULT '[]',
    arr_file_id           INTEGER NOT NULL DEFAULT 0,
    added_at              INTEGER,
    PRIMARY KEY (instance, arr_id)
);
CREATE INDEX arr_title_title ON arr_title (title_id);
CREATE INDEX arr_title_version ON arr_title (version_id);

-- Which instance a version's state and file come from. The first instance to import a version owns it; another
-- instance mapped to the same version is compared against it and its differences are reported as conflicts.
CREATE TABLE version_source (
    version_id  INTEGER PRIMARY KEY REFERENCES version (id) ON DELETE CASCADE,
    instance    TEXT    NOT NULL,
    arr_id      INTEGER NOT NULL
);

-- The file an arr holds for a version, as the arr reports it. It becomes a Link once the library scan reads the
-- symlink target (readlink); until then the target is unknown. release_title is the grab's title from history, so
-- the score never comes from the renamed file name. languages is a JSON array of names; media_info is the arr's
-- mediaInfo object as JSON.
CREATE TABLE version_file (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    version_id           INTEGER NOT NULL REFERENCES version (id) ON DELETE CASCADE,
    title_id             INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    instance             TEXT    NOT NULL,
    arr_file_id          INTEGER NOT NULL,
    path                 TEXT    NOT NULL,
    relative_path        TEXT    NOT NULL DEFAULT '',
    size_bytes           INTEGER NOT NULL DEFAULT 0,
    quality              TEXT    NOT NULL DEFAULT '',
    quality_revision     INTEGER NOT NULL DEFAULT 1,
    release_group        TEXT    NOT NULL DEFAULT '',
    scene_name           TEXT    NOT NULL DEFAULT '',
    edition              TEXT    NOT NULL DEFAULT '',
    languages            TEXT    NOT NULL DEFAULT '[]',
    media_info           TEXT    NOT NULL DEFAULT '{}',
    custom_format_score  INTEGER,
    indexer_flags        INTEGER NOT NULL DEFAULT 0,
    original_file_path   TEXT    NOT NULL DEFAULT '',
    cutoff_not_met       INTEGER NOT NULL DEFAULT 0 CHECK (cutoff_not_met IN (0, 1)),
    added_at             INTEGER,
    download_id          TEXT,
    release_title        TEXT,
    content_id           INTEGER REFERENCES content (id),
    UNIQUE (instance, arr_file_id)
);
CREATE INDEX version_file_version ON version_file (version_id);
CREATE INDEX version_file_content ON version_file (content_id);

-- arr history, every event, sanitised: data keeps an allowlist of keys and drops anything that looks like a URL or a
-- credential (no download URLs, GUIDs or passkeys). info_hash is set for torrent downloads (lower-case hex); usenet
-- download ids are kept in download_id only. title_id and version_id are NULL when the arr title is not imported.
CREATE TABLE arr_history (
    instance             TEXT    NOT NULL REFERENCES arr_instance (name) ON DELETE CASCADE,
    arr_id               INTEGER NOT NULL,
    event_type           TEXT    NOT NULL,
    date_at              INTEGER NOT NULL,
    arr_title_id         INTEGER NOT NULL DEFAULT 0,
    arr_episode_id       INTEGER,
    title_id             INTEGER REFERENCES title (id),
    version_id           INTEGER REFERENCES version (id),
    download_id          TEXT,
    info_hash            TEXT,
    protocol             TEXT    NOT NULL DEFAULT '' CHECK (protocol IN ('', 'torrent', 'usenet')),
    source_title         TEXT    NOT NULL DEFAULT '',
    quality              TEXT    NOT NULL DEFAULT '',
    custom_format_score  INTEGER,
    languages            TEXT    NOT NULL DEFAULT '[]',
    data                 TEXT    NOT NULL DEFAULT '{}',
    PRIMARY KEY (instance, arr_id)
);
CREATE INDEX arr_history_hash ON arr_history (info_hash) WHERE info_hash IS NOT NULL;
CREATE INDEX arr_history_download ON arr_history (instance, download_id) WHERE download_id IS NOT NULL;
CREATE INDEX arr_history_title ON arr_history (title_id);

-- Registry seeding: which versions a content (info hash) was grabbed or imported for, and under which release
-- title. The *_at columns are the first such event; failed_at is the first failed download of it.
CREATE TABLE content_version (
    content_id     INTEGER NOT NULL REFERENCES content (id) ON DELETE CASCADE,
    version_id     INTEGER NOT NULL REFERENCES version (id) ON DELETE CASCADE,
    release_title  TEXT    NOT NULL DEFAULT '',
    source         TEXT    NOT NULL,
    grabbed_at     INTEGER,
    imported_at    INTEGER,
    failed_at      INTEGER,
    PRIMARY KEY (content_id, version_id)
);
CREATE INDEX content_version_version ON content_version (version_id);

-- Release dates per title: from the arr (country '') or TMDb (per country). type: theatrical_limited, theatrical,
-- premiere, digital, physical, tv.
CREATE TABLE title_release_date (
    title_id       INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    source         TEXT    NOT NULL,
    country        TEXT    NOT NULL DEFAULT '',
    type           TEXT    NOT NULL CHECK (type IN ('premiere', 'theatrical_limited', 'theatrical', 'digital',
                                                    'physical', 'tv')),
    date           TEXT    NOT NULL,
    certification  TEXT    NOT NULL DEFAULT '',
    note           TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (title_id, source, country, type, date)
);

-- Metadata API responses (docs/design/tv-metadata.md): keyed by source and request path with query (never the API key),
-- body gzip-compressed. status is the HTTP status (a 404 is cached too, briefly).
CREATE TABLE metadata_cache (
    source      TEXT    NOT NULL,
    key         TEXT    NOT NULL,
    status      INTEGER NOT NULL,
    body        BLOB    NOT NULL,
    fetched_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    PRIMARY KEY (source, key)
);

-- +goose Down
DROP TABLE metadata_cache;
DROP TABLE title_release_date;
DROP TABLE content_version;
DROP TABLE arr_history;
DROP TABLE version_file;
DROP TABLE version_source;
DROP TABLE arr_title;
DROP TABLE arr_quality_profile;
DROP TABLE arr_tag;
DROP TABLE import_run;
DROP TABLE arr_instance;
