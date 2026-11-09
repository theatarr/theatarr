package live

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/metadata/tmdb"
)

type tmdbDoer struct{ n int }

func (d *tmdbDoer) Do(r *http.Request) (*http.Response, error) {
	d.n++
	body := `{"results": [{"id": 566451, "title": "Re:ZERO -Starting Life in Another World- The Frozen Bond",
		"original_title": "Re:ゼロから始める異世界生活 氷結の絆", "release_date": "2019-11-08"}]}`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

func TestTMDbMoviesSearch(t *testing.T) {
	d := &tmdbDoer{}
	c, err := tmdb.New(tmdb.Options{BaseURL: "https://api.themoviedb.org/3", APIKey: "k", Doer: d, MinInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	res, err := TMDbMovies{C: c}.Search(context.Background(), "Re:Zero Frozen Bond")
	if err != nil || len(res) != 1 {
		t.Fatalf("%v %v", res, err)
	}
	r := res[0]
	if r.TMDbID != 566451 || *r.Year != 2019 || *r.InCinemas != "2019-11-08" || !strings.Contains(*r.Title, "Frozen Bond") || d.n != 1 {
		t.Errorf("%+v", r)
	}
}
