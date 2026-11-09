-- Series metadata refresh (docs/design/tv-metadata.md): Theatarr keeps its own episode list from TVDB v4 (TMDb when TVDB lacks a
-- series), so new series get episodes and continuing ones get their new episodes without Sonarr. Conventions as in
-- 00001.

-- +goose Up

-- An episode's identity at its metadata source: the TVDB episode id (or the TMDb one for a series only TMDb has).
-- A refresh finds the row by it first, so a renumbered episode keeps its row, files and history. removed_at is set
-- when the source no longer lists an episode that cannot be deleted (it has a file, a link, a download or an arr
-- alias); it is cleared when the episode comes back.
ALTER TABLE episode ADD COLUMN tvdb_episode_id INTEGER;
ALTER TABLE episode ADD COLUMN tmdb_episode_id INTEGER;
ALTER TABLE episode ADD COLUMN removed_at INTEGER;
CREATE UNIQUE INDEX episode_tvdb ON episode (title_id, tvdb_episode_id) WHERE tvdb_episode_id IS NOT NULL;
CREATE UNIQUE INDEX episode_tmdb ON episode (title_id, tmdb_episode_id) WHERE tmdb_episode_id IS NOT NULL;

-- One row per series the refresh knows: where its episodes come from (source tvdb or tmdb, source_id), its status
-- (Continuing, Ended, Upcoming; TMDb's mapped to these), airs time and zone (air_date_utc_at is built from them),
-- the last good refresh (refreshed_at) and the last try (attempted_at, error). added_at is set by an add that
-- turned a version on: until the next good refresh, searches of the title wait (series_search_wait) and the add's
-- monitoring applies to the episodes that arrive.
CREATE TABLE series_meta (
    title_id      INTEGER PRIMARY KEY REFERENCES title (id) ON DELETE CASCADE,
    source        TEXT    NOT NULL DEFAULT '' CHECK (source IN ('', 'tvdb', 'tmdb')),
    source_id     INTEGER,
    status        TEXT    NOT NULL DEFAULT '',
    air_time      TEXT    NOT NULL DEFAULT '',
    time_zone     TEXT    NOT NULL DEFAULT '',
    episodes      INTEGER NOT NULL DEFAULT 0,
    added_at      INTEGER,
    refreshed_at  INTEGER,
    attempted_at  INTEGER,
    error         TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX series_meta_added ON series_meta (added_at) WHERE added_at IS NOT NULL;

-- Durable monitoring intent of a series version (Sonarr's Series.MonitorNewItems and Season.Monitored): new seasons
-- are monitored when monitor_new is all (never season 0); seasons ({"1": true, "2": false}) is each known season's
-- choice, which the season's new episodes take.
CREATE TABLE series_monitor (
    version_id   INTEGER PRIMARY KEY REFERENCES version (id) ON DELETE CASCADE,
    monitor_new  TEXT    NOT NULL DEFAULT 'all' CHECK (monitor_new IN ('all', 'none')),
    seasons      TEXT    NOT NULL DEFAULT '{}',
    updated_at   INTEGER NOT NULL
);

-- A search asked for a series whose first refresh has not finished (Sonarr searches after the add's refresh). The
-- refresh hands it to the acquirer when it completes. selection is the acquire.Selection as JSON.
CREATE TABLE series_search_wait (
    id          INTEGER PRIMARY KEY,
    title_id    INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    command_id  INTEGER REFERENCES arrview_command (id) ON DELETE CASCADE,
    selection   TEXT    NOT NULL,
    created_at  INTEGER NOT NULL
);
CREATE INDEX series_search_wait_title ON series_search_wait (title_id);

-- +goose Down
DROP INDEX series_search_wait_title;
DROP TABLE series_search_wait;
DROP TABLE series_monitor;
DROP INDEX series_meta_added;
DROP TABLE series_meta;
DROP INDEX episode_tmdb;
DROP INDEX episode_tvdb;
ALTER TABLE episode DROP COLUMN removed_at;
ALTER TABLE episode DROP COLUMN tmdb_episode_id;
ALTER TABLE episode DROP COLUMN tvdb_episode_id;
