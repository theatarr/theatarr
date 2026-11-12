-- The guided arr migration (theatarr migrate inspect/plan/apply) and the go-live check are gone: their saved plans
-- go, and a library's golive_done is stripped.
-- +goose Up
DROP TABLE migration_plan;
UPDATE setting_version SET config = json_remove(config, '$.config.golive_done') WHERE json_type(config, '$.config.golive_done') IS NOT NULL;

-- +goose Down
CREATE TABLE migration_plan (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL,
    inventory   TEXT    NOT NULL,
    plan        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
