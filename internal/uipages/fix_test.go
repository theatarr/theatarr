package uipages

import "testing"

// A refused Plex token names the token field: the setting takes a replacement token, not a sign-in.
func TestFixPlexToken(t *testing.T) {
	if got := Fix(FixPlex, "", "http://plex:32400", FailAuth); got != "Plex refused the token: paste a new one under "+Plex {
		t.Errorf("got %q", got)
	}
}
