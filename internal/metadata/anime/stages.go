package anime

// Stages are the stored outputs of the pipeline's stages: each stage is a function with a stored output. They go into the database with the build (anime_stage), so a review can show what each stage decided.
type Stages struct {
	Crawl    *StageCrawl            `json:"crawl"`
	Joins    []StageJoin            `json:"joins"`
	Mappings []StageMapping         `json:"mappings"`
	Flags    map[string]StageFlags  `json:"flags"`
	Orders   map[string]StageCounts `json:"orders"`
}

// StageCrawl is stage 1: members with their kind and path, and the excluded neighbours.
type StageCrawl struct {
	Members  []StageMember `json:"members"`
	Excluded []Excluded    `json:"excluded"`
}

// StageMember is one crawled AniList member.
type StageMember struct {
	AniListID int     `json:"anilist_id"`
	Title     *string `json:"title"`
	Kind      string  `json:"kind"`
	How       string  `json:"how"`
}

// StageJoin is stage 2 for one entry: the ids it was joined to.
type StageJoin struct {
	Key       string   `json:"key"`
	AniDBID   *int     `json:"anidb_id"`
	KitsuID   *int     `json:"kitsu_id"`
	IDSources []string `json:"id_sources,omitempty"`
}

// StageMapping is stages 3-4 for one entry: the candidates, the decision and the mapping kept.
type StageMapping struct {
	Key      string          `json:"key"`
	Decision *MappingOutcome `json:"decision,omitempty"`
	Method   string          `json:"method,omitempty"`
	SeriesID int             `json:"series_id,omitempty"`
	Episodes []SE            `json:"episodes,omitempty"`
}

// StageFlags is stage 6 for one entry.
type StageFlags struct {
	Flags  []string `json:"flags"`
	Target string   `json:"target"`
}

// StageCounts summarises an order (stages 7-8).
type StageCounts struct {
	Rows         int `json:"rows"`
	MovesApplied int `json:"moves_applied,omitempty"`
	MovesFailed  int `json:"moves_failed,omitempty"`
}

func (s *Stages) recordCrawl(c *Crawl) {
	sc := &StageCrawl{Excluded: []Excluded{}}
	for _, id := range c.Members.keys {
		m := c.Members.m[id]
		k := c.Kind[id]
		if k == "" {
			k = "side"
		}
		sc.Members = append(sc.Members, StageMember{AniListID: id, Title: m.Title.Romaji, Kind: k, How: c.How[id]})
	}
	for _, id := range c.Excluded.keys {
		sc.Excluded = append(sc.Excluded, c.Excluded.m[id])
	}
	s.Crawl = sc
}

func (s *Stages) recordJoins(entries []*Entry) {
	for _, e := range entries {
		s.Joins = append(s.Joins, StageJoin{Key: e.Key, AniDBID: e.AniDBID, KitsuID: e.KitsuID, IDSources: e.IDSources})
	}
}

func (s *Stages) recordMappings(entries []*Entry) {
	s.Mappings = nil
	for _, e := range entries {
		m := StageMapping{Key: e.Key, Decision: e.Mapping}
		if e.TVDB != nil {
			m.Method, m.SeriesID, m.Episodes = e.TVDB.Method, e.TVDB.SeriesID, e.TVDB.Episodes
		}
		s.Mappings = append(s.Mappings, m)
	}
}

func (s *Stages) recordFlags(entries []*Entry) {
	s.Flags = map[string]StageFlags{}
	for _, e := range entries {
		s.Flags[e.Key] = StageFlags{Flags: e.Flags, Target: e.Target}
	}
}

func (s *Stages) recordOrders(r *Result) {
	s.Orders = map[string]StageCounts{
		"release": {Rows: len(r.ReleaseOrder)},
		"watch":   {Rows: len(r.WatchOrder)},
		"story":   {Rows: len(r.StoryOrder.Order), MovesApplied: r.StoryOrder.MovesApplied, MovesFailed: len(r.StoryOrder.MovesFailed)},
	}
}
