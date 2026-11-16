-- Problem reports (phase N, N5; docs/design/users.md "Report a problem"): a household member says a film or an
-- episode with a file is broken; a manager replaces the file or marks the report fixed. episode_id is 0 for a movie,
-- so one open report per user, title and episode is one unique index; a second report updates it. Nothing here names
-- a file or a release: the manager's view reads the title's current files when it lists the reports.

-- +goose Up
CREATE TABLE problem_report (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER REFERENCES web_user (id) ON DELETE SET NULL,
    title_id     INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    episode_id   INTEGER NOT NULL DEFAULT 0,
    kind         TEXT    NOT NULL CHECK (kind IN ('picture', 'sound', 'subtitles', 'wont_play', 'other')),
    note         TEXT    NOT NULL DEFAULT '',
    status       TEXT    NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'fixed')),
    reply        TEXT    NOT NULL DEFAULT '',
    resolved_by  INTEGER REFERENCES web_user (id) ON DELETE SET NULL,
    resolved_at  INTEGER,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE UNIQUE INDEX problem_report_open ON problem_report (user_id, title_id, episode_id) WHERE status = 'open';
CREATE INDEX problem_report_status ON problem_report (status);
CREATE INDEX problem_report_title ON problem_report (title_id);

-- +goose Down
DROP TABLE problem_report;
