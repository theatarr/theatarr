-- Upgrade approval (docs/design/router.md "Upgrade approval"): a release that would replace a file of a version with
-- versions[*].upgrades: approve is held here instead of being added. One pending row per version, title and episode
-- set (episodes: the sorted episode ids joined by commas, '' for a movie); a better release replaces it (state
-- replaced). candidate is the router candidate as JSON (no links: indexer.Release never marshals them), so approval
-- routes exactly what was held. current_* describe the file it would replace when it was held; current_at is that
-- file's time, so a newer file expires the hold. decided_by is the account that approved or declined it (decided_via
-- the API key's name, or 'session'), decided_at when. A declined hash is never held again for the version and title.
-- +goose Up
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

-- +goose Down
DROP TABLE upgrade_hold;
