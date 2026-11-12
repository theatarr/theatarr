package importarr

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"
)

type collectionRow struct {
	name, versions, minimum string
	monitored, search       bool
}

func readCollection(t *testing.T, w interface {
	QueryRow(string, ...any) *sql.Row
}, id int) collectionRow {
	t.Helper()
	var c collectionRow
	if err := w.QueryRow(`SELECT name, versions, minimum_availability, monitored, search_on_add FROM movie_collection
		WHERE tmdb_id = ?`, id).Scan(&c.name, &c.versions, &c.minimum, &c.monitored, &c.search); err != nil {
		t.Fatalf("collection %d: %v", id, err)
	}
	return c
}

func collectionSnapshot() *Snapshot {
	return &Snapshot{Instance: "radarr_4k", Flavour: "radarr", Collections: []Collection{
		{TmdbID: 10, Name: "Alien", Monitored: true, SearchOnAdd: true, MinimumAvailability: "announced"},
		{TmdbID: 11, Name: "Bourne", Monitored: false, SearchOnAdd: true},
		{TmdbID: 12, Name: "Cars", Monitored: true, MinimumAvailability: "odd"},
		{TmdbID: 14, Name: "Dune", Monitored: true},
		{TmdbID: 15, Name: "Evil Dead", Monitored: true},
	}}
}

// The arr's monitored collections become monitored intake collections targeting the version when it
// is a collection target; collections the arr does not monitor, and Theatarr's own, are left alone; a re-run changes
// nothing.
func TestImportCollections(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	ctx := context.Background()
	for _, q := range []string{
		// Theatarr monitors Bourne; the arr does not: it stays as it is.
		`INSERT INTO movie_collection (tmdb_id, name, monitored, versions, search_on_add, updated_at) VALUES (11, 'Bourne', 1, '["movie-web"]', 0, 0)`,
		// Discovered and off: the import turns it on with the arr's settings and targets.
		`INSERT INTO movie_collection (tmdb_id, name, monitored, versions, updated_at) VALUES (12, '', 0, '["movie-web"]', 0)`,
		// Theatarr's own, unknown to the arr.
		`INSERT INTO movie_collection (tmdb_id, name, monitored, versions, updated_at) VALUES (13, 'Mine', 1, '["movie-web"]', 0)`,
		// Already monitored with no targets: it gets the arr's explicit targets, search on add follows the arr.
		`INSERT INTO movie_collection (tmdb_id, name, monitored, versions, search_on_add, updated_at) VALUES (14, 'Dune', 1, '[]', 0, 0)`,
		// Already monitored with a shadow target (movie-remux) and an eligible one: the shadow one goes.
		`INSERT INTO movie_collection (tmdb_id, name, monitored, versions, search_on_add, updated_at) VALUES (15, 'Evil Dead', 1, '["movie-remux","movie-web"]', 0, 0)`,
	} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	im := &Importer{Store: st, Now: fixedNow}
	src := Source{Instance: "radarr_4k", Flavour: "radarr", Version: "movie-4k-theatarr",
		CollectionTargets: []string{"movie-4k-theatarr", "movie-web"}}
	snap := collectionSnapshot()
	snap.Collections[3].SearchOnAdd = true
	sum, err := im.Write(ctx, src, snap)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Collections != (CollectionCounts{Monitored: 4, Added: 1, Updated: 3}) {
		t.Errorf("counts %+v", sum.Collections)
	}
	db := st.Reader()
	if got, want := readCollection(t, db, 10), (collectionRow{"Alien", `["movie-4k-theatarr"]`, "announced", true, true}); got != want {
		t.Errorf("new collection %+v, want %+v", got, want)
	}
	if got, want := readCollection(t, db, 11), (collectionRow{"Bourne", `["movie-web"]`, "released", true, false}); got != want {
		t.Errorf("unmonitored in the arr %+v, want %+v", got, want)
	}
	if got, want := readCollection(t, db, 12), (collectionRow{"Cars", `["movie-4k-theatarr"]`, "released", true, false}); got != want {
		t.Errorf("discovered %+v, want %+v", got, want)
	}
	if got, want := readCollection(t, db, 13), (collectionRow{"Mine", `["movie-web"]`, "released", true, false}); got != want {
		t.Errorf("theatarr's own %+v, want %+v", got, want)
	}
	if got := readCollection(t, db, 14); got.versions != `["movie-4k-theatarr"]` || !got.search {
		t.Errorf("empty targets %+v", got)
	}
	if got := readCollection(t, db, 15); got.versions != `["movie-4k-theatarr","movie-web"]` || got.search {
		t.Errorf("shadow target %+v", got)
	}

	// Idempotent.
	sum, err = im.Write(ctx, src, collectionSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if sum.Collections != (CollectionCounts{Monitored: 4}) || sum.Changes["movie_collection"] != nil {
		t.Errorf("re-run counts %+v, changes %+v", sum.Collections, sum.Changes["movie_collection"])
	}

	// Another library of the instance's that is a target, added to what is already chosen; the arr unmonitoring
	// changes nothing.
	src.Version, src.CollectionTargets = "movie-4k", append(src.CollectionTargets, "movie-4k")
	snap = collectionSnapshot()
	snap.Collections[2].Monitored = false
	if _, err := im.Write(ctx, src, snap); err != nil {
		t.Fatal(err)
	}
	if got := readCollection(t, db, 10); got.versions != `["movie-4k","movie-4k-theatarr"]` || !got.search {
		t.Errorf("active version %+v", got)
	}
	if got := readCollection(t, db, 12); !got.monitored || got.versions != `["movie-4k-theatarr"]` {
		t.Errorf("unmonitored in the arr later %+v", got)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM movie_collection`); n != 6 {
		t.Errorf("%d collections", n)
	}
}

// A version that is not a collection target (a Watch-only version) has nowhere to add a collection's movies: nothing
// is written, with a warning.
func TestImportCollectionsNoTargets(t *testing.T) {
	t.Parallel()
	for _, src := range []Source{
		{Instance: "radarr_anime", Version: "anime-movie"},
		{Instance: "radarr_a", Version: "movie", CollectionTargets: []string{"movie-web"}},
	} {
		st := openStore(t)
		im := &Importer{Store: st, Now: fixedNow}
		sum, err := im.Write(context.Background(), src, collectionSnapshot())
		if err != nil {
			t.Fatal(err)
		}
		if n := one[int](t, st, `SELECT COUNT(*) FROM movie_collection`); n != 0 {
			t.Errorf("%s: %d collections written", src.Version, n)
		}
		if len(sum.Warnings) != 1 || !strings.Contains(sum.Warnings[0], "4 monitored collections not carried over") {
			t.Errorf("%s: warnings %v", src.Version, sum.Warnings)
		}
	}
}

// FetchRadarr reads GET collection; a Radarr that has no such endpoint (404) imports as before, without a warning.
func TestFetchRadarrCollections(t *testing.T) {
	t.Parallel()
	fake := newFake("/mnt/movies-a")
	fake.collections = []map[string]any{
		{"id": 1, "title": "Alien Collection", "tmdbId": 8091, "monitored": true, "searchOnAdd": true,
			"minimumAvailability": "inCinemas", "qualityProfileId": 7, "rootFolderPath": "/mnt/movies-a", "movies": []any{}},
		{"id": 2, "title": "Other", "tmdbId": 9100, "monitored": false},
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	st := openStore(t)
	im := &Importer{Store: st, Now: fixedNow}
	src := Source{Instance: "radarr_a", Flavour: "radarr", Version: "movie-remux", CollectionTargets: []string{"movie-remux"}}
	sum := runImport(t, im, src, srv)
	if sum.Collections != (CollectionCounts{Monitored: 1, Added: 1}) {
		t.Errorf("counts %+v", sum.Collections)
	}
	if got, want := readCollection(t, st.Reader(), 8091), (collectionRow{"Alien Collection", `["movie-remux"]`, "inCinemas", true, true}); got != want {
		t.Errorf("collection %+v, want %+v", got, want)
	}

	fake.mu.Lock()
	fake.collections = nil
	fake.mu.Unlock()
	st2 := openStore(t)
	sum = runImport(t, &Importer{Store: st2, Now: fixedNow}, src, srv)
	if len(sum.Warnings) != 0 || sum.Collections != (CollectionCounts{}) {
		t.Errorf("no collection endpoint: warnings %v, counts %+v", sum.Warnings, sum.Collections)
	}
}
