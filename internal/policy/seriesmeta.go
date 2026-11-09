package policy

import "time"

// SeriesMeta schedules the series metadata refresh (metadata.series, docs/design/tv-metadata.md): Theatarr's own episode list
// from metadata.sources.tvdb, TMDb (metadata.sources.tmdb) for series TVDB lacks.
type SeriesMeta struct {
	// Enabled turns the refresh on; default true when a tvdb or tmdb source is configured.
	Enabled *bool `yaml:"enabled"`
	// Interval is the time between due-series sweeps. Default 1h, minimum 10m. Which series are due follows Sonarr's
	// ShouldRefreshSeries (continuing every 12h, ended monthly).
	Interval Duration `yaml:"interval"`
	// PerRun caps the series one sweep refreshes. Default 50.
	PerRun int `yaml:"per_run"`
}

// Series metadata defaults and limits.
const (
	DefaultSeriesMetaInterval = time.Hour
	MinSeriesMetaInterval     = 10 * time.Minute
	DefaultSeriesMetaPerRun   = 50
	MaxSeriesMetaPerRun       = 2000
)

// SeriesMetaSettings is metadata.series with defaults.
type SeriesMetaSettings struct {
	Enabled  bool
	Interval time.Duration
	PerRun   int
}

// SeriesMetaSettings returns metadata.series with its defaults.
func (p *Policy) SeriesMetaSettings() SeriesMetaSettings {
	s := SeriesMetaSettings{Enabled: true, Interval: DefaultSeriesMetaInterval, PerRun: DefaultSeriesMetaPerRun}
	if p.Metadata == nil || p.Metadata.Series == nil {
		return s
	}
	m := p.Metadata.Series
	if m.Enabled != nil {
		s.Enabled = *m.Enabled
	}
	if m.Interval > 0 {
		s.Interval = m.Interval.D()
	}
	if m.PerRun > 0 {
		s.PerRun = m.PerRun
	}
	return s
}

func (p *Policy) validateSeriesMeta(e *Errors) {
	if p.Metadata == nil || p.Metadata.Series == nil {
		return
	}
	m := p.Metadata.Series
	if m.PerRun < 0 || m.PerRun > MaxSeriesMetaPerRun {
		e.add("metadata.series.per_run", "must be 1 to %d", MaxSeriesMetaPerRun)
	}
	if m.Interval != 0 && m.Interval.D() < MinSeriesMetaInterval {
		e.add("metadata.series.interval", "%v is under the %v minimum", m.Interval.D(), MinSeriesMetaInterval)
	}
	if m.Enabled != nil && *m.Enabled && p.Metadata.Sources["tvdb"] == nil && p.Metadata.Sources["tmdb"] == nil {
		e.add("metadata.series", "needs metadata.sources.tvdb or metadata.sources.tmdb")
	}
}

// ArrFed reports whether an arr instance still owns imported state.
func (p *Policy) ArrFed(instance string) bool {
	return instance != "" && p.Import != nil && p.Import.Arr[instance] != nil
}

// ArrFedInstances lists the owning import.arr instances.
func (p *Policy) ArrFedInstances() []string {
	if p.Import == nil {
		return nil
	}
	var out []string
	for _, k := range sortedKeys(p.Import.Arr) {
		if p.ArrFed(k) {
			out = append(out, k)
		}
	}
	return out
}
