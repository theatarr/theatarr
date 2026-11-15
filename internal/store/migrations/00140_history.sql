-- History stored as rows (docs/design/ui.md "Activity", History): one row per event, written when it happens
-- (internal/store history.go), with the facts History's wording needs. This step writes the rows beside the derived
-- reader (internal/journal history.go), which stays the reader until the readers are cut over; a test compares the
-- two row for row. The one intended difference: an upgrade's replaced files are one row per download and library
-- ("Upgraded: N files replaced") listing the files in replaced, instead of one row per old release.
--
-- ref is the source's journal id, as the derived reader names its rows: dl:<download> (grabbed), dl:<id>:fail,
-- dl:<id>:import, dl:<id>:held (an import held for review; dl:<id>:import there), lib:<library_action> (a group's
-- first action), bl:<router_blocklist rowid> (bl:<rowid>:<at> once the entry is deleted), act:<journal_action>
-- (title_removed), copy:<provider_copy_event>. kind is the journal kind (grab or upgrade for a grab; stall or failure;
-- import; link; blocklist; action; copy). release is NULL when the release is not known; for a deleted (upgrade) row
-- release, quality and score are what replaced the files, and replaced lists the files: [{id, at, path, release,
-- quality, score, episodes}]. episodes are episode ids (JSON). reason is the raw failure, block, held-import or copy
-- message; detail the library action's or the removal's detail (an unlink's reason, a removal's outcome); attempts
-- stay in download_attempt (download_id).
--
-- Backfilled once from download, download_attempt, library_import, library_action, router_blocklist, journal_action
-- (removals) and provider_copy_event (copies "Add to other providers" finished; no new ones are written). No request
-- events: History has none.

-- +goose Up
CREATE TABLE history (
    id           INTEGER PRIMARY KEY,
    ref          TEXT    NOT NULL UNIQUE,
    at           INTEGER NOT NULL,
    event        TEXT    NOT NULL,
    kind         TEXT    NOT NULL,
    title_id     INTEGER NOT NULL DEFAULT 0,
    version_key  TEXT    NOT NULL DEFAULT '',
    download_id  INTEGER,
    release      TEXT,
    info_hash    TEXT    NOT NULL DEFAULT '',
    indexer      TEXT    NOT NULL DEFAULT '',
    size         INTEGER NOT NULL DEFAULT 0,
    quality      TEXT    NOT NULL DEFAULT '',
    score        INTEGER,
    formats      TEXT,
    episodes     TEXT    NOT NULL DEFAULT '[]',
    files        INTEGER NOT NULL DEFAULT 0,
    replaced     TEXT,
    provider     TEXT    NOT NULL DEFAULT '',
    reason       TEXT    NOT NULL DEFAULT '',
    source       TEXT    NOT NULL DEFAULT '',
    import_state TEXT    NOT NULL DEFAULT '',
    by           TEXT    NOT NULL DEFAULT '',
    detail       TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX history_at ON history (at, ref);
CREATE INDEX history_title ON history (title_id, at);
CREATE INDEX history_download ON history (download_id) WHERE download_id IS NOT NULL;

-- Downloads (grabbed, failed, imported, held), blocklist entries, removals, copies, then the library actions; the
-- event writers (internal/store history.go) write the same rows, and store.BackfillHistory runs these statements.
INSERT INTO history (ref, at, event, kind, title_id, version_key, download_id, release, info_hash, indexer, size,
    quality, score, formats, episodes, files, replaced, provider, reason, source, import_state, by, detail)
    SELECT 'dl:' || d.id, COALESCE(d.added_at, (SELECT MIN(a.attempted_at) FROM download_attempt a
        WHERE a.download_id = d.id AND a.outcome = 'accepted'), d.created_at), 'grabbed', CASE WHEN EXISTS (SELECT 1 FROM download p WHERE p.version_id = d.version_id AND p.id <> d.id
        AND p.state = 'imported' AND p.imported_at < d.created_at
        AND (d.episodes = '[]' OR EXISTS (SELECT 1 FROM json_each(p.episodes) pe
            WHERE pe.value IN (SELECT value FROM json_each(d.episodes)))))
        THEN 'upgrade' ELSE 'grab' END,
        d.title_id, d.version_key, d.id, d.release_title, d.info_hash, d.indexer,
        COALESCE(d.size_bytes, 0), d.quality, CASE WHEN d.formats IS NOT NULL OR d.score <> 0 THEN d.score END, d.formats, d.episodes, 0, NULL, COALESCE(d.provider, ''), '', '', '', '', ''
    FROM download d WHERE (d.state NOT IN ('queued', 'adding', 'failed') OR d.added_at IS NOT NULL OR d.reused = 1
    OR EXISTS (SELECT 1 FROM download_attempt a WHERE a.download_id = d.id AND a.outcome = 'accepted'));

INSERT INTO history (ref, at, event, kind, title_id, version_key, download_id, release, info_hash, indexer, size,
    quality, score, formats, episodes, files, replaced, provider, reason, source, import_state, by, detail)
    SELECT 'dl:' || d.id || ':fail', d.updated_at, 'failed',
        CASE WHEN d.error LIKE 'stalled%' THEN 'stall' ELSE 'failure' END,
        d.title_id, d.version_key, d.id, d.release_title, d.info_hash, d.indexer,
        COALESCE(d.size_bytes, 0), d.quality, CASE WHEN d.formats IS NOT NULL OR d.score <> 0 THEN d.score END, d.formats, d.episodes, 0, NULL, COALESCE(d.provider, ''), COALESCE(d.error, ''), '', '',
        '', ''
    FROM download d WHERE d.state = 'failed';

INSERT INTO history (ref, at, event, kind, title_id, version_key, download_id, release, info_hash, indexer, size,
    quality, score, formats, episodes, files, replaced, provider, reason, source, import_state, by, detail)
    SELECT 'dl:' || d.id || ':import', li.updated_at, 'imported', 'import', d.title_id, d.version_key, d.id,
        d.release_title, d.info_hash, d.indexer, COALESCE(d.size_bytes, 0), d.quality,
        CASE WHEN d.formats IS NOT NULL OR d.score <> 0 THEN d.score END, d.formats,
        (SELECT json_group_array(DISTINCT j.value) FROM library_action a, json_each(COALESCE(a.episodes,
            (SELECT json_group_array(le.episode_id) FROM link_episode le WHERE le.link_id = a.link_id))) j
            WHERE a.kind = 'link' AND a.mode = 'applied' AND a.download_id = d.id),
        0, NULL, COALESCE(d.provider, ''), '', '', li.state, '', ''
    FROM library_import li JOIN download d ON d.id = li.download_id
    WHERE li.state IN ('linked', 'partial');

INSERT INTO history (ref, at, event, kind, title_id, version_key, download_id, release, info_hash, indexer, size,
    quality, score, formats, episodes, files, replaced, provider, reason, source, import_state, by, detail)
    SELECT 'dl:' || d.id || ':held', li.updated_at, 'failed', 'import', d.title_id, d.version_key, d.id,
        d.release_title, d.info_hash, d.indexer, COALESCE(d.size_bytes, 0), d.quality,
        CASE WHEN d.formats IS NOT NULL OR d.score <> 0 THEN d.score END, d.formats, '[]', 0, NULL,
        COALESCE(d.provider, ''), COALESCE(li.last_error, ''), '', li.state, '', ''
    FROM library_import li JOIN download d ON d.id = li.download_id
    WHERE li.state = 'rejected' AND d.state <> 'failed';

INSERT INTO history (ref, at, event, kind, title_id, version_key, download_id, release, info_hash, indexer, size,
    quality, score, formats, episodes, files, replaced, provider, reason, source, import_state, by, detail)
SELECT 'bl:' || b.rowid, b.created_at, 'blocked', 'blocklist', COALESCE(b.title_id, 0), b.version_key,
    b.download_id, b.release_title, b.info_hash, COALESCE(d.indexer, ''), COALESCE(d.size_bytes, 0),
    COALESCE(d.quality, ''), CASE WHEN d.formats IS NOT NULL OR d.score <> 0 THEN d.score END, NULL, '[]', 0, NULL,
    '', b.reason, b.source, '', '', ''
FROM router_blocklist b LEFT JOIN download d ON d.id = b.download_id;

INSERT INTO history (ref, at, event, kind, title_id, version_key, download_id, release, info_hash, indexer, size,
    quality, score, formats, episodes, files, replaced, provider, reason, source, import_state, by, detail)
SELECT 'act:' || a.id, a.created_at, 'title_removed', 'action', COALESCE(a.title_id, 0), a.version_key, NULL, NULL,
    '', '', 0, '', NULL, NULL, '[]', 0, NULL, '', '', '', '', a.by, a.detail
FROM journal_action a WHERE a.action IN ('remove_title', 'remove_version');

INSERT INTO history (ref, at, event, kind, title_id, version_key, download_id, release, info_hash, indexer, size,
    quality, score, formats, episodes, files, replaced, provider, reason, source, import_state, by, detail)
SELECT 'copy:' || c.id, c.at, CASE c.state WHEN 'added' THEN 'copy_added' WHEN 'already' THEN 'copy_already'
    ELSE 'copy_not_cached' END, 'copy', COALESCE(c.title_id, 0), '', NULL, c.name, c.info_hash, '', 0,
    COALESCE(c.quality, ''), c.score, NULL, '[]', 0, NULL, c.provider, c.message, '', '', '', ''
FROM provider_copy_event c;

-- removed: one row per action.
WITH m AS (SELECT la.id, la.at, COALESCE(la.title_id, (SELECT title_id FROM download WHERE id = la.download_id),
        (SELECT title_id FROM link WHERE id = la.link_id), 0) AS title_id,
        COALESCE(la.version_key, '') AS version_key, la.download_id, COALESCE(la.path, '') AS path,
        COALESCE(la.release_title, (SELECT release_title FROM link WHERE id = (SELECT l.id FROM link l WHERE l.state = 'unlinked'
        AND (l.id = la.link_id OR (la.link_id IS NULL AND l.path = la.path)) ORDER BY l.id DESC LIMIT 1)), '') AS rel,
        CASE WHEN la.release_title IS NOT NULL OR la.quality IS NOT NULL THEN la.quality
            ELSE (SELECT quality FROM link WHERE id = (SELECT l.id FROM link l WHERE l.state = 'unlinked'
        AND (l.id = la.link_id OR (la.link_id IS NULL AND l.path = la.path)) ORDER BY l.id DESC LIMIT 1)) END AS quality,
        CASE WHEN la.release_title IS NOT NULL OR la.quality IS NOT NULL THEN la.score
            ELSE (SELECT score FROM link WHERE id = (SELECT l.id FROM link l WHERE l.state = 'unlinked'
        AND (l.id = la.link_id OR (la.link_id IS NULL AND l.path = la.path)) ORDER BY l.id DESC LIMIT 1)) END AS score,
        COALESCE(la.episodes, (SELECT json_group_array(le.episode_id) FROM link_episode le WHERE le.link_id =
            COALESCE((SELECT l.id FROM link l WHERE l.state = 'unlinked'
        AND (l.id = la.link_id OR (la.link_id IS NULL AND l.path = la.path)) ORDER BY l.id DESC LIMIT 1), la.link_id, (SELECT id FROM link WHERE path = la.path ORDER BY id DESC LIMIT 1), 0))) AS episodes,
        la.detail
    FROM library_action la WHERE la.kind = 'replace' AND la.mode = 'applied' AND la.download_id IS NULL
        AND la.detail NOT LIKE 'change of episodes failed%')
INSERT INTO history (ref, at, event, kind, title_id, version_key, download_id, release, info_hash, indexer, size,
    quality, score, formats, episodes, files, replaced, provider, reason, source, import_state, by, detail)
SELECT 'lib:' || m.id, m.at, 'removed', 'link', m.title_id, m.version_key, m.download_id,
    CASE WHEN m.rel <> '' OR m.quality IS NOT NULL THEN m.rel END, '', '', 0, COALESCE(m.quality, ''), m.score, NULL,
    m.episodes, 1, NULL, '', '', '', '', '', m.detail
FROM m;

-- missing: one row per action.
WITH m AS (SELECT la.id, la.at, COALESCE(la.title_id, (SELECT title_id FROM download WHERE id = la.download_id),
        (SELECT title_id FROM link WHERE id = la.link_id), 0) AS title_id,
        COALESCE(la.version_key, '') AS version_key, la.download_id, COALESCE(la.path, '') AS path,
        COALESCE(la.release_title, (SELECT release_title FROM link WHERE id = (SELECT l.id FROM link l WHERE l.id = la.link_id AND l.state = 'dead')), '') AS rel,
        CASE WHEN la.release_title IS NOT NULL OR la.quality IS NOT NULL THEN la.quality
            ELSE (SELECT quality FROM link WHERE id = (SELECT l.id FROM link l WHERE l.id = la.link_id AND l.state = 'dead')) END AS quality,
        CASE WHEN la.release_title IS NOT NULL OR la.quality IS NOT NULL THEN la.score
            ELSE (SELECT score FROM link WHERE id = (SELECT l.id FROM link l WHERE l.id = la.link_id AND l.state = 'dead')) END AS score,
        COALESCE(la.episodes, (SELECT json_group_array(le.episode_id) FROM link_episode le WHERE le.link_id =
            COALESCE((SELECT l.id FROM link l WHERE l.id = la.link_id AND l.state = 'dead'), la.link_id, (SELECT id FROM link WHERE path = la.path ORDER BY id DESC LIMIT 1), 0))) AS episodes,
        la.detail
    FROM library_action la WHERE la.kind = 'dead' AND la.mode = 'applied')
INSERT INTO history (ref, at, event, kind, title_id, version_key, download_id, release, info_hash, indexer, size,
    quality, score, formats, episodes, files, replaced, provider, reason, source, import_state, by, detail)
SELECT 'lib:' || m.id, m.at, 'missing', 'link', m.title_id, m.version_key, m.download_id,
    CASE WHEN m.rel <> '' OR m.quality IS NOT NULL THEN m.rel END, '', '', 0, COALESCE(m.quality, ''), m.score, NULL,
    m.episodes, 1, NULL, '', '', '', '', '', m.detail
FROM m;

-- deleted: an upgrade's replaced files, one row per download and library, the files listed in replaced.
WITH m AS (SELECT la.id, la.at, COALESCE(la.title_id, (SELECT title_id FROM download WHERE id = la.download_id),
        (SELECT title_id FROM link WHERE id = la.link_id), 0) AS title_id,
        COALESCE(la.version_key, '') AS version_key, la.download_id, COALESCE(la.path, '') AS path,
        COALESCE(la.release_title, (SELECT release_title FROM link WHERE id = (SELECT l.id FROM link l WHERE l.state = 'unlinked'
        AND (l.id = la.link_id OR (la.link_id IS NULL AND l.path = la.path)) ORDER BY l.id DESC LIMIT 1)), '') AS rel,
        CASE WHEN la.release_title IS NOT NULL OR la.quality IS NOT NULL THEN la.quality
            ELSE (SELECT quality FROM link WHERE id = (SELECT l.id FROM link l WHERE l.state = 'unlinked'
        AND (l.id = la.link_id OR (la.link_id IS NULL AND l.path = la.path)) ORDER BY l.id DESC LIMIT 1)) END AS quality,
        CASE WHEN la.release_title IS NOT NULL OR la.quality IS NOT NULL THEN la.score
            ELSE (SELECT score FROM link WHERE id = (SELECT l.id FROM link l WHERE l.state = 'unlinked'
        AND (l.id = la.link_id OR (la.link_id IS NULL AND l.path = la.path)) ORDER BY l.id DESC LIMIT 1)) END AS score,
        COALESCE(la.episodes, (SELECT json_group_array(le.episode_id) FROM link_episode le WHERE le.link_id =
            COALESCE((SELECT l.id FROM link l WHERE l.state = 'unlinked'
        AND (l.id = la.link_id OR (la.link_id IS NULL AND l.path = la.path)) ORDER BY l.id DESC LIMIT 1), la.link_id, (SELECT id FROM link WHERE path = la.path ORDER BY id DESC LIMIT 1), 0))) AS episodes,
        la.detail
    FROM library_action la WHERE la.kind = 'replace' AND la.mode = 'applied' AND la.download_id IS NOT NULL
        AND la.detail NOT LIKE 'import undone%'),
g AS (SELECT download_id, version_key, MIN(id) AS first, MAX(at) AS at, COUNT(*) AS files,
        json_group_array(json_object('id', id, 'at', at, 'path', path, 'release', rel, 'quality', quality,
            'score', score, 'episodes', json(episodes))) AS replaced
    FROM m GROUP BY download_id, version_key),
e AS (SELECT m.download_id, m.version_key, json_group_array(DISTINCT j.value) AS episodes FROM m, json_each(m.episodes) j
    GROUP BY m.download_id, m.version_key)
INSERT INTO history (ref, at, event, kind, title_id, version_key, download_id, release, info_hash, indexer, size,
    quality, score, formats, episodes, files, replaced, provider, reason, source, import_state, by, detail)
SELECT 'lib:' || g.first, g.at, 'deleted', 'link', COALESCE(d.title_id, 0), g.version_key, g.download_id,
    d.release_title, '', '', 0, COALESCE(d.quality, ''), CASE WHEN d.formats IS NOT NULL OR d.score <> 0 THEN d.score END,
    NULL, COALESCE(e.episodes, '[]'), g.files, g.replaced, COALESCE(d.provider, ''), '', '', '', '', ''
FROM g LEFT JOIN e ON e.download_id = g.download_id AND e.version_key = g.version_key
    LEFT JOIN download d ON d.id = g.download_id;

-- adopted: one row per title, library, release and day, with its first file's quality and score.
WITH m AS (SELECT la.id, la.at, COALESCE(la.title_id, (SELECT title_id FROM download WHERE id = la.download_id),
        (SELECT title_id FROM link WHERE id = la.link_id), 0) AS title_id,
        COALESCE(la.version_key, '') AS version_key, la.download_id, COALESCE(la.path, '') AS path,
        COALESCE(la.release_title, (SELECT release_title FROM link WHERE id = (SELECT l.id FROM link l WHERE l.id = la.link_id AND l.adopted_from IS NOT NULL)), '') AS rel,
        CASE WHEN la.release_title IS NOT NULL OR la.quality IS NOT NULL THEN la.quality
            ELSE (SELECT quality FROM link WHERE id = (SELECT l.id FROM link l WHERE l.id = la.link_id AND l.adopted_from IS NOT NULL)) END AS quality,
        CASE WHEN la.release_title IS NOT NULL OR la.quality IS NOT NULL THEN la.score
            ELSE (SELECT score FROM link WHERE id = (SELECT l.id FROM link l WHERE l.id = la.link_id AND l.adopted_from IS NOT NULL)) END AS score,
        COALESCE(la.episodes, (SELECT json_group_array(le.episode_id) FROM link_episode le WHERE le.link_id =
            COALESCE((SELECT l.id FROM link l WHERE l.id = la.link_id AND l.adopted_from IS NOT NULL), la.link_id, (SELECT id FROM link WHERE path = la.path ORDER BY id DESC LIMIT 1), 0))) AS episodes,
        la.detail, la.at / 86400000 AS day
    FROM library_action la WHERE la.kind = 'link' AND la.mode = 'applied' AND la.download_id IS NULL AND la.detail LIKE 'adopted from %'),
g AS (SELECT title_id, version_key, rel, day, MIN(id) AS first, MAX(at) AS at, COUNT(*) AS files FROM m
    GROUP BY title_id, version_key, rel, day),
e AS (SELECT m.title_id, m.version_key, m.rel, m.day, json_group_array(DISTINCT j.value) AS episodes
    FROM m, json_each(m.episodes) j GROUP BY m.title_id, m.version_key, m.rel, m.day)
INSERT INTO history (ref, at, event, kind, title_id, version_key, download_id, release, info_hash, indexer, size,
    quality, score, formats, episodes, files, replaced, provider, reason, source, import_state, by, detail)
SELECT 'lib:' || g.first, g.at, 'adopted', 'link', g.title_id, g.version_key, NULL,
    CASE WHEN g.rel <> '' OR x.quality IS NOT NULL THEN g.rel END, '', '', 0, COALESCE(x.quality, ''), x.score, NULL,
    COALESCE(e.episodes, '[]'), g.files, NULL, '', '', '', '', '', x.detail
FROM g JOIN m x ON x.id = g.first
    LEFT JOIN e ON e.title_id = g.title_id AND e.version_key = g.version_key AND e.rel = g.rel AND e.day = g.day
WHERE true;

-- +goose Down
DROP TABLE history;
