-- One Pause, Watch only or Live, notifications as a setting (docs/design/settings.md "Global switches").
--
-- Notifications: notify_enabled moves into the notify section as $.enabled. True sets it (a null or missing section
-- becomes {"enabled": true}); false removes it.
--
-- Pause: the switches record becomes {"paused": ...}, on unless both router_enabled and library_enabled were on. A
-- missing key reads as false, as the old record decoded it.
--
-- Libraries: a Live library whose grab is not true becomes Watch only, then every library loses grab. A missing grab
-- is false: the old field was omitted when false and had no other default.
-- +goose Up
INSERT INTO setting_section (key, config, created_at, updated_at)
    SELECT 'notify', '{"enabled":true}', CAST(strftime('%s', 'now') AS INTEGER) * 1000,
           CAST(strftime('%s', 'now') AS INTEGER) * 1000
    FROM setting_global_switches
    WHERE key = 'global' AND json_extract(config, '$.notify_enabled') = 1
      AND NOT EXISTS (SELECT 1 FROM setting_section WHERE key = 'notify');
UPDATE setting_section
    SET config = json_set(CASE WHEN json_type(config) = 'object' THEN config ELSE '{}' END, '$.enabled', json('true'))
    WHERE key = 'notify'
      AND EXISTS (SELECT 1 FROM setting_global_switches WHERE key = 'global' AND json_extract(config, '$.notify_enabled') = 1);
UPDATE setting_section SET config = json_remove(config, '$.enabled')
    WHERE key = 'notify' AND json_type(config, '$.enabled') IS NOT NULL
      AND NOT EXISTS (SELECT 1 FROM setting_global_switches WHERE key = 'global' AND json_extract(config, '$.notify_enabled') = 1);

UPDATE setting_global_switches
    SET config = json_object('paused', json(CASE
        WHEN COALESCE(json_extract(config, '$.router_enabled'), 0) = 1
         AND COALESCE(json_extract(config, '$.library_enabled'), 0) = 1 THEN 'false' ELSE 'true' END))
    WHERE json_type(config, '$.paused') IS NULL;

UPDATE setting_version SET config = json_set(config, '$.config.mode', 'shadow')
    WHERE json_extract(config, '$.config.mode') = 'active' AND COALESCE(json_extract(config, '$.config.grab'), 0) <> 1;
UPDATE setting_version SET config = json_remove(config, '$.config.grab')
    WHERE json_type(config, '$.config.grab') IS NOT NULL;

-- +goose Down
SELECT 1;
