package shadow

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	dshadow "github.com/theatarr/theatarr/internal/decide/shadow"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// Series in the runner. A Sonarr grab is one history row per episode; the rows of one release (same series,
// download id and title, written within two minutes) are one shadow grab, keyed by the first row's history id.
// The arr's release is decided with the search the arr had (its episodes; none for an RSS grab), our own search asks
// the version's indexers for the grab's seasons through the planner, and every result is decided for the grab's
// episodes, each against its own file at grab time.

// grabSeries is grab for a series version.
func (r *Runner) grabSeries(ctx context.Context, p grabPayload, key string, force bool, res *GrabResult) (*GrabResult, error) {
	db := r.st.Reader()
	g, source, err := dshadow.LoadSeriesGrab(ctx, db, p.Instance, p.HistoryID)
	if err != nil {
		return nil, err
	}
	if !force {
		var state string
		err := db.QueryRowContext(ctx, `SELECT state FROM shadow_grab WHERE instance = ? AND history_id = ?`,
			p.Instance, g.HistoryID).Scan(&state)
		if err == nil && state == "done" {
			res.Skipped = "already done"
			return res, nil
		}
	}
	p.HistoryID = g.HistoryID
	res.History = g.HistoryID
	res.Title = fmt.Sprintf("%s (%d)", g.Title.Name, g.Title.Year)
	v, err := decide.LoadVersion(ctx, db, r.pol, g.Title.ID, key)
	if err != nil {
		return nil, err
	}
	arrRel := g.Release
	arrRel.InfoHash = strings.ToLower(g.InfoHash)
	arrD, err := r.eng.DecideSearch(ctx, arrRel, g.Title, v, g.Search)
	if err != nil {
		return nil, err
	}
	withoutOurs(arrD)
	decidedRel := arrRel
	arrRel.Indexer = g.Indexer
	res.Arr = summarize(arrD, arrRel, true)

	// Our own search: the grab's seasons.
	var found *cachedSearch
	searched := false
	var notes []string
	if p.NoSearch || !r.set.Search || r.idx == nil {
		notes = append(notes, "search off")
	} else {
		it, cacheKey := seriesSearchTitle(&g.Title, g.Episodes)
		late := r.now().Sub(g.Date) > maxDefer
		var serr error
		found, serr = r.searchTitle(ctx, it, cacheKey, key)
		switch {
		case serr == nil:
			searched = true
		case late || r.q == nil || force:
			notes = append(notes, "not searched: "+serr.Error())
		default:
			return r.deferGrab(ctx, p, g, key, res, serr)
		}
	}
	if found != nil {
		notes = append(notes, found.notes...)
	}

	// Our criteria for the search we ran: the grab's episodes, as a season search when it took several or a pack.
	ours := g.Search
	if ours == nil {
		ours = dshadow.ArrSearchFor(&g.Title, g.Episodes, "Search", g.ReleaseType)
	}
	var ds []*decide.Decision
	var searchedRels []indexer.Release
	rels := map[*decide.Decision]indexer.Release{}
	// The arr's release is decided against stored standing whether or not our search ran or found anything;
	// a search that did run has stored its answers first, and cached results replay nothing.
	ctx, err = decide.WithStoredSeaDex(ctx, r.st.Reader())
	if err != nil {
		return nil, err
	}
	searchRels := r.withArrCacheEvidence(ctx, found, &decidedRel)
	arrD, err = r.eng.DecideSearch(ctx, decidedRel, g.Title, v, g.Search)
	if err != nil {
		return nil, err
	}
	r.recacheArr(arrD, v, arrRel, decidedRel)
	withoutOurs(arrD)
	res.Arr = summarize(arrD, arrRel, true)
	if found != nil {
		vp := r.pol.Versions[key]
		for i, rel := range found.releases {
			if !slices.Contains(vp.Indexers, rel.Indexer) {
				continue
			}
			searchedRels = append(searchedRels, searchRels[i]) // with cache evidence, as the main ranking
			d, err := r.eng.DecideSearch(ctx, decide.Release{Release: searchRels[i]}, g.Title, v, ours)
			if err != nil {
				return nil, err
			}
			ds = append(ds, d)
			rels[d] = rel
			if d.Accepted {
				res.Accepted++
			}
		}
	}
	if found != nil {
		r.recordCeiling(ctx, g.Title.ID, key, "", "shadow_search", ds, rels, found)
	}
	res.Results = len(ds)
	cands := ds
	if arrD.Accepted {
		cands = append(slices.Clone(ds), arrD)
	}
	var kept []Decided
	foundArr := false
	ranked := decide.Rank(cands)
	if arrD.Accepted {
		rels[arrD] = arrCandidate(arrRel)
	}
	for _, d := range ranked {
		if d == arrD {
			if d.Accepted && res.Pick == nil {
				p := res.Arr
				res.Pick = &p
			}
			continue
		}
		s := summarize(d, decide.Release{Release: rels[d]}, false)
		if sameRelease(&s, &res.Arr) {
			foundArr = true
		}
		if d.Accepted && res.Pick == nil {
			p := s
			res.Pick = &p
		}
		if len(kept) < r.set.KeepResults {
			kept = append(kept, s)
		}
	}
	res.Searched = searched
	res.Note = strings.Join(notes, "; ")
	// The same grab with the arr's parser, for a version on another Sonarr parser mode (parsermode.go).
	if res.Arr.Parser, err = r.seriesParserCheck(ctx, v, g.Title, decidedRel, arrRel, g.Search, ours, searchedRels,
		searched); err != nil {
		return nil, err
	}
	if err := r.store(ctx, g, key, source, res, kept, foundArr); err != nil {
		return nil, err
	}
	r.grabPlan(ctx, res.GrabID, g.Title.ID, v, ranked, rels)
	r.m.grabs.WithLabelValues(res.Outcome.Bucket).Inc()
	r.log.Info("shadow grab", "instance", p.Instance, "history_id", p.HistoryID, "title", res.Title, "version", key,
		"episodes", len(g.Episodes), "bucket", res.Outcome.Bucket, "reason", res.Outcome.Reason, "searched", searched,
		"results", res.Results)
	if err := r.RefreshMetrics(ctx); err != nil {
		r.log.Warn("shadow metrics", "err", err)
	}
	return res, nil
}

// seriesSearchTitle is the planner's title for a series grab: the seasons of its episodes (TVDB numbering; Sonarr
// searches anime by scene numbering too, which the indexer adapters do not take yet), the scene mapping names as
// aliases, and the season sizes for Torrentio's pack sizes. The cache key holds the title and seasons.
func seriesSearchTitle(t *decide.Title, episodes []int64) (indexer.Title, string) {
	it := indexer.Title{Kind: policy.KindSeries, Anime: t.SeriesType == "anime" || t.Anime, Title: t.Name, Year: t.Year,
		IMDb: t.IMDb, TVDB: t.TVDB, EpisodeCounts: map[int]int{}}
	for _, m := range t.SceneMappings {
		if !slices.Contains(it.Aliases, m.Title) {
			it.Aliases = append(it.Aliases, m.Title)
		}
	}
	want := map[int64]bool{}
	for _, id := range episodes {
		want[id] = true
	}
	for _, e := range t.Episodes {
		it.EpisodeCounts[e.Season]++
		if want[e.ID] && !slices.Contains(it.Seasons, e.Season) {
			it.Seasons = append(it.Seasons, e.Season)
		}
	}
	sort.Ints(it.Seasons)
	key := fmt.Sprint(t.ID)
	for _, s := range it.Seasons {
		key += fmt.Sprintf(":%d", s)
	}
	return it, key
}

// ---- RSS for series ----

// seriesCandidate is a series version an RSS item may be for.
type seriesCandidate struct {
	title   decide.Title // without episodes; loaded per decision
	version decide.Version
}

// seriesCandidates includes cutoff-met series: a later episode can air without a metadata refresh.
func (r *Runner) seriesCandidates(ctx context.Context) (map[int64][]seriesCandidate, map[int64]decide.Title, error) {
	var keys []string
	for _, k := range sortedKeys(r.rssVersions()) {
		if r.pol.Versions[k].Kind == policy.KindSeries {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, nil, nil
	}
	series, err := decide.LoadSeries(ctx, r.st.Reader())
	if err != nil {
		return nil, nil, err
	}
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	rows, err := r.st.Reader().QueryContext(ctx, `SELECT id, title_id, policy_key FROM version
		WHERE monitored = 1 AND state IN ('wanted', 'filled', 'cutoff_met') AND policy_key IN (?`+strings.Repeat(", ?", len(keys)-1)+`)`,
		args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := map[int64][]seriesCandidate{}
	for rows.Next() {
		var id, tid int64
		var key string
		if err := rows.Scan(&id, &tid, &key); err != nil {
			return nil, nil, err
		}
		t, ok := series[tid]
		if !ok {
			continue
		}
		out[tid] = append(out[tid], seriesCandidate{title: t, version: decide.Version{ID: id, Key: key, Policy: r.pol.Versions[key]}})
	}
	return out, series, rows.Err()
}

// seriesIndex finds series by the clean names a release may use: the title and its scene mappings.
type seriesIndex struct {
	byClean map[string][]int64
	byTVDB  map[int]int64
}

func (r *Runner) seriesIndex(ctx context.Context, cands map[int64][]seriesCandidate) (*seriesIndex, error) {
	ix := &seriesIndex{byClean: map[string][]int64{}, byTVDB: map[int]int64{}}
	for id, cs := range cands {
		t := cs[0].title
		if err := decide.LoadSeriesVersion(ctx, r.st.Reader(), &t, 0); err != nil {
			return nil, err
		}
		names := []string{t.Name}
		for _, m := range t.SceneMappings {
			names = append(names, m.Title)
		}
		for _, n := range names {
			if c := sonarr.CleanSeriesTitle(n); c != "" && !slices.Contains(ix.byClean[c], id) {
				ix.byClean[c] = append(ix.byClean[c], id)
			}
		}
		if t.TVDB > 0 {
			ix.byTVDB[t.TVDB] = id
		}
	}
	return ix, nil
}

// decideSeriesItem maps an RSS item to our series (the indexer's TVDB id, else the parsed series title against the
// titles and scene mappings) and decides it for every candidate version with the RSS path (no search criteria).
// ranked is rel with the cache evidence the decision ranks by, as for decideItem.
func (r *Runner) decideSeriesItem(ctx context.Context, rel, ranked indexer.Release, cands map[int64][]seriesCandidate,
	ix *seriesIndex, sum *RSSSummary) (bool, error) {
	ctx = decide.WithParseMemo(ctx)
	var ids []int64
	if id, ok := ix.byTVDB[rel.TVDB]; ok && rel.TVDB > 0 {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		p := decide.ParseSeriesRelease(ctx, sonarr.V4, rel.Title)
		if p == nil {
			return false, nil
		}
		titles := []string{p.SeriesTitle}
		if p.SeriesTitleInfo != nil {
			titles = append(titles, p.SeriesTitleInfo.AllTitles...)
			titles = append(titles, p.SeriesTitleInfo.TitleWithoutYear)
		}
		for _, n := range titles {
			for _, id := range ix.byClean[sonarr.CleanSeriesTitle(n)] {
				if !slices.Contains(ids, id) {
					ids = append(ids, id)
				}
			}
		}
	}
	matched := false
	for _, id := range ids {
		for _, c := range cands[id] {
			matched = true
			t := c.title
			if err := decide.LoadSeriesVersion(ctx, r.st.Reader(), &t, c.version.ID); err != nil {
				return matched, err
			}
			d, err := r.eng.Decide(ctx, decide.Release{Release: ranked}, t, c.version, nil)
			if err != nil {
				return matched, err
			}
			r.recordCeiling(ctx, t.ID, c.version.Key, "rss", "rss", []*decide.Decision{d}, map[*decide.Decision]indexer.Release{d: rel}, nil)
			sum.Decided++
			if !d.Accepted || d.MatchType == decide.MatchNone {
				sum.remember = sum.remember && permanentRSS(d)
				continue
			}
			if r.active[c.version.Key] {
				sum.remember = false
				sum.collect(candidate{title: t, version: c.version}, rel, d)
				continue
			}
			pc, err := r.rssParserCheck(ctx, rel, t, c.version)
			if err != nil {
				return matched, err
			}
			picked, err := r.pick(ctx, rel, candidate{title: t, version: c.version, parser: pc}, d)
			if err != nil {
				return matched, err
			}
			if picked {
				sum.Picks++
			}
		}
	}
	return matched, nil
}

// seriesGrabRows is newGrabs' query for a Sonarr instance: grabs after the cursor, one per release (the first row of
// each group; a row whose group began before the cursor was handled with it).
const seriesGrabRows = `SELECT h.arr_id, h.date_at, COALESCE(h.version_id, 0), h.title_id IS NOT NULL
	FROM arr_history h WHERE h.instance = ? AND h.arr_id > ? AND h.event_type = 'grabbed'
	AND NOT EXISTS (SELECT 1 FROM arr_history p WHERE p.instance = h.instance AND p.event_type = 'grabbed'
		AND p.arr_title_id = h.arr_title_id AND COALESCE(p.download_id, '') = COALESCE(h.download_id, '')
		AND p.source_title = h.source_title AND p.arr_id < h.arr_id AND p.date_at <= h.date_at
		AND p.date_at >= h.date_at - 120000)
	ORDER BY h.arr_id`

// isSeriesInstance is whether an import.arr instance feeds a series version.
func (r *Runner) isSeriesInstance(inst string) bool {
	if r.pol.Import == nil || r.pol.Import.Arr[inst] == nil {
		return false
	}
	v := r.pol.Versions[r.pol.Import.Arr[inst].Version]
	return v != nil && v.Kind == policy.KindSeries
}
