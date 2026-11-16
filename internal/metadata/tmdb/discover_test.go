package tmdb

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A discover rule becomes TMDb's discover query (popularity order, the date field of its media, any of its genres),
// read fresh page by page until its limit; each result carries its media type.
func TestDiscover(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		q.Del("api_key")
		queries = append(queries, r.URL.Path+"?"+q.Encode())
		var items []string
		for i := 0; i < 20; i++ {
			items = append(items, fmt.Sprintf(`{"id":%s%02d,"title":"T","name":"N"}`, q.Get("page"), i))
		}
		fmt.Fprintf(w, `{"page":%s,"total_pages":3,"results":[%s]}`, q.Get("page"), strings.Join(items, ","))
	}))
	defer srv.Close()
	c := newClient(t, srv, sqlCache(t))
	ctx := context.Background()
	got, err := c.DiscoverMovies(ctx, Discover{YearFrom: 2010, YearTo: 2020, MinRating: 7.5, MinVotes: 200, Genres: []int{16, 35},
		Language: "ja", Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 30 || got[0].MediaType != "movie" || got[29].ID != 209 || len(queries) != 2 {
		t.Fatalf("got %d results, first %+v, after %v", len(got), got[0], queries)
	}
	want := "/3/discover/movie?include_adult=false&page=1&primary_release_date.gte=2010-01-01&primary_release_date.lte=2020-12-31" +
		"&sort_by=popularity.desc&vote_average.gte=7.5&vote_count.gte=200&with_genres=16%7C35&with_original_language=ja"
	if queries[0] != want {
		t.Errorf("query\n%s\nwant\n%s", queries[0], want)
	}
	// Every page TMDb has, no more: three pages of 20 for a limit of 100.
	queries = nil
	got, err = c.DiscoverTV(ctx, Discover{YearFrom: 2019})
	if err != nil || len(got) != 60 || got[0].MediaType != "tv" || got[0].Name != "N" || len(queries) != 3 ||
		!strings.Contains(queries[0], "first_air_date.gte=2019-01-01") {
		t.Fatalf("tv: %d %v %v", len(got), err, queries)
	}
	if _, err := c.DiscoverMovies(ctx, Discover{Limit: MaxDiscover + 1}); err == nil {
		t.Fatal("a limit over the cap was accepted")
	}
}
