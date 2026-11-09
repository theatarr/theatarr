-- The download client a grab was sent to (versions[*].add_via: client; docs/design/external-clients.md):
-- the clients[*] key, NULL for a download the router added to a provider itself. A client download keeps provider
-- NULL, so the provider sweeps, reconcile, stall timeout, torrent manager and delete paths never touch it.

-- +goose Up
ALTER TABLE download ADD COLUMN client TEXT;

-- +goose Down
ALTER TABLE download DROP COLUMN client;
