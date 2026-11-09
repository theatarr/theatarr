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
	"github.com/theatarr/theatarr/internal/policy"
)

func ip(n int) *int { return &n }

// describe prints the queries compactly: criteria, numbers, titles, mode, coverage.
func describe(qs []seriesQuery) []string {
	var out []string
	for _, sq := range qs {
		q := sq.query
		if q.Type == indexer.QueryText {
			out = append(out, "text "+q.Title)
			continue
		}
		s := fmt.Sprintf("%s s%d e%d", q.Criteria, q.Season, q.Episode)
		if q.Absolute > 0 {
			s += fmt.Sprintf(" abs%d", q.Absolute)
		}
		if q.AirDate != "" {
			s += " " + q.AirDate
		}
		if q.AirYear > 0 {
			s += fmt.Sprintf(" y%d", q.AirYear)
		}
		s += " [" + strings.Join(q.SceneTitles, "|") + "]"
		if len(q.SeasonTitles) > 0 {
			s += " season[" + strings.Join(q.SeasonTitles, "|") + "]"
		}
		if q.Mode != indexer.SearchDefault {
			s += fmt.Sprintf(" mode%d", q.Mode)
		}
		s += fmt.Sprintf(" cover%d", sq.season)
		if sq.complete {
			s += " complete"
		}
		out = append(out, s)
	}
	return out
}

func checkQueries(t *testing.T, name string, got []seriesQuery, want ...string) {
	t.Helper()
	d := describe(got)
	if !slices.Equal(d, want) {
		t.Errorf("%s:\n got %q\nwant %q", name, d, want)
	}
	for _, sq := range got {
		q := sq.query
		if err := q.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

var searchClock = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func TestCriteriaStandardUnchanged(t *testing.T) {
	t.Parallel()
	tt := &decide.Title{Name: "Severance", TVDB: 371980, SeriesType: "standard", Episodes: []decide.Episode{
		{ID: 1, Season: 2, Number: 1}, {ID: 2, Season: 2, Number: 2}}}
	checkQueries(t, "episode", criteriaQueries(tt, &decide.SeriesSearch{Episodes: []int64{2}, Season: 2, Episode: 2}, searchClock),
		"episode s2 e2 [Severance] cover2")
	checkQueries(t, "season", criteriaQueries(tt, &decide.SeriesSearch{Episodes: []int64{1, 2}, Season: 2}, searchClock),
		"season s2 e0 [Severance] cover2 complete")
	checkQueries(t, "season without listed episodes", criteriaQueries(tt, &decide.SeriesSearch{Season: 2}, searchClock),
		"season s2 e0 [Severance] cover2 complete")
	if got := criteriaQueries(tt, &decide.SeriesSearch{}, searchClock); got != nil {
		t.Errorf("a whole-series search keeps the plain query: %v", describe(got))
	}
	if got := criteriaQueries(&decide.Title{Name: "x"}, &decide.SeriesSearch{Season: 1, Episode: 1}, searchClock); got != nil {
		t.Errorf("no episodes known keeps the plain query: %v", describe(got))
	}
}

// TVDB season 1 is two scene seasons (XEM): each half is searched by its scene numbers, and the coverage stays the
// TVDB season but cannot prove absence.
func TestCriteriaSceneNumbering(t *testing.T) {
	t.Parallel()
	tt := &decide.Title{Name: "Show", SeriesType: "standard", UseSceneNumbering: true, Episodes: []decide.Episode{
		{ID: 1, Season: 1, Number: 12},
		{ID: 2, Season: 1, Number: 13, SceneSeason: ip(2), SceneNumber: ip(1)},
		{ID: 3, Season: 1, Number: 14, SceneSeason: ip(2), SceneNumber: ip(2)},
	}}
	checkQueries(t, "episode", criteriaQueries(tt, &decide.SeriesSearch{Episodes: []int64{2}, Episode: 13, Season: 1}, searchClock),
		"episode s2 e1 [Show] cover1")
	checkQueries(t, "season", criteriaQueries(tt, &decide.SeriesSearch{Episodes: []int64{1, 2, 3}, Season: 1}, searchClock),
		"episode s1 e12 [Show] cover1", "season s2 e0 [Show] cover1")
}

// A season-specific scene name is a search term for that season only; an all-season mapping for every season; a
// tvdb-origin alias searches TVDB numbers; non-Latin names are not search terms.
func TestCriteriaSeasonSceneNames(t *testing.T) {
	t.Parallel()
	tt := &decide.Title{Name: "Show", SeriesType: "standard", SceneMappings: []decide.SceneMapping{
		{Title: "Show Second Stage", SceneSeasonNumber: ip(2), SeasonNumber: ip(2), SceneOrigin: "tvdb"},
		{Title: "Show Alt", SceneOrigin: "anidb"},
		{Title: "ショー", SceneOrigin: "tvdb"},
	}, Episodes: []decide.Episode{
		{ID: 1, Season: 1, Number: 1}, {ID: 2, Season: 1, Number: 2},
		{ID: 3, Season: 2, Number: 1}, {ID: 4, Season: 2, Number: 2},
	}}
	checkQueries(t, "season 1", criteriaQueries(tt, &decide.SeriesSearch{Episodes: []int64{1, 2}, Season: 1}, searchClock),
		"season s1 e0 [Show Alt|Show] cover1 complete")
	checkQueries(t, "season 2", criteriaQueries(tt, &decide.SeriesSearch{Episodes: []int64{3, 4}, Season: 2}, searchClock),
		"season s2 e0 [Show Second Stage] mode2 cover2",
		"season s2 e0 [Show Alt|Show] cover2 complete")
	checkQueries(t, "episode of season 2", criteriaQueries(tt, &decide.SeriesSearch{Episodes: []int64{3}, Season: 2, Episode: 1}, searchClock),
		"episode s2 e1 [Show Second Stage] mode2 cover2", "episode s2 e1 [Show Alt|Show] cover2")
}

func TestCriteriaAnime(t *testing.T) {
	aired := searchClock.Add(-48 * time.Hour)
	later := searchClock.Add(72 * time.Hour)
	tt := &decide.Title{Name: "Frieren", SeriesType: "anime", SceneMappings: []decide.SceneMapping{
		{Title: "Sousou no Frieren", SceneOrigin: "anidb"}}, Episodes: []decide.Episode{
		{ID: 1, Season: 1, Number: 28, Absolute: 28, AirDateUTC: &aired},
		{ID: 2, Season: 2, Number: 1, Absolute: 29, SceneAbsolute: ip(1), SceneSeason: ip(2), SceneNumber: ip(1), AirDateUTC: &aired},
		{ID: 3, Season: 2, Number: 2, Absolute: 30, AirDateUTC: &later},
		{ID: 4, Season: 0, Number: 1, Title: "Recap Special"},
	}}
	checkQueries(t, "episode by absolute", criteriaQueries(tt, &decide.SeriesSearch{Episodes: []int64{1}, Season: 1, Episode: 28}, searchClock),
		"anime-episode s1 e28 abs28 [Sousou no Frieren|Frieren] cover1")
	checkQueries(t, "scene absolute", criteriaQueries(tt, &decide.SeriesSearch{Episodes: []int64{2}, Season: 2, Episode: 1}, searchClock),
		"anime-episode s2 e1 abs1 [Sousou no Frieren|Frieren] cover2")
	// Season 2 is still airing: the season search, then each aired episode; the unaired one is not searched.
	checkQueries(t, "airing season", criteriaQueries(tt, &decide.SeriesSearch{Episodes: []int64{2, 3}, Season: 2}, searchClock),
		"anime-season s2 e0 [Sousou no Frieren|Frieren] cover2 complete",
		"anime-episode s2 e1 abs1 [Sousou no Frieren|Frieren] cover2")
	checkQueries(t, "aired season", criteriaQueries(tt, &decide.SeriesSearch{Episodes: []int64{1}, Season: 1, Kind: profile.SearchAnimeSeason}, searchClock),
		"anime-season s1 e0 [Sousou no Frieren|Frieren] cover1 complete")
	// A special without absolute numbers: the episode title, then its numbers.
	checkQueries(t, "special", criteriaQueries(tt, &decide.SeriesSearch{Episodes: []int64{4}, Episode: 1}, searchClock),
		"text Sousou no Frieren Recap Special", "text Frieren Recap Special",
		"episode s0 e1 [Sousou no Frieren|Frieren] cover0")
}

func TestCriteriaDaily(t *testing.T) {
	t.Parallel()
	tt := &decide.Title{Name: "The Daily Show", SeriesType: "daily", Episodes: []decide.Episode{
		{ID: 1, Season: 2024, Number: 40, AirDate: "2024-03-05"},
		{ID: 2, Season: 2024, Number: 41, AirDate: "2024-03-06"},
		{ID: 3, Season: 2025, Number: 1, AirDate: "2025-01-06"},
		{ID: 4, Season: 2025, Number: 2},
	}}
	checkQueries(t, "episode", criteriaQueries(tt, &decide.SeriesSearch{Episodes: []int64{1}, Season: 2024, Episode: 40}, searchClock),
		"daily s0 e0 2024-03-05 [The Daily Show] cover2024")
	checkQueries(t, "no air date", criteriaQueries(tt, &decide.SeriesSearch{Episodes: []int64{4}, Season: 2025, Episode: 2}, searchClock),
		"episode s2025 e2 [The Daily Show] cover2025")
	checkQueries(t, "season", criteriaQueries(tt, &decide.SeriesSearch{Episodes: []int64{1, 2, 3, 4}, Season: 2024}, searchClock),
		"daily-season s0 e0 y2024 [The Daily Show] cover2024", "daily s0 e0 2025-01-06 [The Daily Show] cover2025")
}

type tvFeed struct {
	mu   sync.Mutex
	reqs []string
}

func (f *tvFeed) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml")
	q := r.URL.Query()
	if q.Get("t") == "caps" {
		fmt.Fprint(w, `<caps><limits default="100" max="100"/><searching><search available="yes" supportedParams="q"/>`+
			`<tv-search available="yes" supportedParams="q,season,ep,tvdbid,imdbid"/></searching></caps>`)
		return
	}
	q.Del("apikey")
	q.Del("limit")
	q.Del("cat")
	f.mu.Lock()
	f.reqs = append(f.reqs, q.Encode())
	f.mu.Unlock()
	fmt.Fprint(w, `<rss version="2.0"><channel></channel></rss>`)
}

// Two versions share a scene-numbered anime episode search: the feed is asked Sonarr's anime requests once.
func TestSearchRequestsSceneCriteriaShared(t *testing.T) {
	t.Parallel()
	feed := &tvFeed{}
	srv := httptest.NewServer(http.HandlerFunc(feed.handler))
	defer srv.Close()
	version := func() *policy.VersionPolicy {
		return &policy.VersionPolicy{Kind: policy.KindSeries, Profile: "web", Indexers: []string{"prowlarr"}}
	}
	pol := &policy.Policy{Profiles: map[string]*policy.Profile{"web": {Qualities: []policy.QualityItem{{Name: "WEBDL-1080p"}},
		Cutoff: "WEBDL-1080p"}}, Versions: map[string]*policy.VersionPolicy{"a": version(), "b": version()},
		Indexers: map[string]*policy.Indexer{"prowlarr": {Kind: "torznab", URL: srv.URL + "/api", Privacy: policy.PrivacyPublic}}}
	eng, err := decide.New(pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := indexer.New(pol, &searchGateway{}, indexer.Options{CacheTTL: -1})
	if err != nil {
		t.Fatal(err)
	}
	title := decide.Title{ID: 1, Kind: policy.KindSeries, Name: "Frieren", TVDB: 424536, SeriesType: "anime",
		Episodes: []decide.Episode{{ID: 9, Season: 2, Number: 1, Absolute: 29, SceneSeason: ip(2), SceneNumber: ip(1),
			SceneAbsolute: ip(1)}}}
	ss := &decide.SeriesSearch{Kind: profile.SearchAnimeEpisode, Episodes: []int64{9}, Season: 2, Episode: 1}
	targets := []SearchTarget{
		{ID: "a", Version: decide.Version{ID: 1, Key: "a", Policy: pol.Versions["a"]}, Series: ss},
		{ID: "b", Version: decide.Version{ID: 2, Key: "b", Policy: pol.Versions["b"]}, Series: ss},
	}
	got, err := SearchRequests(context.Background(), eng, idx, title, targets)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || len(got["a"].Coverage) != 1 || got["a"].Coverage[0].Season != 2 {
		t.Fatalf("results %+v", got)
	}
	want := []string{"q=01&t=tvsearch&tvdbid=424536", "ep=1&season=2&t=tvsearch&tvdbid=424536", "q=Frieren+01&t=search",
		"ep=1&q=Frieren&season=2&t=tvsearch"}
	if !slices.Equal(feed.reqs, want) {
		t.Errorf("requests\n got %q\nwant %q", feed.reqs, want)
	}
}
