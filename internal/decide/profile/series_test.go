package profile

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
)

// The cases of Sonarr's fixtures for the series specifications (NzbDrone.Core.Test/DecisionEngineTests @ cab419a),
// written as Go tables: FullSeasonSpecificationFixture, SameEpisodesSpecificationFixture,
// SingleEpisodeAgeDownloadDecisionFixture (SeasonPackOnly), MonitoredEpisodeSpecificationFixture,
// Search/SingleEpisodeSearchMatchSpecificationTests, plus the arr's messages for EpisodeRequested and SceneMapping.

var seriesNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func sonarrEngine() *Engine {
	return &Engine{Flavour: format.Sonarr, Now: func() time.Time { return seriesNow }}
}

func run(t *testing.T, name string, s *Subject) Decision {
	t.Helper()
	e := sonarrEngine()
	sp, ok := e.Spec(name)
	if !ok {
		t.Fatalf("no spec %s", name)
	}
	return e.Run(sp, s)
}

func aired(days float64) *time.Time {
	at := seriesNow.Add(time.Duration(days * float64(24*time.Hour)))
	return &at
}

func TestFullSeasonSpecification(t *testing.T) {
	cases := []struct {
		name   string
		full   bool
		dates  []*time.Time
		accept bool
	}{
		{"not a full season", false, []*time.Time{aired(10)}, true},
		{"all episodes have aired", true, []*time.Time{aired(-10), aired(-3)}, true},
		{"all will have aired in the next 24 hours", true, []*time.Time{aired(-10), aired(0.9)}, true},
		{"one episode has not aired", true, []*time.Time{aired(-10), aired(2)}, false},
		{"an episode has no air date", true, []*time.Time{aired(-10), nil}, false},
	}
	for _, c := range cases {
		s := &Subject{Release: &Release{FullSeason: c.full}}
		for i, d := range c.dates {
			s.Episodes = append(s.Episodes, &Episode{SeasonNumber: 1, EpisodeNumber: i + 1, AirDateUtc: d})
		}
		if got := run(t, "FullSeason", s); got.Accepted != c.accept {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestSameEpisodesGrabSpecification(t *testing.T) {
	e1, e2 := &Episode{ID: 1}, &Episode{ID: 2}
	both := &File{EpisodeIDs: []int64{1, 2}}
	cases := []struct {
		name     string
		episodes []*Episode
		files    []*File
		accept   bool
	}{
		{"fewer episodes than the file", []*Episode{e1}, []*File{both}, false},
		{"more episodes than the file", []*Episode{e1, e2}, []*File{{EpisodeIDs: []int64{1}}, nil}, true},
		{"the same episodes", []*Episode{e1, e2}, []*File{both, both}, true},
		{"the same episodes as several files", []*Episode{e1, e2}, []*File{{EpisodeIDs: []int64{1}}, {EpisodeIDs: []int64{2}}}, true},
	}
	for _, c := range cases {
		s := &Subject{Release: &Release{}}
		for i, ep := range c.episodes {
			x := *ep
			x.File = c.files[i]
			s.Episodes = append(s.Episodes, &x)
		}
		got := run(t, "SameEpisodesGrab", s)
		if got.Accepted != c.accept {
			t.Errorf("%s: %+v", c.name, got)
		}
		if !got.Accepted && got.Message != "Episode file on disk contains more episodes than this release contains" {
			t.Errorf("%s: message %q", c.name, got.Message)
		}
	}
}

func TestSeasonPackOnlySpecification(t *testing.T) {
	var eps []*Episode
	for i, age := range []float64{400, 370, 340, 310} {
		eps = append(eps, &Episode{SeasonNumber: 1, EpisodeNumber: i + 1, AirDateUtc: aired(-age)})
	}
	search := &Search{Kind: SearchSeason, SeasonNumber: 1, Episodes: eps}
	media := &Media{SeriesType: "standard"}
	single := []struct {
		episode, age int
		accept       bool
	}{{1, 200, false}, {4, 200, false}, {1, 600, true}, {1, 365, true}, {4, 365, true}, {1, 0, true}}
	for _, c := range single {
		s := &Subject{Media: media, Search: search, Episodes: []*Episode{eps[c.episode-1]},
			Release: &Release{SeasonSearchMaximumSingleEpisodeAge: c.age}}
		if got := run(t, "SeasonPackOnly", s); got.Accepted != c.accept {
			t.Errorf("single episode %d, age %d: %+v", c.episode, c.age, got)
		}
	}
	for _, age := range []int{200, 600, 365, 0} {
		s := &Subject{Media: media, Search: search, Episodes: eps,
			Release: &Release{FullSeason: true, SeasonSearchMaximumSingleEpisodeAge: age}}
		if got := run(t, "SeasonPackOnly", s); !got.Accepted {
			t.Errorf("season pack, age %d: %+v", age, got)
		}
	}
	s := &Subject{Media: media, Search: search, Episodes: []*Episode{eps[0]},
		Release: &Release{SeasonSearchMaximumSingleEpisodeAge: 200}}
	if got := run(t, "SeasonPackOnly", s); got.Message != "Last episode in this season aired more than 200 days ago, season pack required." {
		t.Errorf("message %q", got.Message)
	}
}

func TestMonitoredEpisodeSpecification(t *testing.T) {
	ep := func(m bool) *Episode { return &Episode{Monitored: m} }
	cases := []struct {
		name      string
		series    bool
		episodes  []*Episode
		search    *Search
		accept    bool
		wantMsg   string
		wantRsn   Reason
		skipCheck bool
	}{
		{"monitored single", true, []*Episode{ep(true)}, nil, true, "", "", false},
		{"monitored multi", true, []*Episode{ep(true), ep(true)}, nil, true, "", "", false},
		{"series not monitored", false, []*Episode{ep(true), ep(true)}, nil, false, "Series is not monitored", ReasonSeriesNotMonitored, false},
		{"only episode not monitored", true, []*Episode{ep(false)}, nil, false, "Episode is not monitored", ReasonEpisodeNotMonitored, false},
		{"both not monitored", true, []*Episode{ep(false), ep(false)}, nil, false, "One or more episodes is not monitored", ReasonEpisodeNotMonitored, false},
		{"first not monitored", true, []*Episode{ep(false), ep(true)}, nil, false, "One or more episodes is not monitored", ReasonEpisodeNotMonitored, false},
		{"second not monitored", true, []*Episode{ep(true), ep(false)}, nil, false, "", "", true},
		{"single episode search", true, []*Episode{ep(false)}, &Search{Kind: SearchSingleEpisode}, true, "", "", false},
		{"season search, monitored", true, []*Episode{ep(true)}, &Search{Kind: SearchSeason}, true, "", "", false},
		{"season search, monitored only", true, []*Episode{ep(false)}, &Search{Kind: SearchSeason, MonitoredEpisodesOnly: true}, false, "", "", true},
		{"single search, flag false", true, []*Episode{ep(false)}, &Search{Kind: SearchSingleEpisode}, true, "", "", false},
		{"single search, flag true", true, []*Episode{ep(false)}, &Search{Kind: SearchSingleEpisode, MonitoredEpisodesOnly: true}, false, "", "", true},
	}
	for _, c := range cases {
		s := &Subject{Media: &Media{Monitored: c.series}, Episodes: c.episodes, Search: c.search, Release: &Release{}}
		got := run(t, "MonitoredEpisode", s)
		if got.Accepted != c.accept {
			t.Errorf("%s: %+v", c.name, got)
			continue
		}
		if !c.accept && !c.skipCheck && (got.Message != c.wantMsg || got.Reason != c.wantRsn) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestSingleEpisodeSearchMatchSpecification(t *testing.T) {
	std := func(season int, eps []int, searchSeason int) *Subject {
		return &Subject{Release: &Release{SeasonNumber: season, EpisodeNumbers: eps},
			Search: &Search{Kind: SearchSingleEpisode, SeasonNumber: searchSeason, EpisodeNumber: 1}}
	}
	cases := []struct {
		name   string
		s      *Subject
		accept bool
		reason Reason
	}{
		{"season does not match", std(10, []int{1}, 5), false, ReasonWrongSeason},
		{"season matches after scene mapping (parsed 10, searched scene 10)", std(10, []int{1}, 10), true, ""},
		{"season does not match after scene mapping", std(10, []int{1}, 9), false, ReasonWrongSeason},
		{"full season for a single episode search", std(5, []int{}, 5), false, ReasonFullSeason},
		{"episode does not match", std(5, []int{2}, 5), false, ReasonWrongEpisode},
		{"the episode", std(5, []int{1}, 5), true, ""},
		{"anime: full season for an episode search", &Subject{Release: &Release{FullSeason: true},
			Search: &Search{Kind: SearchAnimeEpisode}}, false, ReasonFullSeason},
		{"anime: not a full season", &Subject{Release: &Release{}, Search: &Search{Kind: SearchAnimeEpisode}}, true, ""},
		{"anime: full season for a season search", &Subject{Release: &Release{FullSeason: true},
			Search: &Search{Kind: SearchAnimeSeason}}, true, ""},
	}
	for _, c := range cases {
		got := run(t, "SingleEpisodeSearchMatch", c.s)
		if got.Accepted != c.accept || got.Reason != c.reason {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestEpisodeRequestedAndSeasonMatch(t *testing.T) {
	e1 := &Episode{ID: 1, SeasonNumber: 2, EpisodeNumber: 3}
	e2 := &Episode{ID: 2, SeasonNumber: 2, EpisodeNumber: 4}
	e9 := &Episode{ID: 9, SeasonNumber: 2, EpisodeNumber: 9}
	search := &Search{Kind: SearchSeason, SeasonNumber: 2, Episodes: []*Episode{e9}}
	got := run(t, "EpisodeRequested", &Subject{Release: &Release{}, Search: search, Episodes: []*Episode{e2, e1}})
	if got.Accepted || got.Message != "Episode wasn't requested: 2x3-4" {
		t.Errorf("multi: %+v", got)
	}
	got = run(t, "EpisodeRequested", &Subject{Release: &Release{}, Search: search, Episodes: []*Episode{e1}})
	if got.Message != "Episode wasn't requested: 2x3" {
		t.Errorf("single: %+v", got)
	}
	if got := run(t, "EpisodeRequested", &Subject{Release: &Release{}, Search: search, Episodes: []*Episode{e1, e9}}); !got.Accepted {
		t.Errorf("one requested: %+v", got)
	}
	if got := run(t, "SeasonMatch", &Subject{Release: &Release{SeasonNumber: 3}, Search: search}); got.Reason != ReasonWrongSeason {
		t.Errorf("season match: %+v", got)
	}
	if got := run(t, "SceneMapping", &Subject{Release: &Release{}, SceneMapping: &SceneMapping{SceneOrigin: "mixed"}}); got.Message != "Ambiguous numbering" {
		t.Errorf("scene mapping: %+v", got)
	}
	if got := run(t, "SceneMapping", &Subject{Release: &Release{}, SceneMapping: &SceneMapping{SceneOrigin: "unknown:tvdb"}}); !got.Accepted {
		t.Errorf("scene mapping unknown: %+v", got)
	}
	if got := run(t, "MultiSeason", &Subject{Release: &Release{MultiSeason: true}}); got.Reason != ReasonMultiSeason {
		t.Errorf("multi season: %+v", got)
	}
	if got := run(t, "SplitEpisode", &Subject{Release: &Release{SplitEpisode: true}}); got.Reason != ReasonSplitEpisode {
		t.Errorf("split: %+v", got)
	}
}
