-- Tags per version (docs/design/arrview.md "Tags"): a title's view tags belong to a version, so a tag set through one
-- view (a client's debrid-upgrade tag on /v/movie-web-theatarr) does not show in a view of another version of the title. A
-- view shows a tag when any of its versions has it, and writes to its own versions only. Labels (arrview_tag) stay
-- global. Existing rows are copied to every version the title has, so every view keeps showing what it showed.
-- arr_title.tags_copied is the arr's labels the import already copied to the versions (JSON array, as tags), so a
-- label is copied once, when it appears (docs/design/import-arr.md).

-- +goose Up
CREATE TABLE arrview_title_tag_new (
    version   TEXT    NOT NULL,
    title_id  INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    tag_id    INTEGER NOT NULL REFERENCES arrview_tag (id) ON DELETE CASCADE,
    PRIMARY KEY (version, title_id, tag_id)
);
INSERT OR IGNORE INTO arrview_title_tag_new (version, title_id, tag_id)
    SELECT v.policy_key, tt.title_id, tt.tag_id FROM arrview_title_tag tt JOIN version v ON v.title_id = tt.title_id;
DROP TABLE arrview_title_tag;
ALTER TABLE arrview_title_tag_new RENAME TO arrview_title_tag;
CREATE INDEX arrview_title_tag_tag ON arrview_title_tag (tag_id);
CREATE INDEX arrview_title_tag_title ON arrview_title_tag (title_id);
ALTER TABLE arr_title ADD COLUMN tags_copied TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE arr_title DROP COLUMN tags_copied;
CREATE TABLE arrview_title_tag_old (
    title_id  INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    tag_id    INTEGER NOT NULL REFERENCES arrview_tag (id) ON DELETE CASCADE,
    PRIMARY KEY (title_id, tag_id)
);
INSERT OR IGNORE INTO arrview_title_tag_old (title_id, tag_id) SELECT title_id, tag_id FROM arrview_title_tag;
DROP TABLE arrview_title_tag;
ALTER TABLE arrview_title_tag_old RENAME TO arrview_title_tag;
CREATE INDEX arrview_title_tag_tag ON arrview_title_tag (tag_id);
