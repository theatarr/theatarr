-- Operator actions (docs/design/ui.md "Recovery actions"): what a person did by hand through the web UI to recover a
-- download or tidy the library: grab a chosen release, remove a download (and blocklist it, and search again), retry
-- a download, unblock a blocked release, remove a copy or a title, unlink a file. The rows the action changed keep
-- their own history (download, router_blocklist, link, library_action), but some actions erase their trace (an
-- unblock deletes the blocklist row), so the journal lists each action as its own entry (kind action, id act:<id>).
-- by is the user name, or key:<name> for an API key. target is what the action named: the download id, the link
-- path, the blocked hash. detail is the outcome in words. Kept for good, like verdicts: they are the audit trail.
-- +goose Up
CREATE TABLE journal_action (
    id             INTEGER PRIMARY KEY,
    action         TEXT    NOT NULL CHECK (action IN ('grab', 'remove_download', 'retry_download', 'unblock',
                                                      'remove_version', 'remove_title', 'unlink_file')),
    title_id       INTEGER REFERENCES title (id) ON DELETE SET NULL,
    version_key    TEXT    NOT NULL DEFAULT '',
    info_hash      TEXT    NOT NULL DEFAULT '',
    release_title  TEXT    NOT NULL DEFAULT '',
    download_id    INTEGER,
    target         TEXT    NOT NULL DEFAULT '',
    detail         TEXT    NOT NULL DEFAULT '',
    by             TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL
);
CREATE INDEX journal_action_at ON journal_action (created_at);
CREATE INDEX journal_action_title ON journal_action (title_id, version_key, created_at);

-- +goose Down
DROP TABLE journal_action;
