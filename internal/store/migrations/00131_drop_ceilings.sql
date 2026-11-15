-- Source ceilings are gone (phase K, K2): they were display only and nothing searched, grabbed or upgraded by them.
-- Their five tables go, with every ceiling.compute job, queued or finished. A stored library's legacy "ceiling" key
-- is left in its settings row and ignored on load. Down recreates the tables empty.
-- +goose Up
DELETE FROM job WHERE kind = 'ceiling.compute';
DROP TABLE version_ceiling;
DROP TABLE ceiling_search_indexer;
DROP TABLE ceiling_search;
DROP TABLE source_sighting;
DROP TABLE source_ceiling;

-- +goose Down
CREATE TABLE source_ceiling (
    title_id       INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    season         INTEGER NOT NULL DEFAULT -1,
    tier           TEXT NOT NULL DEFAULT 'unknown' CHECK (tier IN ('unknown','tv','dvd','web_1080','bd','web_2160','uhd_bd')),
    status         TEXT NOT NULL DEFAULT 'unknown' CHECK (status IN ('unknown','likely','known','override')),
    override_tier  TEXT CHECK (override_tier IN ('tv','dvd','web_1080','bd','web_2160','uhd_bd')),
    override_note  TEXT NOT NULL DEFAULT '',
    override_by    INTEGER REFERENCES web_user (id) ON DELETE SET NULL,
    override_at    INTEGER,
    override_until INTEGER,
    checked_at     INTEGER NOT NULL DEFAULT 0,
    next_check_at  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (title_id, season)
);
CREATE INDEX source_ceiling_next ON source_ceiling (next_check_at);
CREATE TABLE source_sighting (
    title_id      INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    season        INTEGER NOT NULL DEFAULT -1,
    tier          TEXT NOT NULL CHECK (tier IN ('tv','dvd','web_1080','bd','web_2160','uhd_bd')),
    source        TEXT NOT NULL,
    source_kind   TEXT NOT NULL,
    positive_only INTEGER NOT NULL CHECK (positive_only IN (0,1)),
    example       TEXT NOT NULL DEFAULT '',
    quality       TEXT NOT NULL DEFAULT '',
    count         INTEGER NOT NULL DEFAULT 1,
    first_seen_at INTEGER NOT NULL,
    last_seen_at  INTEGER NOT NULL,
    fetched_at    INTEGER,
    CHECK (source_kind <> 'tmdb' OR fetched_at IS NOT NULL),
    PRIMARY KEY (title_id, season, tier, source, source_kind)
);
CREATE TABLE ceiling_search (
    id          INTEGER PRIMARY KEY,
    title_id    INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    season      INTEGER NOT NULL DEFAULT -1,
    version_key TEXT NOT NULL,
    searched_at INTEGER NOT NULL,
    source      TEXT NOT NULL,
    best_tier   TEXT NOT NULL DEFAULT 'unknown',
    UNIQUE (title_id, season, version_key, searched_at, source)
);
CREATE TABLE ceiling_search_indexer (
    search_id     INTEGER NOT NULL REFERENCES ceiling_search (id) ON DELETE CASCADE,
    indexer       TEXT NOT NULL,
    source_kind   TEXT NOT NULL,
    positive_only INTEGER NOT NULL CHECK (positive_only IN (0,1)),
    complete      INTEGER NOT NULL CHECK (complete IN (0,1)),
    PRIMARY KEY (search_id, indexer)
);
CREATE TABLE version_ceiling (
    version_id       INTEGER NOT NULL REFERENCES version (id) ON DELETE CASCADE,
    season           INTEGER NOT NULL DEFAULT -1,
    effective_cutoff  TEXT NOT NULL DEFAULT '',
    upgrade_state    TEXT NOT NULL DEFAULT 'below_cutoff',
    ceiling          TEXT NOT NULL DEFAULT '{}',
    computed_at      INTEGER NOT NULL,
    PRIMARY KEY (version_id, season)
);
