-- Two indexes on journal_decision for the per-title reads (the lists' "Upgrade found", its evidence, the title page's
-- releases, an episode's stored releases), which read by title and library without a source, and for the set of
-- titles with an accepted release. Retention deletes provider_cache rows a day after they expire
-- (internal/retention); the rows already past that go here once.
-- +goose Up
CREATE INDEX journal_decision_title ON journal_decision (title_id, version_key, created_at DESC);
CREATE INDEX journal_decision_accepted ON journal_decision (title_id, version_key) WHERE accepted > 0;
DELETE FROM provider_cache WHERE expires_at IS NOT NULL
    AND expires_at < (CAST(strftime('%s', 'now') AS INTEGER) - 86400) * 1000;

-- +goose Down
DROP INDEX journal_decision_accepted;
DROP INDEX journal_decision_title;
