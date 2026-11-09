-- Web auth (docs/design/web.md): the signing secret, user accounts, login sessions and per-client API keys.
-- Conventions as in 00001. Nothing secret is stored in clear: passwords are argon2id hashes, session tokens and API
-- keys are SHA-256 hashes of high-entropy random values. The signing secret is the one exception: it signs CSRF
-- tokens and must be readable, so the database file itself is the secret (mode 0600 in the container).

-- +goose Up

-- Instance settings the web layer keeps in the database. name 'signing_secret' holds 32 random bytes, hex.
CREATE TABLE web_setting (
    name        TEXT    PRIMARY KEY,
    value       TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- A login. password_hash is a PHC string ($argon2id$v=19$m=...,t=...,p=...$salt$hash).
CREATE TABLE web_user (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    username             TEXT    NOT NULL UNIQUE,
    password_hash        TEXT    NOT NULL,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    password_changed_at  INTEGER NOT NULL
);

-- A browser session. token_hash is SHA-256 of the cookie value, hex. A session ends at expires_at (absolute) or when
-- it has been idle past the idle timeout (last_seen_at); a password reset deletes every session of the user.
CREATE TABLE web_session (
    token_hash    TEXT    PRIMARY KEY,
    user_id       INTEGER NOT NULL REFERENCES web_user (id) ON DELETE CASCADE,
    created_at    INTEGER NOT NULL,
    last_seen_at  INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    remote        TEXT    NOT NULL DEFAULT '',
    user_agent    TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX web_session_user ON web_session (user_id);
CREATE INDEX web_session_expires ON web_session (expires_at);

-- A per-client API key, sent as X-Api-Key. key_hash is SHA-256 of the key, hex; prefix is its first characters, kept
-- so a list can tell keys apart. A revoked key keeps its row (revoked_at set) for the record.
CREATE TABLE api_key (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL,
    prefix        TEXT    NOT NULL,
    key_hash      TEXT    NOT NULL UNIQUE,
    created_by    INTEGER REFERENCES web_user (id) ON DELETE SET NULL,
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER,
    revoked_at    INTEGER
);
CREATE UNIQUE INDEX api_key_name_active ON api_key (name) WHERE revoked_at IS NULL;

-- +goose Down
DROP TABLE api_key;
DROP TABLE web_session;
DROP TABLE web_user;
DROP TABLE web_setting;
