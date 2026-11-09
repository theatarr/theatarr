package policy

import "time"

// Shadow tunes the live shadow runner in theatarr serve (docs/design/shadow.md): the periodic arr import that keeps the arr
// view fresh, the re-scoring of every new arr grab with a search of our own, and the RSS read at the lowest
// priority. Every field is optional; the section may be left out.
type Shadow struct {
	// ImportInterval is how often the arr.import job reads the import.arr instances (incremental). Default 15m,
	// minimum 5m.
	ImportInterval Duration `yaml:"import_interval"`
	// RSSInterval is how often the shadow.rss job reads the RSS feeds of the shadow and active versions' indexers
	// (it is also production RSS for active versions). Default 15m, the arrs' default RSS sync interval; minimum 10m.
	RSSInterval Duration `yaml:"rss_interval"`
	// RSS turns the shadow.rss job on or off. Default on.
	RSS *bool `yaml:"rss"`
	// Search turns our own search per arr grab on or off. Off, each grab is only decided. Default on.
	Search *bool `yaml:"search"`
	// SearchesPerHour caps our shadow searches (one per title, every indexer of its versions) per clock hour. A grab
	// over the cap waits for the next hour. Default 20, 1 to 200.
	SearchesPerHour int `yaml:"searches_per_hour"`
	// SearchReuse is how long one title's search results serve later grabs of the same title (the other version's
	// arr grabs the same movie). Default 6h, at most 24h.
	SearchReuse Duration `yaml:"search_reuse"`
	// KeepResults is how many decided search results are stored per grab (best first), besides the arr's release.
	// Default 10, 1 to 50.
	KeepResults int `yaml:"keep_results"`
	// LinkWindow is how long an RSS pick waits for an arr grab of the same version before it counts as "we'd grab,
	// arr didn't". Default 24h, 1h to 7 days.
	LinkWindow Duration `yaml:"link_window"`
}

// Shadow runner defaults and limits.
const (
	DefaultShadowImportInterval = 15 * time.Minute
	MinShadowImportInterval     = 5 * time.Minute
	DefaultShadowRSSInterval    = 15 * time.Minute
	MinShadowRSSInterval        = 10 * time.Minute
	DefaultShadowSearchesHour   = 20
	MaxShadowSearchesHour       = 200
	DefaultShadowSearchReuse    = 6 * time.Hour
	MaxShadowSearchReuse        = 24 * time.Hour
	DefaultShadowKeepResults    = 10
	MaxShadowKeepResults        = 50
	DefaultShadowLinkWindow     = 24 * time.Hour
	MinShadowLinkWindow         = time.Hour
	MaxShadowLinkWindow         = 7 * 24 * time.Hour
)

// ShadowSettings is the shadow section with every default applied.
type ShadowSettings struct {
	ImportInterval  time.Duration
	RSSInterval     time.Duration
	RSS             bool
	Search          bool
	SearchesPerHour int
	SearchReuse     time.Duration
	KeepResults     int
	LinkWindow      time.Duration
}

// ShadowSettings returns the shadow section with defaults.
func (p *Policy) ShadowSettings() ShadowSettings {
	s := ShadowSettings{ImportInterval: DefaultShadowImportInterval, RSSInterval: DefaultShadowRSSInterval, RSS: true,
		Search: true, SearchesPerHour: DefaultShadowSearchesHour, SearchReuse: DefaultShadowSearchReuse,
		KeepResults: DefaultShadowKeepResults, LinkWindow: DefaultShadowLinkWindow}
	c := p.Shadow
	if c == nil {
		return s
	}
	if c.ImportInterval != 0 {
		s.ImportInterval = c.ImportInterval.D()
	}
	if c.RSSInterval != 0 {
		s.RSSInterval = c.RSSInterval.D()
	}
	if c.RSS != nil {
		s.RSS = *c.RSS
	}
	if c.Search != nil {
		s.Search = *c.Search
	}
	if c.SearchesPerHour != 0 {
		s.SearchesPerHour = c.SearchesPerHour
	}
	if c.SearchReuse != 0 {
		s.SearchReuse = c.SearchReuse.D()
	}
	if c.KeepResults != 0 {
		s.KeepResults = c.KeepResults
	}
	if c.LinkWindow != 0 {
		s.LinkWindow = c.LinkWindow.D()
	}
	return s
}

func (p *Policy) validateShadow(e *Errors) {
	c := p.Shadow
	if c == nil {
		return
	}
	dur := func(path string, d Duration, lo, hi time.Duration) {
		if d == 0 {
			return
		}
		if d.D() < lo {
			e.add(path, "%v is under the %v minimum", d.D(), lo)
		} else if hi > 0 && d.D() > hi {
			e.add(path, "%v is over the %v maximum", d.D(), hi)
		}
	}
	dur("shadow.import_interval", c.ImportInterval, MinShadowImportInterval, 0)
	dur("shadow.rss_interval", c.RSSInterval, MinShadowRSSInterval, 0)
	dur("shadow.search_reuse", c.SearchReuse, time.Minute, MaxShadowSearchReuse)
	dur("shadow.link_window", c.LinkWindow, MinShadowLinkWindow, MaxShadowLinkWindow)
	num := func(path string, n, hi int) {
		if n < 0 || n > hi {
			e.add(path, "must be 1 to %d, got %d", hi, n)
		}
	}
	num("shadow.searches_per_hour", c.SearchesPerHour, MaxShadowSearchesHour)
	num("shadow.keep_results", c.KeepResults, MaxShadowKeepResults)
}
