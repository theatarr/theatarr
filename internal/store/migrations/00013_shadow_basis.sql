-- Shadow classification by release source (docs/design/shadow.md): an RSS grab is compared against our RSS picks near it
-- and a hand grab against our decision of it, so our later search is information on the row, not its basis.
-- search_better is that search's pick when it ranks above the row's own (JSON: release, indexer, quality, score);
-- NULL otherwise. Conventions as in 00001.
-- +goose Up
ALTER TABLE shadow_diff ADD COLUMN search_better TEXT;

-- +goose Down
ALTER TABLE shadow_diff DROP COLUMN search_better;
