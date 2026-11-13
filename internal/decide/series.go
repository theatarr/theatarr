package decide

// Series mapping: port of Sonarr src/NzbDrone.Core/Parser/ParsingService.cs (Map with search criteria, FindSeries,
// GetSeriesByAllTitles, GetEpisodes, GetAnimeEpisodes, GetStandardEpisodes, GetDailyEpisode,
// ParseSpecialEpisodeTitle), the lookups it makes through SeriesService, EpisodeService and EpisodeRepository, and
// DataAugmentation/Scene/SceneMappingService.cs (FindSceneMapping, FindMappings, FilterSceneMappings,
// GetSceneSeasonNumber) @ cab419a. The "library" is the one series under decision: a decision is always for one
// title, so lookups by title, id or scene mapping either find it or find nothing.

import (
	"errors"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/decide/profile"
)

// MatchAlias is SeriesMatchType.Alias: the release's series title is one of the series' scene mappings.
const MatchAlias MatchType = "Alias"

// SeriesSearch is the arr's SearchCriteriaBase for a series: which kind of search found the release, the season and
// episode it asked for, and the requested episodes (ids of Title.Episodes).
type SeriesSearch struct {
	Kind          profile.SearchKind
	Season        int
	Episode       int
	Episodes      []int64
	MonitoredOnly bool
	Interactive   bool
	// Pack keeps a season search for one episode a season query: acquisition searches a curated season pack by
	// season even when only one of its episodes is wanted.
	Pack bool
}

// errMultiple stands for the arr's SingleOrDefault throwing on several matches; the decision becomes the arr's
// "Unexpected error processing release".
var errMultiple = errors.New("sequence contains more than one matching element")

// remoteEpisode is the part of RemoteEpisode the mapping fills.
type remoteEpisode struct {
	parsed       *sonarr.ParsedEpisodeInfo
	sceneMapping *SceneMapping
	mappedSeason int
	matched      bool
	matchType    MatchType
	episodes     []*Episode
}

// series is the one series the mapping knows, with its lookups.
type series struct {
	t          *Title
	clean      string
	typ        sonarr.SeriesType
	parseTitle func(string) *sonarr.ParsedEpisodeInfo
	// known is an import's mapping: the series is given (Map(parsedEpisodeInfo, 0, 0, null, series, criteria)), so
	// FindSeries is not asked (resolve.go).
	known bool
}

func newSeries(t *Title) *series {
	typ, ok := sonarr.ParseSeriesType(t.SeriesType)
	if !ok {
		typ = sonarr.SeriesTypeStandard
	}
	return &series{t: t, clean: sonarr.CleanSeriesTitle(t.Name), typ: typ, parseTitle: sonarr.ParseTitle}
}

// findByTitle is SeriesRepository.FindByTitle(cleanTitle): the series whose clean title is title's.
func (s *series) findByTitle(title string) bool {
	return strings.ToLower(sonarr.CleanSeriesTitle(title)) == s.clean
}

// findByTitleYear is FindByTitle(title, year).
func (s *series) findByTitleYear(title string, year int) bool {
	return s.findByTitle(title) && s.t.Year == year
}

// findByTitleInexact is SeriesService.FindByTitleInexact: the series' clean title occurs in the title's.
func (s *series) findByTitleInexact(title string) bool {
	return strings.Contains(sonarr.CleanSeriesTitle(title), s.clean)
}

func (s *series) byTvdb(id int) bool { return id > 0 && id == s.t.TVDB }

func (s *series) byImdb(id string) bool { return strings.TrimSpace(id) != "" && id == s.t.IMDb }

// ---- Scene mappings (SceneMappingService) ----

// usable are the mappings FindMappings may return. The arr's mappings with a filter regex (shown as the comment
// "Limited matching") match only release titles the regex accepts; the API does not expose the regex, so they are
// left out (deviation).
func (s *series) mappingsFor(seriesTitle string) []*SceneMapping {
	clean := sonarr.CleanSeriesTitle(seriesTitle)
	var out []*SceneMapping
	for i := range s.t.SceneMappings {
		m := &s.t.SceneMappings[i]
		if m.Comment == "Limited matching" {
			continue
		}
		if sonarr.CleanSeriesTitle(m.Title) == clean {
			out = append(out, m)
		}
	}
	return out
}

// findMappings is FindMappings: the candidates by clean title; with several, the exact title (highest season
// first), else the closest by Levenshtein distance.
func (s *series) findMappings(seriesTitle string) []*SceneMapping {
	cands := s.mappingsFor(seriesTitle)
	if len(cands) <= 1 {
		return cands
	}
	var exact []*SceneMapping
	for _, m := range cands {
		if m.Title == seriesTitle {
			exact = append(exact, m)
		}
	}
	if len(exact) > 0 {
		sort.SliceStable(exact, func(i, j int) bool { return optInt(exact[i].SeasonNumber) > optInt(exact[j].SeasonNumber) })
		return exact
	}
	best := cands[0]
	bestD := levenshtein(seriesTitle, best.Title, 10, 1, 10)
	for _, m := range cands[1:] {
		d := levenshtein(seriesTitle, m.Title, 10, 1, 10)
		if d < bestD || d == bestD && optInt(m.SeasonNumber) > optInt(best.SeasonNumber) {
			best, bestD = m, d
		}
	}
	var out []*SceneMapping
	for _, m := range cands {
		if m.Title == best.Title {
			out = append(out, m)
		}
	}
	return out
}

// optInt is a nullable int with null as -1 for ordering (OrderByDescending puts null last).
func optInt(p *int) int {
	if p == nil {
		return -1 << 31
	}
	return *p
}

func orMinus1(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

// filterBySeason is FilterSceneMappings(candidates, sceneSeasonNumber).
func filterBySeason(cands []*SceneMapping, sceneSeason int) []*SceneMapping {
	var filtered, normal []*SceneMapping
	for _, m := range cands {
		if orMinus1(m.SceneSeasonNumber) != -1 && orMinus1(m.SeasonNumber) != -1 {
			filtered = append(filtered, m)
		} else {
			normal = append(normal, m)
		}
	}
	if sceneSeason == -1 {
		return normal
	}
	if len(filtered) == 0 {
		return normal
	}
	byTitle := map[string]*SceneMapping{}
	var order []string
	for _, m := range filtered {
		if *m.SceneSeasonNumber > sceneSeason {
			continue
		}
		cur, ok := byTitle[m.Title]
		if !ok {
			order = append(order, m.Title)
			byTitle[m.Title] = m
			continue
		}
		if *m.SceneSeasonNumber > *cur.SceneSeasonNumber ||
			*m.SceneSeasonNumber == *cur.SceneSeasonNumber && *m.SeasonNumber > *cur.SeasonNumber {
			byTitle[m.Title] = m
		}
	}
	out := make([]*SceneMapping, 0, len(order))
	for _, k := range order {
		out = append(out, byTitle[k])
	}
	return out
}

// findSceneMapping is FindSceneMapping. Every mapping here is of the one series, so DistinctBy(TvdbId) leaves one.
func (s *series) findSceneMapping(seriesTitle string, sceneSeason int) *SceneMapping {
	if strings.TrimSpace(seriesTitle) == "" {
		return nil
	}
	ms := s.findMappings(seriesTitle)
	if len(ms) == 0 {
		return nil
	}
	ms = filterBySeason(ms, sceneSeason)
	if len(ms) == 0 {
		return nil
	}
	return ms[0]
}

// sceneSeasonNumber is GetSceneSeasonNumber.
func (s *series) sceneSeasonNumber(seriesTitle string) *int {
	if m := s.findSceneMapping(seriesTitle, -1); m != nil {
		return m.SceneSeasonNumber
	}
	return nil
}

// ---- Episode lookups (EpisodeService, EpisodeRepository) ----

func (s *series) episodes() []*Episode {
	out := make([]*Episode, len(s.t.Episodes))
	for i := range s.t.Episodes {
		out[i] = &s.t.Episodes[i]
	}
	return out
}

// find is Find(seriesId, season, episode): SingleOrDefault.
func (s *series) find(season, number int) (*Episode, error) {
	var found *Episode
	for _, e := range s.episodes() {
		if e.Season == season && e.Number == number {
			if found != nil {
				return nil, errMultiple
			}
			found = e
		}
	}
	return found, nil
}

// findAbsolute is Find(seriesId, absoluteEpisodeNumber): SingleOrDefault.
func (s *series) findAbsolute(abs int) (*Episode, error) {
	var found *Episode
	for _, e := range s.episodes() {
		if e.Absolute != 0 && e.Absolute == abs {
			if found != nil {
				return nil, errMultiple
			}
			found = e
		}
	}
	return found, nil
}

// bySceneNumbering is FindEpisodesBySceneNumbering(seriesId, season, episode).
func (s *series) bySceneNumbering(season, number int) []*Episode {
	var out []*Episode
	for _, e := range s.episodes() {
		if e.SceneSeason != nil && e.SceneNumber != nil && *e.SceneSeason == season && *e.SceneNumber == number {
			out = append(out, e)
		}
	}
	return out
}

// bySceneAbsolute is FindEpisodesBySceneNumbering(seriesId, sceneAbsoluteEpisodeNumber).
func (s *series) bySceneAbsolute(abs int) []*Episode {
	var out []*Episode
	for _, e := range s.episodes() {
		if e.SceneAbsolute != nil && *e.SceneAbsolute == abs {
			out = append(out, e)
		}
	}
	return out
}

func (s *series) bySeason(season int) []*Episode {
	var out []*Episode
	for _, e := range s.episodes() {
		if e.Season == season {
			out = append(out, e)
		}
	}
	return out
}

func (s *series) bySceneSeason(season int) []*Episode {
	var out []*Episode
	for _, e := range s.episodes() {
		if e.SceneSeason != nil && *e.SceneSeason == season {
			out = append(out, e)
		}
	}
	return out
}

// byAirDate is EpisodeService.FindOneByAirDate.
func (s *series) byAirDate(date string, part *int) (*Episode, error) {
	var eps []*Episode
	for _, e := range s.episodes() {
		if e.AirDate == date {
			eps = append(eps, e)
		}
	}
	if len(eps) == 0 {
		return nil, nil
	}
	if len(eps) == 1 {
		return eps[0], nil
	}
	var regular []*Episode
	for _, e := range eps {
		if e.Season > 0 {
			regular = append(regular, e)
		}
	}
	if len(regular) == 1 && part == nil {
		return regular[0], nil
	}
	if part != nil && *part <= len(regular) {
		sort.SliceStable(regular, func(i, j int) bool {
			if regular[i].Season != regular[j].Season {
				return regular[i].Season < regular[j].Season
			}
			return regular[i].Number < regular[j].Number
		})
		return regular[*part-1], nil
	}
	return nil, errors.New("multiple episodes with the same air date found. Date: " + date)
}

// findByEpisodeTitle is FindEpisodeByTitle(seriesId, season, releaseTitle): the episode whose normalized title
// occurs leftmost (longest first) in the normalized release title, or in its clean form.
func (s *series) findByEpisodeTitle(season int, releaseTitle string) *Episode {
	norm := sonarr.NormalizeEpisodeTitle(releaseTitle)
	cleanNorm := sonarr.CleanSeriesTitle(norm)
	normLower, cleanLower := strings.ToLower(norm), strings.ToLower(cleanNorm)
	type cand struct {
		pos, length int
		ep          *Episode
	}
	var cs []cand
	for _, e := range s.bySeason(season) {
		if len(e.Title) == 0 {
			continue
		}
		et := sonarr.NormalizeEpisodeTitle(e.Title)
		length := len([]rune(et))
		if p := runeIndex(normLower, strings.ToLower(et)); p >= 0 {
			cs = append(cs, cand{p, length, e})
		}
		if p := runeIndex(cleanLower, strings.ToLower(sonarr.CleanSeriesTitle(et))); p >= 0 {
			cs = append(cs, cand{p, length, e})
		}
	}
	if len(cs) == 0 {
		return nil
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].pos != cs[j].pos {
			return cs[i].pos < cs[j].pos
		}
		return cs[i].length > cs[j].length
	})
	return cs[0].ep
}

func runeIndex(s, sub string) int {
	i := strings.Index(s, sub)
	if i < 0 {
		return -1
	}
	return len([]rune(s[:i]))
}

// ---- ParsingService ----

// getSeries is ParsingService.GetSeries(title) over the one series.
func (s *series) getSeries(title string) bool {
	p := s.parseTitle(title)
	if p == nil {
		return s.findByTitle(title)
	}
	if s.findSceneMapping(p.SeriesTitle, p.SeasonNumber) != nil {
		return true
	}
	if s.findByTitle(p.SeriesTitle) {
		return true
	}
	if p.SeriesTitleInfo != nil && p.SeriesTitleInfo.AllTitles != nil && s.byAllTitles(p) {
		return true
	}
	if p.SeriesTitleInfo != nil {
		return s.findByTitleYear(p.SeriesTitleInfo.TitleWithoutYear, p.SeriesTitleInfo.Year)
	}
	return false
}

// byAllTitles is GetSeriesByAllTitles: every title that resolves must resolve to the same series; with one series
// that is any title resolving to it.
func (s *series) byAllTitles(p *sonarr.ParsedEpisodeInfo) bool {
	for _, t := range p.SeriesTitleInfo.AllTitles {
		if s.findByTitle(t) || s.findSceneMapping(t, p.SeasonNumber) != nil {
			return true
		}
	}
	return false
}

// parseSpecialEpisodeTitle is ParseSpecialEpisodeTitle(parsed, releaseTitle, tvdbId, tvRageId, imdbId, criteria).
func (s *series) parseSpecialEpisodeTitle(p *sonarr.ParsedEpisodeInfo, releaseTitle string, tvdbID int, imdbID string,
	search *SeriesSearch) *sonarr.ParsedEpisodeInfo {
	if search != nil {
		if tvdbID != 0 && tvdbID == s.t.TVDB {
			return s.parseSpecialFor(p, releaseTitle)
		}
		if strings.TrimSpace(imdbID) != "" && imdbID == s.t.IMDb {
			return s.parseSpecialFor(p, releaseTitle)
		}
	}
	found := s.getSeries(releaseTitle) || s.findByTitleInexact(releaseTitle) || s.byTvdb(tvdbID) || s.byImdb(imdbID)
	if !found {
		return nil
	}
	return s.parseSpecialFor(p, releaseTitle)
}

// parseSpecialFor is ParseSpecialEpisodeTitle(parsed, releaseTitle, series).
func (s *series) parseSpecialFor(p *sonarr.ParsedEpisodeInfo, releaseTitle string) *sonarr.ParsedEpisodeInfo {
	if p != nil && p.IsPossibleSceneSeasonSpecial() && s.t.UseSceneNumbering {
		if len(s.bySceneNumbering(p.SeasonNumber, 0)) > 0 {
			return p
		}
	}
	e := s.findByEpisodeTitle(0, releaseTitle)
	if e == nil {
		return nil
	}
	info := sonarr.NewParsedEpisodeInfo()
	info.ReleaseTitle = releaseTitle
	info.SeriesTitle = s.t.Name
	info.SeriesTitleInfo = &sonarr.SeriesTitleInfo{Title: s.t.Name}
	info.SeasonNumber = e.Season
	info.EpisodeNumbers = []int{e.Number}
	info.Quality = sonarr.ParseQuality(releaseTitle)
	info.ReleaseGroup = sonarr.ParseReleaseGroup(releaseTitle)
	info.Languages = sonarr.ParseLanguages(releaseTitle)
	info.Special = true
	return info
}

// mapRelease is ParsingService.Map(parsedEpisodeInfo, tvdbId, tvRageId, imdbId, searchCriteria).
func (s *series) mapRelease(p *sonarr.ParsedEpisodeInfo, tvdbID int, imdbID string, search *SeriesSearch) (*remoteEpisode, error) {
	sm := s.findSceneMapping(p.SeriesTitle, p.SeasonNumber)
	r := &remoteEpisode{parsed: p, sceneMapping: sm, mappedSeason: p.SeasonNumber}
	sceneSource := true
	if sm != nil {
		if sm.SeasonNumber != nil && *sm.SeasonNumber >= 0 && sm.SceneSeasonNumber != nil &&
			*sm.SceneSeasonNumber <= p.SeasonNumber {
			r.mappedSeason += *sm.SeasonNumber - *sm.SceneSeasonNumber
		}
		if sm.SceneOrigin == "tvdb" {
			sceneSource = false
		}
		// The arr's XEM rule (mapping type XemService, parsed season 1) needs the mapping type, which the API does
		// not expose (deviation).
	}
	r.matched, r.matchType = s.findSeries(p, tvdbID, imdbID, sm, search)
	if r.matched && sonarr.ValidateForSeriesType(p, s.typ) {
		eps, err := s.getEpisodes(p, r.mappedSeason, sceneSource, search)
		if err != nil {
			return nil, err
		}
		r.episodes = eps
	}
	return r, nil
}

// mapKnown is mapRelease with the series given, as an import maps it (Map(parsedEpisodeInfo, 0, 0, null, series,
// criteria)).
func (s *series) mapKnown(p *sonarr.ParsedEpisodeInfo, search *SeriesSearch) (*remoteEpisode, error) {
	known := s.known
	s.known = true
	defer func() { s.known = known }()
	return s.mapRelease(p, 0, "", search)
}

// findSeries is FindSeries over the one series.
func (s *series) findSeries(p *sonarr.ParsedEpisodeInfo, tvdbID int, imdbID string, sm *SceneMapping,
	search *SeriesSearch) (bool, MatchType) {
	if s.known {
		return true, MatchID
	}
	if sm != nil {
		return true, MatchAlias
	}
	if search != nil {
		if s.clean == sonarr.CleanSeriesTitle(p.SeriesTitle) {
			return true, MatchTitle
		}
		if tvdbID > 0 && tvdbID == s.t.TVDB {
			return true, MatchID
		}
		if strings.TrimSpace(imdbID) != "" && imdbID == s.t.IMDb && tvdbID <= 0 {
			return true, MatchID
		}
	}
	if s.findByTitle(p.SeriesTitle) {
		return true, MatchTitle
	}
	if p.SeriesTitleInfo != nil && p.SeriesTitleInfo.AllTitles != nil && s.byAllTitles(p) {
		return true, MatchTitle
	}
	if p.SeriesTitleInfo != nil && p.SeriesTitleInfo.Year > 0 {
		if s.findByTitleYear(p.SeriesTitleInfo.TitleWithoutYear, p.SeriesTitleInfo.Year) {
			return true, MatchTitle
		}
	}
	if s.byTvdb(tvdbID) {
		return true, MatchID
	}
	if tvdbID <= 0 && s.byImdb(imdbID) {
		return true, MatchID
	}
	return false, MatchNone
}

func (s *series) searchEpisodes(search *SeriesSearch) []*Episode {
	if search == nil {
		return nil
	}
	want := map[int64]bool{}
	for _, id := range search.Episodes {
		want[id] = true
	}
	var out []*Episode
	for _, e := range s.episodes() {
		if want[e.ID] {
			out = append(out, e)
		}
	}
	return out
}

// getEpisodes is GetEpisodes(parsed, series, mappedSeasonNumber, sceneSource, criteria).
func (s *series) getEpisodes(p *sonarr.ParsedEpisodeInfo, mapped int, sceneSource bool, search *SeriesSearch) ([]*Episode, error) {
	if p.FullSeason {
		if s.t.UseSceneNumbering && sceneSource {
			if eps := s.bySceneSeason(mapped); len(eps) > 0 {
				return eps, nil
			}
		}
		return s.bySeason(mapped), nil
	}
	if p.IsDaily() {
		e, err := s.dailyEpisode(*p.AirDate, p.DailyPart, search)
		if err != nil || e == nil {
			return nil, err
		}
		return []*Episode{e}, nil
	}
	if p.IsAbsoluteNumbering() {
		return s.animeEpisodes(p, sceneSource)
	}
	if p.IsPossibleSceneSeasonSpecial() {
		if sp := s.parseSpecialFor(p, p.ReleaseTitle); sp != nil {
			return s.standardEpisodes(sp, sp.SeasonNumber, false, search)
		}
	}
	if p.Special && mapped != 0 {
		return nil, nil
	}
	return s.standardEpisodes(p, mapped, sceneSource, search)
}

// dailyEpisode is GetDailyEpisode.
func (s *series) dailyEpisode(airDate string, part *int, search *SeriesSearch) (*Episode, error) {
	var found *Episode
	for _, e := range s.searchEpisodes(search) {
		if e.AirDate == airDate {
			if found != nil {
				return nil, errMultiple
			}
			found = e
		}
	}
	if found != nil {
		return found, nil
	}
	return s.byAirDate(airDate, part)
}

// animeEpisodes is GetAnimeEpisodes.
func (s *series) animeEpisodes(p *sonarr.ParsedEpisodeInfo, sceneSource bool) ([]*Episode, error) {
	var out []*Episode
	sceneSeason := s.sceneSeasonNumber(p.SeriesTitle)
	for _, abs := range p.AbsoluteEpisodeNumbers {
		var eps []*Episode
		switch {
		case p.Special:
			e, err := s.find(0, abs)
			if err != nil {
				return nil, err
			}
			if e != nil {
				eps = append(eps, e)
			}
		case sceneSource:
			switch {
			case sceneSeason != nil:
				eps = s.bySceneNumbering(*sceneSeason, abs)
				if len(eps) == 0 {
					e, err := s.find(*sceneSeason, abs)
					if err != nil {
						return nil, err
					}
					if e != nil {
						eps = append(eps, e)
					}
				}
			case p.SeasonNumber > 1 && len(p.EpisodeNumbers) == 0:
				eps = s.bySceneNumbering(p.SeasonNumber, abs)
				if len(eps) == 0 {
					e, err := s.find(p.SeasonNumber, abs)
					if err != nil {
						return nil, err
					}
					if e != nil {
						eps = append(eps, e)
					}
				}
			default:
				eps = s.bySceneAbsolute(abs)
				if len(eps) > 1 { // no multiple results without a scene name mapping
					eps = nil
				}
			}
		}
		if len(eps) == 0 {
			e, err := s.findAbsolute(abs)
			if err != nil {
				return nil, err
			}
			if e != nil {
				eps = append(eps, e)
			}
		}
		out = append(out, eps...)
	}
	return out, nil
}

// standardEpisodes is GetStandardEpisodes.
func (s *series) standardEpisodes(p *sonarr.ParsedEpisodeInfo, mapped int, sceneSource bool, search *SeriesSearch) ([]*Episode, error) {
	var out []*Episode
	requested := s.searchEpisodes(search)
	for _, n := range p.EpisodeNumbers {
		if s.t.UseSceneNumbering && sceneSource {
			var eps []*Episode
			for _, e := range requested {
				if e.SceneSeason != nil && e.SceneNumber != nil && *e.SceneSeason == p.SeasonNumber && *e.SceneNumber == n {
					eps = append(eps, e)
				}
			}
			if len(eps) == 0 {
				eps = s.bySceneNumbering(mapped, n)
			}
			if len(eps) > 0 {
				out = append(out, eps...)
				continue
			}
		}
		var found *Episode
		for _, e := range requested {
			if e.Season == mapped && e.Number == n {
				if found != nil {
					return nil, errMultiple
				}
				found = e
			}
		}
		if found == nil {
			var err error
			if found, err = s.find(mapped, n); err != nil {
				return nil, err
			}
		}
		if found != nil {
			out = append(out, found)
		}
	}
	return out, nil
}

// levenshtein is NzbDrone.Common's LevenshteinDistance with its insert, delete and substitute costs, over UTF-16
// code units as .NET strings are (here: runes).
func levenshtein(text, other string, costInsert, costDelete, costSubstitute int) int {
	if text == other {
		return 0
	}
	a, b := []rune(text), []rune(other)
	if len(a) == 0 {
		return len(b) * costInsert
	}
	if len(b) == 0 {
		return len(a) * costDelete
	}
	m := make([]int, len(b)+1)
	for i := 1; i < len(m); i++ {
		m[i] = i * costInsert
	}
	for i := 0; i < len(a); i++ {
		topLeft := m[0]
		m[0] += costDelete
		for j := 0; j < len(b); j++ {
			top, left := m[j], m[j+1]
			sub := topLeft
			if a[i] != b[j] {
				sub += costSubstitute
			}
			topLeft = m[j+1]
			m[j+1] = min(top+costInsert, left+costDelete, sub)
		}
	}
	return m[len(b)]
}
