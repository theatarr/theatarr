-- Title metadata and art from TMDb (docs/design/ui.md §5, docs/design/artwork.md): one row per title, written by the meta.art
-- job. A row exists once a title was tried; error is set (and the art columns kept from the last good fetch) when the
-- last try failed.
--
-- tmdb_id is the TMDb id the row was fetched with: the title's own, or one found by its TVDB or IMDb id (Sonarr
-- adds carry only a TVDB id). Paths are TMDb image paths ('/abc.jpg'); art_hash is a short hash of the three paths,
-- used as ?v= on image URLs so a new path means a new URL. genres is a JSON array of names. release_date is the
-- movie's release date or the series' first air date (YYYY-MM-DD); last_air_date and next_air_date are series only.
-- Times are Unix milliseconds: refreshed_at is the last good fetch, attempted_at the last try, refresh_after when the
-- job may fetch the row again.

-- +goose Up
CREATE TABLE title_meta (
    title_id           INTEGER PRIMARY KEY REFERENCES title (id) ON DELETE CASCADE,
    tmdb_id            INTEGER,
    overview           TEXT    NOT NULL DEFAULT '',
    tagline            TEXT    NOT NULL DEFAULT '',
    poster_path        TEXT    NOT NULL DEFAULT '',
    backdrop_path      TEXT    NOT NULL DEFAULT '',
    logo_path          TEXT    NOT NULL DEFAULT '',
    genres             TEXT    NOT NULL DEFAULT '[]',
    runtime_minutes    INTEGER,
    rating             REAL,
    rating_votes       INTEGER,
    certification      TEXT    NOT NULL DEFAULT '',
    status             TEXT    NOT NULL DEFAULT '',
    network            TEXT    NOT NULL DEFAULT '',
    original_language  TEXT    NOT NULL DEFAULT '',
    release_date       TEXT    NOT NULL DEFAULT '',
    last_air_date      TEXT    NOT NULL DEFAULT '',
    next_air_date      TEXT    NOT NULL DEFAULT '',
    art_hash           TEXT    NOT NULL DEFAULT '',
    refreshed_at       INTEGER,
    attempted_at       INTEGER NOT NULL,
    refresh_after      INTEGER NOT NULL,
    error              TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX title_meta_due ON title_meta (refresh_after);

-- +goose Down
DROP TABLE title_meta;
