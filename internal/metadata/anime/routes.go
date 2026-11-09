package anime

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Routes, line position and alternate cuts: the overrides open data cannot replace (docs/design/anime.md
// "Overrides").
//
// routes lists route keys in viewing order. A route key is either an alternate's root (al:356, the 2006 series: the
// block is the alternate and every row the watch order routed to it, its specials and recaps included) or an entry on
// the main order (al:19603, Unlimited Blade Works: the block runs from that row to the next route key on the main
// order, or to the end). The listed blocks become one sequence in the watch and story orders, no longer optional
// alternates; the main-order rows outside every block (Fate/Zero and its side stories) are "the line", placed before
// the routes (line_position: first, the default) or after them (last). Alternates not listed stay alternates. The
// release order keeps its dates and only marks each row with its route.
//
// alt_cuts maps an alternate cut that no source numbers (Re:Zero's Director's Cut) onto TVDB episodes of one series:
// map keys end in the cut's episode number (DC1, DC2, ...); values are an episode or a list of them. A release of the
// cut is matched by the cut's name (or match, a regular expression) and its numbers map through the table
// (internal/decide). The cut's own entry, when given, is marked in the orders with the cut's TVDB range.

// RouteResult reports what the routes override did to an order.
type RouteResult struct {
	Applied []string `json:"applied,omitempty"`
	Failed  []string `json:"failed,omitempty"`
}

// AltCutMap is an alternate cut resolved for storage and decisions.
type AltCutMap struct {
	Name     string       `json:"name"`
	Entry    string       `json:"entry,omitempty"`
	SeriesID int          `json:"tvdb"`
	Match    string       `json:"match,omitempty"`
	Episodes map[int][]SE `json:"episodes"` // the cut's episode number -> TVDB episodes
}

// Matches reports whether a release title names the cut: Match as a case-insensitive regular expression, else
// every word of the name (case and punctuation ignored).
func (c *AltCutMap) Matches(title string) bool {
	if c.Match != "" {
		re, err := regexp.Compile("(?i)" + c.Match)
		return err == nil && re.MatchString(title)
	}
	words := wordsOf(c.Name)
	if len(words) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, w := range wordsOf(title) {
		have[w] = true
	}
	for _, w := range words {
		if !have[w] {
			return false
		}
	}
	return true
}

var nonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func wordsOf(s string) []string {
	s = strings.ReplaceAll(strings.ToLower(s), "'", "")
	return strings.Fields(nonWord.ReplaceAllString(s, " "))
}

// Range is the cut's TVDB episodes as a ranges() label.
func (c *AltCutMap) Range() string {
	var all []SE
	seen := map[SE]bool{}
	for _, n := range c.Numbers() {
		for _, se := range c.Episodes[n] {
			if !seen[se] {
				seen[se] = true
				all = append(all, se)
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].less(all[j]) })
	return ranges(all)
}

// Numbers lists the cut's episode numbers in order.
func (c *AltCutMap) Numbers() []int {
	out := make([]int, 0, len(c.Episodes))
	for n := range c.Episodes {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

var trailingNum = regexp.MustCompile(`(\d+)$`)
var seOnly = regexp.MustCompile(`^S(\d+)E(\d+)$`)

// resolve validates an alt cut and turns its map into numbers and episodes.
func (a AltCut) resolve() (*AltCutMap, []string) {
	var errs []string
	out := &AltCutMap{Name: a.Name, Entry: a.Entry, SeriesID: a.TVDB, Match: a.Match, Episodes: map[int][]SE{}}
	if strings.TrimSpace(a.Name) == "" {
		errs = append(errs, "name: required")
	}
	if a.TVDB <= 0 {
		errs = append(errs, "tvdb: want the TVDB series id the map numbers into")
	}
	if a.Match != "" {
		if _, err := regexp.Compile(a.Match); err != nil {
			errs = append(errs, fmt.Sprintf("match: %v", err))
		}
	}
	if len(a.Map) == 0 {
		errs = append(errs, "map: no episodes")
	}
	keys := make([]string, 0, len(a.Map))
	for k := range a.Map {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		m := trailingNum.FindStringSubmatch(k)
		if m == nil {
			errs = append(errs, fmt.Sprintf("map[%s]: the key must end in the cut's episode number (DC1)", k))
			continue
		}
		n, _ := strconv.Atoi(m[1])
		if _, dup := out.Episodes[n]; dup {
			errs = append(errs, fmt.Sprintf("map[%s]: episode %d listed twice", k, n))
			continue
		}
		var vals []any
		switch v := a.Map[k].(type) {
		case []any:
			vals = v
		default:
			vals = []any{v}
		}
		var eps []SE
		for _, v := range vals {
			s, _ := v.(string)
			sm := seOnly.FindStringSubmatch(strings.TrimSpace(s))
			if sm == nil {
				errs = append(errs, fmt.Sprintf("map[%s]: %v is not SxxEyy", k, v))
				continue
			}
			season, _ := strconv.Atoi(sm[1])
			ep, _ := strconv.Atoi(sm[2])
			eps = append(eps, SE{season, ep})
		}
		if len(eps) == 0 {
			errs = append(errs, fmt.Sprintf("map[%s]: no episodes", k))
			continue
		}
		out.Episodes[n] = eps
	}
	return out, errs
}

// AltCutMaps resolves the overrides' alternate cuts (they were validated when parsed).
func (ov *Overrides) AltCutMaps() []*AltCutMap {
	if ov == nil {
		return nil
	}
	var out []*AltCutMap
	for _, a := range ov.AltCuts {
		if m, errs := a.resolve(); len(errs) == 0 {
			out = append(out, m)
		}
	}
	return out
}

// applyRoutes reorders one order's main rows and alternates by the routes override (see the comment at the top).
func applyRoutes(main, alts []*Row, ov *Overrides) ([]*Row, []*Row, RouteResult) {
	var res RouteResult
	if ov == nil || len(ov.Routes) == 0 {
		return main, alts, res
	}
	routeSet := map[string]bool{}
	for _, k := range ov.Routes {
		routeSet[k] = true
	}
	inBlock := map[*Row]bool{}
	blocks := map[string][]*Row{}
	for _, k := range ov.Routes {
		var block []*Row
		// an alternate's root and its routed rows
		for _, r := range alts {
			if r.Key == k && r.Route == "" {
				block = append(block, r)
			}
		}
		if len(block) > 0 {
			for _, r := range alts {
				if r.Route == k {
					block = append(block, r)
				}
			}
			var keep []*Row
			for _, r := range alts {
				if !containsRow(block, r) {
					keep = append(keep, r)
				}
			}
			alts = keep
			for _, r := range block {
				r.Role, r.Optional = "route", bp(false)
				if r.Route == "" {
					r.Route = k
				}
			}
		} else {
			// a stretch of the main order
			start := -1
			for i, r := range main {
				if r.Key == k && !inBlock[r] {
					start = i
					break
				}
			}
			if start >= 0 {
				for i := start; i < len(main); i++ {
					r := main[i]
					if i > start && routeSet[r.Key] {
						break
					}
					if inBlock[r] {
						continue
					}
					block = append(block, r)
				}
				for _, r := range block {
					r.Route = k
				}
			}
		}
		if len(block) == 0 {
			res.Failed = append(res.Failed, k)
			continue
		}
		for _, r := range block {
			inBlock[r] = true
		}
		blocks[k] = block
		res.Applied = append(res.Applied, k)
	}
	var line []*Row
	for _, r := range main {
		if !inBlock[r] {
			line = append(line, r)
		}
	}
	var routed []*Row
	for _, k := range ov.Routes {
		routed = append(routed, blocks[k]...)
	}
	out := make([]*Row, 0, len(line)+len(routed))
	if ov.LinePosition == "last" {
		out = append(append(out, routed...), line...)
	} else {
		out = append(append(out, line...), routed...)
	}
	for i, r := range out {
		r.N = ip(i + 1)
	}
	if alts == nil {
		alts = []*Row{}
	}
	return out, alts, res
}

func containsRow(rs []*Row, r *Row) bool {
	for _, x := range rs {
		if x == r {
			return true
		}
	}
	return false
}

// markRoutes copies the route of each key in the reordered watch order onto the release order.
func markRoutes(release []*Row, from []*Row) {
	by := map[string]string{}
	for _, r := range from {
		if r.Route != "" && by[r.Key] == "" {
			by[r.Key] = r.Route
		}
	}
	for _, r := range release {
		if k := by[r.Key]; k != "" {
			r.Route = k
		}
	}
}

// markAltCuts marks the rows of each cut's own entry with the cut's name and TVDB range.
func markAltCuts(cuts []*AltCutMap, orders ...[]*Row) {
	for _, c := range cuts {
		if c.Entry == "" {
			continue
		}
		for _, rows := range orders {
			for _, r := range rows {
				if r.Key == c.Entry {
					r.AltCut = c.Name
					r.AltCutTVDB = fmt.Sprintf("%d %s", c.SeriesID, c.Range())
				}
			}
		}
	}
}

// applyOrderOverrides applies routes, line position and alternate cuts to the three orders of a result.
func applyOrderOverrides(res *Result, ov *Overrides) {
	if ov == nil {
		return
	}
	if len(ov.Routes) > 0 {
		var main, alts []*Row
		for _, r := range res.WatchOrder {
			if r.Role == "alternate" {
				alts = append(alts, r)
			} else {
				main = append(main, r)
			}
		}
		main, alts, wr := applyRoutes(main, alts, ov)
		res.WatchOrder = append(main, alts...)
		so := res.StoryOrder
		so.Order, so.Alternates, so.Routes = applyRoutes(so.Order, so.Alternates, ov)
		if len(wr.Failed) > 0 && len(so.Routes.Failed) == 0 {
			so.Routes.Failed = wr.Failed
		}
		markRoutes(res.ReleaseOrder, res.WatchOrder)
	}
	res.AltCuts = ov.AltCutMaps()
	markAltCuts(res.AltCuts, res.ReleaseOrder, res.WatchOrder, res.StoryOrder.Order, res.StoryOrder.Alternates)
}
