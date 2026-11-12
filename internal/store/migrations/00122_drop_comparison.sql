-- The comparison with Sonarr/Radarr is gone (docs/design/rss.md): its six settings leave the stored shadow section
-- (decoded strictly, so a leftover field would stop the start), queued shadow.grab jobs go, and so do the dry-run
-- plans it recorded (router_plan.source shadow.grab and shadow.rss, written only for Watch-only libraries; Live RSS
-- routes are stored with source rss), then its tables. RSS keeps shadow_rss_seen, shadow_rss_cursor, shadow_run and
-- its shadow.rss jobs. Down recreates the tables empty.
-- +goose Up
UPDATE setting_section SET config = json_remove(config, '$.import_interval', '$.search', '$.searches_per_hour',
    '$.search_reuse', '$.keep_results', '$.link_window') WHERE key = 'shadow';
DELETE FROM job WHERE kind = 'shadow.grab';
DELETE FROM router_plan WHERE source LIKE 'shadow.%';
DROP TABLE shadow_diff;
DROP TABLE shadow_rss_pick;
DROP TABLE shadow_grab;
DROP TABLE shadow_cursor;
DROP TABLE shadow_compare_grab;
DROP TABLE shadow_compare_source;

-- +goose Down
CREATE TABLE shadow_cursor (
    instance    TEXT    PRIMARY KEY,
    history_id  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE TABLE shadow_grab (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    instance         TEXT    NOT NULL,
    history_id       INTEGER NOT NULL,
    title_id         INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    version_key      TEXT    NOT NULL,
    grabbed_at       INTEGER NOT NULL,
    release_title    TEXT    NOT NULL,
    release_source   TEXT    NOT NULL DEFAULT '',
    arr_indexer      TEXT    NOT NULL DEFAULT '',
    arr_quality      TEXT    NOT NULL DEFAULT '',
    arr_score        INTEGER,
    existing_source  TEXT    NOT NULL DEFAULT 'none' CHECK (existing_source IN ('none', 'version_file', 'history')),
    state            TEXT    NOT NULL CHECK (state IN ('pending', 'done', 'error')),
    error            TEXT    NOT NULL DEFAULT '',
    searched         INTEGER NOT NULL DEFAULT 0 CHECK (searched IN (0, 1)),
    searched_at      INTEGER,
    search_note      TEXT    NOT NULL DEFAULT '',
    result_count     INTEGER NOT NULL DEFAULT 0,
    accepted_count   INTEGER NOT NULL DEFAULT 0,
    found_arr        INTEGER NOT NULL DEFAULT 0 CHECK (found_arr IN (0, 1)),
    arr_decision     TEXT    NOT NULL DEFAULT '{}',
    pick             TEXT,
    results          TEXT    NOT NULL DEFAULT '[]',
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL, history_ids TEXT NOT NULL DEFAULT '[]', episodes TEXT NOT NULL DEFAULT '[]', router_plan_id INTEGER,
    UNIQUE (instance, history_id)
);
CREATE INDEX shadow_grab_title ON shadow_grab (title_id, version_key, grabbed_at);
CREATE TABLE shadow_rss_pick (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    indexer        TEXT    NOT NULL,
    release_title  TEXT    NOT NULL,
    info_hash      TEXT    NOT NULL DEFAULT '',
    title_id       INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    version_key    TEXT    NOT NULL,
    seen_at        INTEGER NOT NULL,
    published_at   INTEGER,
    quality        TEXT    NOT NULL DEFAULT '',
    score          INTEGER NOT NULL DEFAULT 0,
    decision       TEXT    NOT NULL DEFAULT '{}',
    status         TEXT    NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'linked', 'expired')),
    grab_id        INTEGER REFERENCES shadow_grab (id) ON DELETE SET NULL,
    diff_id        INTEGER, episodes TEXT NOT NULL DEFAULT '[]', router_plan_id INTEGER,
    UNIQUE (indexer, release_title, version_key)
);
CREATE INDEX shadow_rss_pick_open ON shadow_rss_pick (version_key, title_id, status);
CREATE TABLE shadow_diff (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    at             INTEGER NOT NULL,
    title_id       INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    version_key    TEXT    NOT NULL,
    bucket         TEXT    NOT NULL CHECK (bucket IN ('agree', 'we_grab_another', 'arr_grabbed_we_wouldnt',
                                                      'we_grab_arr_didnt')),
    reason         TEXT    NOT NULL,
    detail         TEXT    NOT NULL DEFAULT '',
    searched       INTEGER NOT NULL DEFAULT 0 CHECK (searched IN (0, 1)),
    grab_id        INTEGER UNIQUE REFERENCES shadow_grab (id) ON DELETE CASCADE,
    rss_pick_id    INTEGER REFERENCES shadow_rss_pick (id) ON DELETE SET NULL,
    our_release    TEXT    NOT NULL DEFAULT '',
    our_quality    TEXT    NOT NULL DEFAULT '',
    our_score      INTEGER,
    arr_release    TEXT    NOT NULL DEFAULT '',
    arr_quality    TEXT    NOT NULL DEFAULT '',
    arr_score      INTEGER,
    created_at     INTEGER NOT NULL
, kind TEXT NOT NULL DEFAULT 'movie', search_better TEXT, compare_instance TEXT NOT NULL DEFAULT '', compare_key TEXT);
CREATE INDEX shadow_diff_at ON shadow_diff (at);
CREATE INDEX shadow_diff_bucket ON shadow_diff (bucket, at);
CREATE UNIQUE INDEX shadow_diff_compare ON shadow_diff(compare_instance, compare_key);
CREATE INDEX shadow_diff_kind ON shadow_diff (kind, at);
CREATE TABLE shadow_compare_source (
    instance TEXT PRIMARY KEY,
    started_at INTEGER NOT NULL
);
CREATE TABLE shadow_compare_grab (
    instance TEXT NOT NULL,
    history_id INTEGER NOT NULL,
    arr_title_id INTEGER NOT NULL,
    title_id INTEGER REFERENCES title(id) ON DELETE CASCADE,
    version_id INTEGER REFERENCES version(id) ON DELETE CASCADE,
    at INTEGER NOT NULL,
    event TEXT NOT NULL,
    PRIMARY KEY(instance, history_id)
);
CREATE INDEX shadow_compare_grab_at ON shadow_compare_grab(instance, at);
