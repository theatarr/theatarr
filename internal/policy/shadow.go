package policy

import "time"

// Shadow tunes the RSS read in theatarr serve (docs/design/rss.md). The section keeps its historical name. Every field
// is optional; the section may be left out.
type Shadow struct {
	// RSSInterval is how often the shadow.rss job reads the RSS feeds of the Live libraries' indexers. Default 15m,
	// the arrs' default RSS sync interval; minimum 10m.
	RSSInterval Duration `yaml:"rss_interval"`
	// RSS turns the shadow.rss job on or off. Default on.
	RSS *bool `yaml:"rss"`
}

// RSS defaults and limits.
const (
	DefaultShadowRSSInterval = 15 * time.Minute
	MinShadowRSSInterval     = 10 * time.Minute
)

// ShadowSettings is the shadow section with every default applied.
type ShadowSettings struct {
	RSSInterval time.Duration
	RSS         bool
}

// ShadowSettings returns the shadow section with defaults.
func (p *Policy) ShadowSettings() ShadowSettings {
	s := ShadowSettings{RSSInterval: DefaultShadowRSSInterval, RSS: true}
	c := p.Shadow
	if c == nil {
		return s
	}
	if c.RSSInterval != 0 {
		s.RSSInterval = c.RSSInterval.D()
	}
	if c.RSS != nil {
		s.RSS = *c.RSS
	}
	return s
}

func (p *Policy) validateShadow(e *Errors) {
	c := p.Shadow
	if c == nil || c.RSSInterval == 0 {
		return
	}
	if c.RSSInterval.D() < MinShadowRSSInterval {
		e.add("shadow.rss_interval", "%v is under the %v minimum", c.RSSInterval.D(), MinShadowRSSInterval)
	}
}
