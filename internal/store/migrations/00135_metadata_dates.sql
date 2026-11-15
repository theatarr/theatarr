-- Episode synopsis (K11): the overview the series' metadata source gives, filled on the next ordinary refresh.
-- Release dates (K12): of TMDb's per-country rows only the primary release's country is kept (tmdb.PrimaryCountry:
-- the US when TMDb lists a US cinema, digital or physical date, else the country of the earliest such date, the first
-- by code on a shared day); the display reads those rows, nothing reads the others, and the metadata job no longer
-- writes them. The canonical rows (country '') that acquisition reads are untouched. The deleted rows come back only
-- from a backup; a later refresh does not write them again.
-- +goose Up
ALTER TABLE episode ADD COLUMN overview TEXT NOT NULL DEFAULT '';

CREATE TEMP TABLE primary_country AS
    WITH dated AS (
        SELECT title_id, country, substr(date, 1, 10) AS day FROM title_release_date
        WHERE source = 'tmdb' AND country <> '' AND type IN ('theatrical', 'theatrical_limited', 'digital', 'physical')
    )
    SELECT title_id, CASE WHEN MAX(country = 'US') THEN 'US'
        ELSE (SELECT x.country FROM dated x WHERE x.title_id = dated.title_id ORDER BY x.day, x.country LIMIT 1) END
        AS country
    FROM dated GROUP BY title_id;
DELETE FROM title_release_date WHERE source = 'tmdb' AND country <> '' AND country IS NOT
    (SELECT p.country FROM primary_country p WHERE p.title_id = title_release_date.title_id);
DROP TABLE primary_country;

-- +goose Down
ALTER TABLE episode DROP COLUMN overview;
