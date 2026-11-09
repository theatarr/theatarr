package anime

import "strings"

// RowEpisodes narrows an entry's TVDB episodes to one order row's part: the watch order's "up to before S01E13",
// "from S01E12 up to before S01E13" and "from S01E13", or the story order's range labels ("S01E01-E11, S01E13").
// A row without a part has every episode.
func RowEpisodes(r *Row, eps []SE) []SE {
	part := ""
	if r.Part != nil {
		part = strings.TrimSpace(*r.Part)
	}
	if part == "" {
		return append([]SE(nil), eps...)
	}
	if strings.Contains(part, "before") || strings.HasPrefix(part, "from") {
		var out []SE
		var lo, hi *SE
		if m := fromRE.FindStringSubmatch(part); m != nil {
			lo = parseSE(m[1])
		}
		if m := beforeRE.FindStringSubmatch(part); m != nil {
			hi = parseSE(m[1])
		}
		for _, x := range eps {
			if lo != nil && x.less(*lo) || hi != nil && !x.less(*hi) {
				continue
			}
			out = append(out, x)
		}
		return out
	}
	want, err := parseRangeLabel(part)
	if err != nil {
		return append([]SE(nil), eps...)
	}
	in := map[SE]bool{}
	for _, x := range want {
		in[x] = true
	}
	var out []SE
	for _, x := range eps {
		if in[x] {
			out = append(out, x)
		}
	}
	return out
}
