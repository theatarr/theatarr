package anime

import (
	"fmt"
	"strings"
)

// Fix-ups between mapping and ordering (mergeSameTitle, mergeDuplicates, splitFarApart,
// dropSecondaryDuplicates, crossCheckAnibridge).

func titles(xs ...*string) []string { return nonEmpty(xs...) }

// mergeSameTitle: an unmapped AniList entry and an AniDB-only entry with the same title are one work released
// twice (Zoku Owarimonogatari). The AniList entry adopts the AniDB id and is re-mapped.
func (b *builder) mergeSameTitle(entries []*Entry) []*Entry {
	var al []*Entry
	for _, e := range entries {
		if e.Origin == "anilist" && e.TVDB == nil && (e.AniDBID == nil || *e.AniDBID == 0) {
			al = append(al, e)
		}
	}
	var out []*Entry
	for _, e := range entries {
		if e.Origin == "anidb" {
			prim := titles(e.Title)
			if len(e.Names) > 1 && e.Names[1] != "" {
				prim = append(prim, e.Names[1])
			}
			var twin *Entry
			for _, a := range al {
				if jaccard(titles(a.Title, a.TitleEn), prim) >= 0.9 {
					twin = a
					break
				}
			}
			if twin != nil {
				twin.AniDBID = ip(*e.AniDBID)
				twin.note("same title as AniDB %d (%s, %s); mapped through it", *e.AniDBID, pyStr(e.Format), pyStr(e.Start))
				b.m.mapEntry(twin)
				b.arbitrate(twin)
				if len(twin.TMDbMovie) == 0 {
					twin.TMDbMovie = e.TMDbMovie
				}
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

func claimSet(t *TVDBMap) map[claimKey]bool {
	m := map[claimKey]bool{}
	for _, se := range t.Episodes {
		m[claimKey{t.SeriesID, se}] = true
	}
	return m
}

// mergeDuplicates folds an AniDB-only entry into the AniList entries whose TVDB episodes it shares (AniDB groups
// the two Made in Abyss recap films), and hands grouped TMDb ids out by release date.
func (b *builder) mergeDuplicates(entries []*Entry) []*Entry {
	var al []*Entry
	for _, e := range entries {
		if e.Origin == "anilist" && e.TVDB != nil {
			al = append(al, e)
		}
	}
	var out []*Entry
	for _, e := range entries {
		if e.Origin == "anidb" && e.TVDB != nil {
			mine := claimSet(e.TVDB)
			var covering []*Entry
			for _, a := range al {
				for k := range claimSet(a.TVDB) {
					if mine[k] {
						covering = append(covering, a)
						break
					}
				}
			}
			if len(covering) > 0 {
				for _, a := range covering {
					var others []string
					for _, c := range covering {
						if c != a {
							others = append(others, c.Key)
						}
					}
					with := strings.Join(others, ", ")
					if with == "" {
						with = "nothing else"
					}
					a.note("AniDB %d ('%s') groups this with %s", *e.AniDBID, pyStr(e.Title), with)
					if a.AniDBID == nil || *a.AniDBID == 0 {
						a.AniDBID = ip(*e.AniDBID)
					}
					if len(e.TMDbMovie) > 0 && len(a.TMDbMovie) == 0 {
						a.tmdbCandidates = e.TMDbMovie
					}
				}
				continue
			}
		}
		out = append(out, e)
	}
	for _, a := range out {
		cands := a.tmdbCandidates
		a.tmdbCandidates = nil
		if len(cands) == 0 {
			continue
		}
		bestDiff, bestID, found := 0, 0, false
		for _, t := range cands {
			info := b.lookupTMDb(t)
			// Deviation: the golden outputs parse Radarr's inCinemas ("2019-01-04T00:00:00Z") whole here, which never
			// parses, so this step never ran for them; this reads the date part, as checkTMDb does, because the step
			// is meant to pick the TMDb movie whose date is closest. No output of the twelve example franchises
			// (examples/policy/overrides/franchises.yaml) changes.
			var ic *string
			if info != nil && info.InCinemas != nil {
				v := *info.InCinemas
				if len(v) > 10 {
					v = v[:10]
				}
				ic = &v
			}
			d1, d2 := toDateP(ic), toDateP(a.Start)
			if d1 != 0 && d2 != 0 {
				diff := absInt(int(d1 - d2))
				if !found || diff < bestDiff {
					bestDiff, bestID, found = diff, t, true
				}
			}
		}
		if found && bestDiff <= 30 {
			a.TMDbMovie = []int{bestID}
			a.note("TMDb %d picked from grouped AniDB ids %s by release date", bestID, pyIntList(cands))
		}
	}
	return out
}

// dropSecondaryDuplicates: after splitting, a Kitsu or AniDB-only entry may duplicate a split part.
func dropSecondaryDuplicates(entries []*Entry) []*Entry {
	var out []*Entry
	for _, e := range entries {
		if (e.Origin == "kitsu" || e.Origin == "anidb") && (e.KitsuRole == nil || e.KitsuRole.Role != "alternative_version") {
			var dup *Entry
			for _, x := range entries {
				if x == e || x.Origin != "anilist" || str(x.Start) == "" || str(e.Start) == "" || len(x.splitInto) > 0 {
					continue
				}
				if absInt(int(toDateP(x.Start)-toDateP(e.Start))) > dateTolerance(x.Start, e.Start) {
					continue
				}
				if !(ptrEq(x.Format, e.Format) || (*x.Start == *e.Start && len(*e.Start) == 10)) {
					continue
				}
				other := titles(e.Title, e.TitleEn)
				if len(e.Names) > 1 && e.Names[1] != "" {
					other = append(other, e.Names[1])
				}
				if jaccard(titles(x.Title, x.TitleEn), other) >= 0.6 {
					dup = x
					break
				}
			}
			if dup != nil {
				dup.note("%s '%s' is the same work on another site (merged)", e.Key, pyStr(e.Title))
				if e.KitsuID != nil && *e.KitsuID != 0 && (dup.KitsuID == nil || *dup.KitsuID == 0) {
					dup.KitsuID = ip(*e.KitsuID)
				}
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

func ptrEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// splitFarApart splits an AniList entry whose episodes were released more than 180 days apart (Re:Zero's OVAs).
func (b *builder) splitFarApart(entries []*Entry) []*Entry {
	var out []*Entry
	for _, e := range entries {
		tv := e.TVDB
		var eps []SE
		if tv != nil {
			eps = tv.Episodes
		}
		f := str(e.Format)
		if (f != "OVA" && f != "SPECIAL" && f != "ONA" && f != "MOVIE") || len(eps) < 2 {
			out = append(out, e)
			continue
		}
		sh := b.m.show(tv.SeriesID)
		dates := make([]pyDate, len(eps))
		missing, near := false, true
		for i, se := range eps {
			var air *string
			if ep := sh.ep(se); ep != nil {
				air = ep.AirDate
			}
			dates[i] = toDateP(air)
			if dates[i] == 0 {
				missing = true
			}
			if i > 0 && dates[i]-dates[i-1] > 180 {
				near = false
			}
		}
		if missing || near {
			out = append(out, e)
			continue
		}
		e.splitInto = []string{}
		first := len(out)
		for i, se := range eps {
			n := i + 1
			ep := sh.ep(se)
			sub := e.clone()
			sub.Key = fmt.Sprintf("%s#%d", e.Key, n)
			sub.Title = sp(fmt.Sprintf("%s #%d: %s", pyStr(e.Title), n, pyStr(ep.Title)))
			d := dates[i].iso()
			sub.Start, sub.End = sp(d), sp(d)
			sub.Episodes = ip(1)
			sub.ParentKey = e.Key
			t := tv.copyMap()
			t.Episodes = []SE{se}
			sub.TVDB = t
			sub.Flags = append([]string{}, e.Flags...)
			sub.Notes = []string{}
			for _, x := range e.Notes {
				if !strings.HasPrefix(x, "TMDb id from") {
					sub.Notes = append(sub.Notes, x)
				}
			}
			sub.TMDbMovie = []int{}
			sub.splitInto = nil
			base := e.TitleEn
			if str(base) == "" {
				base = e.Title
			}
			sub.TitleEn = sp(fmt.Sprintf("%s #%d: %s", pyStr(base), n, pyStr(ep.Title)))
			sub.note("split from %s (episodes released %s .. %s)", e.Key, dates[0].iso(), dates[len(dates)-1].iso())
			if e.Mapping != nil {
				m := *e.Mapping
				sub.Mapping = &m
			}
			e.splitInto = append(e.splitInto, sub.Key)
			out = append(out, sub)
		}
		tm := e.TMDbMovie
		if e.Anibridge != nil && len(e.Anibridge.TMDbMovie) > 0 {
			tm = e.Anibridge.TMDbMovie
		}
		if len(tm) > 0 {
			out[first].note("parent TMDb ids %s not assigned per part", pyIntList(tm))
		}
	}
	return out
}

func crossCheckAnibridge(e *Entry) {
	var ab [][3]int
	if e.Anibridge != nil {
		ab = e.Anibridge.TVDB
	}
	tv := e.TVDB
	if len(ab) == 0 || tv == nil || len(tv.Episodes) == 0 || tv.Method == "anibridge" {
		if len(ab) == 0 {
			e.AnibridgeAgrees = nil
		} else {
			t := true
			e.AnibridgeAgrees = &t
		}
		return
	}
	mine := claimSet(tv)
	theirs := map[claimKey]bool{}
	for _, t := range ab {
		k := claimKey{t[0], SE{t[1], t[2]}}
		if !strings.Contains(e.Key, "#") || mine[k] {
			theirs[k] = true
		}
	}
	agree := len(mine) == len(theirs)
	for k := range mine {
		if !theirs[k] {
			agree = false
		}
	}
	e.AnibridgeAgrees = &agree
}
