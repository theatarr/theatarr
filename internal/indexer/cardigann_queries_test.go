package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer/cardigann"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestCardigannCriteriaKeywords(t *testing.T) {
	for _, tc := range []struct {
		q    Query
		want string
	}{
		{Query{Type: QuerySeries, Title: "Daily Show", Criteria: CriteriaDaily, AirDate: "2024-03-05"}, "Daily Show 2024 03 05"},
		{Query{Type: QuerySeries, Title: "Anime", Criteria: CriteriaAnimeEpisode, Season: 2, Episode: 1, Absolute: 29}, "Anime 29"},
	} {
		if got := cardigannKeywords(tc.q); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
}

func TestCardigannSendsSceneTitleAndAbsoluteKeywords(t *testing.T) {
	n := newNet(t)
	var mu sync.Mutex
	var asked []string
	n.serve("site.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Query().Get("q"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	}))
	h := cardigannHarness(t, n, "public", nil, nil, false)
	res, err := h.svc.Search(context.Background(), "site", Query{Type: QuerySeries, Title: "Frieren", Criteria: CriteriaAnimeEpisode, Season: 2, Episode: 1, Absolute: 29, SceneTitles: []string{"Sousou no Frieren", "Frieren"}})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, q := range []string{"Sousou no Frieren 29", "Frieren 29", "Sousou no Frieren S02E01", "Frieren S02E01"} {
		if !slices.Contains(asked, q) {
			t.Errorf("missing %q in requests %v", q, asked)
		}
	}
	if len(asked) != 4 || res.Requests != 4 {
		t.Fatalf("requests %v, accounting %d", asked, res.Requests)
	}
}

// idSiteDef is a JSON site whose definition lists ids for movie and TV searches.
const idSiteDef = `---
id: site
name: Site
type: public
links:
  - http://site.test/
caps:
  categorymappings:
    - {id: 1, cat: Movies, desc: "Movies"}
    - {id: 2, cat: TV, desc: "TV"}
  modes:
    search: [q]
    movie-search: [q, imdbid]
    tv-search: [q, season, ep, tvdbid]
search:
  paths:
    - path: api/search
      response:
        type: json
  inputs:
    q: "{{ if .Query.IMDBID }}{{ .Query.IMDBID }}{{ else }}{{ .Keywords }}{{ end }}"
    tvdb: "{{ .Query.TVDBID }}"
    season: "{{ .Query.Season }}"
    ep: "{{ .Query.Ep }}"
  rows:
    selector: results
  fields:
    title:
      selector: name
    infohash:
      selector: hash
`

// idSite serves idSiteDef, answering a release only to requests answer accepts; it returns the requests' queries.
func idSite(t *testing.T, answer func(url.Values) bool) (*harness, func() []url.Values) {
	t.Helper()
	n := newNet(t)
	var (
		mu    sync.Mutex
		asked []url.Values
	)
	n.serve("site.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Query())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if answer(r.URL.Query()) {
			fmt.Fprintf(w, `{"results":[{"name":"Found.1080p","hash":"%s"}]}`, siteHash)
			return
		}
		fmt.Fprint(w, `{"results":[]}`)
	}))
	p := testPolicy()
	p.Indexers["site"] = &policy.Indexer{Kind: "cardigann", Definition: "site", URL: "http://site.test/",
		Privacy: policy.PrivacyPublic, Budget: "site"}
	p.Budgets["site"] = &policy.Budget{Requests: 60, Per: policy.Duration(time.Minute), Burst: 10}
	m := cardigann.NewManager(testDefs{"site": idSiteDef}, nil)
	m.DefaultDelay = time.Millisecond
	h := newHarness(t, n, p, false, func(_ *gateway.Options, o *Options) { o.Cardigann = m })
	return h, func() []url.Values {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(asked)
	}
}

// A definition listing imdbid is asked by id first; the title follows only when the id found nothing.
func TestCardigannMovieIDFirstThenTitle(t *testing.T) {
	q := Query{Type: QueryMovie, Title: "The Matrix", Year: 1999, IMDb: "tt0133093"}
	h, asked := idSite(t, func(v url.Values) bool { return v.Get("q") == "tt0133093" })
	if res := search(t, h, "site", q); len(res.Releases) != 1 || len(asked()) != 1 {
		t.Fatalf("id answered: %d releases, requests %v", len(res.Releases), asked())
	}
	h, asked = idSite(t, func(url.Values) bool { return false })
	search(t, h, "site", q)
	got := asked()
	if len(got) != 2 || got[0].Get("q") != "tt0133093" || got[1].Get("q") != "The Matrix 1999" {
		t.Fatalf("requests %v", got)
	}
}

// A series id search carries the season and episode; the keyword search follows with them but without the id. A
// title-only mapping sends only the keyword search; an id-only mapping without a listed id is not searched by title
// (as on a Torznab feed), and with one it is never followed by a keyword search.
func TestCardigannSeriesIDCarriesSeasonEpisode(t *testing.T) {
	q := Query{Type: QuerySeries, Title: "Show", TVDB: 12345, Season: 1, Episode: 2, Criteria: CriteriaEpisode}
	h, asked := idSite(t, func(url.Values) bool { return false })
	search(t, h, "site", q)
	got := asked()
	if len(got) != 2 {
		t.Fatalf("requests %v", got)
	}
	if g := got[0]; g.Get("tvdb") != "12345" || g.Get("season") != "1" || g.Get("ep") != "2" || g.Get("q") != "S01E02" {
		t.Errorf("id request %v", g)
	}
	if g := got[1]; g.Get("tvdb") != "" || g.Get("season") != "1" || g.Get("ep") != "2" || g.Get("q") != "Show S01E02" {
		t.Errorf("keyword request %v", g)
	}

	titleOnly := q
	titleOnly.Mode = SearchTitle
	h, asked = idSite(t, func(url.Values) bool { return false })
	search(t, h, "site", titleOnly)
	if got := asked(); len(got) != 1 || got[0].Get("tvdb") != "" || got[0].Get("q") != "Show S01E02" {
		t.Errorf("title-only mapping: %v", got)
	}

	idOnly := q
	idOnly.Mode = SearchID
	h, asked = idSite(t, func(url.Values) bool { return false })
	search(t, h, "site", idOnly)
	if got := asked(); len(got) != 1 || got[0].Get("tvdb") != "12345" {
		t.Errorf("id-only mapping: %v", got)
	}

	idOnly.TVDB = 0
	h, asked = idSite(t, func(url.Values) bool { return true })
	if _, err := h.svc.Search(context.Background(), "site", idOnly); !errors.Is(err, ErrUnsupported) || len(asked()) != 0 {
		t.Errorf("id-only mapping without an id: %v, requests %v", err, asked())
	}
}
