-- 00095 added router_pacing.backoff_down as 0, so a provider backing off for an outage when that migration
-- ran counted as not down until its back-off was next written, and a route could reuse a torrent the
-- provider holds meanwhile (the case backoff_down blocks). A back-off still running now whose stored reason is one an
-- outage writes is marked down: a transport error ('error'), an unavailable or unknown answer ('unavailable',
-- 'unknown': also a few refusals that are no outage, which then wait out their back-off before a reuse, the safe
-- side) or downloads that stalled while the provider was down. Rate limits, auth, traffic and holds stay not down.
-- +goose Up
UPDATE router_pacing SET backoff_down = 1
WHERE backoff_down = 0
    AND backoff_until > CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)
    AND (reason IN ('error', 'unavailable', 'unknown') OR reason LIKE 'provider unavailable:%');

-- +goose Down
-- Nothing to undo: backoff_down is renewed with the next back-off.
SELECT 1;
