-- The name the torrent itself declared, as the provider's status read gives it (Real-Debrid's original_filename;
-- TorBox and AllDebrid: the folder the files share): a mount such as zurg names the release folder after it, while
-- download.name, the provider's name, may differ. NULL until a read gives one; the import tries it as a folder name.

-- +goose Up
ALTER TABLE download ADD COLUMN original_name TEXT;

-- +goose Down
ALTER TABLE download DROP COLUMN original_name;
