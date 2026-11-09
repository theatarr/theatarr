-- The arr views (docs/design/arrview.md): Radarr- and Sonarr-shaped APIs over our titles and versions (internal/arrview).
-- What an arr client writes that the domain model has no place for: tags, the profile ids a view lists, the
-- view-level record of a title (root folder, minimum availability, series type, ...), versions a view removed, and
-- commands. Conventions as in 00001. See docs/design/arrview.md.

-- +goose Up

-- Tags (arr TagResource). Ids are global: the same tag in every view. Labels are unique ignoring case and stored
-- lower-case, as Radarr's TagService.Add does.
CREATE TABLE arrview_tag (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    label       TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    created_at  INTEGER NOT NULL
);

-- Tags on a title, whichever view set them.
CREATE TABLE arrview_title_tag (
    title_id  INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    tag_id    INTEGER NOT NULL REFERENCES arrview_tag (id) ON DELETE CASCADE,
    PRIMARY KEY (title_id, tag_id)
);
CREATE INDEX arrview_title_tag_tag ON arrview_title_tag (tag_id);

-- The quality profile ids a view lists (a view's "profiles" are version sets named in policy). An id is given the
-- first time a name is seen and kept, so a client that stored it (Seerr's activeProfileId) keeps working when the
-- policy gains or loses other profiles.
CREATE TABLE arrview_profile (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    view        TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    UNIQUE (view, name)
);

-- A title as one view's client last wrote it: the fields of the arr resource our model does not hold.
-- minimum_availability is Radarr's (tba, announced, inCinemas, released); series_type and monitor_new_items are
-- Sonarr's. quality_profile_id is an arrview_profile id, 0 when none was given.
CREATE TABLE arrview_title (
    view                  TEXT    NOT NULL,
    title_id              INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    quality_profile_id    INTEGER NOT NULL DEFAULT 0,
    root_folder_path      TEXT    NOT NULL DEFAULT '',
    minimum_availability  TEXT    NOT NULL DEFAULT '',
    season_folder         INTEGER NOT NULL DEFAULT 1 CHECK (season_folder IN (0, 1)),
    series_type           TEXT    NOT NULL DEFAULT '',
    monitor_new_items     TEXT    NOT NULL DEFAULT '',
    added_at              INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL,
    PRIMARY KEY (view, title_id)
);
CREATE INDEX arrview_title_title ON arrview_title (title_id);

-- A version a view's DELETE removed. The row is kept (files, history and arr records hang off it) and unmonitored;
-- every view hides it until an add or a PUT turns it on again, which deletes this row.
CREATE TABLE arrview_removed_version (
    version_id  INTEGER PRIMARY KEY REFERENCES version (id) ON DELETE CASCADE,
    view        TEXT    NOT NULL,
    removed_at  INTEGER NOT NULL
);

-- A command a client posted (arr CommandResource). body is the request JSON as sent. Ids are global. While every
-- version a command touches is in shadow mode it does nothing and is stored completed, with a message saying so.
CREATE TABLE arrview_command (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    view        TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    body        TEXT    NOT NULL DEFAULT '{}',
    status      TEXT    NOT NULL CHECK (status IN ('queued', 'started', 'completed', 'failed')),
    result      TEXT    NOT NULL DEFAULT 'unknown' CHECK (result IN ('unknown', 'successful', 'unsuccessful')),
    message     TEXT    NOT NULL DEFAULT '',
    user_agent  TEXT    NOT NULL DEFAULT '',
    queued_at   INTEGER NOT NULL,
    started_at  INTEGER,
    ended_at    INTEGER
);
CREATE INDEX arrview_command_queued ON arrview_command (queued_at);

-- +goose Down
DROP TABLE arrview_command;
DROP TABLE arrview_removed_version;
DROP TABLE arrview_title;
DROP TABLE arrview_profile;
DROP TABLE arrview_title_tag;
DROP TABLE arrview_tag;
