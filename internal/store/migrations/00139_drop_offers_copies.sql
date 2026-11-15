-- Provider copies and the offer retry are gone (phase K, K7).
--
-- "Add to other providers" (provider_copy, 00128) goes. Its History events (provider_copy_event, 00129) stay,
-- read-only, until History is stored on its own. A copy still unfinished is drained here, before its recovery code
-- goes: one past its add (a torrent id on record) gets the event of an added copy, saying it was ended by the upgrade
-- (its torrent stays on the account, an ordinary torrent under Torrents); a queued one ends with nothing. The two
-- finished copies on the owner's install already have their events. router.copy_to_providers leaves the stored router
-- section.
--
-- The offer machine (router_offer, 00038/00064/00069: retries, planned offers, the six-hour sweep) goes. A download
-- imported for one library now tries the title's other libraries once, in the same import (docs/design/router.md
-- "Other libraries at import"); nothing is remembered. Queued offer and copy jobs go with them. Down recreates both
-- tables empty.
-- +goose Up
INSERT INTO provider_copy_event (at, info_hash, name, title_id, provider, state, message, quality, score)
SELECT c.updated_at, c.info_hash, c.name, c.title_id, c.provider, 'added',
    'Added to ' || upper(c.provider) || '; the copy was ended by an upgrade before it settled, its torrent stays on the account',
    NULL, NULL
FROM provider_copy c
WHERE c.state IN ('adding', 'checking') AND COALESCE(c.torrent_id, '') <> '';

DROP TABLE provider_copy;
DROP TABLE router_offer;

DELETE FROM job WHERE kind IN ('router.offer', 'router.copy') AND state IN ('queued', 'running');

UPDATE setting_section SET config = json_remove(config, '$.copy_to_providers')
WHERE key = 'router' AND json_type(config, '$.copy_to_providers') IS NOT NULL;

-- +goose Down
CREATE TABLE router_offer (
    version_key    TEXT    NOT NULL,
    title_id       INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    info_hash      TEXT    NOT NULL,
    from_version   TEXT    NOT NULL,
    release_title  TEXT    NOT NULL DEFAULT '',
    outcome        TEXT    NOT NULL CHECK (outcome IN ('pending', 'routed', 'planned', 'declined', 'same', 'gave_up')),
    reasons        TEXT    NOT NULL DEFAULT '[]',
    attempts       INTEGER NOT NULL DEFAULT 0,
    first_at       INTEGER NOT NULL,
    next_at        INTEGER,
    updated_at     INTEGER NOT NULL,
    source_kind    TEXT    NOT NULL DEFAULT 'download',
    source_id      INTEGER NOT NULL DEFAULT 0,
    retry_attempts INTEGER NOT NULL DEFAULT 0,
    target_state   TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (version_key, title_id, info_hash)
);
CREATE INDEX router_offer_due ON router_offer (next_at) WHERE outcome IN ('pending', 'gave_up');
CREATE INDEX router_offer_source ON router_offer (source_kind, source_id);
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
    until_at    INTEGER,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE (info_hash, provider)
);
