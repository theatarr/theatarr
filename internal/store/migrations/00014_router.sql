-- Download router (docs/design/router.md): downloads added to debrid providers, every provider attempt,
-- persisted pacing per provider, and the dry-run plans recorded for versions the router may not write for.
-- Conventions as in 00001.
--
-- 00001's download, download_version and download_attempt were never written (nothing grabbed before the router). They are
-- replaced: the router adds to providers directly, one download per release and version, so the version moves onto
-- the download row and the states become the router's. DROP TABLE on a table others reference needs foreign keys
-- off, which only works outside a transaction; hence NO TRANSACTION with an explicit BEGIN ... COMMIT (as 00006).
--
-- download.state, shared with the library (import, links):
--   queued       a release was chosen, but every provider was paced or backing off; the router retries it later;
--   adding       the router is adding it (a crash leaves this; the poller fails rows stuck here);
--   added        the provider accepted the add (Real-Debrid: files not selected yet);
--   downloading  the provider is fetching it (uncached), progress 0-100;
--   ready        the provider holds every selected file: the library imports it;
--   importing    the library is importing it (optional, set by the library);
--   imported     linked into the version's library (set by the library);
--   failed       error says why: a miss on every provider for this release, a provider error status, or stuck.
-- Nothing in this schema or the router deletes a provider torrent (debrid torrents are shared by every library).

-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;

DROP TABLE download_attempt;
DROP TABLE download_version;
DROP TABLE download;

-- One release routed for one version. provider and provider_torrent_id are set once a provider accepted it (or
-- already held the hash: reused = 1, no add was sent). name is the provider's torrent name (the mount folder);
-- provider_status the provider's raw status; cached 1 when the provider had it at once, 0 when it had to fetch it,
-- NULL unknown. episodes is a JSON array of episode ids for series. source says what asked (job, cli).
CREATE TABLE download (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    title_id             INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    version_id           INTEGER NOT NULL REFERENCES version (id) ON DELETE CASCADE,
    version_key          TEXT    NOT NULL,
    info_hash            TEXT    NOT NULL,
    release_title        TEXT    NOT NULL,
    indexer              TEXT    NOT NULL,
    quality              TEXT    NOT NULL DEFAULT '',
    score                INTEGER NOT NULL DEFAULT 0,
    size_bytes           INTEGER,
    episodes             TEXT    NOT NULL DEFAULT '[]',
    provider             TEXT,
    provider_torrent_id  TEXT,
    provider_status      TEXT    NOT NULL DEFAULT '',
    name                 TEXT    NOT NULL DEFAULT '',
    progress             REAL    NOT NULL DEFAULT 0,
    cached               INTEGER CHECK (cached IN (0, 1)),
    files_selected       INTEGER NOT NULL DEFAULT 0 CHECK (files_selected IN (0, 1)),
    reused               INTEGER NOT NULL DEFAULT 0 CHECK (reused IN (0, 1)),
    state                TEXT    NOT NULL CHECK (state IN ('queued', 'adding', 'added', 'downloading', 'ready',
                                                           'importing', 'imported', 'failed')),
    error                TEXT,
    source               TEXT    NOT NULL DEFAULT '',
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    added_at             INTEGER,
    ready_at             INTEGER,
    imported_at          INTEGER,
    polled_at            INTEGER
);
CREATE INDEX download_state ON download (state);
CREATE INDEX download_version ON download (version_id, state);
CREATE INDEX download_title ON download (title_id);
CREATE INDEX download_hash ON download (info_hash);
CREATE INDEX download_provider_torrent ON download (provider, provider_torrent_id) WHERE provider_torrent_id IS NOT NULL;

-- Every provider considered for a download, in order. outcome: accepted (added, or reused), refused (the provider
-- answered no; code is the adder's code), skipped (not asked: code says why, e.g. rd_name_rule, known_uncached,
-- paced, backoff, no_key, miss), error (failed in transit or unrecognised), deferred (paced; retry_at). message is
-- sanitised.
CREATE TABLE download_attempt (
    id            INTEGER PRIMARY KEY,
    download_id   INTEGER NOT NULL REFERENCES download (id) ON DELETE CASCADE,
    provider      TEXT    NOT NULL,
    outcome       TEXT    NOT NULL CHECK (outcome IN ('accepted', 'refused', 'skipped', 'error', 'deferred')),
    code          TEXT    NOT NULL DEFAULT '',
    message       TEXT    NOT NULL DEFAULT '',
    retry_at      INTEGER,
    attempted_at  INTEGER NOT NULL
);
CREATE INDEX download_attempt_download ON download_attempt (download_id);

-- Pacing per provider, persisted so a restart neither forgets a back-off nor refills a window. window_* counts adds
-- in the current minute, hour_* uncached adds (and TorBox's not-cached refusals) in the current clock hour.
-- cooldown_until follows a burst (the minute's adds used up) or Real-Debrid's account hold; backoff_until follows
-- errors, failures counting them for the exponential back-off.
CREATE TABLE router_pacing (
    provider        TEXT    PRIMARY KEY,
    window_start    INTEGER NOT NULL DEFAULT 0,
    window_adds     INTEGER NOT NULL DEFAULT 0,
    hour_start      INTEGER NOT NULL DEFAULT 0,
    hour_uncached   INTEGER NOT NULL DEFAULT 0,
    cooldown_until  INTEGER NOT NULL DEFAULT 0,
    backoff_until   INTEGER NOT NULL DEFAULT 0,
    failures        INTEGER NOT NULL DEFAULT 0,
    reason          TEXT    NOT NULL DEFAULT '',
    updated_at      INTEGER NOT NULL
);

-- What the router would have done for a version it may not write for: mode shadow (the version's mode) or disabled
-- (router.enabled is false). outcome would_add names the release and provider; none means no candidate could be
-- placed; deferred means every provider was paced. steps is the JSON list of every release and provider considered
-- with the reason. The shadow report reads these.
CREATE TABLE router_plan (
    id             INTEGER PRIMARY KEY,
    title_id       INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    version_key    TEXT    NOT NULL,
    mode           TEXT    NOT NULL CHECK (mode IN ('shadow', 'disabled')),
    outcome        TEXT    NOT NULL CHECK (outcome IN ('would_add', 'none', 'deferred')),
    release_title  TEXT    NOT NULL DEFAULT '',
    indexer        TEXT    NOT NULL DEFAULT '',
    info_hash      TEXT    NOT NULL DEFAULT '',
    provider       TEXT    NOT NULL DEFAULT '',
    steps          TEXT    NOT NULL DEFAULT '[]',
    source         TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL
);
CREATE INDEX router_plan_version ON router_plan (version_key, created_at);
CREATE INDEX router_plan_title ON router_plan (title_id, created_at);

COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;

DROP TABLE router_plan;
DROP TABLE router_pacing;
DROP TABLE download_attempt;
DROP TABLE download;

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

COMMIT;
PRAGMA foreign_keys = ON;
