package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/rootprobe"
	"github.com/theatarr/theatarr/internal/rootprobe/probetest"
)

// hangRootRead makes the scan read *hook block on path until the test ends, like a hung share that still answered
// the gate's preflight, with a short root timeout; it returns the number of reads that blocked.
func hangRootRead[F any](t *testing.T, hook *F, wrap func(orig F, block func(p string)) F, path string) *atomic.Int64 {
	t.Helper()
	release := make(chan struct{})
	var hung atomic.Int64
	orig, oldTimeout := *hook, policy.RootResolveTimeout
	*hook = wrap(orig, func(p string) {
		if p == path {
			hung.Add(1)
			<-release
		}
	})
	policy.RootResolveTimeout = 100 * time.Millisecond
	t.Cleanup(func() {
		close(release)
		for deadline := time.Now().Add(5 * time.Second); !rootprobe.Default.Idle() && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		*hook, policy.RootResolveTimeout = orig, oldTimeout
	})
	return &hung
}

// The root passes Gate.Reachable, then a directory below it hangs on its listing. The sweep, Verify
// and the dangling summary skip the root ("unreachable") after one bounded wait, and at once while that read is
// stuck, instead of walking it unbounded; nothing is recorded for it.
func TestScanHungListingAfterPreflight(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Dir(plan.Links[0].Path)
	probetest.Mounts(t, e.movies, e.shows, e.trash, folder)
	if err := e.lib.gate.Reachable(e.ctx, e.movies); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	hung := hangRootRead(t, &rootReadDir, func(orig func(*os.File, int) ([]os.DirEntry, error), block func(string)) func(*os.File, int) ([]os.DirEntry, error) {
		return func(f *os.File, n int) ([]os.DirEntry, error) { block(f.Name()); return orig(f, n) }
	}, folder)
	movieScan := func(scans []*LinkScan) *LinkScan {
		for _, s := range scans {
			if s.Version == "movie-remux" {
				return s
			}
		}
		t.Fatalf("no movie scan in %+v", scans)
		return nil
	}
	for i := range 2 {
		scans, err := within(t, 3*time.Second, func() res2 { s, err := e.lib.Relink(e.ctx, "", false); return res2{s, err} }).get()
		if err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
		if sc := movieScan(scans); !strings.HasPrefix(sc.Skipped, "unreachable") || sc.Links != 0 || len(sc.Vanished) != 0 {
			t.Fatalf("sweep %d: %+v", i, sc)
		}
	}
	scans, err := within(t, 3*time.Second, func() res2 { s, err := e.lib.Verify(e.ctx, "movie-remux"); return res2{s, err} }).get()
	if err != nil || !strings.HasPrefix(movieScan(scans).Skipped, "unreachable") {
		t.Fatalf("verify %+v %v", scans, err)
	}
	if n := hung.Load(); n != 1 {
		t.Fatalf("%d listings blocked on the hung directory, want 1", n)
	}
	if got := e.one(`SELECT state FROM link`) + "/" + e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'dead'`); got != "active/0" {
		t.Fatalf("a hung root changed the link: %s", got)
	}
}

// ReconcileLinks' lstat of a link hangs after the root answered its checks: the root is skipped
// ("unreachable") after one bounded wait, and at once while the call is stuck, with nothing recorded.
func TestReconcileHungLstatAfterPreflight(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	path := plan.Links[0].Path
	probetest.Mounts(t, e.movies, e.shows, e.trash, filepath.Dir(path))
	hung := hangRootRead(t, &rootLstat, func(orig func(string) (os.FileInfo, error), block func(string)) func(string) (os.FileInfo, error) {
		return func(p string) (os.FileInfo, error) { block(p); return orig(p) }
	}, path)
	for i := range 2 {
		scans, err := within(t, 3*time.Second, func() res2 { s, err := e.lib.ReconcileLinks(e.ctx, "movie-remux", nil); return res2{s, err} }).get()
		if err != nil || len(scans) != 1 || !strings.HasPrefix(scans[0].Skipped, "unreachable") || len(scans[0].Vanished) != 0 {
			t.Fatalf("reconcile %d: %+v %v", i, scans, err)
		}
	}
	if n := hung.Load(); n != 1 {
		t.Fatalf("%d lstat calls blocked, want 1", n)
	}
	if got := e.one(`SELECT state FROM link`); got != "active" {
		t.Fatalf("link %s", got)
	}
}

// The root filter is in SQL now: a link of the version whose path only shares the root's name as a prefix (a
// sibling directory) is not the root's, even when it is missing.
func TestReconcileRootPrefixIsADirectory(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	for i, p := range []string{e.movies + " Extra/x.mkv", e.movies + "0/x.mkv", e.movies + "-other/x.mkv"} {
		e.exec(`INSERT INTO link (path, target, title_id, state, created_at, updated_at) SELECT ?, target || ?, title_id,
			'active', 0, 0 FROM link ORDER BY id LIMIT 1`, p, i)
		e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (last_insert_rowid(), 10)`)
	}
	scans, err := e.lib.ReconcileLinks(e.ctx, "movie-remux", nil)
	if err != nil || len(scans) != 1 || scans[0].Skipped != "" || len(scans[0].Vanished) != 0 {
		t.Fatalf("siblings reconciled: %+v %v", scans, err)
	}
}

type res2 struct {
	s   []*LinkScan
	err error
}

func (r res2) get() ([]*LinkScan, error) { return r.s, r.err }

// A directory on a slow share that keeps answering is listed whole: each chunk of the listing is
// bounded on its own, not the whole listing (nor the wait for the filesystem's slot).
func TestWalkSlowListingByChunks(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c", "d"} {
		if err := os.Symlink("/nowhere/"+n, filepath.Join(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	origRead, origChunk, oldTimeout := rootReadDir, rootReadChunk, policy.RootResolveTimeout
	rootReadDir = func(f *os.File, n int) ([]os.DirEntry, error) { time.Sleep(60 * time.Millisecond); return f.ReadDir(n) }
	rootReadChunk, policy.RootResolveTimeout = 1, 100*time.Millisecond
	t.Cleanup(func() { rootReadDir, rootReadChunk, policy.RootResolveTimeout = origRead, origChunk, oldTimeout })
	var got []string
	if err := WalkLinks(t.Context(), dir, func(p, target string) { got = append(got, filepath.Base(p)+">"+target) }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a>/nowhere/a,b>/nowhere/b,c>/nowhere/c,d>/nowhere/d" {
		t.Fatalf("walked %v", got)
	}
}

// WalkLinks (the torrent manager's scan roots) fails on a directory it cannot list, where the
// library's own walk skips it; and a listing that hangs ends it as ErrRootDown after one bounded wait.
func TestWalkLinksStrictAndBounded(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	origOpen := rootOpen
	rootOpen = func(p string) (*os.File, error) {
		if p == sub {
			return nil, os.ErrPermission
		}
		return origOpen(p)
	}
	walked := walkRoot(t.Context(), dir, func(rootEntry) {})
	strict := WalkLinks(t.Context(), dir, func(string, string) {})
	rootOpen = origOpen
	if walked != nil || !errors.Is(strict, os.ErrPermission) {
		t.Fatalf("walkRoot %v, WalkLinks %v", walked, strict)
	}
	probetest.Mounts(t, dir, sub)
	hangRootRead(t, &rootReadDir, func(orig func(*os.File, int) ([]os.DirEntry, error), block func(string)) func(*os.File, int) ([]os.DirEntry, error) {
		return func(f *os.File, n int) ([]os.DirEntry, error) { block(f.Name()); return orig(f, n) }
	}, sub)
	for i := range 2 {
		err := within(t, 3*time.Second, func() error { return WalkLinks(t.Context(), dir, func(string, string) {}) })
		if !errors.Is(err, ErrRootDown) {
			t.Fatalf("walk %d: %v", i, err)
		}
	}
}

// The trash purge's walk of an orphan day folder is bounded: a folder that hangs after the gate's
// check stops the purge (ErrRootDown, retried) after one bounded wait, with nothing purged.
func TestPurgeTrashHungListing(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	e.lib.set.TrashKeepDays = 7
	day := filepath.Join(e.trash, "movie-remux", "2026-10-01")
	orphan := filepath.Join(day, "Movie (2001)", "orphan.mkv")
	if err := os.MkdirAll(filepath.Dir(orphan), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(e.mount, "__all__", "x.mkv"), orphan); err != nil {
		t.Fatal(err)
	}
	probetest.Mounts(t, e.movies, e.shows, e.trash, day)
	hangRootRead(t, &rootReadDir, func(orig func(*os.File, int) ([]os.DirEntry, error), block func(string)) func(*os.File, int) ([]os.DirEntry, error) {
		return func(f *os.File, n int) ([]os.DirEntry, error) { block(f.Name()); return orig(f, n) }
	}, day)
	type res struct {
		n   int
		err error
	}
	r := within(t, 3*time.Second, func() res { n, err := e.lib.PurgeTrash(e.ctx); return res{n, err} })
	if !errors.Is(r.err, ErrRootDown) || r.n != 0 {
		t.Fatalf("purge %+v", r)
	}
	if _, err := os.Lstat(orphan); err != nil {
		t.Fatalf("orphan purged: %v", err)
	}
}

// Adopt reads the arr's link bounded: an lstat that hangs after the gate's check stops the adopt
// (ErrRootDown, retried) after one bounded wait, and at once while that call is stuck, with nothing linked.
func TestAdoptHungArrLink(t *testing.T) {
	e := newParallelEnv(t, true)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	arr := filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.mkv")
	e.arrFile(1, "radarr", 10, 1, arr, movieRelease+"/movie.mkv", "Remux-2160p")
	probetest.Mounts(t, e.movies, e.shows, e.trash, e.theatarrMovies, e.theatarrShows, filepath.Dir(arr))
	hung := hangRootRead(t, &rootLstat, func(orig func(string) (os.FileInfo, error), block func(string)) func(string) (os.FileInfo, error) {
		return func(p string) (os.FileInfo, error) { block(p); return orig(p) }
	}, arr)
	type res struct {
		rep *AdoptReport
		err error
	}
	for i := range 2 {
		r := within(t, 3*time.Second, func() res {
			rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"})
			return res{rep, err}
		})
		if !errors.Is(r.err, ErrRootDown) {
			t.Fatalf("adopt %d: %+v %v", i, r.rep, r.err)
		}
	}
	if n := e.one(`SELECT COUNT(*) FROM link`); n != "0" || hung.Load() != 1 {
		t.Fatalf("%s links recorded, %d lstat calls blocked", n, hung.Load())
	}
}
