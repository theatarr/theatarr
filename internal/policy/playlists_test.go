package policy

import "testing"

func TestPlaylistsPolicy(t *testing.T) {
	t.Parallel()
	p := &Policy{}
	if s := p.PlaylistSettings(); s.Enabled || len(s.Orders) != 2 || !s.Routes || s.Name != DefaultPlaylistName {
		t.Errorf("defaults %+v", s)
	}
	if ok, why := p.PlaylistGate(); ok || why != "library.enabled is false" {
		t.Errorf("gate %v %s", ok, why)
	}
	p.Library = &Library{Enabled: true, Plex: &Plex{URL: "http://plex:32400", TokenEnv: "T"}}
	if ok, why := p.PlaylistGate(); ok || why != "playlists.enabled is false" {
		t.Errorf("gate %v %s", ok, why)
	}
	p.Playlists = &Playlists{Enabled: true}
	if ok, _ := p.PlaylistGate(); !ok {
		t.Error("both enabled")
	}
	p.Library.Enabled = false
	if ok, _ := p.PlaylistGate(); ok {
		t.Error("playlists.enabled alone opened the gate")
	}
	for _, c := range []struct {
		pl   Playlists
		want bool
	}{
		{Playlists{Orders: []string{"watch"}}, true},
		{Playlists{Orders: []string{"chronological"}}, false},
		{Playlists{Orders: []string{"watch", "watch"}}, false},
		{Playlists{Name: "{order}"}, false},
		{Playlists{Name: "{franchise}"}, false},
		{Playlists{Name: "{franchise}", Orders: []string{"story"}}, true},
		{Playlists{Interval: Duration(60e9)}, false},
	} {
		e := &Errors{}
		p := &Policy{Playlists: &c.pl}
		p.validatePlaylists(e)
		if (e.err() == nil) != c.want {
			t.Errorf("%+v: %v", c.pl, e.err())
		}
	}
	e := &Errors{}
	(&Policy{Playlists: &Playlists{Enabled: true}}).validatePlaylists(e)
	if e.err() == nil {
		t.Error("enabled without library.plex")
	}
}
