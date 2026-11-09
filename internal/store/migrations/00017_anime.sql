-- Anime metadata engine (docs/design/anime.md: Franchise/Line/Entry/MappingDecision/Order, pipeline, caching and review).
-- A franchise is built by internal/metadata/anime; each build replaces the franchise's rows unless the refresh is
-- held for review (it would drop more than 10% of a series' episodes, or move a mapping with linked files).

-- +goose Up

-- One franchise: its seed and scope, and the last applied build (gzip JSON of the whole result, stages included).
-- pending_result holds a build that was held for review; it is applied when its review items are accepted.
CREATE TABLE anime_franchise (
    slug            TEXT    PRIMARY KEY,
    name            TEXT    NOT NULL,
    seed_anilist    INTEGER NOT NULL,
    scope           TEXT    NOT NULL DEFAULT '',
    config          TEXT    NOT NULL DEFAULT '{}',
    source          TEXT    NOT NULL DEFAULT '' CHECK (source IN ('', 'cache', 'live')),
    result          BLOB,
    built_at        INTEGER,
    pending_result  BLOB,
    pending_source  TEXT    NOT NULL DEFAULT '',
    pending_at      INTEGER,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

-- The stored output of each pipeline stage of the last applied build (crawl, joins, mappings, flags, orders), JSON.
CREATE TABLE anime_stage (
    franchise  TEXT    NOT NULL REFERENCES anime_franchise (slug) ON DELETE CASCADE,
    stage      TEXT    NOT NULL CHECK (stage IN ('crawl', 'joins', 'mappings', 'flags', 'orders')),
    body       TEXT    NOT NULL,
    PRIMARY KEY (franchise, stage)
);

-- The franchise graph: every AniList work the crawl met, as a member (line, branch, side, leaf) or excluded.
CREATE TABLE anime_node (
    franchise   TEXT    NOT NULL REFERENCES anime_franchise (slug) ON DELETE CASCADE,
    anilist_id  INTEGER NOT NULL,
    kind        TEXT    NOT NULL CHECK (kind IN ('line', 'branch', 'side', 'leaf', 'excluded')),
    title       TEXT    NOT NULL DEFAULT '',
    format      TEXT    NOT NULL DEFAULT '',
    why         TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (franchise, anilist_id)
);

-- Entries: keys al:<id>[#n], adb:<id>, kitsu:<id>, tvdb:<series>:S00Exx. body is the entry's JSON.
CREATE TABLE anime_entry (
    franchise    TEXT    NOT NULL REFERENCES anime_franchise (slug) ON DELETE CASCADE,
    key          TEXT    NOT NULL,
    origin       TEXT    NOT NULL CHECK (origin IN ('anilist', 'anidb', 'kitsu', 'tvdb')),
    kind         TEXT    NOT NULL DEFAULT '',
    title        TEXT    NOT NULL DEFAULT '',
    format       TEXT    NOT NULL DEFAULT '',
    start_date   TEXT,
    anilist_id   INTEGER,
    anidb_id     INTEGER,
    kitsu_id     INTEGER,
    flags        TEXT    NOT NULL DEFAULT '[]',
    target       TEXT    NOT NULL DEFAULT '',
    mainline     INTEGER NOT NULL DEFAULT 0 CHECK (mainline IN (0, 1)),
    tmdb_movie   TEXT    NOT NULL DEFAULT '[]',
    body         TEXT    NOT NULL,
    PRIMARY KEY (franchise, key)
);
CREATE INDEX anime_entry_anilist ON anime_entry (anilist_id) WHERE anilist_id IS NOT NULL;

-- Mapping decisions: the TVDB range an entry maps to, how it was decided, and the
-- candidates with their scores. A conflict gets no automatic import.
CREATE TABLE anime_mapping (
    franchise    TEXT    NOT NULL REFERENCES anime_franchise (slug) ON DELETE CASCADE,
    entry_key    TEXT    NOT NULL,
    status       TEXT    NOT NULL CHECK (status IN ('auto', 'arbitrated', 'conflict', 'override', 'heuristic', 'rejected')),
    winner       TEXT    NOT NULL DEFAULT '',
    tvdb_series  INTEGER,
    episodes     TEXT    NOT NULL DEFAULT '[]',
    reason       TEXT    NOT NULL DEFAULT '',
    candidates   TEXT    NOT NULL DEFAULT '[]',
    updated_at   INTEGER NOT NULL,
    PRIMARY KEY (franchise, entry_key)
);
CREATE INDEX anime_mapping_series ON anime_mapping (tvdb_series) WHERE tvdb_series IS NOT NULL;
CREATE INDEX anime_mapping_status ON anime_mapping (status);

-- The three orders: release, watch (automatic) and story (with overrides). Alternates are listed after the main
-- order with role 'alternate'.
CREATE TABLE anime_order (
    franchise  TEXT    NOT NULL REFERENCES anime_franchise (slug) ON DELETE CASCADE,
    kind       TEXT    NOT NULL CHECK (kind IN ('release', 'watch', 'story')),
    position   INTEGER NOT NULL,
    entry_key  TEXT    NOT NULL,
    part       TEXT    NOT NULL DEFAULT '',
    role       TEXT    NOT NULL DEFAULT '',
    optional   INTEGER NOT NULL DEFAULT 0 CHECK (optional IN (0, 1)),
    body       TEXT    NOT NULL,
    PRIMARY KEY (franchise, kind, position)
);

-- The metadata review queue (docs/design/ui.md): mapping conflicts and heuristic mappings, and refreshes held back.
-- kind: conflict, heuristic, refresh_shrink (would drop >10% of a series' episodes), refresh_moves_linked (would
-- move a mapping that has linked files). One open item per franchise, entry and kind.
CREATE TABLE anime_review (
    id           INTEGER PRIMARY KEY,
    franchise    TEXT    NOT NULL REFERENCES anime_franchise (slug) ON DELETE CASCADE,
    entry_key    TEXT    NOT NULL DEFAULT '',
    kind         TEXT    NOT NULL CHECK (kind IN ('conflict', 'heuristic', 'refresh_shrink', 'refresh_moves_linked')),
    summary      TEXT    NOT NULL,
    detail       TEXT    NOT NULL DEFAULT '{}',
    state        TEXT    NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'accepted', 'rejected', 'superseded')),
    note         TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    resolved_at  INTEGER
);
CREATE UNIQUE INDEX anime_review_open ON anime_review (franchise, entry_key, kind) WHERE state = 'open';
CREATE INDEX anime_review_state ON anime_review (state, created_at);

-- Metadata responses (docs/design/anime.md): the validators for conditional requests (If-None-Match, If-Modified-Since).
ALTER TABLE metadata_cache ADD COLUMN etag TEXT NOT NULL DEFAULT '';
ALTER TABLE metadata_cache ADD COLUMN last_modified TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE metadata_cache DROP COLUMN last_modified;
ALTER TABLE metadata_cache DROP COLUMN etag;
DROP TABLE anime_review;
DROP TABLE anime_order;
DROP TABLE anime_mapping;
DROP TABLE anime_entry;
DROP TABLE anime_node;
DROP TABLE anime_stage;
DROP TABLE anime_franchise;
