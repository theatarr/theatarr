-- The old arr-file layer goes (phase K, K3): version_file, version_file_episode, version_source, content_version,
-- arr_frozen_input, release and decision. Every reader now reads Theatarr's links. On an install that adopted its files
-- these tables are empty; leftover rows are converted first:
--
-- * A current arr file (its version's owning instance's, or any of a version without one) becomes a link record at
--   its path with its title, library, episodes, quality, release and the arr's facts in file_metadata, its media info
--   in adopted_media and "<instance> <path>" as adopted_from, as adoption records them. The migration cannot read the
--   disk, so the record is kind 'copy' with the path as its target: the file shows as held, and Theatarr never moves or
--   unlinks it (as it never touched the arr's file). A path that already holds a link keeps it; the library and
--   episodes are added to it when it is the same title's.
-- * A library action that found its title only through an arr file at its path keeps that title (title_id).
-- * A stored profile or naming still taken from an arr (FromArr; FreezeStored converted them at every start, so this
--   is only an install that jumps here from before 00125) becomes the minimal native profile or the default naming,
--   and its libraries Watch only with one message each, as FreezeStored does with one it cannot copy.
--
-- Down recreates the tables empty.
-- +goose Up
INSERT OR IGNORE INTO link (path, target, kind, title_id, release_title, quality, file_metadata, adopted_from,
    adopted_media, state, created_at, updated_at)
SELECT vf.path, vf.path, 'copy', vf.title_id, NULLIF(COALESCE(NULLIF(vf.scene_name, ''), vf.release_title), ''),
    NULLIF(vf.quality, ''),
    json_object('Quality', vf.quality, 'Revision', max(vf.quality_revision, 1), 'SceneName', vf.scene_name,
        'OriginalFilePath', vf.original_file_path, 'RelativePath', vf.relative_path, 'ReleaseGroup', vf.release_group,
        'Edition', vf.edition, 'Languages', json(CASE WHEN json_valid(vf.languages) THEN vf.languages ELSE '[]' END),
        'Size', vf.size_bytes, 'IndexerFlags', vf.indexer_flags, 'ReleaseType', vf.release_type,
        'InfoHash', COALESCE((SELECT c.info_hash FROM content c WHERE c.id = vf.content_id),
            CASE WHEN length(vf.download_id) = 40 THEN lower(vf.download_id) END, '')),
    vf.instance || ' ' || vf.path, vf.media_info, 'active', COALESCE(vf.added_at, 0), COALESCE(vf.added_at, 0)
FROM version_file vf LEFT JOIN version_source vs ON vs.version_id = vf.version_id
WHERE vs.instance IS NULL OR vs.instance = vf.instance
ORDER BY vf.id;
INSERT OR IGNORE INTO link_version (link_id, version_id)
SELECT l.id, vf.version_id FROM version_file vf JOIN link l ON l.path = vf.path AND l.title_id = vf.title_id
LEFT JOIN version_source vs ON vs.version_id = vf.version_id
WHERE (vs.instance IS NULL OR vs.instance = vf.instance) AND l.state = 'active';
INSERT OR IGNORE INTO link_episode (link_id, episode_id)
SELECT l.id, fe.episode_id FROM version_file vf JOIN version_file_episode fe ON fe.version_file_id = vf.id
JOIN link l ON l.path = vf.path AND l.title_id = vf.title_id
LEFT JOIN version_source vs ON vs.version_id = vf.version_id
WHERE (vs.instance IS NULL OR vs.instance = vf.instance) AND l.state = 'active';

UPDATE library_action SET title_id = (SELECT vf.title_id FROM version_file vf WHERE vf.path = library_action.path LIMIT 1)
WHERE title_id IS NULL AND link_id IS NULL AND download_id IS NULL
    AND EXISTS (SELECT 1 FROM version_file vf WHERE vf.path = library_action.path);

UPDATE setting_version SET config = json_set(config, '$.config.mode', 'shadow'),
    updated_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE COALESCE(json_extract(config, '$.config.profile'), '') IN (SELECT key FROM setting_quality_profile
        WHERE COALESCE(json_extract(config, '$.FromArr'), '') <> '')
    OR key IN (SELECT key FROM setting_version_naming WHERE COALESCE(json_extract(config, '$.FromArr'), '') <> '');
INSERT INTO error_event (fingerprint, source, component, level, message, first_seen, last_seen, updated_at)
SELECT 'arr-freeze:' || v.key, 'log', 'settings', 'error', 'Library ' || v.key || ' used a profile or naming taken from'
    || ' Sonarr/Radarr that could not be copied. It is now Watch only. Check its profile and naming in Settings ›'
    || ' Libraries, then set it Live.',
    CAST(strftime('%s', 'now') AS INTEGER) * 1000, CAST(strftime('%s', 'now') AS INTEGER) * 1000,
    CAST(strftime('%s', 'now') AS INTEGER) * 1000
FROM setting_version v
WHERE COALESCE(json_extract(v.config, '$.config.profile'), '') IN (SELECT key FROM setting_quality_profile
        WHERE COALESCE(json_extract(config, '$.FromArr'), '') <> '')
    OR v.key IN (SELECT key FROM setting_version_naming WHERE COALESCE(json_extract(config, '$.FromArr'), '') <> '')
ON CONFLICT (fingerprint) DO UPDATE SET message = excluded.message, last_seen = excluded.last_seen, status = 'new',
    updated_at = excluded.updated_at;
UPDATE setting_quality_profile SET config = '{"Qualities":[{"Name":"WEBDL-1080p","Group":false,"Qualities":null}],"QualityOrder":null,"Cutoff":"WEBDL-1080p","Upgrades":null,"MinScore":0,"CutoffScore":0,"MinUpgradeStep":null,"Sizes":null,"FormatSet":"","Formats":null,"Language":"","Propers":""}',
    updated_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE COALESCE(json_extract(config, '$.FromArr'), '') <> '';
UPDATE setting_version_naming SET config = '{"Folder":"","File":"","Season":"","Specials":"","Daily":"","Anime":"","Colon":"","MultiEpisode":""}',
    updated_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE COALESCE(json_extract(config, '$.FromArr'), '') <> '';

DROP TABLE version_file_episode;
DROP TABLE version_file;
DROP TABLE version_source;
DROP TABLE content_version;
DROP TABLE decision;
DROP TABLE release;
DROP TABLE arr_frozen_input;

-- +goose Down
CREATE TABLE arr_frozen_input (
    instance  TEXT PRIMARY KEY,
    flavour   TEXT NOT NULL,
    body      TEXT NOT NULL
);
CREATE TABLE release (
    id             INTEGER PRIMARY KEY,
    indexer        TEXT    NOT NULL,
    guid           TEXT    NOT NULL,
    origin         TEXT    NOT NULL CHECK (origin IN ('search', 'rss', 'push', 'seadex', 'import')),
    privacy        TEXT    NOT NULL CHECK (privacy IN ('public', 'private', 'semi-private', 'unknown')),
    protocol       TEXT    NOT NULL DEFAULT 'torrent' CHECK (protocol IN ('torrent', 'usenet')),
    info_hash      TEXT,
    title          TEXT    NOT NULL,
    size_bytes     INTEGER,
    published_at   INTEGER,
    download_ref   TEXT,
    indexer_flags  TEXT    NOT NULL DEFAULT '[]',
    seadex         TEXT,
    parser         TEXT,
    parsed         TEXT    NOT NULL DEFAULT '{}',
    first_seen_at  INTEGER NOT NULL,
    last_seen_at   INTEGER NOT NULL,
    UNIQUE (indexer, guid)
);
CREATE INDEX release_info_hash ON release (info_hash) WHERE info_hash IS NOT NULL;
CREATE TABLE decision (
    id               INTEGER PRIMARY KEY,
    release_id       INTEGER NOT NULL REFERENCES release (id) ON DELETE CASCADE,
    version_id       INTEGER NOT NULL REFERENCES version (id) ON DELETE CASCADE,
    accepted         INTEGER NOT NULL CHECK (accepted IN (0, 1)),
    score            INTEGER NOT NULL DEFAULT 0,
    quality          TEXT,
    episodes         TEXT    NOT NULL DEFAULT '[]',
    rejections       TEXT    NOT NULL DEFAULT '[]',
    score_breakdown  TEXT    NOT NULL DEFAULT '{}',
    decided_at       INTEGER NOT NULL,
    UNIQUE (release_id, version_id)
);
CREATE INDEX decision_version ON decision (version_id, accepted);
CREATE TABLE content_version (
    content_id     INTEGER NOT NULL REFERENCES content (id) ON DELETE CASCADE,
    version_id     INTEGER NOT NULL REFERENCES version (id) ON DELETE CASCADE,
    release_title  TEXT    NOT NULL DEFAULT '',
    source         TEXT    NOT NULL,
    grabbed_at     INTEGER,
    imported_at    INTEGER,
    failed_at      INTEGER,
    PRIMARY KEY (content_id, version_id)
);
CREATE INDEX content_version_version ON content_version (version_id);
CREATE TABLE version_source (
    version_id  INTEGER PRIMARY KEY REFERENCES version (id) ON DELETE CASCADE,
    instance    TEXT    NOT NULL,
    arr_id      INTEGER NOT NULL
);
CREATE TABLE version_file (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    version_id           INTEGER NOT NULL REFERENCES version (id) ON DELETE CASCADE,
    title_id             INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    instance             TEXT    NOT NULL,
    arr_file_id          INTEGER NOT NULL,
    path                 TEXT    NOT NULL,
    relative_path        TEXT    NOT NULL DEFAULT '',
    size_bytes           INTEGER NOT NULL DEFAULT 0,
    quality              TEXT    NOT NULL DEFAULT '',
    quality_revision     INTEGER NOT NULL DEFAULT 1,
    release_group        TEXT    NOT NULL DEFAULT '',
    scene_name           TEXT    NOT NULL DEFAULT '',
    edition              TEXT    NOT NULL DEFAULT '',
    languages            TEXT    NOT NULL DEFAULT '[]',
    media_info           TEXT    NOT NULL DEFAULT '{}',
    custom_format_score  INTEGER,
    indexer_flags        INTEGER NOT NULL DEFAULT 0,
    original_file_path   TEXT    NOT NULL DEFAULT '',
    cutoff_not_met       INTEGER NOT NULL DEFAULT 0 CHECK (cutoff_not_met IN (0, 1)),
    added_at             INTEGER,
    download_id          TEXT,
    release_title        TEXT,
    content_id           INTEGER REFERENCES content (id), release_type TEXT NOT NULL DEFAULT '',
    UNIQUE (instance, arr_file_id)
);
CREATE INDEX version_file_version ON version_file (version_id);
CREATE INDEX version_file_content ON version_file (content_id);
CREATE INDEX journal_version_file_download ON version_file (instance, download_id) WHERE download_id IS NOT NULL;
CREATE INDEX version_file_path_content ON version_file (path, content_id);
CREATE TABLE version_file_episode (
    version_file_id  INTEGER NOT NULL REFERENCES version_file (id) ON DELETE CASCADE,
    episode_id       INTEGER NOT NULL REFERENCES episode (id) ON DELETE CASCADE,
    PRIMARY KEY (version_file_id, episode_id)
);
CREATE INDEX version_file_episode_episode ON version_file_episode (episode_id);
