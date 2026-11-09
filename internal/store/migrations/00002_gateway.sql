-- The outbound gateway (internal/gateway, docs/design/indexers.md): persisted budget windows and back-offs, so a restart neither
-- resets a daily cap nor forgets that a budget or an indexer is backing off.

-- +goose Up

-- Requests counted per budget per fixed UTC window. period is 'hour' or 'day'; window_start_at is the window's start.
-- Only budgets with an hourly or daily cap get rows. Rows older than two days are pruned by the gateway.
CREATE TABLE gateway_budget_window (
    budget           TEXT    NOT NULL,
    period           TEXT    NOT NULL CHECK (period IN ('hour', 'day')),
    window_start_at  INTEGER NOT NULL,
    count            INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (budget, period, window_start_at)
);

-- A back-off on a budget (after a 429 or a Retry-After) or on a target (an indexer or provider after repeated
-- failures). subject is 'budget:<key>', 'indexer:<key>', 'provider:<key>', 'metadata:<key>' or 'client:<key>'.
-- until_at 0 means no back-off is active; failures counts consecutive failures and sets the next back-off length.
CREATE TABLE gateway_backoff (
    subject     TEXT    PRIMARY KEY,
    until_at    INTEGER NOT NULL DEFAULT 0,
    failures    INTEGER NOT NULL DEFAULT 0,
    reason      TEXT    NOT NULL DEFAULT '',
    updated_at  INTEGER NOT NULL
);

-- +goose Down
DROP TABLE gateway_backoff;
DROP TABLE gateway_budget_window;
