-- Automatic anime franchise builds (docs/design/anime.md "Automatic builds"): the last discovery outcome per anime
-- series. queued: a build job is queued; built: a franchise was built for it (slug); covered: another franchise
-- already maps its TVDB series; no_mapping: no AniList id is known for its TVDB series; failed: the build failed
-- (detail). The discovery loop retries no_mapping and failed after metadata.anime.refresh_interval.

-- +goose Up
CREATE TABLE anime_discovery (
    title_id   INTEGER PRIMARY KEY REFERENCES title (id) ON DELETE CASCADE,
    tvdb_id    INTEGER NOT NULL,
    outcome    TEXT    NOT NULL CHECK (outcome IN ('queued', 'built', 'covered', 'no_mapping', 'failed')),
    franchise  TEXT    NOT NULL DEFAULT '',
    detail     TEXT    NOT NULL DEFAULT '',
    tried_at   INTEGER NOT NULL
);
CREATE INDEX anime_discovery_tvdb ON anime_discovery (tvdb_id);

-- +goose Down
DROP TABLE anime_discovery;
