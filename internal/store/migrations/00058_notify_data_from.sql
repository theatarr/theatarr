-- data_from names the indexer whose release details a webhook delivery carries ('' when it carries none). Nothing
-- reads it now. Deliveries still pending when this ran were dropped, not sent.
-- +goose Up
ALTER TABLE notify_delivery ADD COLUMN data_from TEXT NOT NULL DEFAULT '';
UPDATE notify_delivery SET state = 'dropped', last_error = 'built before the webhook privacy check'
    WHERE state = 'pending';

-- +goose Down
ALTER TABLE notify_delivery DROP COLUMN data_from;
