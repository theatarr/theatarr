-- Anime, second part (docs/design/anime.md): alternate cuts for decisions, the candidate chosen in review, and the
-- Plex watch-order playlists Theatarr keeps (docs/design/anime.md "Watch-order playlists").

-- +goose Up

-- An overrides file's alternate cuts (alt_cuts), resolved: a release of the cut maps its numbers through episodes
-- ({"1": [[1,1]], "2": [[1,2],[1,3]]}, the cut's number -> TVDB season and episode) into tvdb_series.
CREATE TABLE anime_alt_cut (
    franchise    TEXT    NOT NULL REFERENCES anime_franchise (slug) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    tvdb_series  INTEGER NOT NULL,
    entry_key    TEXT    NOT NULL DEFAULT '',
    match        TEXT    NOT NULL DEFAULT '',
    episodes     TEXT    NOT NULL,
    PRIMARY KEY (franchise, name)
);
CREATE INDEX anime_alt_cut_series ON anime_alt_cut (tvdb_series);

-- The candidate a reviewer accepted when it was not the proposed winner (JSON: source, series_id, episodes).
ALTER TABLE anime_review ADD COLUMN choice TEXT NOT NULL DEFAULT '';

-- Watch-order playlists in Plex, one per franchise, order kind and route ('' for the main order). items is the
-- ordered list of Plex rating keys last synced (or planned, in a dry run); plan is the planned items with what each
-- one is. rating_key is Plex's playlist id once created.
CREATE TABLE plex_playlist (
    id          INTEGER PRIMARY KEY,
    franchise   TEXT    NOT NULL,
    order_kind  TEXT    NOT NULL CHECK (order_kind IN ('release', 'watch', 'story')),
    route       TEXT    NOT NULL DEFAULT '',
    name        TEXT    NOT NULL,
    rating_key  TEXT    NOT NULL DEFAULT '',
    items       TEXT    NOT NULL DEFAULT '[]',
    plan        TEXT    NOT NULL DEFAULT '[]',
    mode        TEXT    NOT NULL DEFAULT 'dry_run' CHECK (mode IN ('dry_run', 'applied', 'failed')),
    detail      TEXT    NOT NULL DEFAULT '',
    synced_at   INTEGER,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE (franchise, order_kind, route)
);

-- +goose Down
DROP TABLE plex_playlist;
ALTER TABLE anime_review DROP COLUMN choice;
DROP TABLE anime_alt_cut;
