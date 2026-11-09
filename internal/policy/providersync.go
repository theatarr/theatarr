package policy

import "time"

// ProviderSync schedules the registry sync (provider.sync) in theatarr serve: every Interval it lists each provider whose
// key is set. It is its own top-level section because providers is a map of provider keys.
type ProviderSync struct {
	// Interval is the time between syncs. Default 6h, minimum 15m.
	Interval Duration `yaml:"interval"`
}

// Provider sync limits.
const (
	DefaultProviderSyncInterval = 6 * time.Hour
	MinProviderSyncInterval     = 15 * time.Minute
)

// ProviderSyncInterval is provider_sync.interval, or the default.
func (p *Policy) ProviderSyncInterval() time.Duration {
	if p.ProviderSync == nil || p.ProviderSync.Interval == 0 {
		return DefaultProviderSyncInterval
	}
	return p.ProviderSync.Interval.D()
}

func (p *Policy) validateProviderSync(e *Errors) {
	if p.ProviderSync == nil || p.ProviderSync.Interval == 0 {
		return
	}
	if d := p.ProviderSync.Interval.D(); d < MinProviderSyncInterval {
		e.add("provider_sync.interval", "%v is under the %v minimum", d, MinProviderSyncInterval)
	}
}
