-- A Discord user id per account (docs/design/users.md): an admin sets it, and a request-ready message to a Discord
-- notification target @mentions the requesters who have one. '' is none.

-- +goose Up
ALTER TABLE web_user ADD COLUMN discord_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE web_user DROP COLUMN discord_id;
