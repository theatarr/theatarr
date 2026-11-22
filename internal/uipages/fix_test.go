package uipages

import "testing"

// A refused Plex token names the token field: the setting takes a replacement token, not a sign-in.
func TestFixPlexToken(t *testing.T) {
	if got := Fix(FixPlex, "", "http://plex:32400", FailAuth); got != "Plex refused the token: paste a new one under "+Plex {
		t.Errorf("got %q", got)
	}
}

// A provider is named by its kind; a key other than the kind's usual one follows, so two accounts stay apart.
func TestProviderName(t *testing.T) {
	for _, c := range []struct{ kind, key, want string }{
		{"realdebrid", "rd", "Real-Debrid"}, {"realdebrid", "rd2", "Real-Debrid (rd2)"}, {"", "tb", "TorBox"},
		{"alldebrid", "", "AllDebrid"},
	} {
		if got := ProviderName(c.kind, c.key); got != c.want {
			t.Errorf("%s/%s: %q, want %q", c.kind, c.key, got, c.want)
		}
	}
}
