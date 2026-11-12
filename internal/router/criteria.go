package router

import (
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
)

// Sonarr's series search criteria (ReleaseSearchService, SceneMappingService.GetSceneNames): what a series search
// asks Prowlarr's feeds, in scene numbering, by air date for daily series and by absolute number for anime, with the
// scene mappings' names as search terms. Sources keyed by ids and TVDB numbering (DMM, Torrentio, Zilean, SeaDex)
// keep the plain query.
//
// Ported from Sonarr v4, except anime seasons, which follow v5: one season search, and the episodes one by one only
// while the season is still airing (v4 always searches every episode as well, dozens of requests per feed).
// Deviations: a daily episode without an air date is searched by its numbers (Sonarr fails the search); the
// episodes are the target's, already the wanted ones, so no monitored filter; non-Latin names are not search terms
// (GetSceneNames' IsEnglish, here also for the episode mappings, as our TVDB aliases include translations).
//
// Each query carries its own decision criteria, as each of Sonarr's SearchCriteriaBase is dispatched and its reports
// decided against it (ReleaseSearchService.Dispatch, GetSearchDecision): the kind of search, the season and episode
// it asked for (scene numbering where it applies) and the episodes it is for. The target's own search stays in TVDB
// numbering; it is what the plain queries of the other sources ask and are judged with (plainCriteria).

// seriesQuery is one query of a series search, the TVDB season its answer covers for the search ceiling, and the
// criteria its answer is judged with.
type seriesQuery struct {
	query    indexer.Query
	season   int
	complete bool
	search   *decide.SeriesSearch
}

// sceneEpisodeMapping is Sonarr's SceneEpisodeMapping.
type sceneEpisodeMapping struct {
	mode            indexer.SearchMode
	titles          []string
	seasonTitles    []string
	season, episode int
}

// sceneSeasonMapping is Sonarr's SceneSeasonMapping.
type sceneSeasonMapping struct {
	eps          []*decide.Episode
	first        sceneEpisodeMapping
	mode         indexer.SearchMode
	titles       []string
	seasonTitles []string
	season       int
}

type criteriaBuilder struct {
	t    *decide.Title
	ss   *decide.SeriesSearch
	now  time.Time
	base indexer.Query
	out  []seriesQuery
}

// criteriaQueries are the queries of a series search for Prowlarr's feeds; nil when the search names no episode
// we know (a whole-series search, a title without episodes), which keeps the plain query.
func criteriaQueries(t *decide.Title, ss *decide.SeriesSearch, now time.Time) []seriesQuery {
	if ss == nil {
		return nil
	}
	eps, single := searchEpisodes(t, ss)
	if len(eps) == 0 {
		return nil
	}
	b := &criteriaBuilder{t: t, ss: ss, now: now, base: indexer.Query{Type: indexer.QuerySeries, Title: t.Name, IMDb: t.IMDb,
		TVDB: t.TVDB, Anime: t.SeriesType == "anime" || t.Anime}}
	if single {
		b.episode(eps[0])
	} else {
		b.season(eps)
	}
	return b.out
}

// plainCriteria are what the answers of a plain query (TVDB numbering: DMM, Torrentio, Zilean, SeaDex, or a feed
// without criteria) are judged with: the target's search, then each of its Sonarr criteria that names another season
// or episode (scene numbering) or episodes the target does not request (a season search naming none), so a release named in either numbering is judged against its own. The best decision
// is kept (searchDecide). Anime, daily and special criteria are not added: no specification reads their numbers.
func plainCriteria(ss *decide.SeriesSearch, qs []seriesQuery) []*decide.SeriesSearch {
	out := []*decide.SeriesSearch{ss}
	for _, sq := range qs {
		c := sq.search
		if c == nil || c.Kind != profile.SearchSeason && c.Kind != profile.SearchSingleEpisode {
			continue
		}
		dup := false
		for _, o := range out {
			// A criteria that requests episodes the kept one does not (a season search naming none) stays: the
			// requested-episode check reads them.
			if o.Kind == c.Kind && o.Season == c.Season && o.Episode == c.Episode && containsAll(o.Episodes, c.Episodes) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, c)
		}
	}
	return out
}

// containsAll reports whether every id of sub is in ids.
func containsAll(ids, sub []int64) bool {
	for _, id := range sub {
		if !slices.Contains(ids, id) {
			return false
		}
	}
	return true
}

// searchEpisodes are the episodes a search is for: its episode ids, else its one episode, else its season (TVDB
// numbering). single is an episode search.
func searchEpisodes(t *decide.Title, ss *decide.SeriesSearch) (eps []*decide.Episode, single bool) {
	switch {
	case len(ss.Episodes) > 0:
		want := map[int64]bool{}
		for _, id := range ss.Episodes {
			want[id] = true
		}
		for i := range t.Episodes {
			if want[t.Episodes[i].ID] {
				eps = append(eps, &t.Episodes[i])
			}
		}
		return eps, len(eps) == 1 && ss.Episode != 0
	case ss.Episode > 0:
		for i := range t.Episodes {
			if e := &t.Episodes[i]; e.Season == ss.Season && e.Number == ss.Episode {
				return []*decide.Episode{e}, true
			}
		}
	case ss.Season > 0:
		for i := range t.Episodes {
			if t.Episodes[i].Season == ss.Season {
				eps = append(eps, &t.Episodes[i])
			}
		}
	}
	return eps, false
}

func intOr(p *int, d int) int {
	if p != nil {
		return *p
	}
	return d
}

// nonNegative is Sonarr's NonNegative(): a negative number is none.
func nonNegative(p *int) *int {
	if p == nil || *p < 0 {
		return nil
	}
	return p
}

// add adds a query for the episodes eps (its coverage), judged as a search of kind for the same episodes.
func (b *criteriaBuilder) add(q indexer.Query, kind profile.SearchKind, eps []*decide.Episode) {
	b.addFor(q, kind, eps, eps)
}

// addFor adds a query covering eps whose criteria request the episodes requested (Sonarr's criteria Episodes).
func (b *criteriaBuilder) addFor(q indexer.Query, kind profile.SearchKind, eps, requested []*decide.Episode) {
	sc := &decide.SeriesSearch{Kind: kind, MonitoredOnly: b.ss.MonitoredOnly, Interactive: b.ss.Interactive}
	switch kind {
	case profile.SearchSingleEpisode, profile.SearchAnimeEpisode:
		sc.Season, sc.Episode = q.Season, q.Episode
	case profile.SearchSeason, profile.SearchAnimeSeason:
		sc.Season = q.Season
		if q.Criteria == indexer.CriteriaAnimeEpisode {
			sc.Episode = q.Episode
		}
	default: // daily, daily season and specials name no numbers the specifications read
		sc.Season = eps[0].Season
	}
	for _, e := range requested {
		sc.Episodes = append(sc.Episodes, e.ID)
	}
	sq := seriesQuery{query: q, season: eps[0].Season, search: sc}
	if q.Criteria == indexer.CriteriaSeason || q.Criteria == indexer.CriteriaAnimeSeason {
		// A season-wide search in TVDB numbering by id can prove absence; a remapped or title-only one cannot.
		sq.complete = q.Season == sq.season && q.Mode&indexer.SearchTitle == 0
		for _, e := range eps {
			if e.Season != sq.season {
				sq.complete = false
			}
		}
	}
	b.out = append(b.out, sq)
}

// episode is EpisodeSearch.
func (b *criteriaBuilder) episode(e *decide.Episode) {
	switch {
	case b.t.SeriesType == "daily" && validAirDate(e.AirDate):
		b.daily(e)
	case b.t.SeriesType == "anime":
		if e.Season == 0 && e.SceneAbsolute == nil && e.Absolute == 0 {
			b.special([]*decide.Episode{e})
			return
		}
		b.animeEpisode(e, profile.SearchAnimeEpisode)
	case e.Season == 0:
		b.special([]*decide.Episode{e})
	default:
		b.single(e)
	}
}

func validAirDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

// single is SearchSingle: one query per scene episode mapping.
func (b *criteriaBuilder) single(e *decide.Episode) {
	for _, m := range mergeEpisodeMappings(b.episodeMappings(e)) {
		if m.mode == indexer.SearchTitle && len(m.titles) == 0 {
			continue
		}
		q := b.base
		q.Criteria, q.Season, q.Episode, q.SceneTitles, q.Mode = indexer.CriteriaEpisode, m.season, m.episode, m.titles, m.mode
		b.add(q, profile.SearchSingleEpisode, []*decide.Episode{e})
	}
}

// daily is SearchDaily.
func (b *criteriaBuilder) daily(e *decide.Episode) {
	q := b.base
	q.Criteria, q.AirDate, q.SceneTitles = indexer.CriteriaDaily, e.AirDate, b.sceneNames([]*decide.Episode{e})
	b.add(q, profile.SearchDaily, []*decide.Episode{e})
}

// animeEpisode is SearchAnime: scene season and episode, scene absolute number. kind is SearchAnimeSeason for the
// episodes of a season search (IsSeasonSearch: a season pack is not rejected).
func (b *criteriaBuilder) animeEpisode(e *decide.Episode, kind profile.SearchKind) {
	q := b.base
	q.Criteria = indexer.CriteriaAnimeEpisode
	q.Season, q.Episode = intOr(e.SceneSeason, e.Season), intOr(e.SceneNumber, e.Number)
	q.Absolute = intOr(e.SceneAbsolute, e.Absolute)
	q.SceneTitles = b.sceneNames([]*decide.Episode{e})
	b.add(q, kind, []*decide.Episode{e})
}

// specialCategories are Sonarr's Categories and AnimeCategories as Prowlarr syncs them (TV, TV/Anime).
var specialCategories = []int{5000, 5070}

// special is SearchSpecial: a text search "<series> <episode title>" per scene title and episode, then each
// episode by its numbers.
func (b *criteriaBuilder) special(eps []*decide.Episode) {
	var clean []string
	for _, n := range b.sceneNames(eps) {
		if c := indexer.CleanSceneTitle(n); c != "" && !containsFold(clean, c) {
			clean = append(clean, c)
		}
	}
	var texts []string
	for _, e := range eps {
		et := strings.TrimSpace(e.Title)
		if et == "" {
			continue
		}
		for _, c := range clean {
			s := strings.ReplaceAll(c+" "+indexer.CleanSceneTitle(et), "+", " ")
			if !containsFold(texts, s) {
				texts = append(texts, s)
				b.addFor(indexer.Query{Type: indexer.QueryText, Title: s, Categories: specialCategories},
					profile.SearchSpecial, []*decide.Episode{e}, eps)
			}
		}
	}
	for _, e := range eps {
		b.single(e)
	}
}

// season is SeasonSearch.
func (b *criteriaBuilder) season(eps []*decide.Episode) {
	switch b.t.SeriesType {
	case "anime":
		b.animeSeason(eps)
		return
	case "daily":
		b.dailySeason(eps)
		return
	}
	for _, m := range b.seasonMappings(eps) {
		switch {
		case m.season == 0:
			b.special(m.eps)
		case m.mode == indexer.SearchTitle && len(m.titles) == 0:
		case len(m.eps) == 1 && !b.ss.Pack:
			q := b.base
			q.Criteria, q.Season, q.Episode = indexer.CriteriaEpisode, m.season, m.first.episode
			q.SceneTitles, q.Mode = m.titles, m.mode
			b.add(q, profile.SearchSingleEpisode, m.eps)
		default:
			q := b.base
			q.Criteria, q.Season, q.SceneTitles, q.Mode = indexer.CriteriaSeason, m.season, m.titles, m.mode
			b.add(q, profile.SearchSeason, m.eps)
		}
	}
}

// animeSeason is v5's SearchAnimeSeason: aired episodes by scene season, one search per season with the season's
// names, and the episodes one by one unless every episode of their seasons has aired (or airs within a day).
func (b *criteriaBuilder) animeSeason(eps []*decide.Episode) {
	var aired []*decide.Episode
	seasons := map[int]bool{}
	for _, e := range eps {
		seasons[e.Season] = true
		if e.AirDateUTC != nil && e.AirDateUTC.Before(b.now) {
			aired = append(aired, e)
		}
	}
	allAired := true
	for i := range b.t.Episodes {
		e := &b.t.Episodes[i]
		if seasons[e.Season] && (e.AirDateUTC == nil || e.AirDateUTC.After(b.now.Add(24*time.Hour))) {
			allAired = false
		}
	}
	mappings := b.seasonMappings(aired)
	done := map[int]bool{}
	for _, m := range mappings {
		if done[m.season] {
			continue
		}
		done[m.season] = true
		if m.season <= 0 {
			continue // AnimeSeasonSearchCriteria asks nothing for season 0
		}
		q := b.base
		q.Criteria, q.Season = indexer.CriteriaAnimeSeason, m.season
		var covered []*decide.Episode
		for _, o := range mappings {
			if o.season == m.season {
				q.SceneTitles = appendFold(q.SceneTitles, o.titles...)
				q.SeasonTitles = appendFold(q.SeasonTitles, o.seasonTitles...)
				covered = append(covered, o.eps...)
			}
		}
		// AnimeSeasonSearchCriteria requests every episode of the search, whichever scene season it asks.
		b.addFor(q, profile.SearchAnimeSeason, covered, eps)
	}
	if !allAired {
		for _, e := range aired {
			b.animeEpisode(e, profile.SearchAnimeSeason)
		}
	}
}

// dailySeason is SearchDailySeason: the episodes with an air date by year, a year search for several.
func (b *criteriaBuilder) dailySeason(eps []*decide.Episode) {
	var years []string
	byYear := map[string][]*decide.Episode{}
	for _, e := range eps {
		if !validAirDate(e.AirDate) {
			continue
		}
		y := e.AirDate[:4]
		if byYear[y] == nil {
			years = append(years, y)
		}
		byYear[y] = append(byYear[y], e)
	}
	for _, y := range years {
		yeps := byYear[y]
		if len(yeps) == 1 {
			b.daily(yeps[0])
			continue
		}
		q := b.base
		q.Criteria, q.SceneTitles = indexer.CriteriaDailySeason, b.sceneNames(yeps)
		t, _ := time.Parse(time.DateOnly, yeps[0].AirDate)
		q.AirYear = t.Year()
		b.add(q, profile.SearchDailySeason, yeps)
	}
}

// episodeMappings is GetSceneEpisodeMappings(series, episode, sceneMappings): one mapping per scene mapping that
// applies to the episode's season, plus the series title in scene numbering unless a mapping has that name.
func (b *criteriaBuilder) episodeMappings(e *decide.Episode) []sceneEpisodeMapping {
	var out []sceneEpisodeMapping
	includeGlobal := true
	clean := sonarr.CleanSeriesTitle(b.t.Name)
	for _, m := range b.t.SceneMappings {
		ignoreScene := m.SceneOrigin == "tvdb" || m.SceneOrigin == "unknown:tvdb"
		mapSceneSeason, mapSeason := nonNegative(m.SceneSeasonNumber), nonNegative(m.SeasonNumber)
		mapped := intOr(e.SceneSeason, e.Season)
		if ignoreScene {
			mapped = e.Season
		}
		release := intOr(mapSceneSeason, mapped)
		if mapSceneSeason != nil {
			// The mapping applies to the season it names (else its scene season).
			alt := *mapSceneSeason
			if mapSeason != nil {
				alt = *mapSeason
			}
			if alt != mapped {
				continue
			}
		} else if mapSeason != nil && *mapSeason != e.Season {
			continue
		}
		if m.Title == b.t.Name {
			includeGlobal = false
		}
		isSeasonTitle := mapSceneSeason != nil && clean != sonarr.CleanSeriesTitle(m.Title)
		sm := sceneEpisodeMapping{season: release, episode: intOr(e.SceneNumber, e.Number)}
		if ignoreScene {
			sm.episode = e.Number
		}
		if isSeasonTitle {
			sm.mode = indexer.SearchTitle
		}
		if indexer.IsEnglishTitle(m.Title) && strings.TrimSpace(m.Title) != "" {
			sm.titles = []string{m.Title}
			if isSeasonTitle {
				sm.seasonTitles = []string{m.Title}
			}
		}
		out = append(out, sm)
	}
	if includeGlobal {
		out = append(out, sceneEpisodeMapping{titles: []string{b.t.Name}, season: intOr(e.SceneSeason, e.Season),
			episode: intOr(e.SceneNumber, e.Number)})
	}
	return out
}

// mergeEpisodeMappings is GetSceneEpisodeMappings(series, episode): mappings of one season, episode and mode as one.
func mergeEpisodeMappings(ms []sceneEpisodeMapping) []sceneEpisodeMapping {
	var out []sceneEpisodeMapping
	for _, m := range ms {
		found := false
		for i := range out {
			if o := &out[i]; o.season == m.season && o.episode == m.episode && o.mode == m.mode {
				o.titles = appendFold(o.titles, m.titles...)
				o.seasonTitles = appendFold(o.seasonTitles, m.seasonTitles...)
				found = true
				break
			}
		}
		if !found {
			m.titles = appendFold(nil, m.titles...)
			m.seasonTitles = appendFold(nil, m.seasonTitles...)
			out = append(out, m)
		}
	}
	return out
}

// seasonMappings is GetSceneSeasonMappings: the episodes grouped by scene and TVDB season, each group's mappings
// merged by release season and mode.
func (b *criteriaBuilder) seasonMappings(eps []*decide.Episode) []sceneSeasonMapping {
	var keys []int
	groups := map[int][]*decide.Episode{}
	for _, e := range eps {
		k := intOr(e.SceneSeason, e.Season)*100000 + e.Season
		if groups[k] == nil {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], e)
	}
	var out []sceneSeasonMapping
	for _, k := range keys {
		group := groups[k]
		for _, em := range b.episodeMappings(group[0]) {
			found := false
			for i := range out {
				if o := &out[i]; o.season == em.season && o.mode == em.mode {
					for _, e := range group {
						if !containsEpisode(o.eps, e) {
							o.eps = append(o.eps, e)
						}
					}
					o.titles = appendFold(o.titles, em.titles...)
					o.seasonTitles = appendFold(o.seasonTitles, em.seasonTitles...)
					found = true
					break
				}
			}
			if !found {
				out = append(out, sceneSeasonMapping{eps: append([]*decide.Episode(nil), group...), first: em,
					mode: em.mode, titles: appendFold(nil, em.titles...), seasonTitles: appendFold(nil, em.seasonTitles...),
					season: em.season})
			}
		}
	}
	return out
}

// sceneNames is SceneMappingService.GetSceneNames for the episodes' TVDB and scene seasons, plus the series title
// (ReleaseSearchService.Get).
func (b *criteriaBuilder) sceneNames(eps []*decide.Episode) []string {
	seasons, sceneSeasons := map[int]bool{}, map[int]bool{}
	for _, e := range eps {
		seasons[e.Season] = true
		sceneSeasons[intOr(e.SceneSeason, e.Season)] = true
	}
	var names []string
	for _, m := range b.t.SceneMappings {
		season, scene := intOr(m.SeasonNumber, -1), intOr(m.SceneSeasonNumber, -1)
		if !seasons[season] && !sceneSeasons[scene] && (season != -1 || scene != -1 || m.SceneOrigin == "tvdb") {
			continue
		}
		if strings.TrimSpace(m.Title) == "" || !indexer.IsEnglishTitle(m.Title) {
			continue
		}
		names = appendFold(names, m.Title)
	}
	return appendFold(names, b.t.Name)
}

func containsFold(ts []string, s string) bool {
	for _, t := range ts {
		if strings.EqualFold(t, s) {
			return true
		}
	}
	return false
}

// appendFold appends the strings not yet in ts, ignoring case (Distinct(InvariantCultureIgnoreCase)).
func appendFold(ts []string, ss ...string) []string {
	for _, s := range ss {
		if s != "" && !containsFold(ts, s) {
			ts = append(ts, s)
		}
	}
	return ts
}

func containsEpisode(eps []*decide.Episode, e *decide.Episode) bool {
	for _, o := range eps {
		if o == e {
			return true
		}
	}
	return false
}
