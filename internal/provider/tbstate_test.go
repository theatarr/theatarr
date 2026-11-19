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

// A downloaded state counts only once TorBox says the files are present; before that it is the finalising step
// (downloading). Explicit expiry and missing-file states stay errors.
func TestTorBoxDownloadedNeedsPresentFiles(t *testing.T) {
	for _, tc := range []struct {
		state             string
		finished, present bool
		want              State
	}{
		{"completed", true, false, StateDownloading},
		{"cached", false, false, StateDownloading},
		{"seeding", true, false, StateDownloading},
		{"uploading", true, false, StateDownloading},
		{"stalledUP", false, false, StateDownloading},
		{"completed", true, true, StateDownloaded},
		{"cached", false, true, StateDownloaded},
		{"seeding", false, true, StateDownloaded},
		{"", true, false, StateError},
		{"expired", true, false, StateError},
		{"missingFiles", true, false, StateError},
	} {
		if got := tbState(tc.state, tc.finished, tc.present, 5<<30); got != tc.want {
			t.Errorf("%q finished=%v present=%v: %v, want %v", tc.state, tc.finished, tc.present, got, tc.want)
		}
	}
}
