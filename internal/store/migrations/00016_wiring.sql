-- Wiring the router into the shadow runner and the arr views (docs/design/router.md "Triggers").
-- router_plan_id on shadow_grab and shadow_rss_pick is the router's dry-run plan for that grab or pick (what it would
-- add, where, or why not), shown in the shadow diff detail. acquire_run is one search for an active version: from an
-- arr view search command (command_id) or the wanted search loop. target names what was searched ('movie', 's01',
-- 's01e02'); the loop searches the targets searched longest ago first. route_job is the router.route job a search
-- with accepted releases enqueued. download.formats is the routed decision's matched custom formats with their scores
-- (JSON [{name, score}]; NULL for downloads routed before it), so the library names files from the decision instead of
-- deciding the release title again. router_blocklist holds hashes the router never routes again for a version: the
-- library's sample and runtime guard rejected their content. Nothing is deleted on any provider. Conventions as in
-- 00001.
-- +goose Up
ALTER TABLE shadow_grab ADD COLUMN router_plan_id INTEGER;
ALTER TABLE shadow_rss_pick ADD COLUMN router_plan_id INTEGER;

CREATE TABLE acquire_run (
    id           INTEGER PRIMARY KEY,
    source       TEXT    NOT NULL CHECK (source IN ('command', 'wanted')),
    command_id   INTEGER REFERENCES arrview_command (id) ON DELETE CASCADE,
    title_id     INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    version_id   INTEGER NOT NULL REFERENCES version (id) ON DELETE CASCADE,
    version_key  TEXT    NOT NULL,
    target       TEXT    NOT NULL,
    reason       TEXT    NOT NULL DEFAULT '' CHECK (reason IN ('', 'missing', 'cutoff')),
    season       INTEGER,
    episodes     TEXT    NOT NULL DEFAULT '[]',
    state        TEXT    NOT NULL CHECK (state IN ('queued', 'searching', 'routed', 'nothing', 'skipped', 'failed')),
    results      INTEGER NOT NULL DEFAULT 0,
    accepted     INTEGER NOT NULL DEFAULT 0,
    route_job    INTEGER,
    message      TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE INDEX acquire_run_command ON acquire_run (command_id) WHERE command_id IS NOT NULL;
CREATE INDEX acquire_run_target ON acquire_run (version_id, target, created_at);
CREATE INDEX acquire_run_source ON acquire_run (source, created_at);

ALTER TABLE download ADD COLUMN formats TEXT;

CREATE TABLE router_blocklist (
    version_key  TEXT    NOT NULL,
    info_hash    TEXT    NOT NULL,
    title_id     INTEGER REFERENCES title (id) ON DELETE CASCADE,
    release_title TEXT    NOT NULL DEFAULT '',
    reason       TEXT    NOT NULL,
    source       TEXT    NOT NULL,
    download_id  INTEGER,
    created_at   INTEGER NOT NULL,
    PRIMARY KEY (version_key, info_hash)
);

-- +goose Down
DROP TABLE router_blocklist;
ALTER TABLE download DROP COLUMN formats;
DROP TABLE acquire_run;
ALTER TABLE shadow_rss_pick DROP COLUMN router_plan_id;
ALTER TABLE shadow_grab DROP COLUMN router_plan_id;
