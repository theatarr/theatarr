package provider

import "testing"

// TorBox's metaDL and checking resolve metadata and never give up on a dead hash, so they are queued (stalled_after
// applies), not downloading (https://notes.debridmediamanager.com/zurg/internals/torrent-lifecycle/).
func TestTorBoxMetadataStatesQueued(t *testing.T) {
	for _, tc := range []struct {
		state string
		size  int64
		want  State
	}{
		{"metaDL", -1, StateQueued},
		{"metaDL", 0, StateQueued},
		{"checking", -1, StateQueued},
		{"checking", 5 << 30, StateQueued},
		{"checkingDL", -1, StateQueued},
		{"checkingDL", 5 << 30, StateDownloading},
		{"processing", 5 << 30, StateDownloading},
		{"uploading", 5 << 30, StateDownloading},
		{"downloading", 5 << 30, StateDownloading},
		{"stalled (no seeds)", 5 << 30, StateQueued},
	} {
		if got := tbState(tc.state, false, false, tc.size); got != tc.want {
			t.Errorf("%s size %d: %v, want %v", tc.state, tc.size, got, tc.want)
		}
	}
}
