-- Add-time searches still to make (internal/acquire, "Add-time searches"): a follower version's title that is new or
-- newly monitored (all_items), or its newly monitored episodes. The arr import writes them in its own transaction;
-- the acquirer starts their searches under the wanted loop's hourly budget, oldest first, and deletes each once its
-- searches were started or it asks for nothing any more. attempts counts drains that failed on it (bounded).
-- +goose Up
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

-- +goose Down
DROP TABLE follow_search_intent;
