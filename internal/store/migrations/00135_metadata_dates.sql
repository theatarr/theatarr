-- Episode synopsis (K11): the overview the series' metadata source gives, filled on the next ordinary refresh.
-- +goose Up
ALTER TABLE episode ADD COLUMN overview TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE episode DROP COLUMN overview;
