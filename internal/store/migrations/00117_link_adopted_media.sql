-- The media info (the arr's mediaInfo JSON) of the arr file an adopted link was made from, kept on the link so the
-- title page shows it without the arr's file rows. Adopted links already in the database take it from the arr file
-- still recorded at link.adopted_from ("<instance> <path>"). NULL for links Theatarr imported (their probe is on
-- content_file) and when unknown.

-- +goose Up
ALTER TABLE link ADD COLUMN adopted_media TEXT;
UPDATE link SET adopted_media = (SELECT vf.media_info FROM version_file vf
    WHERE vf.instance = substr(link.adopted_from, 1, instr(link.adopted_from, ' ') - 1)
      AND vf.path = substr(link.adopted_from, instr(link.adopted_from, ' ') + 1)
    ORDER BY vf.id DESC LIMIT 1)
WHERE adopted_from IS NOT NULL AND download_id IS NULL;

-- +goose Down
ALTER TABLE link DROP COLUMN adopted_media;
