-- Upgrade approval was removed (docs/design/router.md "Upgrades"); pending holds end with it: the next search or RSS
-- read routes a better release automatically.
-- +goose Up
DROP TABLE upgrade_hold;

-- +goose Down
CREATE TABLE upgrade_hold (
    id               INTEGER PRIMARY KEY,
    title_id         INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    version_id       INTEGER NOT NULL REFERENCES version (id) ON DELETE CASCADE,
    version_key      TEXT    NOT NULL,
    episodes         TEXT    NOT NULL DEFAULT '',
    info_hash        TEXT    NOT NULL,
    release_title    TEXT    NOT NULL,
    indexer          TEXT    NOT NULL DEFAULT '',
    quality          TEXT    NOT NULL DEFAULT '',
    score            INTEGER NOT NULL DEFAULT 0,
    quality_rank     INTEGER NOT NULL DEFAULT 0,
    reason           TEXT    NOT NULL DEFAULT '',
    current_quality  TEXT    NOT NULL DEFAULT '',
    current_score    INTEGER,
    current_at       INTEGER NOT NULL DEFAULT 0,
    replaces         TEXT    NOT NULL DEFAULT '[]',
    candidate        TEXT    NOT NULL,
    source           TEXT    NOT NULL DEFAULT '',
    policy_rev       TEXT    NOT NULL DEFAULT '',
    state            TEXT    NOT NULL DEFAULT 'pending'
                     CHECK (state IN ('pending', 'approved', 'declined', 'expired', 'replaced')),
    created_at       INTEGER NOT NULL,
    seen_at          INTEGER NOT NULL,
    decided_at       INTEGER,
    decided_by       INTEGER REFERENCES web_user (id) ON DELETE SET NULL,
    decided_via      TEXT    NOT NULL DEFAULT '',
    note             TEXT    NOT NULL DEFAULT '',
    download_id      INTEGER REFERENCES download (id) ON DELETE SET NULL,
    route_outcome    TEXT    NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX upgrade_hold_pending ON upgrade_hold (version_id, title_id, episodes) WHERE state = 'pending';
CREATE INDEX upgrade_hold_hash ON upgrade_hold (version_id, title_id, info_hash, state);
CREATE INDEX upgrade_hold_state ON upgrade_hold (state, created_at);
CREATE INDEX upgrade_hold_download ON upgrade_hold (download_id);
