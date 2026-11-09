package anime

import (
	"fmt"
	"strconv"
	"strings"
)

// Stages 3 and 4: candidate mappings from both lists and their arbitration (prepareAniListEntry,
// dateFit, arbitrate), plus a decision status (docs/design/anime.md "Arbitration and the review queue").

// Mapping statuses.
const (
	StatusAuto       = "auto"       // the lists agree, or one list is backed by a date fit
	StatusArbitrated = "arbitrated" // the lists disagree and the winner has a clear margin or the anchor signal
	StatusConflict   = "conflict"   // no clear winner: no automatic import of the range, a review item
	StatusOverride   = "override"   // set by an override file
	StatusHeuristic  = "heuristic"  // matched by date and title only: kept, and listed for review
	StatusRejected   = "rejected"   // every candidate was dropped (no TVDB episodes, or dates that don't fit)
)

// Anchor signal values (ScudLee <before> against TVDB airsBefore).
const (
	AnchorMatch    = "match"
	AnchorMismatch = "mismatch"
)

// ArbitratedMargin is the score margin over the runner-up that makes a disagreement "arbitrated" rather than a
// conflict. The score ranges 0..5 (date fit ×2, exists, coverage, title similarity).
const ArbitratedMargin = 0.5

// MappingCandidate is one list's proposed mapping for an entry, with its score parts.
type MappingCandidate struct {
	Source   string   `json:"source"` // scudlee or anibridge
	SeriesID int      `json:"series_id"`
	Episodes []SE     `json:"episodes"`
	Range    string   `json:"range"`
	Exists   float64  `json:"exists"`
	DateFit  *float64 `json:"date_fit"`
	Cover    float64  `json:"cover"`
	TitleSim float64  `json:"title_sim"`
	Score    float64  `json:"score"`
	Anchor   string   `json:"anchor,omitempty"`
	Dropped  string   `json:"dropped,omitempty"`
}

// MappingOutcome is the stored decision for an entry: the candidates, the winner and
// the status.
type MappingOutcome struct {
	Status     string             `json:"status"`
	Winner     string             `json:"winner,omitempty"` // scudlee, anibridge, heuristic, tvdb-only, override
	Margin     *float64           `json:"margin,omitempty"`
	Reason     string             `json:"reason"`
	Candidates []MappingCandidate `json:"candidates,omitempty"`
}

func (b *builder) prepareAniListEntry(e *Entry) {
	b.m.resolveIDs(e)
	b.m.mapEntry(e)
	ab := b.src.Lists.AnibridgeFor(*e.AniListID, e.Episodes)
	e.Anibridge = ab
	if len(e.TMDbMovie) == 0 && len(ab.TMDbMovie) > 0 && (str(e.Format) == "MOVIE" || str(e.Format) == "OVA") {
		e.TMDbMovie = ab.TMDbMovie
		e.note("TMDb id from anibridge only")
	}
	b.arbitrate(e)
}

// dateFit is the share of mapped episodes whose TVDB air date falls inside the entry's run (±10 days; ±45 for
// one-offs); nil when there is nothing to compare.
func (b *builder) dateFit(e *Entry, tid int, eps []SE) *float64 {
	a := toDateP(e.Start)
	endS := e.End
	if str(endS) == "" {
		endS = e.Start
	}
	bd := toDateP(endS)
	sh := b.m.show(tid)
	if a == 0 || len(eps) == 0 || !sh.ok {
		return nil
	}
	pad := pyDate(45)
	if intv(e.Episodes) > 1 && str(e.End) != "" {
		pad = 10
	}
	ok := 0
	for _, se := range eps {
		var air *string
		if ep := sh.ep(se); ep != nil {
			air = ep.AirDate
		}
		if d := toDateP(air); d != 0 && a-pad <= d && d <= bd+pad {
			ok++
		}
	}
	f := float64(ok) / float64(len(eps))
	return &f
}

func fitRepr(f *float64) string {
	if f == nil {
		return "None"
	}
	return pyFloat(round2(*f))
}

func seSet(x []SE) map[SE]bool {
	m := make(map[SE]bool, len(x))
	for _, v := range x {
		m[v] = true
	}
	return m
}

func sameSESet(a, b []SE) bool {
	sa, sb := seSet(a), seSet(b)
	if len(sa) != len(sb) {
		return false
	}
	for k := range sa {
		if !sb[k] {
			return false
		}
	}
	return true
}

// arbitrate picks between the ScudLee-derived mapping and anibridge's AniList-keyed one and
// records the decision status.
func (b *builder) arbitrate(e *Entry) {
	var ab [][3]int
	if e.Anibridge != nil {
		ab = e.Anibridge.TVDB
	}
	type cand struct {
		method string
		tid    int
		eps    []SE
	}
	var cands []cand
	if e.TVDB != nil && len(e.TVDB.Episodes) > 0 {
		cands = append(cands, cand{"scudlee", e.TVDB.SeriesID, e.TVDB.Episodes})
	}
	if len(ab) > 0 {
		tid := ab[0][0]
		var eps []SE
		for _, t := range ab {
			if t[0] == tid {
				eps = append(eps, SE{t[1], t[2]})
			}
		}
		cands = append(cands, cand{"anibridge", tid, eps})
	}
	if len(cands) == 0 {
		return
	}
	out := &MappingOutcome{}
	e.Mapping = out
	n := intv(e.Episodes)
	if n == 0 {
		n = 1
	}
	var scored []MappingCandidate
	for _, c := range cands {
		sh := b.m.show(c.tid)
		mc := MappingCandidate{Source: c.method, SeriesID: c.tid, Episodes: c.eps, Range: ranges(c.eps)}
		have := 0
		for _, se := range c.eps {
			if sh.ep(se) != nil {
				have++
			}
		}
		mc.Exists = float64(have) / float64(len(c.eps))
		if mc.Exists == 0 {
			e.note("%s points at %s of TVDB %d, which TVDB does not have; dropped", c.method, ranges(c.eps), c.tid)
			mc.Dropped = "no such TVDB episodes"
			out.Candidates = append(out.Candidates, mc)
			continue
		}
		mc.DateFit = b.dateFit(e, c.tid, c.eps)
		mc.Cover = float64(min(len(c.eps), n)) / float64(n)
		if c.eps[0][0] == 0 {
			for _, se := range c.eps {
				if ep := sh.ep(se); ep != nil {
					mc.TitleSim = titleSim(e.Names, str(ep.Title), true)
					break
				}
			}
		}
		fit := 0.5
		if mc.DateFit != nil {
			fit = *mc.DateFit
		}
		mc.Score = fit*2 + mc.Exists + mc.Cover + mc.TitleSim
		mc.Anchor = b.anchor(e, c.tid, c.eps)
		scored = append(scored, mc)
	}
	if len(scored) == 0 {
		e.TVDB = nil
		out.Status, out.Reason = StatusRejected, "every candidate points at TVDB episodes that don't exist"
		return
	}
	// stable sort by score, highest first
	for i := 1; i < len(scored); i++ {
		for j := i; j > 0 && scored[j].Score > scored[j-1].Score; j-- {
			scored[j], scored[j-1] = scored[j-1], scored[j]
		}
	}
	out.Candidates = append(scored, out.Candidates...)
	best := scored[0]
	differ := len(scored) > 1 && !sameESet(scored[0].Episodes, scored[1].Episodes)
	if differ {
		var parts []string
		for _, s := range scored {
			parts = append(parts, fmt.Sprintf("%s %s (date fit %s, title %s)", s.Source, s.Range, fitRepr(s.DateFit),
				pyFloat(round2(s.TitleSim))))
		}
		e.note("mappings differ: %s -> %s", strings.Join(parts, "; "), best.Source)
		m := best.Score - scored[1].Score
		out.Margin = &m
	}
	keptReason := ""
	if best.DateFit != nil && *best.DateFit < 0.5 && str(e.Start) != "" {
		bs := seSet(best.Episodes)
		others := false
		for _, c := range scored[1:] {
			common := 0
			for _, se := range c.Episodes {
				if bs[se] {
					common++
				}
			}
			if float64(common) >= 0.5*float64(min(len(c.Episodes), len(best.Episodes))) {
				others = true
			}
		}
		if others || best.Source == "scudlee" || best.TitleSim >= 0.5 {
			// two sources agree, or a direct AniDB join: the dates describe another release (streaming vs
			// broadcast, film vs TV cut), not a wrong mapping
			why := "it is a direct AniDB join"
			if others {
				why = "ScudLee and anibridge agree"
			} else if best.TitleSim >= 0.5 {
				why = "the TVDB title matches"
			}
			e.note("TVDB air dates do not fit %s..%s; kept %s mapping %s because %s", str(e.Start), pyStr(e.End),
				best.Source, best.Range, why)
			keptReason = why
		} else {
			e.note("%s mapping %s rejected: TVDB air dates do not fit %s..%s", best.Source, best.Range, str(e.Start), pyStr(e.End))
			e.TVDB = nil
			rej := best.Source == "anibridge"
			e.AnibridgeRejected = &rej
			out.Status, out.Reason = StatusRejected, "the only fitting candidate's TVDB air dates do not fit the entry's run"
			return
		}
	}
	if best.Source == "anibridge" && str(e.Start) == "" {
		e.note("anibridge mapping %s rejected: entry has no date", best.Range)
		t := true
		e.AnibridgeRejected = &t
		if e.TVDB != nil && len(e.TVDB.Episodes) > 0 {
			// the ScudLee mapping stays in place
			out.Winner = "scudlee"
			out.Status, out.Reason = classify(scored, differ, out.Margin, keptReason, "scudlee")
		} else {
			out.Status, out.Reason = StatusRejected, "anibridge's mapping has no date to check against"
		}
		return
	}
	out.Winner = best.Source
	out.Status, out.Reason = classify(scored, differ, out.Margin, keptReason, best.Source)
	if best.Source == "scudlee" {
		return
	}
	var old []Special
	if e.TVDB != nil {
		old = e.TVDB.AniDBSpecials
	}
	if old == nil {
		old = []Special{}
	}
	e.TVDB = &TVDBMap{SeriesID: best.SeriesID, Episodes: best.Episodes, Method: "anibridge",
		AniDBSpecials: old, UnmappedAniDBEps: []int{}}
}

// sameESet compares two episode lists as sets.
func sameESet(a, b []SE) bool { return sameSESet(a, b) }

// classify gives the design's status for the arbitration's decision.
func classify(scored []MappingCandidate, differ bool, margin *float64, keptReason, winner string) (string, string) {
	var w MappingCandidate
	for _, s := range scored {
		if s.Source == winner {
			w = s
			break
		}
	}
	if !differ {
		switch {
		case len(scored) > 1:
			return StatusAuto, "ScudLee and anibridge agree"
		case keptReason != "":
			return StatusArbitrated, "only " + winner + "; dates do not fit but " + keptReason
		case w.DateFit == nil:
			return StatusAuto, "only " + winner + "; no dates to check"
		case w.Anchor == AnchorMismatch:
			return StatusConflict, "only " + winner + "; ScudLee's placement disagrees with TVDB airsBefore"
		}
		return StatusAuto, "only " + winner + ", backed by TVDB air dates"
	}
	m := 0.0
	if margin != nil {
		m = *margin
	}
	if winner != scored[0].Source {
		return StatusConflict, "the winner is not the best-scored candidate"
	}
	switch {
	case w.Anchor == AnchorMismatch:
		return StatusConflict, "the lists disagree and the winner's placement contradicts TVDB airsBefore"
	case w.Anchor == AnchorMatch:
		return StatusArbitrated, "the lists disagree; the winner's placement matches TVDB airsBefore"
	case m >= ArbitratedMargin:
		return StatusArbitrated, "the lists disagree; score margin " + strconv.FormatFloat(round2(m), 'f', -1, 64)
	}
	return StatusConflict, "the lists disagree and the scores are within " + strconv.FormatFloat(ArbitratedMargin, 'f', -1, 64)
}

// anchor compares ScudLee's <before> placement of AniDB specials with TVDB's airsBefore for the TVDB specials a
// candidate maps to. "<before>;1-26;</before>" means AniDB special 1 goes before episode 26.
// The special is found through the record's season-0 mapping list, or, for a record whose default TVDB season is
// 0, as the candidate's n-th episode. The episode is compared both as given and as mapped through the record.
func (b *builder) anchor(e *Entry, tid int, eps []SE) string {
	if e.AniDBID == nil {
		return ""
	}
	rec := b.src.Lists.ScudLee[*e.AniDBID]
	if rec == nil || rec.Before == nil {
		return ""
	}
	in := seSet(eps)
	sh := b.m.show(tid)
	result := ""
	for _, part := range strings.Split(strings.Trim(*rec.Before, ";"), ";") {
		src, dst, ok := strings.Cut(part, "-")
		s, err1 := strconv.Atoi(src)
		before, err2 := strconv.Atoi(dst)
		if !ok || err1 != nil || err2 != nil {
			continue
		}
		var special *SE
		for _, mp := range rec.Mappings {
			if mp.AniDBSeason != 0 || mp.TVDBSeason != 0 {
				continue
			}
			for _, p := range mp.Pairs {
				if p.Src == s && len(p.Dst) > 0 && p.Dst[0] != 0 {
					special = &SE{0, p.Dst[0]}
				}
			}
		}
		if special == nil && rec.TVDBSeason != nil && *rec.TVDBSeason == "0" && s >= 1 && s <= len(eps) {
			special = &eps[s-1]
		}
		if special == nil || !in[*special] {
			continue
		}
		ep := sh.ep(*special)
		if ep == nil || ep.AiredBeforeEpisodeNumber == nil {
			continue
		}
		want := map[int]bool{before: true}
		if r := b.m.mapEp(rec, 1, before); len(r) > 0 {
			want[r[0][1]] = true
		}
		if want[*ep.AiredBeforeEpisodeNumber] {
			if result == "" {
				result = AnchorMatch
			}
		} else {
			result = AnchorMismatch
		}
	}
	return result
}
