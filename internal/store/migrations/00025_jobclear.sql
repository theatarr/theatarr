-- Failed jobs that people can manage (internal/jobs manage.go). Nothing here deletes a job: a retry is a new job
-- linked to the one it retries, and a dismissal only hides a failed job from "needs attention".
--
-- retry_of: the failed job this one re-runs (POST /jobs/{id}/retry, theatarr jobs retry), with the same kind, payload,
-- priority, max attempts and dedupe key. No foreign key: job pruning may remove the old row first.
-- dismissed_at, dismissed_by: when and by whom (user, key:<name>, cli) a failed job was dismissed.
--
-- A failed job counts as resolved, without any column, once a later job of the same kind finished done with the same
-- payload or with the empty payload {} (a run for every subject of the kind); job_resolve serves that lookup.

-- +goose Up
ALTER TABLE job ADD COLUMN retry_of INTEGER;
ALTER TABLE job ADD COLUMN dismissed_at INTEGER;
ALTER TABLE job ADD COLUMN dismissed_by TEXT;
CREATE INDEX job_resolve ON job (kind, state, id);
CREATE INDEX job_retry_of ON job (retry_of) WHERE retry_of IS NOT NULL;

-- +goose Down
DROP INDEX job_retry_of;
DROP INDEX job_resolve;
ALTER TABLE job DROP COLUMN dismissed_by;
ALTER TABLE job DROP COLUMN dismissed_at;
ALTER TABLE job DROP COLUMN retry_of;
