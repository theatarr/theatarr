-- What the arr history gave for an adopted link, read by the offer sweep and kept so the link keeps qualifying for
-- cross-library offers once the arr history is gone: {"from": the link.adopted_from it was read for, "hash",
-- "release", "indexers": the arr's indexer names, "" for a grab without one}. NULL: not read yet. Adoption clears it
-- when it records a changed file.

-- +goose Up
ALTER TABLE link ADD COLUMN adopted_source TEXT;

-- +goose Down
ALTER TABLE link DROP COLUMN adopted_source;
