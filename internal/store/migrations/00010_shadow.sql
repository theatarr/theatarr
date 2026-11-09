-- The shadow runner (docs/design/shadow.md): every arr grab re-decided with our engine, our own search for the
-- same title, RSS picks, and the diff report. Summaries only: a decision is stored as a compact JSON summary
-- (release, indexer, quality, formats and score, rejections, upgrade), never a full search dump; a grab keeps its
-- best results only (policy shadow.keep_results). Conventions as in 00001.
-- +goose Up

-- Per arr instance, the last history id the runner has looked at. Grabs after it are shadowed; the first start
-- sets it to the instance's latest history id, so old history is never searched.
CREATE TABLE shadow_cursor (
    instance    TEXT    PRIMARY KEY,
    history_id  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- One arr grab re-decided. arr_decision is our decision of the arr's own release for the version, with the file the
-- version had at grab time (existing_source: version_file, history, or none). pick is our best release from our own
-- search plus the arr's release (NULL when nothing was acceptable); results are the best kept_results decisions of
-- the search. searched is 0 when no search ran (search off, or the hourly cap held it back to the end); then
-- search_note says why. found_arr: our search returned the arr's release (same hash or title).
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
    updated_at       INTEGER NOT NULL,
    UNIQUE (instance, history_id)
);
CREATE INDEX shadow_grab_title ON shadow_grab (title_id, version_key, grabbed_at);

-- RSS feed items already decided, so a feed read again decides only what is new. Pruned after 14 days.
CREATE TABLE shadow_rss_seen (
    indexer   TEXT    NOT NULL,
    item_key  TEXT    NOT NULL,
    seen_at   INTEGER NOT NULL,
    PRIMARY KEY (indexer, item_key)
);
CREATE INDEX shadow_rss_seen_at ON shadow_rss_seen (seen_at);

-- An RSS item we would grab for a monitored version that is wanted or below its cutoff. It stays open until an arr
-- grab of the same version links it (status linked, grab_id), or the link window passes (status expired, diff_id:
-- the "we'd grab, arr didn't" row).
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
    diff_id        INTEGER,
    UNIQUE (indexer, release_title, version_key)
);
CREATE INDEX shadow_rss_pick_open ON shadow_rss_pick (version_key, title_id, status);

-- The diff report: one row per arr grab (buckets agree, we_grab_another, arr_grabbed_we_wouldnt) and one per RSS
-- pick that no arr grab followed (we_grab_arr_didnt). reason is a machine-readable category, detail the free text.
-- at is the grab time, or the pick's first sighting.
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
);
CREATE INDEX shadow_diff_at ON shadow_diff (at);
CREATE INDEX shadow_diff_bucket ON shadow_diff (bucket, at);

-- Runner runs (arr.import, shadow.rss) with their JSON summary, for theatarr shadow status. Pruned after 30 days.
CREATE TABLE shadow_run (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT    NOT NULL,
    started_at   INTEGER NOT NULL,
    finished_at  INTEGER NOT NULL,
    ok           INTEGER NOT NULL DEFAULT 1 CHECK (ok IN (0, 1)),
    summary      TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX shadow_run_kind ON shadow_run (kind, started_at);

-- +goose Down
DROP TABLE shadow_run;
DROP TABLE shadow_diff;
DROP TABLE shadow_rss_pick;
DROP TABLE shadow_rss_seen;
DROP TABLE shadow_grab;
DROP TABLE shadow_cursor;
