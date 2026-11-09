-- The quality profile "Add version" preselects for each kind of version. A UI default only: the
-- runtime never reads it, and a version always names its own profile.
-- +goose Up
CREATE TABLE setting_profile_default (
    category   TEXT PRIMARY KEY CHECK (category IN ('movie', 'series', 'anime_movie', 'anime_series')),
    profile    TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE setting_profile_default;
