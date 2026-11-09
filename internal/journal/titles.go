package journal

import (
	"fmt"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/provider"
)

// titleInfo is a title with what the checks need: runtime, languages, names, and for a series its episodes.
type titleInfo struct {
	ref    TitleRef
	t      decide.Title
	names  []string // name, original title, aliases, scene mapping names
	eps    map[int64]episodeInfo
	counts map[int]int // episodes per season (TVDB numbering)
	maxS   int
	total  int
	scene  bool // the series has scene numbering or scene mappings: numbers in names may differ from TVDB's
}

type episodeInfo struct {
	season, number, runtime int
}

func (ti *titleInfo) labels(ids []int64) []string {
	var out []string
	for _, id := range ids {
		if ep, ok := ti.eps[id]; ok {
			out = append(out, fmt.Sprintf("S%02dE%02d", ep.season, ep.number))
		}
	}
	sort.Strings(out)
	return out
}

func (b *builder) title(id int64) (*titleInfo, error) {
	if id == 0 {
		return nil, nil
	}
	b.j.tmu.Lock()
	ti, ok := b.titles[id]
	b.j.tmu.Unlock()
	if ok {
		return ti, nil
	}
	t, err := decide.LoadTitle(b.ctx, b.db, id)
	if err != nil {
		if strings.HasPrefix(err.Error(), "no title") {
			b.setTitle(id, nil)
			return nil, nil
		}
		return nil, err
	}
	ti = &titleInfo{t: t, ref: TitleRef{ID: t.ID, Name: t.Name, Year: t.Year, Kind: string(t.Kind)},
		eps: map[int64]episodeInfo{}, counts: map[int]int{}, scene: t.UseSceneNumbering}
	ti.names = append([]string{t.Name, t.OriginalTitle}, t.Aliases...)
	if t.Kind == "series" {
		rows, err := b.db.QueryContext(b.ctx, `SELECT id, season, number, COALESCE(runtime_minutes, 0) FROM episode
			WHERE title_id = ?`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var eid int64
			var ep episodeInfo
			if err := rows.Scan(&eid, &ep.season, &ep.number, &ep.runtime); err != nil {
				rows.Close()
				return nil, err
			}
			ti.eps[eid] = ep
			if ep.number > ti.counts[ep.season] {
				ti.counts[ep.season] = ep.number
			}
			if ep.season > ti.maxS {
				ti.maxS = ep.season
			}
			if ep.season > 0 {
				ti.total++
			}
		}
		rows.Close()
		rows, err = b.db.QueryContext(b.ctx, `SELECT DISTINCT name FROM title_scene_mapping WHERE title_id = ?`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return nil, err
			}
			ti.names = append(ti.names, n)
			ti.scene = true
		}
		rows.Close()
	}
	b.setTitle(id, ti)
	return ti, nil
}

func (b *builder) setTitle(id int64, ti *titleInfo) {
	b.j.tmu.Lock()
	b.titles[id] = ti
	b.j.tmu.Unlock()
}

// runtimeMinutes is the runtime of what a release holds: the movie's, or the episodes' (the series runtime for an
// episode without one). 0 when unknown.
func (ti *titleInfo) runtimeMinutes(eps []int64) int {
	if ti.t.Kind != "series" {
		return ti.t.Runtime
	}
	total := 0
	for _, id := range eps {
		ep, ok := ti.eps[id]
		switch {
		case ok && ep.runtime > 0:
			total += ep.runtime
		case ti.t.Runtime > 0:
			total += ti.t.Runtime
		default:
			return 0
		}
	}
	return total
}

// clean makes free text safe to return: any word that looks like a URL or carries a key, passkey, token or magnet
// is replaced (the arr history's own test, internal/arr), and control characters are dropped.
func clean(s string) string {
	if s == "" || !arr.UnsafeValue(s) {
		return strings.Map(func(r rune) rune {
			if r < 0x20 && r != '\t' {
				return ' '
			}
			return r
		}, s)
	}
	f := strings.Fields(s)
	for i, w := range f {
		if arr.UnsafeValue(w) {
			f[i] = "[redacted]"
		}
	}
	out := strings.Join(f, " ")
	if arr.UnsafeValue(out) { // a secret split across words ("token= abc"), or a "key" word next to its value
		return "[redacted]"
	}
	return out
}

// cleanMessage is clean for provider and error messages: provider.CleanMessage first (HTML, e-mail addresses,
// length).
func cleanMessage(s string) string { return clean(provider.CleanMessage(s)) }

func cleanCandidate(c *snapshot.Candidate) {
	c.Release, c.Indexer = clean(c.Release), clean(c.Indexer)
	for i := range c.Rejections {
		c.Rejections[i].Message = clean(c.Rejections[i].Message)
	}
	if c.UpgradeWhy != "" {
		c.UpgradeWhy = clean(c.UpgradeWhy)
	}
}
