-- Series: what theatarr import-arr reads from Sonarr beyond the movie shape, and
-- the series parts of the shadow runner. Conventions as in 00001. See docs/design/series.md.

-- +goose Up

-- Series facts the decision reads (Sonarr's Series): series_type is standard, daily or anime (NULL for movies);
-- use_scene_numbering is Sonarr's flag (the series has XEM scene numbers); runtime_minutes is the series runtime
-- (0 unknown); original_language is the arr's language name ("Japanese").
ALTER TABLE title ADD COLUMN series_type TEXT;
ALTER TABLE title ADD COLUMN use_scene_numbering INTEGER NOT NULL DEFAULT 0;
ALTER TABLE title ADD COLUMN runtime_minutes INTEGER;
ALTER TABLE title ADD COLUMN original_language TEXT;
-- The series type as each instance has it: the same series can be anime in one Sonarr and standard in another, and
-- a version decides with its own instance's type. title.series_type is the first import's.
ALTER TABLE arr_title ADD COLUMN series_type TEXT NOT NULL DEFAULT '';

-- Sonarr's scene mappings of a series as its API exposes them (SeriesResource.alternateTitles): a name releases use,
-- the TVDB season it maps to (season_number, NULL or -1 for every season), the scene season it is used for
-- (scene_season_number), its origin (tvdb, mixed, unknown:...) and comment. source is the arr instance. Each import
-- replaces the rows of its source. The arr's filter regex and mapping type are not exposed, so they are not here.
CREATE TABLE title_scene_mapping (
    title_id             INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    source               TEXT    NOT NULL,
    name                 TEXT    NOT NULL,
    season_number        INTEGER,
    scene_season_number  INTEGER,
    scene_origin         TEXT    NOT NULL DEFAULT '',
    comment              TEXT    NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX title_scene_mapping_unique ON title_scene_mapping (title_id, source, name,
    COALESCE(season_number, -2), COALESCE(scene_season_number, -2));

-- The episodes a series file holds (several for a multi-episode file). version_file keeps one row per arr file.
CREATE TABLE version_file_episode (
    version_file_id  INTEGER NOT NULL REFERENCES version_file (id) ON DELETE CASCADE,
    episode_id       INTEGER NOT NULL REFERENCES episode (id) ON DELETE CASCADE,
    PRIMARY KEY (version_file_id, episode_id)
);
CREATE INDEX version_file_episode_episode ON version_file_episode (episode_id);

-- Sonarr's release type of a file (singleEpisode, multiEpisode, seasonPack; '' unknown or a movie): the Release
-- Type custom format condition reads it.
ALTER TABLE version_file ADD COLUMN release_type TEXT NOT NULL DEFAULT '';

-- Shadow runner, series: Sonarr writes one grabbed event per episode, so one series grab is several history rows.
-- shadow_grab.history_id is the first of them; history_ids lists them all, episodes our episode ids (JSON arrays).
-- kind on the diff row lets the report give agreement per kind (movies and series have their own exit bar).
ALTER TABLE shadow_grab ADD COLUMN history_ids TEXT NOT NULL DEFAULT '[]';
ALTER TABLE shadow_grab ADD COLUMN episodes TEXT NOT NULL DEFAULT '[]';
ALTER TABLE shadow_rss_pick ADD COLUMN episodes TEXT NOT NULL DEFAULT '[]';
ALTER TABLE shadow_diff ADD COLUMN kind TEXT NOT NULL DEFAULT 'movie';
CREATE INDEX shadow_diff_kind ON shadow_diff (kind, at);

-- +goose Down
DROP INDEX shadow_diff_kind;
ALTER TABLE shadow_diff DROP COLUMN kind;
ALTER TABLE shadow_rss_pick DROP COLUMN episodes;
ALTER TABLE shadow_grab DROP COLUMN episodes;
ALTER TABLE shadow_grab DROP COLUMN history_ids;
ALTER TABLE version_file DROP COLUMN release_type;
DROP TABLE version_file_episode;
DROP TABLE title_scene_mapping;
ALTER TABLE arr_title DROP COLUMN series_type;
ALTER TABLE title DROP COLUMN original_language;
ALTER TABLE title DROP COLUMN runtime_minutes;
ALTER TABLE title DROP COLUMN use_scene_numbering;
ALTER TABLE title DROP COLUMN series_type;
