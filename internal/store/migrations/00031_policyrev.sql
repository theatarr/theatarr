-- The policy revision (policy.Policy.Revision: a short hash of the policy files as loaded) on the rows a decision
-- leaves behind, so a decision can be tied to the policy that made it.
-- '' on rows written before this migration, and by callers without a loaded policy.
--
-- journal_decision.policy_rev: the policy the search decided under.
-- acquire_run.policy_rev: the policy in force when the run was queued; the search itself may run under a later one,
-- which its journal_decision row then names.
-- download.policy_rev: the policy the router opened the download under.

-- +goose Up
ALTER TABLE journal_decision ADD COLUMN policy_rev TEXT NOT NULL DEFAULT '';
ALTER TABLE acquire_run ADD COLUMN policy_rev TEXT NOT NULL DEFAULT '';
ALTER TABLE download ADD COLUMN policy_rev TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE download DROP COLUMN policy_rev;
ALTER TABLE acquire_run DROP COLUMN policy_rev;
ALTER TABLE journal_decision DROP COLUMN policy_rev;
