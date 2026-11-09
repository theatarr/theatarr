package anime

import (
	"regexp"
	"strconv"
	"strings"
)

// Stage 8: the story order, the story-mode automatic order plus curated moves from the franchise's overrides
// (storyOrder). "entry" with a range moves only that slice of the entry (an arc inside a season);
// "after" with a single episode splits the target there; "after" with a range means after that slice; a plain key
// means after its last part.

// StoryOrder is the story order with what the overrides did.
type StoryOrder struct {
	Basis        string `json:"basis"`
	MovesApplied int    `json:"moves_applied"`
	MovesFailed  []Move `json:"moves_failed"`
	Source       any    `json:"source"`
	Order        []*Row `json:"order"`
	Alternates   []*Row `json:"alternates"`
	// Routes is what the routes override did (not in the golden outputs).
	Routes RouteResult `json:"routes,omitzero"`
}

var seRE = regexp.MustCompile(`^S(\d+)E(\d+)$`)

func parseSE(t string) *SE {
	m := seRE.FindStringSubmatch(t)
	if m == nil {
		return nil
	}
	s, _ := strconv.Atoi(m[1])
	e, _ := strconv.Atoi(m[2])
	return &SE{s, e}
}

var (
	fromRE   = regexp.MustCompile(`from (S\d+E\d+)`)
	beforeRE = regexp.MustCompile(`before (S\d+E\d+)`)
)

func inRange(x, lo, hi SE) bool { return !x.less(lo) && !hi.less(x) }

func sliceBounds(rng string) (*SE, *SE) {
	a, b, _ := strings.Cut(rng, "-")
	lo := parseSE(a)
	switch {
	case strings.HasPrefix(b, "S"):
		return lo, parseSE(b)
	case strings.HasPrefix(b, "E") && lo != nil:
		n, err := strconv.Atoi(b[1:])
		if err != nil {
			return lo, nil
		}
		return lo, &SE{lo[0], n}
	}
	return lo, lo
}

func (b *builder) storyOrder(entries []*Entry, ov *Overrides) *StoryOrder {
	all := b.watchOrder(entries, "story")
	var rows, alternates []*Row
	for _, r := range all {
		if r.Role == "alternate" {
			alternates = append(alternates, r.clone())
		} else {
			rows = append(rows, r.clone())
		}
	}
	res := &StoryOrder{Basis: "AniList chain (topological) + TVDB airs for single specials", MovesFailed: []Move{}}
	if ov != nil {
		res.Source = ov.Source
	}
	byKey := map[string]*Entry{}
	for _, e := range entries {
		byKey[e.Key] = e
	}
	rowEps := func(r *Row) []SE {
		if r.hasEps {
			return r.eps
		}
		var eps []SE
		if e := byKey[r.Key]; e != nil && e.TVDB != nil {
			eps = append(eps, e.TVDB.Episodes...)
		}
		part := str(r.Part)
		if m := fromRE.FindStringSubmatch(part); m != nil {
			lo := parseSE(m[1])
			var keep []SE
			for _, x := range eps {
				if !x.less(*lo) {
					keep = append(keep, x)
				}
			}
			eps = keep
		}
		if m := beforeRE.FindStringSubmatch(part); m != nil {
			hi := parseSE(m[1])
			var keep []SE
			for _, x := range eps {
				if x.less(*hi) {
					keep = append(keep, x)
				}
			}
			eps = keep
		}
		return eps
	}
	anyIn := func(eps []SE, lo, hi SE) bool {
		for _, x := range eps {
			if inRange(x, lo, hi) {
				return true
			}
		}
		return false
	}
	withEps := func(r *Row, eps []SE) *Row {
		c := r.clone()
		c.Part = sp(ranges(eps))
		c.eps, c.hasEps = eps, true
		return c
	}
	indexEqual := func(rs []*Row, r *Row) int {
		for i, x := range rs {
			if rowEqual(x, r) {
				return i
			}
		}
		return -1
	}
	insert := func(rs []*Row, i int, r ...*Row) []*Row {
		out := append([]*Row{}, rs[:i]...)
		out = append(out, r...)
		return append(out, rs[i:]...)
	}
	var moves []Move
	if ov != nil {
		moves = ov.Moves
	}
	for _, mv := range moves {
		key, sl, _ := strings.Cut(mv.Entry, "@")
		var found []*Row
		for _, r := range rows {
			if r.Key == key {
				found = append(found, r)
			}
		}
		if len(found) == 0 {
			var alt *Row
			for _, r := range alternates {
				if r.Key == key {
					alt = r
					break
				}
			}
			if alt == nil {
				res.MovesFailed = append(res.MovesFailed, mv)
				continue
			}
			// an override may pull an alternate (e.g. a retelling) into the story order
			if i := indexEqual(alternates, alt); i >= 0 {
				alternates = append(alternates[:i:i], alternates[i+1:]...)
			}
			c := alt.clone()
			c.Role = "alternate-in-story"
			rows = append(rows, c)
			found = []*Row{c}
		}
		var src *Row
		if sl != "" {
			lo, hi := sliceBounds(sl)
			var host *Row
			if lo != nil && hi != nil {
				for _, r := range found {
					if anyIn(rowEps(r), *lo, *hi) {
						host = r
						break
					}
				}
			}
			if host == nil || lo == nil {
				res.MovesFailed = append(res.MovesFailed, mv)
				continue
			}
			eps := rowEps(host)
			var before, mid, after []SE
			for _, x := range eps {
				switch {
				case x.less(*lo):
					before = append(before, x)
				case !hi.less(x):
					mid = append(mid, x)
				default:
					after = append(after, x)
				}
			}
			j := indexEqual(rows, host)
			var repl []*Row
			for _, p := range [][]SE{before, after} {
				if len(p) > 0 {
					repl = append(repl, withEps(host, p))
				}
			}
			rows = append(append(append([]*Row{}, rows[:j]...), repl...), rows[j+1:]...)
			src = withEps(host, mid)
		} else {
			src = found[0]
			for _, r := range found {
				if i := indexEqual(rows, r); i >= 0 {
					rows = append(rows[:i:i], rows[i+1:]...)
				}
			}
			src = src.clone()
			src.Part = nil
		}
		ref := mv.After
		base, ep, _ := strings.Cut(ref, "@")
		if ref == "start" {
			rows = insert(rows, 0, src)
		} else {
			var targets []int
			for k, r := range rows {
				if r.Key == base {
					targets = append(targets, k)
				}
			}
			if len(targets) == 0 {
				rows = append(rows, src)
				res.MovesFailed = append(res.MovesFailed, mv)
				continue
			}
			last := targets[len(targets)-1]
			switch {
			case ep != "" && strings.Contains(ep, "-"):
				lo, hi := sliceBounds(ep)
				at := last
				if lo != nil && hi != nil {
					for _, k := range targets {
						if anyIn(rowEps(rows[k]), *lo, *hi) {
							at = k
						}
					}
				}
				rows = insert(rows, at+1, src)
			case ep != "":
				cut := parseSE(ep)
				k := last
				if cut != nil {
					for _, t := range targets {
						if seSet(rowEps(rows[t]))[*cut] {
							k = t
							break
						}
					}
				}
				eps := rowEps(rows[k])
				var head, tail []SE
				for _, x := range eps {
					if cut != nil && !cut.less(x) {
						head = append(head, x)
					} else {
						tail = append(tail, x)
					}
				}
				repl := []*Row{withEps(rows[k], head), src}
				if len(tail) > 0 {
					repl = append(repl, withEps(rows[k], tail))
				}
				rows = append(append(append([]*Row{}, rows[:k]...), repl...), rows[k+1:]...)
			default:
				rows = insert(rows, last+1, src)
			}
		}
		src.MovedByOverride = mv.Why
		res.MovesApplied++
	}
	for k, r := range rows {
		r.N = ip(k + 1)
		r.eps, r.hasEps = nil, false
	}
	if rows == nil {
		rows = []*Row{}
	}
	if alternates == nil {
		alternates = []*Row{}
	}
	res.Order, res.Alternates = rows, alternates
	return res
}
