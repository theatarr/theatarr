package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

type searchGateway struct{ searches atomic.Int32 }

func (g *searchGateway) Do(ctx context.Context, _ gateway.Target, req *http.Request) (*http.Response, error) {
	if req.URL.Query().Get("t") != "caps" {
		g.searches.Add(1)
	}
	return http.DefaultClient.Do(req.WithContext(ctx))
}

func TestEpisodeCounts(t *testing.T) {
	t.Parallel()
	eps := []decide.Episode{{Season: 0, Number: 1}, {Season: 1, Number: 1}, {Season: 1, Number: 2}, {Season: 2, Number: 1}}
	got := EpisodeCounts(eps)
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 1 {
		t.Errorf("counts %v", got)
	}
	if EpisodeCounts(nil) != nil {
		t.Error("no episodes must give no counts (the indexer's counter then looks them up)")
	}
}

func TestSearchRequestsSharesQueriesAcrossVersions(t *testing.T) {
	t.Parallel()
	var searches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("t") == "caps" {
			fmt.Fprint(w, `<caps><limits default="100" max="100"/><searching><movie-search available="yes" supportedParams="imdbid,tmdbid"/></searching><categories><category id="2000" name="Movies"/></categories></caps>`)
			return
		}
		searches.Add(1)
		fmt.Fprint(w, `<rss version="2.0"><channel></channel></rss>`)
	}))
	defer srv.Close()
	profile := &policy.Profile{Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}
	version := func(key string) *policy.VersionPolicy {
		return &policy.VersionPolicy{Kind: policy.KindMovie, Profile: "web", Indexers: []string{"shared"}}
	}
	pol := &policy.Policy{Profiles: map[string]*policy.Profile{"web": profile},
		Versions: map[string]*policy.VersionPolicy{"web-a": version("web-a"), "web-b": version("web-b")},
		Indexers: map[string]*policy.Indexer{"shared": {Kind: "torznab", URL: srv.URL + "/api", Privacy: policy.PrivacyPublic}}}
	eng, err := decide.New(pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	gw := &searchGateway{}
	idx, err := indexer.New(pol, gw, indexer.Options{CacheTTL: -1})
	if err != nil {
		t.Fatal(err)
	}
	title := decide.Title{ID: 1, Kind: policy.KindMovie, Name: "Arrival", Year: 2016, IMDb: "tt2543164", TMDb: 329865}
	targets := []SearchTarget{
		{ID: "a", Version: decide.Version{ID: 1, Key: "web-a", Policy: pol.Versions["web-a"]}},
		{ID: "b", Version: decide.Version{ID: 2, Key: "web-b", Policy: pol.Versions["web-b"]}},
	}
	got, err := SearchRequests(context.Background(), eng, idx, title, targets)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["a"].Request.Version != "web-a" || got["b"].Request.Version != "web-b" {
		t.Fatalf("search results %v", got)
	}
	if searches.Load() != 1 || gw.searches.Load() != 1 {
		t.Fatalf("upstream searches=%d gateway searches=%d, want 1 each", searches.Load(), gw.searches.Load())
	}
}

// TestSearchNoAnswer: a target whose every indexer request failed is marked NoAnswer, so acquire does not
// count it as searched; an empty answer is an answer.
func TestSearchNoAnswer(t *testing.T) {
	t.Parallel()
	for _, down := range []bool{false, true} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			if r.URL.Query().Get("t") == "caps" {
				fmt.Fprint(w, `<caps><limits default="100" max="100"/><searching><movie-search available="yes" supportedParams="imdbid,tmdbid"/></searching><categories><category id="2000" name="Movies"/></categories></caps>`)
				return
			}
			if down {
				http.Error(w, "down", http.StatusBadGateway)
				return
			}
			fmt.Fprint(w, `<rss version="2.0"><channel></channel></rss>`)
		}))
		profile := &policy.Profile{Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}
		pol := &policy.Policy{Profiles: map[string]*policy.Profile{"web": profile},
			Versions: map[string]*policy.VersionPolicy{"web": {Kind: policy.KindMovie, Profile: "web",
				Indexers: []string{"one"}}},
			Indexers: map[string]*policy.Indexer{"one": {Kind: "torznab", URL: srv.URL + "/api",
				Privacy: policy.PrivacyPublic}}}
		eng, err := decide.New(pol, decide.Options{})
		if err != nil {
			t.Fatal(err)
		}
		idx, err := indexer.New(pol, &searchGateway{}, indexer.Options{CacheTTL: -1})
		if err != nil {
			t.Fatal(err)
		}
		title := decide.Title{ID: 1, Kind: policy.KindMovie, Name: "Arrival", Year: 2016, IMDb: "tt2543164", TMDb: 329865}
		got, err := SearchRequests(context.Background(), eng, idx, title, []SearchTarget{
			{ID: "a", Version: decide.Version{ID: 1, Key: "web", Policy: pol.Versions["web"]}}})
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got["a"].NoAnswer != down {
			t.Errorf("indexer down %v: NoAnswer %v (notes %v)", down, got["a"].NoAnswer, got["a"].Notes)
		}
	}
}

// TestSearchDropsIDPadding: rows of an id answer that are not about the title are dropped; when none is left, the
// title search runs; when one is left, it does not.
func TestSearchDropsIDPadding(t *testing.T) {
	t.Parallel()
	item := func(title, hash string) string {
		return `<item><title>` + title + `</title><guid>` + hash + `</guid><size>20000000000</size><category>2000</category>` +
			`<torznab:attr name="infohash" value="` + hash + `"/><torznab:attr name="seeders" value="10"/></item>`
	}
	padding := item("Unrelated.Film.2019.2160p.WEB-DL-GRP", strings.Repeat("b", 40))
	arrival := item("Arrival.2016.2160p.WEB-DL-GRP", strings.Repeat("a", 40))
	for _, tc := range []struct {
		name     string
		idAnswer string
		searches int32
	}{
		{"padding only", padding, 2},
		{"padding and the title", padding + arrival, 1},
	} {
		var searches atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			if r.URL.Query().Get("t") == "caps" {
				fmt.Fprint(w, `<caps><limits default="100" max="100"/><searching><search available="yes" supportedParams="q"/><movie-search available="yes" supportedParams="imdbid"/></searching><categories><category id="2000" name="Movies"/></categories></caps>`)
				return
			}
			searches.Add(1)
			body := arrival
			if r.URL.Query().Has("imdbid") {
				body = tc.idAnswer
			}
			fmt.Fprint(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`+body+`</channel></rss>`)
		}))
		profile := &policy.Profile{Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}
		pol := &policy.Policy{Profiles: map[string]*policy.Profile{"web": profile},
			Versions: map[string]*policy.VersionPolicy{"web": {Kind: policy.KindMovie, Profile: "web", Indexers: []string{"one"}}},
			Indexers: map[string]*policy.Indexer{"one": {Kind: "torznab", URL: srv.URL + "/api", Privacy: policy.PrivacyPublic}}}
		eng, err := decide.New(pol, decide.Options{})
		if err != nil {
			t.Fatal(err)
		}
		idx, err := indexer.New(pol, &searchGateway{}, indexer.Options{CacheTTL: -1})
		if err != nil {
			t.Fatal(err)
		}
		title := decide.Title{ID: 1, Kind: policy.KindMovie, Name: "Arrival", Year: 2016, IMDb: "tt2543164", TMDb: 329865}
		got, err := SearchRequests(context.Background(), eng, idx, title, []SearchTarget{
			{ID: "a", Version: decide.Version{ID: 1, Key: "web", Policy: pol.Versions["web"]}}})
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if n := searches.Load(); n != tc.searches {
			t.Errorf("%s: %d upstream searches, want %d", tc.name, n, tc.searches)
		}
		r := got["a"].Ranked
		if len(r) != 1 || r[0].Release != "Arrival.2016.2160p.WEB-DL-GRP" || !r[0].Accepted {
			for _, d := range r {
				t.Logf("%s: %s accepted=%v %v", tc.name, d.Release, d.Accepted, d.Rejections)
			}
			t.Errorf("%s: ranked %d decisions, want only Arrival, accepted", tc.name, len(r))
		}
	}
}
