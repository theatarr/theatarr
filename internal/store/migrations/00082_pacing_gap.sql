-- Real-Debrid add spacing and account-hold escalation (docs/design/router.md "Pacing"). last_add_at is when the last add
-- was sent to the provider (pacing.min_gap counts from it); hold_level and hold_until track the account-hold cooldown
-- ladder (5 min, 15 min, 60 min), reset after a quiet hour.
-- +goose Up
ALTER TABLE router_pacing ADD COLUMN last_add_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE router_pacing ADD COLUMN hold_level INTEGER NOT NULL DEFAULT 0;
ALTER TABLE router_pacing ADD COLUMN hold_until INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE router_pacing DROP COLUMN hold_until;
ALTER TABLE router_pacing DROP COLUMN hold_level;
ALTER TABLE router_pacing DROP COLUMN last_add_at;
