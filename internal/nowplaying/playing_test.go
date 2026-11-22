package nowplaying

import (
	"slices"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/plex"
)

// Playing gives the put-back each session's link and target, names the titles of sessions whose file Plex did not
// say, and answers nothing while the last poll failed or is stale.
func TestPlayingForPutBack(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	tr := New(Options{Now: func() time.Time { return now }})
	tr.updatedAt = now.Add(-time.Minute)
	tr.sess["1"] = &session{s: plex.Session{SessionKey: "1"}, src: &Source{LinkID: 7, Target: "/m/a.mkv", TitleID: 3,
		certain: true}}
	tr.sess["2"] = &session{s: plex.Session{SessionKey: "2", Ambiguous: true}, src: &Source{LinkID: 8, Target: "/m/b.mkv",
		TitleID: 4, certain: true}}
	links, targets, unclear, ok := tr.Playing(2 * time.Minute)
	slices.Sort(links)
	if !ok || !slices.Equal(links, []int64{7, 8}) || len(targets) != 2 || !slices.Equal(unclear, []int64{4}) {
		t.Errorf("links %v targets %v unclear %v answered %v", links, targets, unclear, ok)
	}
	tr.err = "plex down"
	if _, _, _, ok := tr.Playing(2 * time.Minute); ok {
		t.Error("answered with a failed poll")
	}
	tr.err, tr.updatedAt = "", now.Add(-3*time.Minute)
	if _, _, _, ok := tr.Playing(2 * time.Minute); ok {
		t.Error("answered with a stale poll")
	}
}
