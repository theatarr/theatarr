-- The grab journal (internal/journal, docs/journal.md). The journal is a reader over the tables that already record
-- decisions and what followed (shadow_*, router_plan, acquire_run, download, download_attempt, provider_cache,
-- router_blocklist, library_*, link, content*, arr_history). Two things were not stored anywhere, so they get tables:
-- the candidates a search decided between when it handed work to the router, and the verdicts people and agents
-- give on entries. Conventions as in 00001.

-- +goose Up

-- One search decided for a route (an acquire run, or an RSS read of an active version): the best accepted releases
-- in rank order, then the rejected releases with the highest scores, each with quality, score, formats and
-- rejections (journal.Candidate), and the count of every rejection reason across all results. source is the
-- router request source (download.source and router_plan.source carry it: acquire:<run id>, rss). run_id is the
-- acquire_run, when one. Retention: full for 180 days, then trimmed to the chosen release and one runner-up, and
-- deleted when nothing was routed from it (journal.Prune).
CREATE TABLE journal_decision (
    id           INTEGER PRIMARY KEY,
    source       TEXT    NOT NULL,
    run_id       INTEGER,
    title_id     INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    version_key  TEXT    NOT NULL,
    target       TEXT    NOT NULL DEFAULT '',
    results      INTEGER NOT NULL DEFAULT 0,
    accepted     INTEGER NOT NULL DEFAULT 0,
    candidates   TEXT    NOT NULL DEFAULT '[]',
    rejections   TEXT    NOT NULL DEFAULT '{}',
    notes        TEXT    NOT NULL DEFAULT '',
    trimmed      INTEGER NOT NULL DEFAULT 0 CHECK (trimmed IN (0, 1)),
    created_at   INTEGER NOT NULL
);
CREATE INDEX journal_decision_source ON journal_decision (source, title_id, version_key, created_at);
CREATE INDEX journal_decision_run ON journal_decision (run_id) WHERE run_id IS NOT NULL;
CREATE INDEX journal_decision_at ON journal_decision (created_at);

-- A verdict on one journal entry (entry_id is the journal id, e.g. dl:12, arr:radarr_4k_debrid:6531). ok or wrong;
-- both acknowledge the flags the entry had (acked: JSON list of flag codes), so they stop counting as suspect.
-- action is what the verdict did: recorded (nothing else), or blocklisted (the release's hash went on
-- router_blocklist for the version, source journal) with research_job the acquire.research job, when one was
-- queued. by is the user or API key name. Kept for good: verdicts are few and they are the audit trail.
CREATE TABLE journal_verdict (
    id            INTEGER PRIMARY KEY,
    entry_id      TEXT    NOT NULL,
    verdict       TEXT    NOT NULL CHECK (verdict IN ('ok', 'wrong')),
    reason        TEXT    NOT NULL,
    acked         TEXT    NOT NULL DEFAULT '[]',
    title_id      INTEGER REFERENCES title (id) ON DELETE SET NULL,
    version_key   TEXT    NOT NULL DEFAULT '',
    info_hash     TEXT    NOT NULL DEFAULT '',
    action        TEXT    NOT NULL CHECK (action IN ('recorded', 'blocklisted')),
    action_note   TEXT    NOT NULL DEFAULT '',
    research_job  INTEGER,
    by            TEXT    NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL
);
CREATE INDEX journal_verdict_entry ON journal_verdict (entry_id, created_at);
CREATE INDEX journal_verdict_at ON journal_verdict (created_at);

-- Reads the journal makes on tables it does not own: an arr grab's earlier imports and deletions (upgrade, score
-- lowered), the downloads a decision routed, a title's blocklist, and an arr import's files.
CREATE INDEX journal_arr_history_title ON arr_history (instance, arr_title_id, event_type, date_at);
CREATE INDEX journal_download_source ON download (source, title_id, version_key);
CREATE INDEX journal_blocklist_title ON router_blocklist (title_id, version_key, created_at);
CREATE INDEX journal_version_file_download ON version_file (instance, download_id) WHERE download_id IS NOT NULL;

-- +goose Down
DROP INDEX journal_version_file_download;
DROP INDEX journal_blocklist_title;
DROP INDEX journal_download_source;
DROP INDEX journal_arr_history_title;
DROP TABLE journal_verdict;
DROP TABLE journal_decision;
