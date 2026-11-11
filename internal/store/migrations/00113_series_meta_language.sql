-- A series' original language code as the metadata refresh read it (TVDB's "jpn", else TMDb's "ja"), so the searches
-- its first refresh releases are scored with it before meta.art writes title_meta. '' when unknown or before the
-- next refresh.

-- +goose Up
ALTER TABLE series_meta ADD COLUMN original_language TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE series_meta DROP COLUMN original_language;
