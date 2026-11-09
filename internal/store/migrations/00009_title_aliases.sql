-- Title names for release mapping (docs/design/tv-metadata.md): the secondary year and the alternative titles and translations the
-- arrs match releases against (Radarr's MovieMetadata.SecondaryYear, AlternativeTitles and Translations). Conventions
-- as in 00001. See docs/design/import-arr.md.

-- +goose Up

-- secondary_year is the arr's MovieMetadata.SecondaryYear (from its metadata server; TMDb has no such field): a
-- release dated that year maps to the title too.
ALTER TABLE title ADD COLUMN secondary_year INTEGER;

-- One alternative title or translation of a title, per source. source is the arr instance the name came from
-- (its movie resource's alternateTitles) or 'tmdb' (alternative_titles and translations, from theatarr import-arr
-- --enrich). kind: alternative or translation. language is ISO 639-1 (translations), country ISO 3166-1; both
-- empty when the source gives none. Each import replaces the rows of its source.
CREATE TABLE title_alias (
    title_id  INTEGER NOT NULL REFERENCES title (id) ON DELETE CASCADE,
    source    TEXT    NOT NULL,
    kind      TEXT    NOT NULL CHECK (kind IN ('alternative', 'translation')),
    name      TEXT    NOT NULL,
    language  TEXT    NOT NULL DEFAULT '',
    country   TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (title_id, source, kind, name, language, country)
);

-- +goose Down
DROP TABLE title_alias;
ALTER TABLE title DROP COLUMN secondary_year;
