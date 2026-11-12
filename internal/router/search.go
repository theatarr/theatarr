package router

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/ceiling"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/indexer/lookup"
	"github.com/theatarr/theatarr/internal/policy"
)

// Searched is one search decided for a version: every decision ranked, and the route request built from the
// accepted ones.
type Searched struct {
	Coverage []ceiling.Coverage
	Request  Request
	Ranked   []*decide.Decision
	// Notes are indexers that failed or were skipped.
	Notes []string
	// Incomplete: at least one planned request failed or returned a partial answer (our own page_cap aside:
	// indexer.Result.Capped). Releases that did answer route; acquire counts the run as searched with a shorter
	// again_after (acquire.partialAgain).
	Incomplete bool
	// NoAnswer: the target asked at least one indexer and none answered (every request failed). The search says
	// nothing about the title; acquire records it as an infrastructure failure, not as searched.
	NoAnswer bool
	// Releases maps every decision of Ranked to the release it was made for (rejected ones too), for interactive
	// search answers.
	Releases map[*decide.Decision]indexer.Release
}

// SearchTarget is one version's decision criteria in a shared search. Series is the series search in TVDB numbering
// (the episodes wanted, by their own season and number); each query derives its own criteria from it.
type SearchTarget struct {
	ID       string
	Title    *decide.Title
	Version  decide.Version
	Existing *decide.ExistingFile
	Series   *decide.SeriesSearch
}

// SearchRequest searches the version's indexers for the title (through the gateway), decides every release and
// builds the route request: search + decide, nothing written. ss is the series search (season, episode); nil for a
// movie. existing is the movie version's file (nil for none or a series). A refusal because indexer traffic is off
// the VPN is returned as an error wrapping gateway.ErrVPNRequired.
func SearchRequest(ctx context.Context, eng *decide.Engine, svc *indexer.Service, t decide.Title, v decide.Version,
	existing *decide.ExistingFile, ss *decide.SeriesSearch) (*Searched, error) {
	got, err := SearchRequests(ctx, eng, svc, t, []SearchTarget{{ID: v.Key, Version: v, Existing: existing, Series: ss}})
	if err != nil {
		return nil, err
	}
	return got[v.Key], nil
}

type searchQuery struct {
	indexer string
	query   indexer.Query
	// season is the TVDB season the answer covers for the search ceiling; complete a season-wide search that can
	// prove absence.
	season   int
	complete bool
	// criteria are what the answer is judged with (nil for a movie): the query's own Sonarr criteria for a feed's
	// criteria query, plainCriteria for a plain one.
	criteria []*decide.SeriesSearch
}

// searchNow is the clock of anime season searches (which episodes have aired).
var searchNow = time.Now

// relatedRows is the Query.Related of a title's searches: a row of an id request is kept when it is about the title
// for any target's version (decide.Related).
func relatedRows(ctx context.Context, t decide.Title, targets []SearchTarget) func(indexer.Release) bool {
	type check struct {
		title  decide.Title
		sonarr string
	}
	checks := make([]check, 0, len(targets))
	for _, target := range targets {
		c := check{title: t}
		if target.Title != nil {
			c.title = *target.Title
		}
		if target.Version.Policy != nil {
			c.sonarr = target.Version.Policy.Sonarr
		}
		checks = append(checks, c)
	}
	return func(rel indexer.Release) bool {
		for i := range checks {
			if decide.Related(ctx, rel, &checks[i].title, checks[i].sonarr) {
				return true
			}
		}
		return false
	}
}

type queryResult struct {
	result   *indexer.Result
	releases []indexer.Release
	err      error
}

// SearchRequests runs the union of the targets' indexer queries once, then decides each target against only the
// queries its version asked for. The ID lets a caller include more than one target for a version. A series search
// asks Prowlarr's feeds Sonarr's search criteria (criteria.go: scene numbers, air dates, absolute numbers, scene
// names), and judges each answer with the criteria of the query that found it. Cardigann uses the same criteria as
// keyword searches; other sources get the plain query in
// TVDB numbering (SeaDex: the AniList entries of the TVDB season), judged with plainCriteria. A release found by
// several of a target's queries keeps its best decision (Sonarr's DeDupeDecisions: fewest rejections).
func SearchRequests(ctx context.Context, eng *decide.Engine, svc *indexer.Service, t decide.Title,
	targets []SearchTarget) (map[string]*Searched, error) {
	ctx = decide.WithParseMemo(ctx)
	related := relatedRows(ctx, t, targets)
	out := make(map[string]*Searched, len(targets))
	refs := make(map[string][]searchQuery, len(targets))
	queries := make([]searchQuery, 0)
	querySeen := map[string]bool{}
	for _, target := range targets {
		tt := t
		if target.Title != nil {
			tt = *target.Title
		}
		v := target.Version
		if v.Policy == nil {
			return nil, fmt.Errorf("router: version %q has no policy", v.Key)
		}
		q := indexer.Query{Type: indexer.QueryMovie, Title: tt.Name, Year: tt.Year, IMDb: tt.IMDb, TMDb: tt.TMDb, Anime: tt.Anime}
		if tt.Kind == policy.KindSeries {
			if target.Series == nil {
				return nil, errors.New("router: a series search needs a season (and an episode for one episode)")
			}
			q = indexer.Query{Type: indexer.QuerySeries, Title: tt.Name, IMDb: tt.IMDb, TVDB: tt.TVDB,
				Season: target.Series.Season, Episode: target.Series.Episode,
				Anime: tt.SeriesType == "anime" || tt.Anime, EpisodeCounts: EpisodeCounts(tt.Episodes)}
			for _, m := range tt.SceneMappings {
				q.Aliases = append(q.Aliases, m.Title)
			}
		}
		plan := planSeries(&tt, target.Series, q)
		targetSeen := map[string]int{}
		for _, k := range v.Policy.Indexers {
			sr := svc.Searcher(k)
			if sr == nil {
				continue
			}
			for _, sq := range plan.forKind(sr.Kind()) {
				qq := sq.query
				qq.Related = related
				if qq.Validate() != nil || !sr.Supports(qq) {
					continue
				}
				var crit []*decide.SeriesSearch
				if sq.search != nil {
					crit = []*decide.SeriesSearch{sq.search}
				} else if tt.Kind == policy.KindSeries {
					crit = plan.plain
				}
				ref := searchQuery{indexer: k, query: qq, season: sq.season, complete: sq.complete, criteria: crit}
				key := k + "\x00" + qq.Key()
				if i, ok := targetSeen[key]; ok {
					// Two of the target's criteria ask the same request: its answer is judged with both.
					refs[target.ID][i].criteria = appendCriteria(refs[target.ID][i].criteria, crit...)
					continue
				}
				targetSeen[key] = len(refs[target.ID])
				refs[target.ID] = append(refs[target.ID], ref)
				if !querySeen[key] {
					querySeen[key] = true
					queries = append(queries, ref)
				}
			}
		}
		out[target.ID] = &Searched{}
	}

	results := make(map[string]queryResult, len(queries))
	queryCtx, cancelQueries := context.WithCancel(ctx)
	defer cancelQueries()
	// Each indexer has one worker; bound independent indexers without changing query/result order.
	groups := map[string][]searchQuery{}
	for _, ref := range queries {
		groups[ref.indexer] = append(groups[ref.indexer], ref)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	slots := make(chan struct{}, 8)
	for _, group := range groups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-queryCtx.Done():
				return
			}
			defer func() { <-slots }()
			for _, ref := range group {
				if queryCtx.Err() != nil {
					return
				}
				res, err := svc.Search(queryCtx, ref.indexer, ref.query)
				if errors.Is(err, gateway.ErrVPNRequired) {
					cancelQueries()
				}
				got := queryResult{result: res, err: err}
				if err == nil {
					got.releases = res.Releases
				}
				mu.Lock()
				results[ref.indexer+"\x00"+ref.query.Key()] = got
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, ref := range queries {
		if err := results[ref.indexer+"\x00"+ref.query.Key()].err; errors.Is(err, gateway.ErrVPNRequired) {
			return nil, err
		}
	}

	if db := svc.StandingDB(); db != nil {
		var err error
		ctx, err = decide.WithStoredSeaDex(ctx, db)
		if err != nil {
			return nil, err
		}
	}

	for _, target := range targets {
		searched := out[target.ID]
		tt := t
		if target.Title != nil {
			tt = *target.Title
		}
		var rels []indexer.Release
		var snapshots []indexer.SeaDexSnapshot
		var crits [][]*decide.SeriesSearch
		seen := map[string]int{}
		answered := 0
		for _, ref := range refs[target.ID] {
			key := ref.indexer + "\x00" + ref.query.Key()
			got := results[key]
			if got.err != nil {
				searched.Incomplete = true
				searched.Notes = append(searched.Notes, fmt.Sprintf("%s: %v", ref.indexer, got.err))
				continue
			}
			answered++
			snapshots = append(snapshots, got.result.SeaDex...)
			// Our own page_cap is a choice, not a failure: the search counts as done (its coverage stays incomplete
			// for the ceiling below). A request that failed part-way is a partial answer.
			if got.result.Truncated && !got.result.Capped {
				searched.Incomplete = true
				searched.Notes = append(searched.Notes, ref.indexer+": partial answer")
			}
			searched.Notes = append(searched.Notes, got.result.Notes...)
			searcher := svc.Searcher(ref.indexer)
			if tt.Kind == policy.KindSeries && seriesQueriesByKind[searcher.Kind()] == queryKeywords && ref.query.Criteria == "" {
				searched.Incomplete = true
				searched.Notes = append(searched.Notes, ref.indexer+": only a plain keyword query; no episode criteria")
			}
			season := -1
			if tt.Kind == policy.KindSeries {
				season = ref.season
			}
			searched.Coverage = append(searched.Coverage, ceiling.Coverage{Indexer: ref.indexer, Kind: searcher.Kind(), Season: season,
				Complete: !got.result.Cached && !got.result.Shared && !got.result.Truncated && ref.complete})
			for _, r := range got.releases {
				if len(refs[target.ID]) > 1 && r.InfoHash != "" {
					k := ref.indexer + "\x00" + r.InfoHash
					if i, ok := seen[k]; ok {
						crits[i] = appendCriteria(crits[i], ref.criteria...)
						continue
					}
					seen[k] = len(rels)
				}
				rels = append(rels, r)
				crits = append(crits, ref.criteria)
			}
		}
		searched.NoAnswer = len(refs[target.ID]) > 0 && answered == 0
		indexer.MarkSeaDex(rels)
		svc.ApplyCachedOn(ctx, rels)
		svc.CheckAvailability(ctx, []*policy.VersionPolicy{target.Version.Policy}, rels)
		ctx := decide.WithSeaDex(ctx, snapshots)
		byDecision := map[*decide.Decision]indexer.Release{}
		byCriteria := map[string][]*decide.SeriesSearch{}
		var ds []*decide.Decision
		for i, rel := range rels {
			byCriteria[rel.Indexer+"\x00"+rel.InfoHash+"\x00"+rel.Title] = crits[i]
			var d *decide.Decision
			var err error
			if tt.Kind == policy.KindSeries {
				d, err = decideBest(ctx, eng.DecideSearch, rel, tt, target.Version, crits[i], target.Series)
			} else {
				d, err = eng.Decide(decide.WithSearch(ctx), decide.Release{Release: rel}, tt, target.Version, target.Existing)
			}
			if err != nil {
				return nil, err
			}
			byDecision[d] = rel
			ds = append(ds, d)
		}
		searched.Ranked = decide.Rank(ds)
		if checkTop(ctx, svc, target.Version.Policy, searched.Ranked, byDecision) {
			// The check's positives feed the ranking's cache tie-break: rank again, so the order shown is the order
			// the route tries.
			for _, d := range ds {
				eng.Recache(d, target.Version, byDecision[d])
			}
			searched.Ranked = decide.Rank(ds)
		}
		searched.Releases = byDecision
		searched.Request = Request{TitleID: tt.ID, VersionID: target.Version.ID, Version: target.Version.Key,
			Candidates: Candidates(searched.Ranked, byDecision)}
		for i := range searched.Request.Candidates {
			c := &searched.Request.Candidates[i]
			c.Search = true
			c.Criteria = byCriteria[c.Release.Indexer+"\x00"+c.Release.InfoHash+"\x00"+c.Release.Title]
		}
	}
	return out, nil
}

// seriesPlan is a target's queries by source kind: Sonarr's criteria for Prowlarr's feeds, the AniList entries of
// the TVDB season for SeaDex, else the plain query; plain are the criteria the plain query's answers are judged with.
type seriesPlan struct {
	base     seriesQuery
	criteria []seriesQuery
	anilist  []int
	plain    []*decide.SeriesSearch
}

// planSeries plans a target's queries; q is the plain query (TVDB numbering). A movie has the plain query only.
func planSeries(t *decide.Title, ss *decide.SeriesSearch, q indexer.Query) *seriesPlan {
	p := &seriesPlan{base: seriesQuery{query: q, season: q.Season, complete: q.Episode == 0}}
	if t.Kind != policy.KindSeries {
		return p
	}
	p.anilist = lookup.SeasonAniList(t.AnimeMap, ss.Season)
	p.criteria = criteriaQueries(t, ss, searchNow())
	p.plain = plainCriteria(ss, p.criteria)
	return p
}

type seriesQueryKind int

const (
	queryPlain seriesQueryKind = iota
	queryCriteria
	queryKeywords
	queryAniList
)

// This table defines which numbering/query family every adapter consumes.
var seriesQueriesByKind = map[string]seriesQueryKind{
	"torznab": queryCriteria, "cardigann": queryKeywords,
	"dmm": queryPlain, "torrentio": queryPlain, "zilean": queryPlain,
	"seadex": queryAniList,
}

// forKind is the queries for a source of this kind.
func (p *seriesPlan) forKind(kind string) []seriesQuery {
	switch seriesQueriesByKind[kind] {
	case queryCriteria, queryKeywords:
		if len(p.criteria) == 0 {
			// Without usable episode metadata, a keyword query cannot establish season coverage.
			sq := p.base
			if seriesQueriesByKind[kind] == queryKeywords && sq.query.Type == indexer.QuerySeries {
				sq.complete = false
			}
			return []seriesQuery{sq}
		}
		return p.criteria
	case queryAniList:
		if len(p.anilist) == 0 {
			return []seriesQuery{p.base}
		}
		qs := make([]seriesQuery, 0, len(p.anilist))
		for _, id := range p.anilist {
			sq := p.base
			sq.query.AniList = id
			qs = append(qs, sq)
		}
		return qs
	}
	return []seriesQuery{p.base}
}

// decideBest decides a series release with each of the criteria it was found with (fallback when none) and keeps
// the decision with the fewest rejections, the first on a tie: Sonarr decides each criteria's reports apart and
// DeDupeDecisions keeps the best of a release's decisions.
func decideBest(ctx context.Context, decideFn DecideSeriesFunc, rel indexer.Release, t decide.Title, v decide.Version,
	criteria []*decide.SeriesSearch, fallback *decide.SeriesSearch) (*decide.Decision, error) {
	if len(criteria) == 0 {
		criteria = []*decide.SeriesSearch{fallback}
	}
	var best *decide.Decision
	for _, c := range criteria {
		d, err := decideFn(ctx, decide.Release{Release: rel}, t, v, c)
		if err != nil {
			return nil, err
		}
		if best == nil || len(d.Rejections) < len(best.Rejections) {
			best = d
		}
		if len(best.Rejections) == 0 {
			break
		}
	}
	return best, nil
}

// appendCriteria appends the criteria not yet in cs.
func appendCriteria(cs []*decide.SeriesSearch, more ...*decide.SeriesSearch) []*decide.SeriesSearch {
	for _, c := range more {
		if !slices.Contains(cs, c) {
			cs = append(cs, c)
		}
	}
	return cs
}

// checkTop runs the bounded TorBox cache check on the best accepted releases for version v (rank order) and copies a
// positive answer to every decision of the same hash. It reports whether any release gained evidence.
func checkTop(ctx context.Context, svc *indexer.Service, v *policy.VersionPolicy, ranked []*decide.Decision,
	byDecision map[*decide.Decision]indexer.Release) bool {
	rels := make([]indexer.Release, len(ranked))
	var priority, also []*indexer.Release
	for i, d := range ranked {
		rel, ok := byDecision[d]
		if !ok {
			continue
		}
		rels[i] = rel
		if d.Accepted {
			priority = append(priority, &rels[i])
		} else {
			also = append(also, &rels[i])
		}
	}
	if len(priority) == 0 || svc.CheckCached(ctx, []*policy.VersionPolicy{v}, priority, also) == 0 {
		return false
	}
	grew := false
	for i, d := range ranked {
		if old, ok := byDecision[d]; ok {
			grew = grew || len(rels[i].CachedOn) != len(old.CachedOn)
			byDecision[d] = rels[i]
		}
	}
	return grew
}

// EpisodeCounts is episodes per season of a series' episodes (TVDB numbering, specials in season 0): what
// Torrentio's pack sizes are scaled by.
func EpisodeCounts(eps []decide.Episode) map[int]int {
	if len(eps) == 0 {
		return nil
	}
	out := map[int]int{}
	for _, e := range eps {
		out[e.Season]++
	}
	return out
}
