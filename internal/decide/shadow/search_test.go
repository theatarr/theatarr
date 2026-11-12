package shadow

import (
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
)

func ip(n int) *int { return &n }

// SearchFor keeps the target in TVDB numbering; ArrSearchFor is the arr's criteria, in scene numbering for a
// scene-numbered series (season and episode both).
func TestSearchForNumbering(t *testing.T) {
	tt := &decide.Title{SeriesType: "standard", UseSceneNumbering: true, Episodes: []decide.Episode{
		{ID: 1, Season: 1, Number: 1},
		{ID: 3, Season: 1, Number: 3, SceneSeason: ip(2), SceneNumber: ip(5)},
		{ID: 4, Season: 1, Number: 4, SceneSeason: ip(2), SceneNumber: ip(6)},
	}}
	check := func(name string, got *decide.SeriesSearch, kind profile.SearchKind, season, episode int) {
		t.Helper()
		if got == nil || got.Kind != kind || got.Season != season || got.Episode != episode {
			t.Errorf("%s: %+v, want %s s%d e%d", name, got, kind, season, episode)
		}
	}
	check("episode", decide.SearchFor(tt, []int64{3}, "Search", ""), profile.SearchSingleEpisode, 1, 3)
	check("arr episode", ArrSearchFor(tt, []int64{3}, "Search", ""), profile.SearchSingleEpisode, 2, 5)
	check("season", decide.SearchFor(tt, []int64{3, 4}, "Search", "SeasonPack"), profile.SearchSeason, 1, 3)
	check("arr season", ArrSearchFor(tt, []int64{3, 4}, "Search", "SeasonPack"), profile.SearchSeason, 2, 5)
	check("unmapped episode", ArrSearchFor(tt, []int64{1}, "Search", ""), profile.SearchSingleEpisode, 1, 1)
	if decide.SearchFor(tt, []int64{3}, "Rss", "") != nil || ArrSearchFor(tt, []int64{3}, "Rss", "") != nil {
		t.Error("an RSS grab has no search criteria")
	}
	off := *tt
	off.UseSceneNumbering = false
	check("scene numbering off", ArrSearchFor(&off, []int64{3}, "Search", ""), profile.SearchSingleEpisode, 1, 3)
	daily := &decide.Title{SeriesType: "daily", Episodes: []decide.Episode{{ID: 1, Season: 2024, Number: 1},
		{ID: 2, Season: 2024, Number: 2}}}
	check("daily season", decide.SearchFor(daily, []int64{1, 2}, "Search", ""), profile.SearchDailySeason, 2024, 1)
}
