package profile

// Ports of the Sonarr episode-mapping and season-pack specifications @ cab419a (src/NzbDrone.Core/DecisionEngine/
// Specifications): FullSeason, MultiSeason, SplitEpisode, SameEpisodesGrab (with SameEpisodesSpecification),
// SceneMapping, SeasonPackOnly, RssSync/MonitoredEpisode, and Search/{EpisodeRequested, SeasonMatch,
// SingleEpisodeSearchMatch}. Search/SeriesSpecification is the caller's: a decision is always for one series.

import (
	"time"
)

// Reasons of the series specifications and of the mapping (DownloadRejectionReason names).
const (
	ReasonUnknownSeries               Reason = "UnknownSeries"
	ReasonUnknownEpisode              Reason = "UnknownEpisode"
	ReasonMatchesAnotherSeries        Reason = "MatchesAnotherSeries"
	ReasonWrongSeries                 Reason = "WrongSeries"
	ReasonWrongSeason                 Reason = "WrongSeason"
	ReasonWrongEpisode                Reason = "WrongEpisode"
	ReasonFullSeason                  Reason = "FullSeason"
	ReasonFullSeasonNotAired          Reason = "FullSeasonNotAired"
	ReasonMultiSeason                 Reason = "MultiSeason"
	ReasonSplitEpisode                Reason = "SplitEpisode"
	ReasonExistingFileHasMoreEpisodes Reason = "ExistingFileHasMoreEpisodes"
	ReasonAmbiguousNumbering          Reason = "AmbiguousNumbering"
	ReasonNotSeasonPack               Reason = "NotSeasonPack"
	ReasonSeriesNotMonitored          Reason = "SeriesNotMonitored"
	ReasonEpisodeNotMonitored         Reason = "EpisodeNotMonitored"
)

// SearchKind is the SearchCriteriaBase subclass a search used.
type SearchKind string

const (
	SearchSingleEpisode SearchKind = "episode"       // SingleEpisodeSearchCriteria
	SearchSeason        SearchKind = "season"        // SeasonSearchCriteria
	SearchAnimeEpisode  SearchKind = "anime-episode" // AnimeEpisodeSearchCriteria, IsSeasonSearch false
	SearchAnimeSeason   SearchKind = "anime-season"  // AnimeEpisodeSearchCriteria with IsSeasonSearch, AnimeSeasonSearchCriteria
	SearchDaily         SearchKind = "daily"         // DailyEpisodeSearch
	SearchDailySeason   SearchKind = "daily-season"  // DailySeasonSearchCriteria (no specification reads its numbers)
	SearchSpecial       SearchKind = "special"       // SpecialEpisodeSearchCriteria
)

// Search is the SearchCriteriaBase the release was found with; nil for RSS.
type Search struct {
	Kind          SearchKind
	SeasonNumber  int
	EpisodeNumber int
	// Episodes are the requested episodes (SearchCriteriaBase.Episodes), with ids and air dates.
	Episodes []*Episode
	// MonitoredEpisodesOnly and Interactive are the criteria's flags.
	MonitoredEpisodesOnly bool
	Interactive           bool
}

// SceneMapping is the scene mapping the release title was mapped through (RemoteEpisode.SceneMapping).
type SceneMapping struct {
	SceneOrigin string
	Comment     string
}

// The fields below extend the subject for the series specifications.

// seasonPackOnly is SeasonPackOnlySpecification.
func seasonPackOnly(e *Engine, s *Subject) Decision {
	sc := s.Search
	if sc == nil || len(sc.Episodes) == 1 {
		return Accept()
	}
	age := s.Release.SeasonSearchMaximumSingleEpisodeAge
	if age > 0 && s.Media != nil && s.Media.SeriesType == "standard" && !s.Release.FullSeason && len(s.Episodes) >= 1 {
		season := s.Episodes[0].SeasonNumber
		var last *time.Time
		for _, ep := range sc.Episodes {
			if ep.AirDateUtc != nil && ep.SeasonNumber == season && (last == nil || ep.AirDateUtc.After(*last)) {
				last = ep.AirDateUtc
			}
		}
		if last != nil && last.Before(e.now().Add(-time.Duration(age)*24*time.Hour)) {
			return Reject(ReasonNotSeasonPack, "Last episode in this season aired more than {0} days ago, season pack required.", age)
		}
	}
	return Accept()
}

// fullSeason is FullSeasonSpecification: a season pack waits until every episode has aired (a day's grace).
func fullSeason(e *Engine, s *Subject) Decision {
	if s.Release.FullSeason {
		limit := e.now().Add(24 * time.Hour)
		for _, ep := range s.Episodes {
			if ep.AirDateUtc == nil || ep.AirDateUtc.After(limit) {
				return Reject(ReasonFullSeasonNotAired, "Full season release rejected. All episodes haven't aired yet.")
			}
		}
	}
	return Accept()
}

// multiSeason is MultiSeasonSpecification.
func multiSeason(e *Engine, s *Subject) Decision {
	if s.Release.MultiSeason {
		return Reject(ReasonMultiSeason, "Multi-season releases are not supported")
	}
	return Accept()
}

// splitEpisode is SplitEpisodeSpecification.
func splitEpisode(e *Engine, s *Subject) Decision {
	if s.Release.SplitEpisode {
		return Reject(ReasonSplitEpisode, "Split episode releases are not supported")
	}
	return Accept()
}

// sameEpisodesGrab is SameEpisodesGrabSpecification over SameEpisodesSpecification: an existing file must hold no
// episode the release does not.
func sameEpisodesGrab(e *Engine, s *Subject) Decision {
	ids := map[int64]bool{}
	for _, ep := range s.Episodes {
		ids[ep.ID] = true
	}
	for _, ep := range s.Episodes {
		if ep.File == nil {
			continue
		}
		for _, id := range ep.File.EpisodeIDs {
			if !ids[id] {
				return Reject(ReasonExistingFileHasMoreEpisodes, "Episode file on disk contains more episodes than this release contains")
			}
		}
	}
	return Accept()
}

// sceneMapping is SceneMappingSpecification: a mapping of mixed origin is ambiguous.
func sceneMapping(e *Engine, s *Subject) Decision {
	m := s.SceneMapping
	if m == nil || m.SceneOrigin == "" || isBlank(m.SceneOrigin) {
		return Accept()
	}
	origin := m.SceneOrigin
	for i := 0; i < len(origin); i++ {
		if origin[i] == ':' {
			origin = origin[:i]
			break
		}
	}
	if origin == "mixed" {
		if !isBlank(m.Comment) {
			// The arr's message keeps its placeholder: Reject is called without arguments.
			return Reject(ReasonAmbiguousNumbering, "{0} has ambiguous numbering")
		}
		return Reject(ReasonAmbiguousNumbering, "Ambiguous numbering")
	}
	return Accept()
}

// monitoredEpisode is RssSync/MonitoredEpisodeSpecification.
func monitoredEpisode(e *Engine, s *Subject) Decision {
	if s.Search != nil && !s.Search.MonitoredEpisodesOnly {
		return Accept()
	}
	if s.Media != nil && !s.Media.Monitored {
		return Reject(ReasonSeriesNotMonitored, "Series is not monitored")
	}
	monitored := 0
	for _, ep := range s.Episodes {
		if ep.Monitored {
			monitored++
		}
	}
	if monitored == len(s.Episodes) {
		return Accept()
	}
	if len(s.Episodes) == 1 {
		return Reject(ReasonEpisodeNotMonitored, "Episode is not monitored")
	}
	return Reject(ReasonEpisodeNotMonitored, "One or more episodes is not monitored")
}

// episodeRequested is Search/EpisodeRequestedSpecification.
func episodeRequested(e *Engine, s *Subject) Decision {
	if s.Search == nil {
		return Accept()
	}
	want := map[int64]bool{}
	for _, ep := range s.Search.Episodes {
		want[ep.ID] = true
	}
	for _, ep := range s.Episodes {
		if want[ep.ID] {
			return Accept()
		}
	}
	if len(s.Episodes) == 0 {
		return Reject(ReasonWrongEpisode, "Episode wasn't requested")
	}
	eps := append([]*Episode(nil), s.Episodes...)
	sortEpisodes(eps)
	first, last := eps[0], eps[len(eps)-1]
	if len(eps) > 1 {
		return Reject(ReasonWrongEpisode, "Episode wasn't requested: {0}x{1}-{2}", first.SeasonNumber, first.EpisodeNumber,
			last.EpisodeNumber)
	}
	return Reject(ReasonWrongEpisode, "Episode wasn't requested: {0}x{1}", first.SeasonNumber, first.EpisodeNumber)
}

// seasonMatch is Search/SeasonMatchSpecification.
func seasonMatch(e *Engine, s *Subject) Decision {
	if s.Search == nil || s.Search.Kind != SearchSeason {
		return Accept()
	}
	if !s.Release.seasonMatches(s.Search.SeasonNumber) {
		return Reject(ReasonWrongSeason, "Wrong season")
	}
	return Accept()
}

// seasonMatches is the season test of SeasonMatch and SingleEpisodeSearchMatch: v4 compares the parsed season; v5
// looks for the season in SeasonNumbers, else compares the mapped season.
func (r *Release) seasonMatches(season int) bool {
	if !r.V5 {
		return season == r.SeasonNumber
	}
	if len(r.SeasonNumbers) > 0 {
		for _, n := range r.SeasonNumbers {
			if n == season {
				return true
			}
		}
		return false
	}
	return r.MappedSeasonNumber != nil && *r.MappedSeasonNumber == season
}

// singleEpisodeSearchMatch is Search/SingleEpisodeSearchMatchSpecification.
func singleEpisodeSearchMatch(e *Engine, s *Subject) Decision {
	sc := s.Search
	if sc == nil {
		return Accept()
	}
	switch sc.Kind {
	case SearchSingleEpisode:
		if !s.Release.seasonMatches(sc.SeasonNumber) {
			return Reject(ReasonWrongSeason, "Wrong season")
		}
		if len(s.Release.EpisodeNumbers) == 0 {
			return Reject(ReasonFullSeason, "Full season pack")
		}
		for _, n := range s.Release.EpisodeNumbers {
			if n == sc.EpisodeNumber {
				return Accept()
			}
		}
		return Reject(ReasonWrongEpisode, "Wrong episode")
	case SearchAnimeEpisode:
		if s.Release.FullSeason {
			return Reject(ReasonFullSeason, "Full season pack")
		}
	}
	return Accept()
}

func sortEpisodes(eps []*Episode) {
	for i := 1; i < len(eps); i++ {
		for j := i; j > 0 && (eps[j].SeasonNumber < eps[j-1].SeasonNumber ||
			eps[j].SeasonNumber == eps[j-1].SeasonNumber && eps[j].EpisodeNumber < eps[j-1].EpisodeNumber); j-- {
			eps[j], eps[j-1] = eps[j-1], eps[j]
		}
	}
}

func isBlank(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}
