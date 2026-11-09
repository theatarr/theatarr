package anime

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The heuristic TVDB match and stage 6, flags (heuristicMap, setFlags, target).

var (
	recapWords    = wordAlts("recap", "compilation", "summary", "digest", "soushuuhen", "henshuu", "henshuuban", "re-?edit", "retrospective", "omnibus", "soushuu")
	overviewRecap = regexp.MustCompile(`(?i)(compil|recap|summar|first \p{Nd}+ episodes|episodes \p{Nd}+[\s\p{Z}\x{85}\x{1c}-\x{1f}]*(-|to)[\s\p{Z}\x{85}\x{1c}-\x{1f}]*\p{Nd}+)`)
	extraWords    = wordAlts("making of", "interview", "music in", "theater manners", "commercial", "pv", "promo", "live[- ]action", "commentary", "special event", "stage play", "concert", "trailer", "cm")
	directorsCut  = regexp.MustCompile(`(?i)director'?s cut|shin henshuu|new edit`)
)

// wordAlts is Python's re.compile(r"\b(a|b|...)\b", re.I).
func wordAlts(alts ...string) *pyRE {
	var a []pyAlt
	for _, x := range alts {
		a = append(a, pyAlt{x, true, true})
	}
	return newPyRE(true, a...)
}

var formatRank = map[string]int{"TV": 0, "TV_SHORT": 1, "MOVIE": 2, "OVA": 3, "ONA": 4, "SPECIAL": 5}

func fmtRank(f *string) int {
	if f == nil {
		return 9
	}
	if r, ok := formatRank[*f]; ok {
		return r
	}
	return 9
}

type claimKey struct {
	tid int
	se  SE
}

// heuristicMap matches an unmapped entry to unclaimed TVDB episodes by air-date window and title similarity.
func (b *builder) heuristicMap(e *Entry, tvdbIDs []int, claimed map[claimKey]bool) {
	start, end := toDateP(e.Start), toDateP(e.End)
	if start == 0 {
		return
	}
	approx := e.StartApprox || len(str(e.Start)) < 10
	lo := start - 7
	if approx {
		lo = start - 95
	}
	hi := or(end, start) + 7
	if approx || end == 0 {
		hi = or(end, start) + 95
	}
	if approx && start+120 > hi {
		hi = start + 120
	}
	n := intv(e.Episodes)
	if n == 0 {
		n = 1
	}
	type cand struct {
		sim  float64
		air  string
		tid  int
		s, x int
	}
	var best []cand
	names2 := e.Names
	if len(names2) > 2 {
		names2 = names2[:2]
	}
	joined := strings.Join(e.Names, " ")
	for _, tid := range tvdbIDs {
		sh := b.m.show(tid)
		for _, se := range sh.epKeys {
			ep := sh.eps[se]
			if claimed[claimKey{tid, se}] {
				continue
			}
			d := toDateP(ep.AirDate)
			nearSame := jaccard(names2, []string{str(ep.Title)}) >= 0.8
			if d == 0 || !((lo <= d && d <= hi) || (nearSame && absInt(int(d-start)) <= 366)) {
				continue
			}
			if extraWords.MatchString(str(ep.Title)) && !extraWords.MatchString(joined) {
				continue
			}
			if sim := titleSim(e.Names, str(ep.Title), true); sim >= 0.34 {
				best = append(best, cand{sim, str(ep.AirDate), tid, se[0], se[1]})
			}
		}
	}
	if len(best) == 0 {
		return
	}
	sort.SliceStable(best, func(i, j int) bool {
		if best[i].sim != best[j].sim {
			return best[i].sim > best[j].sim
		}
		return best[i].air < best[j].air
	})
	if len(best) > n {
		best = best[:n]
	}
	pick := append([]cand(nil), best...)
	sort.SliceStable(pick, func(i, j int) bool {
		a, c := pick[i], pick[j]
		if a.tid != c.tid {
			return a.tid < c.tid
		}
		if a.s != c.s {
			return a.s < c.s
		}
		return a.x < c.x
	})
	tid := pick[0].tid
	var eps []SE
	minSim := 2.0
	for _, p := range pick {
		if p.tid == tid {
			eps = append(eps, SE{p.s, p.x})
			if p.sim < minSim {
				minSim = p.sim
			}
		}
	}
	e.TVDB = &TVDBMap{SeriesID: tid, Episodes: eps, Method: "heuristic",
		Heuristic:     &Heuristic{MinTitleSim: round2(minSim), Picked: len(eps), Wanted: n},
		AniDBSpecials: []Special{}, UnmappedAniDBEps: []int{}}
	if len(eps) != n {
		e.note("heuristic matched %d of %d episodes", len(eps), n)
	}
}

func (b *builder) setFlags(e *Entry, byKey map[string]*Entry) {
	f := map[string]bool{}
	for _, x := range e.Flags {
		f[x] = true
	}
	rels := e.AniListRelations
	for _, other := range byKey {
		for _, r := range other.AniListRelations {
			if e.AniListID != nil && r.ID == *e.AniListID && r.Type == "SUMMARY" {
				f["recap"] = true
			}
		}
	}
	for _, r := range rels {
		if r.Type == "COMPILATION" {
			f["recap"] = true
		}
	}
	var partners []*Entry
	for _, r := range rels {
		if r.Type != "ALTERNATIVE" {
			continue
		}
		p := byKey[fmt.Sprintf("al:%d", r.ID)]
		if p != nil && !p.hasFlag("extra") && !extraWords.MatchString(str(p.Title)) {
			partners = append(partners, p)
		}
	}
	if len(partners) > 0 || (e.KitsuRole != nil && e.KitsuRole.Role == "alternative_version") {
		f["alternate-version"] = true
	}
	if len(partners) == 1 {
		// a film whose only alternate is one TV arc that aired before it is a compilation (AoT Crimson Bow, JJK).
		// Films with several alternates (UBW 2010 vs the 2006 and 2014 series, Heaven's Feel) are other routes.
		p := partners[0]
		pEnd := p.End
		if str(pEnd) == "" {
			pEnd = p.Start
		}
		hasGap := str(e.Start) != "" && str(pEnd) != ""
		gap := 0
		if hasGap {
			gap = int(or(toDateP(e.Start), pyDateMax) - or(toDateP(pEnd), pyDateMin))
		}
		if str(e.Format) == "MOVIE" && str(p.Format) == "TV" && intv(p.Episodes) >= 6 && hasGap && gap > 0 && gap <= 3*366 {
			// within 3 years: compilation. Later re-tellings (SAO Progressive, 9 years on) stay alternates.
			f["recap"] = true
			e.note("film re-telling %s after it aired: treated as a compilation", p.Key)
		}
	}
	names := strings.Join(e.Names, " ") + " "
	overview := ""
	if e.TVDB != nil {
		names += str(e.TVDB.FirstTitle)
		overview = e.TVDB.Overview
	}
	if recapWords.MatchString(names) || overviewRecap.MatchString(overview) {
		f["recap?"] = true
	}
	if kr := e.KitsuRole; kr != nil {
		if kr.Role == "summary" {
			f["recap"] = true
		}
		if kr.Role == "alternative_version" && str(e.Format) == "MOVIE" {
			if of := byKey[kr.Of]; of != nil && str(of.Format) == "TV" {
				f["recap?"] = true // a film "alternative version" of a TV arc is a compilation (To the Swordsmith Village)
				e.note("Kitsu calls this an alternative version of a TV arc; treated as a compilation film")
			}
		}
	}
	if extraWords.MatchString(names) {
		f["extra"] = true
	}
	if directorsCut.MatchString(names) {
		f["directors-cut"] = true
	}
	parent := false
	for _, r := range rels {
		if r.Type == "PARENT" {
			parent = true
		}
	}
	dur := 99
	if e.DurationMin != nil && *e.DurationMin != 0 {
		dur = *e.DurationMin
	}
	if parent && (str(e.Format) == "TV_SHORT" || str(e.Format) == "ONA") && dur <= 6 {
		f["chibi-short"] = true
	}
	if dur <= 6 {
		f["short"] = true
	}
	if str(e.Start) == "" || sortDate(e.Start) > b.today {
		f["unaired"] = true
	}
	if f["recap"] || f["directors-cut"] {
		delete(f, "recap?")
	}
	e.Flags = sortedFlags(f)
}

// target is where an entry goes: sonarr, sonarr S00, radarr, both, or none.
func target(e *Entry) string {
	var eps []SE
	if e.TVDB != nil {
		eps = e.TVDB.Episodes
	}
	special := len(eps) > 0
	for _, x := range eps {
		if x[0] != 0 {
			special = false
		}
	}
	regular := len(eps) > 0 && !special
	movie := len(e.TMDbMovie) > 0
	switch {
	case regular && movie:
		return "sonarr + radarr"
	case regular:
		return "sonarr"
	case special && movie:
		return "both (sonarr S00 + radarr)"
	case special:
		return "sonarr S00"
	case movie:
		return "radarr"
	}
	return "none"
}
