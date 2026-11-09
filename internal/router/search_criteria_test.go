package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/indexer/cardigann"
	"github.com/theatarr/theatarr/internal/policy"
)

// A native Cardigann indexer is a keyword search, like Prowlarr's Cardigann behind a Torznab feed, but
// only the torznab kind is asked Sonarr's criteria (air dates, absolute numbers, scene titles). A cardigann source
// gets the plain TVDB query, which the adapter turns into "<name> S2024E40" or "<name> S02E01": a daily show or a
// scene-numbered anime episode finds nothing on the native indexer that found it through the Prowlarr feed it
// replaces. The answer still counts as a complete search (coverage), so nothing says it was a bad query.
func TestCardigannGetsSonarrCriteria(t *testing.T) {
	old := searchNow
	searchNow = func() time.Time { return searchClock }
	defer func() { searchNow = old }()
	aired := searchClock.Add(-48 * time.Hour)
	cases := []struct {
		name string
		t    *decide.Title
		ss   *decide.SeriesSearch
		ok   func(q indexer.Query) bool
	}{
		{"daily", &decide.Title{Kind: policy.KindSeries, Name: "The Daily Show", SeriesType: "daily", Episodes: []decide.Episode{
			{ID: 1, Season: 2024, Number: 40, AirDate: "2024-03-05"}}},
			&decide.SeriesSearch{Episodes: []int64{1}, Season: 2024, Episode: 40},
			func(q indexer.Query) bool { return q.AirDate == "2024-03-05" }},
		{"anime scene absolute", &decide.Title{Kind: policy.KindSeries, Name: "Frieren", SeriesType: "anime",
			SceneMappings: []decide.SceneMapping{{Title: "Sousou no Frieren", SceneOrigin: "anidb"}}, Episodes: []decide.Episode{
				{ID: 2, Season: 2, Number: 1, Absolute: 29, SceneAbsolute: ip(1), SceneSeason: ip(2), SceneNumber: ip(1), AirDateUTC: &aired}}},
			&decide.SeriesSearch{Episodes: []int64{2}, Season: 2, Episode: 1},
			func(q indexer.Query) bool { return q.Absolute == 1 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := indexer.Query{Type: indexer.QuerySeries, Title: c.t.Name, Season: c.ss.Season, Episode: c.ss.Episode,
				Anime: c.t.SeriesType == "anime"}
			plan := planSeries(c.t, c.ss, q)
			if len(plan.forKind("torznab")) == 0 || !c.ok(plan.forKind("torznab")[0].query) {
				t.Fatalf("setup: the torznab plan lacks the criteria: %v", describe(plan.forKind("torznab")))
			}
			for _, sq := range plan.forKind("cardigann") {
				if c.ok(sq.query) {
					return
				}
			}
			t.Fatalf("a cardigann indexer is asked only %v (keywords %q S%02dE%02d), never the criteria the torznab feed gets (%v)",
				describe(plan.forKind("cardigann")), c.t.Name, c.ss.Season, c.ss.Episode, describe(plan.forKind("torznab")))
		})
	}
}

func TestKeywordPlainQueryCannotProveSeasonSearched(t *testing.T) {
	t.Parallel()
	title := &decide.Title{Kind: policy.KindSeries, Name: "Daily Show", SeriesType: "daily"}
	plan := planSeries(title, &decide.SeriesSearch{Season: 2024}, indexer.Query{Type: indexer.QuerySeries, Title: title.Name, Season: 2024})
	for _, sq := range plan.forKind("cardigann") {
		if sq.complete {
			t.Fatal("plain keyword query marked season searched without daily episode metadata")
		}
	}
}

// A page that fails after the first makes the answer partial (Incomplete); our own page_cap stopping the paging does
// not (DMM's 200 cap made every search of a popular title a failure), though the coverage stays
// incomplete for the ceiling.
func TestSearchPartialAnswerIsIncomplete(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		pageCap    int
		failPage2  bool
		incomplete bool
	}{
		{name: "page_cap", pageCap: 1, incomplete: false},
		{name: "page 2 fails", failPage2: true, incomplete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("t") == "caps" {
					fmt.Fprint(w, `<caps><limits default="100" max="100"/><searching><movie-search available="yes" supportedParams="imdbid"/></searching><categories><category id="2000" name="Movies"/></categories></caps>`)
					return
				}
				if tc.failPage2 && r.URL.Query().Get("offset") != "" {
					http.Error(w, "boom", http.StatusInternalServerError)
					return
				}
				fmt.Fprint(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><torznab:response offset="0" total="2"/><item><title>Arrival.2016.2160p.WEB-DL-GRP</title><guid>arrival</guid><size>20000000000</size><category>2000</category><torznab:attr name="infohash" value="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"/></item></channel></rss>`)
			}))
			defer srv.Close()
			pol := &policy.Policy{Profiles: map[string]*policy.Profile{"web": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}}, Versions: map[string]*policy.VersionPolicy{"web": {Kind: policy.KindMovie, Profile: "web", Indexers: []string{"one"}}}, Indexers: map[string]*policy.Indexer{"one": {Kind: "torznab", URL: srv.URL, Privacy: policy.PrivacyPublic, PageCap: tc.pageCap}}}
			eng, err := decide.New(pol, decide.Options{})
			if err != nil {
				t.Fatal(err)
			}
			svc, err := indexer.New(pol, &searchGateway{}, indexer.Options{CacheTTL: -1})
			if err != nil {
				t.Fatal(err)
			}
			got, err := SearchRequest(context.Background(), eng, svc, decide.Title{ID: 1, Kind: policy.KindMovie, Name: "Arrival", Year: 2016, IMDb: "tt2543164"}, decide.Version{ID: 1, Key: "web", Policy: pol.Versions["web"]}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.NoAnswer || got.Incomplete != tc.incomplete || len(got.Ranked) != 1 {
				t.Fatalf("answer: incomplete %v (want %v), no answer %v, ranked %d, notes %v", got.Incomplete,
					tc.incomplete, got.NoAnswer, len(got.Ranked), got.Notes)
			}
			if len(got.Coverage) != 1 || got.Coverage[0].Complete {
				t.Fatalf("a truncated answer reported complete coverage: %+v", got.Coverage)
			}
		})
	}
}

type keywordSearchDefinition struct{}

func (keywordSearchDefinition) Load(context.Context, string) (*cardigann.Definition, string, error) {
	def, err := cardigann.ParseDefinition([]byte(`id: empty
name: Empty
type: public
links: [https://site.example/]
caps:
  categorymappings: [{id: 1, cat: TV}]
search:
  paths: [{path: search}]
  inputs: {q: "{{ .Keywords }}"}
  rows: {selector: tr.result}
  fields:
    title: {selector: a}
    download: {selector: a, attribute: href}
`))
	return def, "empty", err
}

func TestPlainKeywordAnswerDoesNotCountAsSeasonSearch(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html></html>") }))
	defer srv.Close()
	pol := &policy.Policy{Profiles: map[string]*policy.Profile{"web": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}}, Versions: map[string]*policy.VersionPolicy{"tv": {Kind: policy.KindSeries, Profile: "web", Indexers: []string{"one"}}}, Indexers: map[string]*policy.Indexer{"one": {Kind: "cardigann", Definition: "empty", URL: srv.URL, Privacy: policy.PrivacyPublic}}}
	eng, err := decide.New(pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	mgr := cardigann.NewManager(keywordSearchDefinition{}, nil)
	svc, err := indexer.New(pol, &searchGateway{}, indexer.Options{CacheTTL: -1, Cardigann: mgr})
	if err != nil {
		t.Fatal(err)
	}
	got, err := SearchRequest(context.Background(), eng, svc, decide.Title{ID: 1, Kind: policy.KindSeries, Name: "Daily", SeriesType: "daily"}, decide.Version{ID: 1, Key: "tv", Policy: pol.Versions["tv"]}, nil, &decide.SeriesSearch{Season: 2024})
	if err != nil {
		t.Fatal(err)
	}
	if got.NoAnswer || !got.Incomplete {
		t.Fatalf("plain keyword answer counted as searched: %+v", got)
	}
	for _, c := range got.Coverage {
		if c.Complete {
			t.Fatalf("plain keyword answer has complete coverage: %v", got.Coverage)
		}
	}
}
