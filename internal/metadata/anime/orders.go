package anime

import (
	"fmt"
	"reflect"
	"sort"
)

// Stage 7: the orders (releaseOrder, topo, watchOrder). The third order, the story order, is in
// story.go with the overrides (stage 8).

// Row is one item of an order.
type Row struct {
	Key             string   `json:"key"`
	Title           *string  `json:"title"`
	Format          *string  `json:"format"`
	Date            *string  `json:"date"`
	TVDB            *string  `json:"tvdb"`
	TMDb            []int    `json:"tmdb"`
	Flags           []string `json:"flags"`
	N               *int     `json:"n"`
	PremieredDuring []string `json:"premiered_during,omitempty"`
	Role            string   `json:"role,omitempty"`
	Optional        *bool    `json:"optional,omitempty"`
	Alternates      []string `json:"alternates,omitempty"`
	Part            *string  `json:"part,omitempty"`
	PlacedBy        string   `json:"placed_by,omitempty"`
	TVDBNote        string   `json:"tvdb_note,omitempty"`
	TVDBPlacesAfter string   `json:"tvdb_places_after,omitempty"`
	AlternateOf     string   `json:"alternate_of,omitempty"`
	Route           string   `json:"route,omitempty"`
	MovedByOverride string   `json:"moved_by_override,omitempty"`
	// Not in the golden outputs: the alternate cut this row's entry is (overrides alt_cuts), and the cut's TVDB range.
	AltCut     string `json:"alt_cut,omitempty"`
	AltCutTVDB string `json:"alt_cut_tvdb,omitempty"`

	eps    []SE
	hasEps bool
}

func (r *Row) clone() *Row {
	c := *r
	return &c
}

func rowEqual(a, b *Row) bool { return reflect.DeepEqual(a, b) }

func item(e *Entry) *Row {
	t := e.TitleEn
	if str(t) == "" {
		t = e.Title
	}
	r := &Row{Key: e.Key, Title: t, Format: e.Format, Date: effDate(e), Flags: nonNilStrings(e.Flags)}
	if e.TVDB != nil && (e.TVDB.validated || e.TVDB.tvdbOnly) {
		r.TVDB = sp(e.TVDB.Range)
	}
	if len(e.TMDbMovie) > 0 {
		r.TMDb = e.TMDbMovie
	}
	return r
}

func bp(v bool) *bool { return &v }

func (b *builder) releaseOrder(entries []*Entry) []*Row {
	var rows []*Entry
	for _, e := range entries {
		if !e.hasFlag("extra") && len(e.splitInto) == 0 {
			rows = append(rows, e)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, c := rows[i], rows[j]
		if x, y := sortDate(effDate(a)), sortDate(effDate(c)); x != y {
			return x < y
		}
		if x, y := fmtRank(a.Format), fmtRank(c.Format); x != y {
			return x < y
		}
		return a.Key < c.Key
	})
	type run struct{ s, t *string }
	var runs []struct {
		run
		key string
	}
	for _, e := range rows {
		if str(e.Format) == "TV" && str(e.End) != "" {
			runs = append(runs, struct {
				run
				key string
			}{run{e.Start, e.End}, e.Key})
		}
	}
	out := []*Row{}
	for i, e := range rows {
		var during []string
		d := str(effDate(e))
		for _, r := range runs {
			if r.key != e.Key && str(r.s) != "" && str(r.t) != "" && *r.s < d && d <= *r.t {
				during = append(during, r.key)
			}
		}
		row := item(e)
		row.N = ip(i + 1)
		row.PremieredDuring = during
		out = append(out, row)
	}
	return out
}

func byDateRank(xs []*Entry) func(i, j int) bool {
	return func(i, j int) bool {
		a, c := xs[i], xs[j]
		if x, y := sortDate(effDate(a)), sortDate(effDate(c)); x != y {
			return x < y
		}
		return fmtRank(a.Format) < fmtRank(c.Format)
	}
}

// topo orders spine entries by the AniList PREQUEL/SEQUEL edges among them (Kahn), earliest date first.
//
// Deviation: the golden outputs walk each node's successors as a Python set of strings, whose order changes with
// PYTHONHASHSEED; this walks them in the order the edges were found, because that order is the same on every
// run. Ready nodes are re-sorted by date and format before every pick, so this matters only for ties.
func topo(spine []*Entry) []*Entry {
	keys := map[int]*Entry{}
	for _, e := range spine {
		if e.AniListID != nil && *e.AniListID != 0 {
			keys[*e.AniListID] = e
		}
	}
	succ := map[string][]string{}
	succSet := map[string]map[string]bool{}
	indeg := map[string]int{}
	by := map[string]*Entry{}
	for _, e := range spine {
		succSet[e.Key] = map[string]bool{}
		indeg[e.Key] = 0
		by[e.Key] = e
	}
	for _, e := range spine {
		for _, r := range e.AniListRelations {
			o := keys[r.ID]
			if o == nil || o == e {
				continue
			}
			var a, c *Entry
			switch r.Type {
			case "SEQUEL":
				a, c = e, o
			case "PREQUEL":
				a, c = o, e
			default:
				continue
			}
			if !succSet[a.Key][c.Key] {
				succSet[a.Key][c.Key] = true
				succ[a.Key] = append(succ[a.Key], c.Key)
				indeg[c.Key]++
			}
		}
	}
	var ready []*Entry
	for _, e := range spine {
		if indeg[e.Key] == 0 {
			ready = append(ready, e)
		}
	}
	var out []*Entry
	done := map[*Entry]bool{}
	for len(ready) > 0 {
		sort.SliceStable(ready, byDateRank(ready))
		k := ready[0]
		ready = ready[1:]
		out = append(out, k)
		done[k] = true
		for _, n := range succ[k.Key] {
			indeg[n]--
			if indeg[n] == 0 {
				ready = append(ready, by[n])
			}
		}
	}
	for _, e := range spine {
		if !done[e] {
			out = append(out, e)
		}
	}
	return out
}

type placed struct {
	e  *Entry
	kw Row // only the placement fields are used
	se SE
}

func roleOf(e *Entry) string {
	if e.hasFlag("recap") || e.hasFlag("recap?") {
		return "recap"
	}
	for _, r := range e.AniListRelations {
		if r.Type == "SPIN_OFF" {
			return "spin-off"
		}
	}
	if containsStr(str(e.FoundVia), "SPIN_OFF") || e.hasFlag("chibi-short") || e.hasFlag("crossover-linked") {
		return "spin-off"
	}
	if e.Origin == "tvdb" {
		return "tvdb-special"
	}
	return "side-story"
}

func containsStr(s, sub string) bool {
	return len(sub) <= len(s) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// watchOrder is the automatic order from open data (mode "watch", release-based) or the story-mode candidate
// (mode "story").
func (b *builder) watchOrder(entries []*Entry, mode string) []*Row {
	var pool []*Entry
	for _, e := range entries {
		if !e.hasFlag("extra") && len(e.splitInto) == 0 {
			pool = append(pool, e)
		}
	}
	byKey := map[string]*Entry{}
	for _, e := range pool {
		byKey[e.Key] = e
	}
	var spine []*Entry
	for _, e := range pool {
		if e.Mainline {
			spine = append(spine, e)
		}
	}
	sort.SliceStable(spine, byDateRank(spine))
	altOf := newOmap[string, string]()
	routeOf := newOmap[string, string]()
	onLine := func(x *Entry) bool {
		if x.Kind == "line" {
			return true
		}
		p := byKey[x.ParentKey]
		return p != nil && p.Kind == "line"
	}
	for _, e := range pool {
		if e.hasFlag("recap") {
			continue // compilations are recaps, not alternates
		}
		var partners []string
		for _, r := range e.AniListRelations {
			if r.Type == "ALTERNATIVE" {
				partners = append(partners, fmt.Sprintf("al:%d", r.ID))
			}
		}
		if e.KitsuRole != nil && e.KitsuRole.Role == "alternative_version" && !e.hasFlag("recap?") {
			partners = append(partners, e.KitsuRole.Of)
		}
		var best *Entry
		for _, p := range partners {
			o := byKey[p]
			if o == nil || o.hasFlag("recap") {
				continue
			}
			if onLine(e) && !onLine(o) {
				continue // the seed's own line is the primary version
			}
			if !(onLine(o) && !onLine(e)) && sortDate(effDate(o)) >= sortDate(effDate(e)) {
				continue // between equals, the earlier release is primary
			}
			if best == nil || altLess(o, best, onLine) {
				best = o
			}
		}
		if best != nil {
			altOf.Set(e.Key, best.Key)
		}
	}
	// a branch alternate carries its own sequels along as one route block (Heaven's Feel I-III, Kai + Final Chapters)
	for _, root := range altOf.Keys() {
		todo := []string{root}
		for len(todo) > 0 {
			cur := byKey[todo[len(todo)-1]]
			todo = todo[:len(todo)-1]
			for _, r := range cur.AniListRelations {
				k := fmt.Sprintf("al:%d", r.ID)
				if r.Type == "SEQUEL" && byKey[k] != nil && !altOf.Has(k) && !routeOf.Has(k) && byKey[k].Kind == "branch" {
					routeOf.Set(k, root)
					todo = append(todo, k)
				}
			}
		}
	}
	var sp2 []*Entry
	for _, e := range spine {
		if !altOf.Has(e.Key) && !routeOf.Has(e.Key) {
			sp2 = append(sp2, e)
		}
	}
	spine = sp2
	owners := map[int]map[string]bool{}
	for _, e := range pool {
		if e.TVDB != nil && e.TVDB.SeriesID != 0 && e.Origin != "tvdb" && !e.hasFlag("recap") && !e.hasFlag("recap?") {
			if owners[e.TVDB.SeriesID] == nil {
				owners[e.TVDB.SeriesID] = map[string]bool{}
			}
			owners[e.TVDB.SeriesID][e.Key] = true
		}
	}
	for _, e := range pool {
		if e.TVDB == nil || e.TVDB.SeriesID == 0 {
			continue
		}
		tid := e.TVDB.SeriesID
		if (e.Origin == "tvdb" || e.hasFlag("recap") || e.hasFlag("recap?")) && len(owners[tid]) > 0 && !altOf.Has(e.Key) {
			roots := map[string]bool{}
			none := false
			for k := range owners[tid] {
				if altOf.Has(k) {
					roots[k] = true
				} else if r, ok := routeOf.Get(k); ok {
					roots[r] = true
				} else {
					none = true
				}
			}
			if !none && len(roots) == 1 {
				for r := range roots {
					routeOf.Set(e.Key, r) // e.g. the 2006 Fate/stay night series' own specials
				}
			}
		}
	}
	if mode == "story" {
		spine = topo(spine)
	}
	inSpine := map[*Entry]bool{}
	for _, e := range spine {
		inSpine[e] = true
	}

	slotByDate := func(e *Entry) int {
		d := sortDate(effDate(e))
		s := -1
		for i, x := range spine {
			if sortDate(effDate(x)) <= d {
				s = i
			}
		}
		return s
	}
	type tvdbSlot struct {
		ok     bool
		slot   int
		mid    *SE
		win    bool
		w0, w1 int
	}
	slotByTVDB := func(e *Entry) tvdbSlot {
		tv := e.TVDB
		if tv == nil || tv.Airs == nil {
			return tvdbSlot{}
		}
		airs := tv.Airs
		tid := tv.SeriesID
		if airs.BeforeSeason != nil {
			ep := 1
			if airs.BeforeEpisode != nil && *airs.BeforeEpisode != 0 {
				ep = *airs.BeforeEpisode
			}
			se := SE{*airs.BeforeSeason, ep}
			for i, x := range spine {
				st := x.TVDB
				if st == nil || st.SeriesID != tid {
					continue
				}
				for k, y := range st.Episodes {
					if y == se {
						if k == 0 {
							return tvdbSlot{ok: true, slot: i - 1}
						}
						return tvdbSlot{ok: true, slot: i, mid: &se}
					}
				}
			}
		}
		if airs.AfterSeason != nil {
			s := *airs.AfterSeason
			last, nxt := -1, -1
			for i, x := range spine {
				st := x.TVDB
				if st == nil || st.SeriesID != tid {
					continue
				}
				has, later := false, false
				for _, y := range st.Episodes {
					if y[0] == s {
						has = true
					}
					if y[0] > s {
						later = true
					}
				}
				if has {
					last = i
				}
				if nxt < 0 && later && (last < 0 || i > last) {
					nxt = i
				}
			}
			if last >= 0 {
				hi := len(spine) - 1
				if nxt >= 0 && nxt > last {
					hi = nxt - 1
				}
				return tvdbSlot{ok: true, slot: last, win: true, w0: last, w1: hi}
			}
		}
		return tvdbSlot{}
	}

	slots := map[int][]placed{}
	mids := map[int][]placed{}
	for _, e := range pool {
		if inSpine[e] || altOf.Has(e.Key) || routeOf.Has(e.Key) {
			continue
		}
		dSlot := slotByDate(e)
		t := slotByTVDB(e)
		eps := intv(e.Episodes)
		if eps == 0 {
			eps = 1
		}
		single := eps == 1 && roleOf(e) != "spin-off"
		useTVDB := t.ok && (e.Origin == "tvdb" || (mode == "story" && single))
		slot, rule := dSlot, "release-date"
		if useTVDB && t.win {
			slot, rule = t.w0, "tvdb-airs (after season)"
			if t.w0 <= dSlot && dSlot <= t.w1 {
				slot = dSlot
			}
		} else if useTVDB {
			slot, rule = t.slot, "tvdb-airs"
		}
		var disagree bool
		if t.win {
			disagree = !(t.w0 <= dSlot && dSlot <= t.w1)
		} else {
			disagree = t.ok && (t.slot != dSlot || t.mid != nil)
		}
		kw := Row{PlacedBy: rule}
		if t.mid != nil && !disagree && !(useTVDB && mode == "story" && !t.win) {
			kw.TVDBNote = fmt.Sprintf("TVDB: before %s, inside %s", *t.mid, spine[t.slot].Key)
		}
		if disagree && !useTVDB {
			after := "start"
			if t.slot >= 0 {
				after = spine[t.slot].Key
			}
			if t.mid != nil {
				after += fmt.Sprintf(" (mid-entry, before %s)", *t.mid)
			}
			kw.TVDBPlacesAfter = after
		}
		dur := 99
		if e.DurationMin != nil && *e.DurationMin != 0 {
			dur = *e.DurationMin
		}
		short := dur < 15
		if useTVDB && t.mid != nil && !t.win && !short {
			mids[slot] = append(mids[slot], placed{e, kw, *t.mid})
			continue
		}
		if mode == "watch" && rule == "release-date" && !short && slot >= 0 {
			x := spine[slot]
			st := x.TVDB
			ed := str(effDate(e))
			d := toDate(ed)
			if str(x.Format) == "TV" && st != nil && len(st.Episodes) > 0 && d != 0 && len(ed) == 10 {
				sh := b.m.show(st.SeriesID)
				var regular []SE
				for _, se := range st.Episodes {
					if se[0] > 0 {
						regular = append(regular, se)
					}
				}
				var nxt *SE
				for _, se := range regular {
					var air *string
					if ep := sh.ep(se); ep != nil {
						air = ep.AirDate
					}
					if ad := toDateP(air); ad != 0 && ad > d {
						s := se
						nxt = &s
						break
					}
				}
				if nxt != nil && *nxt != regular[0] {
					kw.PlacedBy = fmt.Sprintf("release-date (aired during %s, before %s)", x.Key, *nxt)
					mids[slot] = append(mids[slot], placed{e, kw, *nxt})
					continue
				}
			}
		}
		slots[slot] = append(slots[slot], placed{e: e, kw: kw})
	}

	out := []*Row{}
	add := func(e *Entry, role string, kw Row) {
		opt := role != "mainline" || e.hasFlag("recap") || e.hasFlag("recap?") || e.hasFlag("chibi-short") || e.hasFlag("short")
		r := item(e)
		r.N = ip(len(out) + 1)
		r.Role = role
		r.Optional = bp(opt)
		r.Alternates, r.Part = kw.Alternates, kw.Part
		r.PlacedBy, r.TVDBNote, r.TVDBPlacesAfter = kw.PlacedBy, kw.TVDBNote, kw.TVDBPlacesAfter
		out = append(out, r)
	}
	for i := -1; i < len(spine); i++ {
		if i >= 0 {
			x := spine[i]
			var alts []string
			for _, k := range altOf.keys {
				if altOf.m[k] == x.Key {
					alts = append(alts, k)
				}
			}
			inner := append([]placed(nil), mids[i]...)
			sort.SliceStable(inner, func(a, c int) bool { return inner[a].se.less(inner[c].se) })
			if len(inner) == 0 {
				add(x, "mainline", Row{Alternates: alts})
			} else {
				var prev *SE
				for _, p := range inner {
					if prev == nil || p.se != *prev {
						part := ""
						if prev != nil {
							part = fmt.Sprintf("from %s ", *prev)
						}
						part += fmt.Sprintf("up to before %s", p.se)
						add(x, "mainline", Row{Alternates: alts, Part: sp(part)})
					}
					add(p.e, roleOf(p.e), p.kw)
					s := p.se
					prev = &s
				}
				add(x, "mainline", Row{Alternates: alts, Part: sp(fmt.Sprintf("from %s", *prev))})
			}
		}
		ps := append([]placed(nil), slots[i]...)
		sort.SliceStable(ps, func(a, c int) bool { return sortDate(effDate(ps[a].e)) < sortDate(effDate(ps[c].e)) })
		for _, p := range ps {
			add(p.e, roleOf(p.e), p.kw)
		}
	}
	alts := altOf.Keys()
	sort.SliceStable(alts, func(i, j int) bool {
		return sortDate(effDate(byKey[alts[i]])) < sortDate(effDate(byKey[alts[j]]))
	})
	for _, k := range alts {
		v := altOf.m[k]
		r := item(byKey[k])
		r.Role, r.AlternateOf, r.Optional = "alternate", v, bp(true)
		out = append(out, r)
		var routed []string
		for _, x := range routeOf.keys {
			if routeOf.m[x] == k {
				routed = append(routed, x)
			}
		}
		sort.SliceStable(routed, func(i, j int) bool {
			return sortDate(effDate(byKey[routed[i]])) < sortDate(effDate(byKey[routed[j]]))
		})
		for _, x := range routed {
			r := item(byKey[x])
			r.Role, r.AlternateOf, r.Route, r.Optional = "alternate", v, k, bp(true)
			out = append(out, r)
		}
	}
	return out
}

// altLess is min()'s key: on-line partners first, then the earlier release.
func altLess(a, c *Entry, onLine func(*Entry) bool) bool {
	la, lc := !onLine(a), !onLine(c)
	if la != lc {
		return !la
	}
	return sortDate(effDate(a)) < sortDate(effDate(c))
}
