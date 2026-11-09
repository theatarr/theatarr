-- One logical acquire search may serve several version-target rows. Child rows share the root's indexer query.
-- +goose Up
ALTER TABLE acquire_run ADD COLUMN search_run INTEGER REFERENCES acquire_run (id) ON DELETE CASCADE;
CREATE INDEX acquire_run_search ON acquire_run (search_run) WHERE search_run IS NOT NULL;

-- +goose Down
DROP INDEX acquire_run_search;
ALTER TABLE acquire_run DROP COLUMN search_run;
