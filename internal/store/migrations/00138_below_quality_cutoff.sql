-- "Below the quality cutoff" stored with the file state it judges (internal/decide/cutoff_store.go): a movie
-- version's current file (version), an episode's file (episode_version_state). Wanted › Cutoff unmet and the search
-- round's cutoff targets read it. 0 until the library's first state pass after the upgrade judges every library
-- (it runs once per process start).
-- +goose Up
ALTER TABLE version ADD COLUMN below_quality_cutoff INTEGER NOT NULL DEFAULT 0 CHECK (below_quality_cutoff IN (0, 1));
ALTER TABLE episode_version_state ADD COLUMN below_quality_cutoff INTEGER NOT NULL DEFAULT 0
    CHECK (below_quality_cutoff IN (0, 1));

-- +goose Down
ALTER TABLE episode_version_state DROP COLUMN below_quality_cutoff;
ALTER TABLE version DROP COLUMN below_quality_cutoff;
