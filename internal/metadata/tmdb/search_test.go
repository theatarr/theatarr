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

type searchDoer struct{ reqs []*http.Request }

func (d *searchDoer) Do(r *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, r)
	body := `{"page": 1, "results": [
	  {"id": 532321, "title": "Re:ZERO -Starting Life in Another World- Memory Snow", "original_title": "Re:ゼロから始める異世界生活 Memory Snow", "release_date": "2018-10-06", "popularity": 9.1},
	  {"id": 1, "title": "Not for work", "release_date": "2018-01-01", "adult": true}]}`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

func TestSearchMovie(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	d := &searchDoer{}
	c, err := New(Options{BaseURL: "https://api.themoviedb.org/3", APIKey: key, Doer: d, MinInterval: time.Millisecond,
		Cache: &SQLCache{Store: st, Source: "tmdb"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.SearchMovie(ctx, "  Re:Zero   Memory Snow ", 2018)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].ID != 532321 || res[0].Year() != 2018 {
		t.Fatalf("results %+v", res)
	}
	q := d.reqs[0].URL.Query()
	if d.reqs[0].URL.Path != "/3/search/movie" || q.Get("query") != "Re:Zero Memory Snow" || q.Get("year") != "2018" ||
		q.Get("include_adult") != "false" {
		t.Errorf("request %s", d.reqs[0].URL.Path)
	}
	// cached: the same search (spacing aside) sends nothing
	if _, err := c.SearchMovie(ctx, "Re:Zero Memory Snow", 2018); err != nil || len(d.reqs) != 1 || c.Hits != 1 {
		t.Errorf("second search: %v, %d requests, %d hits", err, len(d.reqs), c.Hits)
	}
	var k string
	if err := st.Reader().QueryRow(`SELECT key FROM metadata_cache WHERE source = 'tmdb'`).Scan(&k); err != nil || strings.Contains(k, key) {
		t.Errorf("cache key %q %v", k, err)
	}
	if _, err := c.SearchMovie(ctx, " ", 0); err == nil {
		t.Error("empty query")
	}
}
