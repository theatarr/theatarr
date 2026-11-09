-- First schema: the state entities (docs/design/overview.md), plus the job queue.
-- Conventions (see the package doc in store.go):
--   * timestamps are INTEGER Unix milliseconds UTC, named *_at; calendar dates are TEXT 'YYYY-MM-DD';
--   * booleans are INTEGER 0/1 with a CHECK; enums are TEXT with a CHECK;
--   * JSON is TEXT (jsonb in Postgres);
--   * unique rules over expressions, and partial unique rules, are CREATE UNIQUE INDEX.
-- The anime franchise graph (franchise, line, entry, order, mapping_decision) comes in a later migration (00017).

-- +goose Up

-- A movie or a series: what Seerr, Plex and the views see. Title ids are the same in every view.
CREATE TABLE title (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    kind        TEXT    NOT NULL CHECK (kind IN ('movie', 'series')),
    name        TEXT    NOT NULL,
    sort_name   TEXT    NOT NULL DEFAULT '',
    year        INTEGER,
    tmdb_id     INTEGER,
    tvdb_id     INTEGER,
    imdb_id     TEXT,
    anime       INTEGER NOT NULL DEFAULT 0 CHECK (anime IN (0, 1)),
    monitored   INTEGER NOT NULL DEFAULT 1 CHECK (monitored IN (0, 1)),
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
-- The ids views look titles up by. A TMDb id is unique per kind (TMDb movie and TV ids are separate spaces).
CREATE UNIQUE INDEX title_tmdb ON title (kind, tmdb_id) WHERE tmdb_id IS NOT NULL;
CREATE UNIQUE INDEX title_tvdb ON title (tvdb_id) WHERE tvdb_id IS NOT NULL AND kind = 'series';
CREATE INDEX title_imdb ON title (imdb_id) WHERE imdb_id IS NOT NULL;

-- Further external ids, several per source allowed: one TVDB series can be many AniList entries.
-- source: anilist, anidb, kitsu, mal, tvmaze, tmdb_collection, ...
CREATE TABLE title_external_id (
    title_id     INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    source       TEXT    NOT NULL,
    external_id  TEXT    NOT NULL,
    PRIMARY KEY (title_id, source, external_id)
);
CREATE INDEX title_external_id_lookup ON title_external_id (source, external_id);

-- An episode, stored in TVDB numbering (season 0 = specials).
CREATE TABLE episode (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    title_id             INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    season               INTEGER NOT NULL,
    number               INTEGER NOT NULL,
    name                 TEXT    NOT NULL DEFAULT '',
    air_date             TEXT,
    air_date_utc_at      INTEGER,
    runtime_minutes      INTEGER,
    airs_before_season   INTEGER,
    airs_before_episode  INTEGER,
    airs_after_season    INTEGER,
    monitored            INTEGER NOT NULL DEFAULT 1 CHECK (monitored IN (0, 1)),
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    UNIQUE (title_id, season, number)
);

-- One number per numbering scheme. absolute_tvdb is TVDB's absolute number as TVDB has it; absolute is gap-free.
-- season is NULL for absolute schemes. part_ratio is anibridge's "|ratio" part for two-into-one episodes, kept
-- verbatim. An episode may have two numbers in one scheme (an AniDB double episode), so the rule is per number.
CREATE TABLE episode_number (
    id          INTEGER PRIMARY KEY,
    episode_id  INTEGER NOT NULL REFERENCES episode (id) ON DELETE CASCADE,
    title_id    INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    scheme      TEXT    NOT NULL CHECK (scheme IN ('tvdb', 'absolute_tvdb', 'absolute', 'scene', 'scene_absolute',
                                                   'anidb', 'tmdb', 'alternate')),
    season      INTEGER,
    number      INTEGER NOT NULL,
    part_ratio  TEXT,
    source      TEXT    NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX episode_number_unique ON episode_number (episode_id, scheme, COALESCE(season, -1), number);
-- Parse-time lookup: a parsed number in some scheme, for one title.
CREATE INDEX episode_number_lookup ON episode_number (title_id, scheme, season, number);

-- A version: one per title per version policy (the policy itself lives in YAML, keyed by policy_key).
CREATE TABLE version (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    title_id    INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    policy_key  TEXT    NOT NULL,
    monitored   INTEGER NOT NULL DEFAULT 1 CHECK (monitored IN (0, 1)),
    state       TEXT    NOT NULL DEFAULT 'wanted' CHECK (state IN ('wanted', 'filled', 'cutoff_met')),
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE (title_id, policy_key)
);
CREATE INDEX version_policy ON version (policy_key, state);

-- Per episode state of a series version.
CREATE TABLE episode_version_state (
    version_id     INTEGER NOT NULL REFERENCES version (id) ON DELETE CASCADE,
    episode_id     INTEGER NOT NULL REFERENCES episode (id) ON DELETE CASCADE,
    monitored      INTEGER NOT NULL DEFAULT 1 CHECK (monitored IN (0, 1)),
    state          TEXT    NOT NULL DEFAULT 'wanted' CHECK (state IN ('wanted', 'filled', 'cutoff_met')),
    current_score  INTEGER,
    cutoff_met     INTEGER NOT NULL DEFAULT 0 CHECK (cutoff_met IN (0, 1)),
    updated_at     INTEGER NOT NULL,
    PRIMARY KEY (version_id, episode_id)
);
CREATE INDEX episode_version_state_episode ON episode_version_state (episode_id);

-- A release seen from search, RSS, a push or SeaDex. privacy is a label, judged per result: public, private,
-- semi-private or unknown.
-- download_ref is how to fetch it (a URL or magnet) and may carry a passkey: never logged, never sent to a client.
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

-- Cached-at per provider, keyed by info hash rather than release row, since the same hash comes from many indexers
-- (docs/design/providers.md).
CREATE TABLE provider_cache (
    info_hash   TEXT    NOT NULL,
    provider    TEXT    NOT NULL,
    state       TEXT    NOT NULL CHECK (state IN ('cached', 'uncached', 'miss')),
    source      TEXT    NOT NULL,
    checked_at  INTEGER NOT NULL,
    expires_at  INTEGER,
    PRIMARY KEY (info_hash, provider)
);

-- A release scored against one version. episodes is a JSON array of episode ids for series (packs are decided
-- per episode); rejections is a JSON array of {code, text}; score_breakdown maps format to score.
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

-- A grab. One grab can fill several versions (download_version). download_id is the client's id; for
-- qBittorrent-type clients it is the info hash.
CREATE TABLE download (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    release_id   INTEGER NOT NULL REFERENCES release (id),
    client       TEXT    NOT NULL,
    provider     TEXT,
    download_id  TEXT,
    state        TEXT    NOT NULL CHECK (state IN ('queued', 'submitted', 'downloading', 'completed',
                                                   'waiting_mount', 'importing', 'imported', 'failed', 'abandoned')),
    error        TEXT,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE INDEX download_state ON download (state);
CREATE INDEX download_release ON download (release_id);
CREATE UNIQUE INDEX download_client_id ON download (client, download_id) WHERE download_id IS NOT NULL;

CREATE TABLE download_version (
    download_id  INTEGER NOT NULL REFERENCES download (id) ON DELETE CASCADE,
    version_id   INTEGER NOT NULL REFERENCES version (id) ON DELETE CASCADE,
    episodes     TEXT    NOT NULL DEFAULT '[]',
    PRIMARY KEY (download_id, version_id)
);
CREATE INDEX download_version_version ON download_version (version_id);

-- Every attempt to place a download at a provider (RD, then AD, then TB), with decypharr's error code if refused.
CREATE TABLE download_attempt (
    id            INTEGER PRIMARY KEY,
    download_id   INTEGER NOT NULL REFERENCES download (id) ON DELETE CASCADE,
    provider      TEXT    NOT NULL,
    outcome       TEXT    NOT NULL CHECK (outcome IN ('accepted', 'refused', 'error')),
    code          TEXT,
    message       TEXT,
    attempted_at  INTEGER NOT NULL
);
CREATE INDEX download_attempt_download ON download_attempt (download_id);

-- Content: what one info hash holds. One Content backs many Links.
CREATE TABLE content (
    id             INTEGER PRIMARY KEY,
    info_hash      TEXT    NOT NULL UNIQUE,
    name           TEXT    NOT NULL DEFAULT '',
    mount_folder   TEXT,
    size_bytes     INTEGER,
    state          TEXT    NOT NULL DEFAULT 'present' CHECK (state IN ('present', 'gone', 'unknown')),
    first_seen_at  INTEGER NOT NULL,
    last_seen_at   INTEGER NOT NULL
);

-- Which providers hold a content, under what torrent id and folder name.
CREATE TABLE content_provider (
    content_id   INTEGER NOT NULL REFERENCES content (id) ON DELETE CASCADE,
    provider     TEXT    NOT NULL,
    torrent_id   TEXT,
    folder_name  TEXT,
    seen_at      INTEGER NOT NULL,
    PRIMARY KEY (content_id, provider)
);

-- A file inside a content, with its header probe (probe is JSON; probed once however many links point at it).
CREATE TABLE content_file (
    id          INTEGER PRIMARY KEY,
    content_id  INTEGER NOT NULL REFERENCES content (id) ON DELETE CASCADE,
    path        TEXT    NOT NULL,
    size_bytes  INTEGER NOT NULL,
    probe       TEXT,
    probed_at   INTEGER,
    UNIQUE (content_id, path)
);

-- A library entry: a symlink (later a hardlink or copy) at path. release_title and score_breakdown are fixed at
-- grab time, so the on-disk score never comes from the renamed file name. Inside one Plex library a file is linked
-- once, so several versions may share one link (link_version).
CREATE TABLE link (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    path             TEXT    NOT NULL UNIQUE,
    target           TEXT    NOT NULL,
    kind             TEXT    NOT NULL DEFAULT 'symlink' CHECK (kind IN ('symlink', 'hardlink', 'copy')),
    content_file_id  INTEGER REFERENCES content_file (id),
    title_id         INTEGER NOT NULL REFERENCES title (id),
    download_id      INTEGER REFERENCES download (id),
    release_title    TEXT,
    quality          TEXT,
    score            INTEGER,
    score_breakdown  TEXT    NOT NULL DEFAULT '{}',
    adopted          INTEGER NOT NULL DEFAULT 0 CHECK (adopted IN (0, 1)),
    state            TEXT    NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'dead', 'unlinked')),
    last_checked_at  INTEGER,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);
CREATE INDEX link_content_file ON link (content_file_id);
CREATE INDEX link_title ON link (title_id);
CREATE INDEX link_state ON link (state);

CREATE TABLE link_version (
    link_id     INTEGER NOT NULL REFERENCES link (id) ON DELETE CASCADE,
    version_id  INTEGER NOT NULL REFERENCES version (id) ON DELETE CASCADE,
    PRIMARY KEY (link_id, version_id)
);
CREATE INDEX link_version_version ON link_version (version_id);

-- Episodes a link holds (empty for movies; several for a multi-episode file).
CREATE TABLE link_episode (
    link_id     INTEGER NOT NULL REFERENCES link (id) ON DELETE CASCADE,
    episode_id  INTEGER NOT NULL REFERENCES episode (id) ON DELETE CASCADE,
    PRIMARY KEY (link_id, episode_id)
);
CREATE INDEX link_episode_episode ON link_episode (episode_id);

-- Seerr ids (docs/migration.md): each view answers the ids of the arr instance it replaces.
-- instance is the arr instance name (radarr_4k_debrid); kind is the arr resource; target_id is our id.
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

-- The persistent job queue (internal/jobs). A job is leased by setting state = 'running' and lease_until; an expired
-- lease is taken again, so handlers must be idempotent. dedupe_key is unique among jobs not yet finished.
CREATE TABLE job (
    id            INTEGER PRIMARY KEY,
    kind          TEXT    NOT NULL,
    payload       TEXT    NOT NULL DEFAULT '{}',
    dedupe_key    TEXT,
    state         TEXT    NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'running', 'done', 'failed')),
    priority      INTEGER NOT NULL DEFAULT 0,
    run_at        INTEGER NOT NULL,
    attempts      INTEGER NOT NULL DEFAULT 0,
    max_attempts  INTEGER NOT NULL DEFAULT 5,
    lease_owner   TEXT,
    lease_until   INTEGER,
    last_error    TEXT,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    finished_at   INTEGER
);
CREATE UNIQUE INDEX job_dedupe ON job (dedupe_key) WHERE dedupe_key IS NOT NULL AND state IN ('queued', 'running');
CREATE INDEX job_ready ON job (state, run_at);
CREATE INDEX job_lease ON job (state, lease_until);

-- +goose Down
DROP TABLE job;
DROP TABLE arr_alias;
DROP TABLE link_episode;
DROP TABLE link_version;
DROP TABLE link;
DROP TABLE content_file;
DROP TABLE content_provider;
DROP TABLE content;
DROP TABLE download_attempt;
DROP TABLE download_version;
DROP TABLE download;
DROP TABLE decision;
DROP TABLE provider_cache;
DROP TABLE release;
DROP TABLE episode_version_state;
DROP TABLE version;
DROP TABLE episode_number;
DROP TABLE episode;
DROP TABLE title_external_id;
DROP TABLE title;
