-- acquire_run.infra tells three outcomes apart: 0 searched, 1 no answer (the VPN guard refused it, or
-- no indexer answered; not counted as searched), 2 a partial answer (some indexer failed; counted as searched for a
-- shorter while). Before, a partial answer was recorded as 1. Its rows are the routed ones (a search with no answer
-- has no candidates) and the failed ones whose message says "search incomplete".
-- +goose Up
UPDATE acquire_run SET infra = 2 WHERE infra = 1 AND (state = 'routed' OR message LIKE 'search incomplete (%');

-- +goose Down
UPDATE acquire_run SET infra = 1 WHERE infra = 2;
