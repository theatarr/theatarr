package anime

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// Lists are the static mapping lists and id joins.
//
// Licences: anibridge-mappings is MIT; manami's database is ODbL 1.0 + DbCL 1.0; Anime-Lists (ScudLee) and Fribb
// publish no licence file. NOTICE credits the excerpts kept in testdata.
type Lists struct {
	ScudLee      map[int]*ScudLeeRec
	ScudLeeOrder []int // anidb ids in document order (first occurrence)

	FribbByAniList map[int]*FribbRec
	FribbByAniDB   map[int]*FribbRec

	Anibridge     map[string]map[string]map[string]string // "anilist:<id>" -> target -> source range -> target range
	AnibridgeMeta json.RawMessage

	manami map[manamiKey]*ManamiRec
}

type manamiKey struct {
	site string
	id   int
}

// ScudLeeRec is one <anime> of anime-list-master.xml.
type ScudLeeRec struct {
	AniDBID       int
	Name          string
	TVDBID        *int    // nil when tvdbid is not a number (0 counts as none)
	TVDBKind      *string // "movie", "OVA", "web", "unknown", ... when tvdbid is not a (non-zero) number
	TVDBSeason    *string // defaulttvdbseason: a number, or "a" for absolute
	EpisodeOffset int
	TMDbTV        *int
	TMDbMovie     []int
	IMDb          []string
	Mappings      []ScudLeeMapping
	Before        *string
}

// ScudLeeMapping is one <mapping> with a TVDB season.
type ScudLeeMapping struct {
	AniDBSeason int
	TVDBSeason  int
	Start, End  *int
	Offset      *int
	Pairs       []ScudLeePair
}

// ScudLeePair maps one AniDB episode to TVDB episodes (0 = not on TVDB).
type ScudLeePair struct {
	Src int
	Dst []int
}

// FribbRec is one Fribb anime-list-full entry (the fields the pipeline uses).
type FribbRec struct {
	AniDBID   *int
	AniListID *int
	TMDbMovie []int
	HasTMDb   bool
}

// ManamiRec is a manami anime-offline-database record, reduced to the fields the pipeline uses.
type ManamiRec struct {
	AniDBID     *int
	AniListID   *int
	Title       *string
	Type        *string
	Episodes    *int
	Season      *ManamiSeason
	Synonyms    []string
	Sources     []string
	Related     []string
	DurationMin *int
}

// ManamiSeason is manami's animeSeason.
type ManamiSeason struct {
	Season *string `json:"season"`
	Year   *int    `json:"year"`
}

// ScudLeeGet looks an AniDB id up.
func (l *Lists) ScudLeeGet(adb int) *ScudLeeRec {
	return l.ScudLee[adb]
}

// FribbAniList looks an AniList id up.
func (l *Lists) FribbAniList(id int) *FribbRec {
	return l.FribbByAniList[id]
}

// FribbAniDB looks an AniDB id up.
func (l *Lists) FribbAniDB(id int) *FribbRec {
	return l.FribbByAniDB[id]
}

// Manami looks a record up by site ("anilist", "anidb", "kitsu") and id.
func (l *Lists) Manami(site string, id int) *ManamiRec {
	if l.manami == nil {
		return nil
	}
	return l.manami[manamiKey{site, id}]
}

// ParseScudLee parses anime-list-master.xml.
func (l *Lists) ParseScudLee(r io.Reader) error {
	var doc struct {
		Anime []struct {
			AniDBID       string  `xml:"anidbid,attr"`
			TVDBID        *string `xml:"tvdbid,attr"`
			DefaultSeason *string `xml:"defaulttvdbseason,attr"`
			EpisodeOffset string  `xml:"episodeoffset,attr"`
			TMDbTV        string  `xml:"tmdbtv,attr"`
			TMDbID        string  `xml:"tmdbid,attr"`
			IMDbID        string  `xml:"imdbid,attr"`
			Name          *string `xml:"name"`
			Before        *string `xml:"before"`
			Mappings      []struct {
				AniDBSeason string  `xml:"anidbseason,attr"`
				TVDBSeason  *string `xml:"tvdbseason,attr"`
				Start       string  `xml:"start,attr"`
				End         string  `xml:"end,attr"`
				Offset      string  `xml:"offset,attr"`
				Text        string  `xml:",chardata"`
			} `xml:"mapping-list>mapping"`
		} `xml:"anime"`
	}
	if err := xml.NewDecoder(r).Decode(&doc); err != nil {
		return fmt.Errorf("scudlee: %w", err)
	}
	l.ScudLee = map[int]*ScudLeeRec{}
	l.ScudLeeOrder = nil
	for _, a := range doc.Anime {
		adb, err := strconv.Atoi(strings.TrimSpace(a.AniDBID))
		if err != nil {
			return fmt.Errorf("scudlee: anidbid %q", a.AniDBID)
		}
		rec := &ScudLeeRec{AniDBID: adb, TVDBSeason: a.DefaultSeason}
		if a.Name != nil {
			rec.Name = strings.TrimSpace(*a.Name)
		}
		if a.TVDBID != nil {
			rec.TVDBID = pyNum(*a.TVDBID)
			if rec.TVDBID == nil || *rec.TVDBID == 0 {
				v := *a.TVDBID
				rec.TVDBKind = &v
			}
		}
		if n := pyNum(a.EpisodeOffset); n != nil {
			rec.EpisodeOffset = *n
		}
		rec.TMDbTV = pyNum(a.TMDbTV)
		for _, x := range strings.Split(a.TMDbID, ",") {
			if t := strings.TrimSpace(x); t != "" && allDigits(t) {
				n, _ := strconv.Atoi(t)
				rec.TMDbMovie = append(rec.TMDbMovie, n)
			}
		}
		for _, x := range strings.Split(a.IMDbID, ",") {
			if strings.HasPrefix(x, "tt") {
				rec.IMDb = append(rec.IMDb, x)
			}
		}
		if a.Before != nil {
			if b := strings.TrimSpace(*a.Before); b != "" {
				rec.Before = &b
			}
		}
		for _, m := range a.Mappings {
			if m.TVDBSeason == nil {
				continue // tmdb-only mapping
			}
			as, err1 := strconv.Atoi(m.AniDBSeason)
			ts, err2 := strconv.Atoi(*m.TVDBSeason)
			if err1 != nil || err2 != nil {
				continue
			}
			mp := ScudLeeMapping{AniDBSeason: as, TVDBSeason: ts, Start: pyNum(m.Start), End: pyNum(m.End), Offset: pyNum(m.Offset)}
			for _, part := range strings.Split(strings.Trim(strings.TrimSpace(m.Text), ";"), ";") {
				src, dst, ok := strings.Cut(part, "-")
				if !ok {
					continue
				}
				s, err := strconv.Atoi(src)
				if err != nil {
					continue
				}
				p := ScudLeePair{Src: s}
				for _, d := range strings.Split(dst, "+") {
					n, err := strconv.Atoi(d)
					if err != nil {
						continue
					}
					p.Dst = append(p.Dst, n)
				}
				mp.Pairs = append(mp.Pairs, p)
			}
			rec.Mappings = append(rec.Mappings, mp)
		}
		if _, dup := l.ScudLee[adb]; !dup {
			l.ScudLeeOrder = append(l.ScudLeeOrder, adb)
		}
		l.ScudLee[adb] = rec
	}
	return nil
}

// pyNum is v, trimmed, as an integer, or nil.
func pyNum(v string) *int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return nil
	}
	return &n
}

// ParseFribb parses anime-list-full.json.
func (l *Lists) ParseFribb(r io.Reader) error {
	var raw []struct {
		AniDBID   *int            `json:"anidb_id"`
		AniListID *int            `json:"anilist_id"`
		TMDb      json.RawMessage `json:"themoviedb_id"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return fmt.Errorf("fribb: %w", err)
	}
	l.FribbByAniList = map[int]*FribbRec{}
	l.FribbByAniDB = map[int]*FribbRec{}
	for _, x := range raw {
		f := &FribbRec{AniDBID: x.AniDBID, AniListID: x.AniListID}
		var t struct {
			Movie json.RawMessage `json:"movie"`
		}
		if len(x.TMDb) > 0 && json.Unmarshal(x.TMDb, &t) == nil && len(t.Movie) > 0 {
			var ids []int
			if json.Unmarshal(t.Movie, &ids) != nil {
				var one int
				if json.Unmarshal(t.Movie, &one) == nil {
					ids = []int{one}
				}
			}
			f.TMDbMovie = ids
		}
		if x.AniListID != nil && *x.AniListID != 0 {
			if _, ok := l.FribbByAniList[*x.AniListID]; !ok {
				l.FribbByAniList[*x.AniListID] = f
			}
		}
		if x.AniDBID != nil && *x.AniDBID != 0 {
			l.FribbByAniDB[*x.AniDBID] = f
		}
	}
	return nil
}

// ParseAnibridge parses anibridge-mappings v3 mappings.json, keeping the AniList-keyed records.
func (l *Lists) ParseAnibridge(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	l.Anibridge = map[string]map[string]map[string]string{}
	return decodeOrdered(b, func(k string, v json.RawMessage) error {
		if k == "$meta" {
			l.AnibridgeMeta = append(json.RawMessage(nil), v...)
			return nil
		}
		if !strings.HasPrefix(k, "anilist:") {
			return nil
		}
		var rec map[string]map[string]string
		if err := json.Unmarshal(v, &rec); err != nil {
			return fmt.Errorf("anibridge %s: %w", k, err)
		}
		l.Anibridge[k] = rec
		return nil
	})
}

// ParseManami parses anime-offline-database(-minified).json.
func (l *Lists) ParseManami(r io.Reader) error {
	var doc struct {
		Data []struct {
			Sources  []string      `json:"sources"`
			Title    *string       `json:"title"`
			Type     *string       `json:"type"`
			Episodes *int          `json:"episodes"`
			Season   *ManamiSeason `json:"animeSeason"`
			Synonyms []string      `json:"synonyms"`
			Related  []string      `json:"relatedAnime"`
			Duration *struct {
				Value *float64 `json:"value"`
			} `json:"duration"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return fmt.Errorf("manami: %w", err)
	}
	idx := map[manamiKey]*ManamiRec{}
	setdefault := func(k manamiKey, rec *ManamiRec) {
		if _, ok := idx[k]; !ok {
			idx[k] = rec
		}
	}
	for _, e := range doc.Data {
		rec := &ManamiRec{Title: e.Title, Type: e.Type, Episodes: e.Episodes, Season: e.Season,
			Synonyms: e.Synonyms, Sources: e.Sources, Related: e.Related}
		if rec.Synonyms == nil {
			rec.Synonyms = []string{}
		}
		for _, s := range e.Sources {
			if strings.HasPrefix(s, "https://anilist.co/anime/") {
				rec.AniListID = lastInt(s)
			} else if strings.HasPrefix(s, "https://anidb.net/anime/") {
				rec.AniDBID = lastInt(s)
			}
		}
		if e.Duration != nil && e.Duration.Value != nil {
			if m := int(math.RoundToEven(*e.Duration.Value / 60)); m != 0 {
				rec.DurationMin = &m
			}
		}
		if rec.AniListID != nil {
			setdefault(manamiKey{"anilist", *rec.AniListID}, rec)
		}
		if rec.AniDBID != nil {
			setdefault(manamiKey{"anidb", *rec.AniDBID}, rec)
		}
		for _, s := range rec.Sources {
			if strings.Contains(s, "kitsu.app/anime/") {
				if id := lastInt(s); id != nil {
					setdefault(manamiKey{"kitsu", *id}, rec)
				}
			}
		}
	}
	l.manami = idx
	return nil
}

func lastInt(s string) *int {
	i := strings.LastIndexByte(s, '/')
	return pyNum(s[i+1:])
}

// AnibridgeFor gives an AniList id's TVDB episodes and TMDb movie ids.
func (l *Lists) AnibridgeFor(anilistID int, nEps *int) *AnibridgeMap {
	rec := l.Anibridge["anilist:"+strconv.Itoa(anilistID)]
	type tri = [3]int
	seen := map[tri]bool{}
	var tvdb []tri
	var tmdb []int
	for target, rng := range rec {
		if strings.HasPrefix(target, "tmdb_movie:") {
			if n := pyNum(strings.SplitN(target, ":", 3)[1]); n != nil {
				tmdb = append(tmdb, *n)
			}
		}
		if !strings.HasPrefix(target, "tvdb_show:") {
			continue
		}
		parts := strings.Split(target, ":")
		if len(parts) != 3 || len(parts[2]) < 2 {
			continue
		}
		tid, err1 := strconv.Atoi(parts[1])
		season, err2 := strconv.Atoi(parts[2][1:])
		if err1 != nil || err2 != nil {
			continue
		}
		for src, dst := range rng {
			if d, ratio, ok := strings.Cut(dst, "|"); ok {
				if strings.TrimSpace(ratio) == "0" {
					continue
				}
				dst = d
			}
			a, b, dash := strings.Cut(src, "-")
			srcN := 1
			if dash {
				ai, _ := strconv.Atoi(a)
				end := ai
				if b != "" {
					end, _ = strconv.Atoi(b)
				} else if nEps != nil && *nEps != 0 {
					end = *nEps
				}
				srcN = end - ai + 1
			}
			for _, seg := range strings.Split(dst, ",") {
				x, y, d := strings.Cut(seg, "-")
				xi, err := strconv.Atoi(x)
				if err != nil {
					continue
				}
				yi := xi
				if d && y == "" {
					yi = xi + srcN - 1
				} else if y != "" {
					yi, _ = strconv.Atoi(y)
				}
				for e := xi; e <= yi; e++ {
					t := tri{tid, season, e}
					if !seen[t] {
						seen[t] = true
						tvdb = append(tvdb, t)
					}
				}
			}
		}
	}
	sortTriples(tvdb)
	if tvdb == nil {
		tvdb = [][3]int{}
	}
	return &AnibridgeMap{TVDB: tvdb, TMDbMovie: sortedUniqueInts(tmdb)}
}

func sortTriples(x [][3]int) {
	for i := 1; i < len(x); i++ {
		for j := i; j > 0 && lessTri(x[j], x[j-1]); j-- {
			x[j], x[j-1] = x[j-1], x[j]
		}
	}
}

func lessTri(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// decodeOrdered walks a JSON object's members in document order.
func decodeOrdered(b []byte, fn func(k string, v json.RawMessage) error) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("not a JSON object")
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return err
		}
		if err := fn(kt.(string), v); err != nil {
			return err
		}
	}
	return nil
}

// tvdb is the record's TVDB series id, 0 for none.
func (r *ScudLeeRec) tvdb() int {
	if r == nil || r.TVDBID == nil {
		return 0
	}
	return *r.TVDBID
}
