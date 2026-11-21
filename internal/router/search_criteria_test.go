package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
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

// A page that fails after the first makes the answer partial (Incomplete); our own page_cap stopping the paging does
// not (DMM's 200 cap made every search of a popular title a failure).
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
}

// mergedTitle is a standard series of three aired seasons of two episodes (ids 11, 12, 21, 22, 31, 32).
func mergedTitle() decide.Title {
	t := decide.Title{ID: 1, Kind: policy.KindSeries, Name: "Show", Year: 2020, TVDB: 555, SeriesType: "standard",
		Runtime: 45, Monitored: true, OriginalLanguage: "English"}
	aired := searchClock.Add(-400 * 24 * time.Hour)
	for s := 1; s <= 3; s++ {
		for n := 1; n <= 2; n++ {
			t.Episodes = append(t.Episodes, decide.Episode{ID: int64(10*s + n), Season: s, Number: n, Runtime: 45,
				Monitored: true, AirDate: aired.Format(time.DateOnly), AirDateUTC: &aired})
		}
	}
	return t
}

// A merged search of seasons 1 and 2 asks each season's query and one query without a season, whose answer keeps
// only packs of several seasons (the rest counted); every result is ranked together, the request is flagged and a
// pack of several seasons is for the searched seasons only. Each member gets the results that hold its episodes. A
// failed query without a season makes no member partial; a cardigann source asked the series query is not marked
// incomplete.
func TestMergedSeriesSearch(t *testing.T) {
	old := searchNow
	searchNow = func() time.Time { return searchClock }
	defer func() { searchNow = old }()
	item := func(title string, hash byte) string {
		return fmt.Sprintf(`<item><title>%s</title><guid>%c</guid><size>20000000000</size><category>5000</category>`+
			`<torznab:attr name="seeders" value="10"/><torznab:attr name="infohash" value="%s"/></item>`, title, hash,
			strings.Repeat(string(hash), 40))
	}
	for _, failWhole := range []bool{false, true} {
		var mu sync.Mutex
		var asked []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("t") == "caps" {
				fmt.Fprint(w, `<caps><limits default="100" max="100"/><searching><tv-search available="yes" supportedParams="q,tvdbid,season,ep"/></searching><categories><category id="5000" name="TV"/></categories></caps>`)
				return
			}
			if q.Get("t") != "tvsearch" {
				fmt.Fprint(w, "<html></html>") // the cardigann source's page: no rows
				return
			}
			mu.Lock()
			asked = append(asked, q.Get("tvdbid")+"/"+q.Get("season"))
			mu.Unlock()
			var items string
			switch q.Get("season") {
			case "1":
				items = item("Show.S01.2160p.WEB-DL.DDP5.1.H.265-GRP", 'a') + item("Show.S01-S03.2160p.WEB-DL.DDP5.1.H.265-GRP", 'c')
			case "2":
				items = item("Show.S02.2160p.WEB-DL.DDP5.1.H.265-GRP", 'b')
			case "":
				if failWhole {
					http.Error(w, "boom", http.StatusInternalServerError)
					return
				}
				items = item("Show.S01-S03.2160p.WEB-DL.DDP5.1.H.265-GRP", 'c') + item("Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP", 'd') +
					item("Show.S03.2160p.WEB-DL.DDP5.1.H.265-GRP", 'e')
			}
			fmt.Fprint(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`+items+`</channel></rss>`)
		}))
		pol := &policy.Policy{Profiles: map[string]*policy.Profile{"web": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}},
			Versions: map[string]*policy.VersionPolicy{"tv": {Kind: policy.KindSeries, Profile: "web", Indexers: []string{"one", "kw"}}},
			Indexers: map[string]*policy.Indexer{"one": {Kind: "torznab", URL: srv.URL, Privacy: policy.PrivacyPublic},
				"kw": {Kind: "cardigann", Definition: "empty", URL: srv.URL, Privacy: policy.PrivacyPublic}}}
		eng, err := decide.New(pol, decide.Options{Now: func() time.Time { return searchClock }})
		if err != nil {
			t.Fatal(err)
		}
		svc, err := indexer.New(pol, &searchGateway{}, indexer.Options{CacheTTL: -1, Cardigann: cardigann.NewManager(keywordSearchDefinition{}, nil)})
		if err != nil {
			t.Fatal(err)
		}
		title := mergedTitle()
		seasons := []int{1, 2}
		member := func(s int) *decide.SeriesSearch {
			return &decide.SeriesSearch{Kind: profile.SearchSeason, Season: s, Episodes: []int64{int64(10*s + 1), int64(10*s + 2)},
				MonitoredOnly: true, Seasons: seasons}
		}
		whole := &decide.SeriesSearch{Kind: profile.SearchSeries, Episodes: []int64{11, 12, 21, 22}, MonitoredOnly: true, Seasons: seasons}
		got, err := SearchRequests(context.Background(), eng, svc, title, []SearchTarget{{ID: "m", Version: decide.Version{ID: 1,
			Key: "tv", Policy: pol.Versions["tv"]}, Series: whole, Members: []*decide.SeriesSearch{member(1), member(2)}}})
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		s := got["m"]
		slices.Sort(asked)
		want := []string{"555/", "555/1", "555/2"}
		if failWhole {
			want = append([]string{"/"}, want...) // the failed id request falls back to the title, as today
		}
		if !slices.Equal(asked, want) {
			t.Fatalf("failWhole=%v: asked %v, want %v", failWhole, asked, want)
		}
		if !s.Request.Merged || len(s.Members) != 2 {
			t.Fatalf("failWhole=%v: merged %v, members %d", failWhole, s.Request.Merged, len(s.Members))
		}
		for i, m := range s.Members {
			if m.Incomplete || m.NoAnswer {
				t.Errorf("failWhole=%v: member %d partial %v unanswered %v: %v", failWhole, i, m.Incomplete, m.NoAnswer, m.Notes)
			}
		}
		if s.Incomplete {
			t.Errorf("failWhole=%v: the search is marked incomplete: %v", failWhole, s.Notes)
		}
		var order []string
		for _, c := range s.Request.Candidates {
			order = append(order, fmt.Sprintf("%s%v", c.Release.InfoHash[:1], c.Episodes))
		}
		if want := "c[11 12 21 22] a[11 12] b[21 22]"; strings.Join(order, " ") != want {
			t.Errorf("failWhole=%v: candidates %v, want %s", failWhole, order, want)
		}
		if failWhole {
			if !strings.Contains(strings.Join(s.Members[1].Notes, ";"), "one:") {
				t.Errorf("the failed query without a season is a note on every member: %v", s.Members[1].Notes)
			}
			continue
		}
		if s.Dropped != 2 || !strings.Contains(strings.Join(s.Notes, ";"), "2 results of the search without a season") {
			t.Errorf("dropped %d, notes %v", s.Dropped, s.Notes)
		}
		names := func(ds []*decide.Decision) string {
			var out []string
			for _, d := range ds {
				out = append(out, d.Release)
			}
			return strings.Join(out, ",")
		}
		if got := names(s.Members[1].Ranked); got != "Show.S01-S03.2160p.WEB-DL.DDP5.1.H.265-GRP,Show.S02.2160p.WEB-DL.DDP5.1.H.265-GRP" {
			t.Errorf("season 2's share: %s", got)
		}
	}
}
