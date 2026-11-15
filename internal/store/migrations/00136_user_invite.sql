-- Invite links (docs/design/users.md): an admin invites a person, and the invite is a web_user row in the new
-- status invited carrying the role, auto-approve, the SHA-256 of a single-use token (the token itself is shown once
-- and never stored) and the expiry. Redeeming it (a password or a Plex sign-in) makes the row an active account and
-- clears both invite columns; revoking deletes the row.
--
-- status is a CHECK constraint, which SQLite cannot alter, so the table is rebuilt. Other tables reference web_user,
-- so foreign keys are off for the rebuild, which only works outside a transaction; hence NO TRANSACTION with an
-- explicit BEGIN ... COMMIT (as 00006, 00014). The AUTOINCREMENT counter is carried over so no id is reused.

-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;

CREATE TABLE web_user_new (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    username             TEXT    NOT NULL UNIQUE,
    password_hash        TEXT    NOT NULL,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    password_changed_at  INTEGER NOT NULL,
    role                 TEXT    NOT NULL DEFAULT 'admin' CHECK (role IN ('admin', 'manager', 'user')),
    permissions          INTEGER NOT NULL DEFAULT 0,
    status               TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'pending', 'disabled', 'invited')),
    disabled_at          INTEGER,
    display_name         TEXT    NOT NULL DEFAULT '',
    email                TEXT    NOT NULL DEFAULT '',
    avatar_url           TEXT    NOT NULL DEFAULT '',
    plex_id              INTEGER,
    plex_username        TEXT    NOT NULL DEFAULT '',
    request_versions     TEXT,
    auto_approve         INTEGER NOT NULL DEFAULT 0 CHECK (auto_approve IN (0, 1)),
    last_login_at        INTEGER,
    discord_id           TEXT    NOT NULL DEFAULT '',
    invite_token_hash    TEXT,
    invite_expires_at    INTEGER
);

INSERT INTO web_user_new (id, username, password_hash, created_at, updated_at, password_changed_at, role, permissions,
    status, disabled_at, display_name, email, avatar_url, plex_id, plex_username, request_versions, auto_approve,
    last_login_at, discord_id)
SELECT id, username, password_hash, created_at, updated_at, password_changed_at, role, permissions, status,
    disabled_at, display_name, email, avatar_url, plex_id, plex_username, request_versions, auto_approve,
    last_login_at, discord_id
FROM web_user;

UPDATE sqlite_sequence SET seq = MAX(seq, (SELECT seq FROM sqlite_sequence WHERE name = 'web_user'))
WHERE name = 'web_user_new' AND EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'web_user');

DROP TABLE web_user;
ALTER TABLE web_user_new RENAME TO web_user;
CREATE UNIQUE INDEX web_user_plex ON web_user (plex_id) WHERE plex_id IS NOT NULL;
CREATE UNIQUE INDEX web_user_invite ON web_user (invite_token_hash) WHERE invite_token_hash IS NOT NULL;

COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;

DELETE FROM web_user WHERE status = 'invited';

CREATE TABLE web_user_old (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    username             TEXT    NOT NULL UNIQUE,
    password_hash        TEXT    NOT NULL,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    password_changed_at  INTEGER NOT NULL,
    role                 TEXT    NOT NULL DEFAULT 'admin' CHECK (role IN ('admin', 'manager', 'user')),
    permissions          INTEGER NOT NULL DEFAULT 0,
    status               TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'pending', 'disabled')),
    disabled_at          INTEGER,
    display_name         TEXT    NOT NULL DEFAULT '',
    email                TEXT    NOT NULL DEFAULT '',
    avatar_url           TEXT    NOT NULL DEFAULT '',
    plex_id              INTEGER,
    plex_username        TEXT    NOT NULL DEFAULT '',
    request_versions     TEXT,
    auto_approve         INTEGER NOT NULL DEFAULT 0 CHECK (auto_approve IN (0, 1)),
    last_login_at        INTEGER,
    discord_id           TEXT    NOT NULL DEFAULT ''
);

INSERT INTO web_user_old (id, username, password_hash, created_at, updated_at, password_changed_at, role, permissions,
    status, disabled_at, display_name, email, avatar_url, plex_id, plex_username, request_versions, auto_approve,
    last_login_at, discord_id)
SELECT id, username, password_hash, created_at, updated_at, password_changed_at, role, permissions, status,
    disabled_at, display_name, email, avatar_url, plex_id, plex_username, request_versions, auto_approve,
    last_login_at, discord_id
FROM web_user;

UPDATE sqlite_sequence SET seq = MAX(seq, (SELECT seq FROM sqlite_sequence WHERE name = 'web_user'))
WHERE name = 'web_user_old' AND EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'web_user');

DROP TABLE web_user;
ALTER TABLE web_user_old RENAME TO web_user;
CREATE UNIQUE INDEX web_user_plex ON web_user (plex_id) WHERE plex_id IS NOT NULL;

COMMIT;
PRAGMA foreign_keys = ON;
