-- The arr views decide anime for a title their add creates when the request says nothing about it
-- (docs/design/arrview.md "Writes"). anime_source records where the decision came from, so a wrong guess is visible:
-- request (series type, root folder or profile), mapping (Theatarr's anime mapping data), tmdb (Japanese-language
-- animation, or not), none (no data: not anime). '' for records from before, and for adds of titles already held.
-- +goose Up
ALTER TABLE arrview_title ADD COLUMN anime_source TEXT NOT NULL DEFAULT ''
    CHECK (anime_source IN ('', 'request', 'mapping', 'tmdb', 'none'));

-- +goose Down
ALTER TABLE arrview_title DROP COLUMN anime_source;
