-- Notification target URLs carry their credential in the path or query, and the settings audit stored them whole
-- (docs/design/settings.md "Sections"). New rows keep only scheme and host; this scrubs the URL of every notify target
-- in existing rows. The audit keeps which targets changed, not where they pointed.
-- +goose Up
UPDATE settings_audit
SET before = json_set(before, '$.config.targets', json((
        SELECT json_group_object(t.key, CASE WHEN json_type(t.value, '$.url') = 'text'
            THEN json_set(t.value, '$.url', '[redacted]') ELSE json(t.value) END)
        FROM json_each(settings_audit.before, '$.config.targets') AS t)))
WHERE entity = 'sections' AND key = 'notify' AND json_type(before, '$.config.targets') = 'object';

UPDATE settings_audit
SET after = json_set(after, '$.config.targets', json((
        SELECT json_group_object(t.key, CASE WHEN json_type(t.value, '$.url') = 'text'
            THEN json_set(t.value, '$.url', '[redacted]') ELSE json(t.value) END)
        FROM json_each(settings_audit.after, '$.config.targets') AS t)))
WHERE entity = 'sections' AND key = 'notify' AND json_type(after, '$.config.targets') = 'object';

-- +goose Down
-- The scrubbed URLs are gone; nothing to restore.
SELECT 1;
