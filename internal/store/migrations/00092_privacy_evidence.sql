-- Durable privacy evidence for restores. A restore re-adds a backed-up hash only when nothing Theatarr
-- recorded makes it private (internal/torrents/restore.go). The records it read (downloads, router plans, shadow
-- rows, journal decisions) expire with retention, go with a deleted title, and arr history goes with a removed arr
-- instance; once gone, a known-private hash looked unseen and restored on the backup's proof alone. This table keeps
-- the compact part of that evidence for good: retention never prunes it and nothing cascades into it.
--
-- One row per hash, indexer and kind; first_seen and source are the first record of it (INSERT OR IGNORE).
--   private = 1: a verdict on the release itself: it was private (an item-level private flag, a PrivateToDebrid
--                rejection, the router's private refusal). It refuses the restore whatever the indexer's current
--                classification: a public source never erases a private item verdict.
--   private = 0: the hash was seen from this indexer (a policy key, an arr's name for it, or '' for none named).
--                A restore classifies the indexer by the current policy, as it does for live records: not a public
--                indexer of the policy (private, semi-private, unknown, unlisted, none named) refuses.
-- info_hash is the lower-case hex v1 hash. Written by store.RecordPrivacy wherever such a record is written.

-- +goose Up
CREATE TABLE privacy_evidence (
    info_hash   TEXT    NOT NULL,
    indexer     TEXT    NOT NULL DEFAULT '',
    private     INTEGER NOT NULL CHECK (private IN (0, 1)),
    source      TEXT    NOT NULL,
    first_seen  INTEGER NOT NULL,
    PRIMARY KEY (info_hash, indexer, private)
) WITHOUT ROWID;

-- Backfill from what is recorded now, before retention prunes more of it. first_seen is the record's own time. Every
-- JSON value read is checked with json_valid first: one malformed value must not stop the migration
-- and with it the service's start; such a value adds nothing.
INSERT OR IGNORE INTO privacy_evidence (info_hash, indexer, private, source, first_seen)
SELECT h, ix, p, src, MIN(at) FROM (
    SELECT lower(info_hash) AS h, COALESCE(indexer, '') AS ix, 0 AS p, 'download' AS src, created_at AS at
        FROM download WHERE info_hash IS NOT NULL AND info_hash <> ''
    UNION ALL SELECT lower(info_hash), COALESCE(indexer, ''), 1, 'download', created_at FROM download
        WHERE info_hash IS NOT NULL AND info_hash <> '' AND json_extract(CASE WHEN json_valid(release_data) THEN release_data END, '$.private') = 1
    UNION ALL SELECT lower(info_hash), COALESCE(indexer, ''), 0, 'router plan', created_at FROM router_plan
        WHERE info_hash IS NOT NULL AND info_hash <> ''
    UNION ALL SELECT lower(json_extract(s.value, '$.hash')), '', 1, 'router plan step', p.created_at
        FROM router_plan p, json_each(CASE WHEN json_valid(p.steps) THEN p.steps ELSE '[]' END) s
        WHERE s.type = 'object' AND json_extract(s.value, '$.code') = 'private'
        AND COALESCE(json_extract(s.value, '$.hash'), '') <> ''
    UNION ALL SELECT lower(info_hash), COALESCE(indexer, ''), 0, 'shadow RSS pick', seen_at FROM shadow_rss_pick
        WHERE info_hash IS NOT NULL AND info_hash <> ''
    UNION ALL SELECT lower(info_hash), COALESCE(CASE WHEN json_valid(data) THEN json_extract(data, '$.indexer') END, ''),
        0, 'arr history', date_at
        FROM arr_history WHERE info_hash IS NOT NULL AND info_hash <> ''
        AND (event_type = 'grabbed' OR COALESCE(CASE WHEN json_valid(data) THEN json_extract(data, '$.indexer') END,
            '') <> '')
    UNION ALL SELECT lower(json_extract(d.value, '$.info_hash')), COALESCE(json_extract(d.value, '$.indexer'), ''), 0,
        'shadow grab', g.created_at
        FROM shadow_grab g, json_each(json_array(
            json(CASE WHEN json_valid(g.pick) THEN g.pick ELSE 'null' END),
            json(CASE WHEN json_valid(g.arr_decision) THEN g.arr_decision ELSE 'null' END))) d
        WHERE d.type = 'object' AND COALESCE(json_extract(d.value, '$.info_hash'), '') <> ''
    UNION ALL SELECT lower(json_extract(d.value, '$.info_hash')), COALESCE(json_extract(d.value, '$.indexer'), ''), 0,
        'shadow grab', g.created_at
        FROM shadow_grab g, json_each(CASE WHEN json_valid(g.results) THEN g.results ELSE '[]' END) d
        WHERE d.type = 'object' AND COALESCE(json_extract(d.value, '$.info_hash'), '') <> ''
    UNION ALL SELECT lower(json_extract(d.value, '$.info_hash')), COALESCE(json_extract(d.value, '$.indexer'), ''), 1,
        'shadow grab', g.created_at
        FROM shadow_grab g, json_each(CASE WHEN json_valid(g.results) THEN g.results ELSE '[]' END) d
        WHERE d.type = 'object' AND COALESCE(json_extract(d.value, '$.info_hash'), '') <> ''
        AND EXISTS (SELECT 1 FROM json_each(d.value, '$.rejections') r
            WHERE json_extract(r.value, '$.reason') = 'PrivateToDebrid')
    UNION ALL SELECT lower(json_extract(c.value, '$.info_hash')), COALESCE(json_extract(c.value, '$.indexer'), ''), 1,
        'decision', j.created_at
        FROM journal_decision j, json_each(CASE WHEN json_valid(j.candidates) THEN j.candidates ELSE '[]' END) c
        WHERE c.type = 'object' AND COALESCE(json_extract(c.value, '$.info_hash'), '') <> ''
        AND EXISTS (SELECT 1 FROM json_each(c.value, '$.rejections') r
            WHERE json_extract(r.value, '$.reason') = 'PrivateToDebrid')
) WHERE length(h) = 40 GROUP BY h, ix, p;

-- +goose Down
DROP TABLE privacy_evidence;
