package policy

import "time"

// AnimeBuild schedules the automatic anime franchise builds (metadata.anime, docs/design/anime.md "Automatic
// builds").
type AnimeBuild struct {
	// AutoBuild builds franchises for anime series without one and rebuilds old ones (default true). Building only
	// writes mappings; episodes whose mapping is not settled import through TVDB's numbering, and their conflicts
	// show in Admin › Reviews.
	AutoBuild *bool `yaml:"auto_build"`
	// RefreshInterval is how old a franchise's build may get before it is rebuilt. Default 168h (7 days), minimum
	// 24h. Rebuilds are spread over the interval; a franchise with a refresh held for review waits for the review.
	RefreshInterval Duration `yaml:"refresh_interval"`
}

// Anime build defaults and limits.
const (
	DefaultAnimeRefreshInterval = 7 * 24 * time.Hour
	MinAnimeRefreshInterval     = 24 * time.Hour
)

// AnimeBuildSettings is metadata.anime with defaults.
type AnimeBuildSettings struct {
	AutoBuild       bool
	RefreshInterval time.Duration
}

// AnimeBuildSettings returns metadata.anime with its defaults.
func (p *Policy) AnimeBuildSettings() AnimeBuildSettings {
	s := AnimeBuildSettings{AutoBuild: true, RefreshInterval: DefaultAnimeRefreshInterval}
	if p == nil || p.Metadata == nil || p.Metadata.Anime == nil {
		return s
	}
	a := p.Metadata.Anime
	if a.AutoBuild != nil {
		s.AutoBuild = *a.AutoBuild
	}
	if a.RefreshInterval > 0 {
		s.RefreshInterval = a.RefreshInterval.D()
	}
	return s
}

func (p *Policy) validateAnimeBuild(e *Errors) {
	if p.Metadata == nil || p.Metadata.Anime == nil {
		return
	}
	if d := p.Metadata.Anime.RefreshInterval.D(); d != 0 && d < MinAnimeRefreshInterval {
		e.add("metadata.anime.refresh_interval", "%v is under the %v minimum", d, MinAnimeRefreshInterval)
	}
}
