-- Comparison after cutover stores observations separately from the owning arr import.
-- +goose Up
CREATE TABLE shadow_compare_source (
    instance TEXT PRIMARY KEY,
    started_at INTEGER NOT NULL
);
CREATE TABLE shadow_compare_grab (
    instance TEXT NOT NULL,
    history_id INTEGER NOT NULL,
    arr_title_id INTEGER NOT NULL,
    title_id INTEGER REFERENCES title(id) ON DELETE CASCADE,
    version_id INTEGER REFERENCES version(id) ON DELETE CASCADE,
    at INTEGER NOT NULL,
    event TEXT NOT NULL,
    PRIMARY KEY(instance, history_id)
);
CREATE INDEX shadow_compare_grab_at ON shadow_compare_grab(instance, at);
ALTER TABLE shadow_diff ADD COLUMN compare_instance TEXT NOT NULL DEFAULT '';
ALTER TABLE shadow_diff ADD COLUMN compare_key TEXT;
CREATE UNIQUE INDEX shadow_diff_compare ON shadow_diff(compare_instance, compare_key);

-- +goose Down
DROP INDEX shadow_diff_compare;
ALTER TABLE shadow_diff DROP COLUMN compare_key;
ALTER TABLE shadow_diff DROP COLUMN compare_instance;
DROP TABLE shadow_compare_grab;
DROP TABLE shadow_compare_source;
