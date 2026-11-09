-- A restore's re-add whose file selection did not finish. Conventions as in 00001.
--
-- torrent_restore_pending: one row per torrent the torrent manager added to a provider that waits for a file
-- selection (Real-Debrid), written as soon as the add answered with the new torrent's id and removed once the
-- selection is made. A row left behind (a failed detail read or selection, or the process ending) makes the next
-- restore of that hash resume the selection on exactly that torrent (action resume) instead of skipping it as
-- present. files is the backup's selection (JSON array of {id, path, bytes}); selection is how it is applied
-- (files, all); error is the last failure in words.
-- +goose Up
CREATE TABLE torrent_restore_pending (
    provider    TEXT    NOT NULL,
    torrent_id  TEXT    NOT NULL,
    info_hash   TEXT    NOT NULL,
    backup_id   TEXT    NOT NULL DEFAULT '',
    files       TEXT    NOT NULL DEFAULT '[]',
    selection   TEXT    NOT NULL,
    error       TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (provider, torrent_id)
);

-- +goose Down
DROP TABLE torrent_restore_pending;
