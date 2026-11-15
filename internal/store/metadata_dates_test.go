package store_test

import (
	"context"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
)

// 00135: episodes get an empty overview; of TMDb's per-country release rows only the primary country's stay (the US
// when it has a cinema, digital or physical date, else the country of the earliest such date), and the canonical and
// arr rows are untouched.
func TestMigrationMetadataDates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.OpenAt(t, 134)
	db := st.Writer()
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'US Film', 0, 0),
			(2, 'movie', 'French Film', 0, 0), (3, 'series', 'Show', 0, 0)`,
		`INSERT INTO episode (title_id, season, number, created_at, updated_at) VALUES (3, 1, 1, 0, 0)`,
		`INSERT INTO title_release_date (title_id, source, country, type, date) VALUES
			(1, 'tmdb', 'US', 'theatrical', '1985-07-03'), (1, 'tmdb', 'US', 'premiere', '1985-06-01'),
			(1, 'tmdb', 'GB', 'theatrical', '1985-06-20'), (1, 'tmdb', 'DE', 'digital', '2026-12-01'),
			(1, 'tmdb-art', '', 'theatrical', '1985-07-03'), (1, 'tmdb-art', '', 'digital', '2026-12-01'),
			(1, 'radarr', '', 'physical', '1990-01-01'),
			(2, 'tmdb', 'FR', 'theatrical', '2001-04-25'), (2, 'tmdb', 'BE', 'theatrical', '2001-05-02'),
			(2, 'tmdb', 'DE', 'premiere', '2001-01-01'), (2, 'tmdb', 'GB', 'physical', '2025-03-01')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := st.UpTo(ctx, 135); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]string{
		`SELECT group_concat(title_id || source || country || type, ' ') FROM (SELECT * FROM title_release_date
			ORDER BY title_id, source, country, type)`: "1radarrphysical 1tmdbUSpremiere 1tmdbUStheatrical " +
			"1tmdb-artdigital 1tmdb-arttheatrical 2tmdbFRtheatrical",
		`SELECT '[' || overview || ']' FROM episode`:    "[]",
		`SELECT COUNT(*) FROM pragma_foreign_key_check`: "0",
	} {
		var got string
		if err := db.QueryRowContext(ctx, q).Scan(&got); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", q, got, err, want)
		}
	}
}
