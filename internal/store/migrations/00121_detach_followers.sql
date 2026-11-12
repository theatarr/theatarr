-- Following is gone (docs/design/library.md). A library that followed another becomes an ordinary library: what it
-- inherited is copied to its own rows first, then the follow tables go.
--
-- Naming: a naming row that took its naming from another library (FromVersion, the Go field name: naming rows are
-- policy.Naming as JSON) gets that library's row verbatim, empty templates and FromArr included, or '{}' (the
-- built-in templates) when it has none. Then every naming row loses FromVersion and every library row its follows.
--
-- Minimum availability: a former follower's copy of a title read the followed copy's arr record. Where that value
-- differs from the library's own default, it becomes the library's own choice (arrview_title v/<key>) with
-- updated_at 0, so any newer choice still wins. A row with no value (a list sync's) takes it, also with updated_at
-- 0; a row that already holds a choice keeps it.
--
-- Series type: a series with none takes the first arr record's (by instance, arr id).
-- +goose Up
UPDATE setting_version_naming SET config = COALESCE((SELECT s.config FROM setting_version_naming s
    WHERE s.key = json_extract(setting_version_naming.config, '$.FromVersion')), '{}')
    WHERE COALESCE(json_extract(config, '$.FromVersion'), '') <> '';
UPDATE setting_version_naming SET config = json_remove(config, '$.FromVersion')
    WHERE json_type(config, '$.FromVersion') IS NOT NULL;
UPDATE setting_version SET config = json_remove(config, '$.config.follows')
    WHERE json_type(config, '$.config.follows') IS NOT NULL;

INSERT INTO arrview_title (view, title_id, minimum_availability, added_at, updated_at)
SELECT 'v/' || v.policy_key, v.title_id, i.m, 0, 0 FROM (SELECT f.version_id,
        (SELECT a.minimum_availability FROM arr_title a LEFT JOIN version_source vs ON vs.version_id = a.version_id
            AND vs.instance = a.instance AND vs.arr_id = a.arr_id WHERE a.version_id = f.follows_id
            ORDER BY (vs.instance IS NOT NULL) DESC, a.instance LIMIT 1) AS m FROM version_follow f) i
    JOIN version v ON v.id = i.version_id
    WHERE COALESCE(i.m, '') <> '' AND i.m <> COALESCE(NULLIF((SELECT json_extract(sv.config,
        '$.config.minimum_availability') FROM setting_version sv WHERE sv.key = v.policy_key), ''), 'released')
ON CONFLICT (view, title_id) DO UPDATE SET minimum_availability = excluded.minimum_availability, updated_at = 0
    WHERE arrview_title.minimum_availability = '';

UPDATE title SET series_type = (SELECT a.series_type FROM arr_title a WHERE a.title_id = title.id
        AND a.kind = 'series' AND a.series_type <> '' ORDER BY a.instance, a.arr_id LIMIT 1)
    WHERE kind = 'series' AND COALESCE(series_type, '') = '' AND EXISTS (SELECT 1 FROM arr_title a
        WHERE a.title_id = title.id AND a.kind = 'series' AND a.series_type <> '');

-- Nothing claims a library.adopt job any more.
DELETE FROM job WHERE kind = 'library.adopt' AND state = 'queued';

DROP TABLE follow_search_intent;
DROP TABLE version_follow;

-- +goose Down
CREATE TABLE version_follow (
    version_id INTEGER PRIMARY KEY REFERENCES version (id) ON DELETE CASCADE,
    follows_id INTEGER NOT NULL REFERENCES version (id) ON DELETE CASCADE,
    CHECK (version_id <> follows_id)
);
CREATE TABLE follow_search_intent (
    id          INTEGER PRIMARY KEY,
    title_id    INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    version_key TEXT    NOT NULL,
    all_items   INTEGER NOT NULL DEFAULT 0 CHECK (all_items IN (0, 1)),
    episodes    TEXT    NOT NULL DEFAULT '[]',
    attempts    INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE (title_id, version_key)
);
