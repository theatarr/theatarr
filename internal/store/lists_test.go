package store

import (
	"context"
	"testing"
)

func TestMovieListsMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openAt(t, 54)
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 53); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := p.UpTo(ctx, 54); err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{
			`INSERT INTO movie_collection(tmdb_id,updated_at) VALUES(7,0)`,
			`INSERT INTO movie_import_list(name,tmdb_id,created_at,updated_at) VALUES('List',9,0,0)`,
			`INSERT INTO movie_exclusion(tmdb_id,created_at) VALUES(201,0)`,
		} {
			if _, err := st.Writer().Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		var monitored bool
		var versions, movies, minimum string
		if err := st.Reader().QueryRow(`SELECT monitored,versions,movies,minimum_availability FROM movie_collection WHERE tmdb_id=7`).Scan(&monitored, &versions, &movies, &minimum); err != nil {
			t.Fatal(err)
		}
		if monitored || versions != "[]" || movies != "[]" || minimum != "released" {
			t.Fatalf("defaults: monitored=%v versions=%s movies=%s availability=%s", monitored, versions, movies, minimum)
		}
		if _, err := p.DownTo(ctx, 53); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"movie_collection", "movie_import_list", "movie_exclusion"} {
			if _, err := st.Writer().Exec(`SELECT * FROM ` + table); err == nil {
				t.Fatalf("%s survived rollback", table)
			}
		}
	}
}
