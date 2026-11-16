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

// TestListSourcesMigration: 00146 keeps every list's id, view and key, gives each its media (series for Sonarr),
// carries the id counter over, takes the new kinds and a TMDb list once per media, and adds the exclusion columns
// and the series exclusion table; down keeps the rows 00116 can hold.
func TestListSourcesMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openAt(t, 146)
	p, err := st.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 145); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO import_list(id,name,tmdb_id,view,monitor,versions,last_sync_at,created_at,updated_at) VALUES(4,'One',9,'radarr','none','["m"]',5,1,2)`,
		`INSERT INTO import_list(id,name,list_type,url,api_key,created_at,updated_at) VALUES(7,'S','sonarr','http://10.0.0.6:8989','key',0,0)`,
		`INSERT INTO import_list(id,name,list_type,url,api_key,created_at,updated_at) VALUES(12,'R','radarr','http://10.0.0.5:7878','key',0,0)`,
		`DELETE FROM import_list WHERE id=12`,
		`INSERT INTO movie_exclusion(tmdb_id,created_at) VALUES(201,0)`,
	} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.UpTo(ctx, 146); err != nil {
		t.Fatal(err)
	}
	var name, listType, media, view, versions, key string
	var tmdbID, last int64
	if err := st.Reader().QueryRow(`SELECT name,list_type,media,tmdb_id,view,versions,last_sync_at FROM import_list WHERE id=4`).
		Scan(&name, &listType, &media, &tmdbID, &view, &versions, &last); err != nil {
		t.Fatal(err)
	}
	if name != "One" || listType != "tmdb" || media != "movie" || tmdbID != 9 || view != "radarr" || versions != `["m"]` || last != 5 {
		t.Fatalf("tmdb row: %s %s %s %d %s %s %d", name, listType, media, tmdbID, view, versions, last)
	}
	if err := st.Reader().QueryRow(`SELECT media,api_key FROM import_list WHERE id=7`).Scan(&media, &key); err != nil ||
		media != "series" || key != "key" {
		t.Fatalf("sonarr row: media %q key %q err %v", media, key, err)
	}
	ok := []string{
		`INSERT INTO import_list(name,tmdb_id,media,created_at,updated_at) VALUES('One TV',9,'series',0,0)`,
		`INSERT INTO import_list(name,list_type,rule,created_at,updated_at) VALUES('D1','tmdb_discover','{}',0,0)`,
		`INSERT INTO import_list(name,list_type,rule,created_at,updated_at) VALUES('D2','tmdb_discover','{}',0,0)`,
		`INSERT INTO import_list(name,list_type,url,created_at,updated_at) VALUES('J','json','https://example.org/l.json',0,0)`,
		`INSERT INTO import_list(name,list_type,media,url,created_at,updated_at) VALUES('J','json','series','https://example.org/l.json',0,0)`,
		`INSERT INTO movie_exclusion(tmdb_id,title,year,created_at) VALUES(202,'Two',2020,0)`,
		`INSERT INTO series_exclusion(tvdb_id,title,year,created_at) VALUES(300,'Show',2019,0)`,
	}
	for _, q := range ok {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var id int64
	if err := st.Reader().QueryRow(`SELECT MIN(id) FROM import_list WHERE name='One TV'`).Scan(&id); err != nil || id <= 12 {
		t.Fatalf("new list id %d reuses a deleted one (err %v)", id, err)
	}
	for _, q := range []string{
		`INSERT INTO import_list(name,tmdb_id,created_at,updated_at) VALUES('Again',9,0,0)`,
		`INSERT INTO import_list(name,list_type,url,created_at,updated_at) VALUES('J','json','https://example.org/l.json',0,0)`,
		`INSERT INTO import_list(name,list_type,media,url,created_at,updated_at) VALUES('S','sonarr','movie','http://x',0,0)`,
		`INSERT INTO import_list(name,list_type,created_at,updated_at) VALUES('D','tmdb_discover',0,0)`,
		`INSERT INTO import_list(name,list_type,created_at,updated_at) VALUES('X','imdb',0,0)`,
	} {
		if _, err := st.Writer().Exec(q); err == nil {
			t.Fatalf("accepted: %s", q)
		}
	}
	if _, err := p.DownTo(ctx, 145); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM import_list`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("down kept %d lists, want the tmdb movie and sonarr rows (err %v)", n, err)
	}
	if _, err := st.Writer().Exec(`SELECT * FROM series_exclusion`); err == nil {
		t.Fatal("series_exclusion survived rollback")
	}
	if _, err := p.UpTo(ctx, 146); err != nil {
		t.Fatal(err)
	}
}
