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

func TestImportListMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openAt(t, 116)
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 115); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO movie_import_list(id,name,tmdb_id,monitor,versions,last_sync_at,created_at,updated_at) VALUES(4,'One',9,'none','["m"]',5,1,2)`,
		`INSERT INTO movie_import_list(id,name,tmdb_id,created_at,updated_at) VALUES(7,'Two',10,0,0)`,
	} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.UpTo(ctx, 116); err != nil {
		t.Fatal(err)
	}
	var name, listType, monitor, versions, url, key string
	var tmdbID, last int64
	if err := st.Reader().QueryRow(`SELECT name,list_type,tmdb_id,monitor,versions,url,api_key,last_sync_at FROM import_list WHERE id=4`).
		Scan(&name, &listType, &tmdbID, &monitor, &versions, &url, &key, &last); err != nil {
		t.Fatal(err)
	}
	if name != "One" || listType != "tmdb" || tmdbID != 9 || monitor != "none" || versions != `["m"]` || url != "" || key != "" || last != 5 {
		t.Fatalf("copied row: %s %s %d %s %s %q %q %d", name, listType, tmdbID, monitor, versions, url, key, last)
	}
	if _, err := st.Writer().Exec(`INSERT INTO import_list(name,tmdb_id,created_at,updated_at) VALUES('Again',9,0,0)`); err == nil {
		t.Fatal("a second tmdb row with the same TMDb id was accepted")
	}
	for _, u := range []string{"http://10.0.0.5:7878", "http://10.0.0.6:7878"} {
		if _, err := st.Writer().Exec(`INSERT INTO import_list(name,list_type,url,api_key,created_at,updated_at) VALUES('R','radarr',?,'k',0,0)`, u); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Writer().Exec(`INSERT INTO import_list(name,list_type,url,api_key,created_at,updated_at) VALUES('R','radarr','http://10.0.0.5:7878','k',0,0)`); err == nil {
		t.Fatal("a second radarr row with the same url was accepted")
	}
	if _, err := p.DownTo(ctx, 115); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM movie_import_list WHERE id IN (4,7)`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("down restored %d tmdb rows, err %v", n, err)
	}
	if _, err := st.Writer().Exec(`SELECT * FROM import_list`); err == nil {
		t.Fatal("import_list survived rollback")
	}
}
