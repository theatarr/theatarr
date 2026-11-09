package indexer

import (
	"fmt"
	"sort"

	"github.com/theatarr/theatarr/internal/policy"
)

// Title is what the planner plans a search for.
type Title struct {
	Kind    policy.Kind
	Anime   bool
	Title   string
	Year    int
	Aliases []string
	IMDb    string
	TMDb    int
	TVDB    int
	AniList int
	// Seasons to search, for a series (one search per season; anime seasons don't fan out per episode).
	Seasons []int
	// EpisodeCounts per season, passed on to sources that need them (Torrentio pack sizes).
	EpisodeCounts map[int]int
}

// Step is one indexer in a plan, with the queries to send it and the versions its results may serve.
type Step struct {
	Indexer string
	Kind    string
	// Versions whose search this step serves.
	Versions []string
	Queries  []Query
}

// Plan is the ordered list of searches for one title.
type Plan struct {
	Steps []Step
	// Skipped says why an indexer some version lists is not searched.
	Skipped map[string]string
}

// kindRank orders sources within a plan: SeaDex's best pick first for anime (one cheap request), then Prowlarr
// feeds and native (cardigann) sites, DMM (tight per-key budget, and its results say what is cached), Torrentio, Zilean. Ties break on the key. A skeleton: priorities and per-version preferences come with the decision engine.
var kindRank = map[string]int{"seadex": 0, "torznab": 1, "dmm": 2, "torrentio": 3, "zilean": 4, "cardigann": 1}

// PlanSearch decides which indexers to query for a title, in what order, for every version of the policy that
// applies to it (same kind; anime versions for anime titles). Each indexer is searched once for all the versions
// that list it.
func PlanSearch(p *policy.Policy, svc *Service, t Title) Plan {
	plan := Plan{Skipped: map[string]string{}}
	byIndexer := map[string][]string{}
	for _, vk := range sortedKeys(p.Versions) {
		v := p.Versions[vk]
		if v.Kind != t.Kind || (v.Anime != nil && *v.Anime != t.Anime) {
			continue
		}
		for _, ik := range v.Indexers {
			if p.Indexers[ik] == nil {
				continue
			}
			byIndexer[ik] = append(byIndexer[ik], vk)
		}
	}
	queries := t.queries()
	for ik, versions := range byIndexer {
		delete(plan.Skipped, ik) // another version may use it
		sr := svc.Searcher(ik)
		if sr == nil {
			plan.Skipped[ik] = "no searcher"
			continue
		}
		st := Step{Indexer: ik, Kind: sr.Kind(), Versions: versions}
		for _, q := range queries {
			if q.Validate() == nil && sr.Supports(q) {
				st.Queries = append(st.Queries, q)
			}
		}
		if len(st.Queries) == 0 {
			plan.Skipped[ik] = fmt.Sprintf("%s cannot search this title with the ids it has", sr.Kind())
			continue
		}
		plan.Steps = append(plan.Steps, st)
	}
	sort.Slice(plan.Steps, func(i, j int) bool {
		a, b := plan.Steps[i], plan.Steps[j]
		if pa, pb := svc.Priority(a.Indexer), svc.Priority(b.Indexer); pa != pb {
			return pa < pb
		}
		ra, rb := rank(a.Kind), rank(b.Kind)
		if ra != rb {
			return ra < rb
		}
		return a.Indexer < b.Indexer
	})
	return plan
}

func rank(kind string) int {
	if r, ok := kindRank[kind]; ok {
		return r
	}
	return len(kindRank)
}

// queries are the searches for a title: one for a movie, one per season for a series.
func (t Title) queries() []Query {
	q := Query{Title: t.Title, Year: t.Year, Aliases: t.Aliases, IMDb: t.IMDb, TMDb: t.TMDb, TVDB: t.TVDB,
		AniList: t.AniList, Anime: t.Anime, EpisodeCounts: t.EpisodeCounts}
	if t.Kind == policy.KindMovie {
		q.Type = QueryMovie
		return []Query{q}
	}
	q.Type = QuerySeries
	var out []Query
	for _, s := range t.Seasons {
		qs := q
		qs.Season = s
		out = append(out, qs)
	}
	return out
}

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
