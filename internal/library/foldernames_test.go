package library

import (
	"slices"
	"testing"
)

// zurg's folder name is the torrent name with strings.TrimSuffix(".mp4") and then strings.TrimSuffix(".mkv"), case
// sensitive (https://notes.debridmediamanager.com/zurg/internals/naming/): "Movie.2019.mkv.mp4 becomes Movie.2019",
// "Movie.2019.MKV keeps its .MKV". That name comes first; the earlier candidates stay.
func TestReleaseFolderNamesZurgExactFirst(t *testing.T) {
	t.Parallel()
	const hash = "abcdef0123456789abcdef0123456789abcdef01"
	for _, tc := range []struct {
		name  string
		first string
		also  []string
	}{
		{"Movie.2019.mkv.mp4", "Movie.2019", []string{"Movie.2019 {abcdef}", "Movie.2019.mkv", "Movie.2019.mkv.mp4"}},
		{"Movie.2019.mkv", "Movie.2019", []string{"Movie.2019 {abcdef}", "Movie.2019.mkv"}},
		{"Movie.2019.mp4", "Movie.2019", []string{"Movie.2019.mp4"}},
		{"Movie.2019.MKV", "Movie.2019.MKV", []string{"Movie.2019"}},
		{"Movie.2019.mp4.mkv", "Movie.2019.mp4", []string{"Movie.2019", "Movie.2019.mp4.mkv"}},
		{"Movie 2019", "Movie 2019", []string{"Movie 2019 {abcdef}"}},
	} {
		got := ReleaseFolderNames(hash, tc.name)
		if len(got) == 0 || got[0] != tc.first {
			t.Errorf("%s: %q, want %q first", tc.name, got, tc.first)
		}
		for _, w := range tc.also {
			if !slices.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", tc.name, got, w)
			}
		}
	}
}
