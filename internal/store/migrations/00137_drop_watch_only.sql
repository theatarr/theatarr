-- Watch only (mode shadow) is gone (phase K, K6): a library is Live. The global Pause, each title's monitoring in a
-- library and THEATARR_FORCE_OFF stay. A stored library that was not Live (its mode not active; an empty mode read as
-- shadow) must not start grabbing on upgrade, so it becomes Live with:
--
-- * every title-library row of it not monitored (version.monitored 0), and each title's monitored flag recomputed
--   from its remaining rows, as catalog.SyncMonitored does;
-- * enable: request, so an arr client's add no longer turns it on by default;
-- * the global Pause switched on;
-- * one card on System › Errors (fingerprint watch-only) naming the libraries and what to do;
-- * no list, collection or account's request libraries adding to it (below).
--
-- 00134's arr-freeze cards no longer say to set the library Live. The mode key leaves every stored library (it is
-- ignored when read; versions.<key>.mode in a policy file is dropped before the strict decode). The dry-run plans
-- (router_plan), the verdicts on their journal entries and the RSS run log nothing read (shadow_run) go. Down
-- recreates the two tables empty; it cannot tell which library was Watch only.
-- +goose Up
CREATE TEMP TABLE k6_watch_only AS SELECT key FROM setting_version
    WHERE COALESCE(json_extract(config, '$.config.mode'), '') <> 'active';

UPDATE version SET monitored = 0, updated_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE monitored = 1 AND policy_key IN (SELECT key FROM k6_watch_only);
UPDATE title SET monitored = m.monitored, updated_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
FROM (SELECT t.id, (SELECT COALESCE(MAX(v.monitored), 0) FROM version v WHERE v.title_id = t.id
        AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)) AS monitored
    FROM title t WHERE t.id IN (SELECT title_id FROM version WHERE policy_key IN (SELECT key FROM k6_watch_only))) m
WHERE title.id = m.id AND title.monitored <> m.monitored;

UPDATE setting_version SET config = json_set(config, '$.config.enable', 'request'),
    updated_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE key IN (SELECT key FROM k6_watch_only);
UPDATE setting_version SET config = json_remove(config, '$.config.mode')
WHERE json_type(config, '$.config.mode') IS NOT NULL;

INSERT INTO setting_global_switches (key, config, created_at, updated_at)
SELECT 'global', '{"paused":true}', CAST(strftime('%s', 'now') AS INTEGER) * 1000,
    CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE EXISTS (SELECT 1 FROM k6_watch_only)
ON CONFLICT (key) DO UPDATE SET config = json_set(config, '$.paused', json('true')), updated_at = excluded.updated_at;

INSERT INTO error_event (fingerprint, source, component, level, message, first_seen, last_seen, updated_at)
SELECT 'watch-only', 'log', 'settings', 'error', 'Watch only is gone: every library is Live. ' ||
    CASE WHEN COUNT(*) = 1 THEN 'Library ' ELSE 'Libraries ' END || group_concat(key, ', ') ||
    CASE WHEN COUNT(*) = 1 THEN ' was' ELSE ' were' END || ' Watch only, so ' ||
    CASE WHEN COUNT(*) = 1 THEN 'its' ELSE 'their' END || ' titles are now not monitored, arr clients no longer add' ||
    ' to ' || CASE WHEN COUNT(*) = 1 THEN 'it' ELSE 'them' END || ', and Pause is on. Monitor what should download,' ||
    ' check Settings › Libraries, then turn Pause off.',
    CAST(strftime('%s', 'now') AS INTEGER) * 1000, CAST(strftime('%s', 'now') AS INTEGER) * 1000,
    CAST(strftime('%s', 'now') AS INTEGER) * 1000
FROM (SELECT key FROM k6_watch_only ORDER BY key)
WHERE true
HAVING COUNT(*) > 0
ON CONFLICT (fingerprint) DO UPDATE SET message = excluded.message, last_seen = excluded.last_seen, status = 'new',
    updated_at = excluded.updated_at;
UPDATE error_event SET message = replace(message, ' It is now Watch only. Check its profile and naming in Settings ›'
        || ' Libraries, then set it Live.', ' Check its profile and naming in Settings › Libraries.'),
    updated_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE fingerprint LIKE 'arr-freeze:%' AND message LIKE '%then set it Live.';

-- Lists and collections add monitored titles to their targets whatever enable says, and one without targets takes
-- every library that fits. So, with a converted library: its key leaves every list's and collection's targets; one
-- that targeted only converted libraries is turned off (a list disabled, a collection unmonitored); one without
-- targets gets the other libraries of its kind as targets (turned off when there are none); and the converted keys
-- leave each account's stored request libraries.
CREATE TEMP TABLE k6_live AS SELECT key, json_extract(config, '$.config.kind') AS kind FROM setting_version
    WHERE key NOT IN (SELECT key FROM k6_watch_only);

UPDATE import_list SET enabled = 0, updated_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE EXISTS (SELECT 1 FROM k6_watch_only) AND enabled = 1 AND NOT EXISTS (SELECT 1 FROM json_each(import_list.versions) j
        WHERE j.value NOT IN (SELECT key FROM k6_watch_only))
    AND (json_array_length(versions) > 0 OR NOT EXISTS (SELECT 1 FROM k6_live
        WHERE kind = CASE import_list.list_type WHEN 'sonarr' THEN 'series' ELSE 'movie' END));
UPDATE import_list SET versions = (SELECT json_group_array(j.value) FROM json_each(import_list.versions) j
        WHERE j.value NOT IN (SELECT key FROM k6_watch_only)),
    updated_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE EXISTS (SELECT 1 FROM json_each(import_list.versions) j WHERE j.value IN (SELECT key FROM k6_watch_only));
UPDATE import_list SET versions = (SELECT json_group_array(key) FROM (SELECT key FROM k6_live
        WHERE kind = CASE import_list.list_type WHEN 'sonarr' THEN 'series' ELSE 'movie' END ORDER BY key)),
    updated_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE EXISTS (SELECT 1 FROM k6_watch_only) AND enabled = 1 AND json_array_length(versions) = 0;

UPDATE movie_collection SET monitored = 0, updated_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE EXISTS (SELECT 1 FROM k6_watch_only) AND monitored = 1 AND NOT EXISTS (SELECT 1 FROM json_each(movie_collection.versions) j
        WHERE j.value NOT IN (SELECT key FROM k6_watch_only))
    AND (json_array_length(versions) > 0 OR NOT EXISTS (SELECT 1 FROM k6_live WHERE kind = 'movie'));
UPDATE movie_collection SET versions = (SELECT json_group_array(j.value) FROM json_each(movie_collection.versions) j
        WHERE j.value NOT IN (SELECT key FROM k6_watch_only)),
    updated_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE EXISTS (SELECT 1 FROM json_each(movie_collection.versions) j WHERE j.value IN (SELECT key FROM k6_watch_only));
UPDATE movie_collection SET versions = (SELECT json_group_array(key) FROM (SELECT key FROM k6_live WHERE kind = 'movie'
        ORDER BY key)), updated_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE EXISTS (SELECT 1 FROM k6_watch_only) AND monitored = 1 AND json_array_length(versions) = 0;

-- An account's request libraries: NULL (every library) and a list are both read through the defaults when empty, so
-- an account left with none, or with NULL, gets the Live libraries; with no Live library it is left as it was.
UPDATE web_user SET request_versions = COALESCE(
        (SELECT json_group_array(j.value) FROM json_each(web_user.request_versions) j
            WHERE j.value NOT IN (SELECT key FROM k6_watch_only) HAVING COUNT(*) > 0),
        (SELECT json_group_array(key) FROM (SELECT key FROM k6_live ORDER BY key)))
WHERE EXISTS (SELECT 1 FROM k6_watch_only) AND EXISTS (SELECT 1 FROM k6_live)
    AND (request_versions IS NULL OR (json_valid(request_versions) AND (json_array_length(request_versions) = 0
        OR EXISTS (SELECT 1 FROM json_each(web_user.request_versions) j WHERE j.value IN (SELECT key FROM k6_watch_only)))));

DROP TABLE k6_live;
DROP TABLE k6_watch_only;

DELETE FROM journal_verdict WHERE entry_id LIKE 'plan:%';
DROP TABLE router_plan;
DROP TABLE shadow_run;

-- +goose Down
CREATE TABLE shadow_run (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT    NOT NULL,
    started_at   INTEGER NOT NULL,
    finished_at  INTEGER NOT NULL,
    ok           INTEGER NOT NULL DEFAULT 1 CHECK (ok IN (0, 1)),
    summary      TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX shadow_run_kind ON shadow_run (kind, started_at);
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
