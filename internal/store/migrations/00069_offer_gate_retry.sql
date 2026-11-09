-- Offers accepted while the router gate was shut are retried once grabbing is on. Before this,
-- an accepted offer to an active target was recorded 'routed' even when router.enabled was off and its route only a
-- dry-run plan, so it was never offered again. Such a row has no download of the target for that hash: it becomes
-- 'planned', which the offerer offers again once the target's gate is open. A routed offer whose route made a
-- download keeps 'routed'.
-- +goose Up
UPDATE router_offer SET outcome = 'planned' WHERE outcome = 'routed' AND NOT EXISTS (
    SELECT 1 FROM download d WHERE d.version_key = router_offer.version_key AND d.title_id = router_offer.title_id
      AND lower(d.info_hash) = router_offer.info_hash);

-- +goose Down
SELECT 1;
