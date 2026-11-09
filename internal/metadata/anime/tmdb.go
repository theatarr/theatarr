package anime

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// TMDb ids, TVDB-only entries, gaps and the XEM summary (findTMDb, checkTMDb, tvdbOnlyEntries,
// regularGaps, xemSummary).

// effDate is the best release date: an exact AniList/Kitsu date, else the TVDB air date, else the season.
func effDate(e *Entry) *string {
	if s := e.Start; s != nil && len(*s) == 10 && !e.StartApprox {
		return s
	}
	if e.TVDB != nil && str(e.TVDB.FirstAir) != "" {
		return e.TVDB.FirstAir
	}
	return e.Start
}

func (b *builder) lookupTMDb(id int) *MovieInfo {
	if b.src.Movies == nil {
		return nil
	}
	info, err := b.src.Movies.LookupTMDb(b.ctx, id)
	if err != nil {
		return nil
	}
	return info
}

var moviePrefix = regexp.MustCompile(`^Movie: `)

// findTMDb searches movie-like entries without a TMDb id by title, and accepts a release within 7 days.
func (b *builder) findTMDb(e *Entry, byKey map[string]*Entry) {
	f := str(e.Format)
	dur := intv(e.DurationMin)
	eps := intv(e.Episodes)
	if eps == 0 {
		eps = 1
	}
	movieLike := f == "MOVIE" || ((f == "OVA" || f == "SPECIAL" || f == "TVDB_SPECIAL") && eps == 1 && dur >= 40)
	ed := str(effDate(e))
	if len(e.TMDbMovie) > 0 || !movieLike || e.hasFlag("extra") || ed == "" || len(ed) < 10 {
		return
	}
	parent := byKey[e.ParentKey]
	var tvt *string
	if e.TVDB != nil {
		tvt = e.TVDB.FirstTitle
	}
	var terms []*string
	if parent != nil {
		pt := parent.TitleEn
		if str(pt) == "" {
			pt = parent.Title
		}
		terms = append(terms, sp(strings.TrimSpace(strings.SplitN(str(pt), " OVA", 2)[0]+" "+str(tvt))))
	}
	terms = append(terms, e.TitleEn, e.Title)
	if str(tvt) != "" {
		tvTerm := sp(e.TVDB.SeriesTitle + " " + moviePrefix.ReplaceAllString(*tvt, ""))
		if e.Origin == "tvdb" {
			terms = []*string{tvTerm}
		} else {
			terms = append(terms[:min(2, len(terms)):min(2, len(terms))], tvTerm)
		}
	}
	d := toDate(ed)
	var use []string
	for _, t := range terms {
		if str(t) != "" {
			use = append(use, *t)
		}
	}
	if len(use) > 3 {
		use = use[:3]
	}
	if b.src.Movies == nil {
		return
	}
	for _, term := range use {
		res, err := b.src.Movies.Search(b.ctx, term)
		if err != nil {
			continue
		}
		if len(res) > 10 {
			res = res[:10]
		}
		for _, c := range res {
			ic := str(c.InCinemas)
			if len(ic) > 10 {
				ic = ic[:10]
			}
			cd := toDate(ic)
			if cd != 0 && absInt(int(cd-d)) <= 7 &&
				titleSim(e.Names, pyStr(c.Title)+" "+str(c.OriginalTitle), true) >= 0.3 {
				e.TMDbMovie = []int{c.TMDbID}
				e.note("TMDb %d found by Radarr title search '%s' (date match)", c.TMDbID, term)
				return
			}
		}
	}
}

// checkTMDb looks a TMDb id up and rejects it when both the title and the date disagree.
func (b *builder) checkTMDb(id int, e *Entry) TMDbCheck {
	info := b.lookupTMDb(id)
	if info == nil {
		return TMDbCheck{TMDbID: id, LookupFailed: true}
	}
	res := TMDbCheck{TMDbID: id, Title: info.Title, Year: info.Year, InCinemas: info.InCinemas}
	if info.Collection != nil {
		res.Collection = info.Collection.Title
	}
	ic := str(info.InCinemas)
	if len(ic) > 10 {
		ic = ic[:10]
	}
	d1, d2 := toDate(ic), toDateP(effDate(e))
	dateOK := d1 != 0 && d2 != 0 && absInt(int(d1-d2)) <= 45
	if d1 != 0 && d2 != 0 && !dateOK {
		res.Warning = fmt.Sprintf("TMDb release %s vs entry %s", d1, d2)
	}
	sim := titleSim(e.Names, pyStr(info.Title)+" "+str(info.OriginalTitle), true)
	if !dateOK && sim < 0.3 {
		res.Rejected = true
	}
	return res
}

// XEMSummary is how TheXEM's scene numbering differs from TVDB's for a series.
type XEMSummary struct {
	Error    string
	Rows     int
	Differs  int
	Examples []string
}

func (x XEMSummary) MarshalJSON() ([]byte, error) {
	o := newObj()
	if x.Error != "" {
		o.add("error", x.Error)
		return o.bytes()
	}
	o.add("rows", x.Rows)
	o.add("scene_differs_from_tvdb", x.Differs)
	o.add("examples", nonNilStrings(x.Examples))
	return o.bytes()
}

func (b *builder) xemSummary(tid int) XEMSummary {
	if b.src.XEM == nil {
		return XEMSummary{Error: "no XEM source"}
	}
	m, err := b.src.XEM.Map(b.ctx, tid)
	if err != nil {
		return XEMSummary{Error: err.Error()}
	}
	out := XEMSummary{Rows: len(m.Data)}
	for _, r := range m.Data {
		if r.Scene == nil || r.TVDB == nil || (*r.Scene == XEMNumber{}) || (*r.TVDB == XEMNumber{}) {
			continue
		}
		if r.Scene.Season != r.TVDB.Season || r.Scene.Episode != r.TVDB.Episode {
			out.Differs++
			if len(out.Examples) < 3 {
				out.Examples = append(out.Examples, fmt.Sprintf("TVDB S%02dE%02d = scene S%02dE%02d",
					r.TVDB.Season, r.TVDB.Episode, r.Scene.Season, r.Scene.Episode))
			}
		}
	}
	return out
}

// tvdbOnlyEntries keeps TVDB Season 0 episodes no entry claims, so the orders can place them by airsBefore/After.
func (b *builder) tvdbOnlyEntries(tvdbIDs []int, claimed map[claimKey]bool, entries []*Entry) []*Entry {
	type owner struct {
		key string
		src int
	}
	specialsOf := map[claimKey]owner{}
	for _, e := range entries {
		if e.TVDB == nil {
			continue
		}
		for _, x := range e.TVDB.AniDBSpecials {
			specialsOf[claimKey{e.TVDB.SeriesID, SE{x[1], x[2]}}] = owner{e.Key, x[0]}
		}
	}
	var out []*Entry
	for _, tid := range tvdbIDs {
		sh := b.m.show(tid)
		keys := append([]SE(nil), sh.epKeys...)
		sortSE(keys)
		for _, se := range keys {
			ep := sh.eps[se]
			if se[0] != 0 || claimed[claimKey{tid, se}] {
				continue
			}
			title := str(ep.Title)
			airs := Airs{AfterSeason: ep.AiredAfterSeasonNumber, BeforeSeason: ep.AiredBeforeSeasonNumber,
				BeforeEpisode: ep.AiredBeforeEpisodeNumber}
			ent := &Entry{
				Key: fmt.Sprintf("tvdb:%d:S00E%02d", tid, se[1]), Title: sp(title), Names: []string{title},
				Format: sp("TVDB_SPECIAL"), Start: ep.AirDate, End: ep.AirDate, Episodes: ip(1),
				DurationMin: ep.Runtime, AniListRelations: []Relation{}, Origin: "tvdb",
				FoundVia: sp("TVDB Season 0, not claimed by any entry"),
				TVDB: &TVDBMap{SeriesID: tid, Episodes: []SE{{0, se[1]}}, Method: "tvdb-only", Range: fmt.Sprintf("S00E%02d", se[1]),
					SeriesTitle: sh.title, FirstAir: ep.AirDate, FirstTitle: sp(title), Airs: &airs, tvdbOnly: true},
				TMDbMovie: []int{}, Flags: []string{}, Notes: []string{},
				Mapping: &MappingOutcome{Status: StatusAuto, Winner: "tvdb-only", Reason: "TVDB Season 0 episode no entry claims"},
			}
			if o, ok := specialsOf[claimKey{tid, SE{0, se[1]}}]; ok {
				ent.note("ScudLee: AniDB special S%d of %s", o.src, o.key)
				ent.SpecialOf = o.key
			}
			b.setFlags(ent, map[string]*Entry{})
			if ov := str(ep.Overview); ov != "" && recapWords.MatchString(ov) && !ent.hasFlag("recap?") {
				ent.Flags = append(ent.Flags, "recap?")
				f := map[string]bool{}
				for _, x := range ent.Flags {
					f[x] = true
				}
				ent.Flags = sortedFlags(f)
			}
			ent.Target = "sonarr S00"
			out = append(out, ent)
		}
	}
	return out
}

func sortSE(x []SE) {
	for i := 1; i < len(x); i++ {
		for j := i; j > 0 && x[j].less(x[j-1]); j-- {
			x[j], x[j-1] = x[j-1], x[j]
		}
	}
}

func (b *builder) regularGaps(tvdbIDs []int, claimed map[claimKey]bool) []string {
	out := []string{}
	for _, tid := range tvdbIDs {
		sh := b.m.show(tid)
		keys := append([]SE(nil), sh.epKeys...)
		sortSE(keys)
		for _, se := range keys {
			if se[0] > 0 && !claimed[claimKey{tid, se}] {
				ep := sh.eps[se]
				out = append(out, fmt.Sprintf("%d %s %s %s", tid, se, pyStr(ep.AirDate), pyStr(ep.Title)))
			}
		}
	}
	return out
}

// UnmarshalJSON reads a stored summary.
func (x *XEMSummary) UnmarshalJSON(b []byte) error {
	var v struct {
		Error    string   `json:"error"`
		Rows     int      `json:"rows"`
		Differs  int      `json:"scene_differs_from_tvdb"`
		Examples []string `json:"examples"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*x = XEMSummary{Error: v.Error, Rows: v.Rows, Differs: v.Differs, Examples: v.Examples}
	return nil
}
