package rss

import (
	"context"
	"slices"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

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
		p := decide.ParseSeriesRelease(ctx, rel.Title)
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
			r.recordCeiling(ctx, t.ID, c.version.Key, "rss", "rss", []*decide.Decision{d}, map[*decide.Decision]indexer.Release{d: rel})
			sum.Decided++
			if !d.Accepted || d.MatchType == decide.MatchNone {
				sum.remember = sum.remember && permanentRSS(d)
				continue
			}
			sum.remember = false
			sum.collect(candidate{title: t, version: c.version}, rel, d)
		}
	}
	return matched, nil
}
