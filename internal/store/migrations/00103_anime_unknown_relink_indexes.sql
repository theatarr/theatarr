-- An arr-view add whose anime check could not reach TMDb records 'unknown' rather than 'none': the title
-- is not anime for now, and the next add, PUT or refresh of it asks TMDb again and turns on the anime versions when
-- the answer is anime (docs/design/arrview.md "Writes"). SQLite cannot change a CHECK, so the column is replaced.
-- +goose Up
ALTER TABLE arrview_title ADD COLUMN anime_source_new TEXT NOT NULL DEFAULT ''
    CHECK (anime_source_new IN ('', 'request', 'mapping', 'tmdb', 'none', 'unknown'));
UPDATE arrview_title SET anime_source_new = anime_source;
ALTER TABLE arrview_title DROP COLUMN anime_source;
ALTER TABLE arrview_title RENAME COLUMN anime_source_new TO anime_source;
-- The relink sweep looks each healthy link's folder up in the registry by mount folder, name and provider folder
-- name (e2eperf 3); without these each lookup scanned the whole registry.
CREATE INDEX content_mount_folder ON content (mount_folder);
CREATE INDEX content_name ON content (name);
CREATE INDEX content_provider_folder ON content_provider (folder_name);

-- +goose Down
DROP INDEX content_provider_folder;
DROP INDEX content_name;
DROP INDEX content_mount_folder;
ALTER TABLE arrview_title ADD COLUMN anime_source_old TEXT NOT NULL DEFAULT ''
    CHECK (anime_source_old IN ('', 'request', 'mapping', 'tmdb', 'none'));
UPDATE arrview_title SET anime_source_old = CASE anime_source WHEN 'unknown' THEN 'none' ELSE anime_source END;
ALTER TABLE arrview_title DROP COLUMN anime_source;
ALTER TABLE arrview_title RENAME COLUMN anime_source_old TO anime_source;
