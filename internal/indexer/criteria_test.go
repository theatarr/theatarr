package indexer

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestCleanSceneTitle(t *testing.T) {
	for in, want := range map[string]string{
		"The Daily Show with Trevor Noah": "Daily+Show+with+Trevor+Noah",
		"Marvel's Agents of S.H.I.E.L.D.": "Marvels+Agents+of+SHIELD",
		"Pokémon & Friends":               "Pokemon+and+Friends",
		"Mob Psycho 100 (2016)":           "Mob+Psycho+100+2016",
		"Theatre":                         "Theatre",
	} {
		if got := CleanSceneTitle(in); got != want {
			t.Errorf("CleanSceneTitle(%q) = %q, want %q", in, got, want)
		}
	}
	if IsEnglishTitle("進撃の巨人") || !IsEnglishTitle("Shingeki no Kyojin") {
		t.Error("IsEnglishTitle")
	}
}

func TestCriteriaQueryValidateAndKey(t *testing.T) {
	base := Query{Type: QuerySeries, TVDB: 1, Title: "Show"}
	ok := []Query{
		{Criteria: CriteriaEpisode, Season: 0, Episode: 5}, // a special keeps season 0
		{Criteria: CriteriaAnimeEpisode, Absolute: 13},
		{Criteria: CriteriaDaily, AirDate: "2024-03-05"},
		{Criteria: CriteriaDailySeason, AirYear: 2024},
	}
	for _, c := range ok {
		q := base
		q.Criteria, q.Season, q.Episode, q.Absolute, q.AirDate, q.AirYear = c.Criteria, c.Season, c.Episode, c.Absolute, c.AirDate, c.AirYear
		if err := q.Validate(); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	bad := []Query{
		{Criteria: CriteriaDaily, AirDate: "2024-3-5"},
		{Criteria: CriteriaDailySeason},
		{Criteria: CriteriaEpisode, Absolute: 3},
		{Criteria: "weekly"},
		{Absolute: 3},
	}
	for _, c := range bad {
		q := base
		q.Criteria, q.Absolute, q.AirDate, q.AirYear = c.Criteria, c.Absolute, c.AirDate, c.AirYear
		if err := q.Validate(); !errors.Is(err, ErrBadQuery) {
			t.Errorf("%+v: %v, want a bad query", c, err)
		}
	}
	if err := (&Query{Type: QueryMovie, Title: "x", Criteria: CriteriaEpisode}).Validate(); err == nil {
		t.Error("a movie query took series criteria")
	}
	a, b := base, base
	a.Criteria, b.Criteria = CriteriaSeason, CriteriaSeason
	a.SceneTitles, b.SceneTitles = []string{"Show", "Alt"}, []string{"alt", "Show"}
	if a.Key() != b.Key() {
		t.Errorf("title order or case changed the key: %s vs %s", a.Key(), b.Key())
	}
	b.SceneTitles = []string{"Show"}
	if a.Key() == b.Key() || base.Key() == a.Key() {
		t.Error("the titles and the criteria must be part of the key")
	}
}

var criteriaCaps = &caps{
	search: capsMode{available: true, params: map[string]bool{"q": true}},
	tv:     capsMode{available: true, params: map[string]bool{"q": true, "season": true, "ep": true, "tvdbid": true, "imdbid": true}},
}

func reqStrings(tiers [][]url.Values) [][]string {
	var out [][]string
	for _, tier := range tiers {
		var s []string
		for _, p := range tier {
			p.Del("cat")
			s = append(s, p.Encode())
		}
		out = append(out, s)
	}
	return out
}

func TestCriteriaRequests(t *testing.T) {
	base := Query{Type: QuerySeries, Title: "The Show", TVDB: 7, IMDb: "tt0000007", SceneTitles: []string{"The Show", "Show: Alt"}}
	cases := []struct {
		name string
		q    func(Query) Query
		caps *caps
		want string
	}{
		{"episode: ids first, titles only as a second tier", func(q Query) Query {
			q.Criteria, q.Season, q.Episode = CriteriaEpisode, 3, 4
			return q
		}, criteriaCaps, `[[ep=4&season=3&t=tvsearch&tvdbid=7] [ep=4&q=Show&season=3&t=tvsearch ep=4&q=Show+Alt&season=3&t=tvsearch]]`},
		{"season title mode: titles only", func(q Query) Query {
			q.Criteria, q.Season, q.Mode, q.SceneTitles = CriteriaSeason, 2, SearchTitle, []string{"Show S2 Name"}
			return q
		}, criteriaCaps, `[[q=Show+S2+Name&season=2&t=tvsearch] []]`},
		{"special in season 0", func(q Query) Query {
			q.Criteria, q.Season, q.Episode = CriteriaEpisode, 0, 5
			q.SceneTitles = nil
			return q
		}, criteriaCaps, `[[ep=5&season=00&t=tvsearch&tvdbid=7] [ep=5&q=Show&season=00&t=tvsearch]]`},
		{"daily by air date", func(q Query) Query {
			q.Criteria, q.AirDate, q.SceneTitles = CriteriaDaily, "2024-03-05", nil
			return q
		}, criteriaCaps, `[[ep=03%2F05&season=2024&t=tvsearch&tvdbid=7] [ep=03%2F05&q=Show&season=2024&t=tvsearch]]`},
		{"daily season by year", func(q Query) Query {
			q.Criteria, q.AirYear, q.SceneTitles = CriteriaDailySeason, 2024, nil
			return q
		}, criteriaCaps, `[[season=2024&t=tvsearch&tvdbid=7] [q=Show&season=2024&t=tvsearch]]`},
		{"anime episode by absolute number and standard format", func(q Query) Query {
			q.Criteria, q.Season, q.Episode, q.Absolute, q.Anime = CriteriaAnimeEpisode, 2, 1, 13, true
			q.SceneTitles = []string{"The Show"}
			return q
		}, criteriaCaps, `[[q=13&t=tvsearch&tvdbid=7 ep=1&season=2&t=tvsearch&tvdbid=7 q=Show+13&t=search ep=1&q=Show&season=2&t=tvsearch]]`},
		{"anime season with a season title", func(q Query) Query {
			q.Criteria, q.Season, q.Anime = CriteriaAnimeSeason, 2, true
			q.SceneTitles, q.SeasonTitles = []string{"The Show", "Show Second Stage"}, []string{"Show Second Stage"}
			return q
		}, criteriaCaps, `[[season=2&t=tvsearch&tvdbid=7 q=Show&season=2&t=tvsearch q=Show+Second+Stage&t=search]]`},
		{"title parameter preferred to q", func(q Query) Query {
			q.Criteria, q.Season, q.Mode = CriteriaSeason, 1, SearchTitle
			return q
		}, &caps{tv: capsMode{available: true, params: map[string]bool{"title": true, "q": true, "season": true}}},
			`[[season=1&t=tvsearch&title=The+Show season=1&t=tvsearch&title=Show%3A+Alt] []]`},
		{"raw engine sends raw and clean titles", func(q Query) Query {
			q.Criteria, q.Season, q.Mode, q.SceneTitles = CriteriaSeason, 1, SearchTitle, []string{"The Show"}
			return q
		}, &caps{tv: capsMode{available: true, raw: true, params: map[string]bool{"q": true, "season": true}}},
			`[[q=The+Show&season=1&t=tvsearch q=Show&season=1&t=tvsearch] []]`},
	}
	for _, c := range cases {
		q := c.q(base)
		if err := q.Validate(); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := reqStrings(criteriaRequests(q, c.caps, q.categoryList(c.caps)))
		if s := fmtSlices(got); s != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, s, c.want)
		}
	}
	// No episode caps: no episode search (Sonarr's SupportsEpisodeSearch).
	q := base
	q.Criteria, q.Season, q.Episode = CriteriaEpisode, 1, 1
	if got := criteriaRequests(q, &caps{tv: capsMode{available: true, params: map[string]bool{"tvdbid": true, "season": true}}}, ""); got != nil {
		t.Errorf("requests without ep caps: %v", got)
	}
	// Anime criteria need the q search.
	q.Criteria, q.Absolute = CriteriaAnimeEpisode, 1
	if got := criteriaRequests(q, &caps{tv: criteriaCaps.tv}, ""); got != nil {
		t.Errorf("anime requests without q search: %v", got)
	}
}

func fmtSlices(tiers [][]string) string {
	var b strings.Builder
	b.WriteString("[")
	for i, tier := range tiers {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString("[" + strings.Join(tier, " ") + "]")
	}
	b.WriteString("]")
	return b.String()
}

// The title tier is asked only when every id request found nothing; categories follow the query.
func TestTorznabCriteriaTiers(t *testing.T) {
	loadFixtures(t, "prowlarr_caps.xml")
	n := newNet(t)
	rec := &recorder{}
	idHits := true
	serveProwlarr(n, rec, "prowlarr_caps.xml", func(r *http.Request) string {
		if r.URL.Query().Has("tvdbid") && !idHits {
			return feedOf(0, 0, 0, "")
		}
		return feedOf(0, 1, 1, "")
	})
	h := newHarness(t, n, testPolicy(), false)
	q := Query{Type: QuerySeries, Title: "Show", TVDB: 7, Criteria: CriteriaEpisode, Season: 3, Episode: 4,
		SceneTitles: []string{"Show", "Show Alt"}}
	res := search(t, h, "prowlarr", q)
	if reqs := rec.all(); len(reqs) != 2 || len(res.Releases) != 1 || res.Requests != 2 {
		t.Fatalf("ids found releases: %d requests (result %d), %d releases; want caps + one", len(reqs), res.Requests, len(res.Releases))
	}
	idHits = false
	q.Episode = 5
	res = search(t, h, "prowlarr", q)
	reqs := rec.all()[2:]
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want the id search then two title searches", len(reqs))
	}
	if got := reqs[1].URL.Query(); got.Get("q") != "Show" || got.Get("season") != "3" || got.Get("ep") != "5" || got.Get("cat") != "5000" {
		t.Errorf("title request %v", got)
	}
	if reqs[2].URL.Query().Get("q") != "Show Alt" || len(res.Releases) != 1 {
		t.Errorf("second title request %v, %d releases", reqs[2].URL.Query(), len(res.Releases))
	}
}

func TestMovieIDFallsBackToTitleAndYear(t *testing.T) {
	adapter := &torznab{}
	c := &caps{movie: capsMode{available: true, params: map[string]bool{"imdbid": true, "q": true}},
		categories: map[int]bool{2000: true}}
	q := Query{Type: QueryMovie, IMDb: "tt0133093", Title: "The Matrix", Year: 1999}
	tiers, ok := adapter.requests(q, c)
	if !ok || len(tiers) != 2 || tiers[0][0].Get("imdbid") != "0133093" || tiers[1][0].Get("q") != "The Matrix 1999" ||
		tiers[1][0].Get("t") != "movie" {
		t.Fatalf("movie tiers: %v, %v", tiers, ok)
	}
}

func TestCategoryListUsesSupportedAnimeCategories(t *testing.T) {
	q := Query{Type: QuerySeries, Anime: true}
	if got := q.categoryList(&caps{categories: map[int]bool{5000: true}}); got != "5000" {
		t.Errorf("supported categories: %q", got)
	}
	if got := q.categoryList(&caps{categories: map[int]bool{}}); got != "" {
		t.Errorf("unknown categories: %q", got)
	}
}
