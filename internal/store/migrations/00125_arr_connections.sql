-- The periodic arr import is gone: its jobs, its section, the views' alias_from and the arr tables go
-- (docs/design/library.md "Removing a library", decide.md "Frozen arr profiles"). Nothing of a library that is
-- still in the settings is deleted, and no Live library's links, monitoring, profile or naming changes, in order:
--
-- 1. Queued arr.import jobs (no handler any more) and the error card of a failed import go.
-- 2. A Watch-only library that copied an arr instance (named under import.arr) stays as it is, with one error card
--    saying it is no longer updated. A Live library named there gets no card: it is an ordinary library now.
-- 3. The import section and its stored credential go; views lose identity.alias_from.
-- 4. A copy of a movie whose minimum availability came from its arr record (arr_title) gets that value as its own
--    choice (arrview_title v/<key>, updated_at 0), as 00121 did for followers, so its rule stays the same. A blank
--    row (a list sync's) takes it; an existing choice is kept. A value equal to the library's default needs no row.
-- 5. With libraries stored in the database (settings_e3_bootstrap), a profile taken from an arr (FromArr, the Go
--    field name: profile rows are policy.Profile as JSON) that no library names goes.
-- 6. arr_frozen_input keeps the stored snapshot (arr_policy_snapshot.body) of each instance a remaining FromArr
--    profile or naming names; FreezeStored (internal/migration) turns those rows into native copies at the next
--    start. A later migration drops it.
-- 7. The arr tables, import_run and library_naming go.
-- 8. With libraries stored in the database: rows of libraries no longer in setting_version go, as removing a library
--    does (internal/settings/libraryrows.go): links of those downloads lose download_id, links held only by them
--    go, then their versions (their children cascade), their text-keyed rows and their v/<key> view rows; the
--    episode and title monitoring of the titles they had is recomputed, and movie collections drop their keys.
-- +goose Up
DELETE FROM job WHERE kind = 'arr.import';
DELETE FROM error_event WHERE source = 'job' AND component = 'arr.import';

INSERT OR IGNORE INTO error_event (fingerprint, source, level, message, first_seen, last_seen, updated_at)
SELECT 'arr-mirror:' || sv.key, 'log', 'error',
    'Library ' || sv.key || ' copied a Sonarr/Radarr instance. That copy is no longer updated. Delete the library when you no longer need it.',
    n.ms, n.ms, n.ms
FROM setting_section s, json_each(s.config, '$.arr') a
    JOIN setting_version sv ON sv.key = json_extract(a.value, '$.version'),
    (SELECT CAST(strftime('%s', 'now') AS INTEGER) * 1000 AS ms) n
WHERE s.key = 'import' AND json_type(s.config, '$.arr') = 'object'
    AND COALESCE(json_extract(sv.config, '$.config.mode'), '') <> 'active'
GROUP BY sv.key;

CREATE TEMP TABLE arr_gone_secret AS SELECT secret_id AS id FROM setting_section
    WHERE key = 'import' AND secret_id IS NOT NULL;
DELETE FROM setting_section WHERE key = 'import';
DELETE FROM setting_secret WHERE id IN (SELECT id FROM arr_gone_secret);
DROP TABLE arr_gone_secret;
UPDATE setting_view_binding SET config = json_remove(config, '$.identity.alias_from')
    WHERE json_type(config, '$.identity.alias_from') IS NOT NULL;

INSERT INTO arrview_title (view, title_id, minimum_availability, added_at, updated_at)
SELECT 'v/' || v.policy_key, v.title_id, i.m, 0, 0
FROM (SELECT a.version_id, a.minimum_availability AS m,
        ROW_NUMBER() OVER (PARTITION BY a.version_id
            ORDER BY (vs.instance IS NOT NULL) DESC, a.instance) AS n
    FROM arr_title a LEFT JOIN version_source vs ON vs.version_id = a.version_id
        AND vs.instance = a.instance AND vs.arr_id = a.arr_id WHERE a.kind = 'movie') i
JOIN version v ON v.id = i.version_id
WHERE i.n = 1 AND COALESCE(i.m, '') <> ''
    AND i.m <> COALESCE(NULLIF((SELECT json_extract(sv.config, '$.config.minimum_availability')
        FROM setting_version sv WHERE sv.key = v.policy_key), ''), 'released')
ON CONFLICT (view, title_id) DO UPDATE SET minimum_availability = excluded.minimum_availability, updated_at = 0
    WHERE arrview_title.minimum_availability = '';

DELETE FROM setting_quality_profile WHERE COALESCE(json_extract(config, '$.FromArr'), '') <> ''
    AND EXISTS (SELECT 1 FROM settings_e3_bootstrap)
    AND key NOT IN (SELECT json_extract(config, '$.config.profile') FROM setting_version
        WHERE json_extract(config, '$.config.profile') IS NOT NULL);

CREATE TABLE arr_frozen_input (
    instance  TEXT PRIMARY KEY,
    flavour   TEXT NOT NULL,
    body      TEXT NOT NULL
);
INSERT INTO arr_frozen_input (instance, flavour, body)
SELECT s.instance, i.flavour, s.body FROM arr_policy_snapshot s JOIN arr_instance i ON i.name = s.instance
WHERE s.instance IN (
    SELECT trim(substr(r, 1, instr(r || '/', '/') - 1)) FROM (SELECT json_extract(config, '$.FromArr') AS r
        FROM setting_quality_profile WHERE COALESCE(json_extract(config, '$.FromArr'), '') <> '')
    UNION SELECT trim(json_extract(config, '$.FromArr')) FROM setting_version_naming
        WHERE COALESCE(json_extract(config, '$.FromArr'), '') <> '');

DROP TABLE arr_tag;
DROP TABLE arr_quality_profile;
DROP TABLE arr_policy_snapshot;
DROP TABLE arr_blocklist;
DROP TABLE arr_history;
DROP TABLE arr_alias;
DROP TABLE arr_title;
DROP TABLE arr_instance;
DROP TABLE import_run;
DROP TABLE library_naming;

CREATE TEMP TABLE arr_gone_version AS SELECT id, title_id FROM version
    WHERE EXISTS (SELECT 1 FROM settings_e3_bootstrap) AND policy_key NOT IN (SELECT key FROM setting_version);
CREATE TEMP TABLE arr_gone_key AS SELECT DISTINCT policy_key AS key FROM version
    WHERE id IN (SELECT id FROM arr_gone_version);
UPDATE link SET download_id = NULL WHERE download_id IN (SELECT d.id FROM download d
    WHERE d.version_id IN (SELECT id FROM arr_gone_version));
DELETE FROM link WHERE id IN (SELECT link_id FROM link_version WHERE version_id IN (SELECT id FROM arr_gone_version))
    AND NOT EXISTS (SELECT 1 FROM link_version lv WHERE lv.link_id = link.id
        AND lv.version_id NOT IN (SELECT id FROM arr_gone_version));
DELETE FROM version WHERE id IN (SELECT id FROM arr_gone_version);
DELETE FROM router_offer WHERE version_key IN (SELECT key FROM arr_gone_key)
    OR from_version IN (SELECT key FROM arr_gone_key);
DELETE FROM search_request WHERE version_key IN (SELECT key FROM arr_gone_key);
DELETE FROM arrview_title_tag WHERE version IN (SELECT key FROM arr_gone_key);
DELETE FROM arrview_title WHERE view IN (SELECT 'v/' || key FROM arr_gone_key);
UPDATE episode SET monitored = s.m, updated_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
    FROM (SELECT episode_id, MAX(monitored) AS m FROM episode_version_state GROUP BY episode_id) s
    WHERE s.episode_id = episode.id AND episode.monitored <> s.m
        AND episode.title_id IN (SELECT title_id FROM arr_gone_version);
UPDATE title SET monitored = t.m, updated_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
    FROM (SELECT g.title_id, COALESCE(MAX(v.monitored), 0) AS m FROM (SELECT DISTINCT title_id FROM arr_gone_version) g
        LEFT JOIN version v ON v.title_id = g.title_id AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r
            WHERE r.version_id = v.id) GROUP BY g.title_id) t
    WHERE t.title_id = title.id AND title.monitored <> t.m;
UPDATE movie_collection SET versions = (SELECT json_group_array(j.value) FROM json_each(movie_collection.versions) j
        WHERE j.value IN (SELECT key FROM setting_version))
    WHERE EXISTS (SELECT 1 FROM settings_e3_bootstrap) AND EXISTS (SELECT 1 FROM json_each(movie_collection.versions) j
        WHERE j.value NOT IN (SELECT key FROM setting_version));
DROP TABLE arr_gone_key;
DROP TABLE arr_gone_version;

-- +goose Down
DROP TABLE arr_frozen_input;
CREATE TABLE arr_instance (
    name          TEXT    PRIMARY KEY,
    flavour       TEXT    NOT NULL CHECK (flavour IN ('radarr', 'sonarr')),
    policy_key    TEXT    NOT NULL,
    app_version   TEXT    NOT NULL DEFAULT '',
    root_folders  TEXT    NOT NULL DEFAULT '[]',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
CREATE TABLE import_run (
    id           INTEGER PRIMARY KEY,
    instance     TEXT    NOT NULL,
    started_at   INTEGER NOT NULL,
    finished_at  INTEGER NOT NULL,
    summary      TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX import_run_instance ON import_run (instance, started_at);
CREATE TABLE arr_tag (
    instance  TEXT    NOT NULL REFERENCES arr_instance (name) ON DELETE CASCADE,
    arr_id    INTEGER NOT NULL,
    label     TEXT    NOT NULL,
    PRIMARY KEY (instance, arr_id)
);
CREATE TABLE arr_quality_profile (
    instance             TEXT    NOT NULL REFERENCES arr_instance (name) ON DELETE CASCADE,
    arr_id               INTEGER NOT NULL,
    name                 TEXT    NOT NULL,
    cutoff               TEXT    NOT NULL DEFAULT '',
    upgrade_allowed      INTEGER NOT NULL DEFAULT 1 CHECK (upgrade_allowed IN (0, 1)),
    min_format_score     INTEGER NOT NULL DEFAULT 0,
    cutoff_format_score  INTEGER NOT NULL DEFAULT 0,
    quality_items        TEXT    NOT NULL DEFAULT '[]',
    PRIMARY KEY (instance, arr_id)
);
CREATE TABLE arr_title (
    instance              TEXT    NOT NULL REFERENCES arr_instance (name) ON DELETE CASCADE,
    arr_id                INTEGER NOT NULL,
    kind                  TEXT    NOT NULL CHECK (kind IN ('movie', 'series')),
    title_id              INTEGER NOT NULL REFERENCES title (id),
    version_id            INTEGER NOT NULL REFERENCES version (id),
    monitored             INTEGER NOT NULL CHECK (monitored IN (0, 1)),
    quality_profile_id    INTEGER NOT NULL DEFAULT 0,
    minimum_availability  TEXT    NOT NULL DEFAULT '',
    status                TEXT    NOT NULL DEFAULT '',
    path                  TEXT    NOT NULL DEFAULT '',
    root_folder_path      TEXT    NOT NULL DEFAULT '',
    tags                  TEXT    NOT NULL DEFAULT '[]',
    arr_file_id           INTEGER NOT NULL DEFAULT 0,
    added_at              INTEGER,
    series_type           TEXT    NOT NULL DEFAULT '',
    tags_copied           TEXT    NOT NULL DEFAULT '[]',
    PRIMARY KEY (instance, arr_id)
);
CREATE INDEX arr_title_title ON arr_title (title_id);
CREATE INDEX arr_title_version ON arr_title (version_id);
CREATE TABLE arr_history (
    instance             TEXT    NOT NULL REFERENCES arr_instance (name) ON DELETE CASCADE,
    arr_id               INTEGER NOT NULL,
    event_type           TEXT    NOT NULL,
    date_at              INTEGER NOT NULL,
    arr_title_id         INTEGER NOT NULL DEFAULT 0,
    arr_episode_id       INTEGER,
    title_id             INTEGER REFERENCES title (id),
    version_id           INTEGER REFERENCES version (id),
    download_id          TEXT,
    info_hash            TEXT,
    protocol             TEXT    NOT NULL DEFAULT '' CHECK (protocol IN ('', 'torrent', 'usenet')),
    source_title         TEXT    NOT NULL DEFAULT '',
    quality              TEXT    NOT NULL DEFAULT '',
    custom_format_score  INTEGER,
    languages            TEXT    NOT NULL DEFAULT '[]',
    data                 TEXT    NOT NULL DEFAULT '{}',
    PRIMARY KEY (instance, arr_id)
);
CREATE INDEX arr_history_hash ON arr_history (info_hash) WHERE info_hash IS NOT NULL;
CREATE INDEX arr_history_download ON arr_history (instance, download_id) WHERE download_id IS NOT NULL;
CREATE INDEX arr_history_title ON arr_history (title_id);
CREATE INDEX journal_arr_history_title ON arr_history (instance, arr_title_id, event_type, date_at);
CREATE TABLE arr_alias (
    instance    TEXT    NOT NULL,
    kind        TEXT    NOT NULL CHECK (kind IN ('movie', 'series', 'episode', 'moviefile', 'episodefile',
                                                 'tag', 'qualityprofile')),
    arr_id      INTEGER NOT NULL,
    target_id   INTEGER NOT NULL,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (instance, kind, arr_id)
);
CREATE INDEX arr_alias_target ON arr_alias (kind, target_id);
CREATE TABLE library_naming (
    instance    TEXT    PRIMARY KEY,
    naming      TEXT    NOT NULL,
    qualities   TEXT    NOT NULL DEFAULT '{}',
    fetched_at  INTEGER NOT NULL
);
CREATE TABLE arr_policy_snapshot (
    instance    TEXT PRIMARY KEY REFERENCES arr_instance (name) ON DELETE CASCADE,
    body        TEXT NOT NULL,
    fetched_at  INTEGER NOT NULL
);
CREATE TABLE arr_blocklist (
    instance      TEXT NOT NULL REFERENCES arr_instance (name) ON DELETE CASCADE,
    version_key   TEXT NOT NULL,
    info_hash     TEXT NOT NULL,
    title_id      INTEGER REFERENCES title (id) ON DELETE SET NULL,
    release_title TEXT NOT NULL,
    PRIMARY KEY (instance, version_key, info_hash)
);
