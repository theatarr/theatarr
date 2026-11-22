package profile

// Ports of the Sonarr episode-mapping and season-pack specifications @ cab419a (src/NzbDrone.Core/DecisionEngine/
// Specifications): FullSeason, MultiSeason, SplitEpisode, SameEpisodesGrab (with SameEpisodesSpecification),
// SceneMapping, SeasonPackOnly, RssSync/MonitoredEpisode, and Search/{EpisodeRequested, SeasonMatch,
// SingleEpisodeSearchMatch}. Search/SeriesSpecification is the caller's: a decision is always for one series.

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
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
	ReasonNotAiredYet                 Reason = "NotAiredYet" // ours
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
	// SearchSeries (ours) is the query without a season of a merged search for several seasons: it takes releases
	// of two or more seasons only.
	SearchSeries SearchKind = "series"
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
	if s.Release.pack() {
		limit := e.now().Add(24 * time.Hour)
		for _, ep := range s.Episodes {
			if ep.AirDateUtc == nil || ep.AirDateUtc.After(limit) {
				return Reject(ReasonFullSeasonNotAired, "Full season release rejected. All episodes haven't aired yet.")
			}
		}
	}
	return Accept()
}

// notAiredLead is how long before its air time a TV capture of an episode is taken.
const notAiredLead = 36 * time.Hour

// notAired is ours: a television capture (HDTV, SDTV, Raw-HD) of an episode cannot exist before the broadcast, so one
// whose episode airs more than notAiredLead from now is a fake and is passed over for now (a temporary rejection: a
// later sighting is decided again). Only a television source the name states counts (not the parser's HDTV fallback
// for a name without a source). Packs keep fullSeason; WEB, disc and unknown sources are never delayed, nor an
// episode without an air date, a special, an upgrade (every mapped episode has a file), or a file already on disk.
// The deciding episode is the earliest of a release of one or two episodes and the latest with an air date of a
// longer range, so a fake "E01-E10" does not ride on E01.
func notAired(e *Engine, s *Subject) Decision {
	src := sonarr.QualitySource(s.Release.Quality.Quality.Source)
	if s.OnDisk || s.Release.pack() || len(s.Episodes) == 0 || !s.Release.SourceNamed ||
		src != sonarr.SourceTelevision && src != sonarr.SourceTelevisionRaw {
		return Accept()
	}
	// The deciding episode: the earliest of one or two, the latest with an air date of more.
	rng := len(s.Episodes) > 2
	var ep *Episode
	held := true // every mapped episode has a file: an upgrade
	for _, x := range s.Episodes {
		held = held && x.File != nil
		if rng && x.AirDateUtc == nil {
			continue
		}
		later := ep != nil && (x.SeasonNumber > ep.SeasonNumber ||
			x.SeasonNumber == ep.SeasonNumber && x.EpisodeNumber > ep.EpisodeNumber)
		if ep == nil || later == rng {
			ep = x
		}
	}
	now := e.now()
	if held || ep == nil || ep.SeasonNumber == 0 || ep.AirDateUtc == nil || !ep.AirDateUtc.After(now.Add(notAiredLead)) {
		return Accept()
	}
	zone := ""
	if s.Media != nil {
		zone = s.Media.TimeZone
	}
	return Reject(ReasonNotAiredYet, fmt.Sprintf("Not aired yet: S%02dE%02d airs %s; a TV recording cannot exist before then.",
		ep.SeasonNumber, ep.EpisodeNumber, airWords(ep, zone, now.Location())))
}

// airWords is an episode's air time as the UI says it, in loc with the zone's short name: "Thu 26 Nov, 9 pm UTC"
// ("9:30 pm" off the hour). An instant at exactly midnight of the air date in the series' zone (UTC when unknown) is
// the refresh's placeholder for a listing without an airs time (tvrefresh.MidnightAir, as Calendar reads it): the day
// alone.
func airWords(ep *Episode, zone string, loc *time.Location) string {
	listing, err := time.LoadLocation(zone)
	if err != nil {
		listing = time.UTC
	}
	if d, err := time.ParseInLocation(time.DateOnly, ep.AirDay, listing); err == nil && d.Equal(*ep.AirDateUtc) {
		return d.Format("Mon 2 Jan")
	}
	t := ep.AirDateUtc.In(loc)
	clock := t.Format("3")
	if t.Minute() != 0 {
		clock = t.Format("3:04")
	}
	return t.Format("Mon 2 Jan") + ", " + clock + " " + strings.ToLower(t.Format("PM")) + " " + t.Format("MST")
}

// multiSeason is MultiSeasonSpecification, for a multi-season release whose span was not read (Release.Seasons):
// one with a proven span is decided as a pack of all its seasons.
func multiSeason(e *Engine, s *Subject) Decision {
	if s.Release.MultiSeason && len(s.Release.Seasons) == 0 {
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

// seasonMatch is Search/SeasonMatchSpecification. A search of the whole series (SearchSeries) takes a release of
// two or more seasons (a read span or the complete-series reading) that maps an episode of a searched season.
func seasonMatch(e *Engine, s *Subject) Decision {
	if s.Search != nil && s.Search.Kind == SearchSeries {
		if len(s.Release.Seasons) < 2 {
			return Reject(ReasonWrongSeason, "Holds one season")
		}
		// The mapped episodes' seasons (TVDB numbering, as the searched episodes'), not the name's scene seasons.
		for _, ep := range s.Search.Episodes {
			for _, got := range s.Episodes {
				if got.SeasonNumber == ep.SeasonNumber {
					return Accept()
				}
			}
		}
		return Reject(ReasonWrongSeason, "Wrong season")
	}
	if s.Search == nil || s.Search.Kind != SearchSeason {
		return Accept()
	}
	if !s.Release.seasonMatches(s.Search.SeasonNumber) {
		return Reject(ReasonWrongSeason, "Wrong season")
	}
	return Accept()
}

// pack says whether the release is a full-season release: a season pack, or a span of several seasons (an anime
// absolute batch that fills whole seasons is no season pack by its name).
func (r *Release) pack() bool { return r.FullSeason || len(r.Seasons) >= 2 }

// seasonMatches is the season test of SeasonMatch and SingleEpisodeSearchMatch: the parsed season, or a season of
// a pack's span.
func (r *Release) seasonMatches(season int) bool {
	return season == r.SeasonNumber || slices.Contains(r.Seasons, season)
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
	case SearchDaily:
		if len(s.Release.Seasons) > 0 {
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
