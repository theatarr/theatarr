package anime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Media is one AniList media record, with the fields AniListQuery asks for.
type Media struct {
	ID       int     `json:"id"`
	IDMal    *int    `json:"idMal"`
	Type     string  `json:"type"`
	Format   *string `json:"format"`
	Status   *string `json:"status"`
	Episodes *int    `json:"episodes"`
	Duration *int    `json:"duration"`
	Title    struct {
		Romaji  *string `json:"romaji"`
		English *string `json:"english"`
		Native  *string `json:"native"`
	} `json:"title"`
	Synonyms  []string  `json:"synonyms"`
	StartDate FuzzyDate `json:"startDate"`
	EndDate   FuzzyDate `json:"endDate"`
	Relations struct {
		Edges []MediaEdge `json:"edges"`
	} `json:"relations"`
}

// MediaEdge is one AniList relation.
type MediaEdge struct {
	RelationType string `json:"relationType"`
	Node         struct {
		ID     int     `json:"id"`
		Type   string  `json:"type"`
		Format *string `json:"format"`
		Title  struct {
			Romaji *string `json:"romaji"`
		} `json:"title"`
	} `json:"node"`
}

// namesOf is a media's names: romaji, English and synonyms.
func namesOf(m *Media) []string {
	out := nonEmpty(m.Title.Romaji, m.Title.English)
	for _, s := range m.Synonyms {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Relation is an AniList relation type and target id. It marshals as ["SEQUEL", 123].
type Relation struct {
	Type string
	ID   int
}

func (r Relation) MarshalJSON() ([]byte, error) { return json.Marshal([]any{r.Type, r.ID}) }

func (r *Relation) UnmarshalJSON(b []byte) error {
	var v []json.RawMessage
	if err := json.Unmarshal(b, &v); err != nil || len(v) != 2 {
		return fmt.Errorf("relation: %s", b)
	}
	if err := json.Unmarshal(v[0], &r.Type); err != nil {
		return err
	}
	return json.Unmarshal(v[1], &r.ID)
}

// Special is a ScudLee AniDB special mapped into TVDB: [anidb episode, tvdb season, tvdb episode].
type Special [3]int

// Entry is one work in a franchise: a season, cour, film, OVA or TVDB-only special. Its JSON
// form is the golden outputs' entry, key for key.
type Entry struct {
	Key              string     `json:"key"`
	Title            *string    `json:"title"`
	TitleEn          *string    `json:"title_en"`
	Names            []string   `json:"names"`
	AniListID        *int       `json:"anilist_id"`
	MalID            *int       `json:"mal_id"`
	AniDBID          *int       `json:"anidb_id"`
	KitsuID          *int       `json:"kitsu_id"`
	Format           *string    `json:"format"`
	Status           *string    `json:"status"`
	Start            *string    `json:"start"`
	End              *string    `json:"end"`
	StartApprox      bool       `json:"start_approx,omitempty"`
	Episodes         *int       `json:"episodes"`
	DurationMin      *int       `json:"duration_min"`
	AniListRelations []Relation `json:"anilist_relations"`
	FoundVia         *string    `json:"found_via"`
	Origin           string     `json:"origin"`
	Kind             string     `json:"kind,omitempty"` // line, branch, side, leaf; empty for TVDB-only entries
	TVDB             *TVDBMap   `json:"tvdb"`
	TMDbMovie        []int      `json:"tmdb_movie"`
	Flags            []string   `json:"flags"`
	Notes            []string   `json:"notes"`

	IDSources         []string        `json:"id_sources,omitempty"`
	ScudLee           *ScudLeeInfo    `json:"scudlee,omitempty"`
	Anibridge         *AnibridgeMap   `json:"anibridge,omitempty"`
	AnibridgeRejected *bool           `json:"anibridge_rejected,omitempty"`
	AnibridgeAgrees   *bool           `json:"anibridge_agrees,omitempty"`
	KitsuRole         *KitsuRole      `json:"kitsu_role,omitempty"`
	ParentKey         string          `json:"parent_key,omitempty"`
	SpecialOf         string          `json:"special_of,omitempty"`
	Target            string          `json:"target"`
	Mainline          bool            `json:"mainline"`
	TMDbCheck         []TMDbCheck     `json:"tmdb_check,omitempty"`
	Mapping           *MappingOutcome `json:"mapping_decision,omitempty"` // not in the golden outputs: see arbitrate.go

	splitInto      []string
	tmdbCandidates []int
}

func (e *Entry) hasFlag(f string) bool { return contains(e.Flags, f) }

func (e *Entry) note(format string, a ...any) { e.Notes = append(e.Notes, fmt.Sprintf(format, a...)) }

// clone is dict(e): a shallow copy whose lists are shared until replaced.
func (e *Entry) clone() *Entry {
	c := *e
	return &c
}

// ScudLeeInfo summarises the ScudLee record an entry was mapped through.
type ScudLeeInfo struct {
	TVDB     any     `json:"tvdb"` // the TVDB id, or ScudLee's kind ("movie", "OVA", ...) when it has none
	Season   *string `json:"season"`
	Offset   int     `json:"offset"`
	Mappings int     `json:"mappings"`
}

// AnibridgeMap is anibridge's AniList-keyed mapping: TVDB [series, season, episode] triples and TMDb movie ids.
type AnibridgeMap struct {
	TVDB      [][3]int `json:"tvdb"`
	TMDbMovie []int    `json:"tmdb_movie"`
}

// KitsuRole is how a Kitsu-only entry relates to a member.
type KitsuRole struct {
	Role string `json:"role"`
	Of   string `json:"of"`
}

// TMDbCheck is the check of one TMDb id against the movie lookup.
type TMDbCheck struct {
	TMDbID       int
	LookupFailed bool
	Title        *string
	Year         *int
	InCinemas    *string
	Collection   *string
	Warning      string
	Rejected     bool
}

func (c TMDbCheck) MarshalJSON() ([]byte, error) {
	o := newObj()
	o.add("tmdb_id", c.TMDbID)
	if c.LookupFailed {
		o.add("radarr_lookup", "failed")
		return o.bytes()
	}
	o.add("title", c.Title)
	o.add("year", c.Year)
	o.add("inCinemas", c.InCinemas)
	o.add("collection", c.Collection)
	if c.Warning != "" {
		o.add("warning", c.Warning)
	}
	if c.Rejected {
		o.add("rejected", true)
	}
	return o.bytes()
}

// Heuristic records how a heuristic mapping was found.
type Heuristic struct {
	MinTitleSim float64 `json:"min_title_sim"`
	Picked      int     `json:"picked"`
	Wanted      int     `json:"wanted"`
}

// TVDBMap is an entry's TVDB mapping ("tvdb" in the JSON).
type TVDBMap struct {
	SeriesID         int
	Episodes         []SE
	Method           string // scudlee, anibridge, heuristic, tvdb-only, override
	Heuristic        *Heuristic
	AniDBSpecials    []Special
	UnmappedAniDBEps []int

	// Filled by validation.
	SeriesTitle     string
	MissingOnTVDB   []SE
	MissingInSonarr any // []SE, or "series not in Sonarr"
	HasFirst        bool
	FirstAir        *string
	FirstTitle      *string
	Overview        string
	Airs            *Airs
	Range           string
	SpecialsRange   string

	validated bool
	sonarr    bool
	tvdbOnly  bool
}

// Airs is TVDB's placement of a special.
type Airs struct {
	AfterSeason   *int
	BeforeSeason  *int
	BeforeEpisode *int
}

func (a Airs) empty() bool {
	return a.AfterSeason == nil && a.BeforeSeason == nil && a.BeforeEpisode == nil
}

func (a Airs) MarshalJSON() ([]byte, error) {
	o := newObj()
	if a.AfterSeason != nil {
		o.add("airedAfterSeasonNumber", *a.AfterSeason)
	}
	if a.BeforeSeason != nil {
		o.add("airedBeforeSeasonNumber", *a.BeforeSeason)
	}
	if a.BeforeEpisode != nil {
		o.add("airedBeforeEpisodeNumber", *a.BeforeEpisode)
	}
	return o.bytes()
}

// copyMap is dict(tv): a shallow copy.
func (t *TVDBMap) copyMap() *TVDBMap {
	c := *t
	return &c
}

func (t *TVDBMap) MarshalJSON() ([]byte, error) {
	o := newObj()
	o.add("series_id", t.SeriesID)
	o.add("episodes", nonNilSE(t.Episodes))
	o.add("method", t.Method)
	if t.tvdbOnly {
		o.add("range", t.Range)
		o.add("series_title", t.SeriesTitle)
		o.add("tvdb_first_air", t.FirstAir)
		o.add("tvdb_first_title", t.FirstTitle)
		if t.Airs != nil {
			o.add("airs", t.Airs)
		} else {
			o.add("airs", map[string]int{})
		}
		return o.bytes()
	}
	if t.Heuristic != nil {
		o.add("heuristic", t.Heuristic)
	}
	o.add("anidb_specials", nonNilSpecials(t.AniDBSpecials))
	o.add("unmapped_anidb_eps", nonNilInts(t.UnmappedAniDBEps))
	if t.validated {
		o.add("series_title", t.SeriesTitle)
		o.add("missing_on_tvdb", nonNilSE(t.MissingOnTVDB))
		if t.sonarr {
			if l, ok := t.MissingInSonarr.([]SE); ok {
				o.add("missing_in_sonarr", nonNilSE(l))
			} else {
				o.add("missing_in_sonarr", t.MissingInSonarr)
			}
		}
		if t.HasFirst {
			o.add("tvdb_first_air", t.FirstAir)
			o.add("tvdb_first_title", t.FirstTitle)
			o.add("tvdb_overview", t.Overview)
			if t.Airs != nil {
				o.add("airs", t.Airs)
			}
		}
		o.add("range", t.Range)
		o.add("specials_range", t.SpecialsRange)
	}
	return o.bytes()
}

func nonNilSE(x []SE) []SE {
	if x == nil {
		return []SE{}
	}
	return x
}

func nonNilSpecials(x []Special) []Special {
	if x == nil {
		return []Special{}
	}
	return x
}

func nonNilInts(x []int) []int {
	if x == nil {
		return []int{}
	}
	return x
}

func nonNilStrings(x []string) []string {
	if x == nil {
		return []string{}
	}
	return x
}

// obj writes a JSON object with keys in insertion order.
type obj struct {
	buf bytes.Buffer
	err error
	n   int
}

func newObj() *obj {
	o := &obj{}
	o.buf.WriteByte('{')
	return o
}

func (o *obj) add(k string, v any) {
	if o.err != nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		o.err = err
		return
	}
	if o.n > 0 {
		o.buf.WriteByte(',')
	}
	o.n++
	o.buf.WriteString(strconv.Quote(k))
	o.buf.WriteByte(':')
	o.buf.Write(b)
}

func (o *obj) bytes() ([]byte, error) {
	if o.err != nil {
		return nil, o.err
	}
	o.buf.WriteByte('}')
	return o.buf.Bytes(), nil
}

// UnmarshalJSON reads the form MarshalJSON writes (a stored build).
func (t *TVDBMap) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	var v struct {
		SeriesID         int             `json:"series_id"`
		Episodes         []SE            `json:"episodes"`
		Method           string          `json:"method"`
		Heuristic        *Heuristic      `json:"heuristic"`
		AniDBSpecials    []Special       `json:"anidb_specials"`
		UnmappedAniDBEps []int           `json:"unmapped_anidb_eps"`
		SeriesTitle      string          `json:"series_title"`
		MissingOnTVDB    []SE            `json:"missing_on_tvdb"`
		MissingInSonarr  json.RawMessage `json:"missing_in_sonarr"`
		FirstAir         *string         `json:"tvdb_first_air"`
		FirstTitle       *string         `json:"tvdb_first_title"`
		Overview         string          `json:"tvdb_overview"`
		Airs             *Airs           `json:"airs"`
		Range            string          `json:"range"`
		SpecialsRange    string          `json:"specials_range"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*t = TVDBMap{SeriesID: v.SeriesID, Episodes: v.Episodes, Method: v.Method, Heuristic: v.Heuristic,
		AniDBSpecials: v.AniDBSpecials, UnmappedAniDBEps: v.UnmappedAniDBEps, SeriesTitle: v.SeriesTitle,
		MissingOnTVDB: v.MissingOnTVDB, FirstAir: v.FirstAir, FirstTitle: v.FirstTitle, Overview: v.Overview,
		Airs: v.Airs, Range: v.Range, SpecialsRange: v.SpecialsRange}
	t.tvdbOnly = v.Method == "tvdb-only"
	_, t.validated = raw["series_title"]
	_, t.HasFirst = raw["tvdb_first_air"]
	if m, ok := raw["missing_in_sonarr"]; ok {
		t.sonarr = true
		var l []SE
		if json.Unmarshal(m, &l) == nil {
			t.MissingInSonarr = nonNilSE(l)
		} else {
			var s string
			_ = json.Unmarshal(m, &s)
			t.MissingInSonarr = s
		}
	}
	return nil
}

// UnmarshalJSON reads TVDB's three placement fields.
func (a *Airs) UnmarshalJSON(b []byte) error {
	var v struct {
		AfterSeason   *int `json:"airedAfterSeasonNumber"`
		BeforeSeason  *int `json:"airedBeforeSeasonNumber"`
		BeforeEpisode *int `json:"airedBeforeEpisodeNumber"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*a = Airs{AfterSeason: v.AfterSeason, BeforeSeason: v.BeforeSeason, BeforeEpisode: v.BeforeEpisode}
	return nil
}

// UnmarshalJSON reads a stored check.
func (c *TMDbCheck) UnmarshalJSON(b []byte) error {
	var v struct {
		TMDbID     int     `json:"tmdb_id"`
		Lookup     string  `json:"radarr_lookup"`
		Title      *string `json:"title"`
		Year       *int    `json:"year"`
		InCinemas  *string `json:"inCinemas"`
		Collection *string `json:"collection"`
		Warning    string  `json:"warning"`
		Rejected   bool    `json:"rejected"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*c = TMDbCheck{TMDbID: v.TMDbID, LookupFailed: v.Lookup == "failed", Title: v.Title, Year: v.Year,
		InCinemas: v.InCinemas, Collection: v.Collection, Warning: v.Warning, Rejected: v.Rejected}
	return nil
}
