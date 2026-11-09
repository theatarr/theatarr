package policy

import "time"

// Lists schedules Radarr collection and import-list refreshes.
type Lists struct {
	// Interval is the time between refreshes. Default 24h, like Radarr.
	Interval Duration `yaml:"interval"`
}

const (
	DefaultListInterval = 24 * time.Hour
	MinListInterval     = 10 * time.Minute
)

// ListSettings is metadata.lists with defaults.
type ListSettings struct{ Interval time.Duration }

// ListSettings returns metadata.lists with its defaults.
func (p *Policy) ListSettings() ListSettings {
	s := ListSettings{Interval: DefaultListInterval}
	if p.Metadata != nil && p.Metadata.Lists != nil && p.Metadata.Lists.Interval > 0 {
		s.Interval = p.Metadata.Lists.Interval.D()
	}
	return s
}

func (p *Policy) validateLists(e *Errors) {
	if p.Metadata == nil || p.Metadata.Lists == nil {
		return
	}
	if d := p.Metadata.Lists.Interval.D(); d != 0 && d < MinListInterval {
		e.add("metadata.lists.interval", "%v is under the %v minimum", d, MinListInterval)
	}
}
