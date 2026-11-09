-- +goose Up
-- Keep the source relationship available to readers that do not load policy. Followers still own no arr rows.
CREATE TABLE version_follow (
    version_id INTEGER PRIMARY KEY REFERENCES version (id) ON DELETE CASCADE,
    follows_id INTEGER NOT NULL REFERENCES version (id) ON DELETE CASCADE,
    CHECK (version_id <> follows_id)
);

-- Keep each instance's contribution so removing one entry cannot clear another instance's block.
CREATE TABLE arr_blocklist (
    instance      TEXT NOT NULL REFERENCES arr_instance (name) ON DELETE CASCADE,
    version_key   TEXT NOT NULL,
    info_hash     TEXT NOT NULL,
    title_id      INTEGER REFERENCES title (id) ON DELETE SET NULL,
    release_title TEXT NOT NULL,
    PRIMARY KEY (instance, version_key, info_hash)
);

-- +goose Down
DROP TABLE arr_blocklist;
DROP TABLE version_follow;
