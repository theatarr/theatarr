package arrview

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/metadata/tmdb"
)

// fakeTMDb is a TMDb API on httptest: title search, movie details, find by TVDB id and series records, from the
// maps below. fail makes every answer a 500.
type fakeTMDb struct {
	mu       sync.Mutex
	search   map[string]string // "query|year" -> results JSON
	movies   map[int]string    // id -> movie JSON
	tvByTVDB map[int]int
	tv       map[int]string // id -> series JSON
	fail     bool
	reqs     []string
}

func (f *fakeTMDb) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r.URL.Path+"?"+r.URL.RawQuery)
	if f.fail {
		http.Error(w, `{"status_message":"down"}`, http.StatusInternalServerError)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/3")
	q := r.URL.Query()
	var body string
	switch {
	case p == "/search/movie":
		body = f.search[q.Get("query")+"|"+q.Get("year")]
		if body == "" {
			body = "[]"
		}
		body = `{"page":1,"results":` + body + `}`
	case strings.HasPrefix(p, "/movie/"):
		id, _ := strconv.Atoi(strings.TrimPrefix(p, "/movie/"))
		body = f.movies[id]
	case strings.HasPrefix(p, "/find/"):
		id, _ := strconv.Atoi(strings.TrimPrefix(p, "/find/"))
		if tv := f.tvByTVDB[id]; tv > 0 {
			body = `{"tv_results":[{"id":` + strconv.Itoa(tv) + `}]}`
		} else {
			body = `{"tv_results":[]}`
		}
	case strings.HasPrefix(p, "/tv/"):
		id, _ := strconv.Atoi(strings.TrimPrefix(p, "/tv/"))
		body = f.tv[id]
	}
	if body == "" {
		http.Error(w, `{"status_code":34}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func (f *fakeTMDb) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

// withTMDb gives the env a TMDb client against f and restarts the service.
func (e *env) withTMDb(f *fakeTMDb) {
	e.t.Helper()
	srv := httptest.NewServer(f)
	e.t.Cleanup(srv.Close)
	c, err := tmdb.New(tmdb.Options{BaseURL: srv.URL + "/3", APIKey: "k", Doer: srv.Client(),
		MinInterval: time.Millisecond, Backoff: time.Millisecond})
	if err != nil {
		e.t.Fatal(err)
	}
	e.tmdb = c
	e.start()
}

const akiraSearch = `[
  {"id": 149, "title": "Akira", "original_title": "AKIRA", "original_language": "ja", "release_date": "1988-07-16",
   "overview": "A secret military project.", "poster_path": "/akira.jpg", "backdrop_path": "/akira-bg.jpg",
   "genre_ids": [16, 878], "vote_average": 7.9, "vote_count": 4200, "popularity": 30.5},
  {"id": 101, "title": "Alpha", "release_date": "2020-05-01", "original_language": "en"},
  {"id": 149, "title": "Akira (duplicate)", "release_date": "1988-07-16"},
  {"id": 0, "title": "No id"}]`

// Movie lookup by name: the catalog first, then TMDb's title search for movies the catalog lacks, as un-added Radarr
// movies (no id) with what the phone apps and Seerr show; a trailing year in the term narrows the search; no
// duplicates; TMDb errors keep the catalog answer. The app's add then posts the tmdbId (Ruddarr's add sheet, LunaSea).
func TestMovieLookupByNameFromTMDb(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := &fakeTMDb{search: map[string]string{
		"Akira|1988":  akiraSearch,
		"alpha|":      `[{"id": 101, "title": "Alpha", "release_date": "2020-05-01"}, {"id": 555, "title": "Alpha Two", "release_date": "2024-02-02", "poster_path": "/a2.jpg"}]`,
		"Delta TMDb|": `[{"id": 104, "title": "Delta (TMDb name)", "release_date": "2022-01-01"}]`,
	}, movies: map[int]string{
		149: `{"id": 149, "title": "Akira", "original_language": "ja", "release_date": "1988-07-16", "genres": [{"id": 16, "name": "Animation"}]}`,
		555: `{"id": 555, "title": "Alpha Two", "original_language": "en", "release_date": "2024-02-02", "genres": [{"id": 28, "name": "Action"}]}`,
	}}
	e.withTMDb(f)

	var res []map[string]any
	e.call("GET", "/radarr/api/v3/movie/lookup?term="+url.QueryEscape("Akira 1988"), nil, 200, &res)
	if len(res) != 2 {
		t.Fatalf("lookup Akira 1988: %d results %v", len(res), res)
	}
	reqs := f.requests()
	if len(reqs) != 1 || !strings.Contains(reqs[0], "query=Akira") || !strings.Contains(reqs[0], "year=1988") {
		t.Errorf("TMDb requests %v", reqs)
	}
	if res[0]["title"] != "Akira" || res[0]["tmdbId"].(float64) != 149 {
		t.Fatalf("first result %v", res[0])
	}
	a := res[0]
	if _, ok := a["id"]; ok {
		t.Errorf("an uncatalogued movie has an id: %v", a["id"])
	}
	if a["year"].(float64) != 1988 || a["overview"] != "A secret military project." || a["folder"] != "Akira (1988)" ||
		a["titleSlug"] != "149" || a["originalTitle"] != "AKIRA" || a["remotePoster"] != "https://image.tmdb.org/t/p/original/akira.jpg" {
		t.Errorf("un-added movie %v", a)
	}
	imgs, _ := a["images"].([]any)
	if len(imgs) != 2 || imgs[0].(map[string]any)["coverType"] != "poster" ||
		imgs[0].(map[string]any)["remoteUrl"] != "https://image.tmdb.org/t/p/original/akira.jpg" {
		t.Errorf("images %v", imgs)
	}
	if r, _ := a["ratings"].(map[string]any)["tmdb"].(map[string]any); r == nil || r["value"].(float64) != 7.9 || r["votes"].(float64) != 4200 {
		t.Errorf("ratings %v", a["ratings"])
	}
	// TMDb's own Alpha (101) is our title: the view's resource with its id.
	if res[1]["id"].(float64) != IDBase+1 || res[1]["title"] != "Alpha" {
		t.Errorf("catalogued result %v", res[1])
	}

	// Catalog matches first, then TMDb's that the catalog lacks, without the duplicate.
	res = nil
	e.call("GET", "/radarr/api/v3/movie/lookup?term=alpha", nil, 200, &res)
	if len(res) != 2 || res[0]["id"].(float64) != IDBase+1 || res[1]["tmdbId"].(float64) != 555 || res[1]["id"] != nil {
		t.Errorf("alpha lookup %v", res)
	}
	// A TMDb result we hold under another name is answered from the catalog (Delta: known, in no view: no id).
	res = nil
	e.call("GET", "/radarr/api/v3/movie/lookup?term="+url.QueryEscape("Delta TMDb"), nil, 200, &res)
	if len(res) != 1 || res[0]["title"] != "Delta" || res[0]["id"] != nil || res[0]["tmdbId"].(float64) != 104 {
		t.Errorf("Delta lookup %v", res)
	}

	// The app's add posts the lookup back with its tmdbId: the movie is created and then answers lookups with an id.
	var m map[string]any
	e.call("POST", "/radarr/api/v3/movie", map[string]any{"title": "Alpha Two", "year": 2024, "tmdbId": 555,
		"qualityProfileId": e.profileID("radarr", "Rules default"), "rootFolderPath": "/media/movies",
		"monitored": true, "minimumAvailability": "released", "addOptions": map[string]any{"searchForMovie": false}}, 201, &m)
	tid := int64(m["id"].(float64)) - IDBase
	if got := e.versionsOf(tid); len(got) != 2 || !got["movie-remux"] || !got["movie-web"] {
		t.Errorf("Alpha Two versions %v", got)
	}
	res = nil
	e.call("GET", "/radarr/api/v3/movie/lookup?term="+url.QueryEscape("alpha"), nil, 200, &res)
	if len(res) != 2 || res[1]["id"] == nil || res[1]["id"].(float64) != float64(IDBase+tid) {
		t.Errorf("after the add %v", res)
	}

	// TMDb down: the catalog's answer, no error.
	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()
	res = nil
	e.call("GET", "/radarr/api/v3/movie/lookup?term=Gamma", nil, 200, &res)
	if len(res) != 1 || res[0]["title"] != "Gamma" {
		t.Errorf("lookup with TMDb down %v", res)
	}
	res = nil
	e.call("GET", "/radarr/api/v3/movie/lookup?term=Nothing", nil, 200, &res)
	if len(res) != 0 {
		t.Errorf("no catalog match with TMDb down %v", res)
	}
}

// A trailing number that is a title, not a year ("Blade Runner 2049"), still finds the movie: the search without
// the year runs when the year-narrowed one finds nothing.
func TestMovieLookupYearFallback(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := &fakeTMDb{search: map[string]string{
		"Wonder Woman 1984|": `[{"id": 464052, "title": "Wonder Woman 1984", "release_date": "2020-12-16"}]`,
	}}
	e.withTMDb(f)
	var res []map[string]any
	e.call("GET", "/radarr/api/v3/movie/lookup?term="+url.QueryEscape("Wonder Woman 1984"), nil, 200, &res)
	if len(res) != 1 || res[0]["tmdbId"].(float64) != 464052 {
		t.Errorf("lookup %v (requests %v)", res, f.requests())
	}
}

// Without a TMDb client the lookup is the catalog's, as before.
func TestMovieLookupNoTMDb(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var res []map[string]any
	e.call("GET", "/radarr/api/v3/movie/lookup?term=Akira", nil, 200, &res)
	if len(res) != 0 {
		t.Errorf("lookup %v", res)
	}
	b, _ := json.Marshal(res)
	if string(b) != "[]" {
		t.Errorf("body %s", b)
	}
}
