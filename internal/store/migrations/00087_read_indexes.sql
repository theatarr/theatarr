-- Polling timestamp ranges and target-scoped file hash evidence. Keep the acquire target index covering
-- infra so wanted/add-time searches can ignore infrastructure failures without reading each historical row.
-- +goose Up
CREATE INDEX download_updated ON download (updated_at);
CREATE INDEX library_import_updated ON library_import (updated_at, download_id);
CREATE INDEX job_updated ON job (updated_at, id);
CREATE INDEX title_meta_refreshed ON title_meta (refreshed_at);
CREATE INDEX version_updated ON version (updated_at);
DROP INDEX acquire_run_target;
CREATE INDEX acquire_run_target ON acquire_run (version_id, target, infra, created_at);
CREATE INDEX link_active_target ON link (target, path, content_file_id) WHERE state = 'active' AND kind = 'symlink';
CREATE INDEX version_file_path_content ON version_file (path, content_id);

-- +goose Down
DROP INDEX version_file_path_content;
DROP INDEX link_active_target;
DROP INDEX acquire_run_target;
CREATE INDEX acquire_run_target ON acquire_run (version_id, target, created_at);
DROP INDEX version_updated;
DROP INDEX title_meta_refreshed;
DROP INDEX job_updated;
DROP INDEX library_import_updated;
DROP INDEX download_updated;
