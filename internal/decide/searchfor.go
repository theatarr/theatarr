package decide

import (
	"slices"

	"github.com/theatarr/theatarr/internal/decide/profile"
)

// SearchFor is the search for these episodes in TVDB numbering, the target's own identity: none for an RSS grab;
// otherwise a season search when it took several episodes or a pack, else a single-episode search (anime and daily
// kinds for those series). A search asks each source its own numbering and judges each answer with that query's
// criteria (router.SearchRequests); ArrSearchFor is the arr's criteria for a replay.
func SearchFor(t *Title, episodes []int64, releaseSource, releaseType string) *SeriesSearch {
	if releaseSource == "Rss" || releaseSource == "ReleasePush" {
		return nil
	}
	sc := &SeriesSearch{Episodes: episodes, Interactive: releaseSource == "InteractiveSearch"}
	if e := firstEpisode(t, episodes); e != nil {
		sc.Season, sc.Episode = e.Season, e.Number
	}
	multi := len(episodes) > 1 || releaseType == "SeasonPack" || releaseType == "seasonPack"
	switch {
	case t.SeriesType == "anime" && multi:
		sc.Kind = profile.SearchAnimeSeason
	case t.SeriesType == "anime":
		sc.Kind = profile.SearchAnimeEpisode
	case t.SeriesType == "daily" && !multi:
		sc.Kind = profile.SearchDaily
	case t.SeriesType == "daily":
		sc.Kind = profile.SearchDailySeason
	case multi:
		sc.Kind = profile.SearchSeason
	default:
		sc.Kind = profile.SearchSingleEpisode
	}
	return sc
}

// firstEpisode is the title's episode of the first of these ids that it has, in the title's episode order.
func firstEpisode(t *Title, episodes []int64) *Episode {
	for i := range t.Episodes {
		if slices.Contains(episodes, t.Episodes[i].ID) {
			return &t.Episodes[i]
		}
	}
	return nil
}
