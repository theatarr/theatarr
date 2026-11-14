-- History's facts, recorded when they happen (docs/design/ui.md "Activity", History). A link row is reused when a
-- later file takes its path, and a provider_copy row when the same copy is asked for again, so neither can say
-- afterwards what an event was about.
--
-- library_action.release_title, quality, score: the release of the file the action is about, as it was then: a link's
-- new file (an import or an adoption), a replace's old file (the one deleted for an upgrade or removed by hand).
-- NULL on rows from before this migration and when unknown; History then shows what the link rows still tell, or
-- nothing.
--
-- library_action.title_id, episodes: the title the action is about and the episode ids (JSON) its file held then, since
-- a link row can be deleted (a change of episodes) or its episodes changed later. NULL on older rows and when unknown.
--
-- provider_copy_event: one row per copy that ended added, already (the provider held the torrent already) or
-- not_cached ("Add to other providers", router copy.go), with the release name, quality and score of the file it was
-- asked for. Kept by design, like the journal: a few rows per copied torrent. Seeded from the copies already finished,
-- with what they kept (no quality or score).

-- +goose Up
ALTER TABLE library_action ADD COLUMN release_title TEXT;
ALTER TABLE library_action ADD COLUMN quality TEXT;
ALTER TABLE library_action ADD COLUMN score INTEGER;
ALTER TABLE library_action ADD COLUMN title_id INTEGER;
ALTER TABLE library_action ADD COLUMN episodes TEXT;

CREATE TABLE provider_copy_event (
    id         INTEGER PRIMARY KEY,
    at         INTEGER NOT NULL,
    info_hash  TEXT    NOT NULL,
    name       TEXT    NOT NULL DEFAULT '',
    title_id   INTEGER REFERENCES title (id) ON DELETE SET NULL,
    provider   TEXT    NOT NULL,
    state      TEXT    NOT NULL CHECK (state IN ('added', 'already', 'not_cached')),
    message    TEXT    NOT NULL DEFAULT '',
    quality    TEXT,
    score      INTEGER
);
CREATE INDEX provider_copy_event_at ON provider_copy_event (at);
INSERT INTO provider_copy_event (at, info_hash, name, title_id, provider, state, message)
SELECT updated_at, info_hash, name, title_id, provider,
    CASE WHEN state = 'added' AND message LIKE 'Already on %' THEN 'already' ELSE state END, message
FROM provider_copy WHERE state IN ('added', 'not_cached') ORDER BY updated_at, id;

-- +goose Down
DROP TABLE provider_copy_event;
ALTER TABLE library_action DROP COLUMN episodes;
ALTER TABLE library_action DROP COLUMN title_id;
ALTER TABLE library_action DROP COLUMN score;
ALTER TABLE library_action DROP COLUMN quality;
ALTER TABLE library_action DROP COLUMN release_title;
