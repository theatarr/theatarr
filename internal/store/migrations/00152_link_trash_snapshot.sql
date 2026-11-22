-- Undo an upgrade (internal/library return.go, docs/design/library.md "Undo an upgrade"): a trash row of a file an
-- import displaced keeps that file's row as it was (snapshot, JSON: content file, download, file facts, adoption
-- fields, folder hash, episodes, release, quality, score) and the link that took its place (successor: link id,
-- target, download id), written before the new link overwrites anything. Undo restores the file from it exactly.
-- NULL for rows from before this migration and for trash rows that are no import's displaced file: those get no Undo.

-- +goose Up
ALTER TABLE link_trash ADD COLUMN snapshot TEXT;

-- +goose Down
ALTER TABLE link_trash DROP COLUMN snapshot;
