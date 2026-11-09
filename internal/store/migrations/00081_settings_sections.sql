-- +goose Up
-- Policy sections the settings database owns as a whole (metadata, library, notify, ...), and the views' identity
-- and API key (docs/design/settings.md "Sections"). Each section and the views are seeded from YAML once.
CREATE TABLE setting_section (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT NOT NULL UNIQUE,
    config TEXT NOT NULL CHECK (json_valid(config)),
    secret_id INTEGER UNIQUE REFERENCES setting_secret (id),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE settings_sections_bootstrap (
    section TEXT PRIMARY KEY,
    generation INTEGER NOT NULL REFERENCES settings_generation (generation)
);
ALTER TABLE setting_view_binding ADD COLUMN secret_id INTEGER REFERENCES setting_secret (id);

-- +goose Down
ALTER TABLE setting_view_binding DROP COLUMN secret_id;
DROP TABLE settings_sections_bootstrap;
DROP TABLE setting_section;
