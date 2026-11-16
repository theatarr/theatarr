-- The torrent hash a library link's mount folder points to, for display only (phase M, 1a; docs/design/torrents.md
-- "Folder hash"). Files adopted from Sonarr/Radarr often record no hash, so "Cached on" was empty for them. The
-- torrents manager's fill (internal/torrents folderhash.go, after each provider sync) sets it to the one hash of the
-- record's torrents whose folders hold the link's target, else NULL. Only the web's display readers read it;
-- router.LinkStoredHashSQL, removal and the torrents usage index do not. Filled by the next sync, not here.

-- +goose Up
ALTER TABLE link ADD COLUMN folder_hash TEXT;

-- +goose Down
ALTER TABLE link DROP COLUMN folder_hash;
