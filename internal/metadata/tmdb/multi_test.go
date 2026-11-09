package tmdb

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

type multiDoer struct{ reqs []*http.Request }

func (d *multiDoer) Do(r *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, r)
	body := `{"page": 1, "results": [
	  {"media_type": "movie", "id": 438631, "title": "Dune", "release_date": "2021-09-15", "poster_path": "/d5NXSklXo0qyIYkgV94XAgMIckC.jpg", "genre_ids": [878, 12]},
	  {"media_type": "person", "id": 1, "name": "Someone"},
	  {"media_type": "tv", "id": 90228, "name": "Dune: Prophecy", "first_air_date": "2024-11-17", "original_language": "en"},
	  {"media_type": "tv", "id": 5, "name": "Dune Anime", "first_air_date": "", "original_language": "ja", "genre_ids": [16]},
	  {"media_type": "movie", "id": 2, "title": "Adult", "adult": true}]}`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

func TestSearchMulti(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	d := &multiDoer{}
	c, err := New(Options{BaseURL: "https://api.themoviedb.org/3", APIKey: key, Doer: d, MinInterval: time.Millisecond,
		Cache: &SQLCache{Store: st, Source: "tmdb"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.SearchMulti(ctx, "  Dune  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 || res[0].DisplayName() != "Dune" || res[0].Year() != 2021 || res[1].DisplayName() != "Dune: Prophecy" ||
		res[1].Year() != 2024 || res[2].Year() != 0 {
		t.Fatalf("results %+v", res)
	}
	if res[0].LooksAnime() || res[1].LooksAnime() || !res[2].LooksAnime() {
		t.Error("anime guess")
	}
	q := d.reqs[0].URL.Query()
	if d.reqs[0].URL.Path != "/3/search/multi" || q.Get("query") != "dune" || q.Get("include_adult") != "false" {
		t.Errorf("request %s %v", d.reqs[0].URL.Path, q)
	}
	// Cached, whatever the case and spacing.
	if _, err := c.SearchMulti(ctx, "DUNE"); err != nil || len(d.reqs) != 1 {
		t.Errorf("second search: %v, %d requests", err, len(d.reqs))
	}
	if _, err := c.SearchMulti(ctx, "   "); err == nil {
		t.Error("empty query accepted")
	}
}

// The anime guess is the same for search results, movie details and series records: Japanese-language animation.
func TestLooksAnimeDetails(t *testing.T) {
	anim := []Genre{{ID: GenreAnimation, Name: "Animation"}}
	m := &Movie{OriginalLanguage: "ja"}
	m.Genres = append(m.Genres, struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}{ID: GenreAnimation, Name: "Animation"})
	if !m.LooksAnime() {
		t.Error("Japanese animated movie")
	}
	m.OriginalLanguage = "en"
	if m.LooksAnime() {
		t.Error("English animated movie")
	}
	if !(&TVSeries{OriginalLanguage: "ja", Genres: anim}).LooksAnime() || (&TVSeries{OriginalLanguage: "ja"}).LooksAnime() {
		t.Error("series guess")
	}
	if !LooksAnime("ja", []int{878, GenreAnimation}) || LooksAnime("ko", []int{GenreAnimation}) {
		t.Error("LooksAnime")
	}
}
