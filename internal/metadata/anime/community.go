package anime

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The comparison with community watch orders (compareCommunity). It is an evaluation aid: it reads
// community/<slug>.json, a list of entry keys (or "key@SxxEyy-SxxEzz" arcs) per order.

// Community is a community order file.
type Community struct {
	Sources       json.RawMessage `json:"sources"`
	Recommended   []string        `json:"recommended"`
	Release       []string        `json:"release"`
	Chronological []string        `json:"chronological"`
	Ignore        []string        `json:"ignore"`
}

// CommunityResult compares our orders with the community's.
type CommunityResult struct {
	Sources json.RawMessage `json:"sources"`
	Against *omapJSON       `json:"against"`
}

// CommunityDiff is one comparison.
type CommunityDiff struct {
	CommunityEntries int      `json:"community_entries"`
	Matched          int      `json:"matched"`
	MissingHere      []string `json:"missing_here"`
	ExtraHere        []string `json:"extra_here"`
	OrderInversions  []string `json:"order_inversions"`
}

// omapJSON is an ordered string-keyed object of diffs.
type omapJSON struct {
	Keys   []string
	Values []*CommunityDiff
}

func (o *omapJSON) MarshalJSON() ([]byte, error) {
	w := newObj()
	for i, k := range o.Keys {
		w.add(k, o.Values[i])
	}
	return w.bytes()
}

func (o *omapJSON) UnmarshalJSON(b []byte) error {
	*o = omapJSON{}
	return decodeOrdered(b, func(k string, v json.RawMessage) error {
		var d CommunityDiff
		if err := json.Unmarshal(v, &d); err != nil {
			return err
		}
		o.Keys = append(o.Keys, k)
		o.Values = append(o.Values, &d)
		return nil
	})
}

var labelSegRE = regexp.MustCompile(`^S(\d+)E(\d+)(?:-E(\d+))?`)
var labelStartRE = regexp.MustCompile(`^S\d+E\d+`)

func compareCommunity(res *Result, com *Community) *CommunityResult {
	if com == nil {
		return nil
	}
	ents := map[string]*Entry{}
	for _, e := range res.Entries {
		ents[e.Key] = e
	}
	rowEps := func(r *Row) []SE {
		var eps []SE
		if e := ents[r.Key]; e != nil && e.TVDB != nil {
			eps = e.TVDB.Episodes
		}
		part := str(r.Part)
		m1, m2 := fromRE.FindStringSubmatch(part), beforeRE.FindStringSubmatch(part)
		if m1 != nil || m2 != nil {
			var keep []SE
			for _, x := range eps {
				if m1 != nil && x.less(*parseSE(m1[1])) {
					continue
				}
				if m2 != nil && !x.less(*parseSE(m2[1])) {
					continue
				}
				keep = append(keep, x)
			}
			return keep
		}
		if part != "" && labelStartRE.MatchString(part) {
			var out []SE
			for _, seg := range strings.Split(part, ", ") {
				m := labelSegRE.FindStringSubmatch(seg)
				if m == nil {
					continue
				}
				s, _ := strconv.Atoi(m[1])
				a, _ := strconv.Atoi(m[2])
				bb := a
				if m[3] != "" {
					bb, _ = strconv.Atoi(m[3])
				}
				for x := a; x <= bb; x++ {
					out = append(out, SE{s, x})
				}
			}
			return out
		}
		return eps
	}
	position := func(rows []*Row, it string) int {
		key, rng, _ := strings.Cut(it, "@")
		var cands []int
		for i, r := range rows {
			if r.Key == key {
				cands = append(cands, i)
			}
		}
		if len(cands) == 0 {
			return -1
		}
		if rng == "" {
			return cands[0]
		}
		a, bs, _ := strings.Cut(rng, "-")
		lo := parseSE(a)
		hi := lo
		if bs != "" {
			hi = parseSE(bs)
		}
		if lo != nil && hi != nil {
			for _, i := range cands {
				for _, x := range rowEps(rows[i]) {
					if inRange(x, *lo, *hi) {
						return i
					}
				}
			}
		}
		return cands[0]
	}
	out := &CommunityResult{Sources: com.Sources, Against: &omapJSON{}}
	watch := res.WatchOrder
	story := append(append([]*Row{}, res.StoryOrder.Order...), res.StoryOrder.Alternates...)
	type pair struct {
		name   string
		rows   []*Row
		theirs []string
	}
	pairs := []pair{{"watch_order vs recommended", watch, com.Recommended}}
	if len(com.Release) > 0 {
		pairs = append(pairs, pair{"watch_order vs release", watch, com.Release})
	}
	if len(com.Chronological) > 0 {
		pairs = append(pairs, pair{"story_order vs chronological", story, com.Chronological})
	} else {
		pairs = append(pairs, pair{"story_order vs recommended", story, com.Recommended})
	}
	for _, p := range pairs {
		pos := map[string]int{}
		for _, it := range p.theirs {
			pos[it] = position(p.rows, it)
		}
		var common []string
		for _, it := range p.theirs {
			if pos[it] >= 0 {
				common = append(common, it)
			}
		}
		inversions := []string{}
		for i, a := range common {
			for _, c := range common[i+1:] {
				if pos[a] > pos[c] {
					inversions = append(inversions, fmt.Sprintf("community: %s before %s; here: after", a, c))
				}
			}
		}
		keys := map[string]bool{}
		for _, it := range p.theirs {
			k, _, _ := strings.Cut(it, "@")
			keys[k] = true
		}
		extra := []string{}
		for _, r := range p.rows {
			if !keys[r.Key] && !contains(com.Ignore, r.Key) && !(r.Optional != nil && *r.Optional) && !contains(extra, r.Key) {
				extra = append(extra, r.Key)
			}
		}
		missing := []string{}
		for _, it := range p.theirs {
			if pos[it] < 0 {
				missing = append(missing, it)
			}
		}
		out.Against.Keys = append(out.Against.Keys, p.name)
		out.Against.Values = append(out.Against.Values, &CommunityDiff{CommunityEntries: len(p.theirs),
			Matched: len(common), MissingHere: missing, ExtraHere: extra, OrderInversions: inversions})
	}
	return out
}
