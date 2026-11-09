-- Users, roles and requests (docs/design/ui.md §3, §8; docs/design/users.md). Conventions as in 00001.
--
-- web_user grows a role (a preset of permission bits), extra permission bits, a status, Plex sign-in fields and the
-- per-user request settings. Existing accounts stay admins (the column default). Plex accounts have no password: their
-- password_hash is '' and the local login refuses them. A Plex account's token is never stored (docs/design/users.md).
--
-- request is a user's request for a title and some of its versions; request_user holds the extra requesters of a
-- merged request (the first requester is request.user_id).

-- +goose Up

-- role: admin, manager or user. permissions: extra bits on top of the role (internal/auth Perm). status: active,
-- pending (a new Plex account waiting for an admin) or disabled. request_versions: JSON version keys this user's
-- requests ask for; NULL is every version that fits the title. auto_approve: requests apply at once.
ALTER TABLE web_user ADD COLUMN role TEXT NOT NULL DEFAULT 'admin' CHECK (role IN ('admin', 'manager', 'user'));
ALTER TABLE web_user ADD COLUMN permissions INTEGER NOT NULL DEFAULT 0;
ALTER TABLE web_user ADD COLUMN status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'pending', 'disabled'));
ALTER TABLE web_user ADD COLUMN disabled_at INTEGER;
ALTER TABLE web_user ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE web_user ADD COLUMN email TEXT NOT NULL DEFAULT '';
ALTER TABLE web_user ADD COLUMN avatar_url TEXT NOT NULL DEFAULT '';
ALTER TABLE web_user ADD COLUMN plex_id INTEGER;
ALTER TABLE web_user ADD COLUMN plex_username TEXT NOT NULL DEFAULT '';
ALTER TABLE web_user ADD COLUMN request_versions TEXT;
ALTER TABLE web_user ADD COLUMN auto_approve INTEGER NOT NULL DEFAULT 0 CHECK (auto_approve IN (0, 1));
ALTER TABLE web_user ADD COLUMN last_login_at INTEGER;
CREATE UNIQUE INDEX web_user_plex ON web_user (plex_id) WHERE plex_id IS NOT NULL;

-- API keys belong to their creator, who now is their owner; names are unique per owner among active keys.
DROP INDEX api_key_name_active;
CREATE UNIQUE INDEX api_key_owner_name_active ON api_key (COALESCE(created_by, 0), name) WHERE revoked_at IS NULL;
CREATE INDEX api_key_owner ON api_key (created_by);

-- A request. title_id is NULL until approved (or when the title was already in the library, its id from the start).
-- poster_path is the TMDb poster path captured at request time, so a request shows art before the title exists.
-- versions: JSON version keys; seasons: JSON season numbers (series; [] is every season).
CREATE TABLE request (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER REFERENCES web_user (id) ON DELETE SET NULL,
    kind         TEXT    NOT NULL CHECK (kind IN ('movie', 'series')),
    tmdb_id      INTEGER,
    tvdb_id      INTEGER,
    title_id     INTEGER REFERENCES title (id) ON DELETE SET NULL,
    name         TEXT    NOT NULL,
    year         INTEGER,
    anime        INTEGER NOT NULL DEFAULT 0 CHECK (anime IN (0, 1)),
    poster_path  TEXT    NOT NULL DEFAULT '',
    versions     TEXT    NOT NULL DEFAULT '[]',
    seasons      TEXT    NOT NULL DEFAULT '[]',
    note         TEXT    NOT NULL DEFAULT '',
    status       TEXT    NOT NULL CHECK (status IN ('pending', 'approved', 'declined', 'canceled', 'processing',
                                                    'available', 'failed')),
    decided_by   INTEGER REFERENCES web_user (id) ON DELETE SET NULL,
    decided_at   INTEGER,
    reason       TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE INDEX request_status ON request (status);
CREATE INDEX request_by_user ON request (user_id);
CREATE INDEX request_tmdb ON request (kind, tmdb_id);
CREATE INDEX request_title ON request (title_id);

-- Extra requesters of a merged request, with the versions and note each asked for.
CREATE TABLE request_user (
    request_id  INTEGER NOT NULL REFERENCES request (id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL REFERENCES web_user (id) ON DELETE CASCADE,
    versions    TEXT    NOT NULL DEFAULT '[]',
    note        TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (request_id, user_id)
);
CREATE INDEX request_user_user ON request_user (user_id);

-- +goose Down
DROP TABLE request_user;
DROP TABLE request;
DROP INDEX api_key_owner;
DROP INDEX api_key_owner_name_active;
CREATE UNIQUE INDEX api_key_name_active ON api_key (name) WHERE revoked_at IS NULL;
DROP INDEX web_user_plex;
ALTER TABLE web_user DROP COLUMN last_login_at;
ALTER TABLE web_user DROP COLUMN auto_approve;
ALTER TABLE web_user DROP COLUMN request_versions;
ALTER TABLE web_user DROP COLUMN plex_username;
ALTER TABLE web_user DROP COLUMN plex_id;
ALTER TABLE web_user DROP COLUMN avatar_url;
ALTER TABLE web_user DROP COLUMN email;
ALTER TABLE web_user DROP COLUMN display_name;
ALTER TABLE web_user DROP COLUMN disabled_at;
ALTER TABLE web_user DROP COLUMN status;
ALTER TABLE web_user DROP COLUMN permissions;
ALTER TABLE web_user DROP COLUMN role;
