package importarr

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/metadata/tmdb"
)

type fakeTMDb struct{ calls int }

func (f *fakeTMDb) Movie(_ context.Context, id int) (*tmdb.Movie, error) {
	f.calls++
	if id == 200 {
		return nil, tmdb.ErrNotFound
	}
	m := &tmdb.Movie{ID: id, Title: "TMDb title", ReleaseDate: "2004-06-01", IMDbID: "tt0000400"}
	if id == 100 {
		m.IMDbID = "tt9999999" // differs from the arr's
		m.ReleaseDate = "2001-05-01"
	}
	m.ExternalIDs.WikidataID = "Q1"
	m.AlternativeTitles.Titles = []tmdb.AlternativeTitle{{Country: "DE", Title: "Der Titel"}, {Country: "FR", Title: ""}}
	tr := tmdb.Translation{Country: "ES", Language: "es"}
	tr.Data.Title = "El título"
	keep := tmdb.Translation{Country: "US", Language: "en"} // keeps the title: no name
	m.Translations.Translations = []tmdb.Translation{tr, keep}
	m.ReleaseDates.Results = []tmdb.CountryReleases{{Country: "US", Releases: []tmdb.Release{
		{Type: 3, Date: time.Date(2004, 6, 1, 0, 0, 0, 0, time.UTC), Certification: "PG-13"},
		{Type: 3, Date: time.Date(2004, 6, 1, 0, 0, 0, 0, time.UTC)}, // duplicate
		{Type: 4, Date: time.Date(2004, 10, 1, 0, 0, 0, 0, time.UTC)},
	}}}
	return m, nil
}

func TestEnrich(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	srv := httptest.NewServer(newFake("/mnt/movies-a"))
	defer srv.Close()
	runImport(t, &Importer{Store: st, Now: fixedNow}, Source{Instance: "radarr_a", Flavour: "radarr", Version: "movie-remux"}, srv)

	md := &fakeTMDb{}
	now := func() int64 { return 1 }
	sum, err := Enrich(context.Background(), st, md, now)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Candidates != 3 || sum.Looked != 2 || sum.NotFound != 1 {
		t.Errorf("summary %+v", sum)
	}
	if sum.Filled["imdb_id"] != 1 || sum.Filled["release_dates"] != 2 || sum.Filled["wikidata"] != 2 {
		t.Errorf("filled %v", sum.Filled)
	}
	if len(sum.Disagreements) != 1 {
		t.Errorf("disagreements %v", sum.Disagreements)
	}
	if s := one[string](t, st, `SELECT imdb_id FROM title WHERE tmdb_id = 400`); s != "tt0000400" {
		t.Errorf("Delta imdb %q", s)
	}
	if s := one[string](t, st, `SELECT imdb_id FROM title WHERE tmdb_id = 100`); s != "tt0000001" {
		t.Errorf("Alpha imdb overwritten: %q", s)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM title_release_date WHERE source = 'tmdb'`); n != 4 {
		t.Errorf("tmdb dates %d", n)
	}
	if n := one[int](t, st, "SELECT COUNT(*) FROM title_release_date WHERE source='tmdb' AND fetched_at=1"); n != 4 {
		t.Errorf("TMDb fetch timestamps %d", n)
	}
	// Alternative titles and translated titles, without empty ones; Alpha and Delta.
	if n := one[int](t, st, `SELECT COUNT(*) FROM title_alias WHERE source = 'tmdb' AND kind = 'alternative'`); n != 2 {
		t.Errorf("tmdb alternative titles %d", n)
	}
	if s := one[string](t, st, `SELECT t.language || '/' || t.country || '/' || t.name FROM title_alias t JOIN title ti
		ON ti.id = t.title_id WHERE t.source = 'tmdb' AND t.kind = 'translation' AND ti.tmdb_id = 100`); s != "es/ES/El título" {
		t.Errorf("translation %q", s)
	}
	if sum.Filled["aliases"] != 2 {
		t.Errorf("filled aliases %v", sum.Filled)
	}
	// Second run: only the title TMDb does not know is a candidate again, and nothing changes.
	sum, err = Enrich(context.Background(), st, md, now)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Candidates != 1 || sum.Changes.Total() != 0 {
		t.Errorf("second run %+v %s", sum, sum.Changes)
	}
}
