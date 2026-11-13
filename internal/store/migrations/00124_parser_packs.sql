-- Every series library parses release names with v4plus and takes season packs by the any_upgradable rule
-- (docs/design/series.md): the stored library settings sonarr, packs and packs_threshold go. Stored versions decode
-- leniently, so this only tidies the rows; a save drops them too. Down keeps the rows as they are.
-- +goose Up
UPDATE setting_version SET config = json_remove(config, '$.config.sonarr', '$.config.packs', '$.config.packs_threshold')
    WHERE json_type(config, '$.config.sonarr') IS NOT NULL OR json_type(config, '$.config.packs') IS NOT NULL
    OR json_type(config, '$.config.packs_threshold') IS NOT NULL;

-- +goose Down
SELECT 1;
