package anime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Franchise is one franchise to build (an entry of franchises.json).
type Franchise struct {
	Slug        string `json:"slug" yaml:"slug"`
	Name        string `json:"name" yaml:"name"`
	SeedAniList int    `json:"seed_anilist" yaml:"seed_anilist"`
	Scope       string `json:"scope,omitempty" yaml:"scope"`
	LineDepth   *int   `json:"line_depth,omitempty" yaml:"line_depth"`
	ExtraTVDB   []int  `json:"extra_tvdb,omitempty" yaml:"extra_tvdb"`
}

// config is the franchise's settings without slug and name, as the result's "config" shows them.
func (f Franchise) config() map[string]any {
	c := map[string]any{"seed_anilist": f.SeedAniList}
	if f.Scope != "" {
		c["scope"] = f.Scope
	}
	if f.LineDepth != nil {
		c["line_depth"] = *f.LineDepth
	}
	if len(f.ExtraTVDB) > 0 {
		c["extra_tvdb"] = f.ExtraTVDB
	}
	return c
}

// Options tune a build.
type Options struct {
	// Today decides the "unaired" flag and the generated date, as YYYY-MM-DD. Default: today (UTC).
	Today string
	// Overrides are the franchise's curated corrections; nil for none.
	Overrides *Overrides
	// Community, if set, is compared with the orders (an evaluation aid).
	Community *Community
}

// TVDBSeries is one TVDB series of the franchise.
type TVDBSeries struct {
	ID                int             `json:"id"`
	Title             string          `json:"title"`
	SkyhookAniListIDs json.RawMessage `json:"skyhook_anilist_ids"`
	InSonarr          *bool           `json:"in_sonarr"`
	XEM               XEMSummary      `json:"xem"`
}

// Result is a built franchise. Its JSON is the golden out/<slug>.json, plus our mapping decisions per entry
// ("mapping_decision") and the stage outputs ("stages").
type Result struct {
	Franchise            string           `json:"franchise"`
	Slug                 string           `json:"slug"`
	SeedAniList          int              `json:"seed_anilist"`
	Generated            string           `json:"generated"`
	Config               map[string]any   `json:"config"`
	TVDBSeriesList       []TVDBSeries     `json:"tvdb_series"`
	Entries              []*Entry         `json:"entries"`
	ExcludedNeighbours   []Excluded       `json:"excluded_neighbours"`
	TVDBRegularUnclaimed []string         `json:"tvdb_regular_unclaimed"`
	ReleaseOrder         []*Row           `json:"release_order"`
	WatchOrder           []*Row           `json:"watch_order"`
	StoryOrder           *StoryOrder      `json:"story_order"`
	Community            *CommunityResult `json:"community"`
	DatasetVersions      map[string]any   `json:"dataset_versions"`
	Stages               *Stages          `json:"stages,omitempty"`
	// AltCuts are the overrides' alternate cuts, resolved (not in the golden outputs).
	AltCuts []*AltCutMap `json:"alt_cuts,omitempty"`
}

type builder struct {
	ctx      context.Context
	src      *Sources
	m        *mapper
	today    string
	excluded map[string]bool
	st       *Stages
}

func anilistEntry(m *Media, how map[int]string) *Entry {
	var rels []Relation
	for _, e := range m.Relations.Edges {
		if e.Node.Type == "ANIME" {
			rels = append(rels, Relation{e.RelationType, e.Node.ID})
		}
	}
	if rels == nil {
		rels = []Relation{}
	}
	names := nonEmpty(m.Title.Romaji, m.Title.English, m.Title.Native)
	for _, s := range m.Synonyms {
		if s != "" {
			names = append(names, s)
		}
	}
	var fv *string
	if h, ok := how[m.ID]; ok {
		fv = sp(h)
	}
	return &Entry{
		Key: fmt.Sprintf("al:%d", m.ID), Title: m.Title.Romaji, TitleEn: m.Title.English, Names: names,
		AniListID: ip(m.ID), MalID: m.IDMal, Format: m.Format, Status: m.Status,
		Start: fuzzy(m.StartDate), End: fuzzy(m.EndDate), Episodes: m.Episodes, DurationMin: m.Duration,
		AniListRelations: rels, FoundVia: fv, Origin: "anilist",
		TMDbMovie: []int{}, Flags: []string{}, Notes: []string{},
	}
}

func claimedOf(entries []*Entry) map[claimKey]bool {
	c := map[claimKey]bool{}
	for _, e := range entries {
		if e.TVDB != nil {
			for _, se := range e.TVDB.Episodes {
				c[claimKey{e.TVDB.SeriesID, se}] = true
			}
		}
	}
	return c
}

// Build runs the pipeline for one franchise.
func Build(ctx context.Context, src *Sources, f Franchise, opt Options) (*Result, error) {
	if src == nil || src.AniList == nil || src.TVDB == nil || src.Kitsu == nil || src.Lists == nil {
		return nil, fmt.Errorf("anime: AniList, TVDB, Kitsu and the lists are required")
	}
	today := opt.Today
	if today == "" {
		today = time.Now().UTC().Format("2006-01-02")
	}
	b := &builder{ctx: ctx, src: src, m: newMapper(ctx, src), today: today, excluded: map[string]bool{}, st: &Stages{}}
	if opt.Overrides != nil {
		for _, k := range opt.Overrides.Exclude {
			b.excluded[k] = true
		}
	}
	seed := f.SeedAniList

	// 1. crawl
	cr, err := crawlAniList(ctx, src.AniList, seed, CrawlOptions{LineDepth: f.LineDepth, Scope: f.Scope})
	if err != nil {
		return nil, err
	}
	b.st.recordCrawl(cr)

	// 2-4. joins, candidate mappings, arbitration
	var entries []*Entry
	for _, id := range cr.Members.keys {
		e := anilistEntry(cr.Members.m[id], cr.How)
		if k, ok := cr.Kind[id]; ok {
			e.Kind = k
		} else {
			e.Kind = "side"
		}
		b.prepareAniListEntry(e)
		entries = append(entries, e)
	}
	seedMedia, _ := cr.Members.Get(seed)
	seedNames := namesOf(seedMedia)

	// TVDB series of the franchise: those the line, side and branch entries map to
	tv := map[int]bool{}
	for _, e := range entries {
		if (e.Kind == "line" || e.Kind == "side" || e.Kind == "branch") && e.TVDB != nil {
			tv[e.TVDB.SeriesID] = true
		}
	}
	for _, t := range f.ExtraTVDB {
		tv[t] = true
	}
	var readmit []int
	for _, aid := range cr.Excluded.keys {
		x := cr.Excluded.m[aid]
		if len(x.Why) < 9 || x.Why[:9] != "CHARACTER" {
			continue
		}
		fr := src.Lists.FribbAniList(aid)
		var rec *ScudLeeRec
		if fr != nil && fr.AniDBID != nil {
			rec = src.Lists.ScudLeeGet(*fr.AniDBID)
		}
		if rec != nil && tv[rec.tvdb()] {
			readmit = append(readmit, aid)
			cr.How[aid] = fmt.Sprintf("%s; re-admitted: ScudLee maps AniDB %d into TVDB %d", x.Why, *fr.AniDBID, rec.tvdb())
		}
	}
	if len(readmit) > 0 {
		_, order, err := fetchMedia(ctx, src.AniList, sortedInts(readmit))
		if err != nil {
			return nil, err
		}
		for _, m := range order {
			e := anilistEntry(m, cr.How)
			e.Kind = "leaf"
			e.Flags = append(e.Flags, "crossover-linked")
			b.prepareAniListEntry(e)
			entries = append(entries, e)
			cr.Excluded.Delete(m.ID)
		}
	}
	b.st.recordJoins(entries)

	// 5a. secondary entries
	secondary := b.secondaryEntries(entries, tv, seedNames)
	for _, e := range secondary {
		e.Kind = "side"
		b.m.mapEntry(e)
		if e.TVDB != nil && len(e.TVDB.Episodes) > 0 {
			e.Mapping = &MappingOutcome{Status: StatusAuto, Winner: "scudlee", Reason: "ScudLee maps the AniDB entry (no AniList entry to cross-check)"}
		}
	}
	entries = append(entries, secondary...)
	for _, e := range entries {
		if e.TVDB != nil {
			tv[e.TVDB.SeriesID] = true
		}
	}
	var tvdbIDs []int
	for _, t := range sortedInts(keysOf(tv)) {
		if b.m.show(t).ok {
			tvdbIDs = append(tvdbIDs, t)
		}
	}
	// series reached only through leaves are mapped but not swept for specials or gaps
	var coreTVDB []int
	for _, t := range tvdbIDs {
		for _, e := range entries {
			if e.TVDB != nil && e.TVDB.SeriesID == t && e.Kind != "leaf" {
				coreTVDB = append(coreTVDB, t)
				break
			}
		}
	}

	// heuristic for anything still unmapped
	claimed := claimedOf(entries)
	order := append([]*Entry(nil), entries...)
	sort.SliceStable(order, func(i, j int) bool { return order[i].Origin == "anilist" && order[j].Origin != "anilist" })
	for _, e := range order {
		if e.TVDB == nil && !e.hasFlag("extra") {
			b.heuristicMap(e, coreTVDB, claimed)
			if e.TVDB != nil {
				for _, se := range e.TVDB.Episodes {
					claimed[claimKey{e.TVDB.SeriesID, se}] = true
				}
				e.Mapping = &MappingOutcome{Status: StatusHeuristic, Winner: "heuristic",
					Reason: fmt.Sprintf("matched by air date and title (similarity %s); no list maps it", pyFloat(e.TVDB.Heuristic.MinTitleSim))}
			}
		}
	}

	entries = b.mergeSameTitle(entries)
	entries = b.mergeDuplicates(entries)
	entries = b.splitFarApart(entries)
	entries = dropSecondaryDuplicates(entries)
	b.applyMappingOverrides(entries, opt.Overrides)
	b.st.recordMappings(entries)

	var sonarr map[int]map[SE]bool
	if src.Library != nil {
		if s, err := src.Library.SonarrEpisodes(ctx, tvdbIDs); err == nil {
			sonarr = s
		}
	}
	byKey := map[string]*Entry{}
	for _, e := range entries {
		byKey[e.Key] = e
	}
	// 6. validation and flags
	for _, e := range entries {
		b.m.validate(e, sonarr)
		crossCheckAnibridge(e)
		b.setFlags(e, byKey)
		e.Target = target(e)
		parent := byKey[e.ParentKey]
		e.Mainline = (e.Kind == "line" && !e.hasFlag("recap") && !containsStr(e.Key, "#")) ||
			(parent != nil && parent.Kind == "line" && !parent.hasFlag("recap"))
	}
	claimed = claimedOf(entries)
	// 5b. TVDB-only specials
	entries = append(entries, b.tvdbOnlyEntries(coreTVDB, claimed, entries)...)
	for _, e := range entries {
		b.findTMDb(e, byKey)
		if len(e.TMDbMovie) > 0 {
			e.TMDbCheck = nil
			var bad []int
			for _, t := range e.TMDbMovie {
				c := b.checkTMDb(t, e)
				e.TMDbCheck = append(e.TMDbCheck, c)
				if c.Rejected {
					bad = append(bad, t)
				}
			}
			if len(bad) > 0 {
				var keep []int
				for _, t := range e.TMDbMovie {
					if !containsInt(bad, t) {
						keep = append(keep, t)
					}
				}
				e.TMDbMovie = nonNilInts(keep)
				e.note("TMDb %s rejected (title and date do not match)", pyIntList(bad))
			}
		}
	}
	b.st.recordFlags(entries)

	gaps := b.regularGaps(coreTVDB, claimed)
	// 7-8. orders and overrides
	res := &Result{
		Franchise: f.Name, Slug: f.Slug, SeedAniList: seed, Generated: today, Config: f.config(),
		ReleaseOrder: b.releaseOrder(b.inOrders(entries)), WatchOrder: b.watchOrder(b.inOrders(entries), "watch"),
		StoryOrder: b.storyOrder(b.inOrders(entries), opt.Overrides), TVDBRegularUnclaimed: gaps,
		DatasetVersions: map[string]any{"anibridge": src.Lists.AnibridgeMeta},
	}
	for _, t := range tvdbIDs {
		sh := b.m.show(t)
		s := TVDBSeries{ID: t, Title: sh.title, SkyhookAniListIDs: sh.raw.AniListIDs, XEM: b.xemSummary(t)}
		if sonarr != nil {
			_, in := sonarr[t]
			s.InSonarr = bp(in)
		}
		res.TVDBSeriesList = append(res.TVDBSeriesList, s)
	}
	sorted := append([]*Entry(nil), entries...)
	sort.SliceStable(sorted, byDateRank(sorted))
	res.Entries = sorted
	res.ExcludedNeighbours = []Excluded{}
	for _, id := range sortedKeys(cr.Excluded) {
		res.ExcludedNeighbours = append(res.ExcludedNeighbours, cr.Excluded.m[id])
	}
	if res.TVDBSeriesList == nil {
		res.TVDBSeriesList = []TVDBSeries{}
	}
	applyOrderOverrides(res, opt.Overrides)
	res.Community = compareCommunity(res, opt.Community)
	b.st.recordOrders(res)
	res.Stages = b.st
	return res, nil
}

// inOrders leaves out the entries an override excludes.
func (b *builder) inOrders(entries []*Entry) []*Entry {
	if len(b.excluded) == 0 {
		return entries
	}
	var out []*Entry
	for _, e := range entries {
		if !b.excluded[e.Key] {
			out = append(out, e)
		}
	}
	return out
}

// applyMappingOverrides pins the mappings an override file gives.
func (b *builder) applyMappingOverrides(entries []*Entry, ov *Overrides) {
	if ov == nil {
		return
	}
	for _, fix := range ov.Mappings {
		eps, err := parseRangeLabel(fix.Episodes)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.Key != fix.Entry {
				continue
			}
			var specials []Special
			if e.TVDB != nil {
				specials = e.TVDB.AniDBSpecials
			}
			e.TVDB = &TVDBMap{SeriesID: fix.TVDB, Episodes: eps, Method: "override", AniDBSpecials: nonNilSpecials(specials),
				UnmappedAniDBEps: []int{}}
			prev := e.Mapping
			e.Mapping = &MappingOutcome{Status: StatusOverride, Winner: "override", Reason: "override: " + fix.Why}
			if prev != nil {
				e.Mapping.Candidates = prev.Candidates
			}
		}
	}
}

func keysOf(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sortedInts(x []int) []int {
	out := append([]int(nil), x...)
	sort.Ints(out)
	return out
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
