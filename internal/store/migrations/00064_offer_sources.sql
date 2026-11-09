-- Offers retain their source identity and a bounded retry schedule after the initial six-hour window.
-- +goose Up
ALTER TABLE router_offer ADD COLUMN source_kind TEXT NOT NULL DEFAULT 'download';
ALTER TABLE router_offer ADD COLUMN source_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE router_offer ADD COLUMN retry_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE router_offer ADD COLUMN target_state TEXT NOT NULL DEFAULT '';

UPDATE router_offer SET source_id = COALESCE((SELECT d.id FROM download d
    WHERE d.title_id = router_offer.title_id AND d.version_key = router_offer.from_version
      AND lower(d.info_hash) = router_offer.info_hash ORDER BY d.id DESC LIMIT 1), 0);
UPDATE router_offer SET next_at = updated_at + 86400000 WHERE outcome = 'gave_up';
DROP INDEX router_offer_due;
CREATE INDEX router_offer_due ON router_offer (next_at) WHERE outcome IN ('pending', 'gave_up');

-- +goose Down
DROP INDEX router_offer_due;
CREATE INDEX router_offer_due ON router_offer (next_at) WHERE outcome = 'pending';
UPDATE router_offer SET next_at = NULL WHERE outcome = 'gave_up';
ALTER TABLE router_offer DROP COLUMN target_state;
ALTER TABLE router_offer DROP COLUMN retry_attempts;
ALTER TABLE router_offer DROP COLUMN source_id;
ALTER TABLE router_offer DROP COLUMN source_kind;
