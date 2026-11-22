package nowplaying

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/plex"
)

// MayPlay answers the put-back's one question about one replacement: a session playing it for certain, or a session
// of its title whose file Plex did not say, may be playing it; any other session (another title's, or a music stream
// with no Theatarr title) is not. Nothing is answered while the last poll failed or is stale.
func TestMayPlayForPutBack(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	tr := New(Options{Now: func() time.Time { return now }})
	tr.updatedAt = now.Add(-time.Minute)
	tr.sess["music"] = &session{s: plex.Session{SessionKey: "music", Type: "track"}}
	tr.sess["other"] = &session{s: plex.Session{SessionKey: "other", Ambiguous: true}, src: &Source{LinkID: 8,
		Target: "/m/b.mkv", TitleID: 4, certain: true}}
	if may, ok := tr.MayPlay(7, "/m/a.mkv", 3, 2*time.Minute); may || !ok {
		t.Errorf("unrelated sessions: may %v answered %v", may, ok)
	}
	tr.sess["same"] = &session{s: plex.Session{SessionKey: "same", Ambiguous: true}, src: &Source{LinkID: 9,
		Target: "/m/c.mkv", TitleID: 3, certain: true}}
	if may, _ := tr.MayPlay(7, "/m/a.mkv", 3, 2*time.Minute); !may {
		t.Error("a session of the title with its file unsaid")
	}
	delete(tr.sess, "same")
	tr.sess["it"] = &session{s: plex.Session{SessionKey: "it"}, src: &Source{LinkID: 7, Target: "/m/a.mkv", TitleID: 3,
		certain: true}}
	if may, _ := tr.MayPlay(7, "/m/a.mkv", 3, 2*time.Minute); !may {
		t.Error("the replacement itself")
	}
	tr.err = "plex down"
	if _, ok := tr.MayPlay(7, "/m/a.mkv", 3, 2*time.Minute); ok {
		t.Error("answered with a failed poll")
	}
	tr.err, tr.updatedAt = "", now.Add(-3*time.Minute)
	if _, ok := tr.MayPlay(7, "/m/a.mkv", 3, 2*time.Minute); ok {
		t.Error("answered with a stale poll")
	}
}
