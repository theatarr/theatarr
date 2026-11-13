-- One search schedule (internal/acquire/schedule.go): search_request holds, per row of the Wanted page (one library's
-- missing or cutoff-unmet part of a film or of one season), when it is next searched. next_at is in milliseconds;
-- tries counts the empty searches of new content in a row (or the searches no indexer answered, for other rows); why
-- is NULL for the regular schedule, 'asked' for Search now, 'new' for the new-content retries, else the failure the
-- router reported.
--
-- Search now requests still queued stay due and asked. The queued Search now drains (acquire.searchnow) go: the
-- schedule starts those rows. Queued retry chains of new content (acquire.research with reason fresh) go: the
-- schedule retries new content by itself.
-- +goose Up
ALTER TABLE search_request ADD COLUMN next_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE search_request ADD COLUMN tries INTEGER NOT NULL DEFAULT 0;
ALTER TABLE search_request ADD COLUMN why TEXT;
UPDATE search_request SET next_at = created_at, why = 'asked';
CREATE INDEX search_request_next_at ON search_request (next_at);

DELETE FROM job WHERE state = 'queued' AND (kind = 'acquire.searchnow' OR (kind = 'acquire.research'
    AND json_valid(payload) AND json_extract(payload, '$.reason') = 'fresh'));

-- +goose Down
DROP INDEX search_request_next_at;
ALTER TABLE search_request DROP COLUMN why;
ALTER TABLE search_request DROP COLUMN tries;
ALTER TABLE search_request DROP COLUMN next_at;
