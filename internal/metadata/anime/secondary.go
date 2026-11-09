package anime

import (
	"fmt"
	"strings"
)

// Stage 5 (first half): entries AniList lacks (secondaryEntries).

var seasonMonth = map[string]int{"WINTER": 1, "SPRING": 4, "SUMMER": 7, "FALL": 10}

var kitsuRoles = setOf("sequel", "prequel", "alternative_version", "alternative_setting", "side_story",
	"parent_story", "summary", "full_story", "spinoff")

// dateTolerance is days of slack for comparing dates of different precision.
func dateTolerance(ds ...*string) int {
	p := -1
	for _, d := range ds {
		if l := len(str(d)); p < 0 || l < p {
			p = l
		}
	}
	switch {
	case p >= 10:
		return 3
	case p >= 7:
		return 45
	}
	return 400
}

func (b *builder) anidbEntry(adb int, why string) *Entry {
	l := b.src.Lists
	mn := l.Manami("anidb", adb)
	if mn == nil {
		mn = &ManamiRec{Synonyms: []string{}}
	}
	rec := l.ScudLeeGet(adb)
	if mn.AniListID != nil && *mn.AniListID != 0 {
		return nil // AniList has it; the AniList crawl decides membership
	}
	var start *string
	if mn.Season != nil && mn.Season.Year != nil && *mn.Season.Year != 0 {
		month, ok := seasonMonth[str(mn.Season.Season)]
		if !ok {
			month = 1
		}
		start = sp(fmt.Sprintf("%04d-%02d", *mn.Season.Year, month))
	}
	var recName *string
	if rec != nil {
		recName = &rec.Name
	}
	title := mn.Title
	if title == nil || *title == "" {
		title = recName
	}
	names := nonEmpty(mn.Title, recName)
	for _, s := range mn.Synonyms {
		if s != "" {
			names = append(names, s)
		}
	}
	return &Entry{
		Key: fmt.Sprintf("adb:%d", adb), Title: title, Names: names, AniDBID: ip(adb),
		Format: mn.Type, Start: start, StartApprox: true, Episodes: mn.Episodes, DurationMin: mn.DurationMin,
		AniListRelations: []Relation{}, FoundVia: sp(why), Origin: "anidb", TMDbMovie: []int{},
		Flags: []string{"not-on-anilist"}, Notes: []string{},
	}
}

func (b *builder) secondaryEntries(entries []*Entry, tvdbIDs map[int]bool, seedNames []string) []*Entry {
	l := b.src.Lists
	haveAdb := newPySet()
	haveKitsu := map[int]bool{}
	for _, e := range entries {
		if e.AniDBID != nil && *e.AniDBID != 0 {
			haveAdb.Add(*e.AniDBID)
		}
	}
	for _, e := range entries {
		if e.KitsuID != nil && *e.KitsuID != 0 {
			haveKitsu[*e.KitsuID] = true
		}
	}
	var out []*Entry

	// (a) ScudLee entries pointing at the franchise's TVDB series
	for _, adb := range l.ScudLeeOrder {
		rec := l.ScudLee[adb]
		if tvdbIDs[rec.tvdb()] && !haveAdb.Has(adb) {
			if e := b.anidbEntry(adb, fmt.Sprintf("ScudLee maps AniDB %d to TVDB %d", adb, rec.tvdb())); e != nil {
				out = append(out, e)
				haveAdb.Add(adb)
			}
		}
	}
	// (b) manami links between AniDB entries (two-way links only, to avoid crossovers)
	frontier := haveAdb.Copy()
	seen := map[int]bool{}
	for _, x := range frontier.Items() {
		seen[x] = true
	}
	for frontier.Len() > 0 {
		nxt := newPySet()
		for _, adb := range frontier.Items() {
			mn := l.Manami("anidb", adb)
			if mn == nil {
				continue
			}
			for _, r := range mn.Related {
				if !strings.Contains(r, "anidb.net/anime/") {
					continue
				}
				op := lastInt(r)
				if op == nil {
					continue
				}
				other := *op
				if seen[other] {
					continue
				}
				om := l.Manami("anidb", other)
				if om == nil || !contains(om.Related, fmt.Sprintf("https://anidb.net/anime/%d", adb)) {
					continue
				}
				seen[other] = true
				rec := l.ScudLeeGet(other)
				anchored := rec != nil && tvdbIDs[rec.tvdb()]
				stemOK := false
				for _, n := range seedNames {
					s := stem(n)
					if len(s) >= 4 && strings.HasPrefix(normWords(str(om.Title)), s) {
						stemOK = true
						break
					}
				}
				if !anchored && (rec.tvdb() != 0 || !stemOK) {
					continue // untyped link to another series (Fate -> Kara no Kyoukai)
				}
				if e := b.anidbEntry(other, fmt.Sprintf("manami two-way link from AniDB %d", adb)); e != nil {
					out = append(out, e)
					haveAdb.Add(other)
					nxt.Add(other)
				}
			}
		}
		frontier = nxt
	}
	// (c) Kitsu relationships of members; keep destinations known to neither AniList nor AniDB
	for _, ent := range append([]*Entry(nil), entries...) {
		if ent.KitsuID == nil || *ent.KitsuID == 0 {
			continue
		}
		k, err := b.src.Kitsu.Anime(b.ctx, *ent.KitsuID)
		if err != nil {
			ent.note("Kitsu fetch failed: %v", err)
			continue
		}
		type rk struct{ t, id string }
		inc := map[rk]*KitsuIncluded{}
		for i := range k.Included {
			x := &k.Included[i]
			inc[rk{x.Type, x.ID}] = x
		}
		for _, rel := range k.Data.Relationships.MediaRelationships.Data {
			r := inc[rk{"mediaRelationships", rel.ID}]
			if r == nil {
				continue
			}
			role := r.Attributes.Role
			dest := r.Relationships.Destination.Data
			if dest == nil || dest.Type != "anime" || !kitsuRoles[role] {
				continue
			}
			kp := pyNum(dest.ID)
			if kp == nil {
				continue
			}
			kid := *kp
			mn := l.Manami("kitsu", kid)
			if haveKitsu[kid] || (mn != nil && (intv(mn.AniListID) != 0 || intv(mn.AniDBID) != 0)) {
				continue
			}
			var a KitsuIncluded
			if x := inc[rk{"anime", dest.ID}]; x != nil {
				a = *x
			}
			at := a.Attributes
			haveKitsu[kid] = true
			same := func(x *Entry) bool {
				sim := titleSim(x.Names, str(at.CanonicalTitle), false)
				if str(x.Start) == "" || str(at.StartDate) == "" {
					return sim >= 0.9
				}
				days := absInt(int(or(toDateP(x.Start), pyDateMin) - or(toDateP(at.StartDate), pyDateMax)))
				return days <= dateTolerance(x.Start, at.StartDate) && sim >= 0.6
			}
			var dup *Entry
			for _, x := range entries {
				if same(x) {
					dup = x
					break
				}
			}
			if dup != nil && role != "alternative_version" {
				dup.note("Kitsu %d '%s' looks like a duplicate of this entry", kid, pyStr(at.CanonicalTitle))
				continue
			}
			names := nonEmpty(at.CanonicalTitle)
			for _, v := range at.Titles.Values {
				if v != nil && *v != "" {
					names = append(names, *v)
				}
			}
			if mn != nil {
				for _, s := range mn.Synonyms {
					if s != "" {
						names = append(names, s)
					}
				}
			}
			var titleEn *string
			for i, key := range at.Titles.Keys {
				if key == "en" {
					titleEn = at.Titles.Values[i]
				}
			}
			var format *string
			if s := strings.ToUpper(str(at.Subtype)); s != "" {
				format = &s
			}
			out = append(out, &Entry{
				Key: fmt.Sprintf("kitsu:%d", kid), Title: at.CanonicalTitle, TitleEn: titleEn, Names: names,
				KitsuID: ip(kid), Format: format, Start: at.StartDate, End: at.EndDate, Episodes: at.EpisodeCount,
				DurationMin: at.EpisodeLength, AniListRelations: []Relation{},
				KitsuRole: &KitsuRole{Role: role, Of: ent.Key},
				FoundVia:  sp(fmt.Sprintf("Kitsu %s of %s (kitsu %d)", role, ent.Key, *ent.KitsuID)), Origin: "kitsu",
				TMDbMovie: []int{}, Flags: []string{"not-on-anilist", "not-on-anidb"}, Notes: []string{},
			})
		}
	}
	return out
}
