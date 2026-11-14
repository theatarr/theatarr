-- History's facts, recorded when they happen (docs/design/ui.md "Activity", History). A link row is reused when a
-- later file takes its path, and a provider_copy row when the same copy is asked for again, so neither can say
-- afterwards what an event was about.
--
-- library_action.release_title, quality, score: the release of the file the action is about, as it was then: a link's
-- new file (an import or an adoption), a replace's old file (the one deleted for an upgrade or removed by hand).
-- NULL on rows from before this migration and when unknown; History then shows what the link rows still tell, or
-- nothing.
--
-- provider_copy_event: one row per copy that ended added or not_cached ("Add to other providers", router copy.go),
-- with the release name, quality and score of the file it was asked for. Kept by design, like the journal: a few
-- rows per copied torrent.

-- +goose Up
ALTER TABLE library_action ADD COLUMN release_title TEXT;
ALTER TABLE library_action ADD COLUMN quality TEXT;
ALTER TABLE library_action ADD COLUMN score INTEGER;

CREATE TABLE provider_copy_event (
    id         INTEGER PRIMARY KEY,
    at         INTEGER NOT NULL,
    info_hash  TEXT    NOT NULL,
    name       TEXT    NOT NULL DEFAULT '',
    title_id   INTEGER REFERENCES title (id) ON DELETE SET NULL,
    provider   TEXT    NOT NULL,
    state      TEXT    NOT NULL CHECK (state IN ('added', 'not_cached')),
    message    TEXT    NOT NULL DEFAULT '',
    quality    TEXT,
    score      INTEGER
);
CREATE INDEX provider_copy_event_at ON provider_copy_event (at);

-- +goose Down
DROP TABLE provider_copy_event;
ALTER TABLE library_action DROP COLUMN score;
ALTER TABLE library_action DROP COLUMN quality;
ALTER TABLE library_action DROP COLUMN release_title;
