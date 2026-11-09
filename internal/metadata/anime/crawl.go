package anime

import (
	"context"
	"fmt"
	"sort"
)

// Stage 1: the typed AniList crawl.

var followRels = setOf("PREQUEL", "SEQUEL", "PARENT", "SIDE_STORY", "SUMMARY", "ALTERNATIVE", "SPIN_OFF",
	"COMPILATION", "CONTAINS")

var sideRels = setOf("PARENT", "SIDE_STORY", "SUMMARY", "COMPILATION", "CONTAINS")

var kindRank = map[string]int{"line": 0, "branch": 1, "side": 2, "leaf": 3}

// Excluded is a related work the crawl left out, and why.
type Excluded struct {
	AniListID int     `json:"anilist_id"`
	Title     *string `json:"title"`
	Format    *string `json:"format"`
	Why       string  `json:"why"`
}

// Crawl is stage 1's output.
type Crawl struct {
	Members  *omap[int, *Media]
	Excluded *omap[int, Excluded]
	How      map[int]string
	Kind     map[int]string
}

// CrawlOptions scope a crawl (franchises.json).
type CrawlOptions struct {
	MaxNodes  int
	LineDepth *int
	Scope     string // "" or "stem"
}

func fetchMedia(ctx context.Context, al AniList, ids []int) (map[int]*Media, []*Media, error) {
	if len(ids) == 0 {
		return map[int]*Media{}, nil, nil
	}
	list, err := al.Media(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	m := make(map[int]*Media, len(list))
	var order []*Media
	for _, x := range list {
		if _, dup := m[x.ID]; !dup {
			order = append(order, x)
		}
		m[x.ID] = x
	}
	return m, order, nil
}

func titleOnly(romaji *string) *Media {
	m := &Media{}
	m.Title.Romaji = romaji
	return m
}

// sameSeries reports whether any title of m starts with the stem of a reference's romaji or English
// title (synonyms make stems too loose).
func sameSeries(m *Media, refs ...*Media) bool {
	var mine []string
	for _, n := range namesOf(m) {
		mine = append(mine, normWords(n))
	}
	for _, r := range refs {
		for _, n := range []*string{r.Title.Romaji, r.Title.English} {
			s := stem(str(n))
			if len(s) < 4 {
				continue
			}
			for _, x := range mine {
				if len(x) >= len(s) && x[:len(s)] == s {
					return true
				}
			}
		}
	}
	return false
}

// crawlAniList is the typed BFS over AniList relations. Each node gets a kind: line (seed, PREQUEL ancestors,
// SEQUEL descendants), branch (sideways or same-stem ALTERNATIVE, own direction only), side (PARENT, SIDE_STORY,
// SUMMARY, COMPILATION, CONTAINS, expanding downwards), leaf (SPIN_OFF of a line node or OTHER with a back-edge,
// never expanded). A stronger path upgrades and re-expands a node.
func crawlAniList(ctx context.Context, al AniList, seed int, opt CrawlOptions) (*Crawl, error) {
	if opt.MaxNodes == 0 {
		opt.MaxNodes = 120
	}
	members := newOmap[int, *Media]()
	excluded := newOmap[int, Excluded]()
	how := map[int]string{seed: "seed"}
	kind := map[int]string{seed: "line"}
	direction := map[int]int{seed: 0}
	hops := map[int]int{seed: 0}
	todo := pySetOf([]int{seed})
	type cand struct {
		src int
		rel string
	}
	cands := newOmap[int, cand]()
	reexpand := newPySet()

	assign := func(nid int, nk string, d, h int, why string) bool {
		if k, ok := kind[nid]; ok && kindRank[k] <= kindRank[nk] {
			return false
		}
		kind[nid], direction[nid], hops[nid] = nk, d, h
		how[nid] = why
		if members.Has(nid) {
			reexpand.Add(nid)
		} else {
			todo.Add(nid)
		}
		return true
	}
	exclude := func(nid int, title, format *string, why string) {
		excluded.SetDefault(nid, Excluded{AniListID: nid, Title: title, Format: format, Why: why})
	}

	got, _, err := fetchMedia(ctx, al, []int{seed})
	if err != nil {
		return nil, err
	}
	seedMedia := got[seed]
	if seedMedia == nil {
		return nil, fmt.Errorf("anilist: seed %d not found", seed)
	}
	for (todo.Len() > 0 || cands.Len() > 0 || reexpand.Len() > 0) && members.Len() < opt.MaxNodes {
		fetch := map[int]bool{}
		for _, i := range todo.Items() {
			fetch[i] = true
		}
		for _, i := range cands.keys {
			fetch[i] = true
		}
		ids := make([]int, 0, len(fetch))
		for i := range fetch {
			ids = append(ids, i)
		}
		sort.Ints(ids)
		got, _, err := fetchMedia(ctx, al, ids)
		if err != nil {
			return nil, err
		}
		var newIDs []int
		for _, i := range todo.Items() {
			if got[i] != nil {
				newIDs = append(newIDs, i)
			}
		}
		for _, i := range reexpand.Items() {
			if members.Has(i) {
				newIDs = append(newIDs, i)
			}
		}
		reexpand = newPySet()
		for _, i := range newIDs {
			members.SetDefault(i, got[i])
		}
		for _, cid := range cands.keys {
			c := cands.m[cid]
			m := got[cid]
			if m == nil || members.Has(cid) {
				continue
			}
			var back []string
			for _, e := range m.Relations.Edges {
				if members.Has(e.Node.ID) && followRels[e.RelationType] {
					back = append(back, e.RelationType)
				}
			}
			if len(back) > 0 {
				members.Set(cid, m)
				if _, ok := kind[cid]; !ok {
					kind[cid] = "leaf" // OTHER-linked works are kept but not expanded (Nisekoimonogatari)
				}
				if _, ok := direction[cid]; !ok {
					direction[cid] = 0
				}
				if _, ok := hops[cid]; !ok {
					hops[cid] = hops[c.src]
				}
				how[cid] = fmt.Sprintf("%s from %d, back-edge %s", c.rel, c.src, back[0])
				newIDs = append(newIDs, cid)
			} else {
				excluded.Set(cid, Excluded{AniListID: cid, Title: m.Title.Romaji, Format: m.Format,
					Why: fmt.Sprintf("%s from %d, no franchise back-edge", c.rel, c.src)})
			}
		}
		todo, cands = newPySet(), newOmap[int, cand]()
		altPending := newOmap[int, int]()
		order := pySetOf(newIDs).Items()
		sort.SliceStable(order, func(a, b int) bool { return kindRank[kind[order[a]]] < kindRank[kind[order[b]]] })
		for _, mid := range order {
			k := kind[mid]
			if k == "leaf" {
				continue
			}
			mm, _ := members.Get(mid)
			for _, e := range mm.Relations.Edges {
				n, rel := e.Node, e.RelationType
				nid := n.ID
				if n.Type != "ANIME" || nid == mid {
					continue
				}
				_, known := kind[nid]
				switch {
				case rel == "PREQUEL" || rel == "SEQUEL":
					d := 1
					if rel == "PREQUEL" {
						d = -1
					}
					var nk string
					switch k {
					case "line":
						if dir := direction[mid]; dir == 0 || dir == d {
							outDepth := opt.LineDepth != nil && hops[mid]+1 > *opt.LineDepth
							outStem := opt.Scope == "stem" && !sameSeries(titleOnly(n.Title.Romaji), seedMedia)
							if outDepth || outStem {
								if !known {
									why := "series stem"
									if outDepth {
										why = "line_depth"
									}
									exclude(nid, n.Title.Romaji, n.Format,
										fmt.Sprintf("%s of %d, outside the configured scope (%s)", rel, mid, why))
								}
								continue
							}
							nk = "line"
						} else {
							nk = "branch" // sideways from the line
						}
					case "branch":
						if d != direction[mid] {
							continue
						}
						if opt.Scope == "stem" && !sameSeries(titleOnly(n.Title.Romaji), mm, seedMedia) {
							if !known {
								exclude(nid, n.Title.Romaji, n.Format,
									fmt.Sprintf("%s of %d, outside the configured scope (series stem)", rel, mid))
							}
							continue
						}
						nk = "branch"
					default: // side: follow movie/OVA chains, not into other TV series
						if str(n.Format) == "TV" {
							continue
						}
						nk = "side"
					}
					assign(nid, nk, d, hops[mid]+1, fmt.Sprintf("%s from %d", rel, mid))
				case sideRels[rel]:
					if rel == "PARENT" && k == "side" {
						continue // side nodes only expand downwards; PARENT would climb into another series
					}
					assign(nid, "side", 0, hops[mid], fmt.Sprintf("%s from %d", rel, mid))
				case rel == "ALTERNATIVE":
					if !known {
						altPending.SetDefault(nid, mid)
					}
				case rel == "SPIN_OFF":
					if k == "line" {
						assign(nid, "leaf", 0, hops[mid], fmt.Sprintf("SPIN_OFF from %d (not expanded)", mid))
					} else if !known {
						exclude(nid, n.Title.Romaji, n.Format, fmt.Sprintf("SPIN_OFF of non-line node %d", mid))
					}
				case rel == "OTHER":
					if !known {
						cands.SetDefault(nid, cand{mid, rel})
					}
				case rel == "CHARACTER":
					if !known {
						exclude(nid, n.Title.Romaji, n.Format, fmt.Sprintf("CHARACTER from %d (crossover or cameo)", mid))
					}
				}
			}
		}
		if altPending.Len() > 0 {
			got, _, err := fetchMedia(ctx, al, sortedKeys(altPending))
			if err != nil {
				return nil, err
			}
			seedM, _ := members.Get(seed)
			for _, nid := range altPending.keys {
				src := altPending.m[nid]
				m := got[nid]
				if _, known := kind[nid]; m == nil || known {
					continue
				}
				srcM, _ := members.Get(src)
				if sameSeries(m, seedM) || (opt.Scope != "stem" && sameSeries(m, srcM)) {
					assign(nid, "branch", 1, hops[src], fmt.Sprintf("ALTERNATIVE from %d (same series stem)", src))
				} else {
					exclude(nid, m.Title.Romaji, m.Format,
						fmt.Sprintf("ALTERNATIVE from %d, different series (alternate universe or other branch)", src))
				}
			}
		}
		kept := newOmap[int, cand]()
		for _, k := range cands.keys {
			if _, known := kind[k]; !known && !excluded.Has(k) {
				kept.Set(k, cands.m[k])
			}
		}
		cands = kept
	}
	for _, k := range excluded.Keys() {
		if members.Has(k) {
			excluded.Delete(k)
		}
	}
	return &Crawl{Members: members, Excluded: excluded, How: how, Kind: kind}, nil
}

func sortedKeys[V any](o *omap[int, V]) []int {
	ks := o.Keys()
	sort.Ints(ks)
	return ks
}
