-- A file that broke while someone was playing it (internal/library playingbreak.go, docs/design/now-playing.md "A
-- file that stops working while it plays"). One row per file of an incident: the watched file's row first (its id is
-- the incident's), then each file of the same release found broken with it. The row keeps the old copy as it was
-- (every link column a same-path import overwrites, and its episodes) so it can be judged when it reads again, and
-- what the repair came to. Rows are found by title, version, path and old target: a same-path import may give the
-- link row a new id. No backfill.
--
-- settled: '' open (acted on), 'copy' (readable again after another account took the release), 'none' (nothing was
-- done), 'revived' (the old copy read again and nothing else held its film or episodes), 'restored' (put back in
-- place of its replacement), 'aside' (it reads again but stays aside). fixed_by: 'copy', 'replaced' (an import took
-- its place) or ''. serving: the account zurg logged serving the file again after step 1 ('' when unknown). reported: the outcome sent about the watched file, once ('' none yet).

-- +goose Up
CREATE TABLE playing_break (
    id              INTEGER PRIMARY KEY,
    incident        INTEGER NOT NULL,
    watched         INTEGER NOT NULL DEFAULT 0 CHECK (watched IN (0, 1)),
    link_id         INTEGER NOT NULL,
    title_id        INTEGER NOT NULL,
    version_key     TEXT    NOT NULL,
    path            TEXT    NOT NULL,
    old_target      TEXT    NOT NULL,
    info_hash       TEXT    NOT NULL DEFAULT '',
    inner_file      TEXT    NOT NULL DEFAULT '',
    read_offset     INTEGER NOT NULL DEFAULT 0,
    content_file_id INTEGER,
    download_id     INTEGER,
    release_title   TEXT,
    quality         TEXT,
    score           INTEGER,
    file_metadata   TEXT    NOT NULL DEFAULT '{}',
    adopted         INTEGER NOT NULL DEFAULT 0,
    adopted_from    TEXT,
    adopted_source  TEXT,
    adopted_media   TEXT,
    folder_hash     TEXT,
    episodes        TEXT    NOT NULL DEFAULT '[]',
    broken_at       INTEGER NOT NULL,
    out_account     TEXT    NOT NULL DEFAULT '',
    serving         TEXT    NOT NULL DEFAULT '',
    searched        INTEGER NOT NULL DEFAULT 0 CHECK (searched IN (0, 1)),
    fixed_by        TEXT    NOT NULL DEFAULT '' CHECK (fixed_by IN ('', 'copy', 'replaced')),
    new_release     TEXT    NOT NULL DEFAULT '',
    back_since      INTEGER,
    settled         TEXT    NOT NULL DEFAULT ''
                    CHECK (settled IN ('', 'copy', 'none', 'revived', 'restored', 'aside')),
    reported        TEXT    NOT NULL DEFAULT '',
    viewer          TEXT    NOT NULL DEFAULT '',
    player          TEXT    NOT NULL DEFAULT '',
    message         TEXT    NOT NULL DEFAULT '',
    updated_at      INTEGER NOT NULL
);
CREATE INDEX playing_break_title ON playing_break (title_id, version_key, broken_at);
CREATE INDEX playing_break_open ON playing_break (settled, incident);
CREATE INDEX playing_break_link ON playing_break (path, old_target);

-- +goose Down
DROP TABLE playing_break;
