-- One search schedule, settings (docs/design/router.md "The search schedule"): router.wanted loses enabled, interval
-- and retry_new (the section is decoded strictly, so a leftover field would stop the start). A stored enabled false
-- turned both backlog switches off, so it becomes missing false and cutoff_unmet off first. A stored null (or no
-- enabled) meant the default, on, and changes nothing. New content is always retried; the round runs every 15
-- minutes.
-- +goose Up
UPDATE setting_section SET config = json_set(config, '$.wanted.missing', json('false'), '$.wanted.cutoff_unmet', 'off')
    WHERE key = 'router' AND json_valid(config) AND json_type(config, '$.wanted.enabled') = 'false';
UPDATE setting_section SET config = json_remove(config, '$.wanted.enabled', '$.wanted.interval', '$.wanted.retry_new')
    WHERE key = 'router' AND json_valid(config) AND json_type(config, '$.wanted') = 'object';

-- +goose Down
SELECT 1;
