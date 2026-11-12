-- Quality sync applies by itself: no pending reviews, no mode; every source re-reads its upstream once.
-- +goose Up
UPDATE settings_quality_review SET state = 'superseded' WHERE state = 'pending';
UPDATE setting_quality_sync SET config = json_remove(config, '$.mode') WHERE json_type(config, '$.mode') IS NOT NULL;
UPDATE settings_quality_sync_run SET config_digest = '';

-- +goose Down
SELECT 1;
