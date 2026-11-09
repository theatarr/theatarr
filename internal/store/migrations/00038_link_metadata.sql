-- Keep the original file inputs on the link so active decisions can re-score without an arr or a download row.
-- Old links retain every available input from their download/release; decide reconstructs the missing parse from
-- the frozen release title (Revision 0). New imports store the parsed fields too.
-- +goose Up
ALTER TABLE link ADD COLUMN file_metadata TEXT NOT NULL DEFAULT '{}';
ALTER TABLE download ADD COLUMN release_data TEXT;

UPDATE link SET
    release_title = COALESCE(NULLIF(release_title, ''), (SELECT release_title FROM download WHERE id = link.download_id)),
    quality = COALESCE(NULLIF(quality, ''), (SELECT quality FROM download WHERE id = link.download_id)),
    score = COALESCE(score, (SELECT score FROM download WHERE id = link.download_id));

UPDATE link SET file_metadata = (
    SELECT json_object(
        'SceneName', COALESCE(l.release_title, d.release_title, r.title, ''),
        'Quality', COALESCE(l.quality, d.quality, ''),
        'Score', COALESCE(l.score, d.score),
        'Formats', json(COALESCE(d.formats, '[]')),
        'Size', COALESCE(cf.size_bytes, d.size_bytes, r.size_bytes, 0),
        'InfoHash', COALESCE(c.info_hash, d.info_hash, r.info_hash, ''),
        'Languages', json((SELECT json_group_array(CASE j.type WHEN 'text' THEN j.value
            WHEN 'object' THEN json_extract(j.value, '$.name') END)
            FROM json_each(r.parsed, '$.languages') j WHERE j.type IN ('text', 'object'))),
        'flags', json(COALESCE(r.indexer_flags, '[]')))
    FROM link l LEFT JOIN download d ON d.id = l.download_id
    LEFT JOIN content_file cf ON cf.id = l.content_file_id LEFT JOIN content c ON c.id = cf.content_id
    LEFT JOIN release r ON r.id = COALESCE(
        (SELECT id FROM release WHERE info_hash = COALESCE(d.info_hash, c.info_hash) AND indexer = d.indexer
            ORDER BY last_seen_at DESC, id DESC LIMIT 1),
        (SELECT id FROM release WHERE info_hash = COALESCE(d.info_hash, c.info_hash)
            ORDER BY last_seen_at DESC, id DESC LIMIT 1))
    WHERE l.id = link.id
);

-- +goose Down
ALTER TABLE download DROP COLUMN release_data;
ALTER TABLE link DROP COLUMN file_metadata;
