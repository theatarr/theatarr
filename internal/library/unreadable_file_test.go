package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/retention"
)

// A link adopted more than the retention window ago, and never refreshed since, is still refreshed when
// the arr finally upgrades its file. Its provenance was only the "adopted from ..." library_action, which the
// nightly retention deletes.
func TestRetentionKeepsAdoptedLinksRefreshable(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	e.withScans()
	old := "Amelie.2001.1080p.WEB-DL-POKE"
	e.release(old, map[string]float64{"movie.mkv": 7000})
	arrDir := filepath.Join(e.movies, "Amelie The Movie (2001)")
	e.arrFile(1, "radarr", 10, 1, filepath.Join(arrDir, "Amelie.WEB.mkv"), old+"/movie.mkv", "WEBDL-1080p")
	if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"}); err != nil || rep.Linked != 1 {
		t.Fatalf("adopt %+v %v", rep, err)
	}
	e.exec(`DELETE FROM job`)

	// 200 days later the nightly retention job runs with its defaults.
	if sum, err := retention.Prune(e.ctx, e.st, e.now.Add(200*24*time.Hour), retention.Defaults); err != nil {
		t.Fatal(err)
	} else {
		t.Logf("retention pruned %v", sum)
	}

	upgraded := "Amelie.2001.2160p.Remux-BYNDR"
	e.release(upgraded, map[string]float64{"movie.mkv": 7000})
	e.arrUpgrade(1, old, 2, "radarr", 10, 1, filepath.Join(arrDir, "Amelie.Remux.mkv"), upgraded+"/movie.mkv", "Remux-2160p")

	rep, err := e.lib.Refresh(e.ctx, RefreshOptions{Version: "movie-theatarr"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Links != 1 || rep.Swapped != 1 {
		t.Fatalf("after retention the adopted link is no longer refreshed: links %d swapped %d (%+v)", rep.Links,
			rep.Swapped, rep)
	}
}

// One ready download whose file read fails with EIO (rclone's answer for a debrid link that cannot be
// unrestricted) while the mount answers is backed off on its own, visibly; the pass imports the downloads behind it.
// A mount that does not answer still pauses every import.
func TestOneUnreadableFileDoesNotStallImports(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	rel := "Show.S01.2160p.WEB-DL.DDP5.1.H.265-GRP"
	e.release(rel, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2700,
		"Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2650})
	e.download(2, "tv", 2, 20, rel, rel, "[201, 202]")
	e.exec(`UPDATE download SET info_hash = '1111111111111111111111111111111111111111' WHERE id = 2`)
	broken := filepath.Join(e.mount, "__all__", movieRelease, "movie.mkv")
	inner := e.lib.probe
	fail := true
	e.lib.probe = func(p string, n int64) (*probe.Result, error) {
		if p == broken && fail {
			return nil, &os.PathError{Op: "read", Path: p, Err: syscall.EIO}
		}
		return inner(p, n)
	}
	if _, err := e.lib.Run(e.ctx); err != nil {
		t.Fatalf("one unreadable file stopped the pass: %v", err)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 2`); got != "imported" {
		t.Fatalf("the healthy series download is %s: one broken movie file stalls every import", got)
	}
	if got := e.one(`SELECT library_import.state || ' ' || download.state FROM library_import JOIN download
		ON download.id = download_id WHERE download_id = 1`); got != "waiting_mount ready" {
		t.Fatalf("broken download %s, want waiting_mount ready", got)
	}
	if got := e.one(`SELECT last_error FROM library_import WHERE download_id = 1`); !strings.HasPrefix(got, UnreadablePrefix) {
		t.Fatalf("broken download's error %q", got)
	}
	// Backed off: the next pass leaves it; after the back-off it is tried again and imports once readable.
	fail = false
	if _, err := e.lib.Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "ready" {
		t.Fatalf("retried within the back-off: %s", got)
	}
	e.now = e.now.Add(unreadableBackoff)
	if _, err := e.lib.Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "imported" {
		t.Fatalf("after the back-off: %s", got)
	}
}

// The same EIO while the mount's release root does not answer stops the pass (mount down).
func TestUnreadableFileOnADownMountPausesImports(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	broken := filepath.Join(e.mount, "__all__", movieRelease, "movie.mkv")
	inner := e.lib.probe
	e.lib.probe = func(p string, n int64) (*probe.Result, error) {
		if p == broken {
			// The mount goes away under the read.
			_ = os.RemoveAll(filepath.Join(e.mount, "__all__"))
			return nil, &os.PathError{Op: "read", Path: p, Err: syscall.EIO}
		}
		return inner(p, n)
	}
	if _, err := e.lib.Run(e.ctx); !errors.Is(err, ErrMountDown) {
		t.Fatalf("pass: %v, want ErrMountDown", err)
	}
	if got := e.one(`SELECT COALESCE((SELECT last_error FROM library_import WHERE download_id = 1), '')`); strings.HasPrefix(got, UnreadablePrefix) {
		t.Fatalf("a down mount was taken for one unreadable file: %q", got)
	}
}

// eioDirFS fails the listing of one directory with EIO, as zurg does for a torrent it cannot read.
type eioDirFS struct {
	osFS
	dir string
}

func (f eioDirFS) ReadDir(name string) ([]os.DirEntry, error) {
	if name == f.dir {
		return nil, &os.PathError{Op: "readdirent", Path: name, Err: syscall.EIO}
	}
	return os.ReadDir(name)
}

// One release folder whose listing fails while the mount answers leaves the links into it
// alone; the sweep goes on and still notes the other link's miss.
func TestRelinkSweepSurvivesOneUnreadableFolder(t *testing.T) {
	t.Parallel()
	e, _, _ := deadEnv(t, true, policy.ModeActive)
	p := filepath.Join(e.movies, "Other (2002)", "Other.mkv")
	tgt := filepath.Join(e.mount, "__all__", "Other", "a.mkv")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(tgt, p); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, title_id, release_title, quality, state, created_at, updated_at)
		VALUES (2, ?, ?, 1, 'Other', 'WEBDL-1080p', 'active', 0, 0)`, p, tgt)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (2, 10)`)
	e.lib.mount.fs = eioDirFS{dir: filepath.Join(e.mount, "__all__", "Other")}
	if _, err := e.lib.Relink(e.ctx, "", false); err != nil {
		t.Fatalf("one unreadable folder stopped the sweep: %v", err)
	}
	if got := e.one(`SELECT COALESCE(missing_since, 'null') || ' ' || state FROM link WHERE id = 1`); strings.HasPrefix(got, "null") {
		t.Errorf("the missing link was not noted: %s", got)
	}
	if got := e.one(`SELECT COALESCE(missing_since, 'null') || ' ' || state FROM link WHERE id = 2`); got != "null active" {
		t.Errorf("the link into the unreadable folder: %s", got)
	}
}

// A sweep refused for the mount forgets the misses noted before it, so the first sweep after an outage
// only notes them again and cannot confirm a dead mark.
func TestRefusedSweepForgetsNotedMisses(t *testing.T) {
	t.Parallel()
	e, _, _ := deadEnv(t, true, policy.ModeActive)
	e.sweep()
	if got := e.one(`SELECT COALESCE(missing_since, 'null') FROM link WHERE id = 1`); got == "null" {
		t.Fatal("the first sweep noted nothing")
	}
	// zurg serves an empty __all__ for a while: the sweep is refused.
	all := filepath.Join(e.mount, "__all__")
	if err := os.Rename(all, all+".away"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(all, 0o755); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Minute)
	if _, err := e.lib.Relink(e.ctx, "", false); !errors.Is(err, ErrMountDown) {
		t.Fatalf("sweep of an empty listing: %v", err)
	}
	if err := os.Remove(all); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(all+".away", all); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(2 * deadConfirm)
	e.sweep()
	if got := e.one(`SELECT state FROM link WHERE id = 1`); got != "active" {
		t.Fatalf("the first sweep after the outage marked the link %s", got)
	}
}
