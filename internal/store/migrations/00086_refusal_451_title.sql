-- Grabbing fixes.
--
-- router_refusal_451.title_id, held: the title whose route got the 451 (0: unknown, a restore from the torrent
-- manager) and whether the 451 was explained by an account hold. 451s on different hashes of one title are per-hash
-- verdicts, not a hold; only 451s on different titles' hashes within holdWindow hold the account. The rows are kept
-- for miss_ttl so that a title whose releases Real-Debrid keeps refusing is skipped there (docs/design/router.md
-- "Pacing").
--
-- download.poll_outage_since, stall_offset: when the provider's status reads began failing for reasons that say
-- nothing about the torrent (a gateway refusal, a transport error, a 5xx, while no other torrent of the provider
-- read fine in the same pass), and the time (milliseconds) such outages took since the add. The stall timeouts do not
-- count that time (docs/design/router.md "Polling").
--
-- provider_cache_expires: retention prunes rows expired for 30 days (internal/retention).
-- +goose Up
ALTER TABLE router_refusal_451 ADD COLUMN title_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE router_refusal_451 ADD COLUMN held INTEGER NOT NULL DEFAULT 0;
ALTER TABLE download ADD COLUMN poll_outage_since INTEGER;
ALTER TABLE download ADD COLUMN stall_offset INTEGER NOT NULL DEFAULT 0;
CREATE INDEX provider_cache_expires ON provider_cache (expires_at) WHERE expires_at IS NOT NULL;

-- +goose Down
DROP INDEX provider_cache_expires;
ALTER TABLE download DROP COLUMN stall_offset;
ALTER TABLE download DROP COLUMN poll_outage_since;
ALTER TABLE router_refusal_451 DROP COLUMN held;
ALTER TABLE router_refusal_451 DROP COLUMN title_id;
