-- theatarr migrate (docs/migration.md). Conventions as in 00001.
--
-- migration_plan: one saved migration plan. inventory is what migrate inspect read (JSON, the arrs' raw GET bodies
-- left out); plan is the plan with every step's outcome (JSON, schema_version inside). Neither ever holds an API
-- key: services and secrets are named, never carried.

-- +goose Up
CREATE TABLE migration_plan (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL,
    inventory   TEXT    NOT NULL,
    plan        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- +goose Down
DROP TABLE migration_plan;
