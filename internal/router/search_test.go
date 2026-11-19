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
	bad := &failingEngine{Engine: eng, fail: func(rel decide.Release, _ *decide.SeriesSearch) bool {
		return strings.HasSuffix(rel.Title, "-BAD")
	}}
	title := decide.Title{ID: 1, Kind: policy.KindMovie, Name: "Arrival", Year: 2016, IMDb: "tt2543164"}
	targets := []SearchTarget{
		{ID: "a", Version: decide.Version{ID: 1, Key: "web-a", Policy: version}},
		{ID: "b", Version: decide.Version{ID: 2, Key: "web-b", Policy: version}},
	}
	got, err := SearchRequests(context.Background(), bad, idx, title, targets)
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
	cancelling := &failingEngine{Engine: eng, cancel: cancel}
	if _, err := SearchRequests(ctx, cancelling, idx, title, targets); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled search: %v", err)
	}

	// A series release: an error under its first criterion leaves the second criterion's decision, even a rejection
	// with two reasons; only when every criterion errored is the error the release's rejection.
	c1, c2 := &decide.SeriesSearch{Season: 1}, &decide.SeriesSearch{Season: 1, Episode: 1}
	rejected := &decide.Decision{Release: "Show.S01E01", Rejections: []decide.Rejection{{Reason: "A"}, {Reason: "B"}}}
	fe := &failingEngine{Engine: eng, fail: func(_ decide.Release, s *decide.SeriesSearch) bool { return s == c1 },
		series: rejected}
	rel := indexer.Release{Title: "Show.S01E01", Indexer: "one"}
	if d, err := searchDecideBest(context.Background(), fe, rel, title, targets[0].Version, []*decide.SeriesSearch{c1, c2}, nil); err != nil || d != rejected {
		t.Errorf("criteria: %+v %v", d, err)
	}
	d, err := searchDecideBest(context.Background(), fe, rel, title, targets[0].Version, []*decide.SeriesSearch{c1}, nil)
	if err != nil || d.Accepted || !undecided(d) || d.Release != "Show.S01E01" || d.Version != "web-a" {
		t.Errorf("only an erroring criterion: %+v %v", d, err)
	}
}

// failingEngine is the engine, with an error for the releases fail picks (and a cancel before deciding when set);
// series, when set, is the decision of every other series release.
type failingEngine struct {
	*decide.Engine
	fail   func(decide.Release, *decide.SeriesSearch) bool
	cancel func()
	series *decide.Decision
}

func (f *failingEngine) Decide(ctx context.Context, rel decide.Release, t decide.Title, v decide.Version,
	x *decide.ExistingFile) (*decide.Decision, error) {
	if f.cancel != nil {
		f.cancel()
		return nil, ctx.Err()
	}
	if f.fail(rel, nil) {
		return nil, errors.New("adapt: aggregate languages: Remove(9, 4) on 10 characters")
	}
	return f.Engine.Decide(ctx, rel, t, v, x)
}

func (f *failingEngine) DecideSearch(ctx context.Context, rel decide.Release, t decide.Title, v decide.Version,
	s *decide.SeriesSearch) (*decide.Decision, error) {
	if f.fail(rel, s) {
		return nil, errors.New("boom")
	}
	if f.series != nil {
		return f.series, nil
	}
	return f.Engine.DecideSearch(ctx, rel, t, v, s)
}

// TestFreshRecheckKeepsEngineError: the recheck of a queued series search release keeps returning an engine error
// (only a batch search makes it the release's rejection).
func TestFreshRecheckKeepsEngineError(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.pol.Profiles = map[string]*policy.Profile{"tv": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}}
	e.pol.Versions["series"].Profile = "tv"
	e.exec(`UPDATE title SET year=2024,runtime_minutes=45 WHERE id=2`)
	e.exec(`INSERT INTO episode(id,title_id,season,number,name,air_date,air_date_utc_at,created_at,updated_at)
		VALUES(101,2,1,1,'Pilot','2024-01-01',1000,0,0)`)
	eng, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetProfiles(eng.Profile)
	failing := func(context.Context, decide.Release, decide.Title, decide.Version, *decide.SeriesSearch) (*decide.Decision, error) {
		return nil, errors.New("adapt: aggregate languages: Remove(9, 4) on 10 characters")
	}
	e.r.SetDecider(eng.Decide, failing)
	c := cand(h("a"), "Show.S01E01.2160p.WEB-DL-GRP")
	c.Release.Seeders = 10
	c.Episodes = []int64{101}
	c.Search, c.Identity, c.Origin = true, RouteIdentityVersion, OriginSearch
	c.Criteria = []*decide.SeriesSearch{{Season: 1, Episode: 1}}
	// Decided under an older policy revision: the route decides it again before adding.
	_, err = e.r.Route(e.ctx, Request{TitleID: 2, VersionID: 20, Version: "series", Source: "wanted",
		DecisionRev: "older", Candidates: []Candidate{c}})
	if err == nil || !strings.Contains(err.Error(), "aggregate languages") {
		t.Fatalf("recheck error: %v", err)
	}
}
