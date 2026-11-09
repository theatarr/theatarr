-- Display-only source ceilings. Search coverage is kept independently of the journal's candidate sample.
-- +goose Up
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
    fetched_at    INTEGER, -- Required for TMDb evidence; never extended by a local recompute.
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
-- Preserve the order and groups of the owning arr profile, using the response already imported.
ALTER TABLE arr_quality_profile ADD COLUMN quality_items TEXT NOT NULL DEFAULT '[]';

ALTER TABLE title_release_date ADD COLUMN fetched_at INTEGER;

-- +goose Down
ALTER TABLE title_release_date DROP COLUMN fetched_at;
ALTER TABLE arr_quality_profile DROP COLUMN quality_items;
DROP TABLE version_ceiling;
DROP TABLE ceiling_search_indexer;
DROP TABLE ceiling_search;
DROP TABLE source_sighting;
DROP TABLE source_ceiling;
