-- Searches asked for on the Wanted page ("Search now", internal/acquire/searchnow.go): one row of the page, which is
-- one library's missing or cutoff-unmet targets of a movie or of a series season (season -1: a movie). The acquirer
-- starts them oldest first under the wanted loop's hourly budget and deletes each when its search was started or it
-- asks for nothing any more. Asking again for a queued row keeps its place.
-- +goose Up
CREATE TABLE search_request (
    id          INTEGER PRIMARY KEY,
    title_id    INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    version_key TEXT    NOT NULL,
    reason      TEXT    NOT NULL CHECK (reason IN ('missing', 'cutoff')),
    season      INTEGER NOT NULL DEFAULT -1,
    created_at  INTEGER NOT NULL,
    UNIQUE (title_id, version_key, reason, season)
);

-- +goose Down
DROP TABLE search_request;
