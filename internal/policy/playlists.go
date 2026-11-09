package policy

import (
	"strings"
	"time"
)

// Playlists configures the anime watch-order playlists (docs/design/anime.md): one admin-owned Plex video
// playlist per franchise and order kind, and one per route, built from the library's linked episodes and movies.
//
// Plex is written only when both library.enabled and playlists.enabled are true (both default false) and
// library.plex has a token; otherwise a sync is a dry run that records what it would create and calls nothing.
// The Plex server, its token and the section ids are the library's (library.plex, library.versions[k].plex_section).
type Playlists struct {
	// Enabled allows playlist writes in Plex, together with library.enabled. Default false.
	Enabled bool `yaml:"enabled"`
	// Orders lists the order kinds that get a playlist: release, watch, story. Default [watch, story].
	Orders []string `yaml:"orders"`
	// Routes gives each alternate route of the watch order (Fate's routes) its own playlist. Default true.
	Routes *bool `yaml:"routes"`
	// SkipOptional leaves out optional rows (recaps, side stories, shorts). Default false.
	SkipOptional bool `yaml:"skip_optional"`
	// Name is the playlist title; {franchise} and {order} are replaced. Default "{franchise} ({order})".
	Name string `yaml:"name"`
	// Franchises limits the sync to these slugs; empty: every built franchise.
	Franchises []string `yaml:"franchises"`
	// Interval is how often theatarr serve syncs. Default 6h, minimum 1h.
	Interval Duration `yaml:"interval"`
}

// Playlist defaults.
const (
	DefaultPlaylistName     = "{franchise} ({order})"
	DefaultPlaylistInterval = 6 * time.Hour
	MinPlaylistInterval     = time.Hour
)

// PlaylistSettings is the playlists section with defaults.
type PlaylistSettings struct {
	Enabled      bool
	Orders       []string
	Routes       bool
	SkipOptional bool
	Name         string
	Franchises   []string
	Interval     time.Duration
}

// PlaylistSettings returns the playlists section with defaults; a policy without one gets disabled playlists.
func (p *Policy) PlaylistSettings() PlaylistSettings {
	s := PlaylistSettings{Orders: []string{"watch", "story"}, Routes: true, Name: DefaultPlaylistName,
		Interval: DefaultPlaylistInterval}
	pl := p.Playlists
	if pl == nil {
		return s
	}
	s.Enabled, s.SkipOptional, s.Franchises = pl.Enabled, pl.SkipOptional, pl.Franchises
	if len(pl.Orders) > 0 {
		s.Orders = pl.Orders
	}
	if pl.Routes != nil {
		s.Routes = *pl.Routes
	}
	if pl.Name != "" {
		s.Name = pl.Name
	}
	if pl.Interval != 0 {
		s.Interval = pl.Interval.D()
	}
	return s
}

// PlaylistGate says whether playlist writes in Plex are allowed by the policy, and why not. The token is checked
// by the caller.
func (p *Policy) PlaylistGate() (bool, string) {
	switch {
	case p.Library == nil || !p.Library.Enabled:
		return false, "library.enabled is false"
	case p.Playlists == nil || !p.Playlists.Enabled:
		return false, "playlists.enabled is false"
	case p.Library.Plex == nil:
		return false, "library.plex is not set"
	}
	return true, ""
}

func (p *Policy) validatePlaylists(e *Errors) {
	pl := p.Playlists
	if pl == nil {
		return
	}
	seen := map[string]bool{}
	for _, o := range pl.Orders {
		if o != "release" && o != "watch" && o != "story" {
			e.add("playlists.orders", "%q: want release, watch or story", o)
		}
		if seen[o] {
			e.add("playlists.orders", "%q listed twice", o)
		}
		seen[o] = true
	}
	if pl.Name != "" && !strings.Contains(pl.Name, "{franchise}") {
		e.add("playlists.name", "must contain {franchise}")
	}
	if pl.Name != "" && len(pl.Orders) != 1 && !strings.Contains(pl.Name, "{order}") {
		e.add("playlists.name", "must contain {order} when more than one order gets a playlist")
	}
	if pl.Interval != 0 && pl.Interval.D() < MinPlaylistInterval {
		e.add("playlists.interval", "%v is under the %v minimum", pl.Interval.D(), MinPlaylistInterval)
	}
	if pl.Enabled && (p.Library == nil || p.Library.Plex == nil) {
		e.add("playlists.enabled", "needs library.plex (the server, token and section ids are the library's)")
	}
}
