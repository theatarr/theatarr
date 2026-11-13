-- "Add to other providers" (docs/design/router.md "Copies to other providers"): one row per torrent hash and provider
-- the torrent was (or is being) added to beside the provider that already holds it. A new request for the same pair
-- replaces a finished row; an unfinished one is returned as it is. state: queued, adding (written before the add is
-- sent; own_add says a successful account listing did not show the hash), checking (torrent_id stored right after the
-- add), then added, not_cached, refused, skipped or failed. message holds the plain reason. Nothing reads this table
-- but the copy code and the copies fields of the title page and the Torrents page.
-- +goose Up
CREATE TABLE provider_copy (
    id          INTEGER PRIMARY KEY,
    info_hash   TEXT    NOT NULL,
    name        TEXT    NOT NULL DEFAULT '',
    title_id    INTEGER REFERENCES title (id) ON DELETE SET NULL,
    provider    TEXT    NOT NULL,
    torrent_id  TEXT,
    own_add     INTEGER NOT NULL DEFAULT 0 CHECK (own_add IN (0, 1)),
    state       TEXT    NOT NULL CHECK (state IN ('queued', 'adding', 'checking', 'added', 'not_cached', 'refused',
                                                 'skipped', 'failed')),
    message     TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE (info_hash, provider)
);

-- +goose Down
DROP TABLE provider_copy;
