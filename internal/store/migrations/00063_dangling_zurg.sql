-- What zurg's read-only API (library.mount.zurg_api) adds to the dangling-link report (00021).
--
-- library_dangling.cause is why a link dangles when the API says: 'no_video' when its torrent is listed with no
-- video left upstream; NULL when unknown (the API off, or no answer for the link). library_dangling_scan keeps, per
-- report, how many dangling links that was (no_video), how many listed torrents have no video (no_video_torrents),
-- whether zurg was repairing (repairing), and why the API could not be read (zurg_error). All NULL without the API.

-- +goose Up
ALTER TABLE library_dangling ADD COLUMN cause TEXT;
ALTER TABLE library_dangling_scan ADD COLUMN no_video INTEGER;
ALTER TABLE library_dangling_scan ADD COLUMN no_video_torrents INTEGER;
ALTER TABLE library_dangling_scan ADD COLUMN repairing INTEGER;
ALTER TABLE library_dangling_scan ADD COLUMN zurg_error TEXT;

-- +goose Down
ALTER TABLE library_dangling_scan DROP COLUMN zurg_error;
ALTER TABLE library_dangling_scan DROP COLUMN repairing;
ALTER TABLE library_dangling_scan DROP COLUMN no_video_torrents;
ALTER TABLE library_dangling_scan DROP COLUMN no_video;
ALTER TABLE library_dangling DROP COLUMN cause;
