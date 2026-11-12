package library

import (
	"context"
	"database/sql"
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

// hangLstat makes policy.ResolvePath's lstat of path block until the test ends, like a hung share, with a short
// root timeout; it returns the number of lstat calls that blocked.
func hangLstat(t *testing.T, path string) *atomic.Int64 {
	t.Helper()
	release := make(chan struct{})
	var hung atomic.Int64
	oldTimeout := policy.RootResolveTimeout
	policy.ResolveLstat, policy.RootResolveTimeout = func(p string) (os.FileInfo, error) {
		if p == path {
			hung.Add(1)
			<-release
		}
		return os.Lstat(p)
	}, 100*time.Millisecond
	t.Cleanup(func() {
		close(release)
		for deadline := time.Now().Add(5 * time.Second); !rootprobe.Default.Idle() && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		policy.ResolveLstat, policy.RootResolveTimeout = os.Lstat, oldTimeout
	})
	return &hung
}

// hangStat makes the root stat of path block until the test ends (policy.RootStat), with a short root timeout.
func hangStat(t *testing.T, path string) {
	t.Helper()
	release := make(chan struct{})
	oldTimeout := policy.RootResolveTimeout
	policy.RootStat, policy.RootResolveTimeout = func(p string) (os.FileInfo, error) {
		if p == path {
			<-release
		}
		return os.Stat(p)
	}, 100*time.Millisecond
	t.Cleanup(func() {
		close(release)
		for deadline := time.Now().Add(5 * time.Second); !rootprobe.Default.Idle() && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		policy.RootStat, policy.RootResolveTimeout = os.Stat, oldTimeout
	})
}

// within runs f and fails the test when it does not return in limit.
func within[T any](t *testing.T, limit time.Duration, f func() T) T {
	t.Helper()
	done := make(chan T, 1)
	go func() { done <- f() }()
	select {
	case v := <-done:
		return v
	case <-time.After(limit):
		t.Fatalf("still blocked after %s", limit)
	}
	var zero T
	return zero
}

// The root answers, but a directory below it is on a share that stops answering (a mount inside the
// root). A write there is refused ("unreachable") after one bounded wait, and at once while that call is stuck,
// instead of resolving the destination's parent unbounded with the caller's link locks held.
func TestGateHungDestinationRefusesWithoutHanging(t *testing.T) {
	dir := t.TempDir()
	mine, trash := filepath.Join(dir, "theatarr"), filepath.Join(dir, "trash")
	show := filepath.Join(mine, "Show")
	for _, d := range []string{show, trash} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	probetest.Mounts(t, mine, show, trash)
	hung := hangLstat(t, show)
	p := &policy.Policy{Versions: map[string]*policy.VersionPolicy{"K": {Mode: policy.ModeActive, Library: mine}},
		Library: &policy.Library{Enabled: true, Trash: policy.LibraryTrash{Dir: trash},
			Versions: map[string]*policy.LibraryVersion{"K": {Roots: []string{mine}}}}}
	g := NewGate(p)
	for i := range 3 {
		err := within(t, 3*time.Second, func() error {
			return g.CheckPath("K", filepath.Join(show, "Season 01", "ep.mkv"))
		})
		var ref *Refused
		if !errors.As(err, &ref) || ref.Reason != "unreachable" || !errors.Is(err, ErrRootDown) || !Unavailable(err) {
			t.Fatalf("check %d: %v", i, err)
		}
	}
	if n := hung.Load(); n != 1 {
		t.Fatalf("%d lstat calls on the hung directory, want 1", n)
	}
	// Elsewhere in the root, writes go on.
	if err := g.CheckPath("K", filepath.Join(mine, "Movie", "movie.mkv")); err != nil {
		t.Fatalf("a path beside the hung directory: %v", err)
	}
}

// The debrid mount's parent is on a share that stops answering. The mount's aliases are resolved
// through the root prober: a write is refused ("unreachable") at once instead of hanging, and MountPaths (settings
// validation) answers without the parent's alias instead of holding the save.
func TestGateHungMountParentRefusesWithoutHanging(t *testing.T) {
	dir := t.TempDir()
	mine, trash, share := filepath.Join(dir, "theatarr"), filepath.Join(dir, "trash"), filepath.Join(dir, "share")
	mount := filepath.Join(share, "zurg")
	for _, d := range []string{mine, trash, mount} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	probetest.Mounts(t, mine, trash, share)
	hangLstat(t, share)
	p := &policy.Policy{Versions: map[string]*policy.VersionPolicy{"K": {Mode: policy.ModeActive, Library: mine}},
		Library: &policy.Library{Enabled: true, Mount: policy.LibraryMount{Path: mount},
			Trash: policy.LibraryTrash{Dir: trash}, Versions: map[string]*policy.LibraryVersion{"K": {Roots: []string{mine}}}}}
	g := NewGate(p)
	for i := range 2 {
		err := within(t, 3*time.Second, func() error { return g.CheckPath("K", filepath.Join(mine, "m.mkv")) })
		if !errors.Is(err, ErrRootDown) || !strings.Contains(err.Error(), share) {
			t.Fatalf("check %d: %v", i, err)
		}
	}
	got := within(t, 3*time.Second, p.MountPaths)
	for _, m := range got {
		if m != mount {
			t.Fatalf("mount paths %v", got)
		}
	}
	if err := within(t, 3*time.Second, func() error { return p.Validate() }); err != nil &&
		strings.Contains(err.Error(), "not answering") {
		t.Fatalf("validation refused for the hung parent: %v", err)
	}
}

// An import whose version's own root does not answer is a pause, like a mount outage: no plan under
// the hung root (it would read it unbounded), no dry run recorded, and Run goes on with the other downloads.
func TestImportUnreachableRootPlansNothing(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	probetest.Mounts(t, e.movies, e.shows, e.trash)
	hangStat(t, e.movies)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	type res struct {
		plan *Plan
		err  error
	}
	r := within(t, 3*time.Second, func() res { p, err := e.lib.Import(e.ctx, 1, false); return res{p, err} })
	if !errors.Is(r.err, ErrRootDown) || errors.Is(r.err, ErrMountDown) || r.plan != nil {
		t.Fatalf("import %+v %v", r.plan, r.err)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_import WHERE download_id = 1`); got != "0" {
		t.Fatalf("library_import rows %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action`); got != "0" {
		t.Fatalf("actions recorded %s", got)
	}
	// A dry run (the API's plan) refuses the same way instead of planning under the root.
	r = within(t, 3*time.Second, func() res { p, err := e.lib.Import(e.ctx, 1, true); return res{p, err} })
	if !errors.Is(r.err, ErrRootDown) {
		t.Fatalf("dry run %+v %v", r.plan, r.err)
	}
	if err := within(t, 3*time.Second, func() error { _, err := e.lib.Run(e.ctx); return err }); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "ready" {
		t.Fatalf("download state %s", got)
	}
}

// A shadow version (refused by the gate before its roots are checked) is planned as a dry run, but
// not under a root that does not answer.
func TestImportShadowUnreachableRootPlansNothing(t *testing.T) {
	e := newEnv(t, true, policy.ModeShadow)
	probetest.Mounts(t, e.movies, e.shows, e.trash)
	hangStat(t, e.movies)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	err := within(t, 3*time.Second, func() error { _, err := e.lib.Import(e.ctx, 1, false); return err })
	if !errors.Is(err, ErrRootDown) {
		t.Fatalf("import: %v", err)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_import WHERE download_id = 1`); got != "0" {
		t.Fatalf("library_import rows %s", got)
	}
}

// Reconcile, relink, verify and adopt skip a root that does not answer instead of walking it.
func TestScansSkipUnreachableRoot(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	probetest.Mounts(t, e.movies, e.shows, e.trash)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	hangStat(t, e.movies)
	scans := within(t, 3*time.Second, func() []*LinkScan {
		out, err := e.lib.ReconcileLinks(e.ctx, "movie-remux", nil)
		if err != nil {
			t.Errorf("reconcile: %v", err)
		}
		return out
	})
	if len(scans) != 1 || !strings.HasPrefix(scans[0].Skipped, "unreachable: ") {
		t.Fatalf("reconcile %+v", scans)
	}
	for _, dry := range []bool{false, true} {
		scans = within(t, 3*time.Second, func() []*LinkScan {
			out, err := e.lib.Relink(e.ctx, "movie-remux", dry)
			if err != nil {
				t.Errorf("relink: %v", err)
			}
			return out
		})
		if len(scans) != 1 || !strings.HasPrefix(scans[0].Skipped, "unreachable: ") || scans[0].Refused != "" {
			t.Fatalf("relink dry=%v %+v", dry, scans)
		}
	}
	scans = within(t, 3*time.Second, func() []*LinkScan {
		out, err := e.lib.Verify(e.ctx, "movie-remux")
		if err != nil {
			t.Errorf("verify: %v", err)
		}
		return out
	})
	if len(scans) != 1 || !strings.HasPrefix(scans[0].Skipped, "unreachable: ") {
		t.Fatalf("verify %+v", scans)
	}
	// The other version is paused too, as its writes are (the gate cannot check its root against the hung one), and
	// records nothing: no dry runs while the share is away.
	scans, err := e.lib.Relink(e.ctx, "tv", false)
	if err != nil || len(scans) != 1 || !strings.Contains(scans[0].Skipped, e.movies) || scans[0].DryRun {
		t.Fatalf("tv relink %+v %v", scans, err)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action`); got != "0" {
		t.Fatalf("actions recorded %s", got)
	}
}

// An adopt whose gate finds a root that does not answer stops (Unavailable), and the import retries it rather than
// failing for good.
func TestAdoptUnreachableRoot(t *testing.T) {
	e := newParallelEnv(t, true)
	probetest.Mounts(t, e.movies, e.shows, e.trash, e.theatarrMovies, e.theatarrShows)
	hangStat(t, e.movies)
	err := within(t, 3*time.Second, func() error {
		_, err := e.adopt("movie-theatarr")
		return err
	})
	if !Unavailable(err) {
		t.Fatalf("adopt: %v", err)
	}
}

// The library imported the release itself and its symlink went missing; adopt names the instance's file
// of the same release to the same path. The row an import made is never taken over.
func TestAdoptLeavesOwnImportRow(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-theatarr", 1, 11, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil || len(plan.Links) != 1 {
		t.Fatalf("import %+v %v", plan, err)
	}
	own := plan.Links[0].Path
	arrLink := filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.mkv")
	e.arrFile(1, "radarr", 10, 1, arrLink, movieRelease+"/movie.mkv", "Remux-2160p")
	if err := os.Remove(own); err != nil {
		t.Fatal(err)
	}
	rep, err := e.adopt("movie-theatarr")
	if err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT COALESCE(download_id, 0) || ' ' || COALESCE(adopted_from, '-') FROM link WHERE path = ?`, own); got != "1 -" {
		t.Fatalf("the library's own link row was taken over by adopt: download_id/adopted_from = %s (report %+v)", got, rep)
	}
	if rep.Linked != 0 || rep.Skipped[AdoptConflict] != 1 {
		t.Fatalf("report %+v", rep)
	}
	if _, err := os.Lstat(own); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("adopt made a symlink at the imported path: %v", err)
	}
}

// A rename reads the old symlink before its commit, never inside the write transaction (a readlink
// on a share that stops answering would hold the database's writer).
func TestRenameReadsLinkOutsideWriteTx(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil || len(plan.Links) != 1 {
		t.Fatalf("import %+v %v", plan, err)
	}
	old := plan.Links[0].Path
	id, _ := sqlInt(e.one(`SELECT id FROM link WHERE path = ?`, old))
	var inTx atomic.Int64
	readLink = func(p string) (string, error) {
		if p == old {
			ctx, cancel := context.WithTimeout(e.ctx, 300*time.Millisecond)
			defer cancel()
			if err := e.st.WriteTx(ctx, func(*sql.Tx) error { return nil }); err != nil {
				inTx.Add(1)
			}
		}
		return os.Readlink(p)
	}
	t.Cleanup(func() { readLink = os.Readlink })
	newPath := filepath.Join(filepath.Dir(old), "renamed.mkv")
	if err := e.lib.RenameLink(e.ctx, id, newPath); err != nil {
		t.Fatal(err)
	}
	if n := inTx.Load(); n != 0 {
		t.Fatalf("%d readlink(s) of the old symlink ran inside the write transaction", n)
	}
	if got := e.one(`SELECT path FROM link WHERE id = ?`, id); got != newPath {
		t.Fatalf("path %s", got)
	}
}

func sqlInt(s string) (int64, error) {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("not a number: " + s)
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

// A client's path whose way into the mount hangs (the helper killed at the deadline) is the mount not
// answering (ErrMountDown), counted by the guard like any hung call, never "not on the debrid mount".
func TestLocateHungPathIsMountDown(t *testing.T) {
	m := helperMount(t, 300*time.Millisecond)
	p := filepath.Join(t.TempDir(), "HANG", "Rel.mkv")
	for i := range 3 {
		if _, err := m.Locate(context.Background(), p); !errors.Is(err, ErrMountDown) || errors.Is(err, ErrNotOnMount) {
			t.Fatalf("locate %d of a hung path: %v, want ErrMountDown", i, err)
		}
	}
	if b := m.Breaker(); b.State != BreakerOpen {
		t.Fatalf("three hung calls in a row left the circuit %s (consecutive %d)", b.State, b.Consecutive)
	}
	// A path that is not there, or not a symlink, is still not on the mount.
	m2 := helperMount(t, 2*time.Second)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.mkv"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(dir, "missing", "x.mkv"), filepath.Join(dir, "file.mkv"),
		filepath.Join(dir, "file.mkv", "below")} {
		if _, err := m2.Locate(context.Background(), p); !errors.Is(err, ErrNotOnMount) {
			t.Fatalf("locate %s: %v, want ErrNotOnMount", p, err)
		}
	}
}

// Zurg restarts while a sweep walks the roots; its half-loaded listing must not confirm a miss
// noted before. The mount is checked again before the sweep writes: down now, the sweep stops and forgets the misses.
func TestRelinkRechecksMountBeforeConfirmingMiss(t *testing.T) {
	t.Parallel()
	e, _, _ := deadEnv(t, true, policy.ModeActive)
	e.sweep() // the miss is noted
	e.now = e.now.Add(deadConfirm + time.Minute)
	all := filepath.Join(e.mount, "__all__")
	e.lib.beforeSweepWrite = func() {
		e.lib.beforeSweepWrite = nil
		// zurg restarted: its release root lists nothing yet.
		if err := os.Rename(all, all+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(all, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.lib.Relink(e.ctx, "", false); !errors.Is(err, ErrMountDown) {
		t.Fatalf("sweep across the restart: %v, want ErrMountDown", err)
	}
	if got := e.one(`SELECT state || ' ' || COALESCE(missing_since, 'null') FROM link WHERE id = 1`); got != "active null" {
		t.Fatalf("link %s, want active with the miss forgotten", got)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 10`); got != "filled" {
		t.Fatalf("version %s", got)
	}
}

// The root answers, but a share mounted below it does not; a walk of the root
// would cross into it, so the scan skips the root instead of blocking.
func TestScanSkipsRootWithHungNestedMount(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
	nested := filepath.Join(e.movies, "4k")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	probetest.Mounts(t, e.movies, e.shows, e.trash, nested)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	hangStat(t, nested)
	scans := within(t, 3*time.Second, func() []*LinkScan {
		out, err := e.lib.Relink(e.ctx, "movie-remux", false)
		if err != nil {
			t.Errorf("relink: %v", err)
		}
		return out
	})
	if len(scans) != 1 || !strings.Contains(scans[0].Skipped, nested) {
		t.Fatalf("relink %+v", scans)
	}
}
