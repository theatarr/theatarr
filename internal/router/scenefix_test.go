package router

import (
	"context"
	"crypto/sha1"
	"encoding/json"
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
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/policy"
)

// sceneSources is a Prowlarr feed answering by the season and episode asked (key "season/ep", "season/" for a
// season search) and a Zilean answering every query; both record what they were asked.
type sceneSources struct {
	mu     sync.Mutex
	feed   map[string][]string
	zilean []string
	reqs   []string
}

func (s *sceneSources) handler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path == "/zilean/dmm/filtered" {
		s.reqs = append(s.reqs, "zilean "+q.Get("Season")+"/"+q.Get("Episode"))
		var list []map[string]any
		for i, title := range s.zilean {
			list = append(list, map[string]any{"raw_title": title, "info_hash": fmt.Sprintf("%040x", 0xa0+i),
				"size": 2_000_000_000, "category": "tvSeries"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	if q.Get("t") == "caps" {
		fmt.Fprint(w, `<caps><limits default="100" max="100"/><searching><search available="yes" supportedParams="q"/>`+
			`<tv-search available="yes" supportedParams="q,season,ep,tvdbid,imdbid"/></searching></caps>`)
		return
	}
	key := q.Get("season") + "/" + q.Get("ep")
	if q.Get("tvdbid") != "" {
		s.reqs = append(s.reqs, "feed "+key)
	}
	var b strings.Builder
	b.WriteString(`<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`)
	if q.Get("tvdbid") != "" {
		for _, title := range s.feed[key] {
			fmt.Fprintf(&b, `<item><title>%s</title><guid>%s</guid><size>2000000000</size>`+
				`<torznab:attr name="infohash" value="%x"/><torznab:attr name="seeders" value="50"/></item>`,
				title, title, sha1.Sum([]byte(title)))
		}
	}
	b.WriteString(`</channel></rss>`)
	fmt.Fprint(w, b.String())
}

// sceneSearch runs one shared search over the feed and Zilean for a title and returns the decisions by release title.
func sceneSearch(t *testing.T, src *sceneSources, title decide.Title, ss *decide.SeriesSearch) map[string]*decide.Decision {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(src.handler))
	defer srv.Close()
	vp := &policy.VersionPolicy{Kind: policy.KindSeries, Profile: "web", Indexers: []string{"prowlarr", "zilean"}}
	pol := &policy.Policy{Profiles: map[string]*policy.Profile{"web": {Qualities: []policy.QualityItem{{Name: "WEBDL-1080p"}},
		Cutoff: "WEBDL-1080p"}}, Versions: map[string]*policy.VersionPolicy{"a": vp},
		Indexers: map[string]*policy.Indexer{
			"prowlarr": {Kind: "torznab", URL: srv.URL + "/api", Privacy: policy.PrivacyPublic},
			"zilean":   {Kind: "zilean", URL: srv.URL + "/zilean", Privacy: policy.PrivacyPublic},
		}}
	eng, err := decide.New(pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := indexer.New(pol, &searchGateway{}, indexer.Options{CacheTTL: -1})
	if err != nil {
		t.Fatal(err)
	}
	got, err := SearchRequests(context.Background(), eng, idx, title,
		[]SearchTarget{{ID: "a", Version: decide.Version{ID: 1, Key: "a", Policy: vp}, Series: ss}})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*decide.Decision{}
	for d, rel := range got["a"].Releases {
		out[rel.Indexer+" "+rel.Title] = d
	}
	return out
}

func checkAccepted(t *testing.T, ds map[string]*decide.Decision, want ...string) {
	t.Helper()
	var got []string
	for k, d := range ds {
		if d.Accepted {
			got = append(got, k)
		} else {
			t.Logf("rejected %s: %v", k, d.Rejections)
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("accepted %q, want %q", got, want)
	}
}

var sceneAired = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// sceneShow is a standard (or anime) series whose TVDB season 1 is two scene seasons: episodes 1-2 are scene
// S01E01-02, episodes 3-4 scene S02E05-06.
func sceneShow(seriesType string) decide.Title {
	t := decide.Title{ID: 1, Kind: policy.KindSeries, Name: "Scene Show", TVDB: 4242, IMDb: "tt4242424", SeriesType: seriesType,
		UseSceneNumbering: true, Monitored: true, Anime: seriesType == "anime"}
	for i, se := range [][2]int{{1, 1}, {1, 2}, {2, 5}, {2, 6}} {
		t.Episodes = append(t.Episodes, decide.Episode{ID: int64(i + 1), Season: 1, Number: i + 1, Absolute: i + 1,
			SceneSeason: ip(se[0]), SceneNumber: ip(se[1]), Monitored: true, AirDateUTC: &sceneAired})
	}
	return t
}

// Canonical S01E03 is scene S02E05. The feed is asked S02E05, and the scene-named release found there is
// judged with that query's criteria (season 2, episode 5), not rejected as the wrong episode. Zilean is asked the TVDB
// numbers; its answers are judged in either numbering.
func TestSceneEpisodeJudgedWithItsQuery(t *testing.T) {
	for _, typ := range []string{"standard", "anime"} {
		t.Run(typ, func(t *testing.T) {
			title := sceneShow(typ)
			kind := profile.SearchSingleEpisode
			if typ == "anime" {
				kind = profile.SearchAnimeEpisode
			}
			src := &sceneSources{feed: map[string][]string{"2/5": {"Scene.Show.S02E05.1080p.WEB-DL.DDP5.1.H.264-GRP"}},
				zilean: []string{"Scene.Show.S02E05.1080p.WEB-DL.DDP5.1.H.264-ZIL", "Scene.Show.S01E03.1080p.WEB-DL.DDP5.1.H.264-TVDB"}}
			ss := &decide.SeriesSearch{Kind: kind, Episodes: []int64{3}, Season: 1, Episode: 3}
			ds := sceneSearch(t, src, title, ss)
			checkAccepted(t, ds, "prowlarr Scene.Show.S02E05.1080p.WEB-DL.DDP5.1.H.264-GRP",
				"zilean Scene.Show.S02E05.1080p.WEB-DL.DDP5.1.H.264-ZIL", "zilean Scene.Show.S01E03.1080p.WEB-DL.DDP5.1.H.264-TVDB")
			if !slices.Contains(src.reqs, "feed 2/5") || !slices.Contains(src.reqs, "zilean 1/3") {
				t.Errorf("requests %q: the feed asks scene S02E05, Zilean TVDB S01E03", src.reqs)
			}
			for k, d := range ds {
				if !slices.Equal(d.EpisodeIDs, []int64{3}) {
					t.Errorf("%s maps to %v, want episode 3", k, d.EpisodeIDs)
				}
			}
		})
	}
}

// A TVDB season spanning two scene seasons: each scene season is searched, and each pack judged against its own
// season (not rejected as WrongSeason against the first one's).
func TestSceneSeasonPacksJudgedPerSceneSeason(t *testing.T) {
	for _, typ := range []string{"standard", "anime"} {
		t.Run(typ, func(t *testing.T) {
			title := sceneShow(typ)
			kind := profile.SearchSeason
			if typ == "anime" {
				kind = profile.SearchAnimeSeason
			}
			src := &sceneSources{feed: map[string][]string{
				"1/": {"Scene.Show.S01.1080p.WEB-DL.DDP5.1.H.264-GRP"},
				"2/": {"Scene.Show.S02.1080p.WEB-DL.DDP5.1.H.264-GRP"},
			}, zilean: []string{"Scene.Show.S02.1080p.WEB-DL.DDP5.1.H.264-ZIL"}}
			ss := &decide.SeriesSearch{Kind: kind, Episodes: []int64{1, 2, 3, 4}, Season: 1}
			ds := sceneSearch(t, src, title, ss)
			checkAccepted(t, ds, "prowlarr Scene.Show.S01.1080p.WEB-DL.DDP5.1.H.264-GRP",
				"prowlarr Scene.Show.S02.1080p.WEB-DL.DDP5.1.H.264-GRP", "zilean Scene.Show.S02.1080p.WEB-DL.DDP5.1.H.264-ZIL")
			if d := ds["prowlarr Scene.Show.S02.1080p.WEB-DL.DDP5.1.H.264-GRP"]; d != nil && !slices.Equal(d.EpisodeIDs, []int64{3, 4}) {
				t.Errorf("scene season 2 pack maps to %v, want 3, 4", d.EpisodeIDs)
			}
			if !slices.Contains(src.reqs, "feed 1/") || !slices.Contains(src.reqs, "feed 2/") || !slices.Contains(src.reqs, "zilean 1/") {
				t.Errorf("requests %q", src.reqs)
			}
		})
	}
}

// Each query's criteria: the scene numbers it asked and the episodes it is for, with the target's flags.
func TestCriteriaCarryQueryNumbering(t *testing.T) {
	title := sceneShow("standard")
	crit := func(qs []seriesQuery) []string {
		var out []string
		for _, sq := range qs {
			c := sq.search
			out = append(out, fmt.Sprintf("%s s%d e%d %v mon=%v", c.Kind, c.Season, c.Episode, c.Episodes, c.MonitoredOnly))
		}
		return out
	}
	ep := &decide.SeriesSearch{Kind: profile.SearchSingleEpisode, Episodes: []int64{3}, Season: 1, Episode: 3, MonitoredOnly: true}
	qs := criteriaQueries(&title, ep, searchClock)
	if got, want := crit(qs), []string{"episode s2 e5 [3] mon=true"}; !slices.Equal(got, want) {
		t.Errorf("episode criteria %q, want %q", got, want)
	}
	if got := plainCriteria(ep, qs); len(got) != 2 || got[0] != ep || got[1] != qs[0].search {
		t.Errorf("plain criteria %v: the target's, then the scene one", got)
	}
	season := &decide.SeriesSearch{Kind: profile.SearchSeason, Episodes: []int64{1, 2, 3, 4}, Season: 1}
	qs = criteriaQueries(&title, season, searchClock)
	if got, want := crit(qs), []string{"season s1 e0 [1 2] mon=false", "season s2 e0 [3 4] mon=false"}; !slices.Equal(got, want) {
		t.Errorf("season criteria %q, want %q", got, want)
	}
	if got := plainCriteria(season, qs); len(got) != 2 || got[1].Season != 2 {
		t.Errorf("plain criteria %v: the TVDB season, then scene season 2", got)
	}

	anime := sceneShow("anime")
	aseason := &decide.SeriesSearch{Kind: profile.SearchAnimeSeason, Episodes: []int64{1, 2, 3, 4}, Season: 1}
	qs = criteriaQueries(&anime, aseason, searchClock)
	if got, want := crit(qs), []string{"anime-season s1 e0 [1 2 3 4] mon=false", "anime-season s2 e0 [1 2 3 4] mon=false"}; !slices.Equal(got, want) {
		t.Errorf("anime season criteria %q, want %q", got, want)
	}
	if got := plainCriteria(aseason, qs); len(got) != 1 {
		t.Errorf("anime criteria read no numbers; plain keeps the target's only: %v", got)
	}
}

// Sources keyed by ids get TVDB numbers; SeaDex looks up AniList entries by the TVDB season, not the scene season.
func TestPlanPlainSourcesTVDBNumbering(t *testing.T) {
	title := sceneShow("anime")
	title.AnimeMap = &animemap.Series{TVDB: 4242, Entries: []*animemap.Entry{
		{Key: "al:100", Episodes: []anime.SE{{1, 1}, {1, 2}, {1, 3}, {1, 4}}},
		{Key: "al:200", Episodes: []anime.SE{{2, 1}}},
	}}
	ss := &decide.SeriesSearch{Kind: profile.SearchAnimeEpisode, Episodes: []int64{3}, Season: 1, Episode: 3}
	q := indexer.Query{Type: indexer.QuerySeries, Title: title.Name, IMDb: title.IMDb, TVDB: title.TVDB, Season: ss.Season,
		Episode: ss.Episode, Anime: true}
	p := planSeries(&title, ss, q)
	for _, kind := range []string{"dmm", "torrentio", "zilean"} {
		qs := p.forKind(kind)
		if len(qs) != 1 || qs[0].query.Season != 1 || qs[0].query.Episode != 3 || qs[0].search != nil {
			t.Errorf("%s: %+v, want the plain query S01E03", kind, qs)
		}
	}
	sd := p.forKind("seadex")
	if len(sd) != 1 || sd[0].query.AniList != 100 || sd[0].query.Season != 1 {
		t.Errorf("seadex: %+v, want AniList 100 (TVDB season 1)", sd)
	}
	if fq := p.forKind("torznab"); len(fq) != 1 || fq[0].query.Season != 2 || fq[0].query.Episode != 5 {
		t.Errorf("torznab: %+v, want scene S02E05", fq)
	}
}

// A series without scene numbering: one criteria per query in its own numbering, and plain answers judged with the
// target's search only.
func TestCriteriaNoSceneMappingUnchanged(t *testing.T) {
	t.Parallel()
	tt := &decide.Title{Name: "Severance", TVDB: 371980, SeriesType: "standard", Episodes: []decide.Episode{
		{ID: 1, Season: 2, Number: 1}, {ID: 2, Season: 2, Number: 2}}}
	ep := &decide.SeriesSearch{Kind: profile.SearchSingleEpisode, Episodes: []int64{2}, Season: 2, Episode: 2}
	qs := criteriaQueries(tt, ep, searchClock)
	if len(qs) != 1 || qs[0].search.Season != 2 || qs[0].search.Episode != 2 || qs[0].search.Kind != profile.SearchSingleEpisode {
		t.Errorf("criteria %+v", qs[0].search)
	}
	if got := plainCriteria(ep, qs); len(got) != 1 || got[0] != ep {
		t.Errorf("plain criteria %v, want the target's only", got)
	}
	season := &decide.SeriesSearch{Kind: profile.SearchSeason, Episodes: []int64{1, 2}, Season: 2}
	if got := plainCriteria(season, criteriaQueries(tt, season, searchClock)); len(got) != 1 {
		t.Errorf("plain criteria %v, want the target's only", got)
	}
}

// A season search that names no episodes (interactive search of a season): plain answers are also judged with the
// season criteria that carry the season's episodes, so a single episode of that season counts as requested.
func TestPlainCriteriaKeepRequestedEpisodes(t *testing.T) {
	t.Parallel()
	tt := &decide.Title{Name: "Severance", TVDB: 371980, SeriesType: "standard", Episodes: []decide.Episode{
		{ID: 1, Season: 1, Number: 1}, {ID: 2, Season: 1, Number: 2}}}
	season := &decide.SeriesSearch{Kind: profile.SearchSeason, Season: 1, Interactive: true}
	got := plainCriteria(season, criteriaQueries(tt, season, searchClock))
	if len(got) != 2 || got[0] != season || !slices.Equal(got[1].Episodes, []int64{1, 2}) {
		t.Errorf("plain criteria %+v, want the target's, then season 1 with episodes 1, 2", got)
	}
}

// An interactive or Wanted season search as acquire.InteractiveMany sends it (the season's episode ids): for anime and
// daily series too, a plain source's answer for one episode of that season is requested, not rejected (plain
// answers are not judged with the anime and daily criteria).
func TestSeasonSearchAnimeDailyEpisodeRequested(t *testing.T) {
	daily := decide.Title{ID: 1, Kind: policy.KindSeries, Name: "Daily Show", TVDB: 4243, IMDb: "tt4242425", SeriesType: "daily",
		Monitored: true, Episodes: []decide.Episode{
			{ID: 1, Season: 2024, Number: 40, AirDate: "2024-03-05", Monitored: true, AirDateUTC: &sceneAired},
			{ID: 2, Season: 2024, Number: 41, AirDate: "2024-03-06", Monitored: true, AirDateUTC: &sceneAired}}}
	anime := decide.Title{ID: 1, Kind: policy.KindSeries, Name: "Anime Show", TVDB: 4244, IMDb: "tt4242426", SeriesType: "anime",
		Anime: true, Monitored: true, Episodes: []decide.Episode{
			{ID: 1, Season: 1, Number: 1, Absolute: 1, Monitored: true, AirDateUTC: &sceneAired},
			{ID: 2, Season: 1, Number: 2, Absolute: 2, Monitored: true, AirDateUTC: &sceneAired}}}
	for _, c := range []struct {
		name    string
		title   decide.Title
		ss      *decide.SeriesSearch
		release string
	}{
		{"daily", daily, &decide.SeriesSearch{Kind: profile.SearchDailySeason, Season: 2024, Episodes: []int64{1, 2}, Interactive: true},
			"Daily.Show.2024.03.06.1080p.WEB-DL.DDP5.1.H.264-ZIL"},
		{"anime", anime, &decide.SeriesSearch{Kind: profile.SearchAnimeSeason, Season: 1, Episodes: []int64{1, 2}, Interactive: true},
			"Anime.Show.S01E02.1080p.WEB-DL.DDP5.1.H.264-ZIL"},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := &sceneSources{zilean: []string{c.release}}
			ds := sceneSearch(t, src, c.title, c.ss)
			checkAccepted(t, ds, "zilean "+c.release)
		})
	}
}

// A Specials search (season 0) as acquire.InteractiveMany sends it, with the season's episodes: an answer for one
// special is requested, not rejected.
func TestSeasonSearchSpecialsEpisodeRequested(t *testing.T) {
	title := decide.Title{ID: 1, Kind: policy.KindSeries, Name: "Plain Show", TVDB: 4245, IMDb: "tt4242427", SeriesType: "standard",
		Runtime: 45, Monitored: true, Episodes: []decide.Episode{
			{ID: 1, Season: 0, Number: 1, Title: "Behind the Scenes", Monitored: true, AirDateUTC: &sceneAired},
			{ID: 2, Season: 1, Number: 1, Monitored: true, AirDateUTC: &sceneAired}}}
	rel := "Plain.Show.S00E01.1080p.WEB-DL.DDP5.1.H.264-ZIL"
	src := &sceneSources{zilean: []string{rel}}
	ds := sceneSearch(t, src, title, &decide.SeriesSearch{Kind: profile.SearchSeason, Season: 0, Episodes: []int64{1},
		Interactive: true})
	checkAccepted(t, ds, "zilean "+rel)
}

// A standard season search as acquire.InteractiveMany sends it (with the season's episode ids) asks the indexers
// exactly what the same search naming only its season asked before.
func TestSeasonSearchEpisodeIDsSameRequests(t *testing.T) {
	title := sceneShow("standard")
	reqs := func(ss *decide.SeriesSearch) []string {
		src := &sceneSources{}
		sceneSearch(t, src, title, ss)
		slices.Sort(src.reqs)
		return src.reqs
	}
	before := reqs(&decide.SeriesSearch{Kind: profile.SearchSeason, Season: 1, Interactive: true})
	after := reqs(&decide.SeriesSearch{Kind: profile.SearchSeason, Season: 1, Episodes: []int64{1, 2, 3, 4}, Interactive: true})
	if len(before) == 0 || !slices.Equal(before, after) {
		t.Errorf("requests %q, want the same as naming the season only: %q", after, before)
	}
}
