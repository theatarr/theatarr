-- Privacy is a label only: whether a private indexer's releases go to debrid is
-- the user's choice, made by listing the indexer in a version. Nothing reads the durable privacy evidence any more
-- (restores re-add every hash of a backup), so its table goes.
-- +goose Up
DROP TABLE IF EXISTS privacy_evidence;

-- +goose Down
CREATE TABLE privacy_evidence (
    info_hash   TEXT    NOT NULL,
    indexer     TEXT    NOT NULL DEFAULT '',
    private     INTEGER NOT NULL CHECK (private IN (0, 1)),
    source      TEXT    NOT NULL,
    first_seen  INTEGER NOT NULL,
    PRIMARY KEY (info_hash, indexer, private)
) WITHOUT ROWID;
