package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
		if got["a"].Indexers != 1 {
			t.Errorf("indexer down %v: Indexers %d, want 1 (asked, answered or not)", down, got["a"].Indexers)
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

// TestSearchByOriginalTitle: a film found only under its original title is found by one more keyword request with
// that title (one q per request); a productive id search still asks no keywords.
func TestSearchByOriginalTitle(t *testing.T) {
	t.Parallel()
	item := func(title, hash string) string {
		return `<item><title>` + title + `</title><guid>` + hash + `</guid><size>20000000000</size><category>2000</category>` +
			`<torznab:attr name="infohash" value="` + hash + `"/><torznab:attr name="seeders" value="10"/></item>`
	}
	original := item("Kimi.no.Na.wa.2016.2160p.WEB-DL-GRP", strings.Repeat("a", 40))
	for _, tc := range []struct {
		name     string
		idAnswer string
		want     []string
	}{
		{"id finds nothing", "", []string{"imdbid=2543164", "q=Your Name 2016", "q=Kimi no Na wa 2016"}},
		{"id finds the film", original, []string{"imdbid=2543164"}},
	} {
		var mu sync.Mutex
		var asked []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			q := r.URL.Query()
			if q.Get("t") == "caps" {
				fmt.Fprint(w, `<caps><limits default="100" max="100"/><searching><search available="yes" supportedParams="q"/><movie-search available="yes" supportedParams="imdbid"/></searching><categories><category id="2000" name="Movies"/></categories></caps>`)
				return
			}
			body := ""
			mu.Lock()
			switch {
			case q.Has("imdbid"):
				asked = append(asked, "imdbid="+q.Get("imdbid"))
				body = tc.idAnswer
			default:
				for _, v := range q["q"] {
					asked = append(asked, "q="+v)
				}
				if q.Get("q") == "Kimi no Na wa 2016" {
					body = original
				}
			}
			mu.Unlock()
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
		title := decide.Title{ID: 1, Kind: policy.KindMovie, Name: "Your Name.", OriginalTitle: "Kimi no Na wa.", Year: 2016,
			IMDb: "tt2543164"}
		got, err := SearchRequests(context.Background(), eng, idx, title, []SearchTarget{
			{ID: "a", Version: decide.Version{ID: 1, Key: "web", Policy: pol.Versions["web"]}}})
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(asked) != fmt.Sprint(tc.want) {
			t.Errorf("%s: requests %q, want %q", tc.name, asked, tc.want)
		}
		r := got["a"].Ranked
		if len(r) != 1 || r[0].Release != "Kimi.no.Na.wa.2016.2160p.WEB-DL-GRP" || !r[0].Accepted {
			for _, d := range r {
				t.Logf("%s: %s accepted=%v %v", tc.name, d.Release, d.Accepted, d.Rejections)
			}
			t.Errorf("%s: ranked %d decisions, want the film by its original title, accepted", tc.name, len(r))
		}
	}
}

// TestSearchReleaseErrorIsItsRejection: a multi-library search where the engine returns an error for one release:
// that release is rejected with the error in both libraries, the valid releases are decided for both, and the batch
// goes on. A cancelled search still fails. For a series, an error under one criterion leaves another criterion's
// decision for the same release.
func TestSearchReleaseErrorIsItsRejection(t *testing.T) {
	t.Parallel()
	item := func(title, hash string) string {
		return `<item><title>` + title + `</title><guid>` + hash + `</guid><size>20000000000</size><category>2000</category>` +
			`<torznab:attr name="infohash" value="` + hash + `"/><torznab:attr name="seeders" value="10"/></item>`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("t") == "caps" {
			fmt.Fprint(w, `<caps><limits default="100" max="100"/><searching><movie-search available="yes" supportedParams="imdbid"/></searching><categories><category id="2000" name="Movies"/></categories></caps>`)
			return
		}
		fmt.Fprint(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`+
			item("Arrival.2016.2160p.WEB-DL-BAD", strings.Repeat("b", 40))+
			item("Arrival.2016.2160p.WEB-DL-GRP", strings.Repeat("a", 40))+`</channel></rss>`)
	}))
	defer srv.Close()
	profile := &policy.Profile{Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}
	version := &policy.VersionPolicy{Kind: policy.KindMovie, Profile: "web", Indexers: []string{"one"}}
	pol := &policy.Policy{Profiles: map[string]*policy.Profile{"web": profile},
		Versions: map[string]*policy.VersionPolicy{"web-a": version, "web-b": version},
		Indexers: map[string]*policy.Indexer{"one": {Kind: "torznab", URL: srv.URL + "/api", Privacy: policy.PrivacyPublic}}}
	eng, err := decide.New(pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := indexer.New(pol, &searchGateway{}, indexer.Options{CacheTTL: -1})
	if err != nil {
		t.Fatal(err)
	}
	bad := func(ctx context.Context, rel decide.Release, tt decide.Title, v decide.Version, x *decide.ExistingFile) (*decide.Decision, error) {
		if strings.HasSuffix(rel.Title, "-BAD") {
			return nil, errors.New("adapt: aggregate languages: Remove(9, 4) on 10 characters")
		}
		return eng.Decide(ctx, rel, tt, v, x)
	}
	title := decide.Title{ID: 1, Kind: policy.KindMovie, Name: "Arrival", Year: 2016, IMDb: "tt2543164"}
	targets := []SearchTarget{
		{ID: "a", Version: decide.Version{ID: 1, Key: "web-a", Policy: version}},
		{ID: "b", Version: decide.Version{ID: 2, Key: "web-b", Policy: version}},
	}
	got, err := searchRequests(context.Background(), eng, bad, eng.DecideSearch, idx, title, targets)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		r := got[id].Ranked
		if len(r) != 2 || r[0].Release != "Arrival.2016.2160p.WEB-DL-GRP" || !r[0].Accepted ||
			r[1].Release != "Arrival.2016.2160p.WEB-DL-BAD" || r[1].Accepted || len(r[1].Rejections) != 1 ||
			!strings.Contains(r[1].Rejections[0].Message, "aggregate languages") {
			for _, d := range r {
				t.Logf("%s: %s accepted=%v %v", id, d.Release, d.Accepted, d.Rejections)
			}
			t.Errorf("target %s: ranked %d", id, len(r))
		}
		if len(got[id].Request.Candidates) != 1 {
			t.Errorf("target %s: %d route candidates", id, len(got[id].Request.Candidates))
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancelling := func(c context.Context, rel decide.Release, tt decide.Title, v decide.Version, x *decide.ExistingFile) (*decide.Decision, error) {
		cancel()
		return nil, c.Err()
	}
	if _, err := searchRequests(ctx, eng, cancelling, eng.DecideSearch, idx, title, targets); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled search: %v", err)
	}

	// Series criteria: the first criterion errors, the second decides; with every criterion erroring the release is
	// rejected with the error.
	c1, c2 := &decide.SeriesSearch{Season: 1}, &decide.SeriesSearch{Season: 1, Episode: 1}
	ok := &decide.Decision{Release: "Show.S01E01", Accepted: true}
	fn := func(_ context.Context, _ decide.Release, _ decide.Title, _ decide.Version, s *decide.SeriesSearch) (*decide.Decision, error) {
		if s == c1 {
			return nil, errors.New("boom")
		}
		return ok, nil
	}
	rel := indexer.Release{Title: "Show.S01E01", Indexer: "one"}
	if d, err := decideBest(context.Background(), fn, rel, title, targets[0].Version, []*decide.SeriesSearch{c1, c2}, nil); err != nil || d != ok {
		t.Errorf("criteria: %+v %v", d, err)
	}
	d, err := decideBest(context.Background(), fn, rel, title, targets[0].Version, []*decide.SeriesSearch{c1}, nil)
	if err != nil || d.Accepted || len(d.Rejections) != 1 || d.Release != "Show.S01E01" || d.Version != "web-a" {
		t.Errorf("only an erroring criterion: %+v %v", d, err)
	}
}
