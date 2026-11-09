-- Episode identity from grab to import (docs/design/library.md "Which file is which episode").
--
-- download.episode_map is the decision's own mapping of a series release when it was not Sonarr's (JSON
-- {"how": "franchise" | "alt_cut:<name>", "entry": "<entry key>"}), NULL otherwise: the import maps the release's
-- files the same way.
-- download.source_path is the path a path import or a manual import named on the mount (a release folder, a folder
-- inside it, or one file), NULL when the release is found by its hash and names.
-- download_file is each file's episode identity, with its provenance: source manual is a client's explicit choice
-- (manual import: only these files import, with these episodes; a movie's row has no episodes); source import is
-- what an import resolved and linked (its retries keep it). rel is the file's path inside the release folder.

-- +goose Up
ALTER TABLE download ADD COLUMN episode_map TEXT;
ALTER TABLE download ADD COLUMN source_path TEXT;
CREATE TABLE download_file (
    download_id  INTEGER NOT NULL REFERENCES download (id) ON DELETE CASCADE,
    rel          TEXT    NOT NULL,
    episodes     TEXT    NOT NULL DEFAULT '[]',
    source       TEXT    NOT NULL CHECK (source IN ('manual', 'import')),
    how          TEXT    NOT NULL DEFAULT '',
    updated_at   INTEGER NOT NULL,
    PRIMARY KEY (download_id, rel)
);

-- +goose Down
DROP TABLE download_file;
ALTER TABLE download DROP COLUMN source_path;
ALTER TABLE download DROP COLUMN episode_map;
