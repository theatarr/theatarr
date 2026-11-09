-- Link races. Conventions as in 00001.
--
-- link.missing_since: when a relink sweep first found the link's file missing from the mount. A link is marked dead
-- only when a later sweep, at least library.deadConfirm after it, still misses it; a sweep that finds the file again
-- clears it. One half-loaded folder listing therefore never kills a link.
--
-- library_lease: a named lease across processes (the CLI and the service share the database). The follower refresh
-- of a version holds 'refresh:<version>' while it swaps links, so two refreshes never remove each other's new link.
-- until is when a lease left by a crash may be taken over.

-- +goose Up
ALTER TABLE link ADD COLUMN missing_since INTEGER;

CREATE TABLE library_lease (
    name  TEXT    PRIMARY KEY,
    owner TEXT    NOT NULL,
    until INTEGER NOT NULL
);

-- +goose Down
DROP TABLE library_lease;
ALTER TABLE link DROP COLUMN missing_since;
