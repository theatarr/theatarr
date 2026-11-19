package rss

import (
	"context"
	"encoding/json"
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
	for _, k := range sortedKeys(r.active) {
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

// seriesIndex finds series by the clean names a release may use: the title and its scene mappings. It also keeps
// each candidate version's series as loaded for the run's first item of it (loaded, by version id).
type seriesIndex struct {
	byClean map[string][]int64
	byTVDB  map[int]int64
	// byIMDb is every catalog series by normalised IMDb id: the fallback for an item with no TVDB id whose name
	// finds none, when the id names one series and that series is a candidate.
	byIMDb map[string][]int64
	loaded map[int64]decide.Title
}

func (r *Runner) seriesIndex(ctx context.Context, cands map[int64][]seriesCandidate, all map[int64]decide.Title) (*seriesIndex, error) {
	ix := &seriesIndex{byClean: map[string][]int64{}, byTVDB: map[int]int64{}, byIMDb: map[string][]int64{},
		loaded: map[int64]decide.Title{}}
	if len(cands) == 0 {
		return ix, nil
	}
	// The scene mapping names of every candidate series, in one query.
	ids := make([]int64, 0, len(cands))
	for id := range cands {
		ids = append(ids, id)
	}
	list, _ := json.Marshal(ids)
	scene := map[int64][]string{}
	// A "Limited matching" alias (Sonarr limits it to some releases; the regex is not evaluated) finds no series.
	rows, err := r.st.Reader().QueryContext(ctx, `SELECT title_id, name FROM title_scene_mapping
		WHERE title_id IN (SELECT value FROM json_each(?)) AND comment != 'Limited matching' ORDER BY title_id, source, rowid`,
		string(list))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return nil, err
		}
		scene[id] = append(scene[id], name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for id, cs := range cands {
		t := cs[0].title
		names := append([]string{t.Name}, scene[id]...)
		for _, n := range names {
			if c := sonarr.CleanSeriesTitle(n); c != "" && !slices.Contains(ix.byClean[c], id) {
				ix.byClean[c] = append(ix.byClean[c], id)
			}
		}
		if t.TVDB > 0 {
			ix.byTVDB[t.TVDB] = id
		}
	}
	for id, t := range all {
		if imdb := sonarr.NormalizeImdbID(t.IMDb); imdb != "" {
			ix.byIMDb[imdb] = append(ix.byIMDb[imdb], id)
		}
	}
	return ix, nil
}

// decideSeriesItem maps an RSS item to our series (the indexer's TVDB id, else the parsed series title against the
// titles and scene mappings, else, with no TVDB id, the one series with the item's IMDb id) and decides it for every candidate version with the RSS path (no search criteria).
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
		// No TVDB id and no series by name: the item's IMDb id, when exactly one series has it (DMM sends IMDb
		// ids, no TVDB). The decision checks the episode as usual.
		if imdb := sonarr.NormalizeImdbID(rel.IMDb); len(ids) == 0 && rel.TVDB == 0 && imdb != "" && len(ix.byIMDb[imdb]) == 1 {
			if len(cands[ix.byIMDb[imdb][0]]) > 0 {
				ids = ix.byIMDb[imdb]
			}
		}
	}
	matched := false
	for _, id := range ids {
		for _, c := range cands[id] {
			matched = true
			t, ok := ix.loaded[c.version.ID]
			if !ok {
				t = c.title
				if err := decide.LoadSeriesVersion(ctx, r.st.Reader(), &t, c.version.ID); err != nil {
					return matched, err
				}
				ix.loaded[c.version.ID] = t
			}
			d, err := r.decide(ctx, ranked, t, c.version, nil, sum)
			if err != nil {
				return matched, err
			}
			if d == nil {
				continue
			}
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
