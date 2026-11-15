-- The provider sync's folder name of a torrent is its name in the record (provider_torrent.name, the value the sync
-- also wrote into content_provider.folder_name). Mount relinking looks a folder up by that name (library/relink.go);
-- without this index each lookup scanned the record.
-- +goose Up
CREATE INDEX provider_torrent_name ON provider_torrent (name);

-- +goose Down
DROP INDEX provider_torrent_name;
