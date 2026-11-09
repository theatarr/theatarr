-- Library helpers: dangling-link reports and offers between versions.
--
-- library_dangling is the latest dangling-link report: every link into the zurg mount
-- whose target is missing, in the version roots and the extra read-only roots (library.dangling.roots). Each report
-- replaces the rows; first_seen_at survives while a link stays dangling. library_dangling_scan keeps one row per
-- report.
--
-- router_offer is the offer state: a release imported for one version, offered to another version of the same title
-- (grab once, link twice). One row per (version, title, hash): the outcome, the target's reasons when it declined
-- (remembered, so the same file is not offered again), and the retry schedule while the target cannot decide yet.

-- +goose Up
CREATE TABLE library_dangling (
    path           TEXT    PRIMARY KEY,
    target         TEXT    NOT NULL,
    root           TEXT    NOT NULL,
    version_key    TEXT,
    folder         TEXT    NOT NULL DEFAULT '',
    first_seen_at  INTEGER NOT NULL,
    last_seen_at   INTEGER NOT NULL
);

CREATE TABLE library_dangling_scan (
    id        INTEGER PRIMARY KEY,
    at        INTEGER NOT NULL,
    roots     TEXT    NOT NULL,
    links     INTEGER NOT NULL,
    dangling  INTEGER NOT NULL,
    error     TEXT,
    took_ms   INTEGER NOT NULL
);

CREATE TABLE router_offer (
    version_key   TEXT    NOT NULL,
    title_id      INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    info_hash     TEXT    NOT NULL,
    from_version  TEXT    NOT NULL,
    release_title TEXT    NOT NULL DEFAULT '',
    outcome       TEXT    NOT NULL CHECK (outcome IN ('pending', 'routed', 'planned', 'declined', 'same', 'gave_up')),
    reasons       TEXT    NOT NULL DEFAULT '[]',
    attempts      INTEGER NOT NULL DEFAULT 0,
    first_at      INTEGER NOT NULL,
    next_at       INTEGER,
    updated_at    INTEGER NOT NULL,
    PRIMARY KEY (version_key, title_id, info_hash)
);
CREATE INDEX router_offer_due ON router_offer (next_at) WHERE outcome = 'pending';

-- +goose Down
DROP TABLE router_offer;
DROP TABLE library_dangling_scan;
DROP TABLE library_dangling;
