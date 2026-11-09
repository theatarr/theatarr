package tmdb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

const key = "tmdb-test-key-0123"

// A trimmed movie answer, written by hand in TMDb's shape.
const movieBody = `{"id": 603, "imdb_id": "tt0133093", "title": "The Matrix", "original_title": "The Matrix",
 "original_language": "en", "release_date": "1999-03-31", "runtime": 136, "status": "Released",
 "belongs_to_collection": {"id": 2344, "name": "The Matrix Collection"},
 "external_ids": {"imdb_id": "tt0133093", "wikidata_id": "Q83495"},
 "release_dates": {"results": [{"iso_3166_1": "US", "release_dates": [
   {"certification": "R", "iso_639_1": "", "note": "", "release_date": "1999-03-31T00:00:00.000Z", "type": 3},
   {"certification": "R", "iso_639_1": "", "note": "DVD", "release_date": "1999-09-21T00:00:00.000Z", "type": 5}]}]},
 "alternative_titles": {"titles": [{"iso_3166_1": "IT", "title": "Matrix", "type": ""}]},
 "translations": {"translations": [
   {"iso_3166_1": "DE", "iso_639_1": "de", "name": "Deutsch", "english_name": "German", "data": {"title": "Matrix", "overview": "..."}},
   {"iso_3166_1": "US", "iso_639_1": "en", "name": "English", "english_name": "English", "data": {"title": ""}}]}}`

type fake struct {
	mu    sync.Mutex
	calls []string
	fail  int // answer 429 this many times first
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.URL.Path)
	if r.URL.Query().Get("api_key") != key {
		http.Error(w, `{"status_message":"Invalid API key"}`, http.StatusUnauthorized)
		return
	}
	if f.fail > 0 {
		f.fail--
		w.Header().Set("Retry-After", "1")
		http.Error(w, "slow down", http.StatusTooManyRequests)
		return
	}
	switch r.URL.Path {
	case "/3/movie/603":
		if r.URL.Query().Get("append_to_response") != "external_ids,release_dates,alternative_titles,translations" {
			http.Error(w, "want append_to_response", 400)
			return
		}
		io.WriteString(w, movieBody)
	case "/3/find/tt0133093":
		io.WriteString(w, `{"movie_results": [{"id": 603, "title": "The Matrix", "release_date": "1999-03-31"}]}`)
	case "/3/find/tt9999999":
		io.WriteString(w, `{"movie_results": []}`)
	default:
		http.Error(w, `{"status_code": 34}`, 404)
	}
}

func newClient(t *testing.T, srv *httptest.Server, cache Cache) *Client {
	t.Helper()
	c, err := New(Options{BaseURL: srv.URL + "/3", APIKey: key, Doer: srv.Client(), Cache: cache,
		MinInterval: time.Millisecond, Now: func() time.Time { return time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func sqlCache(t *testing.T) *SQLCache {
	t.Helper()
	st, err := store.Open(context.Background(), storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &SQLCache{Store: st, Source: "tmdb"}
}

func TestMovieAndCache(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cache := sqlCache(t)
	c := newClient(t, srv, cache)
	ctx := context.Background()

	m, err := c.Movie(ctx, 603)
	if err != nil {
		t.Fatal(err)
	}
	if m.IMDbID != "tt0133093" || m.Year() != 1999 || m.ExternalIDs.WikidataID != "Q83495" ||
		m.BelongsToCollection.ID != 2344 || len(m.ReleaseDates.Results[0].Releases) != 2 ||
		m.AlternativeTitles.Titles[0].Country != "IT" || len(m.Translations.Translations) != 2 ||
		m.Translations.Translations[0].Data.Title != "Matrix" || m.Translations.Translations[0].Language != "de" {
		t.Errorf("movie %+v", m)
	}
	if TypeName(m.ReleaseDates.Results[0].Releases[1].Type) != "physical" {
		t.Error("release type")
	}
	// External ids and release dates come from the cached answer.
	if _, err := c.ExternalIDs(ctx, 603); err != nil {
		t.Fatal(err)
	}
	if rd, err := c.ReleaseDates(ctx, 603); err != nil || rd[0].Country != "US" {
		t.Fatal(rd, err)
	}
	if len(f.calls) != 1 || c.Requests != 1 || c.Hits != 2 {
		t.Errorf("calls %v requests %d hits %d", f.calls, c.Requests, c.Hits)
	}
	// The cache key never holds the key; an old release gets the long TTL.
	var k string
	var ttl int64
	cache.Store.Reader().QueryRow(`SELECT key, expires_at - fetched_at FROM metadata_cache`).Scan(&k, &ttl)
	if strings.Contains(k, key) || k != "/movie/603?append_to_response=external_ids%2Crelease_dates%2Calternative_titles%2Ctranslations" {
		t.Errorf("cache key %q", k)
	}
	if time.Duration(ttl)*time.Millisecond != 30*24*time.Hour {
		t.Errorf("ttl %v", time.Duration(ttl)*time.Millisecond)
	}
	// An expired entry is a miss.
	cache.Now = func() time.Time { return time.Now().Add(31 * 24 * time.Hour) }
	if _, err := c.Movie(ctx, 603); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 {
		t.Errorf("expired entry served: %v", f.calls)
	}
}

func TestNotFoundAndFind(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := newClient(t, srv, sqlCache(t))
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := c.Movie(ctx, 1); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing movie: %v", err)
		}
	}
	if len(f.calls) != 1 {
		t.Errorf("404 not cached: %v", f.calls)
	}
	id, err := c.FindByIMDb(ctx, "tt0133093")
	if err != nil || id != 603 {
		t.Errorf("find %d %v", id, err)
	}
	if _, err := c.FindByIMDb(ctx, "tt9999999"); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty find %v", err)
	}
	if _, err := c.FindByIMDb(ctx, "603"); err == nil {
		t.Error("bad IMDb id accepted")
	}
}

func TestRetryAndMask(t *testing.T) {
	f := &fake{fail: 1}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := newClient(t, srv, nil)
	if _, err := c.Movie(context.Background(), 603); err != nil {
		t.Fatalf("after one 429: %v", err)
	}
	if len(f.calls) != 2 {
		t.Errorf("calls %v", f.calls)
	}
	// A transport error carries the URL, key included: it must come back masked.
	bad, _ := New(Options{BaseURL: "http://127.0.0.1:1/3", APIKey: key, MinInterval: time.Millisecond, Backoff: time.Millisecond,
		Doer: doerFunc(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed for " + r.URL.String())
		})})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := bad.Movie(ctx, 603)
	if err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "***") {
		t.Errorf("unmasked: %v", err)
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (d doerFunc) Do(r *http.Request) (*http.Response, error) { return d(r) }
