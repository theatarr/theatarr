package library

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/outcome"
)

// coveredEnv: brokenWhilePlaying's movie (old copy at quality old) replaced by an import of quality nw, at the same
// path (one row, a new target) or at another path (a new link; the dead old one moved aside, as apply does). The
// watched row is fixed and its outcome reported replaced. It returns the incident and the replacement's path.
type coveredEnv struct {
	*env
	id       int64
	oldPath  string
	oldTgt   string
	newPath  string
	newTgt   string
	mu       sync.Mutex
	outcomes []string
}

func newCoveredEnv(t *testing.T, old, nw string, samePath bool) *coveredEnv {
	t.Helper()
	e, id := brokenWhilePlaying(t)
	c := &coveredEnv{env: e, id: id, oldPath: filepath.Join(e.movies, "Movie (2001)", "Movie.mkv"),
		oldTgt: filepath.Join(e.mount, "__all__", "Name", "episode.mkv"),
		newTgt: filepath.Join(e.mount, "__all__", "Other", "a.mkv")}
	e.lib.OnPlayingOutcome(func(o outcome.Outcome) {
		c.mu.Lock()
		c.outcomes = append(c.outcomes, o.Kind)
		c.mu.Unlock()
	})
	e.exec(`UPDATE link SET quality = ? WHERE id = 1`, old)
	e.exec(`UPDATE playing_break SET quality = ?, fixed_by = 'replaced', new_release = 'Other', reported = 'replaced'
		WHERE id = ?`, old, id)
	if samePath {
		c.newPath = c.oldPath
		e.exec(`UPDATE link SET target = ?, state = 'active', release_title = 'Other', quality = ?, content_file_id = NULL,
			missing_since = NULL WHERE id = 1`, c.newTgt, nw)
		if err := os.Remove(c.oldPath); err != nil {
			t.Fatal(err)
		}
	} else {
		c.newPath = filepath.Join(e.movies, "Movie (2001)", "Movie New.mkv")
		e.exec(`INSERT INTO link (id, path, target, title_id, release_title, quality, state, created_at, updated_at)
			VALUES (2, ?, ?, 1, 'Other', ?, 'active', 0, 0)`, c.newPath, c.newTgt, nw)
		e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (2, 10)`)
		e.exec(`UPDATE link SET state = 'unlinked' WHERE id = 1`)
		if err := os.Remove(c.oldPath); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(c.newTgt, c.newPath); err != nil {
		t.Fatal(err)
	}
	return c
}

// twoReads runs the come-back on two hourly sweeps far enough apart.
func (c *coveredEnv) twoReads() {
	c.t.Helper()
	for range 2 {
		if err := c.lib.ComeBack(c.ctx); err != nil {
			c.t.Fatal(err)
		}
		c.now = c.now.Add(deadConfirm + time.Minute)
	}
}

func (c *coveredEnv) settled() string {
	c.t.Helper()
	return c.one(`SELECT settled FROM playing_break WHERE id = ?`, c.id)
}

func (c *coveredEnv) missLifted() bool {
	c.t.Helper()
	return c.one(`SELECT COUNT(*) FROM provider_cache WHERE source = 'router:playing'`) == "0"
}

func (c *coveredEnv) kinds() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.outcomes...)
}

// actions is how many library actions there are.
func (e *env) actions() int {
	e.t.Helper()
	var n int
	if err := e.st.Reader().QueryRow(`SELECT COUNT(*) FROM library_action`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// An old copy that reads again on two sweeps while an import replaced it is kept aside, whether it is better, equal
// or worse: nothing in the library changes, the release's miss is lifted (ordinary searches may take it again), and
// one relink action says so; History has no removed row and no outcome is reported.
func TestComeBackKeepsReplacedCopyAside(t *testing.T) {
	for _, samePath := range []bool{true, false} {
		for _, old := range []string{"Remux-2160p", "WEBDL-1080p"} {
			for _, nw := range []string{"WEBDL-1080p", "Remux-2160p"} {
				name := old + " over " + nw
				c := newCoveredEnv(t, old, nw, samePath)
				before := c.actions()
				c.twoReads()
				if c.settled() != "aside" || !c.missLifted() {
					t.Errorf("same path %v, %s: settled %q, miss lifted %v", samePath, name, c.settled(), c.missLifted())
				}
				if got, _ := os.Readlink(c.newPath); got != c.newTgt {
					t.Errorf("same path %v, %s: symlink %q", samePath, name, got)
				}
				if got := c.one(`SELECT state || ' ' || target FROM link WHERE path = ?`, c.newPath); got != "active "+c.newTgt {
					t.Errorf("same path %v, %s: replacement %s", samePath, name, got)
				}
				if n := c.actions() - before; n != 1 || c.one(`SELECT kind || ': ' || detail FROM library_action
					ORDER BY id DESC LIMIT 1`) != "relink: "+asideWords {
					t.Errorf("same path %v, %s: %d actions, last %s", samePath, name, n,
						c.one(`SELECT kind || ': ' || detail FROM library_action ORDER BY id DESC LIMIT 1`))
				}
				if got := c.one(`SELECT COUNT(*) FROM history WHERE event = 'removed'`); got != "0" {
					t.Errorf("same path %v, %s: %s removed rows", samePath, name, got)
				}
				if len(c.kinds()) != 0 {
					t.Errorf("same path %v, %s: outcomes %v", samePath, name, c.kinds())
				}
			}
		}
	}
}

// A replaced copy whose replacement was removed later (nothing holds its place now) is kept aside as well, its miss
// lifted, and not revived: the ordinary search may take the release again.
func TestComeBackReplacementGoneAside(t *testing.T) {
	c := newCoveredEnv(t, "Remux-2160p", "WEBDL-1080p", false)
	c.exec(`UPDATE link SET state = 'unlinked' WHERE id = 2`)
	if err := os.Remove(c.newPath); err != nil {
		t.Fatal(err)
	}
	c.twoReads()
	if c.settled() != "aside" || !c.missLifted() || c.one(`SELECT state FROM link WHERE id = 1`) != "unlinked" {
		t.Errorf("settled %q, miss lifted %v, old link %s", c.settled(), c.missLifted(),
			c.one(`SELECT state FROM link WHERE id = 1`))
	}
	if _, err := os.Lstat(c.oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the old copy was linked again: %v", err)
	}
}

// With Pause on, nothing is settled; the next sweep after it keeps the copy aside.
func TestComeBackAsideWaitsForPause(t *testing.T) {
	c := newCoveredEnv(t, "Remux-2160p", "WEBDL-1080p", false)
	c.pol.Router.Enabled = false
	c.twoReads()
	if c.settled() != "" {
		t.Errorf("paused: settled %q", c.settled())
	}
	c.pol.Router.Enabled = true
	if err := c.lib.ComeBack(c.ctx); err != nil {
		t.Fatal(err)
	}
	if c.settled() != "aside" {
		t.Errorf("after Pause: %q", c.settled())
	}
}

// A replaced row older than 30 days is settled aside without a read, in words that do not claim it works.
func TestComeBackOldReplacedRowAside(t *testing.T) {
	c := newCoveredEnv(t, "Remux-2160p", "WEBDL-1080p", false)
	c.now = c.now.Add(31 * 24 * time.Hour)
	if err := os.Truncate(c.oldTgt, 0); err != nil { // a read would fail
		t.Fatal(err)
	}
	if err := c.lib.ComeBack(c.ctx); err != nil {
		t.Fatal(err)
	}
	if c.settled() != "aside" || !c.missLifted() || c.one(`SELECT state FROM link WHERE id = 2`) != "active" {
		t.Errorf("settled %q, miss lifted %v", c.settled(), c.missLifted())
	}
	if got := c.one(`SELECT kind || ': ' || detail FROM library_action ORDER BY id DESC LIMIT 1`); got !=
		"relink: not checked again after 30 days; kept aside" {
		t.Errorf("action %s", got)
	}
}

// packEnv: playingEnv's series with an E01-E02 pack (link 9, hashB, quality WEBDL-1080p) broken while playing and
// marked dead, its miss on rd; link 1 (E01, WEBDL-1080p) stays active, link 2 (E02) is gone. It returns the incident.
func packEnv(t *testing.T) (*env, int64) {
	t.Helper()
	e := playingEnv(t)
	pack := filepath.Join(e.mount, "__all__", "Pack", "pack.mkv")
	if err := os.MkdirAll(filepath.Dir(pack), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pack, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(pack, 4<<20); err != nil {
		t.Fatal(err)
	}
	// Link 3 (E03) becomes the E01-E02 pack; link 1 (E01) is replaced by a better file; link 2 (E02) goes.
	e.exec(`UPDATE link SET state = 'unlinked' WHERE id = 2`)
	packPath := filepath.Join(e.shows, "Show (2020)", "Season 01", "pack.mkv")
	if err := os.Symlink(pack, packPath); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, first_seen_at, last_seen_at) VALUES (2, ?, 'Pack', 'Pack', 0, 0)`, hashB)
	e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (9, 2, 'pack.mkv', 1)`)
	e.exec(`INSERT INTO link (id, path, target, content_file_id, title_id, release_title, quality, state, created_at, updated_at)
		VALUES (9, ?, ?, 9, 2, 'Show.S01.Pack', 'WEBDL-1080p', 'active', 0, 0)`, packPath, pack)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (9, 20)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (9, 201), (9, 202)`)
	id, err := e.lib.OpenPlayingIncident(e.ctx, PlayingIncident{TitleID: 2, Version: "tv"}, PlayingFile{LinkID: 9, Offset: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: 9, Target: pack, Offset: 1 << 20}, false,
		[]string{"rd"}); err != nil || !ok {
		t.Fatalf("marked %v: %v", ok, err)
	}
	return e, id
}

// A pack file partly covered by others (old E01-E02, E01 has another file) is kept aside, not revived: its dead
// symlink goes to the trash with one relink action and no History removed row, the miss is lifted, and an hourly sweep
// after it revives nothing (no two files for E01).
func TestComeBackPartlyCoveredPackAside(t *testing.T) {
	e, id := packEnv(t)
	before := e.actions()
	for range 2 {
		if err := e.lib.ComeBack(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.now = e.now.Add(deadConfirm + time.Minute)
	}
	if got := e.one(`SELECT settled FROM playing_break WHERE id = ?`, id) + " " +
		e.one(`SELECT state FROM link WHERE id = 9`) + " " + e.one(`SELECT state FROM link WHERE id = 1`); got !=
		"aside unlinked active" {
		t.Errorf("got %s", got)
	}
	packPath := filepath.Join(e.shows, "Show (2020)", "Season 01", "pack.mkv")
	if _, err := os.Lstat(packPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the aside copy's symlink is still in the library: %v", err)
	}
	if got := e.one(`SELECT COUNT(*) FROM link_trash WHERE link_id = 9`); got != "1" {
		t.Errorf("trash rows %s", got)
	}
	if n := e.actions() - before; n != 1 {
		t.Errorf("%d actions", n)
	}
	if got := e.one(`SELECT kind FROM library_action ORDER BY id DESC LIMIT 1`); got != "relink" {
		t.Errorf("action %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM history WHERE event = 'removed'`) + " " +
		e.one(`SELECT COUNT(*) FROM provider_cache WHERE source = 'router:playing'`); got != "0 0" {
		t.Errorf("removed rows, misses: %s", got)
	}
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 9`) + " " + e.one(`SELECT COUNT(*) FROM link l JOIN link_episode le
		ON le.link_id = l.id WHERE l.state = 'active' AND le.episode_id = 201`); got != "unlinked 1" {
		t.Errorf("after a sweep: %s", got)
	}
}

// A real partial import: E01 imported over the broken E01-E02 pack moved the pack's link aside (E02 still missing, the
// row not fixed). The pack reads again: it is still looked at, and kept aside with its miss lifted.
func TestComeBackPartialImportPackAside(t *testing.T) {
	e, id := packEnv(t)
	packPath := e.one(`SELECT path FROM link WHERE id = 9`)
	if err := os.Remove(packPath); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE link SET state = 'unlinked' WHERE id = 9`)
	for range 2 {
		if err := e.lib.ComeBack(e.ctx); err != nil {
			t.Fatal(err)
		}
		e.now = e.now.Add(deadConfirm + time.Minute)
	}
	if got := e.one(`SELECT settled FROM playing_break WHERE id = ?`, id) + " " +
		e.one(`SELECT state FROM link WHERE id = 9`) + " " + e.one(`SELECT state FROM link WHERE id = 1`) + " " +
		e.one(`SELECT COUNT(*) FROM provider_cache WHERE source = 'router:playing'`); got != "aside unlinked active 0" {
		t.Errorf("got %s", got)
	}
}

// A replaced row older than 30 days waits while paused: nothing settled, its miss kept, and it is not read either;
// after Pause it is kept aside.
func TestComeBackOldReplacedRowWaitsForPause(t *testing.T) {
	c := newCoveredEnv(t, "Remux-2160p", "WEBDL-1080p", false)
	c.now = c.now.Add(31 * 24 * time.Hour)
	c.pol.Router.Enabled = false
	if err := c.lib.ComeBack(c.ctx); err != nil {
		t.Fatal(err)
	}
	if c.settled() != "" || c.missLifted() || c.one(`SELECT back_since IS NULL FROM playing_break WHERE id = ?`, c.id) != "1" {
		t.Fatalf("paused: settled %q, miss lifted %v, read %s", c.settled(), c.missLifted(),
			c.one(`SELECT back_since IS NOT NULL FROM playing_break WHERE id = ?`, c.id))
	}
	c.pol.Router.Enabled = true
	if err := c.lib.ComeBack(c.ctx); err != nil {
		t.Fatal(err)
	}
	if c.settled() != "aside" {
		t.Errorf("after Pause: %q", c.settled())
	}
}
