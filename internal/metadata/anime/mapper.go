package anime

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Stages 2 and 3: id joins and the ScudLee mapping.

// show is a TVDB series as the mapper uses it.
type show struct {
	id      int
	ok      bool
	title   string
	raw     *Show
	epKeys  []SE // document order (first occurrence)
	eps     map[SE]*ShowEpisode
	abs     map[int]SE
	failure string
}

func (s *show) ep(se SE) *ShowEpisode { return s.eps[se] }

type mapper struct {
	ctx   context.Context
	src   *Sources
	shows map[int]*show
}

func newMapper(ctx context.Context, src *Sources) *mapper {
	return &mapper{ctx: ctx, src: src, shows: map[int]*show{}}
}

func (m *mapper) show(id int) *show {
	if s, ok := m.shows[id]; ok {
		return s
	}
	s := &show{id: id, eps: map[SE]*ShowEpisode{}, abs: map[int]SE{}}
	d, err := m.src.TVDB.Show(m.ctx, id)
	if err != nil || d == nil {
		if err != nil {
			s.failure = err.Error()
		}
		d = &Show{}
	}
	s.raw = d
	s.ok = d.HasEpisodes
	if d.Title != nil {
		s.title = *d.Title
	} else {
		s.title = fmt.Sprintf("(TVDB %d unavailable)", id)
	}
	for i := range d.Episodes {
		e := &d.Episodes[i]
		k := SE{e.SeasonNumber, e.EpisodeNumber}
		if _, dup := s.eps[k]; !dup {
			s.epKeys = append(s.epKeys, k)
		}
		s.eps[k] = e
	}
	for i := range d.Episodes {
		e := &d.Episodes[i]
		if e.AbsoluteEpisodeNumber != nil && *e.AbsoluteEpisodeNumber != 0 && e.SeasonNumber > 0 {
			s.abs[*e.AbsoluteEpisodeNumber] = SE{e.SeasonNumber, e.EpisodeNumber}
		}
	}
	m.shows[id] = s
	return s
}

// resolveIDs joins AniList to AniDB (Fribb, else manami) and Kitsu (manami).
func (m *mapper) resolveIDs(e *Entry) {
	if e.AniListID == nil || *e.AniListID == 0 {
		return
	}
	l := m.src.Lists
	f := l.FribbAniList(*e.AniListID)
	mn := l.Manami("anilist", *e.AniListID)
	if f != nil && f.AniDBID != nil && *f.AniDBID != 0 {
		e.AniDBID = ip(*f.AniDBID)
		e.IDSources = []string{"fribb"}
	} else if mn != nil && mn.AniDBID != nil && *mn.AniDBID != 0 {
		e.AniDBID = ip(*mn.AniDBID)
		e.IDSources = []string{"manami"}
	}
	if mn != nil {
		e.KitsuID = nil
		for _, s := range mn.Sources {
			if strings.Contains(s, "kitsu.app/anime/") {
				e.KitsuID = lastInt(s)
				break
			}
		}
	}
}

// mapEntry fills the TVDB mapping and TMDb ids from ScudLee (AniDB-keyed), applied the way HAMA does.
func (m *mapper) mapEntry(e *Entry) {
	l := m.src.Lists
	var rec *ScudLeeRec
	var fribb *FribbRec
	if e.AniDBID != nil && *e.AniDBID != 0 {
		rec = l.ScudLeeGet(*e.AniDBID)
		fribb = l.FribbAniDB(*e.AniDBID)
	}
	if fribb != nil {
		e.TMDbMovie = sortedUniqueInts(fribb.TMDbMovie)
	}
	if rec == nil {
		return
	}
	e.TMDbMovie = sortedUniqueInts(append(append([]int{}, e.TMDbMovie...), rec.TMDbMovie...))
	info := &ScudLeeInfo{Season: rec.TVDBSeason, Offset: rec.EpisodeOffset, Mappings: len(rec.Mappings)}
	if rec.tvdb() != 0 {
		info.TVDB = rec.tvdb()
	} else if rec.TVDBKind != nil {
		info.TVDB = *rec.TVDBKind
	}
	e.ScudLee = info
	if rec.tvdb() == 0 {
		if rec.TVDBKind != nil && *rec.TVDBKind != "" {
			e.note("ScudLee marks AniDB %d as '%s' (no TVDB series)", *e.AniDBID, *rec.TVDBKind)
		}
		return
	}
	n := intv(e.Episodes)
	if n == 0 {
		n = 1
	}
	var eps []SE
	unmapped := []int{}
	for ep := 1; ep <= n; ep++ {
		r := m.mapEp(rec, 1, ep)
		if r == nil {
			unmapped = append(unmapped, ep)
		} else {
			eps = append(eps, r...)
		}
	}
	var specials []Special
	for _, mp := range rec.Mappings {
		if mp.AniDBSeason != 0 {
			continue
		}
		for _, p := range mp.Pairs {
			for _, d := range p.Dst {
				if d != 0 {
					specials = append(specials, Special{p.Src, mp.TVDBSeason, d})
				}
			}
		}
		if intv(mp.Start) != 0 && intv(mp.End) != 0 && mp.Offset != nil {
			for ep := *mp.Start; ep <= *mp.End; ep++ {
				specials = append(specials, Special{ep, mp.TVDBSeason, ep + *mp.Offset})
			}
		}
	}
	e.TVDB = &TVDBMap{SeriesID: rec.tvdb(), Episodes: dedupeSE(eps), Method: "scudlee",
		AniDBSpecials: sortedSpecials(specials), UnmappedAniDBEps: unmapped}
}

func (m *mapper) mapEp(rec *ScudLeeRec, aseason, ep int) []SE {
	for _, mp := range rec.Mappings {
		if mp.AniDBSeason != aseason {
			continue
		}
		for _, p := range mp.Pairs {
			if p.Src == ep {
				var out []SE
				for _, d := range p.Dst {
					if d != 0 {
						out = append(out, SE{mp.TVDBSeason, d})
					}
				}
				return out
			}
		}
	}
	for _, mp := range rec.Mappings {
		if mp.AniDBSeason == aseason && intv(mp.Start) != 0 && intv(mp.End) != 0 && *mp.Start <= ep && ep <= *mp.End {
			return []SE{{mp.TVDBSeason, ep + intv(mp.Offset)}}
		}
	}
	if rec.TVDBSeason == nil {
		return nil
	}
	if *rec.TVDBSeason == "a" {
		if se, ok := m.show(rec.tvdb()).abs[ep+rec.EpisodeOffset]; ok {
			return []SE{se}
		}
		return nil
	}
	s, err := strconv.Atoi(strings.TrimSpace(*rec.TVDBSeason))
	if err != nil {
		return nil
	}
	return []SE{{s, ep + rec.EpisodeOffset}}
}

func dedupeSE(x []SE) []SE {
	seen := map[SE]bool{}
	out := []SE{}
	for _, v := range x {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func sortedSpecials(x []Special) []Special {
	seen := map[Special]bool{}
	out := []Special{}
	for _, v := range x {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && lessTri(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// validate checks a mapping against TVDB and Sonarr and records TVDB's first episode, placement and range labels.
func (m *mapper) validate(e *Entry, sonarr map[int]map[SE]bool) {
	t := e.TVDB
	if t == nil {
		return
	}
	s := m.show(t.SeriesID)
	t.validated = true
	t.SeriesTitle = s.title
	t.MissingOnTVDB = []SE{}
	for _, se := range t.Episodes {
		if s.ep(se) == nil {
			t.MissingOnTVDB = append(t.MissingOnTVDB, se)
		}
	}
	if sonarr != nil {
		t.sonarr = true
		if have, ok := sonarr[t.SeriesID]; ok {
			miss := []SE{}
			for _, se := range t.Episodes {
				if !have[se] {
					miss = append(miss, se)
				}
			}
			t.MissingInSonarr = miss
		} else {
			t.MissingInSonarr = "series not in Sonarr"
		}
	}
	var first *ShowEpisode
	for _, se := range t.Episodes {
		if ep := s.ep(se); ep != nil {
			first = ep
			break
		}
	}
	if first != nil {
		t.HasFirst = true
		t.FirstAir = first.AirDate
		t.FirstTitle = first.Title
		ov := str(first.Overview)
		if r := []rune(ov); len(r) > 300 {
			ov = string(r[:300])
		}
		t.Overview = ov
		airs := Airs{AfterSeason: first.AiredAfterSeasonNumber, BeforeSeason: first.AiredBeforeSeasonNumber,
			BeforeEpisode: first.AiredBeforeEpisodeNumber}
		if t.Episodes[0][0] == 0 && !airs.empty() {
			t.Airs = &airs
		}
		a, b := toDateP(e.Start), toDateP(first.AirDate)
		if a != 0 && b != 0 && e.Start != nil && len(*e.Start) == 10 && absInt(int(a-b)) > 7 {
			e.note("date mismatch: AniList/primary %s vs TVDB %s", *e.Start, pyStr(first.AirDate))
		}
	}
	t.Range = ranges(t.Episodes)
	sp := make([]SE, len(t.AniDBSpecials))
	for i, x := range t.AniDBSpecials {
		sp[i] = SE{x[1], x[2]}
	}
	t.SpecialsRange = ranges(sp)
}
